package engine

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbe(t *testing.T) {
	data := rnd(5000)
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Cookie") != "c=1" {
			t.Errorf("Cookie iletilmedi: %q", r.Header.Get("Cookie"))
		}
		w.Header().Set("Content-Disposition", `attachment; filename="rapor.pdf"`)
		return false
	})
	p, err := Probe(context.Background(), srv.URL+"/x", http.Header{"Cookie": {"c=1"}}, nil)
	if err != nil || p != (ProbeResult{Name: "rapor.pdf", Size: 5000, Resumable: true}) {
		t.Fatalf("%+v %v", p, err)
	}
	// aralık desteği yok → 200: boyut Content-Length'ten, resumable false
	plain := serveNoRange(t, data)
	p, err = Probe(context.Background(), plain+"/a%20b.zip", nil, nil)
	if err != nil || p != (ProbeResult{Name: "a b.zip", Size: 5000, Resumable: false}) {
		t.Fatalf("%+v %v", p, err)
	}
}

func serveNoRange(t *testing.T, data []byte) string {
	srv, _ := serve(t, data, func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Content-Length", "5000")
		w.Write(data)
		return true
	})
	return srv.URL
}

func TestMoveToTrashNeverOverwrites(t *testing.T) {
	dir, trash := t.TempDir(), t.TempDir()
	for i, body := range []string{"bir", "iki"} {
		p := filepath.Join(dir, "x.zip")
		os.WriteFile(p, []byte(body), 0o644)
		if err := MoveToTrash(trash, p); err != nil {
			t.Fatal(i, err)
		}
	}
	a, _ := os.ReadFile(filepath.Join(trash, "x.zip"))
	b, _ := os.ReadFile(filepath.Join(trash, "x (2).zip"))
	if string(a) != "bir" || string(b) != "iki" {
		t.Fatalf("%q %q", a, b)
	}
}

func TestQuarantine(t *testing.T) {
	p := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(p, []byte("x"), 0o644)
	if err := Quarantine(p); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("xattr", "-p", "com.apple.quarantine", p).Output()
	if err != nil || !strings.HasPrefix(string(out), "0081;") || !strings.Contains(string(out), ";Octo;") {
		t.Fatalf("%q %v", out, err)
	}
}

