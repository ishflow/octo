package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// Job çalışan bir indirmedir (*Download bunu sağlar).
type Job interface {
	Snapshot() Snapshot
	Pause()
	Wait() error
}

// Deps: Manager'ın dış dünyası. Gerçekleri entegrasyonda (deps_default.go) bağlanır, testler sahte verir.
type Deps struct {
	Start             func(ctx context.Context, r Request, resume bool) (Job, error) // resume=true → .part'tan devam
	Probe             func(ctx context.Context, url string, h http.Header, auth AuthFunc) (ProbeResult, error)
	ProbeStream       func(ctx context.Context, url string, h http.Header, page string, auth AuthFunc) (StreamInfo, error) // HLS/DASH manifest
	Password          func(host string) (string, error)                                                                    // site girişi parolası (Keychain; hesap = Login.Host)
	UniquePath        func(path, mode string, inUse func(string) bool) string
	CategoryFor       func(name string, cats []Category) string
	DefaultCategories func() []Category
	SetLimit          func(bytesPerSec int64)
	Run               func(name string, args ...string) error // open / pmset / osascript
	Trash             func(path string) error
	Quarantine        func(path string) error // bitmiş dosyaya com.apple.quarantine
	Now               func() time.Time
}

const (
	progressEvery = 500 * time.Millisecond
	tickEvery     = 30 * time.Second // kuyruk zamanlayıcısı
	actionDelay   = 35 * time.Second // kapatma geri sayımı
	subBuffer     = 1024
)

// run bir öğenin çalışan indirmesidir. stop: "" | pause | queue | remove | close (neden durduruldu).
type run struct {
	job     Job
	lim     *Limiter // dosya başına hız sınırı; SetItemLimit çalışırken değiştirir
	cancel  context.CancelFunc
	byQueue bool
	stop    string
	done    chan struct{}
	// resume: durdurulurken Resume geldi; koşu kapanınca öğe yeniden başlar (komut kaybolmaz)
	resume bool
	// unquarantined: karantina yazılamadı; kuyruk onDone "open" bu dosyayı açmaz (Gatekeeper sormadan çalışırdı)
	unquarantined bool
}

// Manager indirmeleri, kuyrukları, zamanlayıcıyı ve kalıcı durumu yönetir.
type Manager struct {
	mu       sync.Mutex
	dir      string
	d        Deps
	items    []*Item
	nextID   int
	settings Settings
	queues   []*Queue

	runs      map[string]*run
	prog      map[string]ProgressItem // son gönderilen ilerleme (yalnız değişenler gider)
	retries   map[string]int          // kuyruk deneme sayısı; kaydedilmez
	cool      map[string]bool         // bu turda başarısız olanlar sonraki tick'e kadar beklenir
	ran       map[string]bool         // kuyruk bu çalışmada en az bir öğe başlattı
	cut       map[string]bool         // bu çalışmada kullanıcı bitmemiş öğeyi kuyruktan çıkardı → onDone yok
	removing  map[string]bool         // Remove sürüyor; bu öğeler başlatılmaz
	lastFile  map[string]string       // kuyruğun bu çalışmada biten son dosyası
	lastFired map[string]time.Time    // zamanlayıcı başlangıcı (59 sn çift tetik koruması)

	subs        map[chan Event]struct{}
	actionGen   int
	actionTimer *time.Timer
	actionDelay time.Duration
	actionName  string    // bekleyen geri sayım (sonradan bağlanan uygulama için)
	actionAt    time.Time // geri sayımın biteceği an (gerçek saat; zamanlayıcı da gerçek)

	blocked string // boş değilse indirme başlatılmaz ve durum yazılmaz (eski servis çalışıyor)

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed bool
}

// NewManager veriyi yükler, hız sınırını uygular ve ilerleme/zamanlayıcı döngülerini başlatır.
func NewManager(dataDir string, d Deps) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	m := &Manager{
		dir: dataDir, d: d,
		runs: map[string]*run{}, prog: map[string]ProgressItem{}, retries: map[string]int{},
		cool: map[string]bool{}, ran: map[string]bool{}, cut: map[string]bool{}, removing: map[string]bool{}, lastFile: map[string]string{},
		lastFired: map[string]time.Time{}, subs: map[chan Event]struct{}{}, actionDelay: actionDelay,
	}
	if err := m.load(); err != nil {
		return nil, fmt.Errorf("veri okunamadı: %w", err)
	}
	m.applyLimit()
	m.ctx, m.cancel = context.WithCancel(context.Background())
	m.wg.Add(2)
	go m.every(progressEvery, m.progress)
	go m.every(tickEvery, m.Tick)
	return m, nil
}

func (m *Manager) every(d time.Duration, f func()) {
	defer m.wg.Done()
	t := time.NewTicker(d)
	defer t.Stop()
	for {
		select {
		case <-m.ctx.Done():
			return
		case <-t.C:
			f()
		}
	}
}

// ---- yardımcılar (m.mu tutulurken) ----

