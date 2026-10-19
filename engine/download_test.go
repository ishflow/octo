package engine

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// slowReader: bağlantı başına hız sınırı (sunucu tarafı tek bağlantı darboğazı gibi).
type slowReader struct{ *bytes.Reader }

func (s slowReader) Read(p []byte) (int, error) {
	if len(p) > 32<<10 {
		p = p[:32<<10]
	}
	time.Sleep(16 * time.Millisecond) // ~2 MB/s bağlantı başına
	return s.Reader.Read(p)
}

func serve(t *testing.T, data []byte, h func(w http.ResponseWriter, r *http.Request) bool) (*httptest.Server, *int32) {
	var peak, cur int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&cur, 1)
		defer atomic.AddInt32(&cur, -1)
		for p := atomic.LoadInt32(&peak); n > p && !atomic.CompareAndSwapInt32(&peak, p, n); p = atomic.LoadInt32(&peak) {
		}
		if h != nil && h(w, r) {
			return
		}
		http.ServeContent(w, r, "f.bin", time.Time{}, slowReader{bytes.NewReader(data)})
	}))
	t.Cleanup(srv.Close)
	return srv, &peak
}

func download(t *testing.T, u string, n int) []byte {
	out := filepath.Join(t.TempDir(), "f.bin")
	d, err := Start(context.Background(), Request{URL: u, Path: out, MaxConns: n}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestContentRange(t *testing.T) {
	if s, n, ok := contentRange("bytes 100-199/1000"); !ok || s != 100 || n != 1000 {
		t.Fatal(s, n, ok)
	}
	if _, _, ok := contentRange("bytes */1000"); ok {
		t.Fatal("geçersiz kabul edildi")
	}
}

func rnd(n int) []byte {
	b := make([]byte, n)
	rand.Read(b)
	return b
}

func TestMultiConnection(t *testing.T) {
	data := rnd(40 << 20)
	srv, peak := serve(t, data, nil)
	start := time.Now()
	got := download(t, srv.URL+"/f.bin", 8)
	if !bytes.Equal(got, data) {
		t.Fatal("içerik farklı")
	}
	if *peak < 8 {
		t.Fatalf("en fazla %d bağlantı açıldı, 8 bekleniyordu", *peak)
	}
	t.Logf("40 MB, %v, en fazla %d bağlantı", time.Since(start), *peak)
}

func TestNoRangeSupport(t *testing.T) {
	data := rnd(3 << 20)
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		w.Write(data) // Range yok sayılır, 200
		return true
	})
	if !bytes.Equal(download(t, srv.URL+"/x", 8), data) {
		t.Fatal("içerik farklı")
	}
}

// Bir bağlantı biraz veri gönderip donuyor: boştaki bağlantı parçayı devralmalı.
func TestStalledConnectionTakenOver(t *testing.T) {
	data := rnd(20 << 20)
	var once sync.Once
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		stall := false
		if !strings.HasPrefix(r.Header.Get("Range"), "bytes=0-") {
			once.Do(func() { stall = true })
		}
		if !stall {
			return false
		}
		var from int
		fmt.Sscanf(r.Header.Get("Range"), "bytes=%d-", &from)
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", from, len(data)-1, len(data)))
		w.WriteHeader(http.StatusPartialContent)
		w.Write(data[from : from+100<<10])
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return true
	})
	start := time.Now()
	if !bytes.Equal(download(t, srv.URL+"/f.bin", 8), data) {
		t.Fatal("içerik farklı")
	}
	if d := time.Since(start); d > 15*time.Second {
		t.Fatalf("takılan parça devralınmadı, %v sürdü", d)
	}
}

