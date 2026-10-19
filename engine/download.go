// Package engine: klasik parçalı indirme yöneticisi davranışını izleyen Mac indirme motoru.
// Kurallar: tek bağlantıyla başla, 2 sn'de bir bağlantıyı
// ikiye katla (1→2→4→8), her yeni bağlantı en çok kalan parçanın ikinci yarısını alır,
// işi biten bağlantı yine en büyük parçayı böler, takılan parçayı boştaki bağlantı devralır.
// Fark: klasik yöneticiler parçaları ayrı dosyalara yazıp sonda birleştirir; burada tek dosyaya
// ofsetle yazılır, sondaki birleştirme kopyası yok.
package engine

import (
	"bytes"
	"cmp"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// cleanUserAgent: kırpılmış ayar değeri; boş ya da denetim karakteri (başlık enjeksiyonu) içeriyorsa varsayılan.
func cleanUserAgent(s string) string {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsFunc(s, func(r rune) bool { return r < 0x20 && r != '\t' || r == 0x7f }) {
		return userAgent
	}
	return s
}

const (
	minChunk    = 11680           // 8*1460, en küçük parça
	minSplit    = 2*minChunk + 3  // bundan az kalan parça bölünmez (23363)
	perConnMin  = 0x2800          // bağlantı başına en az 10 KB
	hardMaxConn = 32              // bağlantı üst sınırı
	rampEvery   = 2 * time.Second // bağlantı sayısı en erken 2 sn'de bir artar
	stealAfter  = 2 * time.Second // bölünecek parça yoksa 2 sn veri gelmeyen parça devralınır
	maxRetries  = 20
	userAgent   = "octo/1.0" // tarayıcı UA'sı bazı sunucularda (ör. Hetzner) bağlantıyı kestiriyor
)

// readTimeout: 30 sn veri yoksa bağlantı yeniden kurulur. Test için değiştirilebilir.
var readTimeout = 30 * time.Second

// Bağlantı durumları (Seg.Status); ilerleme penceresinde olduğu gibi gösterilir.
const (
	statusTimeout  = "Zaman aşımı, yeniden bağlanıyor"
	statusNewPiece = "Yeni parça aldı"
)

// ErrPaused: Pause sonrası Wait'in döndürdüğü hata; .part dosyası korunur.
var ErrPaused = errors.New("duraklatıldı")

var (
	errTailMismatch = errors.New("içerik değişti") // içerik değişimi kontrolü
	errDropped      = errors.New("bağlantı bırakıldı")
)

// Request bir indirmeyi tanımlar.
type Request struct {
	URL    string
	Header http.Header // her isteğe eklenir (Referer, Cookie, User-Agent...); boş UA için varsayılan kullanılır
	// Path: son dosya yolu; motor Path+".part" yazar, bitince yeniden adlandırır.
	// Boşsa sunucunun verdiği ad ve geçerli dizin kullanılır.
	Path     string
	MaxConns int // 0 ise 8
	// UserAgent: Header'da User-Agent yoksa gönderilir; boş ya da geçersizse varsayılan (octo/1.0).
	UserAgent string
	Auth      AuthFunc
	// Overwrite: bitişte hedefte o arada oluşmuş dosya ezilir (Duplicate "overwrite"); yoksa numaralı ad seçilir.
	Overwrite bool
	// Trash: Overwrite'ta hedefteki eski dosyayı Çöp'e taşır; nil ise eski dosya ezilir, hata verirse numaralı ad seçilir.
	Trash func(path string) error
	// ServerDate: bitişte dosyanın değişiklik tarihi sunucunun Last-Modified'ı olur (Ayarlar > "Sunucu tarihini kullan").
	ServerDate bool
	Limit      *Limiter    // dosya başına hız sınırı (nil = yok); küresel sınırla birlikte uygulanır
	Stream     *StreamSpec // nil değilse URL bir HLS/DASH manifest'idir (StartStream)
	Page       string      // kaynak sayfa: akışta Referer yoksa yerel ağ kuralı buna bakar
}

// AuthFunc host için kayıtlı site girişini verir (yoksa ok=false). Yalnız 401 yanıtında çağrılır.
type AuthFunc func(host string) (user, pass string, ok bool)

// Seg bir bağlantının parçasıdır; End dahil, Pos yazılacak sıradaki bayt (Pos > End: parça bitti).
type Seg struct {
	Start int64 `json:"start"`
	Pos   int64 `json:"pos"`
	End   int64 `json:"end"`
	// Status: bağlantının son olayı (statusTimeout, statusNewPiece); boş = veri alınıyor. Durum dosyasına yazılmaz.
	Status string `json:"status,omitempty"`
}

// State: Path+".part.json" durum dosyası; Resume bundan devam eder.
type State struct {
	URL          string `json:"url"`
	Size         int64  `json:"size"`
	ETag         string `json:"etag"`
	LastModified string `json:"lastModified"`
	Chunks       []Seg  `json:"chunks"`
}

// Snapshot indirmenin anlık durumudur.
type Snapshot struct {
	Got, Size int64 // Size bilinmiyorsa -1
	Speed     float64
	Conns     []Seg
	Resumable bool // sunucu aralık isteğini destekliyor
	// Restarted: devam edilemedi, indirme 0'dan yeniden başladı (içerik/boyut/tarih değişti ya da 200).
	Restarted     bool
	RestartReason string
	Path          string // son dosya yolu; bitişte ad doluysa numaralanmış olabilir
}

type chunk struct {
	start, pos, end int64 // end dahil; pos = yazılacak sıradaki bayt
	gen             int   // sahibi değişince artar; eski sahip kendini durdurur
	last            time.Time
	cancel          context.CancelFunc
	orphan          bool            // sahibi yok (devamdan gelen ya da bırakılan parça); split önce bunları verir
	ctx             context.Context // bağlantının bağlamı; cancel bunu iptal eder
	throttled       bool            // hız sınırlayıcıyı bekliyor: takılmış sayılmaz, devralınmaz
	status          string          // Seg.Status
}

func (c *chunk) rem() int64 { return c.end - c.pos + 1 }

// Download çalışan bir indirmedir.
type Download struct {
	mu         sync.Mutex
	url        string
	header     http.Header
	userAgent  string // Header'da UA yoksa gönderilen; newDownload temizler
	auth       AuthFunc
	authHdrs   map[string]string // 401'de eklenen site girişleri (anahtar: küçük harf host:port); send yalnız https'te ekler, d.header'a girmez
	path       string
	overwrite  bool
	trash      func(string) error
	serverDate bool
	maxConn    int
	size       int64
	f          *os.File
	chunks     []*chunk // start'a göre sıralı
	active     int
	wg         sync.WaitGroup
	client     *http.Client
	lim        *Limiter
	excl       bool  // open .part'ı O_EXCL ile açsın (yeni indirme)
	err        error // ilk gerçek hata
	ranged     bool
	written    int64 // aralıksız modda yazılan bayt
	singleDone bool  // aralıksız modda gövde sonuna kadar okundu
	speed      float64
	paused     bool
	etag       string
	lastMod    string
	pending    string // istenen 0'dan yeniden başlatmanın nedeni
	restarted  bool
	reason     string
	completed  bool

	saveMu sync.Mutex     // durum dosyası yazımlarını sıraya koyar
	saving atomic.Bool    // watch'ın arka plan durum yazımı sürüyor (en çok bir)
	saves  sync.WaitGroup // finish, done'u kapatmadan önce arka plan yazımını bekler

	ctx       context.Context // indirmenin ömrü; Pause keser
	cancel    context.CancelFunc
	run       context.Context // bir çalıştırma; 0'dan yeniden başlatma yalnız bunu keser (işçi yokken değişir)
	runCancel context.CancelFunc
	done      chan struct{}
	result    error
}

func newDialer() *net.Dialer {
	return &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
}

func newClient() *http.Client {
	tr := &http.Transport{
		Proxy:                 systemProxy.proxy, // ortam değişkeni önce, yoksa macOS sistem proxy'si
		DialContext:           newDialer().DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: readTimeout, // yanıt gelmeyen bağlantı da yeniden kurulsun
		// HTTP/2 kapalı: h2 tüm istekleri tek TCP bağlantısına toplar, çoklu bağlantının anlamı kalmaz.
		TLSNextProto:        map[string]func(string, *tls.Conn) http.RoundTripper{},
		DisableCompression:  true, // gzip açılırsa bayt aralıkları bozulur
		MaxIdleConnsPerHost: hardMaxConn,
	}
	return &http.Client{Transport: tr, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 { // Go'nun varsayılan sınırı
			return errors.New("10 yönlendirmeden sonra durduruldu")
		}
		stripOnRedirect(r.Header, via[0].URL, r.URL) // Go aynı ana makinede şemaya, Referer'da kökene bakmaz
		return nil
	}}
}

