package engine

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <stdlib.h>
#include <Security/Security.h>

static OSStatus copyPassword(const char *service, const char *account, CFDataRef *out) {
	CFStringRef s = CFStringCreateWithCString(NULL, service, kCFStringEncodingUTF8);
	CFStringRef a = CFStringCreateWithCString(NULL, account, kCFStringEncodingUTF8);
	OSStatus st = errSecParam;
	if (s && a) {
		const void *keys[] = {kSecClass, kSecAttrService, kSecAttrAccount, kSecReturnData, kSecMatchLimit};
		const void *vals[] = {kSecClassGenericPassword, s, a, kCFBooleanTrue, kSecMatchLimitOne};
		CFDictionaryRef q = CFDictionaryCreate(NULL, keys, vals, 5, &kCFTypeDictionaryKeyCallBacks, &kCFTypeDictionaryValueCallBacks);
		st = SecItemCopyMatching(q, (CFTypeRef *)out);
		CFRelease(q);
	}
	if (s) CFRelease(s);
	if (a) CFRelease(a);
	return st;
}
*/
import "C"

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unsafe"
)

// DefaultDeps servisin gerçek bağımlılıklarıdır: tüm indirmeler tek hız sınırlayıcıyı paylaşır.
func DefaultDeps() Deps {
	lim := NewLimiter()
	home, _ := os.UserHomeDir()
	return Deps{
		Start: func(ctx context.Context, r Request, resume bool) (Job, error) {
			if r.Stream != nil { // akış her zaman var olan parçalardan sürer; .part'a bakan resume anlamsız
				j, err := StartStream(ctx, r, r.Limit.within(lim))
				if err != nil {
					return nil, err
				}
				return j, nil
			}
			start := Start
			if resume { // r.URL öğenin güncel (yenilenmiş) adresidir, .part.json'daki değil
				start = Resume
			}
			d, err := start(ctx, r, r.Limit.within(lim))
			if err != nil {
				return nil, err // nil *Download'ı arayüze sarmamak için
			}
			return d, nil
		},
		Probe:             Probe,
		ProbeStream:       ProbeStream,
		Password:          keychainPassword,
		UniquePath:        UniquePath,
		CategoryFor:       CategoryFor,
		DefaultCategories: DefaultCategories,
		SetLimit:          lim.Set,
		Run:               func(name string, args ...string) error { return exec.Command("/usr/bin/"+name, args...).Run() }, // open/pmset/osascript; PATH'e güvenilmez
		Trash:             func(p string) error { return MoveToTrash(filepath.Join(home, ".Trash"), p) },
		Quarantine:        Quarantine,
		Now:               time.Now,
	}
}

// keychainPassword uygulamanın Keychain'e yazdığı site girişi parolasını okur (LoginKeychain.swift ile aynı servis).
// Security.framework ile okunur ki öğenin ACL'i soran süreci octo olarak görsün (uygulama octo'yu güvenilir ekler;
// /usr/bin/security ile okumak her seferinde izin penceresi açardı). cgo gerekir: CGO_ENABLED=1.
func keychainPassword(host string) (string, error) {
	b, st := secCopyPassword("com.uba.hizliindir.login", host)
	if st != 0 {
		return "", fmt.Errorf("Anahtar Zinciri'nde %s için parola okunamadı", host)
	}
	return string(b), nil
}

// secCopyPassword SecItemCopyMatching ile genel parolayı ve OSStatus'u döner; testte sahtesiyle değişir.
var secCopyPassword = func(service, account string) ([]byte, int32) {
	cs, ca := C.CString(service), C.CString(account)
	defer C.free(unsafe.Pointer(cs))
	defer C.free(unsafe.Pointer(ca))
	var data C.CFDataRef
	if st := C.copyPassword(cs, ca, &data); st != C.errSecSuccess {
		return nil, int32(st)
	}
	defer C.CFRelease(C.CFTypeRef(data))
	return C.GoBytes(unsafe.Pointer(C.CFDataGetBytePtr(data)), C.int(C.CFDataGetLength(data))), 0
}

// Probe `Range: bytes=0-0` GET ile adı, toplam boyutu ve aralık desteğini öğrenir (klasik indirme yöneticisi davranışı).
func Probe(ctx context.Context, u string, h http.Header, auth AuthFunc) (ProbeResult, error) {
	return probe(ctx, &Download{header: h.Clone(), auth: auth, client: newClient()}, u)
}

func probe(ctx context.Context, d *Download, u string) (ProbeResult, error) {
	resp, err := d.do(ctx, u, "bytes=0-0")
	if err != nil {
		return ProbeResult{}, err
	}
	defer resp.Body.Close()
	p := ProbeResult{Name: NameFrom(resp), Size: -1, Kind: mediaKind(resp.Header.Get("Content-Type"), resp.Request.URL.Path)}
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if _, total, ok := contentRange(resp.Header.Get("Content-Range")); ok {
			p.Size, p.Resumable = total, true
		}
	case http.StatusOK:
		p.Size = resp.ContentLength // bilinmiyorsa -1
	default:
		return ProbeResult{}, fmt.Errorf("sunucu %s döndü", resp.Status)
	}
	return p, nil
}