func TestHeadersForwarded(t *testing.T) {
	data := rnd(6 << 20)
	var mu sync.Mutex
	var seen []http.Header
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		seen = append(seen, r.Header.Clone())
		mu.Unlock()
		return false
	})
	h := http.Header{}
	h.Set("Referer", "R")
	h.Set("Cookie", "C")
	h.Set("User-Agent", "U")
	out := filepath.Join(t.TempDir(), "f.bin")
	d, err := Start(context.Background(), Request{URL: srv.URL, Header: h, Path: out, MaxConns: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 {
		t.Fatalf("yalnız %d istek görüldü", len(seen))
	}
	for i, got := range seen {
		if got.Get("Referer") != "R" || got.Get("Cookie") != "C" || got.Get("User-Agent") != "U" {
			t.Fatalf("istek %d başlıkları eksik: %v", i, got)
		}
		if got.Get("Accept-Encoding") != "identity" {
			t.Fatalf("istek %d Accept-Encoding=%q", i, got.Get("Accept-Encoding"))
		}
	}
	if h.Get("Accept-Encoding") != "" {
		t.Fatal("çağıranın başlık haritası değiştirildi")
	}
}

func TestDefaultUserAgent(t *testing.T) {
	var ua atomic.Value
	srv, _ := serve(t, rnd(1<<20), func(w http.ResponseWriter, r *http.Request) bool {
		ua.Store(r.Header.Get("User-Agent"))
		return false
	})
	download(t, srv.URL, 2)
	if ua.Load() != "octo/1.0" {
		t.Fatalf("UA=%v", ua.Load())
	}
}

// Ayarlardaki User-Agent boş başlıkta kullanılır; tarayıcının gönderdiği ise ezilmez. Boş/geçersiz değer varsayılana düşer.
func TestConfiguredUserAgent(t *testing.T) {
	for _, tc := range []struct{ name, cfg, hdr, want string }{
		{"ayar", "Ozel/2.0", "", "Ozel/2.0"},
		{"tarayıcı kazanır", "Ozel/2.0", "Tarayici/9", "Tarayici/9"},
		{"boş", "", "", "octo/1.0"},
		{"boşluk", "  \t", "", "octo/1.0"},
		{"satır sonu", "A\r\nX: y", "", "octo/1.0"},
		{"kırpılır", "  Ozel/2.0 ", "", "Ozel/2.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ua atomic.Value
			srv, _ := serve(t, rnd(1<<20), func(w http.ResponseWriter, r *http.Request) bool {
				ua.Store(r.Header.Get("User-Agent"))
				return false
			})
			h := http.Header{}
			if tc.hdr != "" {
				h.Set("User-Agent", tc.hdr)
			}
			d, err := Start(context.Background(), Request{URL: srv.URL, Header: h, UserAgent: tc.cfg, Path: filepath.Join(t.TempDir(), "f.bin"), MaxConns: 2}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Wait(); err != nil {
				t.Fatal(err)
			}
			if ua.Load() != tc.want {
				t.Fatalf("UA=%v, istenen %q", ua.Load(), tc.want)
			}
		})
	}
}

func TestPauseStopsAndKeepsPart(t *testing.T) {
	data := rnd(20 << 20)
	srv, _ := serve(t, data, nil)
	out := filepath.Join(t.TempDir(), "f.bin")
	d, err := Start(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	d.Pause()
	res := make(chan error, 1)
	go func() { res <- d.Wait() }()
	select {
	case err := <-res:
		if !errors.Is(err, ErrPaused) {
			t.Fatalf("Wait=%v, ErrPaused bekleniyordu", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Wait 2 sn içinde dönmedi")
	}
	if _, err := os.Stat(out + ".part"); err != nil {
		t.Fatal(".part yok:", err)
	}
	if _, err := os.Stat(out); err == nil {
		t.Fatal("tamamlanmamış dosya asıl adla yazıldı")
	}
	// Durum dosyası imzalı adresteki anahtarı taşır: yalnız kullanıcı okur (B27 L4).
	if fi, err := os.Stat(out + ".part.json"); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf(".part.json izni: %v %v", fi.Mode(), err)
	}
	// .part önceden tam boyda açıldığı için boyut değil içerik karşılaştırılır.
	a, _ := os.ReadFile(out + ".part")
	time.Sleep(500 * time.Millisecond)
	b, _ := os.ReadFile(out + ".part")
	if !bytes.Equal(a, b) {
		t.Fatal("duraklatmadan sonra dosyaya yazılmaya devam ediliyor")
	}
	if s := d.Snapshot(); s.Got <= 0 || s.Got >= s.Size {
		t.Fatalf("anlık durum tutarsız: %+v", s)
	}
}

func TestEmptyPathUsesServerName(t *testing.T) {
	data := rnd(1 << 20)
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Disposition", `attachment; filename="srv.bin"`)
		return false
	})
	t.Chdir(t.TempDir())
	d, err := Start(context.Background(), Request{URL: srv.URL}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile("srv.bin")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("sunucudaki adla dosya yazılmadı", err)
	}
}

func TestSameSite(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "dl.example.com", true},
		{"EXAMPLE.com", "cdn.dl.example.com", true},
		{"dl.example.com", "example.com", false},
		{"example.com", "evilexample.com", false},
		{"localhost", "127.0.0.1", false},
	} {
		if got := sameSite(c.a, c.b); got != c.want {
			t.Errorf("sameSite(%q,%q)=%v", c.a, c.b, got)
		}
	}
}

