package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeJob testin sürdüğü sahte indirmedir.
type fakeJob struct {
	mu     sync.Mutex
	snap   Snapshot
	done   chan error
	once   sync.Once
	resume bool
	hold   bool          // Pause işi bitirmez; test end ile bitirir (yavaş duran iş)
	paused chan struct{} // Pause çağrıldı
	pOnce  sync.Once
}

func (j *fakeJob) Snapshot() Snapshot { j.mu.Lock(); defer j.mu.Unlock(); return j.snap }
func (j *fakeJob) Pause() {
	j.pOnce.Do(func() { close(j.paused) })
	if !j.hold {
		j.end(ErrPaused)
	}
}
func (j *fakeJob) Wait() error    { return <-j.done }
func (j *fakeJob) end(err error)  { j.once.Do(func() { j.done <- err }) }
func (j *fakeJob) set(s Snapshot) { j.mu.Lock(); j.snap = s; j.mu.Unlock() }

type harness struct {
	t   *testing.T
	m   *Manager
	dir string // veri dizini
	dl  string // indirme dizini

	mu          sync.Mutex
	jobs        map[string]*fakeJob // yol → iş
	reqs        []Request
	now         time.Time
	runs        [][]string
	trashed     []string
	quarantined []string
	limit       int64
	startErr    error
	hold        bool
	password    string   // site girişi parolası (sahte Keychain)
	asked       []string // parolası sorulan Login.Host'lar
	trashErr    error
	probed      []http.Header
	pages       []string   // ProbeStream sayfa adresleri
	qErr        error      // Quarantine hatası
	stream      StreamInfo // ProbeStream yanıtı
	streamErr   error
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, dir: t.TempDir(), dl: t.TempDir(), jobs: map[string]*fakeJob{}, now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.Local)}
	h.open()
	s := h.m.settings
	s.DownloadDir = h.dl
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	return h
}

func (h *harness) deps() Deps {
	return Deps{
		Start: func(ctx context.Context, r Request, resume bool) (Job, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.reqs = append(h.reqs, r)
			if h.startErr != nil {
				return nil, h.startErr
			}
			j := &fakeJob{done: make(chan error, 1), resume: resume, snap: Snapshot{Size: -1}, hold: h.hold, paused: make(chan struct{})}
			h.jobs[r.Path] = j
			return j, nil
		},
		Probe: func(ctx context.Context, url string, hd http.Header, auth AuthFunc) (ProbeResult, error) {
			h.mu.Lock()
			h.probed = append(h.probed, hd)
			h.mu.Unlock()
			size := int64(100)
			if strings.Contains(url, "farkli") {
				size = 999
			}
			if strings.Contains(url, "boyutsuz") {
				size = -1
			}
			return ProbeResult{Name: filepath.Base(url), Size: size, Resumable: true}, nil
		},
		ProbeStream: func(ctx context.Context, url string, hd http.Header, page string, auth AuthFunc) (StreamInfo, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.probed = append(h.probed, hd)
			h.pages = append(h.pages, page)
			return h.stream, h.streamErr
		},
		Password: func(host string) (string, error) {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.asked = append(h.asked, host)
			return h.password, nil
		},
		UniquePath: func(p, mode string, inUse func(string) bool) string {
			ext := filepath.Ext(p)
			base := strings.TrimSuffix(p, ext)
			for n := 2; ; n++ {
				if _, err := os.Stat(p); err != nil && !inUse(p) {
					return p
				}
				p = fmt.Sprintf("%s (%d)%s", base, n, ext)
			}
		},
		CategoryFor: func(name string, cats []Category) string {
			for _, c := range cats {
				if slices.Contains(c.Exts, strings.TrimPrefix(filepath.Ext(name), ".")) {
					return c.Name
				}
			}
			return ""
		},
		DefaultCategories: func() []Category {
			return []Category{{Name: "Arşiv", Exts: []string{"zip"}, Dir: "Arsiv"}}
		},
		SetLimit: func(b int64) { h.mu.Lock(); h.limit = b; h.mu.Unlock() },
		Run: func(name string, args ...string) error {
			h.mu.Lock()
			h.runs = append(h.runs, append([]string{name}, args...))
			h.mu.Unlock()
			return nil
		},
		Trash: func(p string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			if h.trashErr != nil {
				return h.trashErr
			}
			h.trashed = append(h.trashed, p)
			return nil
		},
		Quarantine: func(p string) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.quarantined = append(h.quarantined, p)
			return h.qErr
		},
		Now: func() time.Time { h.mu.Lock(); defer h.mu.Unlock(); return h.now },
	}
}

func (h *harness) open() {
	m, err := NewManager(h.dir, h.deps())
	if err != nil {
		h.t.Fatal(err)
	}
	h.m = m
	h.t.Cleanup(m.Close)
}

func (h *harness) forget(name string) {
	h.mu.Lock()
	delete(h.jobs, filepath.Join(h.dl, name))
	h.mu.Unlock()
}

func (h *harness) setNow(t time.Time) { h.mu.Lock(); h.now = t; h.mu.Unlock() }

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for end := time.Now().Add(2 * time.Second); time.Now().Before(end); time.Sleep(2 * time.Millisecond) {
		if f() {
			return
		}
	}
	t.Fatalf("zaman aşımı: %s", what)
}

// job yolu verilen işin başlamasını bekler.
func (h *harness) job(name string) *fakeJob {
	h.t.Helper()
	p := filepath.Join(h.dl, name)
	var j *fakeJob
	eventually(h.t, "iş başladı "+name, func() bool {
		h.mu.Lock()
		j = h.jobs[p]
		h.mu.Unlock() // h.mu'yu m.mu'dan önce bırak (Manager Now için h.mu alıyor)
		return j != nil && h.m.running(p)
	})
	return j
}

func (m *Manager) running(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, it := range m.items {
		if r := m.runs[it.ID]; r != nil && r.job != nil && itemPath(it) == path {
			return true
		}
	}
	return false
}

func (h *harness) item(id string) Item {
	items, _, _ := h.m.List()
	for _, it := range items {
		if it.ID == id {
			return it
		}
	}
	return Item{}
}

func (h *harness) waitState(id string, s ItemState) {
	h.t.Helper()
	eventually(h.t, id+" "+string(s), func() bool { return h.item(id).State == s })
}

func (h *harness) queue(id string) Queue {
	_, qs, _ := h.m.List()
	for _, q := range qs {
		if q.ID == id {
			return q
		}
	}
	return Queue{}
}

func (h *harness) add(name string, start bool) Item {
	h.t.Helper()
	it, err := h.m.Add(AddArgs{URL: "http://x/" + name, Name: name, Start: start})
	if err != nil {
		h.t.Fatal(err)
	}
	return it
}

func (h *harness) runCount() int { h.mu.Lock(); defer h.mu.Unlock(); return len(h.runs) }

// ran: komut çalıştı mı (abone yokken geri sayım ayrıca "open -b" çalıştırır, sayı güvenilmez).
func (h *harness) ran(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.ContainsFunc(h.runs, func(r []string) bool { return r[0] == name })
}

func (h *harness) activeCount() int {
	h.m.mu.Lock()
	defer h.m.mu.Unlock()
	return len(h.m.runs)
}

func (h *harness) setMain(f func(q *Queue)) {
	h.t.Helper()
	q := h.queue(mainQueue)
	f(&q)
	if err := h.m.SaveQueue(q); err != nil {
		h.t.Fatal(err)
	}
}