// Yardımcı komutlar PATH'ten aranmaz: PATH'e konan sahte xattr/true/security çalışmamalı (B27 L6).
func TestHelpersIgnorePATH(t *testing.T) {
	fake := t.TempDir()
	for name, body := range map[string]string{"xattr": "exit 0", "true": "exit 1", "security": "echo sahte"} {
		if err := os.WriteFile(filepath.Join(fake, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", fake)
	p := filepath.Join(t.TempDir(), "f.bin")
	os.WriteFile(p, []byte("x"), 0o644)
	if err := Quarantine(p); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("/usr/bin/xattr", "-p", "com.apple.quarantine", p).Run(); err != nil {
		t.Fatal("karantina PATH'teki sahte xattr ile yazıldı")
	}
	if err := DefaultDeps().Run("true"); err != nil {
		t.Fatal("Run PATH'teki sahte true'yu çalıştırdı:", err)
	}
	if pw, err := keychainPassword("octo-test-yok.invalid"); err == nil || pw == "sahte" {
		t.Fatal("parola PATH'teki sahte security'den okundu")
	}
}

// Site girişi parolası gerçek Keychain'den okunur; test Keychain'e dokunmaz, yalnız bağlı olduğunu denetler.
func TestDefaultDepsHasPassword(t *testing.T) {
	if DefaultDeps().Password == nil {
		t.Fatal("Password bağlı değil: eşleşen site girişinde motor çökerdi")
	}
}

func TestKeychainPasswordReadsViaSecurityFramework(t *testing.T) {
	orig := secCopyPassword
	t.Cleanup(func() { secCopyPassword = orig })
	var gotService, gotAccount string
	secCopyPassword = func(service, account string) ([]byte, int32) {
		gotService, gotAccount = service, account
		if account == "yok.example" {
			return nil, -25300 // errSecItemNotFound
		}
		return []byte("gizli\n"), 0 // parola olduğu gibi döner; security'nin satır sonu kırpması yok
	}
	p, err := keychainPassword("site.example")
	if err != nil || p != "gizli\n" {
		t.Fatalf("parola = %q, %v", p, err)
	}
	if gotService != "com.uba.hizliindir.login" || gotAccount != "site.example" {
		t.Fatalf("sorgu servis=%q hesap=%q; LoginKeychain.swift ile aynı olmalı", gotService, gotAccount)
	}
	if _, err := keychainPassword("yok.example"); err == nil || !strings.Contains(err.Error(), "yok.example") {
		t.Fatalf("bulunamayan öğe hatası: %v", err)
	}
}

const scutilSample = `<dictionary> {
  ExceptionsList : <array> {
    0 : *.local
    1 : 169.254/16
    2 : 10.*
    3 : intranet.example.com
  }
  ExcludeSimpleHostnames : 1
  HTTPEnable : 1
  HTTPPort : 8080
  HTTPProxy : proxy.example.com
  HTTPSEnable : 1
  HTTPSPort : 8443
  HTTPSProxy : sproxy.example.com
}
`

func proxyFor(t *testing.T, r *proxyResolver, rawURL string) string {
	t.Helper()
	req, _ := http.NewRequest("GET", rawURL, nil)
	u, err := r.proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if u == nil {
		return ""
	}
	return u.String()
}

func fakeScutil(out string, err error, calls *int) func() ([]byte, error) {
	return func() ([]byte, error) { *calls++; return []byte(out), err }
}

func TestSystemProxy(t *testing.T) {
	calls := 0
	r := newProxyResolver(fakeScutil(scutilSample, nil, &calls))
	r.env = func(*http.Request) (*url.URL, error) { return nil, nil }
	r.envSet = func() bool { return false }

	for _, tc := range []struct{ url, want string }{
		{"http://files.example.org/a.zip", "http://proxy.example.com:8080"},
		{"https://files.example.org/a.zip", "http://sproxy.example.com:8443"},
		{"http://foo.local/a", ""},            // *.local
		{"http://169.254.10.1/a", ""},         // CIDR
		{"http://10.2.3.4/a", ""},             // 10.* joker
		{"http://intranet.example.com/a", ""}, // tam ad
		{"http://intranet.example.org/a", "http://proxy.example.com:8080"},
		{"http://nas/a", ""},            // ExcludeSimpleHostnames
		{"http://127.0.0.1:9000/a", ""}, // geri döngü asla proxy'ye gitmez
		{"http://localhost/a", ""},
		{"http://[::1]/a", ""},
	} {
		if got := proxyFor(t, r, tc.url); got != tc.want {
			t.Errorf("%s: proxy %q, beklenen %q", tc.url, got, tc.want)
		}
	}
	if calls != 1 {
		t.Errorf("scutil %d kez çalıştı, önbellekle 1 beklenir", calls)
	}
}

func TestSystemProxyEnvFirst(t *testing.T) {
	calls := 0
	r := newProxyResolver(fakeScutil(scutilSample, nil, &calls))
	env, _ := url.Parse("http://env.example.com:3128")
	r.env = func(*http.Request) (*url.URL, error) { return env, nil }
	r.envSet = func() bool { return true }
	if got := proxyFor(t, r, "http://files.example.org/a"); got != env.String() {
		t.Errorf("ortam değişkeni önce gelmeli: %q", got)
	}
	// NO_PROXY ile dışlanan adres: ortam ayarlı olduğundan sistem proxy'sine düşülmez.
	r.env = func(*http.Request) (*url.URL, error) { return nil, nil }
	if got := proxyFor(t, r, "http://files.example.org/a"); got != "" {
		t.Errorf("ortam ayarlıyken sistem proxy'si kullanılmamalı: %q", got)
	}
	if calls != 0 {
		t.Errorf("scutil çalışmamalıydı: %d", calls)
	}
}

func TestSystemProxyOffOrBroken(t *testing.T) {
	for name, run := range map[string]func() ([]byte, error){
		"kapalı": func() ([]byte, error) {
			return []byte("<dictionary> {\n  HTTPEnable : 0\n  HTTPProxy : p\n  HTTPPort : 1\n}\n"), nil
		},
		"hata":    func() ([]byte, error) { return nil, errors.New("yok") },
		"boş":     func() ([]byte, error) { return nil, nil },
		"portsuz": func() ([]byte, error) { return []byte("  HTTPEnable : 1\n  HTTPProxy : p\n"), nil },
	} {
		r := newProxyResolver(run)
		r.env = func(*http.Request) (*url.URL, error) { return nil, nil }
		r.envSet = func() bool { return false }
		if got := proxyFor(t, r, "http://files.example.org/a"); got != "" {
			t.Errorf("%s: proxy %q, beklenen yok", name, got)
		}
	}
}

// Dosya başına sınırlayıcı küresel sınırlayıcının altına bağlanır (ikisi birlikte uygulanır).
func TestDefaultDepsItemLimitWithinGlobal(t *testing.T) {
	srv, _ := serve(t, rnd(5000), nil)
	own := NewLimiter()
	job, err := DefaultDeps().Start(context.Background(), Request{URL: srv.URL + "/x", Path: filepath.Join(t.TempDir(), "x"), Limit: own}, false)
	if err != nil {
		t.Fatal(err)
	}
	job.Wait()
	if d := job.(*Download); d.lim != own || own.parent == nil {
		t.Fatalf("lim %p own %p parent %v", d.lim, own, own.parent)
	}
}