func (m *Manager) item(id string) *Item {
	for _, it := range m.items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

func (m *Manager) queue(id string) *Queue {
	for _, q := range m.queues {
		if q.ID == id {
			return q
		}
	}
	return nil
}

func itemPath(it *Item) string { return filepath.Join(it.Dir, it.Name) }

// public: olaylara/listeye giden kopya; Headers asla dışarı çıkmaz, boş dizi null değil [].
func public(it *Item) Item {
	c := *it
	c.Referrer, c.Headers = it.Headers["Referer"], nil
	c.Conns = append([]Conn{}, it.Conns...)
	if it.Stream != nil { // ilerleme yerinde yazılır; kopya paylaşmasın
		s := *it.Stream
		c.Stream = &s
	}
	return c
}

func (m *Manager) emit(e Event) {
	for ch := range m.subs {
		select {
		case ch <- e:
		default: // shortcut: yavaş abone olay kaçırır; gerekirse abone başına kuyruk
		}
	}
}

func (m *Manager) emitItem(it *Item) {
	c := public(it)
	m.emit(Event{Event: "item", Item: &c})
}

func (m *Manager) emitQueues() {
	m.emit(Event{Event: "queues", Queues: m.queueCopies()})
}

func (m *Manager) queueCopies() []Queue {
	out := make([]Queue, len(m.queues))
	for i, q := range m.queues {
		out[i] = *q
		out[i].Items = slices.Clone(q.Items)
		out[i].Schedule.Days = slices.Clone(q.Schedule.Days)
	}
	return out
}

func logErr(what string, err error) {
	if err != nil {
		log.Printf("octo: %s: %v", what, err)
	}
}

func toHeader(h map[string]string) http.Header {
	out := http.Header{}
	for k, v := range h {
		out.Set(k, v)
	}
	return out
}

// cleanName yol parçalarını atar (eklentiden gelen ad dizin dışına yazamasın).
func cleanName(name string) (string, error) {
	n := filepath.Base(filepath.Clean("/" + strings.ReplaceAll(name, "\\", "/")))
	if n == "/" || n == "." {
		return "", errors.New("geçersiz dosya adı")
	}
	return n, nil
}

// checkURL yalnız http/https adresi kabul eder (file:, ftp:, javascript: reddedilir).
func checkURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("yalnız http ve https adresleri indirilebilir")
	}
	return nil
}

// ---- komutlar ----

// splitAuth Authorization başlığını (büyük/küçük harf fark etmez) haritadan ayırır; girdiye dokunmaz.
// Kimlik yalnız bellekte tutulur (Item.Authorization), kalan başlıklar diske yazılabilir.
func splitAuth(h map[string]string) (rest map[string]string, auth string) {
	if h != nil {
		rest = make(map[string]string, len(h))
	}
	for k, v := range h {
		if strings.EqualFold(k, "Authorization") {
			auth = v
		} else {
			rest[k] = v
		}
	}
	return rest, auth
}

// checkPlainAuth parola düz http adresine gitmesin (tarayıcıdaki gibi yalnız şifreli bağlantıda gönderilir).
func checkPlainAuth(rawURL, auth string) error {
	if auth != "" && strings.HasPrefix(strings.ToLower(strings.TrimSpace(rawURL)), "http://") {
		return errors.New("Kullanıcı adı ve parola düz http adresine gönderilmez; https adresi kullanın")
	}
	return nil
}

// Probe sunucudan ad/boyut bilgisini alır.
func (m *Manager) Probe(url string, headers map[string]string) (ProbeResult, error) {
	if _, auth := splitAuth(headers); auth != "" {
		if err := checkPlainAuth(url, auth); err != nil {
			return ProbeResult{}, err
		}
	}
	p, err := m.d.Probe(m.ctx, url, toHeader(headers), m.auth)
	return p, loginHint(err)
}

// ProbeStream akış manifest'ini çözer (stream.probe): kaliteler, sesler, canlı/korumalı.
func (m *Manager) ProbeStream(u string, headers map[string]string, page string) (StreamInfo, error) {
	if err := checkURL(u); err != nil {
		return StreamInfo{}, err
	}
	if _, auth := splitAuth(headers); auth != "" {
		if err := checkPlainAuth(u, auth); err != nil {
			return StreamInfo{}, err
		}
	}
	return m.d.ProbeStream(m.ctx, u, toHeader(headers), page, m.auth)
}

// streamUsable: canlı ve korumalı akış öğe olamaz (K31).
func streamUsable(info StreamInfo) error {
	switch {
	case info.Protected:
		return errors.New(errProtected)
	case info.Live:
		return errors.New(errLive)
	}
	return nil
}

// streamName akış öğesinin adına kaba göre uzantı verir (.ts | .mp4 | yalnız ses .m4a); eklentinin
// önerdiği bilinen uzantı değiştirilir.
func streamName(name string, info StreamInfo) string {
	ext := ".mp4"
	switch {
	case info.Container == "ts":
		ext = ".ts"
	case len(info.Variants) == 0:
		ext = ".m4a"
	}
	if name == "" {
		return "video" + ext
	}
	if slices.Contains([]string{".mp4", ".ts", ".m4a", ".m3u8", ".mpd", ".m4v"}, strings.ToLower(filepath.Ext(name))) {
		name = strings.TrimSuffix(name, filepath.Ext(name))
	}
	return name + ext
}

