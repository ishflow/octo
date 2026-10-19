package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// pauseAt: indirilen bayt boyutun frac'ına ulaşınca duraklatır ve Wait'in ErrPaused döndüğünü doğrular.
func pauseAt(t *testing.T, d *Download, frac float64) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		s := d.Snapshot()
		if s.Size > 0 && float64(s.Got) >= frac*float64(s.Size) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%%%v'a ulaşılamadı: %+v", frac*100, s)
		}
		time.Sleep(5 * time.Millisecond)
	}
	d.Pause()
	if err := d.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait=%v, ErrPaused bekleniyordu", err)
	}
}

func readState(t *testing.T, out string) State {
	t.Helper()
	b, err := os.ReadFile(out + ".part.json")
	if err != nil {
		t.Fatal("durum dosyası yok:", err)
	}
	var st State
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal("durum dosyası bozuk:", err)
	}
	return st
}

func resumeAndWait(t *testing.T, u, out string, n int) *Download {
	t.Helper()
	d, err := Resume(context.Background(), Request{URL: u, Path: out, MaxConns: n}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	return d
}

func checkFile(t *testing.T, out string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("içerik farklı")
	}
	if _, err := os.Stat(out + ".part.json"); !os.IsNotExist(err) {
		t.Fatal("tamamlanınca durum dosyası silinmedi")
	}
}

