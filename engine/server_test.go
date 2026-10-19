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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeBackend struct {
	mu      sync.Mutex
	subs    []chan Event
	added   []AddArgs
	paused  []string
	redone  []string
	moved   string
	unsubs  atomic.Int32
	pending string // bekleyen geri sayım
	rmErr   error  // Remove'un döneceği hata
}

func (f *fakeBackend) emit(ev Event) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.subs {
		c <- ev
	}
}
func (f *fakeBackend) Add(a AddArgs) (Item, error) {
	f.mu.Lock()
	f.added = append(f.added, a)
	f.mu.Unlock()
	it := Item{ID: "d1", URL: a.URL, State: StateQueued, Headers: map[string]string{"Cookie": "s=1"}}
	f.emit(Event{Event: "item", Item: &it})
	return it, nil
}
func (f *fakeBackend) Probe(url string, h map[string]string) (ProbeResult, error) {
	if strings.HasSuffix(url, ".m3u8") {
		return ProbeResult{Name: "m.m3u8", Size: 5, Kind: "hls"}, nil
	}
	return ProbeResult{Name: "x.iso", Size: 5, Resumable: true}, nil
}
func (f *fakeBackend) ProbeStream(url string, h map[string]string, page string) (StreamInfo, error) {
	if strings.HasSuffix(url, "/err") || (page != "" && page != "https://x/izle") {
		return StreamInfo{}, errors.New(errLive)
	}
	return StreamInfo{Kind: "hls", Container: "ts", Protected: h["Cookie"] == "drm"}, nil // nil dilimler [] yazılmalı
}
func (f *fakeBackend) Pause(ids []string) {
	f.mu.Lock()
	f.paused = append(f.paused, ids...)
	f.mu.Unlock()
	f.emit(Event{Event: "item", Item: &Item{ID: ids[0], State: StatePaused}})
}
func (f *fakeBackend) Resume(ids []string) {}
func (f *fakeBackend) Redownload(ids []string) {
	f.mu.Lock()
	f.redone = append(f.redone, ids...)
	f.mu.Unlock()
}
func (f *fakeBackend) Remove(ids []string, deleteFile bool) error {
	if f.rmErr != nil {
		return f.rmErr
	}
	f.emit(Event{Event: "removed", IDs: ids})
	return nil
}
func (f *fakeBackend) Refresh(id, url string) (Item, error) { return Item{ID: id, URL: url}, nil }
func (f *fakeBackend) Move(id, dir, name string) (Item, error) {
	return Item{ID: id, Dir: dir, Name: name, Headers: map[string]string{"Cookie": "s=1"}}, nil
}
func (f *fakeBackend) SetItemLimit(id string, kbps int64) (Item, error) {
	if id != "d1" {
		return Item{}, os.ErrNotExist
	}
	return Item{ID: id, SpeedLimitKBps: kbps}, nil
}
func (f *fakeBackend) Describe(id, d string) (Item, error) {
	if id != "d1" {
		return Item{}, errors.New("indirme bulunamadı")
	}
	return Item{ID: id, Description: d}, nil
}
func (f *fakeBackend) List() ([]Item, []Queue, Settings) {
	return []Item{{ID: "d1", Headers: map[string]string{"Cookie": "s=1"}}}, nil, Settings{}
}
func (f *fakeBackend) SetSettings(Settings) error  { return nil }
func (f *fakeBackend) SaveQueue(Queue) error       { return nil }
func (f *fakeBackend) DeleteQueue(id string) error { return os.ErrNotExist }
func (f *fakeBackend) StartQueue(id string)        {}
func (f *fakeBackend) StopQueue(id string)         {}
func (f *fakeBackend) CancelAction()               {}
func (f *fakeBackend) MoveToQueue(ids []string, q string) error {
	f.moved = q
	return nil
}
func (f *fakeBackend) PendingAction() (string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending == "" {
		return "", 0
	}
	return f.pending, 20
}
func (f *fakeBackend) Subscribe() (<-chan Event, func()) {
	c := make(chan Event, 16)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.pending != "" { // Manager gibi: geri sayım sürerken yeni aboneye action olayı
		c <- Event{Event: "action", Action: f.pending, Seconds: 20}
	}
	f.subs = append(f.subs, c)
	return c, func() { f.unsubs.Add(1) }
}