// auth: host'a uyan site girişinin parolasını okur (spec §8). Parola yalnız istek başlığına gider.
func (m *Manager) auth(host string) (string, string, bool) {
	m.mu.Lock()
	logins := slices.Clone(m.settings.Logins)
	m.mu.Unlock()
	for _, l := range logins {
		if hostMatches(l.Host, host) {
			p, err := m.d.Password(l.Host)
			logErr("site girişi "+l.Host, err)
			return l.User, p, err == nil
		}
	}
	return "", "", false
}

// loginHint: 401'de kullanıcıya nereye bakacağını söyler (giriş yok, parola yanlış ya da Anahtar Zinciri izni verilmedi).
// shortcut: durum kodu hata metninden okunur ("sunucu 401 … döndü"); 401 hataları türlenirse errors.As'e geçilmeli.
func loginHint(err error) error {
	if err == nil || !strings.Contains(err.Error(), " 401 ") {
		return err
	}
	return fmt.Errorf("%w — giriş gerekli ya da parola yanlış (Ayarlar > Site girişleri)", err)
}

// hostConns: öğenin bağlantı sayısı, URL'nin host'una uyan ilk istisnayla sınırlı (m.mu tutulurken).
func (m *Manager) hostConns(it *Item) int {
	n := it.MaxConns
	if u, err := url.Parse(it.URL); err == nil {
		for _, hc := range m.settings.HostConns {
			if hostMatches(hc.Host, u.Hostname()) {
				return min(n, hc.Conns)
			}
		}
	}
	return n
}

// hostMatches: "*.example.com" example.com'u ve alt alanlarını tutar (Swift HostPattern.matches ile aynı).
func hostMatches(pattern, host string) bool {
	p, h := strings.ToLower(pattern), strings.ToLower(host)
	base, wild := strings.CutPrefix(p, "*.")
	return h == base || wild && strings.HasSuffix(h, "."+base)
}

// Add yeni indirme ekler. Start:true kuyruk sınırı dışında hemen başlar; yoksa kuyruğun sonuna girer.
func (m *Manager) Add(a AddArgs) (Item, error) {
	if err := checkURL(a.URL); err != nil {
		return Item{}, err
	}
	if msg := m.blockedMsg(); msg != "" {
		return Item{}, errors.New(msg)
	}
	headers, auth := splitAuth(a.Headers)
	if err := checkPlainAuth(a.URL, auth); err != nil {
		return Item{}, err
	}
	name, size, resumable := a.Name, int64(-1), false
	var stream *StreamSpec
	if a.Stream != nil {
		if a.Stream.Kind != "hls" && a.Stream.Kind != "dash" {
			return Item{}, errors.New("bilinmeyen akış türü")
		}
		info, err := m.ProbeStream(a.URL, a.Headers, a.PageURL)
		if err != nil {
			return Item{}, err
		}
		if err := streamUsable(info); err != nil {
			return Item{}, err
		}
		stream = &StreamSpec{Kind: a.Stream.Kind, Video: a.Stream.Video, Audio: a.Stream.Audio, Label: a.Stream.Label}
		name, resumable = streamName(name, info), true
	} else if name == "" {
		p, err := m.Probe(a.URL, a.Headers)
		if err != nil {
			return Item{}, err
		}
		name, size, resumable = p.Name, p.Size, p.Resumable
	}
	name, err := cleanName(name)
	if err != nil {
		return Item{}, err
	}
	name = sanitizeName(name)

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Item{}, errors.New("servis kapanıyor")
	}
	qid := a.Queue
	if qid == "" {
		qid = mainQueue
	}
	q := m.queue(qid)
	if q == nil {
		return Item{}, errors.New("kuyruk bulunamadı")
	}
	s := m.settings
	cat := a.Category
	if cat == "" {
		cat = m.d.CategoryFor(name, s.Categories)
	}
	dir := expandHome(a.Dir) // açıkça verilen dizin kategori klasörünü ezer
	if dir == "" {
		dir = s.DownloadDir
		if s.CategoryFolders {
			for _, c := range s.Categories {
				if c.Dir == "" && c.Name != "Genel" { // her kategorinin kendi klasörü var; Genel ana klasörde
					c.Dir = sanitizeName(c.Name) // "/" ya da ".." indirme klasörünün dışına çıkmasın
				}
				if c.Name == cat && c.Dir != "" {
					if c.Dir = expandHome(c.Dir); filepath.IsAbs(c.Dir) {
						dir = c.Dir
					} else {
						dir = filepath.Join(dir, c.Dir)
					}
				}
			}
		}
	}
	mode := s.Duplicate
	if mode == "ask" { // "sor" kararını uygulama verir; buraya gelirse numaralandır
		mode = "number"
	}
	var offer *Item // spec §2: aynı ad ve boyuttaki yarım öğe; yeni adres ona "adresi yenile" olarak önerilir
	for _, o := range m.items {
		if a.Size > 0 && o.Size == a.Size && o.State != StateDone && m.runs[o.ID] == nil && itemPath(o) == filepath.Join(dir, name) {
			offer = o
		}
	}
	path := m.d.UniquePath(filepath.Join(dir, name), mode, func(p string) bool {
		for _, it := range m.items {
			if it.State != StateDone && itemPath(it) == p {
				return true
			}
		}
		return false
	})

	m.nextID++
	it := &Item{
		ID: fmt.Sprintf("d%d", m.nextID), URL: a.URL, Name: filepath.Base(path), Dir: filepath.Dir(path),
		Category: cat, Queue: qid, State: StateQueued, Size: size, ETA: -1, Resumable: resumable,
		Description: a.Description, PageURL: a.PageURL, Added: m.d.Now(), MaxConns: s.MaxConns, Conns: []Conn{}, Headers: headers,
		Authorization: auth, AuthNeeded: auth != "", Stream: stream,
	}
	if offer != nil {
		it.RefreshOf, it.State = offer.ID, StatePaused // kuyruk pompası başlatmasın; kararı uygulama verir
	}
	m.items = append(m.items, it)
	q.Items = append(q.Items, it.ID)
	if a.Start && offer == nil {
		m.start(it, false)
	}
	logErr("indirme kaydı", m.saveItems())
	logErr("kuyruk kaydı", m.saveQueues())
	m.emitItem(it)
	m.emitQueues()
	m.pumpAll()
	return public(it), nil
}

