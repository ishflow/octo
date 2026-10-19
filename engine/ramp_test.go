package engine

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// TestRampTiming: 1→2 beklemesiz, sonra en erken 2 sn'de bir ikiye katlanır (2→4→8).
// Sunucu bağlantı başına ~2 MB/s verir; sınır yok, yani her artış rampEvery'den hemen sonra gelmeli.
func TestRampTiming(t *testing.T) {
	data := rnd(64 << 20)
	srv, _ := serve(t, data, nil)
	d, err := Start(context.Background(), Request{URL: srv.URL + "/f.bin", Path: filepath.Join(t.TempDir(), "f.bin"), MaxConns: 8}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Pause(); d.Wait() })
	start := time.Now()
	reached := map[int]time.Duration{}
	for time.Since(start) < 10*time.Second && reached[8] == 0 {
		d.mu.Lock()
		n := d.active
		d.mu.Unlock()
		if _, ok := reached[n]; !ok {
			reached[n] = time.Since(start)
			t.Logf("t=%v active=%d", reached[n].Round(10*time.Millisecond), n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Pay: watch 250 ms'de bir bakar, yani her adım 2–2,25 sn; 2→4 ~2,3 sn, 4→8 ~4,5 sn olmalı.
	for n, limit := range map[int]time.Duration{2: time.Second, 4: 3 * time.Second, 8: 5500 * time.Millisecond} {
		if at, ok := reached[n]; !ok || at > limit {
			t.Errorf("%d bağlantıya %v'de ulaşıldı (ok=%v), en geç %v bekleniyordu", n, at, ok, limit)
		}
	}
}
