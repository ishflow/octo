package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const testBasic = "Basic U0VDUkVUOlBBUk9MQQ==" // base64 diskte görünmemeli

func (h *harness) addAuth(url string, start bool) (Item, error) {
	return h.m.Add(AddArgs{URL: url, Name: "a.bin", Start: start, Headers: map[string]string{"Cookie": "k=v", "Authorization": testBasic}})
}

func (h *harness) diskJSON() string {
	h.t.Helper()
	b, err := os.ReadFile(filepath.Join(h.dir, downloadsFile))
	if err != nil {
		h.t.Fatal(err)
	}
	return string(b)
}

func TestAuthHeaderSentButNeverWrittenToDisk(t *testing.T) {
	h := newHarness(t)
	it, err := h.addAuth("https://x/a.bin", true)
	if err != nil {
		t.Fatal(err)
	}
	h.job("a.bin")
	h.mu.Lock()
	got, cookie := h.reqs[0].Header.Get("Authorization"), h.reqs[0].Header.Get("Cookie")
	h.mu.Unlock()
	if got != testBasic || cookie != "k=v" {
		t.Fatalf("istek başlıkları: Authorization=%q Cookie=%q", got, cookie)
	}
	h.m.progress()
	if d := h.diskJSON(); strings.Contains(d, "U0VDUkVU") || strings.Contains(strings.ToLower(d), "authorization") {
		t.Fatalf("downloads.json'da kimlik bilgisi var:\n%s", d)
	}
	_ = it
}

func TestAuthLostOnRestartStopsWithLoginNeeded(t *testing.T) {
	h := newHarness(t)
	it, err := h.addAuth("https://x/a.bin", false)
	if err != nil {
		t.Fatal(err)
	}
	h.m.Close()
	h.open()
	h.m.Resume([]string{it.ID})
	got := h.item(it.ID)
	if got.State != StateError || !strings.Contains(got.Error, "Giriş gerekli") {
		t.Fatalf("durum=%s hata=%q", got.State, got.Error)
	}
	if h.runCount() != 0 {
		t.Fatal("kimliksiz indirme başlatıldı")
	}
}

func TestAuthRefusedOnPlainHTTP(t *testing.T) {
	h := newHarness(t)
	if _, err := h.addAuth("http://x/a.bin", true); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("Add: %v", err)
	}
	if _, err := h.m.Probe("http://x/a.bin", map[string]string{"Authorization": testBasic}); err == nil || !strings.Contains(err.Error(), "https") {
		t.Fatalf("Probe: %v", err)
	}
	h.mu.Lock()
	n := len(h.probed)
	h.mu.Unlock()
	if n != 0 {
		t.Fatal("düz http'ye probe gitti")
	}
}

func TestAuthDroppedWhenDone(t *testing.T) {
	h := newHarness(t)
	it, _ := h.addAuth("https://x/a.bin", true)
	h.job("a.bin").end(nil)
	h.waitState(it.ID, StateDone)
	h.m.mu.Lock()
	left, need := h.m.item(it.ID).Authorization, h.m.item(it.ID).AuthNeeded
	h.m.mu.Unlock()
	if left != "" || need {
		t.Fatalf("bitince bellekte kaldı: %q %v", left, need)
	}
}

func TestAuthFollowsRefreshOnlyOnSameSite(t *testing.T) {
	h := newHarness(t)
	it, _ := h.addAuth("https://x.com/a.bin", false)
	if _, err := h.m.Refresh(it.ID, "https://x.com/b.bin"); err != nil {
		t.Fatal(err)
	}
	h.m.Resume([]string{it.ID})
	h.job("a.bin")
	h.mu.Lock()
	same := h.reqs[len(h.reqs)-1].Header.Get("Authorization")
	h.mu.Unlock()
	if same != testBasic {
		t.Fatalf("aynı siteye yenilemede Authorization=%q", same)
	}
	h.m.Pause([]string{it.ID})
	h.waitState(it.ID, StatePaused)
	if _, err := h.m.Refresh(it.ID, "https://baska.com/b.bin"); err != nil {
		t.Fatal(err)
	}
	h.m.Resume([]string{it.ID})
	eventually(t, "ikinci başlatma", func() bool { h.mu.Lock(); defer h.mu.Unlock(); return len(h.reqs) == 2 })
	h.mu.Lock()
	other := h.reqs[1].Header.Get("Authorization")
	h.mu.Unlock()
	if other != "" {
		t.Fatalf("başka siteye Authorization gitti: %q", other)
	}
}