// start indirmeyi arka planda başlatır (m.mu tutulurken).
func (m *Manager) start(it *Item, byQueue bool) {
	if m.closed || m.runs[it.ID] != nil || m.removing[it.ID] {
		return
	}
	if m.blocked != "" {
		it.State, it.Error = StateError, m.blocked
		return
	}
	if it.AuthNeeded && it.Authorization == "" { // kimlik yalnız bellekte durur: motor yeniden başladı
		it.State, it.Error = StateError, "Giriş gerekli: kullanıcı adı ve parola motor yeniden başlayınca silinir. İndirmeyi kaldırıp kimlik bilgisiyle yeniden ekleyin."
		return
	}
	ctx, cancel := context.WithCancel(m.ctx)
	r := &run{cancel: cancel, byQueue: byQueue, done: make(chan struct{}), lim: NewLimiter()}
	r.lim.Set(it.SpeedLimitKBps * 1024)
	m.runs[it.ID] = r
	now := m.d.Now()
	it.State, it.Error, it.RestartReason, it.LastTry, it.QuarantineFailed = StateDownloading, "", "", &now, false
	it.RefreshOf = "" // öneri yeni indirme olarak sürüyor
	hd := toHeader(it.Headers)
	if it.Authorization != "" {
		hd.Set("Authorization", it.Authorization)
	}
	it.Note = "" // önceki koşunun notu (ayrı kalan ses)
	req := Request{URL: it.URL, Header: hd, Path: itemPath(it), MaxConns: m.hostConns(it), UserAgent: m.settings.UserAgent, Auth: m.auth, Overwrite: m.settings.Duplicate == "overwrite", ServerDate: m.settings.UseServerDate, Limit: r.lim, Page: it.PageURL}
	if req.Overwrite {
		req.Trash = m.d.Trash // eski dosya ezilmez, Çöp'e gider
	}
	if it.Stream != nil { // kopya: ilerleme it.Stream'e yazılırken iş kendi seçimini okur
		s := *it.Stream
		req.Stream = &s
	}
	_, err := os.Stat(req.Path + ".part")
	m.wg.Add(1)
	go m.runJob(ctx, it.ID, r, req, err == nil)
}

