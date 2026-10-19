//go:build e2e

// Video fikstür sunucusu ve fikstür sağlama testleri (VG5). Fikstürler scripts/make-video-fixtures.sh ile
// bir kez üretilir ve commit'lenir; testler ffmpeg gerektirmez (ffprobe yoksa yalnız süre/akış denetimi atlanır).
// Çalıştır: go test -race -count=1 -tags e2e -run Video ./engine
package engine

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const videoRoot = "testdata/video"

// videoReq sunucunun gördüğü bir isteğin test için önemli başlıkları.
type videoReq struct {
	Path, Range, Cookie, Referer, Origin, Auth string
}

// videoOpts: noRange Range başlığını yok sayıp her zaman 200 + tam gövde döner (Accept-Ranges yok);
// status yola göre sabit durum kodu; mutate .m3u8/.mpd gövdesini değiştirir (adresleri başka sunucuya çevirmek için);
// onReq her istekte, yanıttan önce çağrılır (yavaşlatmak/kapı koymak için; kilitsiz, testin kendisi eşler).
type videoOpts struct {
	noRange bool
	status  map[string]int
	mutate  func(path string, body []byte) []byte
	onReq   func(r *http.Request)
}

type videoServer struct {
	*httptest.Server
	mu   sync.Mutex
	reqs []videoReq
}

func newVideoServer(t *testing.T, o videoOpts) *videoServer {
	t.Helper()
	vs := &videoServer{}
	vs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		vs.mu.Lock()
		vs.reqs = append(vs.reqs, videoReq{r.URL.Path, r.Header.Get("Range"), r.Header.Get("Cookie"),
			r.Header.Get("Referer"), r.Header.Get("Origin"), r.Header.Get("Authorization")})
		vs.mu.Unlock()
		if o.onReq != nil {
			o.onReq(r)
		}
		if code := o.status[r.URL.Path]; code != 0 {
			http.Error(w, "fikstür hata", code)
			return
		}
		name := filepath.Join(videoRoot, filepath.FromSlash(strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")))
		b, err := os.ReadFile(name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		ext := filepath.Ext(name)
		if o.mutate != nil && (ext == ".m3u8" || ext == ".mpd") {
			b = o.mutate(r.URL.Path, b)
		}
		w.Header().Set("Content-Type", videoMIME[ext])
		if o.noRange {
			r.Header.Del("Range")
			w.Header().Set("Content-Length", strconv.Itoa(len(b)))
			w.Write(b)
			return
		}
		http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(b))
	}))
	t.Cleanup(vs.Close)
	return vs
}

var videoMIME = map[string]string{
	".m3u8": "application/vnd.apple.mpegurl", ".mpd": "application/dash+xml", ".ts": "video/mp2t",
	".mp4": "video/mp4", ".m4s": "video/iso.segment", ".bin": "application/octet-stream",
}

// requests anlık kopya; contains yoluna (önek) kaç istek geldiğini sayar.
func (vs *videoServer) requests() []videoReq {
	vs.mu.Lock()
	defer vs.mu.Unlock()
	return append([]videoReq(nil), vs.reqs...)
}

func (vs *videoServer) count(prefix string) int {
	n := 0
	for _, r := range vs.requests() {
		if strings.HasPrefix(r.Path, prefix) {
			n++
		}
	}
	return n
}

