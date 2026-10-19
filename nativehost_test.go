package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"hizli-indir/engine"
)

// fakeDaemon her satıra ok:true ile yanıt verir; list için ayar döner. Gelen istekleri biriktirir.
type fakeDaemon struct {
	mu         sync.Mutex
	reqs       []map[string]any
	fail       bool
	hold       chan struct{} // adresinde "slow" geçen add yanıtı bu kapanana dek bekler
	busy, peak int           // aynı anda süren add sayısı ve en yükseği
}

func startFake(t *testing.T, sock string, f *fakeDaemon) {
	t.Helper()
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				sc := bufio.NewScanner(c)
				for sc.Scan() {
					var m map[string]any
					json.Unmarshal(sc.Bytes(), &m)
					f.mu.Lock()
					f.reqs = append(f.reqs, m)
					fail := f.fail
					f.busy++
					f.peak = max(f.peak, f.busy)
					f.mu.Unlock()
					if u, _ := m["url"].(string); f.hold != nil && strings.Contains(u, "slow") {
						<-f.hold
					} else if m["cmd"] == "add" {
						time.Sleep(20 * time.Millisecond) // eşzamanlılık görünsün
					}
					f.mu.Lock()
					f.busy--
					f.mu.Unlock()
					r := map[string]any{"id": m["id"], "ok": !fail}
					if fail {
						r["error"] = "olmadı"
					}
					if m["cmd"] == "list" {
						r["settings"] = map[string]any{"captureExts": []string{"zip"}, "excludedSites": []string{"a.com"}, "captureOff": true, "captureMinKB": 100, "hideVideoButton": true}
					}
					if m["cmd"] == "stream.probe" && !fail {
						r["info"] = map[string]any{"kind": "hls", "container": "mp4", "variants": []any{}, "audios": []any{}}
					}
					b, _ := json.Marshal(r)
					c.Write(append(b, '\n'))
				}
			}()
		}
	}()
}

func shortSock(t *testing.T) string {
	d, err := os.MkdirTemp("/tmp", "hi")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return filepath.Join(d, "e.sock")
}

func frames(t *testing.T, msgs ...any) *bytes.Buffer {
	var b bytes.Buffer
	for _, m := range msgs {
		if err := writeFrame(&b, m); err != nil {
			t.Fatal(err)
		}
	}
	return &b
}

func TestFramingRoundTripLittleEndian(t *testing.T) {
	b := frames(t, map[string]any{"type": "hello"})
	raw := b.Bytes()
	if binary.LittleEndian.Uint32(raw[:4]) != uint32(len(raw)-4) {
		t.Fatal("uzunluk öneki little-endian değil")
	}
	got, err := readFrame(b)
	if err != nil || string(got) != `{"type":"hello"}` {
		t.Fatal(string(got), err)
	}
	big := make([]byte, 4)
	binary.LittleEndian.PutUint32(big, maxIn+1)
	if _, err := readFrame(bytes.NewReader(big)); !errors.Is(err, errFrameTooBig) {
		t.Fatal("sınır üstü kabul edildi", err)
	}
}

// Chrome host'a 4 GB'a kadar iletir; 1 MB'lık sınır yalnız host→Chrome yönündedir.
func TestReadFrameAcceptsOver1MB(t *testing.T) {
	var in bytes.Buffer
	body := bytes.Repeat([]byte("x"), maxFrame+200_000)
	binary.Write(&in, binary.LittleEndian, uint32(len(body)))
	in.Write(body)
	got, err := readFrame(&in)
	if err != nil || len(got) != len(body) {
		t.Fatal(len(got), err)
	}
}

func TestServeOversizedInboundReplies(t *testing.T) {
	var in bytes.Buffer
	binary.Write(&in, binary.LittleEndian, uint32(maxIn+1))
	var out bytes.Buffer
	h := &nativeHost{sock: "yok", wait: 0}
	if err := h.serve(&in, &out); !errors.Is(err, errFrameTooBig) {
		t.Fatal(err)
	}
	rs := readReplies(t, &out)
	if len(rs) != 1 || rs[0]["type"] != "error" || !strings.Contains(rs[0]["error"].(string), "çok büyük") {
		t.Fatal(rs)
	}
}