func (m *Manager) runJob(ctx context.Context, id string, r *run, req Request, resume bool) {
	defer m.wg.Done()
	defer close(r.done)
	defer r.cancel()
	job, err := m.d.Start(ctx, req, resume)
	if err == nil {
		m.mu.Lock()
		r.job = job
		stopped := r.stop != ""
		m.mu.Unlock()
		if stopped { // Start sürerken duraklatıldı
			job.Pause()
		}
		err = job.Wait()
	}
	if err == nil {
		if p := job.Snapshot().Path; p != "" { // motor bitişte numaralı ada koymuş olabilir
			req.Path = p
		}
		qerr := m.d.Quarantine(req.Path)
		logErr("karantina", qerr)
		r.unquarantined = qerr != nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finish(id, r, job, err)
}

// finish indirme sonucunu öğeye yazar (m.mu tutulurken).
func (m *Manager) finish(id string, r *run, job Job, err error) {
	if m.runs[id] == r {
		delete(m.runs, id)
	}
	delete(m.prog, id)
	it := m.item(id)
	if it == nil {
		return
	}
	if r.stop == "remove" {
		if err == nil && job != nil { // durdurulamadan bitti (ör. birleştirme): Remove son dosyayı bitmiş öğe gibi ele alsın
			it.State = StateDone
			if p := job.Snapshot().Path; p != "" {
				it.Dir, it.Name = filepath.Dir(p), filepath.Base(p)
			}
		}
		return
	}
	if r.stop == "redownload" { // yazan iş bitti: .part artık atılabilir
		m.restart(it)
		logErr("indirme kaydı", m.saveItems())
		return
	}
	if job != nil {
		m.applySnap(it, job.Snapshot())
		m.applyStream(it, job)
	}
	it.Speed, it.ETA = 0, -1
	queuesChanged := false
	var stale []string
	switch {
	case err == nil:
		now := m.d.Now()
		it.State, it.Finished, it.QuarantineFailed = StateDone, &now, r.unquarantined
		if p := job.Snapshot().Path; p != "" {
			it.Dir, it.Name = filepath.Dir(p), filepath.Base(p)
		}
		// aynı dosyayı gösteren eski "bitti" öğesinin dosyası artık bu; kalsa "dosyayla sil" yenisini atardı
		m.items = slices.DeleteFunc(m.items, func(x *Item) bool {
			if x != it && x.State == StateDone && itemPath(x) == itemPath(it) {
				stale = append(stale, x.ID)
				return true
			}
			return false
		})
		it.Headers, it.Authorization, it.AuthNeeded = nil, "", false // tarayıcı çerezi/kimlik bittikten sonra kalmasın
		if it.Stream != nil {                                        // birleşik dosyanın boyu parçaların toplamından farklıdır
			it.Stream.Phase = ""
			if fi, err := os.Stat(itemPath(it)); err == nil {
				it.Size = fi.Size()
			}
		}
		if it.Size >= 0 {
			it.Got = it.Size
		}
		if q := m.queue(it.Queue); q != nil {
			m.lastFile[q.ID] = itemPath(it)
			if r.unquarantined {
				m.lastFile[q.ID] = ""
			}
			if i := slices.Index(q.Items, id); i >= 0 { // tek seferlik kuyruk: biten öğe kuyruktan çıkar
				q.Items = slices.Delete(q.Items, i, i+1)
				queuesChanged = true
			}
		}
	case r.stop == "queue" || (r.stop == "close" && r.byQueue): // kuyruk sonraki başlatmada sürdürür (klasik yönetici gibi)
		it.State = StateQueued
	case r.stop != "" || errors.Is(err, ErrPaused):
		it.State = StatePaused
	default:
		it.State, it.Error = StateError, loginHint(err).Error()
		if r.byQueue {
			queuesChanged = m.failed(it)
		}
	}
	if r.resume && it.State != StateDone {
		m.start(it, false)
	}
	logErr("indirme kaydı", m.saveItems())
	if len(stale) > 0 {
		m.emit(Event{Event: "removed", IDs: stale})
	}
	m.emitItem(it)
	if queuesChanged {
		logErr("kuyruk kaydı", m.saveQueues())
		m.emitQueues()
	}
	if r.stop != "close" {
		m.pumpAll()
	}
}

// stopRun çalışan indirmeyi durdurur; sonuç finish'te yazılır (m.mu tutulurken).
func (m *Manager) stopRun(r *run, why string) {
	r.stop = why
	if r.job != nil {
		r.job.Pause()
	} else {
		r.cancel()
	}
}

// Pause indirmeleri duraklatır; bekleyenler kuyruk tarafından başlatılmaz.
func (m *Manager) Pause(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	changed := false
	for _, id := range ids {
		it := m.item(id)
		if it == nil {
			continue
		}
		if r := m.runs[id]; r != nil {
			m.stopRun(r, "pause")
			r.resume = false
			continue
		}
		if it.State == StateQueued || it.State == StateError {
			it.State = StatePaused
			changed = true
			m.emitItem(it)
		}
	}
	if changed {
		logErr("indirme kaydı", m.saveItems())
	}
}

// Resume indirmeleri kuyruk sınırı dışında hemen başlatır ("Devam").
func (m *Manager) Resume(ids []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		it := m.item(id)
		if it == nil || it.State == StateDone {
			continue
		}
		if r := m.runs[id]; r != nil {
			r.resume = r.stop != "" && r.stop != "remove" // koşu kapanıyor: bitince yeniden başlat
			continue
		}
		m.start(it, false)
		m.emitItem(it)
	}
	logErr("indirme kaydı", m.saveItems())
}

// Redownload indirmeleri 0'dan yeniden başlatır ("Yeniden indir"). Bitmiş dosyaya dokunulmaz;
// yeni dosya bitişte aynı-ad ayarına göre adlanır (numaralı ya da üzerine yazar).
func (m *Manager) Redownload(ids []string) {
	if m.blockedMsg() != "" { // .part'lara eski servis yazıyor olabilir
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		it := m.item(id)
		if it == nil || m.removing[id] {
			continue
		}
		if r := m.runs[id]; r != nil {
			m.stopRun(r, "redownload") // finish yeniden başlatır
			continue
		}
		m.restart(it)
	}
	logErr("indirme kaydı", m.saveItems())
}