// Yönlendirme: A sunucusu B'ye yönlendirir. İsim eşlemesi: "localhost" ve "127.0.0.1" aynı
// makineyi gösteren iki farklı site sayılır. Aynı ad + farklı port = aynı site (Cookie kalır),
// localhost -> 127.0.0.1 = yabancı site (Cookie düşer). B'nin TÜM aralık istekleri denetlenir.
func TestRedirectCookiePolicy(t *testing.T) {
	for _, tc := range []struct {
		name       string
		hostA      string // A'ya bu adla gidilir
		hostB      string // A, B'ye bu adla yönlendirir
		wantCookie bool
	}{
		{"aynı site", "127.0.0.1", "127.0.0.1", true},
		{"yabancı site", "localhost", "127.0.0.1", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := rnd(6 << 20)
			var mu sync.Mutex
			var withCookie, total int
			srvB, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
				mu.Lock()
				total++
				if r.Header.Get("Cookie") == "C" {
					withCookie++
				}
				mu.Unlock()
				if (r.Header.Get("Cookie") == "C") != tc.wantCookie {
					http.Error(w, "cookie politikası", http.StatusForbidden)
					return true
				}
				return false
			})
			portB := strings.Split(srvB.URL, ":")[2]
			srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "http://"+tc.hostB+":"+portB+"/f.bin", http.StatusFound)
			}))
			t.Cleanup(srvA.Close)
			portA := strings.Split(srvA.URL, ":")[2]
			h := http.Header{}
			h.Set("Cookie", "C")
			out := filepath.Join(t.TempDir(), "f.bin")
			d, err := Start(context.Background(), Request{URL: "http://" + tc.hostA + ":" + portA + "/", Header: h, Path: out, MaxConns: 4}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Wait(); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			defer mu.Unlock()
			if total < 2 {
				t.Fatalf("B'ye yalnız %d istek geldi", total)
			}
			if tc.wantCookie && withCookie != total || !tc.wantCookie && withCookie != 0 {
				t.Fatalf("B isteklerinin %d/%d'sinde Cookie var, wantCookie=%v", withCookie, total, tc.wantCookie)
			}
		})
	}
}

// Sınırlayıcı beklemesi idle zamanlayıcısını tetiklememeli: bağlantı yeniden kurulmamalı.
func TestThrottledNoIdleReconnect(t *testing.T) {
	old := readTimeout
	readTimeout = 300 * time.Millisecond
	t.Cleanup(func() { readTimeout = old })
	data := rnd(100 << 10)
	var reqs atomic.Int32
	srv, _ := serve(t, data, func(http.ResponseWriter, *http.Request) bool { reqs.Add(1); return false })
	lim := NewLimiter()
	lim.Set(20 << 10)
	out := filepath.Join(t.TempDir(), "f.bin")
	d, err := Start(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 1}, lim) // tek bağlantı: 2 sn sessiz parça devralma devreye girmez
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, data) {
		t.Fatal("içerik bozuk")
	}
	if n := reqs.Load(); n != 1 {
		t.Fatalf("%d istek: sınırlayıcı beklemesi bağlantıyı yeniden kurdurdu", n)
	}
}