// downgraded: https'ten http'ye düşüş (tarayıcı Secure çerezi ve kimliği düz HTTP'ye yollamaz).
func downgraded(from, to *url.URL) bool { return from.Scheme == "https" && to.Scheme == "http" }

// stripOnRedirect tarayıcının yönlendirme kuralıdır: başka siteye ya da https → http'ye Cookie gitmez;
// Authorization yalnız aynı kökende (şema+host+port) kalır (Fetch); Referer yeni hedefe göre kırpılır.
func stripOnRedirect(h http.Header, from, to *url.URL) {
	if from.String() == to.String() { // yönlendirme yok: başlıklar çağıranın verdiği gibi kalır
		return
	}
	if !sameSite(from.Hostname(), to.Hostname()) || downgraded(from, to) {
		h.Del("Cookie")
	}
	if from.Scheme != to.Scheme || !strings.EqualFold(from.Host, to.Host) {
		h.Del("Authorization")
	}
	if ref := h.Get("Referer"); ref != "" {
		if ref = ReferrerFor(ref, to.String()); ref != "" {
			h.Set("Referer", ref)
		} else {
			h.Del("Referer")
		}
	}
}

// ReferrerFor: tarayıcının varsayılanı strict-origin-when-cross-origin (decide.mjs referrerFor ile aynı):
// aynı köken tam adres (#'sız), farklı köken yalnız köken, https → http ve web dışı sayfa boş.
func ReferrerFor(page, target string) string {
	p, err1 := url.Parse(page)
	t, err2 := url.Parse(target)
	if err1 != nil || err2 != nil || p.Host == "" || (p.Scheme != "http" && p.Scheme != "https") || (p.Scheme == "https" && t.Scheme == "http") {
		return ""
	}
	origin := p.Scheme + "://" + strings.ToLower(p.Host)
	if origin != t.Scheme+"://"+strings.ToLower(t.Host) {
		return origin + "/"
	}
	p.Fragment, p.RawFragment = "", ""
	return p.String()
}

func (d *Download) get(ctx context.Context, from int64) (*http.Response, error) {
	d.mu.Lock()
	u := d.url
	d.mu.Unlock()
	// Uç açık aralık: bağlantı bitince arkadaki başlamamış parçayı aynı akıştan okumaya devam edebilir.
	return d.do(ctx, u, fmt.Sprintf("bytes=%d-", from))
}