// restart .part'ı atıp öğeyi baştan başlatır (m.mu tutulurken, öğe çalışmıyorken).
func (m *Manager) restart(it *Item) {
	if it.State != StateDone {
		removePartial(itemPath(it))
	}
	it.State, it.Got, it.Conns, it.Finished, it.Speed, it.ETA = StatePaused, 0, []Conn{}, nil, 0, -1
	delete(m.retries, it.ID)
	delete(m.cool, it.ID)
	m.start(it, false)
	m.emitItem(it)
}

// removePartial bitmemiş indirmenin geçici dosyalarını siler: .part/.part.json, akışta parça klasörü
// (yalnız adı p+".octo-parts" olan) ve .stream.json.
func removePartial(p string) {
	for _, f := range []string{p + ".part", p + ".part.json", p + ".stream.json"} {
		if err := os.Remove(f); err != nil && !errors.Is(err, os.ErrNotExist) {
			logErr("silme", err)
		}
	}
	logErr("silme", os.RemoveAll(p+".octo-parts"))
}

// Remove öğeleri listeden çıkarır. Bitmemiş öğenin .part/.part.json'u silinir;
// deleteFile:true bitmiş dosyayı Çöp'e taşır (asla unlink etmez); taşınamayan öğe listede kalır ve hata döner.
func (m *Manager) Remove(ids []string, deleteFile bool) error {
	if msg := m.blockedMsg(); msg != "" { // .part'lara eski servis yazıyor olabilir; silme de kaydedilemez
		return errors.New(msg)
	}
	// 1. aşama (tek kilit): öğeler kuyruklardan çıkar ve "siliniyor" işaretlenir; kilit bırakılınca
	// hiçbir pompa/Resume onları yeniden başlatamaz. Çalışanlar durdurulur.
	m.mu.Lock()
	var waits []chan struct{}
	for _, id := range ids {
		it := m.item(id)
		if it == nil {
			continue
		}
		m.removing[id] = true
		for _, q := range m.queues {
			if i := slices.Index(q.Items, id); i >= 0 {
				q.Items = slices.Delete(q.Items, i, i+1)
				if q.Running && m.runs[id] != nil { // hiç başlamamış öğeyi çıkarmak onDone'u engellemez
					m.cut[q.ID] = true
				}
			}
		}
		if r := m.runs[id]; r != nil {
			m.stopRun(r, "remove")
			waits = append(waits, r.done)
		}
	}
	m.mu.Unlock()
	for _, w := range waits { // .part'ı silmeden önce yazan işin Wait'i dönsün
		<-w
	}

	// 2. aşama: dosyalar ve kayıt.
	m.mu.Lock()
	defer m.mu.Unlock()
	var gone []string
	var errs []error
	for _, id := range ids {
		it := m.item(id)
		if it == nil || !m.removing[id] {
			continue
		}
		delete(m.removing, id)
		p := itemPath(it)
		if it.State == StateDone {
			if deleteFile {
				if err := m.d.Trash(p); err != nil { // öğe kalır: kullanıcı dosyanın silindiğini sanmasın
					logErr("çöpe taşıma", err)
					it.Error = "Çöp'e taşınamadı: " + err.Error()
					m.emitItem(it)
					errs = append(errs, errors.New(it.Error))
					continue
				}
			}
		} else {
			removePartial(p)
		}
		m.items = slices.DeleteFunc(m.items, func(x *Item) bool { return x.ID == id })
		delete(m.retries, id)
		delete(m.cool, id)
		gone = append(gone, id)
	}
	if len(gone) == 0 {
		return errors.Join(errs...)
	}
	logErr("indirme kaydı", m.saveItems())
	logErr("kuyruk kaydı", m.saveQueues())
	m.emit(Event{Event: "removed", IDs: gone})
	m.emitQueues()
	m.pumpAll()
	return errors.Join(errs...)
}