// sockIn kısa yol verir: macOS sun_path sınırı 104 bayt, t.TempDir() uzun kalır.
func sockIn(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "hi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "e.sock")
}

func startServer(t *testing.T, b Backend) (string, context.CancelFunc, chan error) {
	t.Helper()
	sock := sockIn(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	go func() { done <- ServeListener(ctx, b, ln) }()
	t.Cleanup(cancel)
	return sock, cancel, done
}

type cli struct {
	c net.Conn
	r *bufio.Reader
}

func dial(t *testing.T, sock string) *cli {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return &cli{c, bufio.NewReader(c)}
}

func (c *cli) line(t *testing.T) string {
	t.Helper()
	c.c.SetReadDeadline(time.Now().Add(3 * time.Second))
	s, err := c.r.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (c *cli) call(t *testing.T, s string) map[string]any {
	t.Helper()
	if _, err := c.c.Write([]byte(s + "\n")); err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(c.line(t)), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAddSubscribePause(t *testing.T) {
	fb := &fakeBackend{}
	sock, _, _ := startServer(t, fb)
	c := dial(t, sock)
	if r := c.call(t, `{"id":1,"cmd":"subscribe"}`); r["ok"] != true {
		t.Fatal(r)
	}
	// Gerçek bir progress olayı soketten gelmeli (conns nil -> []).
	fb.emit(Event{Event: "progress", Items: []ProgressItem{{ID: "d1", State: StateDownloading, Got: 10, Size: 100}}})
	var pe map[string]any
	if err := json.Unmarshal([]byte(c.line(t)), &pe); err != nil || pe["event"] != "progress" {
		t.Fatal("progress gelmedi:", pe, err)
	}
	pi := pe["items"].([]any)[0].(map[string]any)
	if pi["id"] != "d1" || pi["got"] != float64(10) || len(pi["conns"].([]any)) != 0 {
		t.Fatal(pi)
	}
	// item olayı yanıttan önce gelebilir; id'li satırı bekle, olayı sakla.
	c.c.Write([]byte(`{"id":2,"cmd":"add","url":"http://x/a.iso","headers":{"Cookie":"s=1"},"start":true}` + "\n"))
	var r, ev map[string]any
	for r == nil {
		var m map[string]any
		json.Unmarshal([]byte(c.line(t)), &m)
		if m["event"] == "item" {
			ev = m
		} else {
			r = m
		}
	}
	if r["ok"] != true || r["id"] != float64(2) || r["item"].(map[string]any)["id"] != "d1" {
		t.Fatal(r)
	}
	if fb.added[0].URL != "http://x/a.iso" || !fb.added[0].Start || fb.added[0].Headers["Cookie"] != "s=1" {
		t.Fatalf("%+v", fb.added[0])
	}
	// add yanıtı ile item olayı iki ayrı satır; sırası değişebilir.
	for ev == nil {
		var m map[string]any
		json.Unmarshal([]byte(c.line(t)), &m)
		if m["event"] == "item" {
			ev = m
		}
	}
	if ev == nil {
		t.Fatal("item olayı gelmedi")
	}
	c.c.Write([]byte(`{"id":3,"cmd":"pause","ids":["d1"]}` + "\n"))
	gotPaused, gotReply := false, false
	for i := 0; i < 2; i++ {
		var m map[string]any
		json.Unmarshal([]byte(c.line(t)), &m)
		if m["event"] == "item" && m["item"].(map[string]any)["state"] == "paused" {
			gotPaused = true
		}
		if m["id"] == float64(3) && m["ok"] == true {
			gotReply = true
		}
	}
	if !gotPaused || !gotReply || len(fb.paused) != 1 {
		t.Fatal(gotPaused, gotReply, fb.paused)
	}
}

func TestMalformedKeepsConnection(t *testing.T) {
	sock, _, _ := startServer(t, &fakeBackend{})
	c := dial(t, sock)
	r := c.call(t, `{not json`)
	if r["ok"] != false || r["error"] == "" {
		t.Fatal(r)
	}
	r = c.call(t, `{"id":5,"cmd":"nope"}`)
	if r["ok"] != false || r["id"] != float64(5) {
		t.Fatal(r)
	}
	if r = c.call(t, `{"id":6,"cmd":"probe","url":"http://x"}`); r["ok"] != true || r["name"] != "x.iso" || r["resumable"] != true {
		t.Fatal(r)
	}
	if r = c.call(t, `{"id":7,"cmd":"queue.delete","queue":"zz"}`); r["ok"] != false {
		t.Fatal(r)
	}
	r = c.call(t, `{"id":8,"cmd":"move","item":"d3","dir":"/x","name":"y.zip"}`)
	if it, _ := r["item"].(map[string]any); r["ok"] != true || it["id"] != "d3" || it["dir"] != "/x" || it["name"] != "y.zip" || it["headers"] != nil {
		t.Fatal(r)
	}
}

func TestEmptySlicesAndHeadersStripped(t *testing.T) {
	sock, _, _ := startServer(t, &fakeBackend{})
	c := dial(t, sock)
	c.c.Write([]byte(`{"id":1,"cmd":"list"}` + "\n"))
	s := c.line(t)
	if strings.Contains(strings.NewReplacer(`"finished":null`, "", `"lastTry":null`, "").Replace(s), "null") {
		t.Fatal("null var: ", s)
	}
	for _, bad := range []string{"Cookie", "headers"} {
		if strings.Contains(s, bad) {
			t.Fatalf("%q içeriyor: %s", bad, s)
		}
	}
	if !strings.Contains(s, `"queues":[]`) || !strings.Contains(s, `"captureExts":[]`) || !strings.Contains(s, `"conns":[]`) {
		t.Fatal(s)
	}
	b, _ := json.Marshal(encodeEvent(Event{Event: "removed"}))
	if string(b) != `{"event":"removed","ids":[]}` {
		t.Fatal(string(b))
	}
	b, _ = json.Marshal(encodeEvent(Event{Event: "progress"}))
	if string(b) != `{"event":"progress","items":[]}` {
		t.Fatal(string(b))
	}
}

func TestStaleSocketReplacedAndSecondServeDetected(t *testing.T) {
	sock := sockIn(t)
	// Eski dosya: dinleyen yok.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if _, err := os.Lstat(sock); err != nil {
		t.Fatal("eski soket dosyası yok")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, &fakeBackend{}, sock) }()
	var c net.Conn
	for i := 0; i < 100 && c == nil; i++ {
		c, _ = net.Dial("unix", sock)
		time.Sleep(10 * time.Millisecond)
	}
	if c == nil {
		t.Fatal("eski soket değiştirilemedi")
	}
	c.Close()
	if fi, _ := os.Stat(sock); fi.Mode().Perm() != 0o600 {
		t.Fatal(fi.Mode())
	}
	if err := Serve(ctx, &fakeBackend{}, sock); err != ErrAlreadyRunning {
		t.Fatal("ikinci serve:", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestStuckSubscriberDropped(t *testing.T) {
	old := writeTimeout
	writeTimeout = 150 * time.Millisecond
	defer func() { writeTimeout = old }()
	fb := &fakeBackend{}
	sock, _, _ := startServer(t, fb)
	c := dial(t, sock)
	if r := c.call(t, `{"id":1,"cmd":"subscribe"}`); r["ok"] != true {
		t.Fatal(r)
	}
	// İstemci okumuyor: soket tamponu dolana kadar büyük olaylar yolla.
	big := &Item{ID: "d1", Description: strings.Repeat("x", 1<<20)}
	for i := 0; i < 8; i++ {
		fb.emit(Event{Event: "item", Item: big})
	}
	deadline := time.Now().Add(5 * time.Second)
	for fb.unsubs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if fb.unsubs.Load() != 1 {
		t.Fatal("takılan abone düşürülmedi / unsub çağrılmadı")
	}
}

func TestListenTightensExistingDirMode(t *testing.T) {
	sock := sockIn(t)
	dir := filepath.Dir(sock)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Fatal(fi.Mode())
	}
}

func TestShutdownCommandRepliesThenStops(t *testing.T) {
	sock, _, done := startServer(t, &fakeBackend{})
	c := dial(t, sock)
	if r := c.call(t, `{"id":7,"cmd":"shutdown"}`); r["ok"] != true || r["id"] != float64(7) {
		t.Fatal(r)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown sonrası servis durmadı")
	}
	if _, err := os.Lstat(sock); err == nil {
		t.Fatal("soket dosyası kaldı")
	}
}

func TestDataDirEnvAndDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCTO_DATA_DIR", "")
	t.Setenv("HIZLI_DATA_DIR", "")
	if got, want := DataDir(), filepath.Join(home, "Library", "Application Support", "Octo"); got != want {
		t.Fatalf("varsayılan: %s, beklenen %s", got, want)
	}
	t.Setenv("HIZLI_DATA_DIR", "/eski")
	if got := DataDir(); got != "/eski" {
		t.Fatal("eski env okunmadı:", got)
	}
	t.Setenv("OCTO_DATA_DIR", "/yeni")
	if got := DataDir(); got != "/yeni" {
		t.Fatal("OCTO_DATA_DIR öncelikli değil:", got)
	}
}

func TestMigrateLegacyDataDirCopies(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCTO_DATA_DIR", "")
	t.Setenv("HIZLI_DATA_DIR", "")
	old := filepath.Join(home, "Library", "Application Support", "HizliIndir")
	os.MkdirAll(old, 0o700)
	for _, f := range []string{downloadsFile, settingsFile, queuesFile} {
		os.WriteFile(filepath.Join(old, f), []byte(`{"f":"`+f+`"}`), 0o600)
	}
	os.WriteFile(filepath.Join(old, "engine.sock"), nil, 0o600)
	if err := MigrateLegacyDataDir(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{downloadsFile, settingsFile, queuesFile} {
		if b, err := os.ReadFile(filepath.Join(DataDir(), f)); err != nil || string(b) != `{"f":"`+f+`"}` {
			t.Fatalf("%s kopyalanmadı: %v %s", f, err, b)
		}
		if _, err := os.Stat(filepath.Join(old, f)); err != nil {
			t.Fatalf("eski %s taşındı (kopyalanmalıydı): %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(DataDir(), "engine.sock")); err == nil {
		t.Fatal("soket kopyalanmamalı")
	}
	if fi, _ := os.Stat(DataDir()); fi.Mode().Perm() != 0o700 {
		t.Fatal(fi.Mode())
	}
	// Octo klasörü artık var: ikinci çağrı eski veriyle üzerine yazmaz.
	os.WriteFile(filepath.Join(old, settingsFile), []byte(`{}`), 0o600)
	if err := MigrateLegacyDataDir(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(DataDir(), settingsFile)); string(b) == `{}` {
		t.Fatal("var olan Octo klasörünün üzerine yazıldı")
	}
}

func TestMigrateLegacyDataDirSkips(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCTO_DATA_DIR", "")
	t.Setenv("HIZLI_DATA_DIR", "")
	if err := MigrateLegacyDataDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(DataDir()); err == nil {
		t.Fatal("eski klasör yokken Octo klasörü açılmamalı")
	}
	// Env ile verilen klasörde göç yapılmaz.
	old := filepath.Join(home, "Library", "Application Support", "HizliIndir")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, settingsFile), []byte(`{}`), 0o600)
	t.Setenv("OCTO_DATA_DIR", filepath.Join(home, "ozel"))
	if err := MigrateLegacyDataDir(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "ozel")); err == nil {
		t.Fatal("env klasörüne göç yapıldı")
	}
}

// subscribe yanıtı olaylardan önce gelir; bekleyen geri sayım list'te ve ilk olayda görünür.
func TestSubscribeReplyFirstAndPendingAction(t *testing.T) {
	fb := &fakeBackend{pending: "shutdown"}
	sock, _, _ := startServer(t, fb)
	c := dial(t, sock)
	l := c.call(t, `{"id":1,"cmd":"list"}`)
	if pa, _ := l["pendingAction"].(map[string]any); pa["action"] != "shutdown" || pa["seconds"] != float64(20) {
		t.Fatalf("pendingAction=%v", l["pendingAction"])
	}
	for i := 0; i < 20; i++ {
		c := dial(t, sock)
		if r := c.call(t, `{"id":2,"cmd":"subscribe"}`); r["id"] != float64(2) || r["ok"] != true {
			t.Fatalf("ilk satır yanıt değil: %v", r)
		}
		if ev := c.line(t); !strings.Contains(ev, `"action":"shutdown"`) {
			t.Fatalf("olay=%s", ev)
		}
	}
	fb.mu.Lock()
	fb.pending = ""
	fb.mu.Unlock()
	if l := c.call(t, `{"id":3,"cmd":"list"}`); l["pendingAction"] != nil {
		t.Fatalf("geri sayım yokken %v", l["pendingAction"])
	}
}

// Eski Hızlı İndir servisi soketinde yanıt veriyorsa çalışıyor sayılır; env ile veri klasörü verildiyse bakılmaz.
func TestLegacyEngineRunning(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "hi") // sun_path 104 bayt sınırı
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("HOME", home)
	t.Setenv("OCTO_DATA_DIR", "")
	t.Setenv("HIZLI_DATA_DIR", "")
	if LegacyEngineRunning() {
		t.Fatal("soket yokken çalışıyor dendi")
	}
	ln, err := Listen(filepath.Join(home, "Library", "Application Support", "HizliIndir", "engine.sock"))
	if err != nil {
		t.Fatal(err)
	}
	if !LegacyEngineRunning() {
		t.Fatal("eski servis görülmedi")
	}
	t.Setenv("OCTO_DATA_DIR", filepath.Join(home, "ozel"))
	if LegacyEngineRunning() {
		t.Fatal("env klasöründe eski servise bakılmamalı")
	}
	ln.Close()
	t.Setenv("OCTO_DATA_DIR", "")
	if LegacyEngineRunning() {
		t.Fatal("kapanan servis çalışıyor sayıldı")
	}
}

// Eski servis çalışırken açılan Octo klasöründe yalnız soket kalır; eski servis kapanınca göç yine yapılır.
func TestMigrateIntoDirWithOnlySocket(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("OCTO_DATA_DIR", "")
	t.Setenv("HIZLI_DATA_DIR", "")
	old := filepath.Join(home, "Library", "Application Support", "HizliIndir")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, settingsFile), []byte(`{"f":1}`), 0o600)
	os.MkdirAll(DataDir(), 0o700)
	os.WriteFile(filepath.Join(DataDir(), "engine.sock"), nil, 0o600)
	if err := MigrateLegacyDataDir(); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(DataDir(), settingsFile)); err != nil || string(b) != `{"f":1}` {
		t.Fatalf("göç yapılmadı: %v %s", err, b)
	}
	if ms, _ := filepath.Glob(filepath.Join(filepath.Dir(DataDir()), ".Octo-migrate-*")); len(ms) != 0 {
		t.Fatalf("geçici klasör kaldı: %v", ms)
	}
}

