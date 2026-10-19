package engine

import (
	"testing"
	"time"
)

// stuckBackend: Close, takılmış bir gözetim döngüsünü bekler gibi hiç dönmez (Downloads izin istemi, L2 Hata 2).
type stuckBackend struct {
	fakeBackend
	release chan struct{}
}

func (s *stuckBackend) Close() { <-s.release }

// "shutdown" yanıtı dönmüşken motor Close'ta takılsa bile servis tanınan süre içinde döner; süreç çıkabilir.
func TestShutdownReturnsEvenWhenBackendCloseHangs(t *testing.T) {
	old := shutdownGrace
	shutdownGrace = 200 * time.Millisecond
	t.Cleanup(func() { shutdownGrace = old })
	b := &stuckBackend{release: make(chan struct{})}
	t.Cleanup(func() { close(b.release) })
	sock, _, done := startServer(t, b)
	c := dial(t, sock)
	if r := c.call(t, `{"id":1,"cmd":"shutdown"}`); r["ok"] != true {
		t.Fatal(r)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close takılınca servis dönmedi: süreç çıkamaz")
	}
}