// Refresh süresi dolan indirme adresini yeniler ("adresi yenile").
func (m *Manager) Refresh(id, newURL string) (Item, error) {
	if err := checkURL(newURL); err != nil {
		return Item{}, err
	}
	if msg := m.blockedMsg(); msg != "" { // durum yazılmaz: "ok" dönüp açılışta geri gelmesin
		return Item{}, errors.New(msg)
	}
	m.mu.Lock()
	it := m.item(id)
	var size int64
	var headers map[string]string
	var stream bool
	var auth, page string
	var needs bool
	var offer string // tarayıcıdan gelen öneri: yeni adresin kendi başlıkları (taze çerez) kullanılır, öneri öğesi çıkar
	if it != nil {
		all := maps.Clone(it.Headers)
		if it.Authorization != "" {
			if all == nil {
				all = map[string]string{}
			}
			all["Authorization"] = it.Authorization
		}
		size, stream, page = it.Size, it.Stream != nil, it.PageURL
		headers, auth = splitAuth(carryHeaders(it.URL, newURL, all))
		_, kept := carryHeaders(it.URL, newURL, map[string]string{"Authorization": ""})["Authorization"]
		needs = it.AuthNeeded && kept
		for _, o := range m.items {
			if o.RefreshOf == id && o.URL == newURL && m.runs[o.ID] == nil {
				offer, headers = o.ID, o.Headers
			}
		}
	}
	m.mu.Unlock()
	if it == nil {
		return Item{}, errors.New("indirme bulunamadı")
	}
	probeHeaders := maps.Clone(headers) // kimlik yalnız bellekte: yeni adresin sınanmasına da gider
	if auth != "" {
		if probeHeaders == nil {
			probeHeaders = map[string]string{}
		}
		probeHeaders["Authorization"] = auth
	}
	if stream { // yeni adres de akış olmalı; aynı seçim StartStream'de yeniden planlanır
		info, err := m.ProbeStream(newURL, probeHeaders, page)
		if err == nil {
			err = streamUsable(info)
		}
		if err != nil {
			return Item{}, err
		}
	} else if size >= 0 { // yeni adres aynı boyutu bildirmeli, yoksa devam yanlış dosyayı keser
		p, err := m.Probe(newURL, probeHeaders)
		if err != nil {
			return Item{}, err
		}
		if p.Size < 0 {
			return Item{}, errors.New("yeni adres boyut bildirmiyor; aynı dosya olduğu doğrulanamadı")
		}
		if p.Size != size {
			return Item{}, fmt.Errorf("boyut farklı: yeni adres %d bayt, %d bekleniyordu", p.Size, size)
		}
	}
	m.mu.Lock()
	switch it = m.item(id); {
	case it == nil:
		m.mu.Unlock()
		return Item{}, errors.New("indirme bulunamadı")
	case m.runs[id] != nil:
		m.mu.Unlock()
		return Item{}, errors.New("önce indirmeyi duraklatın")
	}
	it.URL, it.Headers, it.Authorization, it.AuthNeeded = newURL, headers, auth, needs
	logErr("indirme kaydı", m.saveItems())
	m.emitItem(it)
	res := public(it)
	m.mu.Unlock()
	if offer != "" {
		m.Remove([]string{offer}, false)
	}
	return res, nil
}

// Describe öğenin açıklamasını değiştirir (Bilgi paneli).
func (m *Manager) Describe(id, desc string) (Item, error) {
	if msg := m.blockedMsg(); msg != "" { // store yazmaz: "ok" dönüp açılışta kaybolmasın
		return Item{}, errors.New(msg)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.item(id)
	if it == nil {
		return Item{}, errors.New("indirme bulunamadı")
	}
	it.Description = desc
	logErr("indirme kaydı", m.saveItems())
	m.emitItem(it)
	return public(it), nil
}

// carryHeaders: yenilenen adres başka siteyse ya da https'ten http'ye düşüyorsa tarayıcının eski siteye
// verdiği Cookie/Authorization/Referer yeni adrese taşınmaz (yönlendirmedeki follow kuralıyla aynı).
func carryHeaders(from, to string, h map[string]string) map[string]string {
	f, err1 := url.Parse(from)
	t, err2 := url.Parse(strings.TrimSpace(to))
	if err1 == nil && err2 == nil && sameSite(f.Hostname(), t.Hostname()) && !downgraded(f, t) {
		return h
	}
	out := map[string]string{}
	for k, v := range h {
		if !slices.ContainsFunc([]string{"Cookie", "Authorization", "Referer"}, func(s string) bool { return strings.EqualFold(s, k) }) {
			out[k] = v
		}
	}
	return out
}

// Block servisi indirme başlatmayan ve durum yazmayan kipe alır; msg her başlatma denemesinde öğeye yazılır.
func (m *Manager) Block(msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.blocked = msg
}

func (m *Manager) blockedMsg() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.blocked
}

// List tüm öğeleri (başlıksız), kuyrukları ve ayarları verir.
func (m *Manager) List() ([]Item, []Queue, Settings) {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]Item, len(m.items))
	for i, it := range m.items {
		items[i] = public(it)
	}
	return items, m.queueCopies(), m.settings
}

// SetSettings ayarları kaydeder ve hız sınırını uygular.
func (m *Manager) SetSettings(s Settings) error {
	if s.MaxConns < 1 || s.MaxConns > hardMaxConn {
		return fmt.Errorf("bağlantı sayısı 1 ile %d arasında olmalı", hardMaxConn)
	}
	for _, hc := range s.HostConns {
		if hc.Conns < 1 || hc.Conns > hardMaxConn {
			return fmt.Errorf("%s için bağlantı sayısı 1 ile %d arasında olmalı", hc.Host, hardMaxConn)
		}
	}
	if !slices.Contains([]string{"number", "ask", "overwrite"}, s.Duplicate) {
		return errors.New("geçersiz aynı ad seçeneği")
	}
	s.DownloadDir = expandHome(s.DownloadDir)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blocked != "" { // durum yazılmaz: "ok" dönüp açılışta geri gelmesin
		return errors.New(m.blocked)
	}
	m.settings = s
	m.applyLimit()
	return m.saveSettings()
}