// do: If-Range yok; doğrulama istemcide yapılır. 401'de site girişi bir kez eklenir.
func (d *Download) do(ctx context.Context, u, rng string) (*http.Response, error) {
	resp, err := d.send(ctx, u, u, rng)
	d.mu.Lock()
	auth := d.auth
	d.mu.Unlock()
	if err != nil || resp.StatusCode != http.StatusUnauthorized || auth == nil || resp.Request.Header.Get("Authorization") != "" {
		return resp, err
	}
	final := resp.Request.URL    // yönlendirme sonrası adres; giriş onun host'una aittir
	if final.Scheme != "https" { // kayıtlı parola düz HTTP'ye hiç gitmez: yönlendiren ağdaki saldırgan olabilir
		resp.Body.Close()
		return nil, errors.New("giriş gerekli (güvensiz bağlantı)")
	}
	key := strings.ToLower(final.Host)
	d.mu.Lock()
	_, known := d.authHdrs[key]
	d.mu.Unlock()
	if !known { // aynı host için Anahtar Zinciri bir kez okunur
		user, pass, ok := auth(final.Hostname())
		if !ok {
			return resp, nil
		}
		r := http.Request{Header: http.Header{}}
		r.SetBasicAuth(user, pass)
		d.mu.Lock()
		if d.authHdrs == nil {
			d.authHdrs = map[string]string{}
		}
		d.authHdrs[key] = r.Header.Get("Authorization")
		d.mu.Unlock()
	}
	resp.Body.Close()
	return d.send(ctx, u, final.String(), rng)
}

// send: d.header via adresine aittir; u başka yerse (401 sonrası doğrudan final'e) yönlendirme kuralı uygulanır.
func (d *Download) send(ctx context.Context, via, u, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	d.mu.Lock() // 401 yanıtı başka bağlantıda girişi değiştirebilir
	for k, v := range d.header {
		req.Header[k] = v
	}
	if from, err := url.Parse(via); err == nil {
		stripOnRedirect(req.Header, from, req.URL)
	}
	if a, ok := d.authHdrs[strings.ToLower(req.URL.Host)]; ok && req.URL.Scheme == "https" {
		req.Header.Set("Authorization", a)
	}
	d.mu.Unlock()
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", cmp.Or(d.userAgent, userAgent)) // Probe newDownload kullanmaz, userAgent boş kalır
	}
	if req.Header.Get("Accept") == "" {
		req.Header.Set("Accept", "*/*")
	}
	req.Header.Set("Accept-Encoding", "identity") // klasik varsayılan
	if rng != "" {                                // akış parçaları tüm gövdeyle istenir (tarayıcı MSE parçalarına Range yollamaz)
		req.Header.Set("Range", rng)
	}
	return d.client.Do(req)
}

// split: en çok kalanı bul, ikinci yarısını yeni parçaya ver.
// Bölünecek yer yoksa takılmış (stealAfter) bir parçayı devralır. İkisi de yoksa nil.
// Sahipsiz parça varsa önce o verilir. Kilit altında çağrılır.
func (d *Download) split() *chunk {
	for _, c := range d.chunks {
		if c.orphan && c.rem() > 0 {
			c.orphan, c.last = false, time.Now()
			return c
		}
	}
	var best *chunk
	idx := -1
	for i, c := range d.chunks {
		if c.rem() > 0 && (best == nil || c.rem() > best.rem()) {
			best, idx = c, i
		}
	}
	if best == nil {
		return nil
	}
	if r := best.rem(); r >= minSplit {
		ns := best.end - r/2
		if len(d.chunks) == 1 { // ilk bölmede ikinci bağlantı ilk bağlantının yakınından başlar
			ns = min(ns, best.start+10*(best.pos-best.start+minChunk))
		}
		nc := &chunk{start: ns, pos: ns, end: best.end, last: time.Now()}
		best.end = ns - 1
		d.chunks = append(d.chunks[:idx+1], append([]*chunk{nc}, d.chunks[idx+1:]...)...)
		return nc
	}
	// shortcut: klasik yöneticiler takılan parçaya ikinci bağlantıyla yarışır; burada yavaş sahibi kesip parçayı devralıyoruz.
	var stuck *chunk
	for _, c := range d.chunks {
		if c.rem() > 0 && !c.throttled && time.Since(c.last) >= stealAfter && (stuck == nil || c.last.Before(stuck.last)) {
			stuck = c
		}
	}
	if stuck != nil {
		stuck.gen++
		if stuck.cancel != nil {
			stuck.cancel()
		}
		stuck.last = time.Now()
	}
	return stuck
}

// fail: ilk ölümcül hatayı kaydeder ve çalıştırmayı keser; tüm bağlantılar ve ramp hemen durur.
func (d *Download) fail(err error) {
	d.mu.Lock()
	if d.err == nil {
		d.err = err
	}
	d.mu.Unlock()
	d.runCancel()
}

// restart: tüm bağlantıları keser; supervise işçiler durunca indirmeyi 0'dan başlatır.
func (d *Download) restart(reason string) {
	d.mu.Lock()
	if d.pending == "" {
		d.pending = reason
	}
	d.mu.Unlock()
	d.runCancel()
}

// worker: bir parçayı bitirir, sonra split ile yeni iş alır (boşalan bağlantı da en büyük parçayı böler).
// first: ilk veri alan bağlantı, kendi ilk parçasındayken; 403/404/410 yalnız onda hemen ölümcül.
func (d *Download) worker(c *chunk, resp *http.Response, first bool) {
	defer d.wg.Done()
	for {
		d.mu.Lock()
		gen := c.gen
		d.mu.Unlock()
		err := d.fill(c, gen, resp, first)
		resp, first = nil, false // split'ten alınan parçanın isteği ek bağlantı gibidir (403/404 = bağlantı sınırı)
		switch err {
		case nil:
		case errDropped: // drop active'i zaten azalttı
			return
		case errTailMismatch:
			d.restart("içerik sunucuda değişti (64 bayt denetimi)")
		default:
			d.fail(err)
		}
		// Yeni iş alma ve çıkış aynı kilitte: drop ile yarışırsa sahipsiz parça ortada kalmaz.
		d.mu.Lock()
		c = nil
		if err == nil && d.err == nil && d.run.Err() == nil {
			if c = d.split(); c != nil {
				c.status = statusNewPiece
			}
		}
		if c == nil {
			d.active--
		}
		d.mu.Unlock()
		if c == nil {
			return
		}
	}
}

