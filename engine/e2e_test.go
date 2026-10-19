//go:build e2e

// Uçtan uca: gerçek Manager + DefaultDeps + soket servisi; 30 MB, bağlantı başına yavaşlatılmış sunucu.
// Çalıştır: go test -tags e2e -race -run TestE2E -v ./engine
package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// throttled her bağlantıyı rate bayt/sn ile sınırlar (ServeContent önce Seek eder, sonra okur).
type throttled struct {
	*bytes.Reader
	rate int
}

func (r throttled) Read(p []byte) (int, error) {
	p = p[:min(len(p), r.rate/20)]
	time.Sleep(50 * time.Millisecond)
	return r.Reader.Read(p)
}

type sockClient struct {
	t  *testing.T
	c  net.Conn
	sc *bufio.Scanner
	id int
}

func dialSock(t *testing.T, sock string) *sockClient {
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	return &sockClient{t: t, c: c, sc: sc}
}

// call komutu yollar ve yanıtı (olay satırlarını atlayarak) döner.
func (s *sockClient) call(cmd string, args map[string]any) map[string]any {
	s.t.Helper()
	s.id++
	req := map[string]any{"id": s.id, "cmd": cmd}
	for k, v := range args {
		req[k] = v
	}
	b, _ := json.Marshal(req)
	if _, err := s.c.Write(append(b, '\n')); err != nil {
		s.t.Fatal(err)
	}
	for {
		m := s.next()
		if _, ev := m["event"]; ev {
			continue
		}
		if m["ok"] != true {
			s.t.Fatalf("%s: %v", cmd, m)
		}
		return m
	}
}

func (s *sockClient) next() map[string]any {
	s.t.Helper()
	s.c.SetReadDeadline(time.Now().Add(30 * time.Second))
	if !s.sc.Scan() {
		s.t.Fatalf("soket kapandı: %v", s.sc.Err())
	}
	var m map[string]any
	if err := json.Unmarshal(s.sc.Bytes(), &m); err != nil {
		s.t.Fatal(err)
	}
	return m
}

type daemon struct {
	m    *Manager
	stop context.CancelFunc
	done chan error
}