// SetItemLimit dosya başına hız sınırını (KB/s; 0 = kapalı) kaydeder; indirme sürüyorsa hemen uygular.
func (m *Manager) SetItemLimit(id string, kbps int64) (Item, error) {
	if kbps < 0 || kbps > math.MaxInt64/1024 {
		return Item{}, errors.New("geçersiz hız sınırı")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.blocked != "" { // durum yazılmaz: "ok" dönüp açılışta geri gelmesin
		return Item{}, errors.New(m.blocked)
	}
	it := m.item(id)
	if it == nil {
		return Item{}, errors.New("indirme bulunamadı")
	}
	it.SpeedLimitKBps = kbps
	if r := m.runs[id]; r != nil {
		r.lim.Set(kbps * 1024)
	}
	logErr("indirme kaydı", m.saveItems())
	m.emitItem(it)
	return public(it), nil
}

func (m *Manager) applyLimit() {
	var bps int64
	if m.settings.SpeedLimitOn {
		bps = m.settings.SpeedLimitKBps * 1024
	}
	m.d.SetLimit(bps)
}

// Subscribe olay kanalı ve aboneliği bitiren fonksiyonu verir.
func (m *Manager) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, subBuffer)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		close(ch)
		return ch, func() {}
	}
	m.subs[ch] = struct{}{}
	if a, sec := m.pendingAction(); a != "" { // geri sayım sürerken bağlanan uygulama pencereyi açabilsin
		ch <- Event{Event: "action", Action: a, Seconds: sec}
	}
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			if _, ok := m.subs[ch]; ok {
				delete(m.subs, ch)
				close(ch)
			}
		})
	}
}

// Close indirmeleri duraklatır (.part kalır), durumu kaydeder ve döngüleri durdurur.
func (m *Manager) Close() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	for _, r := range m.runs {
		m.stopRun(r, "close")
	}
	m.cancelAction()
	m.mu.Unlock()
	m.cancel()
	m.wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	logErr("indirme kaydı", m.saveItems())
	logErr("kuyruk kaydı", m.saveQueues())
	for ch := range m.subs {
		close(ch)
	}
	m.subs = map[chan Event]struct{}{}
}

// ---- ilerleme ----

func (m *Manager) applySnap(it *Item, s Snapshot) {
	it.Got, it.Size, it.Speed, it.Resumable = s.Got, s.Size, BytesPerSec(s.Speed), s.Resumable
	if s.Restarted && it.RestartReason != s.RestartReason {
		it.RestartReason = s.RestartReason
		log.Printf("octo: %s baştan başladı: %s", it.Name, s.RestartReason)
		m.emitItem(it)
	}
	it.Conns = make([]Conn, len(s.Conns))
	for i, c := range s.Conns {
		it.Conns[i] = Conn(c)
	}
	it.ETA = -1
	if s.Speed > 0 && s.Size >= 0 {
		it.ETA = int64(float64(s.Size-s.Got) / s.Speed)
	}
}

// applyStream akış işinin parça ilerlemesini, evresini, ETA'sını ve notunu öğeye yazar.
func (m *Manager) applyStream(it *Item, job Job) {
	sj, ok := job.(interface{ StreamProgress() StreamProgress })
	if !ok || it.Stream == nil {
		return
	}
	p := sj.StreamProgress()
	it.Stream.SegDone, it.Stream.Segments, it.Stream.Phase = p.SegDone, p.Segments, p.Phase
	if p.Note != "" {
		it.Note = p.Note
	}
	if it.ETA < 0 {
		it.ETA = p.ETA
	}
}

// progress çalışan indirmelerin anlık durumunu okur, yalnız değişenleri yayınlar.
func (m *Manager) progress() {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []ProgressItem
	for _, it := range m.items {
		r := m.runs[it.ID]
		if r == nil || r.job == nil {
			continue
		}
		m.applySnap(it, r.job.Snapshot())
		m.applyStream(it, r.job)
		p := ProgressItem{ID: it.ID, State: it.State, Got: it.Got, Size: it.Size, Speed: it.Speed, ETA: it.ETA, Conns: it.Conns}
		if it.Stream != nil {
			p.SegDone, p.Segments, p.Phase = it.Stream.SegDone, it.Stream.Segments, it.Stream.Phase
		}
		if old, ok := m.prog[it.ID]; ok && old.State == p.State && old.Got == p.Got && old.Size == p.Size &&
			old.Speed == p.Speed && old.ETA == p.ETA && slices.Equal(old.Conns, p.Conns) &&
			old.SegDone == p.SegDone && old.Segments == p.Segments && old.Phase == p.Phase {
			continue
		}
		m.prog[it.ID] = p
		out = append(out, p)
	}
	if len(out) > 0 {
		m.emit(Event{Event: "progress", Items: out})
	}
}
