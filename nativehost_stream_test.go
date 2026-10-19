package main

import (
	"bytes"
	"testing"
	"time"
)

func (f *fakeDaemon) last(cmd string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.reqs) - 1; i >= 0; i-- {
		if f.reqs[i]["cmd"] == cmd {
			return f.reqs[i]
		}
	}
	return nil
}

func TestStreamProbeMessage(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	var out bytes.Buffer
	(&nativeHost{sock: sock, wait: time.Second}).serve(frames(t, map[string]any{"type": "stream.probe", "url": "https://cdn.x/m.m3u8",
		"referrer": "https://site.x/watch?v=1", "cookies": "s=1", "userAgent": "UA", "pageUrl": "https://site.x/watch?v=1"}), &out)
	rs := readReplies(t, &out)
	info, _ := rs[0]["info"].(map[string]any)
	if rs[0]["type"] != "stream" || info["kind"] != "hls" {
		t.Fatalf("yanıt: %v", rs)
	}
	req := f.last("stream.probe")
	h, _ := req["headers"].(map[string]any)
	if req["url"] != "https://cdn.x/m.m3u8" || h["Cookie"] != "s=1" || h["Referer"] != "https://site.x/" || h["User-Agent"] != "UA" || req["pageUrl"] != "https://site.x/watch?v=1" {
		t.Fatalf("istek: %v", req)
	}

	sock2 := shortSock(t)
	startFake(t, sock2, &fakeDaemon{fail: true})
	out.Reset()
	(&nativeHost{sock: sock2, wait: time.Second}).serve(frames(t, map[string]any{"type": "stream.probe", "url": "https://cdn.x/m.m3u8"}), &out)
	if rs := readReplies(t, &out); rs[0]["type"] != "error" || rs[0]["error"] != "olmadı" {
		t.Fatalf("hata yanıtı: %v", rs)
	}
	out.Reset()
	(&nativeHost{sock: sock2, wait: time.Second}).serve(frames(t, map[string]any{"type": "stream.probe", "url": "file:///etc/passwd"}), &out)
	if rs := readReplies(t, &out); rs[0]["type"] != "error" {
		t.Fatalf("http dışı adres: %v", rs)
	}
}

func TestDownloadWithStream(t *testing.T) {
	sock := shortSock(t)
	f := &fakeDaemon{}
	startFake(t, sock, f)
	var out bytes.Buffer
	h := &nativeHost{sock: sock, wait: time.Second}
	h.serve(frames(t, map[string]any{"type": "download", "url": "https://x/m.m3u8", "filename": "T (1080p).mp4", "size": -1,
		"stream": map[string]any{"kind": "hls", "video": "v1", "label": "1080p"}}), &out)
	if rs := readReplies(t, &out); rs[0]["accepted"] != true {
		t.Fatal(rs)
	}
	st, _ := f.last("add")["stream"].(map[string]any)
	if st["kind"] != "hls" || st["video"] != "v1" || st["label"] != "1080p" {
		t.Fatalf("add stream: %v", f.last("add"))
	}
	out.Reset()
	h.serve(frames(t, map[string]any{"type": "download", "url": "https://x/a.zip"}), &out)
	if _, has := f.last("add")["stream"]; has {
		t.Fatalf("dosya indirmesine stream eklendi: %v", f.last("add"))
	}

	sock2 := shortSock(t)
	startFake(t, sock2, &fakeDaemon{fail: true})
	out.Reset()
	(&nativeHost{sock: sock2, wait: time.Second}).serve(frames(t, map[string]any{"type": "download", "url": "https://x/m.m3u8",
		"stream": map[string]any{"kind": "hls"}}), &out)
	if rs := readReplies(t, &out); rs[0]["accepted"] != false || rs[0]["error"] != "olmadı" {
		t.Fatalf("hata metni eklentiye gitmedi: %v", rs)
	}
}

func TestHelloAnnouncesStream(t *testing.T) {
	sock := shortSock(t)
	startFake(t, sock, &fakeDaemon{})
	var out bytes.Buffer
	(&nativeHost{sock: sock, wait: time.Second}).serve(frames(t, map[string]any{"type": "hello"}), &out)
	r := readReplies(t, &out)[0]
	if fs, _ := r["features"].([]any); r["version"] != "1.1" || len(fs) != 1 || fs[0] != "stream" || r["hideVideoButton"] != true {
		t.Fatalf("hello: %v", r)
	}
	out.Reset()
	(&nativeHost{sock: shortSock(t), wait: time.Second}).serve(frames(t, map[string]any{"type": "hello"}), &out)
	r = readReplies(t, &out)[0]
	if fs, _ := r["features"].([]any); r["offline"] != true || len(fs) != 1 || r["hideVideoButton"] != false {
		t.Fatalf("çevrimdışı hello: %v", r)
	}
}
