//go:build e2e

// Video uçtan uca (VG5): gerçek Manager + DefaultDeps + soket servisi, fikstür sunucusu. Yalnız soket sözleşmesini
// kullanır (VG1-design §4): "stream.probe", "add"+"stream". Motor henüz "stream" bilmiyorsa testler atlanır.
// Çalıştır: go test -race -count=1 -tags e2e -run Video ./engine
package engine

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type videoEnv struct {
	t   *testing.T
	cmd *sockClient
	dl  string
}

func newVideoEnv(t *testing.T) *videoEnv {
	t.Helper()
	dataDir, dl, trash, sock := t.TempDir(), t.TempDir(), t.TempDir(), sockIn(t)
	var ran [][]string
	d := startDaemon(t, dataDir, sock, trash, &ran)
	t.Cleanup(func() { d.shutdown(t) })
	e := &videoEnv{t: t, cmd: dialSock(t, sock), dl: dl}
	st := e.cmd.call("list", nil)["settings"].(map[string]any)
	st["downloadDir"] = dl
	e.cmd.call("settings.set", map[string]any{"settings": st})
	if _, err := e.raw("stream.probe", map[string]any{"url": "http://127.0.0.1:1/x.m3u8"}); err != nil && strings.Contains(err.Error(), "Bilinmeyen komut") {
		t.Skip("motor stream.probe bilmiyor (VG2 rc'de değil)")
	}
	return e
}

// raw komutu yollar; ok=false yanıtı hata olarak döner (call gibi Fatal etmez).
func (e *videoEnv) raw(cmd string, args map[string]any) (map[string]any, error) {
	e.t.Helper()
	e.cmd.id++
	req := map[string]any{"id": e.cmd.id, "cmd": cmd}
	for k, v := range args {
		req[k] = v
	}
	b, _ := json.Marshal(req)
	if _, err := e.cmd.c.Write(append(b, '\n')); err != nil {
		e.t.Fatal(err)
	}
	for {
		m := e.cmd.next()
		if _, ev := m["event"]; ev {
			continue
		}
		if m["ok"] != true {
			return nil, fmt.Errorf("%v", m["error"])
		}
		return m, nil
	}
}

func (e *videoEnv) probe(u string, hdr map[string]string) (map[string]any, error) {
	m, err := e.raw("stream.probe", map[string]any{"url": u, "headers": hdr})
	if err != nil {
		return nil, err
	}
	return m["info"].(map[string]any), nil
}

func (e *videoEnv) add(args map[string]any) (map[string]any, error) {
	args["dir"], args["start"] = e.dl, true
	m, err := e.raw("add", args)
	if err != nil {
		return nil, err
	}
	return m["item"].(map[string]any), nil
}

func (e *videoEnv) addStream(u, kind, video, audio string, hdr map[string]string) (map[string]any, error) {
	return e.add(map[string]any{"url": u, "headers": hdr, "stream": map[string]any{"kind": kind, "video": video, "audio": audio}})
}

func (e *videoEnv) item(id string) map[string]any {
	e.t.Helper()
	for _, x := range e.cmd.call("list", nil)["items"].([]any) {
		if m := x.(map[string]any); m["id"] == id {
			return m
		}
	}
	e.t.Fatalf("öğe yok: %s", id)
	return nil
}

// until öğe pred'i sağlayana dek (en çok 30 sn) yoklar.
func (e *videoEnv) until(id string, what string, pred func(map[string]any) bool) map[string]any {
	e.t.Helper()
	for t0 := time.Now(); time.Since(t0) < 30*time.Second; time.Sleep(50 * time.Millisecond) {
		if it := e.item(id); pred(it) {
			return it
		}
	}
	e.t.Fatalf("zaman aşımı (%s): %v", what, e.item(id))
	return nil
}

func (e *videoEnv) finish(id string) map[string]any {
	e.t.Helper()
	return e.until(id, "done/error", func(m map[string]any) bool { return m["state"] == "done" || m["state"] == "error" })
}

// wantMedia: biten öğenin dosyası ext ile biter, 2 akış ve 4±0,5 sn (ffprobe yoksa yalnız dosya varlığı).
func (e *videoEnv) wantMedia(it map[string]any, ext string, streams int) {
	e.t.Helper()
	if it["state"] != "done" {
		e.t.Fatalf("bitmedi: state=%v error=%v", it["state"], it["error"])
	}
	p := filepath.Join(it["dir"].(string), it["name"].(string))
	if !strings.HasSuffix(p, ext) {
		e.t.Fatalf("uzantı %s değil: %s", ext, p)
	}
	if fi, err := os.Stat(p); err != nil || fi.Size() == 0 {
		e.t.Fatalf("dosya yok/boş: %s %v", p, err)
	}
	if n, d := ffprobeVideo(e.t, p); n != streams || d < 3.5 || d > 4.5 {
		e.t.Errorf("%s: akış=%d süre=%.2f (%d akış, 4±0,5 sn beklenir)", p, n, d, streams)
	}
}

