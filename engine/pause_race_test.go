package engine

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// L1 hata 3 (B30): Pause indirmenin her anında tutmalı (ilk istek, rampa, parça devri, bitişe yakın).
// Wait çabuk döner; sonuç ya tamam (asıl dosya = kaynak) ya ErrPaused (artık yazılmaz, devam kaynağı verir).
// Yoğun deneme: go test -race -run TestPauseRaceAnyMoment -count=200 ./engine
func TestPauseRaceAnyMoment(t *testing.T) {
	data := rnd(3 << 20)
	srv, _ := serve(t, data, nil)
	t0 := time.Now()
	if !bytes.Equal(download(t, srv.URL, 8), data) {
		t.Fatal("duraklatmasız indirme bozuk")
	}
	full := time.Since(t0)
	const tries = 12
	for i := range tries + 1 {
		at := full * time.Duration(i) / tries // 0'dan bitişe kadar eşit aralıklı duraklatma anları
		out := filepath.Join(t.TempDir(), "f.bin")
		d, err := Start(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 8}, nil)
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(at)
		d.Pause()
		res := make(chan error, 1)
		go func() { res <- d.Wait() }()
		select {
		case err = <-res:
		case <-time.After(3 * time.Second):
			t.Fatalf("%v: Pause sonrası Wait 3 sn içinde dönmedi: %+v", at, d.Snapshot())
		}
		switch {
		case err == nil:
			if b, _ := os.ReadFile(out); !bytes.Equal(b, data) {
				t.Fatalf("%v: tamam dendi ama dosya kaynakla aynı değil", at)
			}
			continue
		case !errors.Is(err, ErrPaused):
			t.Fatalf("%v: Wait=%v, ErrPaused ya da nil bekleniyordu", at, err)
		}
		a, _ := os.ReadFile(out + ".part")
		time.Sleep(100 * time.Millisecond)
		if b, _ := os.ReadFile(out + ".part"); !bytes.Equal(a, b) {
			t.Fatalf("%v: duraklatmadan sonra dosyaya yazılmaya devam ediliyor", at)
		}
		r, err := Resume(context.Background(), Request{URL: srv.URL, Path: out, MaxConns: 8}, nil)
		if err != nil {
			t.Fatalf("%v: devam: %v", at, err)
		}
		if err := r.Wait(); err != nil {
			t.Fatalf("%v: devam Wait: %v", at, err)
		}
		if b, _ := os.ReadFile(out); !bytes.Equal(b, data) {
			t.Fatalf("%v: duraklat-devam sonrası dosya kaynakla aynı değil", at)
		}
	}
}

// Gerçek Manager: pause sonrası öğe kısa sürede paused olur ve öyle kalır; duraklatılmayan öğe hiç paused olmaz.
// Pause'tan hemen sonra gelen Resume (önceki koşu henüz kapanmamışken) kaybolmamalı.
func TestManagerPauseResumeRace(t *testing.T) {
	data := rnd(3 << 20)
	srv, _ := serve(t, data, nil)
	m, err := NewManager(t.TempDir(), DefaultDeps())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := m.settings
	s.DownloadDir = t.TempDir()
	if err := m.SetSettings(s); err != nil {
		t.Fatal(err)
	}
	get := func(id string) Item {
		items, _, _ := m.List()
		for _, x := range items {
			if x.ID == id {
				return x
			}
		}
		return Item{}
	}
	wait := func(id string, ok func(Item) bool, what string) Item {
		deadline := time.Now().Add(5 * time.Second)
		for x := get(id); ; x = get(id) {
			if ok(x) {
				return x
			}
			if time.Now().After(deadline) {
				t.Fatalf("%s: %s bekleniyordu, durum %s got=%d err=%q", id, what, x.State, x.Got, x.Error)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	for i, dly := range []time.Duration{0, time.Millisecond, 20 * time.Millisecond, 150 * time.Millisecond, 600 * time.Millisecond} {
		it, err := m.Add(AddArgs{URL: srv.URL + "/f.bin", Name: "f.bin", Start: true})
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(dly)
		m.Pause([]string{it.ID})
		x := wait(it.ID, func(x Item) bool { return x.State == StatePaused || x.State == StateDone }, "paused")
		if x.State == StatePaused {
			time.Sleep(100 * time.Millisecond)
			if y := get(it.ID); y.State != StatePaused || y.Got != x.Got {
				t.Fatalf("%d: duraklatıldıktan sonra sürdü: %s %d→%d", i, y.State, x.Got, y.Got)
			}
		}
		// Duraklat ve hemen devam: ikinci komut ilk koşu kapanırken gelir.
		m.Resume([]string{it.ID})
		m.Pause([]string{it.ID})
		m.Resume([]string{it.ID})
		x = wait(it.ID, func(x Item) bool { return x.State == StateDone || x.State == StateError }, "done")
		if x.State != StateDone {
			t.Fatalf("%d: son Resume'dan sonra %s (%s)", i, x.State, x.Error)
		}
		if b, _ := os.ReadFile(filepath.Join(x.Dir, x.Name)); !bytes.Equal(b, data) {
			t.Fatalf("%d: dosya kaynakla aynı değil", i)
		}
	}
}
