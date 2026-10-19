package main

import (
	"bytes"
	"testing"
	"time"
)

// Kaynak sayfa (tam adres) servise ayrı alan olarak gider; Referer kırpılsa da bilgi panelinde görünür.
func TestPageURLForwarded(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	h := &nativeHost{sock: sock, wait: time.Second}
	in := frames(t,
		map[string]any{"type": "download", "url": "https://cdn.net/a.zip", "referrer": "https://site.example/p?q=1", "pageUrl": "https://site.example/p?q=1"},
		map[string]any{"type": "batch", "pageUrl": "https://site.example/list", "links": []map[string]string{{"url": "https://site.example/1"}}},
		map[string]any{"type": "download", "url": "https://cdn.net/b.zip"}, // eski eklenti: alan yok
	)
	var out bytes.Buffer
	if err := h.serve(in, &out); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.reqs) != 3 || f.reqs[0]["pageUrl"] != "https://site.example/p?q=1" || f.reqs[1]["pageUrl"] != "https://site.example/list" {
		t.Fatal(f.reqs)
	}
	if _, has := f.reqs[2]["pageUrl"]; has {
		t.Fatal("boş pageUrl gönderilmemeli:", f.reqs[2])
	}
}