func TestPauseWhileThrottled(t *testing.T) {
	srv, _ := serve(t, rnd(1<<20), nil)
	lim := NewLimiter()
	lim.Set(1 << 10)
	d, err := Start(context.Background(), Request{URL: srv.URL, Path: filepath.Join(t.TempDir(), "f.bin"), MaxConns: 4}, lim)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	t0 := time.Now()
	d.Pause()
	if err := d.Wait(); !errors.Is(err, ErrPaused) || time.Since(t0) > 200*time.Millisecond {
		t.Fatalf("Wait=%v, %v sonra", err, time.Since(t0))
	}
}

// Yeni indirme canlı bir .part'ı asla ezmez (O_EXCL); Resume ise yarım .part'ı sürdürür.
func TestStartRefusesExistingPart(t *testing.T) {
	srv, _ := serve(t, rnd(1<<16), nil)
	out := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(out+".part", []byte("canlı"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Start(context.Background(), Request{URL: srv.URL, Path: out}, nil); err == nil {
		t.Fatal("var olan .part ile Start hata vermeli")
	}
	if b, _ := os.ReadFile(out + ".part"); string(b) != "canlı" {
		t.Fatal(".part ezildi")
	}
}

// https → http (aynı ana makine) yönlendirmesinde Cookie/Authorization düz HTTP'ye gitmez; tarayıcı Secure
// çerezi http'ye asla yollamaz (B27 M1). Yönlendirmenin kendisi ve sonraki aralık istekleri denetlenir.
func TestHTTPSDowngradeDropsCredentials(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Cookie")+r.Header.Get("Authorization"))
		mu.Unlock()
		w.Write([]byte("veri"))
	}))
	t.Cleanup(plain.Close)
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/f.bin", http.StatusFound) // ikisi de 127.0.0.1: aynı site sayılır
	}))
	t.Cleanup(secure.Close)
	c := newClient()
	c.Transport.(*http.Transport).TLSClientConfig = secure.Client().Transport.(*http.Transport).TLSClientConfig
	d := &Download{header: http.Header{"Cookie": {"s=gizli"}, "Authorization": {"Basic eDp5"}}, client: c}
	resp, err := d.do(context.Background(), secure.URL+"/f.bin", "bytes=0-")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	d.follow(resp, secure.URL+"/f.bin")
	if !strings.HasPrefix(d.url, "http://") || d.header.Get("Cookie") != "" || d.header.Get("Authorization") != "" {
		t.Fatalf("url=%s başlıklar=%v", d.url, d.header)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 1 || seen[0] != "" {
		t.Fatalf("düz HTTP sunucusu kimlik bilgisi aldı: %q", seen)
	}
}

