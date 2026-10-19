package engine

import "testing"

func TestServerStreamCommands(t *testing.T) {
	fb := &fakeBackend{}
	sock, _, _ := startServer(t, fb)
	c := dial(t, sock)
	r := c.call(t, `{"id":1,"cmd":"stream.probe","url":"https://x/m.m3u8","headers":{"Cookie":"drm"},"pageUrl":"https://x/izle"}`)
	info, _ := r["info"].(map[string]any)
	if r["ok"] != true || info["kind"] != "hls" || info["container"] != "ts" || info["protected"] != true {
		t.Fatalf("stream.probe: %v", r)
	}
	if v, ok := info["variants"].([]any); !ok || v == nil {
		t.Fatalf("variants null: %v", info)
	}
	if a, ok := info["audios"].([]any); !ok || a == nil {
		t.Fatalf("audios null: %v", info)
	}
	if r = c.call(t, `{"id":2,"cmd":"stream.probe","url":"https://x/err"}`); r["ok"] != false || r["error"] != errLive {
		t.Fatalf("hata: %v", r)
	}
	if r = c.call(t, `{"id":4,"cmd":"stream.probe","url":"https://x/m.m3u8","pageUrl":"https://baska/"}`); r["ok"] != false {
		t.Fatalf("pageUrl iletilmedi: %v", r)
	}
	if r = c.call(t, `{"id":3,"cmd":"probe","url":"https://x/m.m3u8"}`); r["kind"] != "hls" {
		t.Fatalf("probe kind: %v", r)
	}
	if r = c.call(t, `{"id":4,"cmd":"probe","url":"http://x"}`); r["ok"] != true {
		t.Fatal(r)
	} else if _, has := r["kind"]; has {
		t.Fatalf("dosya için kind yazıldı: %v", r)
	}
	if r = c.call(t, `{"id":5,"cmd":"add","url":"https://x/m.m3u8","stream":{"kind":"hls","video":"v1","label":"1080p"}}`); r["ok"] != true {
		t.Fatal(r)
	}
	fb.mu.Lock()
	defer fb.mu.Unlock()
	if s := fb.added[0].Stream; s == nil || s.Video != "v1" || s.Label != "1080p" {
		t.Fatalf("add stream: %+v", s)
	}
}