func TestAuthInOldDownloadsJSONIsScrubbed(t *testing.T) {
	dir := t.TempDir()
	doc := downloadsDoc{NextID: 1, Items: []*Item{{ID: "d1", Name: "a.bin", Queue: mainQueue, State: StatePaused,
		Headers: map[string]string{"Authorization": testBasic, "Cookie": "k=v"}}}}
	if err := writeJSON(dir, downloadsFile, doc); err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, dir: dir, jobs: map[string]*fakeJob{}}
	h.open()
	if d := h.diskJSON(); strings.Contains(d, "U0VDUkVU") || !strings.Contains(d, "k=v") {
		t.Fatalf("eski kayıt temizlenmedi ya da çerez kayboldu:\n%s", d)
	}
	h.m.Resume([]string{"d1"})
	if got := h.item("d1"); got.State != StateError || !strings.Contains(got.Error, "Giriş gerekli") {
		t.Fatalf("durum=%s hata=%q", got.State, got.Error)
	}
}

// Uçtan uca site girişi: Ayarlar'daki giriş → 401'de Anahtar Zinciri (sahte, hesap = desen) → indirme biter.
// Parola ve base64'ü downloads.json, ayarlar, liste, olaylar ve log'a hiç çıkmaz; yanlış parolada hata yol gösterir.
func TestSiteLoginEndToEnd(t *testing.T) {
	var logs strings.Builder
	log.SetOutput(&logs)
	defer log.SetOutput(os.Stderr)
	data := rnd(256 << 10)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "ali" || p != "s3cr3t" {
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			http.Error(w, "giriş gerekli", http.StatusUnauthorized)
			return
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(data))
	}))
	t.Cleanup(srv.Close)
	tlsCfg := srv.Client().Transport.(*http.Transport).TLSClientConfig
	var mu sync.Mutex
	var asked []string
	pw := "s3cr3t"
	d := DefaultDeps()
	d.Start = func(ctx context.Context, r Request, resume bool) (Job, error) { // Start'ın TLS'e güvenen kopyası
		dl := newDownload(ctx, r, nil)
		dl.client.Transport.(*http.Transport).TLSClientConfig = tlsCfg
		dl.excl = true
		if err := dl.open(); err != nil {
			dl.cancel()
			return nil, err
		}
		go dl.supervise()
		return dl, nil
	}
	d.Probe = func(ctx context.Context, u string, h http.Header, auth AuthFunc) (ProbeResult, error) {
		c := newClient()
		c.Transport.(*http.Transport).TLSClientConfig = tlsCfg
		return probe(ctx, &Download{header: h.Clone(), auth: auth, client: c}, u)
	}
	d.Password = func(account string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		asked = append(asked, account)
		return pw, nil
	}
	dir, out := t.TempDir(), t.TempDir()
	m, err := NewManager(dir, d)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ev, unsub := m.Subscribe()
	defer unsub()
	_, _, s := m.List()
	s.Logins = []Login{{Host: "127.0.0.1", User: "ali"}}
	if err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	wait := func(it Item) Item {
		t.Helper()
		var got Item
		eventually(t, it.Name, func() bool {
			items, _, _ := m.List()
			for _, x := range items {
				if x.ID == it.ID {
					got = x
				}
			}
			return got.State == StateDone || got.State == StateError
		})
		return got
	}
	it, err := m.Add(AddArgs{URL: srv.URL + "/f.bin", Dir: out, Name: "f.bin", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(it); got.State != StateDone {
		t.Fatalf("durum %s: %s", got.State, got.Error)
	}
	checkFile(t, filepath.Join(out, "f.bin"), data)
	mu.Lock()
	if !slices.Equal(asked, []string{"127.0.0.1"}) {
		t.Fatalf("Anahtar Zinciri hesabı: %v (giriş deseni bekleniyordu)", asked)
	}
	pw = "yanlis"
	mu.Unlock()
	it, err = m.Add(AddArgs{URL: srv.URL + "/g.bin", Dir: out, Name: "g.bin", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if got := wait(it); got.State != StateError || !strings.Contains(got.Error, "Site girişleri") {
		t.Fatalf("yanlış parolada durum %s, hata %q (Ayarlar > Site girişleri'ne yönlendirmeli)", got.State, got.Error)
	}
	if _, err := m.Probe(srv.URL+"/g.bin", nil); err == nil || !strings.Contains(err.Error(), "Site girişleri") {
		t.Fatalf("Probe hatası %v (Ayarlar > Site girişleri'ne yönlendirmeli)", err)
	}

	m.Close()
	items, _, set := m.List()
	b, _ := json.Marshal([]any{items, set})
	buf := string(b) + logs.String()
	for len(ev) > 0 {
		b, _ = json.Marshal(<-ev)
		buf += string(b)
	}
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		b, _ := os.ReadFile(filepath.Join(dir, f.Name()))
		buf += string(b)
	}
	for _, secret := range []string{"s3cr3t", "yanlis", "YWxpOnMzY3IzdA", "YWxpOnlhbmxpcw"} {
		if strings.Contains(buf, secret) {
			t.Fatalf("parola sızdı (%s): %s", secret, buf)
		}
	}
}