// pause/resume/remove kimliksiz gelirse sessizce "ok" dönmez; durum değiştiren komut log'a yazılır (canlı tanı için).
func TestStateCommandsNeedIDsAndAreLogged(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	fb := &fakeBackend{}
	sock, _, _ := startServer(t, fb)
	c := dial(t, sock)
	for _, cmd := range []string{"pause", "resume", "remove", "redownload"} {
		if r := c.call(t, `{"id":1,"cmd":"`+cmd+`","id_list":["d1"]}`); r["ok"] != false || !strings.Contains(r["error"].(string), "ids") {
			t.Fatalf("%s: %v", cmd, r)
		}
	}
	c.call(t, `{"id":2,"cmd":"pause","ids":["d1"]}`)
	if !strings.Contains(logs.String(), "pause [d1]") {
		t.Fatalf("log: %q", logs.String())
	}
	if r := c.call(t, `{"id":3,"cmd":"redownload","ids":["d1"]}`); r["ok"] != true {
		t.Fatalf("redownload: %v", r)
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if !slices.Equal(fb.redone, []string{"d1"}) || !strings.Contains(logs.String(), "redownload [d1]") {
		t.Fatalf("redone=%v log=%q", fb.redone, logs.String())
	}
}

func TestItemLimitCommand(t *testing.T) {
	sock, _, _ := startServer(t, &fakeBackend{})
	c := dial(t, sock)
	r := c.call(t, `{"id":1,"cmd":"item.limit","item":"d1","kbps":250}`)
	if it, _ := r["item"].(map[string]any); r["ok"] != true || it["speedLimitKBps"] != float64(250) {
		t.Fatal(r)
	}
	for _, bad := range []string{`{"id":2,"cmd":"item.limit","item":"d1"}`, `{"id":3,"cmd":"item.limit","item":"zz","kbps":1}`} {
		if r = c.call(t, bad); r["ok"] != false || r["error"] == "" {
			t.Fatal(bad, r)
		}
	}
}

// hello (native host) zamanı bellekte tutulur; list `lastHello` (unix sn) olarak verir, hello gelmeden alan yoktur.
func TestHelloRecordsLastHello(t *testing.T) {
	sock, _, _ := startServer(t, &fakeBackend{})
	c := dial(t, sock)
	if l := c.call(t, `{"id":1,"cmd":"list"}`); l["lastHello"] != nil {
		t.Fatalf("hello gelmeden lastHello=%v", l["lastHello"])
	}
	before := time.Now().Unix()
	if r := c.call(t, `{"id":2,"cmd":"hello"}`); r["ok"] != true {
		t.Fatalf("hello=%v", r)
	}
	// Ayrı bağlantıdan da görünür: host kısa ömürlü, uygulama başka bağlantıda.
	got, _ := dial(t, sock).call(t, `{"id":3,"cmd":"list"}`)["lastHello"].(float64)
	if int64(got) < before || int64(got) > time.Now().Unix() {
		t.Fatalf("lastHello=%v, beklenen ≥ %d", got, before)
	}
}

// remove hatası (ör. Çöp'e taşınamadı) yanıtta ok:false olarak istemciye gider.
func TestRemoveErrorReachesClient(t *testing.T) {
	fb := &fakeBackend{rmErr: errors.New("Çöp'e taşınamadı: başka birim")}
	sock, _, _ := startServer(t, fb)
	c := dial(t, sock)
	if r := c.call(t, `{"id":1,"cmd":"remove","ids":["d1"],"deleteFile":true}`); r["ok"] != false || !strings.Contains(r["error"].(string), "Çöp") {
		t.Fatalf("remove=%v", r)
	}
}