// Kayıtlı site girişi düz HTTP'ye hiç gitmez (SEC1R N1, F2GR M2): https → http düşüşünde, doğrudan http'de ve
// MITM'in http → başka host'a yönlendirmesinde parola sorulmaz; öğe "güvensiz bağlantı" hatasıyla durur.
// Cookie/Authorization/Referer de düz HTTP'deki başka host'a gitmez. Probe da aynı do yolunu kullanır.
func TestAuthNeverOverPlainHTTP(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Cookie")+r.Header.Get("Authorization")+r.Header.Get("Referer"))
		mu.Unlock()
		http.Error(w, "giriş", http.StatusUnauthorized)
	}))
	t.Cleanup(plain.Close)
	portB := strings.Split(plain.URL, ":")[2]
	secure := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, plain.URL+"/f.bin", http.StatusFound)
	}))
	t.Cleanup(secure.Close)
	mitm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://localhost:"+portB+"/f.bin", http.StatusFound) // başka host
	}))
	t.Cleanup(mitm.Close)
	c := newClient()
	c.Transport.(*http.Transport).TLSClientConfig = secure.Client().Transport.(*http.Transport).TLSClientConfig
	var asked atomic.Int32
	auth := func(string) (string, string, bool) { asked.Add(1); return "u", "pw", true }
	for _, u := range []string{secure.URL + "/f.bin", plain.URL + "/f.bin", mitm.URL + "/f.bin"} {
		d := &Download{header: http.Header{"Cookie": {"s=1"}, "Referer": {"https://page.example/secret?tok=1"}}, auth: auth, client: c}
		if resp, err := d.do(context.Background(), u, "bytes=0-"); err == nil || !strings.Contains(err.Error(), "güvensiz bağlantı") {
			if resp != nil {
				resp.Body.Close()
			}
			t.Fatalf("%s: err=%v", u, err)
		}
	}
	if _, err := Probe(context.Background(), mitm.URL+"/f.bin", nil, auth); err == nil || !strings.Contains(err.Error(), "güvensiz bağlantı") {
		t.Fatalf("Probe err=%v", err)
	}
	if asked.Load() != 0 {
		t.Fatalf("düz HTTP için giriş soruldu (%d)", asked.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 4 {
		t.Fatalf("istek=%d", len(seen))
	}
	for i, h := range seen {
		if strings.Contains(h, "Basic") || i != 1 && strings.Contains(h, "s=1") { // 1: doğrudan istek, çağıranın çerezi gider
			t.Fatalf("düz HTTP sunucusu aldı: %q", seen)
		}
	}
}

// 401 girişi yalnız kendi host'una gider (F2GR M1): A her isteği B'ye yönlendirir, B giriş ister. Giriş B için
// bir kez sorulur; A'ya giden sonraki istekler (Resume'un baştan başlaması, işçi bağlantıları) parolayı taşımaz,
// A'nın çerezi silinmez. B A'nın çerezini almaz, Referer köken biçimine iner (SEC1R N2).
func TestAuthStaysWithItsHost(t *testing.T) {
	var mu sync.Mutex
	var seenA, seenB []http.Header
	b := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenB = append(seenB, r.Header.Clone())
		mu.Unlock()
		if r.Header.Get("Authorization") == "" {
			http.Error(w, "giriş", http.StatusUnauthorized)
			return
		}
		w.Write([]byte("veri"))
	}))
	t.Cleanup(b.Close)
	portB := strings.Split(b.URL, ":")[2]
	a := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seenA = append(seenA, r.Header.Clone())
		mu.Unlock()
		http.Redirect(w, r, "https://b.example.com:"+portB+"/f.bin", http.StatusFound) // sertifika *.example.com'u tutar
	}))
	t.Cleanup(a.Close)
	c := newClient()
	tr := c.Transport.(*http.Transport)
	tr.TLSClientConfig = a.Client().Transport.(*http.Transport).TLSClientConfig
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.Replace(addr, "b.example.com", "127.0.0.1", 1))
	}
	var asked atomic.Int32
	auth := func(host string) (string, string, bool) {
		asked.Add(1)
		if host != "b.example.com" {
			t.Errorf("host=%q", host)
		}
		return "u", "pw", true
	}
	d := &Download{url: a.URL + "/dl", header: http.Header{"Cookie": {"a=1"}, "Referer": {a.URL + "/page?tok=1"}}, auth: auth, client: c}
	for range 2 { // follow çağrılmadan d.url'ye (A) yeniden istek
		resp, err := d.get(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("durum=%d", resp.StatusCode)
		}
	}
	if asked.Load() != 1 {
		t.Fatalf("giriş %d kez soruldu (1 bekleniyordu)", asked.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	for _, h := range seenA {
		if h.Get("Authorization") != "" || h.Get("Cookie") != "a=1" {
			t.Fatalf("A aldı: Authorization=%q Cookie=%q", h.Get("Authorization"), h.Get("Cookie"))
		}
	}
	if len(seenA) != 2 || len(seenB) != 4 || seenB[1].Get("Authorization") == "" || seenB[3].Get("Authorization") == "" {
		t.Fatalf("A=%d B=%d istek", len(seenA), len(seenB))
	}
	for _, h := range seenB {
		if h.Get("Cookie") != "" || h.Get("Referer") != a.URL+"/" {
			t.Fatalf("B aldı: Cookie=%q Referer=%q", h.Get("Cookie"), h.Get("Referer"))
		}
	}
}

// tlsClient: httptest sertifikasına güvenir, *.example.com adlarını 127.0.0.1'e çevirir (sertifika onları tutar).
func tlsClient(srv *httptest.Server) *http.Client {
	c := newClient()
	tr := c.Transport.(*http.Transport)
	tr.TLSClientConfig = srv.Client().Transport.(*http.Transport).TLSClientConfig
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, _ := net.SplitHostPort(addr)
		if strings.HasSuffix(host, "example.com") {
			addr = net.JoinHostPort("127.0.0.1", port)
		}
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	return c
}

// Giriş yalnız aynı kökene (şema+host+port) yönlendirmede kalır (F3GR L1, Fetch kuralı): example.com'un girişi
// aynı kökendeki /real'e gider, ama alt alan adına ve aynı adın başka portuna gitmez. Tarayıcının verdiği
// Authorization da aynı kuralla düşer.
func TestAuthOnlyToSameOrigin(t *testing.T) {
	var mu sync.Mutex
	var leaked []string
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		if a := r.Header.Get("Authorization"); a != "" {
			leaked = append(leaked, r.Host+r.URL.Path+" "+a)
		}
		mu.Unlock()
		if r.URL.Path == "/hop" {
			_, port, _ := net.SplitHostPort(r.Host)
			http.Redirect(w, r, "https://example.com:"+port+"/f", http.StatusFound)
			return
		}
		w.Write([]byte("veri"))
	}))
	t.Cleanup(other.Close)
	portO := strings.Split(other.URL, ":")[2]
	main := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Header.Get("Authorization") == "":
			http.Error(w, "giriş", http.StatusUnauthorized)
		case r.URL.Path == "/dl":
			http.Redirect(w, r, "/real", http.StatusFound) // aynı köken: giriş kalır
		default:
			http.Redirect(w, r, "https://x.example.com:"+portO+"/hop", http.StatusFound)
		}
	}))
	t.Cleanup(main.Close)
	c := tlsClient(main)
	u := "https://example.com:" + strings.Split(main.URL, ":")[2] + "/dl"
	var asked atomic.Int32
	auth := func(string) (string, string, bool) { asked.Add(1); return "u", "pw", true }
	for _, d := range []*Download{
		{header: http.Header{}, auth: auth, client: c},                                            // 401 girişi
		{header: http.Header{"Authorization": {"Basic dGFyYXlpY2k6eA=="}}, auth: auth, client: c}, // tarayıcının başlığı
	} {
		resp, err := d.do(context.Background(), u, "bytes=0-")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("durum=%d", resp.StatusCode)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(leaked) != 0 || asked.Load() != 1 {
		t.Fatalf("başka kökene giriş gitti: %q (sorma %d)", leaked, asked.Load())
	}
}