// drop: ek bağlantıda 403/404 = sunucunun bağlantı sınırı (klasik yönetici davranışı). Bağlantı kapanır, parça
// havuza döner, en çok bağlantı bir azalır. Son açık bağlantı bırakılmaz (false: yeniden denesin).
func (d *Download) drop(c *chunk, gen int) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.active <= 1 {
		return false
	}
	if c.gen == gen { // parça devralındıysa artık başkasının
		c.orphan = true
	}
	d.maxConn = max(1, d.maxConn-1)
	d.active--
	return true
}

// fill: parçayı pos'tan end'e kadar indirir; kopan bağlantıyı kaldığı yerden yeniden kurar.
func (d *Download) fill(c *chunk, gen int, resp *http.Response, first bool) error {
	buf := make([]byte, 256<<10)
	fails := 0
	for {
		d.mu.Lock()
		if c.gen != gen || c.rem() <= 0 || d.run.Err() != nil { // duraklatma/iptal de buradan çıkar
			d.mu.Unlock()
			if resp != nil {
				resp.Body.Close()
			}
			return nil
		}
		cancel, ctx := c.cancel, c.ctx // ilk bağlantının yanıtı Start/Resume'daki bağlamla geldi
		if resp == nil {
			ctx, cancel = context.WithCancel(d.run)
			c.cancel, c.ctx = cancel, ctx
		}
		from := c.pos
		ov := min(64, from-c.start) // kaldığı yerden süren parça 64 bayt geriden istenir
		d.mu.Unlock()
		tail := make([]byte, ov) // [from-ov, from) zaten bu parçanın yazdığı veri
		if _, err := d.f.ReadAt(tail, from-ov); err != nil {
			cancel()
			if resp != nil {
				resp.Body.Close()
			}
			return err
		}

		if resp == nil {
			var err error
			resp, err = d.get(ctx, from-ov)
			if err == nil {
				// 206, istenen başlangıç ve aynı toplam boyut şart
				if st, total, ok := contentRange(resp.Header.Get("Content-Range")); resp.StatusCode != http.StatusPartialContent || !ok || st != from-ov || total != d.size {
					resp.Body.Close()
					err = fmt.Errorf("sunucu aralık isteğine beklenmeyen yanıt: %s %s", resp.Status, resp.Header.Get("Content-Range"))
					switch code := resp.StatusCode; {
					case first && (code == http.StatusForbidden || code == http.StatusNotFound || code == http.StatusGone):
						cancel()
						return fmt.Errorf("sunucu %s döndü", resp.Status) // izin yok / dosya yok: yeniden denemenin yararı yok
					case !first && (code == http.StatusForbidden || code == http.StatusNotFound) && d.drop(c, gen):
						cancel()
						return errDropped
					}
				}
			}
			if err != nil {
				cancel()
				resp = nil
				if ne := net.Error(nil); errors.As(err, &ne) && ne.Timeout() { // ResponseHeaderTimeout
					d.mark(c, gen, statusTimeout)
				}
				if fails++; fails > maxRetries {
					return err
				}
				if fails > 3 {
					select {
					case <-time.After(time.Second):
					case <-d.run.Done():
					}
				}
				continue
			}
		}
		done, err := d.stream(ctx, c, gen, resp.Body, from, tail, buf, cancel)
		resp.Body.Close()
		resp = nil
		cancel()
		if done {
			return err
		}
		if err != nil {
			if fails++; fails > maxRetries {
				return err
			}
		} else {
			fails = 0
		}
	}
}

// stream: gövdeyi okuyup dosyaya yazar. Parça bitince arkadaki parça henüz veri almamışsa
// (<65 bayt) onu da aynı akıştan devralır.
// Gövde off-len(tail)'den başlar; ilk baytlar diskteki tail ile aynı olmalı (içerik değişimi kontrolü).
func (d *Download) stream(ctx context.Context, c *chunk, gen int, body io.Reader, off int64, tail, buf []byte, cancel context.CancelFunc) (bool, error) {
	var timedOut atomic.Bool
	idle := time.AfterFunc(readTimeout, func() { timedOut.Store(true); cancel() })
	defer idle.Stop()
	for {
		n, err := body.Read(buf)
		p := buf[:n]
		if k := min(len(tail), n); k > 0 {
			if !bytes.Equal(p[:k], tail[:k]) {
				return true, errTailMismatch
			}
			tail, p = tail[k:], p[k:]
		}
		if n > 0 {
			idle.Stop() // idle yalnız sunucu sessizliğini ölçer; sınırlayıcı beklemesi sayılmaz
			d.mu.Lock()
			c.throttled = true
			d.mu.Unlock()
			werr := d.lim.Wait(ctx, n)
			d.mu.Lock()
			c.throttled, c.last = false, time.Now() // sınırlayıcı beklemesi 2 sn çalma kuralını tetiklemesin
			d.mu.Unlock()
			if werr != nil {
				return false, werr
			}
			idle.Reset(readTimeout)
		}
		if len(p) > 0 {
			if _, werr := d.f.WriteAt(p, off); werr != nil {
				d.fail(werr)
				return true, werr
			}
			off += int64(len(p))
			d.mu.Lock()
			if c.gen != gen {
				d.mu.Unlock()
				return true, nil
			}
			c.pos, c.last = off, time.Now()
			if c.status == statusTimeout {
				c.status = ""
			}
			if c.rem() <= 0 && !d.takeNext(c) {
				c.pos = c.end + 1 // bölünmeyle end geriye çekilmiş olabilir; fazla yazılan baytlar aynı veri
				d.mu.Unlock()
				return true, nil
			}
			d.mu.Unlock()
		}
		if err != nil {
			if timedOut.Load() {
				d.mark(c, gen, statusTimeout)
			}
			if err == io.EOF {
				err = io.ErrUnexpectedEOF
			}
			return false, err
		}
	}
}