// MoveToTrash dosyayı trash klasörüne benzersiz adla taşır; asla silmez.
// shortcut: başka birimdeki dosya (rename EXDEV) taşınamaz, hata döner; gerekirse NSFileManager.trashItem.
func MoveToTrash(trash, p string) error {
	if err := os.MkdirAll(trash, 0o700); err != nil {
		return err
	}
	return os.Rename(p, UniquePath(filepath.Join(trash, filepath.Base(p)), "number", nil))
}

// Quarantine bitmiş dosyaya Safari/Chrome gibi com.apple.quarantine ekler (Gatekeeper denetlesin).
func Quarantine(p string) error {
	v := fmt.Sprintf("0081;%x;Octo;", time.Now().Unix())
	return exec.Command("/usr/bin/xattr", "-w", "com.apple.quarantine", v, p).Run()
}

// proxyResolver Transport.Proxy'dir: önce ortam değişkenleri (http.ProxyFromEnvironment), hiçbiri
// ayarlı değilse macOS sistem proxy'si (`scutil --proxy`: HTTP/HTTPS proxy + istisna listesi).
// shortcut: PAC/otomatik proxy (ProxyAutoConfig) ve SOCKS okunmaz; gerekirse scutil çıktısındaki
// ProxyAutoConfigURLString/SOCKSProxy alanları eklenir.
type proxyResolver struct {
	env    func(*http.Request) (*url.URL, error)
	envSet func() bool // ortamda proxy değişkeni var mı (NO_PROXY'li adreste sistem proxy'sine düşülmesin)
	run    func() ([]byte, error)

	mu   sync.Mutex
	at   time.Time
	conf sysProxyConf
}

const proxyTTL = 30 * time.Second // ayar değişince yeni indirmeler en geç bu kadar sonra görür

func newProxyResolver(run func() ([]byte, error)) *proxyResolver {
	return &proxyResolver{env: http.ProxyFromEnvironment, envSet: envProxySet, run: run}
}

// systemProxy tüm istemcilerin paylaştığı çözümleyicidir; scutil gerçek komuttur.
var systemProxy = newProxyResolver(func() ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "/usr/sbin/scutil", "--proxy").Output()
})

func envProxySet() bool {
	for _, k := range []string{"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy"} {
		if os.Getenv(k) != "" {
			return true
		}
	}
	return false
}

func (r *proxyResolver) proxy(req *http.Request) (*url.URL, error) {
	if u, err := r.env(req); u != nil || err != nil {
		return u, err
	}
	if r.envSet() {
		return nil, nil
	}
	return r.system().forURL(req.URL), nil
}

func (r *proxyResolver) system() sysProxyConf {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.at.IsZero() || time.Since(r.at) > proxyTTL {
		out, err := r.run()
		r.conf, r.at = sysProxyConf{}, time.Now()
		if err == nil {
			r.conf = parseScutilProxy(string(out))
		}
	}
	return r.conf
}

type sysProxyConf struct {
	http, https   *url.URL
	exceptions    []string
	excludeSimple bool
}

// parseScutilProxy `scutil --proxy` çıktısındaki "Anahtar : değer" satırlarını okur.
func parseScutilProxy(out string) sysProxyConf {
	kv := map[string]string{}
	var c sysProxyConf
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " : ")
		if !ok {
			continue
		}
		if _, err := strconv.Atoi(k); err == nil { // ExceptionsList dizisinin "0 : *.local" satırları
			c.exceptions = append(c.exceptions, strings.TrimSpace(v))
			continue
		}
		kv[k] = strings.TrimSpace(v)
	}
	mk := func(p string) *url.URL {
		host, port := kv[p+"Proxy"], kv[p+"Port"]
		if kv[p+"Enable"] != "1" || host == "" || port == "" {
			return nil
		}
		return &url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}
	}
	c.http, c.https, c.excludeSimple = mk("HTTP"), mk("HTTPS"), kv["ExcludeSimpleHostnames"] == "1"
	return c
}

func (c sysProxyConf) forURL(u *url.URL) *url.URL {
	p := c.http
	if u.Scheme == "https" {
		p = c.https
	}
	if p == nil || c.bypass(u.Hostname()) {
		return nil
	}
	return p
}

// bypass geri döngü adresini, basit ad (noktasız) ayarını ve istisna listesini (tam ad, *.alan, 10.*,
// 169.254/16 gibi eksik CIDR) uygular.
func (c sysProxyConf) bypass(host string) bool {
	host = strings.ToLower(host)
	ip := net.ParseIP(host)
	if host == "localhost" || (ip != nil && ip.IsLoopback()) {
		return true
	}
	if c.excludeSimple && ip == nil && !strings.Contains(host, ".") {
		return true
	}
	for _, e := range c.exceptions {
		e = strings.ToLower(e)
		if ok, _ := path.Match(e, host); ok || e == host {
			return true
		}
		if strings.HasPrefix(e, "*.") && host == e[2:] { // *.local alan adının kendisini de kapsar
			return true
		}
		if addr, bits, ok := strings.Cut(e, "/"); ok && ip != nil {
			if ip4 := ip.To4(); ip4 != nil {
				parts := strings.Split(addr, ".")
				for len(parts) < 4 { // "169.254/16" → 169.254.0.0/16
					parts = append(parts, "0")
				}
				if _, n, err := net.ParseCIDR(strings.Join(parts, ".") + "/" + bits); err == nil && n.Contains(ip4) {
					return true
				}
			}
		}
	}
	return false
}