// Giriş önbelleği host başına tutulur (F3GR L2): A istekleri sırayla B'ye ve C'ye yönlendirir, ikisi farklı
// giriş ister. Her host'un girişi bir kez okunur ve yalnız kendi host'una gider.
func TestAuthCachedPerHost(t *testing.T) {
	var n atomic.Int32
	var wrong atomic.Int32
	var srv *httptest.Server
	srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.Host)
		if host == "127.0.0.1" { // A
			next := []string{"b", "c"}[n.Add(1)%2]
			http.Redirect(w, r, "https://"+next+".example.com:"+strings.Split(srv.URL, ":")[2]+"/f", http.StatusFound)
			return
		}
		user, _, ok := r.BasicAuth()
		if !ok {
			http.Error(w, "giriş", http.StatusUnauthorized)
			return
		}
		if user != host {
			wrong.Add(1)
		}
		w.Write([]byte("veri"))
	}))
	t.Cleanup(srv.Close)
	var asked atomic.Int32
	auth := func(host string) (string, string, bool) { asked.Add(1); return host, "pw", true }
	d := &Download{url: srv.URL + "/dl", header: http.Header{}, auth: auth, client: tlsClient(srv)}
	for range 6 {
		resp, err := d.get(context.Background(), 0)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("durum=%d", resp.StatusCode)
		}
	}
	if asked.Load() != 2 || wrong.Load() != 0 {
		t.Fatalf("giriş %d kez okundu (2 bekleniyordu), yanlış host'a %d", asked.Load(), wrong.Load())
	}
}

