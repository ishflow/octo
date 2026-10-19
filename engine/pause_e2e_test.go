//go:build e2e

// Canlı denemede (L1) görülen "pause ok ama durmadı" için: gerçek Manager + DefaultDeps ile art arda
// duraklat/devam; Resume'un ilk isteği sürerken (5 ms, 50 ms) duraklatma da dahil.
// Çalıştır: go test -tags e2e -race -run TestE2EPauseResumeCycles -v ./engine
package engine

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// patReader: boyutu verilen, içeriği konumdan türeyen sanal dosya.
type patReader struct{ off, size int64 }

func (r *patReader) Read(p []byte) (int, error) {
	if r.off >= r.size {
		return 0, io.EOF
	}
	n := int(min(int64(len(p)), r.size-r.off, 64<<10))
	for i := 0; i < n; i++ {
		p[i] = byte((r.off + int64(i)) * 7)
	}
	r.off += int64(n)
	time.Sleep(10 * time.Millisecond) // ~6 MB/s per conn
	return n, nil
}
func (r *patReader) Seek(o int64, w int) (int64, error) {
	switch w {
	case io.SeekStart:
		r.off = o
	case io.SeekEnd:
		r.off = r.size + o
	}
	return r.off, nil
}

func TestE2EPauseResumeCycles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.iso", time.Time{}, &patReader{size: 4 << 30})
	}))
	defer srv.Close()
	dir, dl := t.TempDir(), t.TempDir()
	m, err := NewManager(dir, DefaultDeps())
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s := m.settings
	s.DownloadDir = dl
	m.SetSettings(s)
	it, err := m.Add(AddArgs{URL: srv.URL + "/x.iso", Name: "x.iso", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	get := func() Item {
		items, _, _ := m.List()
		for _, x := range items {
			if x.ID == it.ID {
				return x
			}
		}
		return Item{}
	}
	delays := []time.Duration{3 * time.Second, 50 * time.Millisecond, 2500 * time.Millisecond, 5 * time.Millisecond, 4 * time.Second, 300 * time.Millisecond, 2100 * time.Millisecond}
	for i, dly := range delays {
		time.Sleep(dly)
		if st := get().State; st != StateDownloading {
			t.Fatalf("döngü %d: duraklatmadan önce %s (%s)", i, st, get().Error)
		}
		m.Pause([]string{it.ID})
		deadline := time.Now().Add(3 * time.Second)
		for get().State != StatePaused {
			if time.Now().After(deadline) {
				t.Fatalf("döngü %d: pause sonrası %s got=%d", i, get().State, get().Got)
			}
			time.Sleep(10 * time.Millisecond)
		}
		g := get().Got
		time.Sleep(400 * time.Millisecond)
		if x := get(); x.Got != g || x.State != StatePaused {
			t.Fatalf("döngü %d: duraklatıldıktan sonra sürdü %d→%d %s", i, g, x.Got, x.State)
		}
		m.Resume([]string{it.ID})
	}
}