// mark: bağlantı durumunu yazar; parça devralındıysa (gen değişti) dokunmaz.
func (d *Download) mark(c *chunk, gen int, status string) {
	d.mu.Lock()
	if c.gen == gen {
		c.status = status
	}
	d.mu.Unlock()
}

// takeNext: kilit altında çağrılır.
func (d *Download) takeNext(c *chunk) bool {
	for i, b := range d.chunks {
		if b.start == c.end+1 && b.rem() > 0 && b.pos-b.start < 65 {
			b.gen++
			if b.cancel != nil {
				b.cancel()
			}
			c.end, c.status = b.end, statusNewPiece
			d.chunks = append(d.chunks[:i], d.chunks[i+1:]...)
			return true
		}
	}
	return false
}

// gotLocked: kilit altında çağrılır.
func (d *Download) gotLocked() int64 {
	if !d.ranged {
		return d.written
	}
	got := d.size
	for _, c := range d.chunks {
		got -= max(c.rem(), 0)
	}
	return got
}

// contentRange: "bytes 0-99/1000" → 0, 1000
func contentRange(h string) (start, total int64, ok bool) {
	h, found := strings.CutPrefix(h, "bytes ")
	dash, slash := strings.IndexByte(h, '-'), strings.LastIndexByte(h, '/')
	if !found || dash < 0 || slash < dash {
		return 0, 0, false
	}
	start, e1 := strconv.ParseInt(h[:dash], 10, 64)
	total, e2 := strconv.ParseInt(h[slash+1:], 10, 64)
	return start, total, e1 == nil && e2 == nil && total > 0
}

// Start ilk yanıt başlıkları gelince döner; indirme arka planda sürer. Bitişi Wait bildirir.
// Yeni .part O_EXCL ile açılır: aynı yola yazan ikinci indirme hata alır, canlı .part ezilmez.
func Start(ctx context.Context, r Request, lim *Limiter) (*Download, error) {
	return start(ctx, r, lim, true)
}

func start(ctx context.Context, r Request, lim *Limiter, excl bool) (*Download, error) {
	d := newDownload(ctx, r, lim)
	d.excl = excl
	if err := d.open(); err != nil {
		d.cancel()
		return nil, err
	}
	go d.supervise()
	return d, nil
}

func newDownload(ctx context.Context, r Request, lim *Limiter) *Download {
	hdr := http.Header{}
	for k, vs := range r.Header { // kopya + kanonik anahtar; çağıranın haritasına dokunulmaz
		for _, v := range vs {
			hdr.Add(k, v)
		}
	}
	maxConn := r.MaxConns
	if maxConn <= 0 {
		maxConn = 8
	}
	d := &Download{url: r.URL, header: hdr, userAgent: cleanUserAgent(r.UserAgent), auth: r.Auth, path: r.Path, overwrite: r.Overwrite, trash: r.Trash, serverDate: r.ServerDate, maxConn: min(maxConn, hardMaxConn), client: newClient(), lim: lim, done: make(chan struct{})}
	d.ctx, d.cancel = context.WithCancel(ctx)
	d.run, d.runCancel = context.WithCancel(d.ctx)
	return d
}

// open: indirmeyi 0'dan kurar (.part sıfırlanır) ve ilk bağlantıyı başlatır. İşçi yokken çağrılır.
func (d *Download) open() error {
	fctx, fcancel := context.WithCancel(d.run) // ilk parçanın bağlamı; devralınınca yalnız o bağlantı kesilir
	from := d.url
	resp, err := d.get(fctx, 0)
	if err != nil {
		fcancel()
		return err
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		resp.Body.Close()
		fcancel()
		return fmt.Errorf("sunucu %s döndü", resp.Status)
	}
	d.follow(resp, from)
	if d.path == "" {
		d.path = FileNameFromResponse(resp)
	}
	_, size, ok := contentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode == http.StatusPartialContent && !ok {
		resp.Body.Close()
		fcancel()
		return fmt.Errorf("sunucu geçersiz aralık yanıtı gönderdi (Content-Range: %q)", resp.Header.Get("Content-Range"))
	}
	flag := os.O_RDWR | os.O_CREATE | os.O_TRUNC
	if d.excl { // yalnız ilk açılış; 0'dan yeniden başlatma kendi .part'ını sıfırlar
		flag |= os.O_EXCL
		d.excl = false
	}
	var f *os.File
	err = os.MkdirAll(filepath.Dir(d.path), 0o755) // kategori alt klasörü ilk indirmede oluşur
	if err != nil {
		err = fileErr("klasör oluşturulamadı", filepath.Dir(d.path), err)
	} else if f, err = os.OpenFile(d.path+".part", flag, 0o644); errors.Is(err, os.ErrExist) {
		err = fmt.Errorf("%s.part zaten var (başka bir indirme kullanıyor olabilir)", d.path)
	} else if err != nil {
		err = fileErr("dosya açılamadı", d.path+".part", err)
	}
	if err == nil && resp.StatusCode == http.StatusPartialContent {
		err = f.Truncate(size)
	}
	if err != nil {
		if f != nil {
			f.Close()
		}
		resp.Body.Close()
		fcancel()
		return err
	}

	d.mu.Lock()
	d.f, d.etag, d.lastMod = f, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	d.chunks, d.written, d.singleDone = nil, 0, false
	if resp.StatusCode == http.StatusPartialContent {
		_, d.size, d.ranged = contentRange(resp.Header.Get("Content-Range"))
	} else {
		d.size, d.ranged = resp.ContentLength, false
	}
	d.wg.Add(1)
	if d.ranged {
		c := &chunk{start: 0, pos: 0, end: d.size - 1, last: time.Now(), cancel: fcancel, ctx: fctx}
		d.chunks = []*chunk{c}
		d.active = 1
		go d.worker(c, resp, true)
	} else { // aralık desteği yoksa tek bağlantı, baştan sona
		go d.single(resp, fcancel)
	}
	d.mu.Unlock()
	return nil
}