// İndirme sürerken hedef adla oluşan dosya bitişte ezilmez, indirme numaralı ada konur; yalnız
// Overwrite istenince ezilir (B27 L5).
func TestFinishDoesNotClobberNewFile(t *testing.T) {
	for _, overwrite := range []bool{false, true} {
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "8")
			w.Write([]byte("yeni"))
			w.(http.Flusher).Flush()
			<-release
			w.Write([]byte("veri"))
		}))
		out := filepath.Join(t.TempDir(), "f.bin")
		d, err := Start(context.Background(), Request{URL: srv.URL, Path: out, Overwrite: overwrite}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(out, []byte("kullanıcının"), 0o644); err != nil {
			t.Fatal(err)
		}
		close(release)
		if err := d.Wait(); err != nil {
			t.Fatal(err)
		}
		srv.Close()
		want, placed := "kullanıcının", filepath.Join(filepath.Dir(out), "f (2).bin")
		if overwrite {
			want, placed = "yeniveri", out
		}
		if b, _ := os.ReadFile(out); string(b) != want {
			t.Fatalf("overwrite=%v: %s içeriği %q", overwrite, out, b)
		}
		if b, _ := os.ReadFile(placed); string(b) != "yeniveri" || d.Snapshot().Path != placed {
			t.Fatalf("overwrite=%v: %s=%q, Snapshot.Path=%s", overwrite, placed, b, d.Snapshot().Path)
		}
		if _, err := os.Stat(out + ".part"); err == nil {
			t.Fatal(".part kaldı")
		}
	}
}

// Overwrite + Trash: hedefteki eski dosya ezilmez, Çöp'e gider; Çöp'e taşınamazsa numaralı ada
// konur (V1a Minor 2).
func TestOverwriteTrashesOldFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("yeni")) }))
	defer srv.Close()
	for _, fail := range []bool{false, true} {
		dir, trash := t.TempDir(), t.TempDir()
		out := filepath.Join(dir, "f.bin")
		if err := os.WriteFile(out, []byte("eski"), 0o644); err != nil {
			t.Fatal(err)
		}
		move := func(p string) error {
			if fail {
				return errors.New("başka birim")
			}
			return MoveToTrash(trash, p)
		}
		d, err := Start(context.Background(), Request{URL: srv.URL, Path: out, Overwrite: true, Trash: move}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := d.Wait(); err != nil {
			t.Fatal(err)
		}
		oldAt, placed := filepath.Join(trash, "f.bin"), out
		if fail {
			oldAt, placed = out, filepath.Join(dir, "f (2).bin")
		}
		if b, _ := os.ReadFile(oldAt); string(b) != "eski" {
			t.Fatalf("fail=%v: eski dosya %s içinde değil: %q", fail, oldAt, b)
		}
		if b, _ := os.ReadFile(placed); string(b) != "yeni" || d.Snapshot().Path != placed {
			t.Fatalf("fail=%v: %s=%q, Snapshot.Path=%s", fail, placed, b, d.Snapshot().Path)
		}
	}
}

// ServerDate açıkken biten dosyanın değişiklik tarihi Last-Modified olur; kapalıyken indirme anı kalır.
func TestServerDate(t *testing.T) {
	data := rnd(200 << 10)
	want := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	for _, tc := range []struct {
		name, header string
		on, stamped  bool
	}{
		{"açık", want.Format(http.TimeFormat), true, true},
		{"kapalı", want.Format(http.TimeFormat), false, false},
		{"bozuk başlık", "dün akşam", true, false},
		{"başlık yok", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
				if tc.header != "" {
					w.Header().Set("Last-Modified", tc.header)
				}
				return false
			})
			out := filepath.Join(t.TempDir(), "f.bin")
			d, err := Start(context.Background(), Request{URL: srv.URL + "/f.bin", Path: out, ServerDate: tc.on}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err := d.Wait(); err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(out)
			if err != nil {
				t.Fatal(err)
			}
			if got := fi.ModTime().Equal(want); got != tc.stamped {
				t.Fatalf("değişiklik tarihi %v, sunucu tarihi uygulanması beklenen: %v", fi.ModTime(), tc.stamped)
			}
		})
	}
}

