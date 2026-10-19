package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"hizli-indir/engine"
)

const (
	maxFrame = 1 << 20  // Chrome native messaging sınırı: host → Chrome yönü
	maxIn    = 64 << 20 // Chrome → host yönü (Chrome 4 GB'a kadar iletir); büyük toplu eklemeler 1 MB'ı aşar
)

var errFrameTooBig = errors.New("ileti çok büyük")

func readFrame(r io.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	l := binary.LittleEndian.Uint32(n[:])
	if l > maxIn {
		return nil, fmt.Errorf("%w: %d bayt", errFrameTooBig, l)
	}
	buf := make([]byte, l)
	_, err := io.ReadFull(r, buf)
	return buf, err
}

func writeFrame(w io.Writer, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(b) > maxFrame {
		return errors.New("yanıt çok büyük")
	}
	var n [4]byte
	binary.LittleEndian.PutUint32(n[:], uint32(len(b)))
	_, err = w.Write(append(n[:], b...))
	return err
}

// daemon, servisin soket istemcisidir (native-host ve `add` kullanır).
type daemon struct {
	c     net.Conn
	r     *bufio.Reader
	next  int
	until time.Time // sıfır değilse her çağrının son anı (toplu eklemenin toplam süresi)
}

// connectDaemon bağlanır; olmazsa launch çalıştırılır ve wait süresince yeniden denenir.
func connectDaemon(sock string, launch func(), wait time.Duration) (*daemon, error) {
	deadline := time.Now().Add(wait)
	launched := false
	for {
		c, err := net.Dial("unix", sock)
		if err == nil {
			return &daemon{c: c, r: bufio.NewReader(c)}, nil
		}
		if launch == nil || !time.Now().Before(deadline) {
			return nil, err
		}
		if !launched {
			launch()
			launched = true
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (d *daemon) Close() { d.c.Close() }

// call komutu yollar, aynı id'li yanıtı bekler (araya giren olaylar atlanır).
func (d *daemon) call(cmd string, args map[string]any) (map[string]any, error) {
	d.next++
	req := map[string]any{"id": d.next, "cmd": cmd}
	for k, v := range args {
		req[k] = v
	}
	line, _ := json.Marshal(req)
	dl := time.Now().Add(30 * time.Second)
	if !d.until.IsZero() && d.until.Before(dl) {
		dl = d.until
	}
	d.c.SetDeadline(dl)
	if _, err := d.c.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	for {
		s, err := d.r.ReadBytes('\n')
		if err != nil {
			return nil, err
		}
		var m map[string]any
		if json.Unmarshal(s, &m) != nil || m["id"] != float64(d.next) {
			continue
		}
		if m["ok"] != true {
			e, _ := m["error"].(string)
			return m, errors.New(e)
		}
		return m, nil
	}
}

type nativeHost struct {
	sock   string
	launch func()
	wait   time.Duration
}

func newNativeHost(sock string) *nativeHost {
	return &nativeHost{sock: sock, wait: 10 * time.Second, launch: func() {
		if err := exec.Command("/usr/bin/open", "-b", "com.uba.octo").Run(); err != nil {
			fmt.Fprintln(os.Stderr, "uygulama açılamadı:", err)
		}
	}}
}

type extMsg struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	Referrer  string `json:"referrer"`
	Cookies   string `json:"cookies"`
	UserAgent string `json:"userAgent"`
	Filename  string `json:"filename"`
	Size      int64  `json:"size"`    // tarayıcının bildirdiği; aynı ad ve boyuttaki yarım öğeye "adresi yenile" önerisi (spec §2)
	PageURL   string `json:"pageUrl"` // Kaynak sayfa (tam adres): Bilgi paneli ve motorun yerel ağ kuralı; başlık olarak gitmez
	Start     bool   `json:"start"`   // batch: eklentide "Başlat"; false ise kuyrukta bekler
	// Stream: download'da URL bir HLS/DASH manifest'idir (video düğmesi); eksik = sıradan dosya.
	Stream *engine.StreamSpec `json:"stream"`
	Links  []struct {
		URL      string `json:"url"`
		Cookies  string `json:"cookies"`  // tarayıcının bu linke göndereceği çerezler
		Referrer string `json:"referrer"` // boşsa üst düzey (sayfa kökeni)
	} `json:"links"`
}

// hostVersion: eklenti features'a bakarak video akışı seçeneklerini açar (eski host: yok).
const hostVersion = "1.1"

var hostFeatures = []string{"stream"}

// headers: Referer target'a göre tarayıcı kuralıyla kırpılır.
func (m extMsg) headers(target, cookies, referrer string) map[string]string {
	h := map[string]string{}
	for k, v := range map[string]string{"Referer": engine.ReferrerFor(referrer, target), "Cookie": cookies, "User-Agent": m.UserAgent} {
		if v != "" {
			h[k] = v
		}
	}
	return h
}

// page: eski eklenti alanı göndermezse add'e de girmez.
func (m extMsg) page(args map[string]any) map[string]any {
	if m.PageURL != "" {
		args["pageUrl"] = m.PageURL
	}
	return args
}

// serve girişten EOF'a kadar iletileri işler; stdout'a yalnız çerçeveli yanıt yazılır.
func (h *nativeHost) serve(in io.Reader, out io.Writer) error {
	for {
		b, err := readFrame(in)
		if err == io.EOF {
			return nil
		}
		if errors.Is(err, errFrameTooBig) { // gövde okunmaz; kullanıcı yanıltıcı "kurulu değil" yerine nedeni görsün
			writeFrame(out, map[string]any{"type": "error", "error": "Seçim çok büyük; daha az bağlantı seçip tekrar deneyin."})
			return err
		}
		if err != nil {
			return err
		}
		var m extMsg
		if json.Unmarshal(b, &m) != nil {
			err = writeFrame(out, map[string]any{"type": "error", "error": "geçersiz ileti"})
		} else {
			err = writeFrame(out, h.handle(m))
		}
		if err != nil {
			return err
		}
	}
}

// defaultExts eklentinin varsayılan yakalama listesidir (decide.mjs DEFAULT_EXTS).
var defaultExts = strings.Fields("3gp 7z aac ace aif apk arj asf avi bin bz2 exe gz gzip img iso lzh m4a m4v mkv mov mp3 mp4 mpa mpe " +
	"mpeg mpg msi msu ogg ogv pdf plj pps ppt qt r0* r1* ra rar rm rmvb sea sit sitx tar tif tiff wav wma wmv z zip")

func (h *nativeHost) handle(m extMsg) map[string]any {
	if m.Type == "open" { // eklenti açılır penceresinden: servis çalışıyor olsa da uygulamayı öne getirir
		if h.launch != nil {
			h.launch()
		}
		return map[string]any{"type": "ack", "accepted": true}
	}
	launch, wait := h.launch, h.wait
	if m.Type == "hello" { // hello uygulamayı açmaz: Chrome her açılışta gönderir
		launch, wait = nil, 0
	}
	d, err := connectDaemon(h.sock, launch, wait)
	if err != nil {
		fmt.Fprintln(os.Stderr, "servis yok:", err)
		if m.Type == "stream.probe" {
			return map[string]any{"type": "error", "error": "Octo açılamadı"}
		}
		if m.Type == "hello" {
			return map[string]any{"type": "hello", "version": hostVersion, "features": hostFeatures, "hideVideoButton": false, "captureExts": defaultExts, "excludedSites": []string{}, "offline": true}
		}
		return map[string]any{"type": "ack", "accepted": false}
	}
	defer d.Close()
	switch m.Type {
	case "hello":
		d.call("hello", nil) // yalnız zaman damgası: eski motor "bilinmeyen komut" derse hello yine de yanıtlanır
		r, err := d.call("list", nil)
		if err != nil {
			return map[string]any{"type": "error", "error": err.Error()}
		}
		set, _ := r["settings"].(map[string]any)
		hide, _ := set["hideVideoButton"].(bool) // eski motor alanı yollamaz: düğme açık
		return map[string]any{"type": "hello", "version": hostVersion, "features": hostFeatures, "hideVideoButton": hide,
			"captureExts": set["captureExts"], "excludedSites": set["excludedSites"], "captureOff": set["captureOff"], "captureMinKB": set["captureMinKB"]}
	case "stream.probe": // kullanıcı video düğmesine tıkladı: uygulama gerekirse açılır
		if u, err := url.Parse(m.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return map[string]any{"type": "error", "error": "yalnız http ve https adresleri indirilebilir"}
		}
		r, err := d.call("stream.probe", m.page(map[string]any{"url": m.URL, "headers": m.headers(m.URL, m.Cookies, m.Referrer)}))
		if err != nil {
			return map[string]any{"type": "error", "error": err.Error()}
		}
		return map[string]any{"type": "stream", "info": r["info"]}
	case "download":
		args := m.page(map[string]any{"url": m.URL, "headers": m.headers(m.URL, m.Cookies, m.Referrer), "name": m.Filename, "size": m.Size, "start": true})
		if m.Stream != nil { // çerez yalnız manifest adresine aittir; parçalara motor taşır/düşürür
			args["stream"] = engine.StreamSpec{Kind: m.Stream.Kind, Video: m.Stream.Video, Audio: m.Stream.Audio, Label: m.Stream.Label}
		}
		_, err := d.call("add", args)
		if err != nil {
			fmt.Fprintln(os.Stderr, "add:", err)
			return map[string]any{"type": "ack", "accepted": false, "error": err.Error()} // video düğmesi metni gösterir
		}
		return map[string]any{"type": "ack", "accepted": true}
	case "batch":
		return h.batch(m, d)
	}
	return map[string]any{"type": "error", "error": "bilinmeyen ileti: " + m.Type}
}

// batchConns: toplu eklemede servise aynı anda giden add (Probe) sayısı; batchWait: toplu işin toplam süresi.
var batchConns, batchWait = 4, 60 * time.Second

// batch linkleri ayrı bağlantılardan eşzamanlı ekler (sıra Probe süresine göre karışabilir). Süre dolunca yanıtı
// gelmeyenler "pending" (servis yine ekleyebilir), hiç gönderilmeyenler "notSent" döner.
func (h *nativeHost) batch(m extMsg, first *daemon) map[string]any {
	var adds []map[string]any
	for _, l := range m.Links {
		if u, err := url.Parse(l.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			continue
		}
		ref := l.Referrer
		if ref == "" {
			ref = m.Referrer
		}
		// Üst düzey sayfa çerezi linklere yayılmaz (host-only/Path/Secure kuralını yalnız tarayıcı bilir).
		adds = append(adds, m.page(map[string]any{"url": l.URL, "headers": m.headers(l.URL, l.Cookies, ref), "queue": "main", "start": m.Start}))
	}
	const (
		notSent = iota
		added
		failed
		pending
	)
	until := time.Now().Add(batchWait)
	res := make([]int, len(adds))
	var mu sync.Mutex
	next := 0
	take := func() int {
		mu.Lock()
		defer mu.Unlock()
		if next == len(adds) || !time.Now().Before(until) {
			return -1
		}
		next++
		return next - 1
	}
	var wg sync.WaitGroup
	for w := 0; w < min(batchConns, len(adds)); w++ {
		d := first
		if w > 0 {
			var err error
			if d, err = connectDaemon(h.sock, nil, 0); err != nil {
				break
			}
		}
		d.until = until
		wg.Add(1)
		go func() {
			defer wg.Done()
			if d != first {
				defer d.Close()
			}
			for i := take(); i >= 0; i = take() {
				_, err := d.call("add", adds[i])
				r := added
				if errors.Is(err, os.ErrDeadlineExceeded) {
					r = pending
				} else if err != nil {
					r = failed
					fmt.Fprintln(os.Stderr, "add:", err)
				}
				mu.Lock()
				res[i] = r
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	ok := len(m.Links) == 0
	var late, unsent []string
	for i, r := range res {
		u := adds[i]["url"].(string)
		switch r {
		case added:
			ok = true
		case pending: // servise ulaştı: accepted:false eklentide yeniden göndermeye (çift öğeye) yol açardı
			ok = true
			late = append(late, u)
		case notSent:
			unsent = append(unsent, u)
		}
	}
	reply := map[string]any{"type": "ack", "accepted": ok}
	if late != nil {
		reply["pending"] = late
	}
	if unsent != nil {
		reply["notSent"] = unsent
	}
	return reply
}