func readReplies(t *testing.T, out *bytes.Buffer) []map[string]any {
	var rs []map[string]any
	for out.Len() > 0 {
		b, err := readFrame(out)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		json.Unmarshal(b, &m)
		rs = append(rs, m)
	}
	return rs
}

func TestHelloDownloadBatch(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	h := &nativeHost{sock: sock, wait: time.Second}
	in := frames(t,
		map[string]any{"type": "hello"},
		map[string]any{"type": "download", "url": "http://x/a.zip", "referrer": "http://x/", "cookies": "s=1", "userAgent": "UA", "filename": "a.zip", "size": 5},
		map[string]any{"type": "batch", "links": []map[string]string{{"url": "http://x/1", "text": "1"}, {"url": "http://x/2", "text": "2"}}, "referrer": "http://x/", "cookies": "", "userAgent": "UA"},
	)
	var out bytes.Buffer
	if err := h.serve(in, &out); err != nil {
		t.Fatal(err)
	}
	rs := readReplies(t, &out)
	if len(rs) != 3 || rs[0]["type"] != "hello" || rs[0]["version"] != "1.1" || rs[0]["captureExts"].([]any)[0] != "zip" || rs[0]["excludedSites"].([]any)[0] != "a.com" ||
		rs[0]["captureOff"] != true || rs[0]["captureMinKB"] != float64(100) {
		t.Fatal(rs)
	}
	if rs[1]["accepted"] != true || rs[2]["accepted"] != true {
		t.Fatal(rs)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	add := f.reqs[2] // [0]=hello [1]=list
	hd := add["headers"].(map[string]any)
	if add["cmd"] != "add" || add["url"] != "http://x/a.zip" || add["name"] != "a.zip" || add["start"] != true || add["size"] != float64(5) ||
		hd["Referer"] != "http://x/" || hd["Cookie"] != "s=1" || hd["User-Agent"] != "UA" {
		t.Fatal(add)
	}
	b1, b2 := f.reqs[3], f.reqs[4] // toplu add'ler eşzamanlı: sıra belirsiz
	if b1["queue"] != "main" || b1["start"] != false || b2["queue"] != "main" || b1["url"] == b2["url"] {
		t.Fatal(b1, b2)
	}
	if _, has := b1["headers"].(map[string]any)["Cookie"]; has {
		t.Fatal("boş Cookie başlığı gönderildi")
	}
}

// Eklentinin "Başlat" düğmesi üst düzey start:true yollar; yalnız true başlatır (yoksa ya da false ise kuyrukta bekler).
func TestBatchStartFlag(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	link := []map[string]string{{"url": "http://x/1"}}
	in := frames(t,
		map[string]any{"type": "batch", "links": link, "start": true},
		map[string]any{"type": "batch", "links": link, "start": false},
		map[string]any{"type": "batch", "links": link, "start": "true"},
	)
	var out bytes.Buffer
	if err := (&nativeHost{sock: sock, wait: time.Second}).serve(in, &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 2 || f.reqs[0]["start"] != true || f.reqs[1]["start"] != false {
		t.Fatal(f.reqs)
	}
}

func TestDownloadRejectedWhenDaemonFails(t *testing.T) {
	sock := shortSock(t)
	startFake(t, sock, &fakeDaemon{fail: true})
	var out bytes.Buffer
	(&nativeHost{sock: sock, wait: time.Second}).serve(frames(t, map[string]any{"type": "download", "url": "http://x/a"}), &out)
	if rs := readReplies(t, &out); rs[0]["accepted"] != false {
		t.Fatal(rs)
	}
}

func TestLaunchesAppAndRetries(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	launched := 0
	h := &nativeHost{sock: sock, wait: 5 * time.Second, launch: func() {
		launched++
		go func() { time.Sleep(300 * time.Millisecond); startFake(t, sock, f) }()
	}}
	var out bytes.Buffer
	h.serve(frames(t, map[string]any{"type": "download", "url": "http://x/a"}), &out)
	if rs := readReplies(t, &out); rs[0]["accepted"] != true || launched != 1 {
		t.Fatal(rs, launched)
	}
	// Servis hiç gelmezse: accepted false.
	h2 := &nativeHost{sock: shortSock(t), wait: 400 * time.Millisecond, launch: func() {}}
	out.Reset()
	h2.serve(frames(t, map[string]any{"type": "download", "url": "http://x/a"}), &out)
	if rs := readReplies(t, &out); rs[0]["accepted"] != false {
		t.Fatal(rs)
	}
}

// Toplu indirmede çerez ve Referer her linkin kendi alanından gelir; üst düzey sayfa çerezi hiçbir linke yayılmaz.
func TestBatchUsesPerLinkCookiesAndReferrer(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	in := frames(t, map[string]any{"type": "batch", "referrer": "https://site.example/", "cookies": "page=1", "userAgent": "UA",
		"links": []map[string]string{
			{"url": "https://site.example/a", "cookies": "s=1", "referrer": "https://site.example/page?q=1"},
			{"url": "https://cdn.site.example/b"},
			{"url": "http://site.example/c", "referrer": "https://site.example/page"},
			{"url": "https://other.net/d", "cookies": "o=1", "referrer": "https://site.example/page"},
			{"url": "ftp://site.example/e", "cookies": "s=1"},
			{"url": "javascript:alert(1)"},
		}})
	var out bytes.Buffer
	if err := (&nativeHost{sock: sock, wait: time.Second}).serve(in, &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 4 {
		t.Fatalf("4 http(s) bağlantı bekleniyordu, %d istek", len(f.reqs))
	}
	want := map[string][2]any{ // url → {Cookie, Referer}; nil = başlık yok
		"https://site.example/a":     {"s=1", "https://site.example/page?q=1"},
		"https://cdn.site.example/b": {nil, "https://site.example/"},
		"http://site.example/c":      {nil, nil}, // https → http: Referer gitmez
		"https://other.net/d":        {"o=1", "https://site.example/"},
	}
	for _, r := range f.reqs {
		hd := r["headers"].(map[string]any)
		w, ok := want[r["url"].(string)]
		if !ok || hd["Cookie"] != w[0] || hd["Referer"] != w[1] || hd["User-Agent"] != "UA" {
			t.Fatalf("%v: %v", r["url"], hd)
		}
	}
}

// Tekli indirmede de Referer tarayıcı kuralıyla kırpılır (eski eklenti tam sayfa adresi yollasa bile).
func TestDownloadReferrerTrimmed(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	in := frames(t,
		map[string]any{"type": "download", "url": "https://cdn.net/a.zip", "referrer": "https://mail.example/?token=x"},
		map[string]any{"type": "download", "url": "http://mail.example/a.zip", "referrer": "https://mail.example/?token=x"},
		map[string]any{"type": "download", "url": "https://mail.example/a.zip", "referrer": "https://mail.example/in#top"},
	)
	var out bytes.Buffer
	if err := (&nativeHost{sock: sock, wait: time.Second}).serve(in, &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, want := range []any{"https://mail.example/", nil, "https://mail.example/in"} {
		if got := f.reqs[i]["headers"].(map[string]any)["Referer"]; got != want {
			t.Fatalf("%d: Referer %v, %v bekleniyordu", i, got, want)
		}
	}
}

func TestReferrerFor(t *testing.T) {
	for _, c := range [][3]string{
		{"https://a.com/p?x=1#h", "https://a.com/f", "https://a.com/p?x=1"},
		{"https://a.com/p", "https://b.a.com/f", "https://a.com/"},
		{"https://a.com:8443/p", "https://a.com/f", "https://a.com:8443/"},
		{"https://a.com/p", "http://a.com/f", ""},
		{"http://a.com/p", "https://a.com/f", "http://a.com/"},
		{"file:///etc/passwd", "https://a.com/f", ""},
		{"", "https://a.com/f", ""},
		{"https://a.com/p", "::bozuk", ""},
	} {
		if got := engine.ReferrerFor(c[0], c[1]); got != c[2] {
			t.Errorf("referrerFor(%q, %q) = %q, %q bekleniyordu", c[0], c[1], got, c[2])
		}
	}
}

func TestHelloOfflineDoesNotLaunch(t *testing.T) {
	launched := false
	h := &nativeHost{sock: shortSock(t), wait: 5 * time.Second, launch: func() { launched = true }}
	var out bytes.Buffer
	start := time.Now()
	h.serve(frames(t, map[string]any{"type": "hello"}), &out)
	r := readReplies(t, &out)[0]
	if launched || time.Since(start) > time.Second {
		t.Fatal("hello uygulamayı açtı/bekledi")
	}
	if r["type"] != "hello" || r["offline"] != true || r["version"] != "1.1" || len(r["captureExts"].([]any)) < 10 || len(r["excludedSites"].([]any)) != 0 {
		t.Fatal(r)
	}
}

// hello motora bildirilir (Ayarlar > Tarayıcı "son bağlantı" için); list'ten önce gider.
func TestHelloNotifiesEngine(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	h := &nativeHost{sock: sock, wait: time.Second}
	var out bytes.Buffer
	if err := h.serve(frames(t, map[string]any{"type": "hello"}), &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 2 || f.reqs[0]["cmd"] != "hello" || f.reqs[1]["cmd"] != "list" {
		t.Fatal(f.reqs)
	}
}

// open ayrı iletidir: servis zaten çalışsa da uygulamayı öne getirir, motora komut gitmez.
func TestOpenLaunchesApp(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	launched := 0
	h := &nativeHost{sock: sock, wait: time.Second, launch: func() { launched++ }}
	var out bytes.Buffer
	if err := h.serve(frames(t, map[string]any{"type": "open"}), &out); err != nil {
		t.Fatal(err)
	}
	if r := readReplies(t, &out)[0]; r["type"] != "ack" || r["accepted"] != true || launched != 1 {
		t.Fatal(r, launched)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 0 {
		t.Fatal(f.reqs)
	}
}

// Toplu eklemede add'ler en çok batchConns eşzamanlı gider; toplam süre dolunca yanıtı gelmeyenler "pending",
// hiç gönderilemeyenler "notSent" olarak döner, accepted:false sayılmaz (V1a Minor 9).
func TestBatchConcurrentWithDeadline(t *testing.T) {
	oldConns, oldWait := batchConns, batchWait
	batchConns, batchWait = 2, time.Second // yük altında hızlı toplu iş sığsın
	t.Cleanup(func() { batchConns, batchWait = oldConns, oldWait })
	sock := shortSock(t)
	f := &fakeDaemon{hold: make(chan struct{})}
	startFake(t, sock, f)
	t.Cleanup(func() { close(f.hold) })
	links := func(us ...string) (ls []map[string]string) {
		for _, u := range us {
			ls = append(ls, map[string]string{"url": u})
		}
		return
	}
	in := frames(t,
		map[string]any{"type": "batch", "links": links("http://x/1", "http://x/2", "http://x/3", "http://x/4", "http://x/5")},
		map[string]any{"type": "batch", "links": links("http://x/slow1", "http://x/slow2", "http://x/6")},
	)
	var out bytes.Buffer
	start := time.Now()
	if err := (&nativeHost{sock: sock, wait: time.Second}).serve(in, &out); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el > 5*time.Second {
		t.Fatalf("toplam süre sınırı işlemedi: %v", el)
	}
	rs := readReplies(t, &out)
	if len(rs) != 2 || rs[0]["accepted"] != true || rs[0]["pending"] != nil || rs[0]["notSent"] != nil {
		t.Fatal(rs)
	}
	pending, _ := rs[1]["pending"].([]any)
	if rs[1]["accepted"] != true || !slices.Equal(pending, []any{"http://x/slow1", "http://x/slow2"}) ||
		!slices.Equal(rs[1]["notSent"].([]any), []any{"http://x/6"}) {
		t.Fatal(rs[1])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.peak != 2 {
		t.Fatalf("en çok 2 eşzamanlı add bekleniyordu, %d", f.peak)
	}
}