// fileErr dosya sistemi hatasını Türkçe verir (OS metni yalnız bilinmeyen durumda kalır).
func fileErr(what, p string, err error) error {
	switch {
	case errors.Is(err, fs.ErrPermission):
		return fmt.Errorf("%s: %s (izin yok)", what, p)
	case errors.Is(err, syscall.ENOSPC):
		return fmt.Errorf("%s: %s (diskte yer yok)", what, p)
	case errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%s: %s (yolda klasör yerine dosya var)", what, p)
	}
	var pe *fs.PathError
	if errors.As(err, &pe) {
		err = pe.Err
	}
	return fmt.Errorf("%s: %s (%v)", what, p, err)
}

// follow: yönlendirme sonrası adresi benimser; tüm bağlantılar bunu kullanır.
func (d *Download) follow(resp *http.Response, from string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.url = resp.Request.URL.String()
	if first, err := url.Parse(from); err == nil { // sonraki aralık istekleri de yönlendirme kuralına uyar
		stripOnRedirect(d.header, first, resp.Request.URL)
	}
}

// Resume Path+".part.json"dan kaldığı yerden sürdürür (klasik denetimlerle). Durum dosyası yok ya da
// bozuksa, ya da sunucudaki dosya değiştiyse/aralık desteklenmiyorsa 0'dan başlar (Snapshot.Restarted).
func Resume(ctx context.Context, r Request, lim *Limiter) (*Download, error) {
	if r.Path == "" {
		return Start(ctx, r, lim)
	}
	var st State
	b, err := os.ReadFile(r.Path + ".part.json")
	if err != nil || json.Unmarshal(b, &st) != nil || !st.valid() {
		return start(ctx, r, lim, false) // eski .part'ı sıfırlar
	}
	f, err := os.OpenFile(r.Path+".part", os.O_RDWR, 0)
	if err != nil {
		return start(ctx, r, lim, false)
	}
	if fi, err := f.Stat(); err != nil || fi.Size() != st.Size {
		f.Close()
		return start(ctx, r, lim, false)
	}
	d := newDownload(ctx, r, lim)
	d.f, d.size, d.etag, d.lastMod, d.ranged = f, st.Size, st.ETag, st.LastModified, true
	var c *chunk // ilk yarım parça; ilk bağlantı onu sürer, diğerleri sahipsiz kalıp split'ten dağıtılır
	for _, s := range st.Chunks {
		nc := &chunk{start: s.Start, pos: s.Pos, end: s.End, last: time.Now()}
		if nc.rem() > 0 {
			nc.orphan = c != nil
			if c == nil {
				c = nc
			}
		}
		d.chunks = append(d.chunks, nc)
	}
	if c == nil { // hepsi inmiş, yalnız adı konmamış
		go d.supervise()
		return d, nil
	}
	ov := min(64, c.pos-c.start)
	fctx, fcancel := context.WithCancel(d.run)
	resp, err := d.get(fctx, c.pos-ov)
	if err != nil {
		fcancel()
		f.Close()
		d.cancel()
		return nil, err
	}
	reason := ""
	cst, total, ok := contentRange(resp.Header.Get("Content-Range"))
	if resp.StatusCode == http.StatusOK {
		total, ok = resp.ContentLength, true // -1: bilinmiyor
	}
	// Başka içerik (süresi dolan linkin giriş sayfası, başka dosya) yarım dosyayı silmesin: kullanıcı karar verir.
	var refuse error
	switch {
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent:
		refuse = fmt.Errorf("sunucu %s döndü", resp.Status)
	case webPage(resp, r.Path):
		refuse = errors.New("sunucu dosya yerine bir web sayfası döndü (bağlantının süresi dolmuş olabilir); yarım dosya korundu, adresi yenileyin")
	case total < 0: // boyutu bilinen dosyaya boyutsuz 200: çoğu kez hata sayfası/JSON
		refuse = errors.New("sunucu dosya boyutunu bildirmedi, aynı dosya olduğu doğrulanamadı; yarım dosya korundu, adresi yenileyin ya da indirmeyi silip yeniden ekleyin")
	case !ok || total != st.Size:
		refuse = fmt.Errorf("sunucudaki dosyanın boyutu farklı (%d, %d bekleniyordu); yarım dosya korundu, adresi yenileyin ya da indirmeyi silip yeniden ekleyin", total, st.Size)
	}
	if refuse != nil {
		resp.Body.Close()
		fcancel()
		f.Close()
		d.cancel()
		return nil, refuse
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		reason = "sunucu kaldığı yerden devamı desteklemiyor"
	case cst != c.pos-ov:
		reason = "sunucu yanlış aralık gönderdi"
	case changed(st.ETag, resp.Header.Get("ETag")):
		reason = "dosya sunucuda değişti (ETag)"
	// adreste '?' varsa (dinamik bağlantı) tarih değişimi yok sayılır
	case changed(st.LastModified, resp.Header.Get("Last-Modified")) && !strings.Contains(r.URL, "?"):
		reason = "dosya sunucuda değişti (Last-Modified)"
	}
	if reason != "" {
		resp.Body.Close()
		fcancel()
		f.Close()
		d.restarted, d.reason = true, reason
		if err := d.open(); err != nil {
			d.cancel()
			return nil, err
		}
		go d.supervise()
		return d, nil
	}
	d.follow(resp, r.URL)
	c.cancel, c.ctx = fcancel, fctx
	d.active = 1
	d.wg.Add(1)
	go d.worker(c, resp, true)
	go d.supervise()
	return d, nil
}