const (
	errProtectedText = "Bu video korumalı (DRM), indirilemez"
	errLiveText      = "Canlı yayın indirilemez"
)

func TestVideoDirectPlainAndNoRange(t *testing.T) {
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{noRange: true}) // (8) Range'i yok sayan sunucuda doğrudan dosya
	it, err := e.add(map[string]any{"url": vs.URL + "/direct.mp4"})
	if err != nil {
		t.Fatal(err)
	}
	e.wantMedia(e.finish(it["id"].(string)), ".mp4", 2)
}

func TestVideoHLSTS(t *testing.T) {
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{})
	info, err := e.probe(vs.URL+"/hls-ts/master.m3u8", nil)
	if err != nil {
		t.Fatal(err)
	}
	vars := info["variants"].([]any)
	if info["kind"] != "hls" || info["container"] != "ts" || len(vars) != 2 || info["live"] == true || info["protected"] == true {
		t.Fatalf("probe: %v", info)
	}
	top := vars[0].(map[string]any)
	if top["height"].(float64) != 90 { // yüksekten düşüğe
		t.Errorf("ilk varyant 90p olmalı: %v", top)
	}
	it, err := e.addStream(vs.URL+"/hls-ts/master.m3u8", "hls", top["id"].(string), "", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.wantMedia(e.finish(it["id"].(string)), ".ts", 2)
}

func TestVideoHLSFMP4(t *testing.T) {
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{})
	it, err := e.addStream(vs.URL+"/hls-fmp4/index.m3u8", "hls", "", "", nil) // EXT-X-MAP, gömülü ses: tek iz
	if err != nil {
		t.Fatal(err)
	}
	e.wantMedia(e.finish(it["id"].(string)), ".mp4", 2)

	info, err := e.probe(vs.URL+"/hls-fmp4-audio/master.m3u8", nil) // ayrı ses: iki iz birleşir
	if err != nil {
		t.Fatal(err)
	}
	v, a := info["variants"].([]any), info["audios"].([]any)
	if len(v) != 1 || len(a) != 1 {
		t.Fatalf("probe: %v", info)
	}
	it, err = e.addStream(vs.URL+"/hls-fmp4-audio/master.m3u8", "hls", v[0].(map[string]any)["id"].(string), a[0].(map[string]any)["id"].(string), nil)
	if err != nil {
		t.Fatal(err)
	}
	e.wantMedia(e.finish(it["id"].(string)), ".mp4", 2)
}

func TestVideoHLSAES128Rejected(t *testing.T) {
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{})
	info, err := e.probe(vs.URL+"/hls-aes/index.m3u8", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info["protected"] != true {
		t.Errorf("AES-128 korumalı sayılmalı: %v", info)
	}
	if _, err := e.addStream(vs.URL+"/hls-aes/index.m3u8", "hls", "", "", nil); err == nil || !strings.Contains(err.Error(), errProtectedText) {
		t.Errorf("add hatası %q, %q beklenir", err, errProtectedText)
	}
	if n := vs.count("/hls-aes/seg"); n != 0 {
		t.Errorf("korumalı akışın parçasına %d istek gitti, 0 olmalı", n)
	}
}

func TestVideoHLSLiveRejected(t *testing.T) {
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{})
	info, err := e.probe(vs.URL+"/hls-live/index.m3u8", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info["live"] != true {
		t.Errorf("ENDLIST'siz liste canlı sayılmalı: %v", info)
	}
	if _, err := e.addStream(vs.URL+"/hls-live/index.m3u8", "hls", "", "", nil); err == nil || !strings.Contains(err.Error(), errLiveText) {
		t.Errorf("add hatası %q, %q beklenir", err, errLiveText)
	}
	if n := vs.count("/hls-live/seg"); n != 0 {
		t.Errorf("canlı akışın parçasına %d istek gitti", n)
	}
}

func TestVideoDASHSeparateAV(t *testing.T) {
	e := newVideoEnv(t)
	for name, noRange := range map[string]bool{"range": false, "norange": true} { // (6) ve (8)
		t.Run(name, func(t *testing.T) {
			e.t = t
			vs := newVideoServer(t, videoOpts{noRange: noRange})
			info, err := e.probe(vs.URL+"/dash/manifest.mpd", nil)
			if err != nil {
				t.Fatal(err)
			}
			v, a := info["variants"].([]any), info["audios"].([]any)
			if info["kind"] != "dash" || len(v) != 1 || len(a) != 1 {
				t.Fatalf("probe: %v", info)
			}
			it, err := e.addStream(vs.URL+"/dash/manifest.mpd", "dash", v[0].(map[string]any)["id"].(string), a[0].(map[string]any)["id"].(string), nil)
			if err != nil {
				t.Fatal(err)
			}
			e.wantMedia(e.finish(it["id"].(string)), ".mp4", 2)
		})
	}
}