func TestQueueAtOnceRunsTwoOfThree(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.AtOnce = 2 })
	a, b, c := h.add("a.bin", false), h.add("b.bin", false), h.add("c.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	h.job("b.bin")
	if got := h.item(c.ID).State; got != StateQueued || h.activeCount() != 2 {
		t.Fatalf("c=%s active=%d", got, h.activeCount())
	}
	h.job("a.bin").end(nil)
	h.waitState(a.ID, StateDone)
	h.job("c.bin")
	if q := h.queue(mainQueue); !slices.Equal(q.Items, []string{b.ID, c.ID}) {
		t.Fatalf("biten öğe kuyruktan çıkmalı: %v", q.Items)
	}
	h.mu.Lock()
	if !slices.Contains(h.quarantined, filepath.Join(h.dl, "a.bin")) {
		t.Fatal("biten dosya karantinaya alınmadı")
	}
	h.mu.Unlock()
}

func TestStartTrueRunsOutsideQueue(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.AtOnce = 1 })
	h.add("a.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	it := h.add("now.bin", true)
	h.job("now.bin")
	if it.Queue != mainQueue || h.activeCount() != 2 {
		t.Fatalf("queue=%q active=%d", it.Queue, h.activeCount())
	}
}

func TestFailureRequeuesAtEnd(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.AtOnce = 1; q.Retries = 2 })
	a, b := h.add("a.bin", false), h.add("b.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin").end(errors.New("bağlantı koptu"))
	h.waitState(a.ID, StateError)
	if q := h.queue(mainQueue); !slices.Equal(q.Items, []string{b.ID, a.ID}) {
		t.Fatalf("başarısız öğe sona gitmeli: %v", q.Items)
	}
	h.job("b.bin").end(nil)
	h.waitState(b.ID, StateDone)
	if h.activeCount() != 0 {
		t.Fatal("başarısız öğe tick'ten önce yeniden başlamamalı")
	}
	h.forget("a.bin")
	h.m.Tick()
	h.job("a.bin").end(errors.New("yine koptu"))
	h.waitState(a.ID, StateError)
	h.m.Tick() // deneme hakkı (2) bitti → kuyruk biter
	eventually(t, "kuyruk durdu", func() bool { return !h.queue(mainQueue).Running })
	if it := h.item(a.ID); it.Error != "yine koptu" {
		t.Fatalf("hata metni: %q", it.Error)
	}
}

func TestSchedulerStartsOnceAt2300(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.Schedule.StartOn = true; q.Schedule.StopOn = true })
	h.add("a.bin", false)
	h.setNow(time.Date(2026, 10, 19, 22, 59, 45, 0, time.Local))
	h.m.Tick()
	if h.queue(mainQueue).Running {
		t.Fatal("22:59'da başlamamalı")
	}
	h.setNow(time.Date(2026, 10, 19, 23, 0, 5, 0, time.Local))
	h.m.Tick()
	if !h.queue(mainQueue).Running {
		t.Fatal("23:00'te başlamalı")
	}
	h.m.StopQueue(mainQueue)
	h.setNow(time.Date(2026, 10, 19, 23, 0, 35, 0, time.Local))
	h.m.Tick()
	if h.queue(mainQueue).Running {
		t.Fatal("59 sn içinde ikinci kez başlamamalı")
	}
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	h.setNow(time.Date(2026, 10, 20, 7, 30, 10, 0, time.Local))
	h.m.Tick()
	if h.queue(mainQueue).Running {
		t.Fatal("07:30'da durmalı")
	}
	h.mu.Lock()
	n := len(h.runs)
	h.mu.Unlock()
	if n != 0 {
		t.Fatal("zamanlı durdurma onDone çalıştırmamalı")
	}
}

func TestSchedulerSkipsDay(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.Schedule.StartOn = true; q.Schedule.Days = []int{7} }) // yalnız Pazar
	h.add("a.bin", false)
	h.setNow(time.Date(2026, 10, 19, 23, 0, 0, 0, time.Local)) // Pazartesi
	h.m.Tick()
	if h.queue(mainQueue).Running {
		t.Fatal("Pazartesi başlamamalı")
	}
	h.setNow(time.Date(2026, 10, 25, 23, 0, 0, 0, time.Local)) // Pazar
	h.m.Tick()
	if !h.queue(mainQueue).Running {
		t.Fatal("Pazar başlamalı")
	}
}

func onDoneSetup(t *testing.T, action string) (*harness, <-chan Event) {
	h := newHarness(t)
	h.m.actionDelay = 100 * time.Millisecond
	h.setMain(func(q *Queue) { q.Schedule.OnDone = action })
	it := h.add("a.bin", false)
	ev, unsub := h.m.Subscribe()
	t.Cleanup(unsub)
	h.m.StartQueue(mainQueue)
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	return h, ev
}