// webPage: dosya HTML değilken sunucu text/html döndü.
func webPage(resp *http.Response, path string) bool {
	mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	ext := strings.ToLower(filepath.Ext(path))
	return mt == "text/html" && ext != ".html" && ext != ".htm"
}

// valid: parçalar 0'dan Size-1'e sırayla, boşluksuz ve çakışmasız döşenmiş, Start<=Pos<=End+1.
// split/takeNext bu düzeni hep korur; bozuk dosya paniğe ya da delikli dosyaya yol açmasın.
func (st State) valid() bool {
	next := int64(0)
	for _, c := range st.Chunks {
		if c.Start != next || c.End < c.Start || c.Pos < c.Start || c.Pos > c.End+1 {
			return false
		}
		next = c.End + 1
	}
	return st.Size > 0 && len(st.Chunks) > 0 && next == st.Size
}

// changed: iki değer de biliniyorsa ve farklıysa.
func changed(old, cur string) bool { return old != "" && cur != "" && old != cur }

// saveState: durum dosyasını atomik yazar (tmp + rename).
func (d *Download) saveState() error {
	d.saveMu.Lock()
	defer d.saveMu.Unlock()
	d.mu.Lock()
	if d.completed {
		d.mu.Unlock()
		return nil
	}
	st := State{URL: d.url, Size: d.size, ETag: d.etag, LastModified: d.lastMod}
	if d.ranged { // aralıksız indirme sürdürülemez: parça listesi boş = 0'dan
		for _, c := range d.chunks {
			st.Chunks = append(st.Chunks, Seg{Start: c.start, Pos: c.pos, End: c.end})
		}
	}
	p := d.path + ".part.json"
	d.mu.Unlock()
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p+".tmp", b, 0o600); err != nil {
		return err
	}
	return os.Rename(p+".tmp", p)
}

// sameSite: Go http.Client'ın yönlendirmede Cookie/Authorization'ı koruma kuralı (port yok sayılır):
// aynı ad ya da ilk adın alt alanı.
func sameSite(initial, dest string) bool {
	i, d := strings.ToLower(initial), strings.ToLower(dest)
	return d == i || strings.HasSuffix(d, "."+i)
}

type countWriter struct{ d *Download }

func (w countWriter) Write(p []byte) (int, error) {
	if err := w.d.lim.Wait(w.d.ctx, len(p)); err != nil {
		return 0, err
	}
	n, err := w.d.f.Write(p)
	w.d.mu.Lock()
	w.d.written += int64(n)
	w.d.mu.Unlock()
	return n, err
}

// single: aralık desteklemeyen sunucuda tüm gövdeyi tek akışta yazar.
func (d *Download) single(resp *http.Response, cancel context.CancelFunc) {
	defer d.wg.Done()
	defer cancel()
	_, err := io.Copy(countWriter{d}, resp.Body)
	resp.Body.Close()
	if err != nil {
		if d.run.Err() == nil { // duraklatma/iptal hata sayılmaz
			d.fail(err)
		}
		return
	}
	d.mu.Lock()
	d.singleDone = true
	d.mu.Unlock()
}

// supervise: hız ölçer, bağlantı sayısını artırır, durum dosyasını yazar, bitince sonucu belirler.
func (d *Download) supervise() {
	for {
		d.watch()
		if !d.restartFromZero() {
			break
		}
	}
	d.finish()
}

// watch: işçiler bitene kadar döner.
// Ek bağlantı hedefi: = min(seçilen, kalan/10KB, 32); seçilen host istisnasıyla
// sınırlıdır (Manager.hostConns). 2 sn'de bir,
// açık bağlantıların hepsi veri alıyorsa sayı ikiye katlanır. İlk artış beklemesiz.
func (d *Download) watch() {
	stop := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(stop)
	}()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var lastRamp time.Time
	lastSave := time.Now()
	d.mu.Lock()
	prevT, prevGot := time.Now(), d.gotLocked()
	d.mu.Unlock()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
		}
		d.mu.Lock()
		got := d.gotLocked()
		inst := float64(got-prevGot) / time.Since(prevT).Seconds()
		d.speed = 0.7*d.speed + 0.3*inst
		prevT, prevGot = time.Now(), got
		d.mu.Unlock()
		if d.ranged && d.run.Err() == nil {
			d.ramp(&lastRamp)
		}
		// G/Ç döngüyü bloklamasın (L2: Downloads izin istemi cevapsızken rename çekirdekte bekliyor)
		if time.Since(lastSave) >= time.Second && d.saving.CompareAndSwap(false, true) {
			d.saves.Add(1)
			go func() {
				defer d.saves.Done()
				d.saveState() // shortcut: yazılamazsa sessizce geçilir; Pause/hata anında yine denenir
				d.saving.Store(false)
			}()
			lastSave = time.Now()
		}
	}
}