// ffprobeVideo: akış sayısı ve süre; ffprobe yoksa test atlanır.
func ffprobeVideo(t *testing.T, path string) (streams int, dur float64) {
	t.Helper()
	bin, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe yok")
	}
	out, err := exec.Command(bin, "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "json", path).Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var r struct {
		Streams []struct {
			CodecType string `json:"codec_type"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatal(err)
	}
	dur, _ = strconv.ParseFloat(r.Format.Duration, 64)
	return len(r.Streams), dur
}

func TestVideoFixturesPresent(t *testing.T) {
	var total int64
	files := 0
	filepath.WalkDir(videoRoot, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			info, _ := d.Info()
			total += info.Size()
			files++
		}
		return nil
	})
	if files == 0 {
		t.Fatalf("%s boş: scripts/make-video-fixtures.sh çalıştırın", videoRoot)
	}
	if total > 1<<20 {
		t.Errorf("fikstürler %d bayt, sınır 1 MiB", total)
	}
	has := func(rel, want string, present bool) {
		b, err := os.ReadFile(filepath.Join(videoRoot, rel))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), want) != present {
			t.Errorf("%s: %q var=%v olmalıydı", rel, want, !present)
		}
	}
	has("hls-ts/master.m3u8", "#EXT-X-STREAM-INF", true)
	has("hls-ts/v0/index.m3u8", "#EXT-X-ENDLIST", true)
	has("hls-fmp4/index.m3u8", "#EXT-X-MAP:URI=", true)
	has("hls-fmp4-audio/master.m3u8", "TYPE=AUDIO", true)
	has("hls-aes/index.m3u8", "#EXT-X-KEY:METHOD=AES-128", true)
	has("hls-live/index.m3u8", "#EXT-X-ENDLIST", false) // canlı: ENDLIST yok
	has("hls-live/index.m3u8", "PLAYLIST-TYPE", false)
	has("dash/manifest.mpd", "ContentProtection", false)
	has("dash/manifest.mpd", "SegmentTimeline", true)
	has("dash-drm/manifest.mpd", "ContentProtection", true)
}

func TestVideoFixtureDirectMP4(t *testing.T) {
	n, d := ffprobeVideo(t, filepath.Join(videoRoot, "direct.mp4"))
	if n != 2 || d < 3.5 || d > 4.5 {
		t.Errorf("direct.mp4: akış=%d süre=%.2f (2 akış, 4±0,5 sn beklenir)", n, d)
	}
}

func vget(t *testing.T, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestVideoServerRangeModes(t *testing.T) {
	rng := newVideoServer(t, videoOpts{})
	if r := vget(t, rng.URL+"/direct.mp4", map[string]string{"Range": "bytes=0-9"}); r.StatusCode != 206 {
		t.Errorf("Range'li sunucu: %d, 206 beklenir", r.StatusCode)
	}
	plain := newVideoServer(t, videoOpts{noRange: true})
	r := vget(t, plain.URL+"/direct.mp4", map[string]string{"Range": "bytes=0-9"})
	if r.StatusCode != 200 || r.Header.Get("Accept-Ranges") != "" || r.Header.Get("Content-Range") != "" {
		t.Errorf("Range'siz sunucu: %d %v, 200 ve aralıksız beklenir", r.StatusCode, r.Header)
	}
	if got := plain.requests(); len(got) != 1 || got[0].Range != "bytes=0-9" {
		t.Errorf("istek kaydı: %+v", got)
	}
}

func TestVideoServerStatusAndMutate(t *testing.T) {
	vs := newVideoServer(t, videoOpts{
		status: map[string]int{"/hls-ts/v0/seg002.ts": 403},
		mutate: func(_ string, b []byte) []byte { return bytes.ReplaceAll(b, []byte("seg"), []byte("xseg")) },
	})
	if r := vget(t, vs.URL+"/hls-ts/v0/seg002.ts", nil); r.StatusCode != 403 {
		t.Errorf("durum: %d", r.StatusCode)
	}
	b := new(bytes.Buffer)
	b.ReadFrom(vget(t, vs.URL+"/hls-ts/v0/index.m3u8", nil).Body)
	if !strings.Contains(b.String(), "xseg000.ts") {
		t.Errorf("mutate uygulanmadı: %s", b)
	}
	if vget(t, vs.URL+"/../video.go", nil).StatusCode != 404 {
		t.Error("kök dışı yol 404 olmalı")
	}
}