func TestVideoDASHContentProtectionRejected(t *testing.T) {
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{})
	info, err := e.probe(vs.URL+"/dash-drm/manifest.mpd", nil)
	if err != nil {
		t.Fatal(err)
	}
	if info["protected"] != true {
		t.Errorf("ContentProtection korumalı sayılmalı: %v", info)
	}
	if _, err := e.addStream(vs.URL+"/dash-drm/manifest.mpd", "dash", "", "", nil); err == nil || !strings.Contains(err.Error(), errProtectedText) {
		t.Errorf("add hatası %q", err)
	}
	if n := vs.count("/dash/chunk"); n != 0 {
		t.Errorf("korumalı akışın parçasına %d istek gitti", n)
	}
}

func TestVideoNoRangeServerHLS(t *testing.T) { // (8) parçalar için
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{noRange: true})
	it, err := e.addStream(vs.URL+"/hls-ts/v0/index.m3u8", "hls", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	e.wantMedia(e.finish(it["id"].(string)), ".ts", 2)
}

func TestVideoCrossOriginSegments(t *testing.T) { // (9)
	e := newVideoEnv(t)
	segs := newVideoServer(t, videoOpts{})
	u, _ := url.Parse(segs.URL)
	other := "http://localhost:" + u.Port() // ikinci köken: farklı ana makine adı
	page := newVideoServer(t, videoOpts{mutate: func(_ string, b []byte) []byte {
		return []byte(strings.ReplaceAll(string(b), "seg", other+"/hls-ts/v0/seg"))
	}})
	hdr := map[string]string{"Cookie": "sid=gizli", "Referer": page.URL + "/izle/video?x=1"}
	it, err := e.addStream(page.URL+"/hls-ts/v0/index.m3u8", "hls", "", "", hdr)
	if err != nil {
		t.Fatal(err)
	}
	e.wantMedia(e.finish(it["id"].(string)), ".ts", 2)
	rs := segs.requests()
	if len(rs) == 0 {
		t.Fatal("parça sunucusuna istek gelmedi")
	}
	for _, r := range rs {
		if r.Cookie != "" || r.Auth != "" {
			t.Errorf("başka siteye Cookie/Authorization gitti: %+v", r)
		}
		if r.Referer != page.URL+"/" {
			t.Errorf("Referer yalnız köken olmalı (%s/), gelen %q", page.URL, r.Referer)
		}
		if r.Origin != page.URL {
			t.Errorf("Origin %q, %q beklenir", r.Origin, page.URL)
		}
	}
	for _, r := range page.requests() { // manifest kendi sitesinden: çerez gitmeli
		if strings.HasSuffix(r.Path, ".m3u8") && r.Cookie != "sid=gizli" {
			t.Errorf("manifest isteğinde çerez yok: %+v", r)
		}
	}
}

func TestVideoSegment403(t *testing.T) { // (10)
	e := newVideoEnv(t)
	vs := newVideoServer(t, videoOpts{status: map[string]int{"/hls-ts/v0/seg002.ts": 403}})
	it, err := e.addStream(vs.URL+"/hls-ts/v0/index.m3u8", "hls", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := e.finish(it["id"].(string))
	if got["state"] != "error" || !strings.Contains(fmt.Sprint(got["error"]), "Akış adresinin süresi dolmuş olabilir") {
		t.Errorf("öğe: state=%v error=%v", got["state"], got["error"])
	}
}

func TestVideoPauseResumeKeepsSegments(t *testing.T) { // (11)
	e := newVideoEnv(t)
	gate := make(chan struct{})
	var released atomic.Bool
	vs := newVideoServer(t, videoOpts{onReq: func(r *http.Request) {
		if r.URL.Path == "/hls-ts/v0/seg002.ts" && !released.Load() {
			select { // dördüncü parça askıda: öteki üçü iner
			case <-gate:
			case <-r.Context().Done():
			}
		}
	}})
	it, err := e.addStream(vs.URL+"/hls-ts/v0/index.m3u8", "hls", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	id := it["id"].(string)
	segDone := func(m map[string]any) int {
		s, _ := m["stream"].(map[string]any)
		n, _ := s["segDone"].(float64)
		return int(n)
	}
	e.until(id, "3 parça indi", func(m map[string]any) bool { return segDone(m) >= 3 })
	e.cmd.call("pause", map[string]any{"ids": []string{id}})
	e.until(id, "paused", func(m map[string]any) bool { return m["state"] == "paused" })
	released.Store(true)
	close(gate)
	e.cmd.call("resume", map[string]any{"ids": []string{id}})
	e.wantMedia(e.finish(id), ".ts", 2)
	for _, p := range []string{"seg000", "seg001", "seg003"} {
		if n := vs.count("/hls-ts/v0/" + p + ".ts"); n != 1 {
			t.Errorf("%s %d kez istendi, devamda yeniden istenmemeli (1)", p, n)
		}
	}
	if n := vs.count("/hls-ts/v0/seg002.ts"); n < 2 {
		t.Errorf("seg002 %d kez istendi, devamda yeniden istenmeli (≥2)", n)
	}
}