// statuses: indirme bitene kadar Snapshot'ta görülen bağlantı durumlarını toplar.
func statuses(d *Download) map[string]bool {
	seen := map[string]bool{}
	done := make(chan struct{})
	go func() { d.Wait(); close(done) }()
	for {
		for _, c := range d.Snapshot().Conns {
			seen[c.Status] = true
		}
		select {
		case <-done:
			return seen
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// Sessiz kalan bağlantı "Zaman aşımı" gösterir, veri yeniden gelince durum silinir.
func TestConnStatusTimeout(t *testing.T) {
	old := readTimeout
	readTimeout = 300 * time.Millisecond
	t.Cleanup(func() { readTimeout = old })
	data := rnd(256 << 10)
	var reqs atomic.Int32
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		switch reqs.Add(1) {
		case 1: // biraz veri gönderip susar
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-%d/%d", len(data)-1, len(data)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(data[:64<<10])
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return true
		case 2: // yeniden bağlanan istek bir süre bekler: durum görülebilsin
			time.Sleep(200 * time.Millisecond)
		}
		return false
	})
	out := filepath.Join(t.TempDir(), "f.bin")
	d, err := Start(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seen := statuses(d); !seen[statusTimeout] {
		t.Fatalf("zaman aşımı durumu görülmedi: %v", seen)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	for _, c := range d.Snapshot().Conns {
		if c.Status == statusTimeout {
			t.Fatal("veri yeniden geldikten sonra zaman aşımı durumu kalmamalı")
		}
	}
}

// Parçasını bitiren bağlantı yeni parça alınca durumu "Yeni parça aldı" olur.
func TestConnStatusNewPiece(t *testing.T) {
	srv, _ := serve(t, rnd(12<<20), nil)
	d, err := Start(context.Background(), Request{URL: srv.URL, Path: filepath.Join(t.TempDir(), "f.bin"), MaxConns: 4}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if seen := statuses(d); !seen[statusNewPiece] {
		t.Fatalf("yeni parça durumu görülmedi: %v", seen)
	}
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
}

// Sınırlayıcıyı bekleyen parça 2 sn veri almasa da takılmış sayılmaz, devralınmaz; gerçekten takılan devralınır.
func TestSplitSkipsThrottled(t *testing.T) {
	old := time.Now().Add(-2 * stealAfter)
	waiting := &chunk{start: 0, pos: 0, end: 999, last: old, throttled: true}
	stuck := &chunk{start: 1000, pos: 1000, end: 1999, last: old.Add(time.Second)}
	d := &Download{chunks: []*chunk{waiting, stuck}}
	if c := d.split(); c != stuck || waiting.gen != 0 || stuck.gen != 1 {
		t.Fatalf("split=%p (takılan %p), gen bekleyen=%d takılan=%d", c, stuck, waiting.gen, stuck.gen)
	}
	d = &Download{chunks: []*chunk{waiting}}
	if c := d.split(); c != nil || waiting.gen != 0 {
		t.Fatalf("yalnız sınırlayıcıyı bekleyen parça varken split=%p, gen=%d", c, waiting.gen)
	}
}

// Probe https'te 401'e kayıtlı girişle yanıt verir (F3GR B2): ad, boyut ve aralık desteği girişli istekten gelir.
func TestProbeAuthOverHTTPS(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != "ali" || p != "s3cr3t" {
			http.Error(w, "giriş", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Disposition", `attachment; filename="rapor.pdf"`)
		http.ServeContent(w, r, "", time.Time{}, strings.NewReader("0123456789"))
	}))
	t.Cleanup(srv.Close)
	var asked atomic.Int32
	auth := func(string) (string, string, bool) { asked.Add(1); return "ali", "s3cr3t", true }
	p, err := probe(context.Background(), &Download{header: http.Header{}, auth: auth, client: tlsClient(srv)}, srv.URL+"/x")
	if err != nil || p.Name != "rapor.pdf" || p.Size != 10 || !p.Resumable || asked.Load() != 1 {
		t.Fatalf("p=%+v err=%v sorma=%d", p, err, asked.Load())
	}
}
