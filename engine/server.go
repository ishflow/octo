package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// ErrAlreadyRunning: soket başka bir servis tarafından yanıtlanıyor.
var ErrAlreadyRunning = errors.New("zaten çalışıyor")

// DataDir veri klasörüdür: $OCTO_DATA_DIR, eski $HIZLI_DATA_DIR ya da ~/Library/Application Support/Octo.
func DataDir() string {
	for _, k := range []string{"OCTO_DATA_DIR", "HIZLI_DATA_DIR"} {
		if d := os.Getenv(k); d != "" {
			return d
		}
	}
	return supportDir("Octo")
}

func supportDir(name string) string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "Application Support", name)
}

// LegacyRunningMsg: eski servis çalışırken Octo indirme başlatmaz; her denemede bu hata görünür.
const LegacyRunningMsg = "Eski Hızlı İndir servisi hâlâ çalışıyor. Aynı dosyalara iki servis yazmasın diye Octo indirme başlatmıyor. " +
	"Eski servisi kapatın (ya da Mac'i yeniden başlatın); kapanınca Octo kendini birkaç saniyede yeniler."

func envDataDir() bool { return os.Getenv("OCTO_DATA_DIR") != "" || os.Getenv("HIZLI_DATA_DIR") != "" }

// LegacyEngineRunning: eski HizliIndir servisi soketinde yanıt veriyor (veri klasörü env ile verildiyse bakılmaz).
func LegacyEngineRunning() bool {
	if envDataDir() {
		return false
	}
	c, err := net.DialTimeout("unix", filepath.Join(supportDir("HizliIndir"), "engine.sock"), time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// MigrateLegacyDataDir: Octo klasöründe durum dosyası yoksa eski HizliIndir klasöründekileri kopyalar
// (taşımaz: eski klasör geri dönüş yolu olarak kalır). Veri klasörü env ile verildiyse bir şey yapmaz.
// Octo klasöründe yalnız soket olabilir: eski servis çalışırken açılan Octo durum yazmaz (Block).
func MigrateLegacyDataDir() error {
	if envDataDir() {
		return nil
	}
	dst, src := DataDir(), supportDir("HizliIndir")
	for _, f := range []string{downloadsFile, settingsFile, queuesFile} {
		if _, err := os.Stat(filepath.Join(dst, f)); !errors.Is(err, os.ErrNotExist) {
			return nil
		}
	}
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	tmp, err := os.MkdirTemp(filepath.Dir(dst), ".Octo-migrate-") // 0700; yarım kopya Octo klasörü gibi görünmesin
	if err != nil {
		return err
	}
	for _, f := range []string{downloadsFile, settingsFile, queuesFile} {
		b, err := os.ReadFile(filepath.Join(src, f))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err == nil {
			err = os.WriteFile(filepath.Join(tmp, f), b, 0o600)
		}
		if err != nil {
			os.RemoveAll(tmp)
			return err
		}
	}
	if err := os.Rename(tmp, dst); err == nil {
		return nil
	}
	// Octo klasörü var (yalnız soketle): dosyalar tek tek taşınır.
	// shortcut: tek tek taşıma atomik değil; yarıda kalırsa sonraki açılış göçü tekrarlamaz (eski klasör yerinde kalır).
	if err := os.MkdirAll(dst, 0o700); err != nil {
		return err
	}
	// downloads.json önce: göç "durum dosyası var mı"ya bakar; yarıda kalırsa indirme listesi kaybolmuş görünmesin.
	for _, f := range []string{downloadsFile, queuesFile, settingsFile} {
		if err := os.Rename(filepath.Join(tmp, f), filepath.Join(dst, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.Remove(tmp)
}

func SockPath() string { return filepath.Join(DataDir(), "engine.sock") }

// Listen soketi açar. Yol varsa: bağlanılabiliyorsa ErrAlreadyRunning, değilse eski dosya silinir.
func Listen(sockPath string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(sockPath), 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(filepath.Dir(sockPath), 0o700); err != nil { // dizin önceden 0755 olabilir
		return nil, err
	}
	if _, err := os.Lstat(sockPath); err == nil {
		if c, err := net.Dial("unix", sockPath); err == nil {
			c.Close()
			return nil, ErrAlreadyRunning
		}
		os.Remove(sockPath)
	}
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// Serve soketi açıp ctx bitene kadar istemcilere hizmet verir.
func Serve(ctx context.Context, b Backend, sockPath string) error {
	ln, err := Listen(sockPath)
	if err != nil {
		return err
	}
	return ServeListener(ctx, b, ln)
}

// shutdownGrace: kapanışta Close ve açık oturumların bitmesi için beklenen en uzun süre.
var shutdownGrace = 10 * time.Second

// ServeListener hazır bir dinleyici üzerinde Serve'in işini yapar (main önce Listen eder, sonra Yönetici kurar).
func ServeListener(ctx context.Context, b Backend, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx) // "shutdown" komutu SIGTERM ile aynı yoldan kapatır
	defer cancel()
	var wg sync.WaitGroup
	stop := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-stop:
		}
		ln.Close()
	}()
	var conns sync.Map
	var lastHello atomic.Int64 // native host'in son hello zamanı (unix sn); bellekte, diske yazılmaz
	for {
		c, err := ln.Accept()
		if err != nil {
			close(stop)
			conns.Range(func(k, _ any) bool { k.(net.Conn).Close(); return true })
			// Süren Add/Probe Yönetici bağlamıyla çalışır: önce o kesilmezse wg.Wait Probe'u (~60 sn) bekler. Close tekrar çağrılabilir.
			// Dosya G/Ç'sinde takılan bir gözetim döngüsü (Downloads izin istemi) Close'u sonsuza dek tutabilir; yanıt "ok" gitmişken
			// süreç asılı kalırsa uygulamanın başlattığı yeni motor aynı .part'a yazar. Süre dolunca dön: main'deki ikinci Close anında biter.
			drained := make(chan struct{})
			go func() {
				defer close(drained)
				if c, ok := b.(interface{ Close() }); ok {
					c.Close()
				}
				wg.Wait()
			}()
			select {
			case <-drained:
			case <-time.After(shutdownGrace):
				log.Print("octo: kapanış ", shutdownGrace, " içinde bitmedi, takılan işler bırakılıyor")
			}
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		conns.Store(c, struct{}{})
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer conns.Delete(c)
			defer c.Close()
			(&session{b: b, c: c, shutdown: cancel, lastHello: &lastHello}).run()
		}()
	}
}

type session struct {
	b      Backend
	c      net.Conn
	mu     sync.Mutex // satır yazımları bölünmesin
	unsub  func()
	events <-chan Event // subscribe yanıtı yazılınca forward başlar (olay yanıttan önce gitmesin)
	// shutdown servisi durdurur; stopping: yanıt yazıldıktan sonra çağrılacak.
	shutdown  func()
	stopping  bool
	lastHello *atomic.Int64 // bağlantılar arası ortak: host kısa ömürlü, uygulama başka bağlantıda
}

type request struct {
	ID         json.RawMessage   `json:"id"`
	Cmd        string            `json:"cmd"`
	URL        string            `json:"url"`
	Headers    map[string]string `json:"headers"`
	PageURL    string            `json:"pageUrl"` // stream.probe: kaynak sayfa (yerel ağ kuralı)
	IDs        []string          `json:"ids"`
	DeleteFile bool              `json:"deleteFile"`
	Queue      json.RawMessage   `json:"queue"`
	Settings   *Settings         `json:"settings"`
	Dir        string            `json:"dir"`  // move
	Name       string            `json:"name"` // move
	KBps       *int64            `json:"kbps"`
	Desc       *string           `json:"description"`
}

// writeTimeout: okumayan bir istemci yazmayı sonsuza dek tıkamasın.
var writeTimeout = 5 * time.Second

func (s *session) send(v any) error {
	line, err := json.Marshal(v)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.SetWriteDeadline(time.Now().Add(writeTimeout))
	_, err = s.c.Write(append(line, '\n'))
	return err
}

func (s *session) run() {
	defer func() {
		if s.unsub != nil {
			s.unsub()
		}
	}()
	sc := bufio.NewScanner(s.c)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		if s.send(s.handle(sc.Bytes())) != nil {
			return
		}
		if s.events != nil {
			go s.forward(s.events)
			s.events = nil
		}
		if s.stopping {
			s.shutdown()
			return
		}
	}
}

func fail(id json.RawMessage, msg string) map[string]any {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	return map[string]any{"id": id, "ok": false, "error": msg}
}

func (s *session) handle(line []byte) map[string]any {
	var req request
	if err := json.Unmarshal(line, &req); err != nil {
		return fail(nil, "Geçersiz istek: JSON okunamadı")
	}
	res := map[string]any{"id": req.ID, "ok": true}
	if len(req.ID) == 0 {
		res["id"] = nil
	}
	// Sözleşmede hem istek kimliği hem hedef kimliği "id" adını taşır (refresh, queue.*). Hedef: string olan "id", "queue" ya da "item".
	var raw map[string]json.RawMessage
	json.Unmarshal(line, &raw)
	target := func() string {
		for _, k := range []string{"id", "queue", "item"} {
			var v string
			if json.Unmarshal(raw[k], &v) == nil && v != "" {
				return v
			}
		}
		return ""
	}
	switch req.Cmd {
	case "pause", "resume", "remove", "redownload":
		if len(req.IDs) == 0 { // kimliksiz komut hiçbir şey yapmadan "ok" dönmesin
			return fail(req.ID, "Geçersiz istek: ids eksik")
		}
		log.Printf("octo: komut %s %v", req.Cmd, req.IDs) // canlı tanı: duraklatma/devam kimden geldi
	case "shutdown", "queue.start", "queue.stop", "action.cancel":
		log.Printf("octo: komut %s %s", req.Cmd, target())
	}
	var err error
	switch req.Cmd {
	case "add":
		var a AddArgs
		if err = json.Unmarshal(line, &a); err != nil {
			return fail(req.ID, "Geçersiz istek: add")
		}
		var it Item
		if it, err = s.b.Add(a); err == nil {
			res["item"] = cleanItem(it)
		}
	case "probe":
		var p ProbeResult
		if p, err = s.b.Probe(req.URL, req.Headers); err == nil {
			res["name"], res["size"], res["resumable"] = p.Name, p.Size, p.Resumable
			if p.Kind != "" { // eski uygulama alanı yok sayar
				res["kind"] = p.Kind
			}
		}
	case "stream.probe":
		var info StreamInfo
		if info, err = s.b.ProbeStream(req.URL, req.Headers, req.PageURL); err == nil {
			info.Variants, info.Audios = nonNil(info.Variants), nonNil(info.Audios)
			res["info"] = info
		}
	case "pause":
		s.b.Pause(req.IDs)
	case "resume":
		s.b.Resume(req.IDs)
	case "remove":
		err = s.b.Remove(req.IDs, req.DeleteFile)
	case "redownload":
		s.b.Redownload(req.IDs)
	case "refresh":
		var it Item
		if it, err = s.b.Refresh(target(), req.URL); err == nil {
			res["item"] = cleanItem(it)
		}
	case "move":
		var it Item
		if it, err = s.b.Move(target(), req.Dir, req.Name); err == nil {
			res["item"] = cleanItem(it)
		}
	case "item.limit":
		if req.KBps == nil {
			return fail(req.ID, "Geçersiz istek: kbps eksik")
		}
		var it Item
		if it, err = s.b.SetItemLimit(target(), *req.KBps); err == nil {
			res["item"] = cleanItem(it)
		}
	case "item.describe":
		if req.Desc == nil {
			return fail(req.ID, "Geçersiz istek: description eksik")
		}
		var it Item
		if it, err = s.b.Describe(target(), *req.Desc); err == nil {
			res["item"] = cleanItem(it)
		}
	case "list":
		items, queues, set := s.b.List()
		out := make([]Item, len(items))
		for i, it := range items {
			out[i] = cleanItem(it)
		}
		qs := make([]Queue, len(queues))
		for i, q := range queues {
			qs[i] = cleanQueue(q)
		}
		res["items"], res["queues"], res["settings"] = out, qs, cleanSettings(set)
		if a, sec := s.b.PendingAction(); a != "" { // geri sayım sürerken açılan uygulama pencereyi gösterebilsin
			res["pendingAction"] = map[string]any{"action": a, "seconds": sec}
		}
		if t := s.lastHello.Load(); t != 0 {
			res["lastHello"] = t
		}
	case "subscribe":
		if s.unsub == nil {
			s.events, s.unsub = s.b.Subscribe()
		}
	case "settings.set":
		if req.Settings == nil {
			return fail(req.ID, "Geçersiz istek: settings eksik")
		}
		err = s.b.SetSettings(*req.Settings)
	case "queue.save":
		var q Queue
		if json.Unmarshal(req.Queue, &q) != nil || q.ID == "" {
			return fail(req.ID, "Geçersiz istek: queue eksik")
		}
		err = s.b.SaveQueue(q)
	case "queue.delete":
		err = s.b.DeleteQueue(target())
	case "queue.start":
		s.b.StartQueue(target())
	case "queue.stop":
		s.b.StopQueue(target())
	case "queue.move":
		var q string
		json.Unmarshal(req.Queue, &q)
		err = s.b.MoveToQueue(req.IDs, q)
	case "action.cancel":
		s.b.CancelAction()
	case "hello":
		s.lastHello.Store(time.Now().Unix())
	case "shutdown":
		s.stopping = true
	default:
		return fail(req.ID, "Bilinmeyen komut: "+req.Cmd)
	}
	if err != nil {
		return fail(req.ID, err.Error())
	}
	return res
}

func (s *session) forward(ch <-chan Event) {
	for ev := range ch {
		if s.send(encodeEvent(ev)) != nil {
			s.c.Close() // takılan abone düşürülür; run() unsub'ı çağırır
			return
		}
	}
}

func encodeEvent(ev Event) any {
	m := map[string]any{"event": ev.Event}
	switch ev.Event {
	case "progress":
		ps := make([]ProgressItem, len(ev.Items))
		for i, p := range ev.Items {
			if p.Conns == nil {
				p.Conns = []Conn{}
			}
			ps[i] = p
		}
		m["items"] = ps
	case "item":
		if ev.Item != nil {
			m["item"] = cleanItem(*ev.Item)
		}
	case "removed":
		m["ids"] = nonNil(ev.IDs)
	case "queues":
		qs := make([]Queue, len(ev.Queues))
		for i, q := range ev.Queues {
			qs[i] = cleanQueue(q)
		}
		m["queues"] = qs
	case "action":
		m["action"], m["seconds"] = ev.Action, ev.Seconds
	}
	return m
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

// cleanItem Headers'ı (Cookie vb.) soketten dışarı vermez, nil dilimleri [] yapar.
func cleanItem(it Item) Item {
	it.Headers = nil
	it.Conns = nonNil(it.Conns)
	return it
}

func cleanQueue(q Queue) Queue {
	q.Items = nonNil(q.Items)
	q.Schedule.Days = nonNil(q.Schedule.Days)
	return q
}

func cleanSettings(s Settings) Settings {
	cats := make([]Category, len(s.Categories))
	for i, c := range s.Categories {
		c.Exts = nonNil(c.Exts)
		cats[i] = c
	}
	s.Categories = cats
	s.CaptureExts = nonNil(s.CaptureExts)
	s.ExcludedSites = nonNil(s.ExcludedSites)
	s.Logins = nonNil(s.Logins)
	s.HostConns = nonNil(s.HostConns)
	return s
}