// restartFromZero: içerik değiştiyse (içerik değişimi) her şeyi atıp 0'dan başlar. İşçiler durmuşken çağrılır.
func (d *Download) restartFromZero() bool {
	d.mu.Lock()
	reason := d.pending
	d.pending = ""
	if reason == "" || d.paused || d.ctx.Err() != nil {
		d.mu.Unlock()
		return false
	}
	d.restarted, d.reason, d.err = true, reason, nil
	d.mu.Unlock()
	d.f.Close()
	d.run, d.runCancel = context.WithCancel(d.ctx)
	if err := d.open(); err != nil {
		if d.ctx.Err() == nil { // Pause/iptal sırasında kesilen istek hata değil
			d.fail(err)
		}
		return false
	}
	return true
}

func (d *Download) ramp(lastRamp *time.Time) {
	if time.Since(*lastRamp) < rampEvery {
		return
	}
	d.mu.Lock()
	if d.lim.NearLimit(d.speed) { // sınıra yakınken bağlantı artırılmaz
		d.mu.Unlock()
		return
	}
	target := min(int64(d.maxConn), (d.size-d.gotLocked())/perConnMin, hardMaxConn)
	receiving := 0
	for _, c := range d.chunks {
		if c.rem() > 0 && c.pos > c.start {
			receiving++
		}
	}
	n := d.active
	ok := int64(n) < target && n <= receiving
	d.mu.Unlock()
	if !ok {
		return
	}
	*lastRamp = time.Now()
	for i := n; i < int(min(int64(2*n), target)); i++ {
		d.mu.Lock()
		var c *chunk
		if d.active > 0 { // hepsi bittiyse ekleme yok; active>0 iken wg sayacı da >0, Add güvenli
			c = d.split()
		}
		if c == nil {
			d.mu.Unlock()
			break
		}
		d.active++
		d.wg.Add(1)
		d.mu.Unlock()
		go d.worker(c, nil, false)
	}
}

// finish: tüm işçiler durduktan sonra dosyayı kapatır, tamamsa adını koyar.
func (d *Download) finish() {
	d.saves.Wait() // watch döndü, yeni yazım başlamaz; süren arka plan yazımı done'dan sonra .part.json'a dokunmasın
	d.f.Close()
	d.client.CloseIdleConnections()
	d.mu.Lock()
	got, err, paused := d.gotLocked(), d.err, d.paused
	complete := d.singleDone && (d.size < 0 || d.written == d.size)
	if d.ranged {
		complete = got == d.size
	}
	d.speed = 0
	d.mu.Unlock()
	var final string
	switch {
	case err != nil && !paused: // duraklatmanın kestiği isteklerin hatası ErrPaused'u örtmez
		d.result = err
	case complete:
		final, err = place(d.path, d.overwrite, d.trash)
		d.result = err
		if err == nil && d.serverDate {
			// shortcut: tarih işaretlenemezse indirme yine başarılı sayılır (yalnız süs); bozuk başlık yok sayılır
			if t, perr := http.ParseTime(d.lastMod); perr == nil {
				os.Chtimes(final, t, t)
			}
		}
	case paused:
		d.result = ErrPaused
	case d.ctx.Err() != nil:
		d.result = d.ctx.Err()
	default:
		d.result = fmt.Errorf("eksik indi: %d/%d", got, d.size)
	}
	if complete && d.result == nil {
		d.saveMu.Lock()
		d.mu.Lock()
		d.completed = true
		orig := d.path
		d.path = final
		d.mu.Unlock()
		os.Remove(orig + ".part.json")
		d.saveMu.Unlock()
	} else {
		d.saveState()
	}
	d.cancel()
	close(d.done)
}

// place path+".part"ı path'e koyar ve son yolu döner. overwrite dışında indirme sürerken o adla
// oluşmuş dosya ezilmez, numaralı ad seçilir (Link hedef varsa EEXIST verir; Rename ezerdi).
// overwrite'ta eski dosya trash ile Çöp'e gider; taşınamazsa numaralı ad seçilir.
func place(path string, overwrite bool, trash func(string) error) (string, error) {
	part := path + ".part"
	if overwrite {
		if _, err := os.Lstat(path); err != nil || trash == nil || trash(path) == nil {
			return path, os.Rename(part, path)
		}
	}
	for {
		err := os.Link(part, path)
		if errors.Is(err, os.ErrExist) {
			path = UniquePath(path, "number", nil)
			continue
		}
		if err != nil { // shortcut: sabit bağ desteklemeyen birimde (exFAT, bazı ağ diskleri) denetle-taşı; arada küçük yarış kalır
			if _, serr := os.Lstat(path); serr == nil {
				path = UniquePath(path, "number", nil)
			}
			return path, os.Rename(part, path)
		}
		os.Remove(part) // dosya yerinde; artık .part yalnız fazladan bir bağ
		return path, nil
	}
}

// Pause tüm bağlantıları keser; .part ve .part.json kalır. Wait ErrPaused döndürür.
func (d *Download) Pause() {
	d.mu.Lock()
	d.paused = true
	d.mu.Unlock()
	d.cancel()
}

// Wait indirme bitene (ya da durana) kadar bekler: tamamsa nil, Pause sonrası ErrPaused.
func (d *Download) Wait() error {
	<-d.done
	return d.result
}

// Path son dosya yoludur (Request.Path boşsa sunucudan türetilmiş ad).
func (d *Download) Path() string { return d.path }

// Snapshot anlık ilerlemeyi verir.
func (d *Download) Snapshot() Snapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := Snapshot{Got: d.gotLocked(), Size: d.size, Speed: d.speed, Resumable: d.ranged, Restarted: d.restarted, RestartReason: d.reason, Path: d.path}
	for _, c := range d.chunks {
		s.Conns = append(s.Conns, Seg{c.start, c.pos, c.end, c.status})
	}
	return s
}
