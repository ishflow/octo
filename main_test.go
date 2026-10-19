package main

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"hizli-indir/engine"
)

func TestRunDownloadsFile(t *testing.T) {
	data := bytes.Repeat([]byte("hizli"), 200<<10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.bin", time.Time{}, bytes.NewReader(data))
	}))
	defer srv.Close()
	out := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(out, []byte("eski"), 0o600) // -o açık hedeftir: "x (2).bin" değil, üzerine yazılır (SEC1R N3)
	name, err := run(srv.URL+"/f.bin", out, 4)
	if err != nil || name != out {
		t.Fatal(name, err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, data) {
		t.Fatal("içerik farklı")
	}
}

// Kapanışta (shutdown/SIGTERM) süren Add/Probe Yönetici bağlamı kesilerek bırakılır; servis Probe'u beklemez (V1a Minor 8).
func TestShutdownCancelsInflightProbe(t *testing.T) {
	deps := engine.DefaultDeps()
	probing := make(chan struct{})
	deps.Probe = func(ctx context.Context, _ string, _ http.Header, _ engine.AuthFunc) (engine.ProbeResult, error) {
		close(probing)
		<-ctx.Done()
		return engine.ProbeResult{}, ctx.Err()
	}
	m, err := engine.NewManager(t.TempDir(), deps)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	sock := shortSock(t)
	ln, err := engine.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- engine.ServeListener(context.Background(), m, ln) }()

	adder, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer adder.Close()
	adder.Write([]byte(`{"id":1,"cmd":"add","url":"http://127.0.0.1:1/a.zip"}` + "\n"))
	<-probing
	stopper, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer stopper.Close()
	stopper.Write([]byte(`{"id":2,"cmd":"shutdown"}` + "\n"))
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("servis süren Probe yüzünden kapanmadı")
	}
}