func waitEvent(t *testing.T, ev <-chan Event, kind string) Event {
	t.Helper()
	for {
		select {
		case e := <-ev:
			if e.Event == kind {
				return e
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("%s olayı gelmedi", kind)
		}
	}
}

func TestOnDoneShutdownCancelled(t *testing.T) {
	h, ev := onDoneSetup(t, "shutdown")
	e := waitEvent(t, ev, "action")
	if e.Action != "shutdown" || e.Seconds != 35 {
		t.Fatalf("olay: %+v", e)
	}
	h.m.CancelAction()
	time.Sleep(250 * time.Millisecond)
	if h.runCount() != 0 {
		t.Fatalf("iptal edilen kapatma çalıştı: %v", h.runs)
	}
}

func TestOnDoneSleepRuns(t *testing.T) {
	h, ev := onDoneSetup(t, "sleep")
	waitEvent(t, ev, "action")
	eventually(t, "pmset", func() bool { return h.runCount() == 1 })
	if r := h.runs[0]; !slices.Equal(r, []string{"pmset", "sleepnow"}) {
		t.Fatalf("komut: %v", r)
	}
}

func TestOnDoneShutdownCommand(t *testing.T) {
	h, _ := onDoneSetup(t, "shutdown")
	eventually(t, "osascript", func() bool { return h.runCount() == 1 })
	if r := h.runs[0]; !slices.Equal(r, []string{"osascript", "-e", `tell application "System Events" to shut down`}) {
		t.Fatalf("komut: %v", r)
	}
}

func TestOnDoneOpenLastFile(t *testing.T) {
	h, _ := onDoneSetup(t, "open")
	eventually(t, "open", func() bool { return h.runCount() == 1 })
	if r := h.runs[0]; !slices.Equal(r, []string{"open", filepath.Join(h.dl, "a.bin")}) {
		t.Fatalf("komut: %v", r)
	}
}

// Karantina yazılamayan dosya kendiliğinden açılmaz: Gatekeeper sormadan çalışırdı (B27 L6).
func TestOnDoneOpenSkipsUnquarantined(t *testing.T) {
	h := newHarness(t)
	h.qErr = errors.New("xattr yok")
	h.setMain(func(q *Queue) { q.Schedule.OnDone = "open" })
	it := h.add("a.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	time.Sleep(20 * time.Millisecond)
	if h.runCount() != 0 {
		t.Fatalf("karantinasız dosya açıldı: %v", h.runs)
	}
}

// Karantina yazılamadıysa öğe bunu bayrakla bildirir; uygulama "Bitince aç"ı atlar (SEC1R N4).
func TestQuarantineFailedFlag(t *testing.T) {
	for _, tc := range []struct {
		qErr error
		want bool
	}{{errors.New("xattr yok"), true}, {nil, false}} {
		h := newHarness(t)
		h.qErr = tc.qErr
		it := h.add("a.bin", false)
		h.m.StartQueue(mainQueue)
		h.job("a.bin").end(nil)
		h.waitState(it.ID, StateDone)
		if got := h.item(it.ID).QuarantineFailed; got != tc.want {
			t.Fatalf("qErr=%v: QuarantineFailed=%v", tc.qErr, got)
		}
	}
}

func TestManualStopSkipsOnDone(t *testing.T) {
	h := newHarness(t)
	h.m.actionDelay = time.Millisecond
	h.setMain(func(q *Queue) { q.Schedule.OnDone = "shutdown" })
	it := h.add("a.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	h.m.StopQueue(mainQueue)
	h.waitState(it.ID, StateQueued)
	time.Sleep(20 * time.Millisecond)
	if h.runCount() != 0 {
		t.Fatal("elle durdurma onDone çalıştırmamalı")
	}
}

func TestPersistenceRoundTrip(t *testing.T) {
	h := newHarness(t)
	q := Queue{Name: "Gece", AtOnce: 0}
	if err := h.m.SaveQueue(q); err != nil {
		t.Fatal(err)
	}
	a, err := h.m.Add(AddArgs{URL: "http://x/a.bin", Name: "a.bin", Headers: map[string]string{"Cookie": "s=1"}, Description: "not", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	h.job("a.bin").set(Snapshot{Got: 40, Size: 100, Speed: 10, Resumable: true})
	h.m.progress()
	b := h.add("b.bin", false)
	items1, qs1, s1 := h.m.List()

	h.m.Close()
	h.open()
	items2, qs2, s2 := h.m.List()
	if len(items2) != 2 || items2[0].State != StatePaused || items2[1].State != StateQueued {
		t.Fatalf("yeniden yükleme: %+v", items2)
	}
	if items2[0].Got != 40 || items2[0].Description != "not" || items2[0].ID != a.ID || items2[1].ID != b.ID {
		t.Fatalf("alanlar korunmadı: %+v", items2[0])
	}
	if h.m.item(a.ID).Headers["Cookie"] != "s=1" {
		t.Fatal("başlıklar diskte kalmalı")
	}
	j1, _ := json.Marshal(s1)
	j2, _ := json.Marshal(s2)
	if string(j1) != string(j2) || len(qs1) != 2 || len(qs2) != 2 || qs2[1].Name != "Gece" || qs2[1].AtOnce != 1 {
		t.Fatalf("ayar/kuyruk: %s %s %+v", j1, j2, qs2)
	}
	_ = items1
	if c := h.add("c.bin", false); c.ID != "d3" {
		t.Fatalf("id sayacı korunmalı: %s", c.ID)
	}
}

func TestDownloadingBecomesPausedOnLoad(t *testing.T) {
	dir := t.TempDir()
	doc := downloadsDoc{NextID: 1, Items: []*Item{{ID: "d1", Name: "a.bin", State: StateDownloading, Speed: 5}}}
	if err := writeJSON(dir, downloadsFile, doc); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, jobs: map[string]*fakeJob{}}
	h.open()
	if it := h.item("d1"); it.State != StatePaused || it.Speed != 0 || it.ETA != -1 {
		t.Fatalf("%+v", it)
	}
}

func TestDefaults(t *testing.T) {
	h := &harness{t: t, dir: t.TempDir(), jobs: map[string]*fakeJob{}}
	h.open()
	_, qs, s := h.m.List()
	home, _ := os.UserHomeDir()
	if s.MaxConns != 8 || s.DownloadDir != filepath.Join(home, "Downloads") || s.Duplicate != "number" || s.SpeedLimitOn || s.SpeedLimitKBps != 500 ||
		len(s.Categories) != 1 || !slices.Contains(s.CaptureExts, "iso") || !slices.Contains(s.CaptureExts, "r0*") {
		t.Fatalf("ayarlar: %+v", s)
	}
	q := qs[0]
	if len(qs) != 1 || q.ID != "main" || q.Name != "Ana kuyruk" || q.AtOnce != 4 || q.Retries != 0 ||
		q.Schedule.Start != "23:00" || q.Schedule.Stop != "07:30" || len(q.Schedule.Days) != 7 || q.Schedule.OnDone != "none" {
		t.Fatalf("ana kuyruk: %+v", q)
	}
}

func TestDuplicateAddGetsNumber(t *testing.T) {
	h := newHarness(t)
	a := h.add("x.iso", true)
	b := h.add("x.iso", true)
	if a.Name != "x.iso" || b.Name != "x (2).iso" {
		t.Fatalf("%s %s", a.Name, b.Name)
	}
}

func TestProbeWhenNameEmpty(t *testing.T) {
	h := newHarness(t)
	it, err := h.m.Add(AddArgs{URL: "http://x/arsiv.zip"})
	if err != nil {
		t.Fatal(err)
	}
	if it.Name != "arsiv.zip" || it.Size != 100 || !it.Resumable || it.Category != "Arşiv" || it.Queue != "main" {
		t.Fatalf("%+v", it)
	}
}

func TestCategoryFolderAndExplicitDir(t *testing.T) {
	h := newHarness(t)
	s := h.m.settings
	s.CategoryFolders = true
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	a := h.add("a.zip", false)
	if a.Dir != filepath.Join(h.dl, "Arsiv") {
		t.Fatalf("kategori klasörü: %s", a.Dir)
	}
	own := t.TempDir()
	b, err := h.m.Add(AddArgs{URL: "http://x/b.zip", Name: "b.zip", Dir: own})
	if err != nil {
		t.Fatal(err)
	}
	if b.Dir != own {
		t.Fatalf("açık dizin kategoriyi ezmeli: %s", b.Dir)
	}
}

func TestNameCannotEscapeDir(t *testing.T) {
	h := newHarness(t)
	it := h.add("../../etc/passwd", false)
	if it.Dir != h.dl || it.Name != "passwd" {
		t.Fatalf("%s %s", it.Dir, it.Name)
	}
}

func TestRemoveDeleteFileUsesTrash(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	final := filepath.Join(h.dl, "a.bin")
	os.WriteFile(final, []byte("x"), 0o644)
	h.m.Remove([]string{it.ID}, true)
	if !slices.Equal(h.trashed, []string{final}) {
		t.Fatalf("çöp: %v", h.trashed)
	}
	if _, err := os.Stat(final); err != nil {
		t.Fatal("dosya unlink edilmemeli (Çöp'e taşıma bağımlılığın işi)")
	}
	if items, _, _ := h.m.List(); len(items) != 0 {
		t.Fatal("öğe listeden çıkmalı")
	}
}

func TestRemoveUnfinishedDeletesPart(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	h.job("a.bin")
	p := filepath.Join(h.dl, "a.bin")
	os.WriteFile(p+".part", []byte("x"), 0o644)
	os.WriteFile(p+".part.json", []byte("{}"), 0o644)
	ev, unsub := h.m.Subscribe()
	defer unsub()
	h.m.Remove([]string{it.ID}, false)
	for _, f := range []string{p + ".part", p + ".part.json"} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s silinmeli", f)
		}
	}
	if e := waitEvent(t, ev, "removed"); !slices.Equal(e.IDs, []string{it.ID}) {
		t.Fatalf("%+v", e)
	}
	if len(h.trashed) != 0 {
		t.Fatal("bitmemiş öğede çöp yok")
	}
}

func TestHeadersNeverLeak(t *testing.T) {
	h := newHarness(t)
	ev, unsub := h.m.Subscribe()
	defer unsub()
	it, err := h.m.Add(AddArgs{URL: "http://x/a.bin", Name: "a.bin", Headers: map[string]string{"Cookie": "gizli"}, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	h.job("a.bin").set(Snapshot{Got: 1, Size: 10})
	h.m.progress()
	h.mu.Lock()
	if h.reqs[0].Header.Get("Cookie") != "gizli" {
		t.Fatal("başlık indirmeye iletilmeli")
	}
	h.mu.Unlock()
	items, _, _ := h.m.List()
	var buf []byte
	b, _ := json.Marshal(items)
	buf = append(buf, b...)
	b, _ = json.Marshal(it)
	buf = append(buf, b...)
	for len(ev) > 0 {
		b, _ = json.Marshal(<-ev)
		buf = append(buf, b...)
	}
	if strings.Contains(string(buf), "gizli") || strings.Contains(string(buf), "headers") {
		t.Fatalf("başlık sızdı: %s", buf)
	}
}

func TestMoveToQueue(t *testing.T) {
	h := newHarness(t)
	if err := h.m.SaveQueue(Queue{Name: "Gece"}); err != nil {
		t.Fatal(err)
	}
	a, b := h.add("a.bin", false), h.add("b.bin", false)
	if err := h.m.MoveToQueue([]string{a.ID}, "q1"); err != nil {
		t.Fatal(err)
	}
	if m, n := h.queue(mainQueue), h.queue("q1"); !slices.Equal(m.Items, []string{b.ID}) || !slices.Equal(n.Items, []string{a.ID}) || h.item(a.ID).Queue != "q1" {
		t.Fatalf("main=%v q1=%v", m.Items, n.Items)
	}
	if h.m.MoveToQueue([]string{a.ID}, "yok") == nil {
		t.Fatal("bilinmeyen kuyruk hata vermeli")
	}
	if h.m.DeleteQueue(mainQueue) == nil {
		t.Fatal("ana kuyruk silinemez")
	}
	if err := h.m.DeleteQueue("q1"); err != nil || h.item(a.ID).Queue != mainQueue {
		t.Fatalf("silme: %v %q", err, h.item(a.ID).Queue)
	}
	if m := h.queue(mainQueue); !slices.Equal(m.Items, []string{b.ID, a.ID}) || h.queue("q1").ID != "" {
		t.Fatalf("silinen kuyruğun öğeleri ana kuyruğa geçmeli: %v", m.Items)
	}
}

func TestPauseResume(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	h.job("a.bin")
	h.m.Pause([]string{it.ID})
	h.waitState(it.ID, StatePaused)
	p := filepath.Join(h.dl, "a.bin")
	os.WriteFile(p+".part", nil, 0o644)
	h.forget("a.bin")
	h.m.Resume([]string{it.ID})
	if j := h.job("a.bin"); !j.resume {
		t.Fatal(".part varken resume=true olmalı")
	}
}

func TestStartErrorMarksItem(t *testing.T) {
	h := newHarness(t)
	h.startErr = errors.New("klasöre yazılamıyor")
	it := h.add("a.bin", true)
	h.waitState(it.ID, StateError)
	if h.item(it.ID).Error != "klasöre yazılamıyor" {
		t.Fatal(h.item(it.ID).Error)
	}
}

func TestProgressChangedOnly(t *testing.T) {
	h := newHarness(t)
	ev, unsub := h.m.Subscribe()
	defer unsub()
	it := h.add("a.bin", true)
	j := h.job("a.bin")
	j.set(Snapshot{Got: 50, Size: 100, Speed: 10, Conns: []Seg{{0, 50, 99, ""}}})
	h.m.progress()
	e := waitEvent(t, ev, "progress")
	p := e.Items[0]
	if p.ID != it.ID || p.Got != 50 || p.ETA != 5 || !slices.Equal(p.Conns, []Conn{{0, 50, 99, ""}}) {
		t.Fatalf("%+v", p)
	}
	h.m.progress()
	select {
	case e := <-ev:
		if e.Event == "progress" {
			t.Fatal("değişmeyen öğe tekrar gönderilmemeli")
		}
	default:
	}
}

func TestSpeedLimit(t *testing.T) {
	h := newHarness(t)
	s := h.m.settings
	s.SpeedLimitOn, s.SpeedLimitKBps = true, 200
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if h.limit != 200*1024 {
		t.Fatal(h.limit)
	}
	s.SpeedLimitOn = false
	h.m.SetSettings(s)
	if h.limit != 0 {
		t.Fatal(h.limit)
	}
}

// ---- Düzeltme turu 1 ----

func TestPauseLastRunningItemNoOnDone(t *testing.T) {
	for _, how := range []string{"pause", "remove"} {
		t.Run(how, func(t *testing.T) {
			h := newHarness(t)
			h.m.actionDelay = 20 * time.Millisecond
			h.setMain(func(q *Queue) { q.Schedule.OnDone = "shutdown" })
			ev, unsub := h.m.Subscribe()
			defer unsub()
			it := h.add("a.bin", false)
			h.m.StartQueue(mainQueue)
			h.job("a.bin")
			if how == "pause" {
				h.m.Pause([]string{it.ID})
				h.waitState(it.ID, StatePaused)
				if q := h.queue(mainQueue); !slices.Equal(q.Items, []string{it.ID}) {
					t.Fatalf("duraklatılan öğe yerini korumalı: %v", q.Items)
				}
			} else {
				h.m.Remove([]string{it.ID}, false)
			}
			eventually(t, "kuyruk durdu", func() bool { return !h.queue(mainQueue).Running })
			time.Sleep(80 * time.Millisecond)
			if h.runCount() != 0 {
				t.Fatalf("kullanıcı işlemi onDone tetikledi: %v", h.runs)
			}
			for len(ev) > 0 {
				if e := <-ev; e.Event == "action" {
					t.Fatal("action olayı gitmemeli")
				}
			}
		})
	}
}

func TestOnDoneOnlyWhenAllDone(t *testing.T) {
	h := newHarness(t)
	h.m.actionDelay = 20 * time.Millisecond
	h.setMain(func(q *Queue) { q.Schedule.OnDone = "shutdown"; q.AtOnce = 2 })
	a, b := h.add("a.bin", false), h.add("b.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin").end(nil)
	h.waitState(a.ID, StateDone)
	time.Sleep(50 * time.Millisecond)
	if h.runCount() != 0 {
		t.Fatal("b sürerken onDone olmamalı")
	}
	h.job("b.bin").end(nil)
	h.waitState(b.ID, StateDone)
	eventually(t, "kapatma komutu", func() bool { return h.ran("osascript") })
}

func TestCloseRequeuesQueueRunPausesManual(t *testing.T) {
	h := newHarness(t)
	q := h.add("q.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("q.bin")
	man := h.add("man.bin", true)
	h.job("man.bin")
	h.m.Close()
	h.open()
	if s := h.item(q.ID).State; s != StateQueued {
		t.Fatalf("kuyruğun işi sırada olmalı: %s", s)
	}
	if s := h.item(man.ID).State; s != StatePaused {
		t.Fatalf("elle başlatılan duraklatılmış olmalı: %s", s)
	}
}

func TestCrashReloadRequeuesQueueRuns(t *testing.T) {
	dir := t.TempDir()
	doc := downloadsDoc{NextID: 2, QueueRuns: []string{"d1"}, Items: []*Item{
		{ID: "d1", Name: "q.bin", Queue: mainQueue, State: StateDownloading},
		{ID: "d2", Name: "m.bin", Queue: mainQueue, State: StateDownloading},
	}}
	if err := writeJSON(dir, downloadsFile, doc); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, jobs: map[string]*fakeJob{}}
	h.open()
	if h.item("d1").State != StateQueued || h.item("d2").State != StatePaused {
		t.Fatalf("%s %s", h.item("d1").State, h.item("d2").State)
	}
}

func TestQueueRunsPersistedWhileRunning(t *testing.T) {
	h := newHarness(t)
	q := h.add("q.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("q.bin")
	h.add("x.bin", false) // kayıt tetikler
	var doc downloadsDoc
	if err := readJSON(h.dir, downloadsFile, &doc); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(doc.QueueRuns, []string{q.ID}) {
		t.Fatalf("queueRuns: %v", doc.QueueRuns)
	}
}

func TestOnlyHTTPURLs(t *testing.T) {
	h := newHarness(t)
	ok := h.add("a.bin", false)
	for _, u := range []string{"ftp://x/a.bin", "file:///etc/passwd", "javascript:alert(1)", "http://", "", "x/a.bin"} {
		if _, err := h.m.Add(AddArgs{URL: u, Name: "a.bin"}); err == nil || !strings.Contains(err.Error(), "http") {
			t.Errorf("Add %q: %v", u, err)
		}
		if _, err := h.m.Refresh(ok.ID, u); err == nil {
			t.Errorf("Refresh %q kabul edildi", u)
		}
	}
	if _, err := h.m.Add(AddArgs{URL: "https://x/b.bin", Name: "b.bin"}); err != nil {
		t.Fatal(err)
	}
	if it, err := h.m.Refresh(ok.ID, "https://y/a.bin"); err != nil || it.URL != "https://y/a.bin" {
		t.Fatal(it.URL, err)
	}
}

func TestRemoveRacesPump(t *testing.T) {
	h := newHarness(t)
	h.hold = true
	h.setMain(func(q *Queue) { q.AtOnce = 1 })
	a, b := h.add("a.bin", false), h.add("b.bin", false)
	h.m.StartQueue(mainQueue)
	ja := h.job("a.bin")
	pa := filepath.Join(h.dl, "a.bin")
	pb := filepath.Join(h.dl, "b.bin")
	for _, f := range []string{pa + ".part", pa + ".part.json", pb + ".part"} {
		if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	removed := make(chan struct{})
	go func() { h.m.Remove([]string{a.ID, b.ID}, false); close(removed) }()
	<-ja.paused
	// a hâlâ duruyor; bu arada pompa çalışır ve slot açılır: b başlatılmamalı
	h.setMain(func(q *Queue) { q.AtOnce = 2 })
	h.m.Tick()
	if _, err := os.Stat(pa + ".part"); err != nil {
		t.Fatal("iş durmadan .part silindi")
	}
	ja.end(ErrPaused)
	<-removed
	h.mu.Lock()
	for _, r := range h.reqs {
		if r.Path == pb {
			t.Error("silinen öğe için iş başlatıldı")
		}
	}
	h.mu.Unlock()
	for _, f := range []string{pa + ".part", pa + ".part.json", pb + ".part"} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s silinmeli", f)
		}
	}
	if items, _, _ := h.m.List(); len(items) != 0 {
		t.Fatalf("öğeler kalmamalı: %d", len(items))
	}
}

func TestHandStartedDoesNotUseAtOnceSlot(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.AtOnce = 1 })
	h.add("now.bin", true)
	h.job("now.bin")
	h.add("a.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	if h.activeCount() != 2 {
		t.Fatalf("active=%d", h.activeCount())
	}
}

func TestRemoveNeverStartedKeepsOnDone(t *testing.T) {
	h := newHarness(t)
	h.m.actionDelay = time.Millisecond
	h.setMain(func(q *Queue) { q.AtOnce = 1; q.Schedule.OnDone = "shutdown" })
	a := h.add("a.bin", false)
	b := h.add("b.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	h.m.Remove([]string{b.ID}, false)
	h.job("a.bin").end(nil)
	h.waitState(a.ID, StateDone)
	eventually(t, "kapatma", func() bool { return h.ran("osascript") })
}

// Uygulama yeni kuyruğun kimliğini kendisi seçip queue.save yollar.
func TestSaveQueueClientChosenID(t *testing.T) {
	h := newHarness(t)
	if err := h.m.SaveQueue(Queue{ID: "q4821", Name: "Gece"}); err != nil {
		t.Fatal(err)
	}
	if q := h.queue("q4821"); q.Name != "Gece" || q.AtOnce != 1 {
		t.Fatalf("%+v", q)
	}
}

// Adres yenileme: yeni adresin boyutu farklıysa reddedilir, adres değişmez (spec Özellik 2).
func TestRefreshChecksSize(t *testing.T) {
	h := newHarness(t)
	it, err := h.m.Add(AddArgs{URL: "http://x/a.zip"}) // Probe: boyut 100
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.m.Refresh(it.ID, "http://y/farkli.zip"); err == nil || !strings.Contains(err.Error(), "boyut farklı") {
		t.Fatalf("Refresh=%v", err)
	}
	if u := h.item(it.ID).URL; u != "http://x/a.zip" {
		t.Fatalf("adres değişti: %s", u)
	}
	if _, err := h.m.Refresh(it.ID, "http://y/boyutsuz.zip"); err == nil || !strings.Contains(err.Error(), "boyut bildirmiyor") {
		t.Fatalf("boyutsuz: %v", err)
	}
	if r, err := h.m.Refresh(it.ID, "http://y/a.zip"); err != nil || r.URL != "http://y/a.zip" {
		t.Fatalf("aynı boyut: %+v %v", r, err)
	}
}

// Motor bitişte numaralı ada koyduysa (B27 L5) öğe yeni adı alır, karantina o dosyaya yazılır.
// Duplicate "overwrite" ise motora ezme izni gider.
func TestFinishedJobPathRenamesItem(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.zip", true)
	j := h.job("a.zip")
	j.set(Snapshot{Size: -1, Path: filepath.Join(h.dl, "a (2).zip")})
	j.end(nil)
	h.waitState(it.ID, StateDone)
	h.mu.Lock()
	q, ow := h.quarantined, h.reqs[0].Overwrite
	h.mu.Unlock()
	if got := h.item(it.ID).Name; got != "a (2).zip" || len(q) != 1 || q[0] != filepath.Join(h.dl, "a (2).zip") || ow {
		t.Fatalf("ad=%s karantina=%v overwrite=%v", got, q, ow)
	}
	s := h.m.settings
	s.Duplicate = "overwrite"
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	h.add("b.zip", true)
	h.job("b.zip")
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.reqs[1].Overwrite {
		t.Fatal("overwrite kipinde ezme izni gitmedi")
	}
}

// "Üzerine yaz" kipinde motora Çöp işlevi gider; aynı dosyayı gösteren eski "bitti" öğesi
// listeden düşer (V1a Minor 2: sonra "dosyayla sil" yeni dosyayı Çöp'e atardı).
func TestOverwriteDropsStaleDoneItem(t *testing.T) {
	h := newHarness(t)
	s := h.m.settings
	s.Duplicate = "overwrite"
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	old := h.add("a.zip", true)
	h.job("a.zip").end(nil)
	h.waitState(old.ID, StateDone)
	h.forget("a.zip")
	it := h.add("a.zip", true)
	j := h.job("a.zip")
	h.mu.Lock()
	trash := h.reqs[1].Trash
	h.mu.Unlock()
	if trash == nil {
		t.Fatal("overwrite kipinde Çöp işlevi gitmedi")
	}
	ev, unsub := h.m.Subscribe()
	defer unsub()
	j.end(nil)
	if e := waitEvent(t, ev, "removed"); !slices.Equal(e.IDs, []string{old.ID}) {
		t.Fatalf("%+v", e)
	}
	h.waitState(it.ID, StateDone)
	if items, _, _ := h.m.List(); len(items) != 1 || items[0].ID != it.ID {
		t.Fatalf("liste: %+v", items)
	}
}

// Biten indirmenin tarayıcı çerezleri diskte kalmaz (B27 L4).
func TestDoneItemForgetsHeaders(t *testing.T) {
	h := newHarness(t)
	it, err := h.m.Add(AddArgs{URL: "http://x/a.zip", Name: "a.zip", Headers: map[string]string{"Cookie": "gizli=1"}, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	h.job("a.zip").end(nil)
	h.waitState(it.ID, StateDone)
	b, err := os.ReadFile(filepath.Join(h.dir, "downloads.json"))
	if err != nil || strings.Contains(string(b), "gizli") {
		t.Fatalf("downloads.json çerez taşıyor: %s %v", b, err)
	}
}

// Adres başka siteye yenilenince eski sitenin çerezi/kimliği/Referer'ı yeni siteye gitmez (B27 M2);
// aynı sitede ve User-Agent'ta kalır. https → http yenilemesi de kimlik düşürür.
func TestRefreshDropsCredentialsAcrossSites(t *testing.T) {
	for _, tc := range []struct {
		to   string
		keep bool
	}{
		{"https://mirror.example/a.zip", false},
		{"http://cdn.bank.example/a.zip", false},
		{"https://cdn.bank.example/a.zip", true},
	} {
		h := newHarness(t)
		hd := map[string]string{"Cookie": "s=1", "Authorization": "Basic eDp5", "Referer": "https://bank.example/", "User-Agent": "UA"}
		it, err := h.m.Add(AddArgs{URL: "https://bank.example/dl?id=1", Headers: hd})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := h.m.Refresh(it.ID, tc.to); err != nil {
			t.Fatal(err)
		}
		h.m.Resume([]string{it.ID})
		h.job(it.Name)
		h.mu.Lock()
		got := []http.Header{h.probed[len(h.probed)-1], h.reqs[len(h.reqs)-1].Header}
		h.mu.Unlock()
		for i, g := range got {
			for _, k := range []string{"Cookie", "Authorization", "Referer"} {
				if (g.Get(k) != "") != tc.keep {
					t.Fatalf("%s %d: %s=%q", tc.to, i, k, g.Get(k))
				}
			}
			if g.Get("User-Agent") != "UA" {
				t.Fatalf("%s %d: User-Agent düştü", tc.to, i)
			}
		}
	}
}

// 0'dan yeniden başlatma öğede görünür (olay + liste); yeni başlatmada temizlenir.
func TestRestartReasonShownOnItem(t *testing.T) {
	h := newHarness(t)
	ev, unsub := h.m.Subscribe()
	defer unsub()
	it := h.add("a.bin", true)
	h.job("a.bin").set(Snapshot{Size: 10, Restarted: true, RestartReason: "sunucu kaldığı yerden devamı desteklemiyor"})
	h.m.progress()
	for {
		e := waitEvent(t, ev, "item")
		if e.Item.RestartReason != "" {
			break
		}
	}
	if r := h.item(it.ID).RestartReason; !strings.Contains(r, "desteklemiyor") {
		t.Fatalf("restartReason=%q", r)
	}
	h.m.Pause([]string{it.ID})
	h.waitState(it.ID, StatePaused)
	h.forget("a.bin")
	h.m.Resume([]string{it.ID})
	h.job("a.bin")
	if r := h.item(it.ID).RestartReason; r != "" {
		t.Fatalf("yeni başlatmada temizlenmedi: %q", r)
	}
}

// Kategori klasörleri açıkken klasörü verilmemiş kategori kendi adını kullanır; Genel ana klasörde kalır.
func TestCategoryFolderDefaultsToCategoryName(t *testing.T) {
	h := newHarness(t)
	s := h.m.settings
	s.CategoryFolders = true
	s.Categories = []Category{{Name: "Genel"}, {Name: "Arşiv", Exts: []string{"zip"}}, {Name: "../dış", Exts: []string{"iso"}}}
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	if a := h.add("a.iso", false); filepath.Dir(a.Dir) != h.dl { // kategori adı indirme klasöründen çıkmaz
		t.Fatalf("../dış: %s", a.Dir)
	}
	if a := h.add("a.zip", false); a.Dir != filepath.Join(h.dl, "Arşiv") {
		t.Fatalf("Arşiv: %s", a.Dir)
	}
	g, err := h.m.Add(AddArgs{URL: "http://x/b.txt", Name: "b.txt", Category: "Genel"})
	if err != nil || g.Dir != h.dl {
		t.Fatalf("Genel: %s %v", g.Dir, err)
	}
}

// Ayarlardaki User-Agent her indirme isteğine taşınır.
func TestSettingsUserAgentReachesRequest(t *testing.T) {
	h := newHarness(t)
	s := h.m.settings
	s.UserAgent = "Ozel/2.0"
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	h.add("a.bin", true)
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.reqs) != 1 || h.reqs[0].UserAgent != "Ozel/2.0" {
		t.Fatalf("reqs=%+v", h.reqs)
	}
}

// Hiç başlamamış öğeyi başka kuyruğa taşımak onDone'u engellemez (Remove ile aynı kural).
func TestMoveNeverStartedKeepsOnDone(t *testing.T) {
	h := newHarness(t)
	h.m.actionDelay = time.Millisecond
	h.setMain(func(q *Queue) { q.AtOnce = 1; q.Schedule.OnDone = "shutdown" })
	if err := h.m.SaveQueue(Queue{ID: "q2", Name: "İki"}); err != nil {
		t.Fatal(err)
	}
	a := h.add("a.bin", false)
	b := h.add("b.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin")
	if err := h.m.MoveToQueue([]string{b.ID}, "q2"); err != nil {
		t.Fatal(err)
	}
	h.job("a.bin").end(nil)
	h.waitState(a.ID, StateDone)
	eventually(t, "kapatma", func() bool { return h.ran("osascript") })
}

func TestAddNameSanitized(t *testing.T) {
	h := newHarness(t)
	if it := h.add("evil\u202egpj.exe", false); it.Name != "evilgpj.exe" {
		t.Fatalf("%q", it.Name)
	}
}

// Çöp'e taşınamayan dosyanın öğesi listede kalır ve hata gösterir (kullanıcı silindi sanmasın).
func TestRemoveTrashFailureKeepsItem(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	h.mu.Lock()
	h.trashErr = errors.New("cross-device link")
	h.mu.Unlock()
	err := h.m.Remove([]string{it.ID}, true)
	if err == nil || !strings.Contains(err.Error(), "Çöp") {
		t.Fatalf("Remove=%v", err) // istemci hatayı yanıtta görmeli
	}
	if got := h.item(it.ID); got.ID == "" || !strings.Contains(got.Error, "Çöp") {
		t.Fatalf("%+v", got)
	}
}

func TestSaveQueueClampsAtOnce(t *testing.T) {
	h := newHarness(t)
	if err := h.m.SaveQueue(Queue{ID: "q9", Name: "Çok", AtOnce: 50}); err != nil {
		t.Fatal(err)
	}
	if q := h.queue("q9"); q.AtOnce != 10 {
		t.Fatalf("atOnce=%d", q.AtOnce)
	}
}

// Bekleyen geri sayım sonradan bağlanan uygulamaya görünür: PendingAction ve yeni aboneye action olayı.
func TestPendingActionVisibleToLateSubscriber(t *testing.T) {
	h := newHarness(t)
	h.setMain(func(q *Queue) { q.Schedule.OnDone = "shutdown" })
	it := h.add("a.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	eventually(t, "geri sayım", func() bool { a, _ := h.m.PendingAction(); return a == "shutdown" })
	if _, sec := h.m.PendingAction(); sec < 1 || sec > 35 {
		t.Fatalf("seconds=%d", sec)
	}
	ev, unsub := h.m.Subscribe()
	defer unsub()
	if e := waitEvent(t, ev, "action"); e.Action != "shutdown" || e.Seconds < 1 {
		t.Fatalf("%+v", e)
	}
	h.m.CancelAction()
	if a, _ := h.m.PendingAction(); a != "" {
		t.Fatalf("iptalden sonra %q", a)
	}
}

// Eski servis çalışırken (Block) hiçbir indirme başlamaz, durum dosyaları yazılmaz, hata Türkçe görünür.
func TestBlockedRefusesStartsAndWrites(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", false)
	if err := h.m.SaveQueue(Queue{Name: "Gece"}); err != nil {
		t.Fatal(err)
	}
	_, qs, _ := h.m.List()
	night := qs[len(qs)-1].ID
	h.m.Block("Eski Hızlı İndir servisi çalışıyor")
	files := map[string][]byte{}
	for _, f := range []string{downloadsFile, settingsFile, queuesFile} {
		files[f], _ = os.ReadFile(filepath.Join(h.dir, f))
	}
	if _, err := h.m.Add(AddArgs{URL: "http://x/b.bin", Name: "b.bin"}); err == nil || !strings.Contains(err.Error(), "Eski Hızlı İndir") {
		t.Fatalf("Add=%v", err)
	}
	h.m.Resume([]string{it.ID})
	h.m.StartQueue(mainQueue)
	h.m.Tick()
	if got := h.item(it.ID); got.State != StateError || !strings.Contains(got.Error, "Eski Hızlı İndir") {
		t.Fatalf("%+v", got)
	}
	s := h.m.settings
	s.MaxConns = 3
	if err := h.m.SetSettings(s); err == nil || h.m.settings.MaxConns == 3 {
		t.Fatalf("SetSettings=%v", err) // kaydedilemeyen değişiklik "ok" dönmesin
	}
	if _, err := h.m.Refresh(it.ID, "http://y/a.bin"); err == nil || !strings.Contains(err.Error(), "Eski Hızlı İndir") {
		t.Fatalf("Refresh=%v", err)
	}
	blocked := func(what string, err error) { // kaydedilemeyen değişiklik "ok" dönüp açılışta kaybolmasın
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "Eski Hızlı İndir") {
			t.Fatalf("%s=%v", what, err)
		}
	}
	_, err := h.m.Describe(it.ID, "not")
	blocked("Describe", err)
	blocked("SaveQueue", h.m.SaveQueue(Queue{Name: "Yeni"}))
	blocked("DeleteQueue", h.m.DeleteQueue(night))
	blocked("MoveToQueue", h.m.MoveToQueue([]string{it.ID}, night))
	if got := h.item(it.ID); got.Description != "" || got.Queue != mainQueue {
		t.Fatalf("Block kipinde öğe değişti: %+v", got)
	}
	os.WriteFile(filepath.Join(it.Dir, it.Name+".part"), []byte("x"), 0o600) // eski servis yazıyor olabilir
	if err := h.m.Remove([]string{it.ID}, true); err == nil || !strings.Contains(err.Error(), "Eski Hızlı İndir") {
		t.Fatalf("Remove=%v", err)
	}
	if _, err := os.Stat(filepath.Join(it.Dir, it.Name+".part")); err != nil || h.item(it.ID).ID == "" {
		t.Fatalf("Block kipinde Remove sildi: %v", err)
	}
	h.mu.Lock()
	n := len(h.reqs)
	h.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d indirme başladı", n)
	}
	for f, b := range files {
		if now, _ := os.ReadFile(filepath.Join(h.dir, f)); string(now) != string(b) {
			t.Fatalf("%s yazıldı", f)
		}
	}
}

// Site girişi: parola yalnız 401'de istenecek Auth'tan gelir; liste, olay ve log'a çıkmaz.
func TestLoginAuthNeverLeaks(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	h := newHarness(t)
	h.mu.Lock()
	h.password = "s3cr3t"
	h.mu.Unlock()
	s := h.m.settings
	s.Logins = []Login{{Host: "*.ornek.com", User: "ali"}}
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	ev, unsub := h.m.Subscribe()
	defer unsub()
	it, err := h.m.Add(AddArgs{URL: "http://dl.ornek.com/a.bin", Name: "a.bin", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	h.job("a.bin").set(Snapshot{Got: 1, Size: 10})
	h.m.progress()
	h.mu.Lock()
	auth := h.reqs[0].Auth
	h.mu.Unlock()
	if u, p, ok := auth("DL.ornek.com"); !ok || u != "ali" || p != "s3cr3t" {
		t.Fatalf("Auth=%q %q %v", u, p, ok)
	}
	if _, _, ok := auth("ornek.com.evil"); ok {
		t.Fatal("başka host'a giriş verildi")
	}
	h.mu.Lock()
	if !slices.Equal(h.asked, []string{"*.ornek.com"}) {
		t.Fatalf("sorulan: %v", h.asked)
	}
	h.mu.Unlock()
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	items, _, set := h.m.List()
	b, _ := json.Marshal([]any{items, set, it})
	buf := string(b)
	for len(ev) > 0 {
		b, _ = json.Marshal(<-ev)
		buf += string(b)
	}
	buf += logs.String()
	if strings.Contains(buf, "s3cr3t") || strings.Contains(buf, "YWxpOnMzY3IzdA") {
		t.Fatalf("parola sızdı: %s", buf)
	}
}

// Sözleşme: speed bayt/sn tam sayıdır (Swift Int64 kesirli sayıyı çözemez, progress olayları atılırdı).
func TestSpeedIsWholeNumberInJSON(t *testing.T) {
	h := newHarness(t)
	ev, unsub := h.m.Subscribe()
	defer unsub()
	h.add("a.bin", true)
	h.job("a.bin").set(Snapshot{Got: 1, Size: 1 << 30, Speed: 19020899.29184468})
	h.m.progress()
	p := waitEvent(t, ev, "progress")
	items, _, _ := h.m.List()
	b, _ := json.Marshal([]any{encodeEvent(p), items})
	if !strings.Contains(string(b), `"speed":19020899,`) || strings.Contains(string(b), "19020899.") {
		t.Fatalf("speed tam sayı değil: %s", b)
	}
}

// Önceki sürümün downloads.json'ı kesirli speed taşıyabilir; açılış bozulmamalı.
func TestLoadOldFractionalSpeed(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, downloadsFile), []byte(`{"nextId":1,"items":[{"id":"d1","url":"http://x/a","name":"a","state":"paused","size":10,"speed":19020899.29}]}`), 0o600)
	m, err := NewManager(dir, (&harness{t: t, jobs: map[string]*fakeJob{}}).deps())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if items, _, _ := m.List(); len(items) != 1 || items[0].Speed != 0 {
		t.Fatalf("%+v", items)
	}
}

// Yeniden indir: bitmiş dosyaya dokunmadan 0'dan başlar (yeni dosya aynı-ad ayarına göre adlanır).
func TestRedownloadDoneStartsFromZero(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	j := h.job("a.bin")
	j.set(Snapshot{Size: 100, Got: 100})
	j.end(nil)
	h.waitState(it.ID, StateDone)
	final := filepath.Join(h.dl, "a.bin")
	os.WriteFile(final, []byte("x"), 0o644)
	h.forget("a.bin")
	h.m.Redownload([]string{it.ID})
	if j := h.job("a.bin"); j.resume {
		t.Fatal("yeniden indirme devam etmemeli")
	}
	got := h.item(it.ID)
	if got.State != StateDownloading || got.Got != 0 || got.Finished != nil {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Stat(final); err != nil || len(h.trashed) != 0 {
		t.Fatal("bitmiş dosyaya dokunulmamalı")
	}
}

func TestRedownloadPausedDropsPart(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	h.job("a.bin")
	h.m.Pause([]string{it.ID})
	h.waitState(it.ID, StatePaused)
	p := filepath.Join(h.dl, "a.bin")
	os.WriteFile(p+".part", []byte("x"), 0o644)
	os.WriteFile(p+".part.json", []byte("{}"), 0o644)
	h.forget("a.bin")
	h.m.Redownload([]string{it.ID})
	if j := h.job("a.bin"); j.resume {
		t.Fatal(".part atılmalı, iş 0'dan başlamalı")
	}
	for _, f := range []string{p + ".part", p + ".part.json"} {
		if _, err := os.Stat(f); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s silinmeli", f)
		}
	}
}

// Süren indirme önce durur; .part, yazan iş bittikten sonra atılır ve iş 0'dan yeniden başlar.
func TestRedownloadRunningRestarts(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	old := h.job("a.bin")
	p := filepath.Join(h.dl, "a.bin")
	os.WriteFile(p+".part", []byte("x"), 0o644)
	h.forget("a.bin")
	h.m.Redownload([]string{it.ID})
	<-old.paused
	if j := h.job("a.bin"); j.resume {
		t.Fatal("yeniden indirme devam etmemeli")
	}
	if _, err := os.Stat(p + ".part"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(".part silinmeli")
	}
	if s := h.item(it.ID).State; s != StateDownloading {
		t.Fatal(s)
	}
}

// Spec §2 ikinci yol: tarayıcıdan aynı ad ve boyutla gelen indirme, yarım öğe için "adresi yenile" önerisi olur;
// öneri başlamaz. Yenilemede tarayıcının yeni başlıkları kullanılır ve öneri öğesi listeden çıkar.
func TestBrowserDuplicateOffersRefresh(t *testing.T) {
	h := newHarness(t)
	old, err := h.m.Add(AddArgs{URL: "http://x/a.zip"}) // Probe: boyut 100; kuyrukta yarım bekler
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.m.Add(AddArgs{URL: "http://y/a.zip", Name: "a.zip", Size: 999})
	if err != nil || o.RefreshOf != "" {
		t.Fatalf("farklı boyut öneri olmamalı: %+v %v", o, err)
	}
	hd := map[string]string{"Cookie": "yeni=1"}
	o, err = h.m.Add(AddArgs{URL: "http://cdn/a.zip?t=2", Name: "a.zip", Size: 100, Start: true, Headers: hd})
	if err != nil || o.RefreshOf != old.ID || o.State != StatePaused {
		t.Fatalf("öneri: %+v %v", o, err)
	}
	h.m.mu.Lock()
	started, cookie := h.m.runs[o.ID] != nil, ""
	h.m.mu.Unlock()
	if started {
		t.Fatal("öneri öğesi başladı")
	}
	if _, err := h.m.Refresh(old.ID, o.URL); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	probed := h.probed[len(h.probed)-1]
	h.mu.Unlock()
	h.m.mu.Lock()
	if it := h.m.item(old.ID); it != nil {
		cookie = it.Headers["Cookie"]
	}
	h.m.mu.Unlock()
	if h.item(o.ID).ID != "" || h.item(old.ID).URL != o.URL || cookie != "yeni=1" || probed.Get("Cookie") != "yeni=1" {
		t.Fatalf("öneri kaldı ya da başlık taşınmadı: %+v %q", h.item(old.ID), cookie)
	}
}

// Öneri reddedilip yeni indirme olarak başlatılınca işaret düşer.
func TestRefreshOfferClearedOnStart(t *testing.T) {
	h := newHarness(t)
	old, err := h.m.Add(AddArgs{URL: "http://x/a.zip"}) // Probe: boyut 100; kuyrukta yarım bekler
	if err != nil {
		t.Fatal(err)
	}
	o, err := h.m.Add(AddArgs{URL: "http://cdn/a.zip", Name: "a.zip", Size: 100})
	if err != nil || o.RefreshOf != old.ID {
		t.Fatalf("öneri: %+v %v", o, err)
	}
	h.m.Resume([]string{o.ID})
	if it := h.item(o.ID); it.RefreshOf != "" || it.State != StateDownloading {
		t.Fatalf("%+v", it)
	}
}

// Ayarlar > Kaydetme > "Sunucu tarihini kullan" isteğe taşınır; varsayılan kapalı.
func TestServerDateSettingReachesRequest(t *testing.T) {
	h := newHarness(t)
	h.add("a.zip", true)
	h.job("a.zip")
	s := h.m.settings
	if s.UseServerDate {
		t.Fatal("varsayılan kapalı olmalı")
	}
	s.UseServerDate = true
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	h.add("b.zip", true)
	h.job("b.zip")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reqs[0].ServerDate || !h.reqs[1].ServerDate {
		t.Fatalf("ServerDate istekleri: %v %v", h.reqs[0].ServerDate, h.reqs[1].ServerDate)
	}
}

func (m *Manager) runLimit(id string) *Limiter {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r := m.runs[id]; r != nil {
		return r.lim
	}
	return nil
}

func limRate(l *Limiter) float64 { l.mu.Lock(); defer l.mu.Unlock(); return l.rate }

// Dosya başına hız sınırı: kaydedilir, başlarken isteğe gider, çalışan indirmede hemen etkili olur.
func TestItemSpeedLimit(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.bin", true)
	h.job("a.bin")
	if r := limRate(h.m.runLimit(it.ID)); r != 0 {
		t.Fatalf("sınırsız başlamalı, rate %v", r)
	}
	ev, unsub := h.m.Subscribe()
	defer unsub()
	got, err := h.m.SetItemLimit(it.ID, 300)
	if err != nil || got.SpeedLimitKBps != 300 {
		t.Fatalf("%+v %v", got, err)
	}
	if r := limRate(h.m.runLimit(it.ID)); r != 300*1024 {
		t.Fatalf("çalışan indirmeye uygulanmadı: %v", r)
	}
	for e := range ev {
		if e.Event == "item" {
			if e.Item.SpeedLimitKBps != 300 {
				t.Fatalf("%+v", e.Item)
			}
			break
		}
	}
	h.m.Pause([]string{it.ID})
	h.waitState(it.ID, StatePaused)
	h.forget("a.bin")
	h.m.Resume([]string{it.ID})
	h.job("a.bin")
	h.mu.Lock()
	req := h.reqs[len(h.reqs)-1]
	h.mu.Unlock()
	if req.Limit == nil || limRate(req.Limit) != 300*1024 {
		t.Fatalf("devamda sınır isteğe gitmedi: %+v", req.Limit)
	}
	h.m.Pause([]string{it.ID})
	h.waitState(it.ID, StatePaused)
	h.m.Close()
	h.open()
	if got := h.item(it.ID); got.SpeedLimitKBps != 300 {
		t.Fatalf("sınır kaydedilmedi: %+v", got)
	}
	if got, err := h.m.SetItemLimit(it.ID, 0); err != nil || got.SpeedLimitKBps != 0 {
		t.Fatal(got, err)
	}
	if _, err := h.m.SetItemLimit(it.ID, -1); err == nil {
		t.Fatal("negatif sınır reddedilmeli")
	}
	if _, err := h.m.SetItemLimit("yok", 10); err == nil {
		t.Fatal("bilinmeyen öğe hata vermeli")
	}
}

// Host istisnası: hedef bağlantı = min(seçilen, host kuralı); kural yoksa seçilen; geçersiz sayı reddedilir.
func TestHostConnsCapsTarget(t *testing.T) {
	h := newHarness(t)
	s := h.m.settings
	s.MaxConns = 16
	s.HostConns = []HostConn{{Host: "*.hetzner.com", Conns: 2}, {Host: "genis.com", Conns: 32}}
	if err := h.m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	for _, u := range []string{"http://ash-speed.HETZNER.com/a.bin", "http://hetzner.com/b.bin", "http://genis.com/c.bin", "http://baska.com/d.bin"} {
		if _, err := h.m.Add(AddArgs{URL: u, Name: filepath.Base(u), Start: true}); err != nil {
			t.Fatal(err)
		}
	}
	h.mu.Lock()
	var got []int
	for _, r := range h.reqs {
		got = append(got, r.MaxConns)
	}
	h.mu.Unlock()
	if !slices.Equal(got, []int{2, 2, 16, 16}) {
		t.Fatalf("MaxConns=%v", got)
	}
	for _, n := range []int{0, 33} {
		s.HostConns = []HostConn{{Host: "x.com", Conns: n}}
		if err := h.m.SetSettings(s); err == nil {
			t.Fatalf("Conns=%d kabul edildi", n)
		}
	}
}

// İş durdurulamadan başarıyla biterse (ör. video birleştirmesi sürerken Remove) öğe bitmiş sayılır:
// deleteFile ile son dosya Çöp'e gider, diskte listede olmayan sahipsiz dosya kalmaz.
func TestRemoveWhileFinishingTrashesResult(t *testing.T) {
	h := newHarness(t)
	h.hold = true
	it := h.add("v.mp4", true)
	j := h.job("v.mp4")
	final := filepath.Join(h.dl, "v.mp4")
	j.set(Snapshot{Path: final, Size: 5, Got: 5})
	errc := make(chan error, 1)
	go func() { errc <- h.m.Remove([]string{it.ID}, true) }()
	<-j.paused
	j.end(nil) // birleştirme Pause'a bakmadan bitti
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.trashed, []string{final}) {
		t.Fatalf("Çöp'e giden: %v", h.trashed)
	}
}
