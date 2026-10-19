package engine

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// blockState durum dosyasının .tmp'sini adlandırılmış boru yapar: okuyucu açılana dek saveState'in
// WriteFile'ı çekirdekte bekler (macOS Downloads izin istemi cevapsızken L2'de görülen takılma gibi).
// Dönen işlev okuyucuyu açıp yazımı serbest bırakır.
func blockState(t *testing.T, path string) (release func()) {
	t.Helper()
	fifo := path + ".part.json.tmp"
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	released := false
	release = func() {
		if released {
			return
		}
		released = true
		go func() {
			if f, err := os.OpenFile(fifo, os.O_RDONLY, 0); err == nil {
				io.Copy(io.Discard, f)
				f.Close()
			}
		}()
	}
	t.Cleanup(release)
	return release
}

// Durum dosyası yazımı takılsa da gözetim döngüsü dönmeli: hız ölçülür, bağlantı rampası sürer.
func TestSaveStateBlockedWatchKeepsTicking(t *testing.T) {
	srv, _ := serve(t, rnd(64<<20), nil) // bağlantı başı ~2 MB/s
	path := filepath.Join(t.TempDir(), "f.bin")
	release := blockState(t, path)
	d, err := Start(context.Background(), Request{URL: srv.URL + "/f.bin", Path: path, MaxConns: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { release(); d.Pause(); d.Wait() })
	// 1. sn'de yazım takılır; rampa 2→4'e ~2,3 sn'de çıkar (döngü takılırsa 2'de kalır)
	deadline := time.Now().Add(4 * time.Second)
	for {
		d.mu.Lock()
		n := d.active
		d.mu.Unlock()
		if n >= 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("yazım takılıyken rampa durdu: %d bağlantı", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
	release()
	d.Pause()
	if err := d.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatal(err)
	}
	var st State
	if b, err := os.ReadFile(path + ".part.json"); err != nil || json.Unmarshal(b, &st) != nil || len(st.Chunks) == 0 {
		t.Fatalf("duraklatınca durum dosyası yazılmadı: %v %q", err, b)
	}
}

// Biten indirme, sürmekte olan durum yazımını bekler; yazım bittikten sonra .part.json geri gelmez.
func TestSaveStateInFlightDoesNotResurrectAfterFinish(t *testing.T) {
	srv, _ := serve(t, rnd(3<<20), nil)
	path := filepath.Join(t.TempDir(), "f.bin")
	release := blockState(t, path)
	d, err := Start(context.Background(), Request{URL: srv.URL + "/f.bin", Path: path, MaxConns: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(2500 * time.Millisecond) // yazım 1. sn'de takıldı, veri ~1,5 sn'de bitti
	release()
	if err := d.Wait(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path + ".part.json"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("bitmiş indirmenin durum dosyası kaldı: %v", err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Size() != 3<<20 {
		t.Fatalf("son dosya: %v", err)
	}
}