func startDL(t *testing.T, u, out string, n int) *Download {
	t.Helper()
	d, err := Start(context.Background(), Request{URL: u, Path: out, MaxConns: n}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestResumeAfterPauseMatchesSHA(t *testing.T) {
	data := rnd(20 << 20)
	var resumed atomic.Bool
	var mu sync.Mutex
	var ranges []string
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		if resumed.Load() {
			mu.Lock()
			ranges = append(ranges, r.Header.Get("Range"))
			mu.Unlock()
		}
		return false
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	pauseAt(t, startDL(t, srv.URL, out, 8), 0.4)
	st := readState(t, out)
	if st.Size != int64(len(data)) || len(st.Chunks) == 0 {
		t.Fatalf("durum eksik: size=%d chunks=%d", st.Size, len(st.Chunks))
	}
	var want string
	for _, c := range st.Chunks {
		if c.Pos <= c.End {
			want = fmt.Sprintf("bytes=%d-", c.Pos-min(64, c.Pos-c.Start))
			break
		}
	}
	resumed.Store(true)
	d := resumeAndWait(t, srv.URL, out, 8)
	checkFile(t, out, data)
	if s := d.Snapshot(); s.Restarted {
		t.Fatalf("baştan başlatıldı: %s", s.RestartReason)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(ranges) == 0 || ranges[0] != want {
		t.Fatalf("ilk devam isteği %v, %q bekleniyordu", ranges, want)
	}
	for _, r := range ranges {
		if r == "bytes=0-" {
			t.Fatal("devamda baştan istendi")
		}
	}
}

// Sunucudaki içerik değişti (boyut, tarih aynı): 64 baytlık kuyruk tutmaz, her şey 0'dan iner.
func TestResumeTailMismatchRestarts(t *testing.T) {
	data1, data2 := rnd(20<<20), rnd(20<<20)
	var cur atomic.Pointer[[]byte]
	cur.Store(&data1)
	srv, _ := serve(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		http.ServeContent(w, r, "f.bin", time.Time{}, slowReader{bytes.NewReader(*cur.Load())})
		return true
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	pauseAt(t, startDL(t, srv.URL, out, 8), 0.4)
	cur.Store(&data2)
	d := resumeAndWait(t, srv.URL, out, 8)
	checkFile(t, out, data2)
	if s := d.Snapshot(); !s.Restarted || s.RestartReason == "" {
		t.Fatalf("baştan başlatma bildirilmedi: %+v", s.RestartReason)
	}
}

func TestResumeChangedLastModifiedRestarts(t *testing.T) {
	data := rnd(10 << 20)
	var mod atomic.Int64
	mod.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	srv, _ := serve(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		http.ServeContent(w, r, "f.bin", time.Unix(mod.Load(), 0), slowReader{bytes.NewReader(data)})
		return true
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	pauseAt(t, startDL(t, srv.URL, out, 8), 0.4)
	if readState(t, out).LastModified == "" {
		t.Fatal("Last-Modified durum dosyasına yazılmadı")
	}
	mod.Add(3600)
	d := resumeAndWait(t, srv.URL, out, 8)
	checkFile(t, out, data)
	if s := d.Snapshot(); !s.Restarted || !strings.Contains(s.RestartReason, "Last-Modified") {
		t.Fatalf("Last-Modified değişimi baştan başlatmadı: %q", s.RestartReason)
	}
}

// Devam isteğine 200: sunucu artık aralık desteklemiyor, 0'dan ve Resumable=false.
func TestResume200Restarts(t *testing.T) {
	data := rnd(10 << 20)
	var noRange atomic.Bool
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		if noRange.Load() {
			w.Header().Set("Content-Length", strconv.Itoa(len(data))) // boyutsuz 200 reddedilir (TestResumeWrongContentKeepsPart)
			w.Write(data)
			return true
		}
		return false
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	pauseAt(t, startDL(t, srv.URL, out, 8), 0.4)
	noRange.Store(true)
	d := resumeAndWait(t, srv.URL, out, 8)
	checkFile(t, out, data)
	if s := d.Snapshot(); !s.Restarted || s.Resumable {
		t.Fatalf("200 sonrası Restarted=%v Resumable=%v", s.Restarted, s.Resumable)
	}
}

// %0'da duraklatma (hiç bayt gelmeden), sonra devam; ayrıca boş parça listeli durum dosyası.
func TestPauseBeforeFirstByte(t *testing.T) {
	data := rnd(4 << 20)
	var held atomic.Bool
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		if !held.CompareAndSwap(false, true) {
			return false
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return true
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	d := startDL(t, srv.URL, out, 8)
	d.Pause()
	if err := d.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait=%v", err)
	}
	if s := d.Snapshot(); s.Got != 0 {
		t.Fatalf("Got=%d", s.Got)
	}
	if st := readState(t, out); len(st.Chunks) != 1 || st.Chunks[0] != (Seg{0, 0, int64(len(data)) - 1, ""}) {
		t.Fatalf("durum: %+v", st.Chunks)
	}
	resumeAndWait(t, srv.URL, out, 8)
	checkFile(t, out, data)

	// Parça listesi boş durum dosyası ve .part yok: 0'dan başlamalı.
	out2 := filepath.Join(t.TempDir(), "g.bin")
	if err := os.WriteFile(out2+".part.json", []byte(`{"size":5,"chunks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	resumeAndWait(t, srv.URL, out2, 8)
	checkFile(t, out2, data)
}

// Devamda sunucu başka içerik verirse (giriş sayfası, farklı boyut) yarım dosya kesilmez; Türkçe hata döner.
func TestResumeWrongContentKeepsPart(t *testing.T) {
	cases := []struct {
		name, want string
		reply      func(w http.ResponseWriter, r *http.Request)
	}{
		{"200 html", "web sayfası", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte("<html>giriş yapın</html>"))
		}},
		{"200 farklı boyut", "boyut", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			w.Write(rnd(1000))
		}},
		{"200 boyutsuz", "boyutunu bildirmedi", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/octet-stream")
			w.(http.Flusher).Flush() // Content-Length yok: chunked
			w.Write([]byte(`{"error":"token expired"}`))
		}},
		{"206 farklı boyut", "boyut", func(w http.ResponseWriter, r *http.Request) {
			http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(rnd(3<<20)))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data := rnd(10 << 20)
			var other atomic.Bool
			srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
				if other.Load() {
					tc.reply(w, r)
					return true
				}
				return false
			})
			out := filepath.Join(t.TempDir(), "f.bin")
			pauseAt(t, startDL(t, srv.URL, out, 4), 0.4)
			before, _ := os.ReadFile(out + ".part")
			other.Store(true)
			d, err := Resume(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 4}, nil)
			if err == nil {
				d.Wait()
				t.Fatal("hata bekleniyordu, indirme sürdü")
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "yarım dosya korundu") {
				t.Fatalf("hata=%q", err)
			}
			after, _ := os.ReadFile(out + ".part")
			if !bytes.Equal(before, after) {
				t.Fatal(".part değişti")
			}
			readState(t, out)
		})
	}
}

// Sunucu 401 dönünce site girişi (Basic auth) bir kez sorulur; sonraki bağlantılar başlığı taşır.
// Giriş yalnız https'te gider (F2GR M2): sunucu TLS, istemci httptest sertifikasına güvenir.
func TestBasicAuthOn401(t *testing.T) {
	data := rnd(8 << 20)
	var denied atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "ali" || p != "s3cr3t" {
			denied.Add(1)
			w.Header().Set("WWW-Authenticate", `Basic realm="x"`)
			http.Error(w, "giriş gerekli", http.StatusUnauthorized)
			return
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, slowReader{bytes.NewReader(data)})
	}))
	t.Cleanup(srv.Close)
	startTLS := func(r Request) (*Download, error) { // start'ın TLS'e güvenen kopyası
		d := newDownload(context.Background(), r, nil)
		d.client.Transport.(*http.Transport).TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
		d.excl = true
		if err := d.open(); err != nil {
			d.cancel()
			return nil, err
		}
		go d.supervise()
		return d, nil
	}
	var asked atomic.Int32
	auth := func(host string) (string, string, bool) {
		asked.Add(1)
		if host != "127.0.0.1" {
			t.Errorf("host=%q", host)
		}
		return "ali", "s3cr3t", true
	}
	out := filepath.Join(t.TempDir(), "f.bin")
	d, err := startTLS(Request{URL: srv.URL, Path: out, MaxConns: 8, Auth: auth})
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	checkFile(t, out, data)
	if denied.Load() != 1 || asked.Load() != 1 {
		t.Fatalf("401 sayısı %d, giriş sorma %d (1 ve 1 bekleniyordu)", denied.Load(), asked.Load())
	}
	if _, err := startTLS(Request{URL: srv.URL, Path: out + "2"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("girişsiz Start=%v", err)
	}
}

func TestStartCreatesMissingDir(t *testing.T) {
	data := rnd(100 << 10)
	srv, _ := serve(t, data, nil)
	out := filepath.Join(t.TempDir(), "Arşiv", "alt", "f.bin")
	d, err := Start(context.Background(), Request{URL: srv.URL, Path: out}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	checkFile(t, out, data)

	file := filepath.Join(t.TempDir(), "dosya")
	os.WriteFile(file, nil, 0o644)
	_, err = Start(context.Background(), Request{URL: srv.URL, Path: filepath.Join(file, "f.bin")}, nil)
	if err == nil || !strings.Contains(err.Error(), "klasör oluşturulamadı") {
		t.Fatalf("hata=%v", err)
	}
}

// Content-Range'i okunamayan 206 boyutu 0 yapmamalı: Türkçe hata.
func TestBadContentRangeFails(t *testing.T) {
	srv, _ := serve(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Range", "bozuk")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("abc"))
		return true
	})
	_, err := Start(context.Background(), Request{URL: srv.URL, Path: filepath.Join(t.TempDir(), "f.bin")}, nil)
	if err == nil || !strings.Contains(err.Error(), "aralık") {
		t.Fatalf("hata=%v", err)
	}
}

// Sunucu her 3. aralık isteğine 404 (bağlantı sınırı): ek bağlantı düşer, indirme yine tamamlanır.
func TestExtraConn404DropsConnection(t *testing.T) {
	data := rnd(20 << 20)
	var n atomic.Int32
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Range") != "" && n.Add(1)%3 == 0 {
			http.NotFound(w, r)
			return true
		}
		return false
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	d := startDL(t, srv.URL, out, 8)
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	checkFile(t, out, data)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.maxConn >= 8 {
		t.Fatalf("404'ten sonra maxConn düşmedi: %d", d.maxConn)
	}
}

// Fix 1: 0'dan yeniden başlatmanın ilk isteği sürerken Pause → Wait ErrPaused (context canceled değil).
func TestPauseDuringRestartGET(t *testing.T) {
	data1, data2 := rnd(20<<20), rnd(20<<20)
	var cur atomic.Pointer[[]byte]
	cur.Store(&data1)
	var swapped atomic.Bool
	hit := make(chan struct{}, 1)
	srv, _ := serve(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		if swapped.Load() && r.Header.Get("Range") == "bytes=0-" { // yeniden başlatmanın isteği bekletilir
			select {
			case hit <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return true
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, slowReader{bytes.NewReader(*cur.Load())})
		return true
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	pauseAt(t, startDL(t, srv.URL, out, 8), 0.4)
	cur.Store(&data2)
	swapped.Store(true)
	d, err := Resume(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-hit:
	case <-time.After(10 * time.Second):
		t.Fatal("yeniden başlatma isteği gelmedi")
	}
	d.Pause()
	if err := d.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait=%v, ErrPaused bekleniyordu", err)
	}
}

// Fix 2: '?' içeren adreste Last-Modified değişimi yok sayılır, ETag değişimi yine 0'dan başlatır.
func TestResumeQueryURLValidators(t *testing.T) {
	for _, tc := range []struct {
		name        string
		etagChanges bool
		wantRestart bool
	}{
		{"tarih değişti, ETag aynı", false, false},
		{"ETag değişti", true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := rnd(10 << 20)
			var mod atomic.Int64
			mod.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
			var etag atomic.Value
			etag.Store(`"v1"`)
			srv, _ := serve(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
				w.Header().Set("ETag", etag.Load().(string))
				http.ServeContent(w, r, "f.bin", time.Unix(mod.Load(), 0), slowReader{bytes.NewReader(data)})
				return true
			})
			u := srv.URL + "/f.bin?token=1"
			out := filepath.Join(t.TempDir(), "f.bin")
			pauseAt(t, startDL(t, u, out, 8), 0.4)
			mod.Add(3600)
			if tc.etagChanges {
				etag.Store(`"v2"`)
			}
			d := resumeAndWait(t, u, out, 8)
			checkFile(t, out, data)
			if s := d.Snapshot(); s.Restarted != tc.wantRestart {
				t.Fatalf("Restarted=%v (%q), %v bekleniyordu", s.Restarted, s.RestartReason, tc.wantRestart)
			}
		})
	}
}

// Fix 3: ilk bağlantıda 403/404/410 hemen ölümcül (20 yeniden deneme yok).
func TestFirstConnForbiddenFatal(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusGone} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			data := rnd(4 << 20)
			var first atomic.Bool
			srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
				if first.CompareAndSwap(false, true) { // ilk yanıt yarıda kesilir
					w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
					w.Header().Set("Content-Length", fmt.Sprint(len(data)))
					w.WriteHeader(http.StatusPartialContent)
					w.Write(data[:100<<10])
					return true
				}
				http.Error(w, "yok", code)
				return true
			})
			out := filepath.Join(t.TempDir(), "f.bin")
			d := startDL(t, srv.URL, out, 1)
			res := make(chan error, 1)
			go func() { res <- d.Wait() }()
			select {
			case err := <-res:
				if err == nil || errors.Is(err, ErrPaused) {
					t.Fatalf("Wait=%v, hata bekleniyordu", err)
				}
			case <-time.After(time.Second):
				d.Pause()
				t.Fatal("Wait 1 sn içinde dönmedi")
			}
		})
	}
}

// Fix 4: bozuk durum dosyası paniğe yol açmaz, durum yokmuş gibi 0'dan başlanır.
func TestResumeCorruptStateStartsFresh(t *testing.T) {
	data := rnd(1 << 20)
	n := int64(len(data))
	srv, _ := serve(t, data, nil)
	for name, chunks := range map[string]string{
		"pos sonun ötesinde":    fmt.Sprintf(`[{"start":0,"pos":%d,"end":%d}]`, n+5, n-1),
		"negatif başlangıç":     fmt.Sprintf(`[{"start":-5,"pos":10,"end":%d}]`, n-1),
		"pos başlangıçtan önce": fmt.Sprintf(`[{"start":100,"pos":50,"end":%d},{"start":0,"pos":0,"end":99}]`, n-1),
		"boyuttan taşan":        fmt.Sprintf(`[{"start":0,"pos":10,"end":%d}]`, n+100),
		"çakışan":               fmt.Sprintf(`[{"start":0,"pos":500,"end":1000},{"start":900,"pos":900,"end":%d}]`, n-1),
		"boşluklu":              fmt.Sprintf(`[{"start":0,"pos":500,"end":1000},{"start":2000,"pos":2000,"end":%d}]`, n-1),
		"sırasız":               fmt.Sprintf(`[{"start":1001,"pos":1001,"end":%d},{"start":0,"pos":1001,"end":1000}]`, n-1),
	} {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(t.TempDir(), "f.bin")
			if err := os.WriteFile(out+".part", bytes.Repeat([]byte{0xAA}, int(n)), 0o644); err != nil {
				t.Fatal(err)
			}
			st := fmt.Sprintf(`{"size":%d,"chunks":%s}`, n, chunks)
			if err := os.WriteFile(out+".part.json", []byte(st), 0o644); err != nil {
				t.Fatal(err)
			}
			done := make(chan *Download, 1)
			go func() {
				d, err := Resume(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 4}, nil)
				if err == nil {
					err = d.Wait()
				}
				if err != nil {
					t.Error(err)
				}
				done <- d
			}()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("Resume takıldı")
			}
			if !t.Failed() {
				checkFile(t, out, data)
			}
		})
	}
}

// Fix 2. tur: 8 bağlantıda ilk bağlantının 403'ü tüm işçileri hemen durdurur; sonra sunucuya istek gitmez.
func TestFirstConnForbiddenStopsAllConns(t *testing.T) {
	data := rnd(20 << 20)
	var dead atomic.Bool
	var deadAt atomic.Int64
	var reqs atomic.Int32
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		reqs.Add(1)
		if dead.Load() {
			http.Error(w, "yasak", http.StatusForbidden)
			return true
		}
		if r.Header.Get("Range") != "bytes=0-" {
			return false // ek bağlantılar normal, yavaş akar
		}
		// İlk yanıt 4 MB'ta kesilir; sonra her istek 403.
		w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(http.StatusPartialContent)
		for off := 0; off < 4<<20; off += 32 << 10 {
			if _, err := w.Write(data[off : off+32<<10]); err != nil {
				return true
			}
			time.Sleep(16 * time.Millisecond)
		}
		deadAt.Store(time.Now().UnixNano())
		dead.Store(true)
		return true
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	d := startDL(t, srv.URL, out, 8)
	res := make(chan error, 1)
	go func() { res <- d.Wait() }()
	select {
	case err := <-res:
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Fatalf("Wait=%v, 403 hatası bekleniyordu", err)
		}
		if took := time.Since(time.Unix(0, deadAt.Load())); took > time.Second {
			t.Fatalf("403'ten sonra Wait %v sürdü", took)
		}
	case <-time.After(15 * time.Second):
		d.Pause()
		t.Fatal("Wait dönmedi")
	}
	n := reqs.Load()
	time.Sleep(500 * time.Millisecond)
	if m := reqs.Load(); m != n {
		t.Fatalf("Wait döndükten sonra sunucuya %d istek daha gitti", m-n)
	}
}