func startDaemon(t *testing.T, dataDir, sock, trash string, ran *[][]string) *daemon {
	d := DefaultDeps()
	d.Trash = func(p string) error { return MoveToTrash(trash, p) }
	d.Run = func(name string, args ...string) error {
		*ran = append(*ran, append([]string{name}, args...))
		return nil
	}
	m, err := NewManager(dataDir, d)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	dm := &daemon{m: m, stop: cancel, done: make(chan error, 1)}
	go func() { dm.done <- Serve(ctx, m, sock) }()
	for i := 0; ; i++ { // soket hazır olana dek
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if i > 200 {
			t.Fatal("servis açılmadı")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return dm
}

// shutdown main.go'daki SIGTERM yolu: önce Serve biter, sonra Manager.Close durumu yazar.
func (d *daemon) shutdown(t *testing.T) {
	d.stop()
	if err := <-d.done; err != nil {
		t.Fatal(err)
	}
	d.m.Close()
}

func activeConns(conns []any) int {
	n := 0
	for _, c := range conns {
		m := c.(map[string]any)
		if m["pos"].(float64) <= m["end"].(float64) {
			n++
		}
	}
	return n
}

func TestE2EPauseCloseReopenResume(t *testing.T) {
	const size, perConn = 30 << 20, 640 << 10
	data := rnd(size)
	want := sha256.Sum256(data)
	mod := time.Unix(1_700_000_000, 0)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="buyuk.bin"`)
		http.ServeContent(w, r, "buyuk.bin", mod, throttled{bytes.NewReader(data), perConn})
	}))
	defer srv.Close()

	dataDir, dl, trash, sock := t.TempDir(), t.TempDir(), t.TempDir(), sockIn(t)
	var ran [][]string
	d1 := startDaemon(t, dataDir, sock, trash, &ran)

	cmd := dialSock(t, sock)
	st := cmd.call("list", nil)["settings"].(map[string]any)
	st["downloadDir"] = dl
	cmd.call("settings.set", map[string]any{"settings": st})
	sub := dialSock(t, sock)
	sub.call("subscribe", nil)

	it := cmd.call("add", map[string]any{"url": srv.URL + "/indir", "start": true})["item"].(map[string]any)
	id := it["id"].(string)
	if it["name"] != "buyuk.bin" || it["size"].(float64) != size {
		t.Fatalf("probe: %v", it)
	}
	t0 := time.Now()
	maxConns, got := 0, 0.0
	for maxConns < 4 || got < 0.4*size {
		ev := sub.next()
		if ev["event"] != "progress" {
			continue
		}
		for _, p := range ev["items"].([]any) {
			p := p.(map[string]any)
			if p["id"] == id {
				maxConns = max(maxConns, activeConns(p["conns"].([]any)))
				got = p["got"].(float64)
			}
		}
		if time.Since(t0) > 60*time.Second {
			t.Fatalf("ilerleme yok: conns=%d got=%.0f", maxConns, got)
		}
	}
	phase1 := time.Since(t0)
	cmd.call("pause", map[string]any{"ids": []string{id}})
	for {
		ev := sub.next()
		if ev["event"] == "item" && ev["item"].(map[string]any)["id"] == id && ev["item"].(map[string]any)["state"] == "paused" {
			got = ev["item"].(map[string]any)["got"].(float64)
			break
		}
	}
	t.Logf("1. aşama: %d bağlantı görüldü, %.1f MB / %v = %.1f MB/s, duraklatıldı", maxConns, got/(1<<20), phase1.Round(time.Millisecond), got/(1<<20)/phase1.Seconds())
	cmd.c.Close()
	sub.c.Close()
	d1.shutdown(t)
	if _, err := os.Stat(filepath.Join(dl, "buyuk.bin.part.json")); err != nil {
		t.Fatalf("durum dosyası yok: %v", err)
	}

	// Aynı veri diziniyle yeniden aç, devam et.
	d2 := startDaemon(t, dataDir, sock, trash, &ran)
	defer d2.shutdown(t)
	cmd = dialSock(t, sock)
	var item map[string]any
	for _, x := range cmd.call("list", nil)["items"].([]any) {
		if x.(map[string]any)["id"] == id {
			item = x.(map[string]any)
		}
	}
	if item["state"] != "paused" || item["got"].(float64) < 0.4*size {
		t.Fatalf("yeniden açılışta öğe: %v", item)
	}
	sub = dialSock(t, sock)
	sub.call("subscribe", nil)
	t1 := time.Now()
	cmd.call("resume", map[string]any{"ids": []string{id}})
	maxConns2 := 0
	for done := false; !done; {
		ev := sub.next()
		switch ev["event"] {
		case "progress":
			for _, p := range ev["items"].([]any) {
				if p := p.(map[string]any); p["id"] == id {
					maxConns2 = max(maxConns2, activeConns(p["conns"].([]any)))
				}
			}
		case "item":
			x := ev["item"].(map[string]any)
			if x["id"] == id && x["state"] == "error" {
				t.Fatalf("hata: %v", x["error"])
			}
			done = x["id"] == id && x["state"] == "done"
		}
	}
	phase2 := time.Since(t1)
	rest := float64(size) - got
	t.Logf("2. aşama: %d bağlantı, kalan %.1f MB / %v = %.1f MB/s", maxConns2, rest/(1<<20), phase2.Round(time.Millisecond), rest/(1<<20)/phase2.Seconds())

	f, err := os.Open(filepath.Join(dl, "buyuk.bin"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	io.Copy(h, f)
	if [32]byte(h.Sum(nil)) != want {
		t.Fatal("SHA-256 farklı")
	}
	t.Logf("SHA-256 eşleşti: %x", want)
	for _, s := range []string{".part", ".part.json"} {
		if _, err := os.Stat(filepath.Join(dl, "buyuk.bin"+s)); err == nil {
			t.Fatalf("%s kaldı", s)
		}
	}
	if maxConns < 4 {
		t.Fatalf("en az 4 bağlantı bekleniyordu: %d", maxConns)
	}
}
