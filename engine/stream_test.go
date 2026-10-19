package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- plan / probe (ağsız) ----

func fakeFetch(t *testing.T, files map[string]string) fetchFunc {
	return func(u string) ([]byte, *url.URL, error) {
		b, ok := files[u]
		if !ok {
			return nil, nil, fmt.Errorf("yok: %s", u)
		}
		return []byte(b), mustURL(t, u), nil
	}
}

const fmp4Media = "#EXTM3U\n#EXT-X-MAP:URI=\"init.mp4\"\n#EXTINF:2,\ns0.m4s\n#EXTINF:2,\ns1.m4s\n#EXT-X-ENDLIST\n"

func TestProbeStreamHLSMaster(t *testing.T) {
	f := fakeFetch(t, map[string]string{
		"https://site.example/v/master.m3u8":    hlsMaster,
		"https://cdn.example.com/hi/index.m3u8": fmp4Media,
	})
	info, err := probeStream("https://site.example/v/master.m3u8", f)
	if err != nil {
		t.Fatal(err)
	}
	if info.Kind != "hls" || info.Container != "mp4" || info.Live || info.Protected || info.Duration != 4 {
		t.Fatalf("info: %+v", info)
	}
	vs, as := masterIDs(t, hlsMaster)
	if len(info.Variants) != 2 || info.Variants[0].ID != vs[1] || info.Variants[0].Label != "1080p" || info.Variants[0].Audio != as[0] || info.Variants[1].ID != vs[0] {
		t.Fatalf("varyantlar yüksekten düşüğe değil: %+v", info.Variants)
	}
	if len(info.Audios) != 2 || info.Audios[0].ID != as[0] || !info.Audios[0].Default {
		t.Fatalf("sesler: %+v", info.Audios)
	}
}

func TestProbeStreamFlags(t *testing.T) {
	cases := map[string]struct {
		body       string
		live, prot bool
	}{
		"aes":      {"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k\"\n#EXTINF:1,\ns.ts\n#EXT-X-ENDLIST\n", false, true},
		"canlı":    {"#EXTM3U\n#EXTINF:1,\ns.ts\n", true, false},
		"dash dyn": {`<MPD type="dynamic"><Period/></MPD>`, true, false},
		"dash drm": {`<MPD type="static"><Period><AdaptationSet><ContentProtection/></AdaptationSet></Period></MPD>`, false, true},
	}
	for name, c := range cases {
		info, err := probeStream("https://x.example/m", fakeFetch(t, map[string]string{"https://x.example/m": c.body}))
		if err != nil || info.Live != c.live || info.Protected != c.prot || info.Variants == nil || info.Audios == nil {
			t.Errorf("%s: %+v %v", name, info, err)
		}
	}
	// Tek medya listesi: seçilecek tek "Video" seçeneği.
	info, err := probeStream("https://x.example/m", fakeFetch(t, map[string]string{"https://x.example/m": fmp4Media}))
	if err != nil || len(info.Variants) != 1 || info.Variants[0].ID != "" || info.Container != "mp4" {
		t.Fatalf("medya listesi: %+v %v", info, err)
	}
	if _, err := probeStream("https://x.example/m", fakeFetch(t, map[string]string{"https://x.example/m": "<html></html>"})); err == nil {
		t.Fatal("HTML akış sayıldı")
	}
}

func TestPlanStreamHLSSeparateAudio(t *testing.T) {
	f := fakeFetch(t, map[string]string{
		"https://site.example/v/master.m3u8":    hlsMaster,
		"https://site.example/v/low/index.m3u8": fmp4Media,
		"https://site.example/v/audio/en.m3u8":  fmp4Media,
		"https://site.example/v/audio/tr.m3u8":  strings.ReplaceAll(fmp4Media, "s0", "tr0"),
	})
	vs, as := masterIDs(t, hlsMaster)
	p, err := planStream("https://site.example/v/master.m3u8", StreamSpec{Kind: "hls", Video: vs[0], Audio: as[1]}, f)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 2 || p.Container != "mp4" || p.Tracks[1].Segs[0].URL != "https://site.example/v/audio/tr0.m4s" || p.Tracks[0].Init == nil {
		t.Fatalf("plan: %+v", p)
	}
	if p, err = planStream("https://site.example/v/master.m3u8", StreamSpec{Kind: "hls", Video: vs[0]}, f); err != nil || p.Tracks[1].Segs[0].URL != "https://site.example/v/audio/s0.m4s" {
		t.Fatalf("varsayılan ses seçilmedi: %+v %v", p, err)
	}
	if _, err = planStream("https://site.example/v/master.m3u8", StreamSpec{Kind: "hls", Video: "v9"}, f); err == nil {
		t.Fatal("olmayan varyant kabul edildi")
	}
}

func TestPlanStreamTSAudioFallback(t *testing.T) {
	ts := "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n"
	master := `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="a",NAME="en",DEFAULT=YES,URI="a.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=5000000,RESOLUTION=1920x1080,AUDIO="a"
hi.m3u8
#EXT-X-STREAM-INF:BANDWIDTH=900000,RESOLUTION=640x360
mux.m3u8
`
	files := map[string]string{"https://x.example/m.m3u8": master, "https://x.example/hi.m3u8": ts, "https://x.example/mux.m3u8": ts, "https://x.example/a.m3u8": ts}
	vs, _ := masterIDs(t, master)
	p, err := planStream("https://x.example/m.m3u8", StreamSpec{Kind: "hls", Video: vs[0]}, fakeFetch(t, files))
	if err != nil || len(p.Tracks) != 1 || p.Container != "ts" || p.Label != "360p" {
		t.Fatalf("gömülü seslı varyanta düşmedi: %+v %v", p, err)
	}
	delete(files, "https://x.example/mux.m3u8")
	files["https://x.example/m.m3u8"] = strings.SplitAfter(master, "hi.m3u8\n")[0]
	if _, err := planStream("https://x.example/m.m3u8", StreamSpec{Kind: "hls", Video: vs[0]}, fakeFetch(t, files)); err == nil || !strings.Contains(err.Error(), "Ayrı ses") {
		t.Fatalf("ayrı sesli TS reddedilmedi: %v", err)
	}
}

func TestPlanStreamDASH(t *testing.T) {
	f := fakeFetch(t, map[string]string{"https://cdn.example/v/m.mpd": mpdTimeline})
	p, err := planStream("https://cdn.example/v/m.mpd", StreamSpec{Kind: "dash"}, f)
	if err != nil || len(p.Tracks) != 2 || p.Tracks[0].Init.URL != "https://cdn.example/v/init-2.m4s" || p.Label != "1080p" {
		t.Fatalf("en yüksek görüntü + ses: %+v %v", p, err)
	}
	p, err = planStream("https://cdn.example/v/m.mpd", StreamSpec{Kind: "dash", Video: "0", Audio: "1"}, f)
	if err != nil || p.Tracks[0].Init.URL != "https://cdn.example/v/init-0.m4s" || p.Segments() != 10 {
		t.Fatalf("seçim: %+v %v", p, err)
	}
	if _, err := planStream("https://cdn.example/v/m.mpd", StreamSpec{Kind: "dash", Audio: "x"}, f); err == nil {
		t.Fatal("olmayan ses kabul edildi")
	}
}

// ---- indirme (yerel test sunucusu) ----

type streamSrv struct {
	mu   sync.Mutex
	hits map[string]int
	hdrs map[string]http.Header
}

func (s *streamSrv) note(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hits == nil {
		s.hits, s.hdrs = map[string]int{}, map[string]http.Header{}
	}
	s.hits[r.URL.Path]++
	s.hdrs[r.URL.Path] = r.Header.Clone()
}

func (s *streamSrv) count(p string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits[p]
}

func runStream(t *testing.T, u string, spec StreamSpec, h http.Header) (string, *StreamJob, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.out")
	j, err := StartStream(context.Background(), Request{URL: u, Path: path, Header: h, Stream: &spec}, nil)
	if err != nil {
		return path, nil, err
	}
	return path, j, j.Wait()
}

func noTemp(t *testing.T, path string) {
	t.Helper()
	for _, p := range []string{path + ".octo-parts", path + ".stream.json", path + ".part"} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("geçici dosya kaldı: %s", p)
		}
	}
}

func TestStreamDownloadHLSTS(t *testing.T) {
	var s streamSrv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.note(r)
		switch r.URL.Path {
		case "/p.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXTINF:1,\ns1.ts\n#EXTINF:1,\ns2.ts\n#EXT-X-ENDLIST\n")
		default:
			if r.Header.Get("Range") != "" {
				t.Errorf("tüm parça isteğinde Range: %s", r.Header.Get("Range"))
			}
			fmt.Fprint(w, strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/"), ".ts"))
		}
	}))
	defer srv.Close()
	path, j, err := runStream(t, srv.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "s0s1s2" {
		t.Fatalf("içerik %q", b)
	}
	if p := j.StreamProgress(); p.SegDone != 3 || p.Segments != 3 {
		t.Fatalf("ilerleme %+v", p)
	}
	if sn := j.Snapshot(); sn.Got != 6 || sn.Path != path || !sn.Resumable {
		t.Fatalf("snapshot %+v", sn)
	}
	noTemp(t, path)
}

// Video dosyası ve ilk kez açılan kategori klasörü sıradan indirme gibi umask'a bırakılır (0644/0755), 0600/0700 değil.
func TestStreamOutputPermissions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n")
			return
		}
		fmt.Fprint(w, "ok")
	}))
	defer srv.Close()
	dir := filepath.Join(t.TempDir(), "Video")
	path := filepath.Join(dir, "v.ts")
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/p.m3u8", Path: path, Stream: &StreamSpec{Kind: "hls"}}, nil)
	if err == nil {
		err = j.Wait()
	}
	if err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]os.FileMode{dir: 0o005, path: 0o004} { // başkaları okuyabilsin (umask 022'de 0755/0644)
		if st, err := os.Stat(p); err != nil || st.Mode().Perm()&want != want {
			t.Errorf("%s izni %v (%v)", p, st.Mode().Perm(), err)
		}
	}
}

func TestStreamDownloadByteRangeIgnoredByServer(t *testing.T) {
	data := "0123456789abcdefghij"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:5@10\nall.ts\n#EXTINF:1,\n#EXT-X-BYTERANGE:3\nall.ts\n#EXT-X-ENDLIST\n")
			return
		}
		fmt.Fprint(w, data) // Range yok sayılır: 200 + tüm gövde
	}))
	defer srv.Close()
	path, j, err := runStream(t, srv.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "abcdefgh" {
		t.Fatalf("içerik %q", b)
	}
	if sn := j.Snapshot(); sn.Size != 8 {
		t.Fatalf("aralıklar biliniyor, boyut 8 olmalı: %d", sn.Size)
	}
}

func dashServer(t *testing.T, s *streamSrv, video, audio []byte, block chan struct{}) *httptest.Server {
	mpd := `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video"><SegmentTemplate timescale="1" duration="1" initialization="$RepresentationID$-init.mp4" media="$RepresentationID$-$Number$.m4s"/>
<Representation id="v" bandwidth="100" width="160" height="90"/></AdaptationSet>
<AdaptationSet contentType="audio"><SegmentTemplate timescale="1" duration="1" initialization="$RepresentationID$-init.mp4" media="$RepresentationID$-$Number$.m4s"/>
<Representation id="a" bandwidth="10"/></AdaptationSet></Period></MPD>`
	files := map[string][]byte{"/m.mpd": []byte(mpd)}
	for id, b := range map[string][]byte{"v": video, "a": audio} { // init (ftyp+moov) + iki parça (moof+mdat)
		bs := topBoxesRaw(b)
		files["/"+id+"-init.mp4"], files["/"+id+"-1.m4s"], files["/"+id+"-2.m4s"] = bytes.Join(bs[:2], nil), bytes.Join(bs[2:4], nil), bytes.Join(bs[4:6], nil)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.note(r)
		if block != nil && r.URL.Path == "/a-2.m4s" && s.count(r.URL.Path) == 1 {
			<-block
		}
		b, ok := files[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
}

func topBoxesRaw(b []byte) [][]byte {
	var out [][]byte
	for len(b) >= 8 {
		n := int(uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]))
		out, b = append(out, b[:n]), b[n:]
	}
	return out
}

func testTrack(timescale uint32, entry, tag string, flags uint32) []byte {
	b := testInit(1, timescale, entry)
	b = append(b, testFrag(1, 1, flags, 0, tag+"0")...)
	return append(b, testFrag(2, 1, flags, uint64(timescale), tag+"1")...)
}

func TestStreamDownloadDASHMerges(t *testing.T) {
	var s streamSrv
	srv := dashServer(t, &s, testTrack(1000, "avc1", "V", 0x020000), testTrack(48000, "mp4a", "A", 0x020000), nil)
	defer srv.Close()
	path, j, err := runStream(t, srv.URL+"/m.mpd", StreamSpec{Kind: "dash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	top := topBoxes(t, b)
	var mdats string
	for _, x := range top {
		if x.typ == "mdat" {
			mdats += string(x.body)
		}
	}
	if top[0].typ != "ftyp" || mdats != "V0A0V1A1" {
		t.Fatalf("birleşik dosya: %v %q", types(top), mdats)
	}
	if p := j.StreamProgress(); p.Segments != 6 || p.SegDone != 6 || p.Note != "" {
		t.Fatalf("ilerleme %+v", p)
	}
	noTemp(t, path)
}

func TestStreamMergeFailureKeepsBothFiles(t *testing.T) {
	var s streamSrv
	srv := dashServer(t, &s, testTrack(1000, "avc1", "V", 0x1), testTrack(48000, "mp4a", "A", 0x020000), nil)
	defer srv.Close()
	path, j, err := runStream(t, srv.URL+"/m.mpd", StreamSpec{Kind: "dash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSuffix(path, filepath.Ext(path))
	for _, p := range []string{base + ".video.mp4", base + ".audio.m4a"} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("ayrı dosya yok: %v", err)
		}
	}
	if p := j.StreamProgress(); !strings.HasPrefix(p.Note, "Ses ayrı dosyada kaldı") || j.Snapshot().Path != base+".video.mp4" {
		t.Fatalf("not/yol: %+v %s", p, j.Snapshot().Path)
	}
	noTemp(t, path)
}

// Tek izli akış parçalı MP4 değilse inen veri atılmaz: parçalar olduğu gibi son dosyaya konur, not düşülür
// (önceden .part siliniyor, devam her şeyi baştan indirip aynı hataya düşüyordu).
func TestStreamSingleTrackMergeFailureKeepsData(t *testing.T) {
	mpd := `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video"><SegmentTemplate timescale="1" duration="1" initialization="init.mp4" media="$Number$.m4s"/>
<Representation id="v" bandwidth="100" width="160" height="90"/></AdaptationSet></Period></MPD>`
	init := testInit(1, 1000, "avc1")
	files := map[string][]byte{"/m.mpd": []byte(mpd), "/init.mp4": init, "/1.m4s": []byte("ham1"), "/2.m4s": []byte("ham2")}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	path, j, err := runStream(t, srv.URL+"/m.mpd", StreamSpec{Kind: "dash"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(init)+"ham1ham2" {
		t.Fatalf("içerik %q", b)
	}
	if p := j.StreamProgress(); !strings.Contains(p.Note, "birleştirilemedi") || j.Snapshot().Path != path {
		t.Fatalf("not/yol: %+v %s", p, j.Snapshot().Path)
	}
	noTemp(t, path)
}

func TestStreamEncryptedInitStopsBeforeMedia(t *testing.T) {
	var s streamSrv
	srv := dashServer(t, &s, testTrack(1000, "encv", "V", 0x020000), testTrack(48000, "mp4a", "A", 0x020000), nil)
	defer srv.Close()
	path, _, err := runStream(t, srv.URL+"/m.mpd", StreamSpec{Kind: "dash"}, nil)
	if err == nil || err.Error() != errProtected {
		t.Fatalf("hata: %v", err)
	}
	if n := s.count("/v-1.m4s") + s.count("/a-1.m4s"); n != 0 {
		t.Fatalf("korumalı akışta medya parçası istendi: %d", n)
	}
	noTemp(t, path)
}

func TestStreamPauseResumeSkipsDoneSegments(t *testing.T) {
	var s streamSrv
	block := make(chan struct{})
	srv := dashServer(t, &s, testTrack(1000, "avc1", "V", 0x020000), testTrack(48000, "mp4a", "A", 0x020000), block)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "video.mp4")
	spec := StreamSpec{Kind: "dash"}
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for j.StreamProgress().SegDone < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	j.Pause()
	if err := j.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait: %v", err)
	}
	close(block)
	if _, err := os.Stat(path + ".stream.json"); err != nil {
		t.Fatal("durum dosyası yok")
	}
	before := s.count("/v-1.m4s")
	j, err = StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	if s.count("/v-1.m4s") != before || s.count("/v-init.mp4") != 1 {
		t.Fatalf("inen parça yeniden istendi: v-1 %d→%d, init %d", before, s.count("/v-1.m4s"), s.count("/v-init.mp4"))
	}
	if sn := j.Snapshot(); sn.Restarted {
		t.Fatal("aynı akış baştan başladı")
	}
	noTemp(t, path)
}

func TestStreamSegmentForbidden(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n")
			return
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_, _, err := runStream(t, srv.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, nil)
	if err == nil || err.Error() != errExpired {
		t.Fatalf("hata: %v", err)
	}
}

func TestStreamRetriesServerError(t *testing.T) {
	old := segRetryDelay
	segRetryDelay = time.Millisecond
	t.Cleanup(func() { segRetryDelay = old })
	// 429/408: CDN'in 8 eşzamanlı parça isteğini geçici olarak geri çevirmesi işi durdurmamalı
	for _, code := range []int{http.StatusBadGateway, http.StatusTooManyRequests, http.StatusRequestTimeout} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			var n atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/p.m3u8" {
					fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n")
					return
				}
				if n.Add(1) < 3 {
					w.WriteHeader(code)
					return
				}
				fmt.Fprint(w, "ok")
			}))
			defer srv.Close()
			path, _, err := runStream(t, srv.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, nil)
			if b, _ := os.ReadFile(path); err != nil || string(b) != "ok" {
				t.Fatalf("%v %q", err, b)
			}
		})
	}
}

func TestStreamHeadersCrossOrigin(t *testing.T) {
	var segs streamSrv
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		segs.note(r)
		fmt.Fprint(w, "x")
	}))
	defer cdn.Close()
	cdnURL := strings.Replace(cdn.URL, "127.0.0.1", "localhost", 1) // başka site (çerez ana makineye bağlı)
	var site streamSrv
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		site.note(r)
		switch r.URL.Path {
		case "/p.m3u8":
			fmt.Fprintf(w, "#EXTM3U\n#EXTINF:1,\nown.ts\n#EXTINF:1,\n%s/c.ts\n#EXT-X-ENDLIST\n", cdnURL)
		default:
			fmt.Fprint(w, "y")
		}
	}))
	defer page.Close()
	h := http.Header{"Cookie": {"sid=1"}, "Referer": {page.URL + "/watch?v=1"}}
	if _, _, err := runStream(t, page.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, h); err != nil {
		t.Fatal(err)
	}
	own := site.hdrs["/own.ts"]
	if own.Get("Cookie") != "sid=1" || own.Get("Referer") != page.URL+"/watch?v=1" || own.Get("Origin") != "" {
		t.Fatalf("aynı köken: %v", own)
	}
	c := segs.hdrs["/c.ts"]
	if c.Get("Cookie") != "" || c.Get("Referer") != page.URL+"/" || c.Get("Origin") != page.URL {
		t.Fatalf("çapraz köken: %v", c)
	}
}

func TestProbeStreamOverHTTP(t *testing.T) {
	var s streamSrv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.note(r)
		if r.URL.Path == "/m.m3u8" {
			w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
			fmt.Fprint(w, fmp4Media)
			return
		}
		w.Header().Set("Content-Type", "application/dash+xml")
		fmt.Fprint(w, "<MPD/>")
	}))
	defer srv.Close()
	info, err := ProbeStream(context.Background(), srv.URL+"/m.m3u8", http.Header{"Cookie": {"c=1"}}, "", nil)
	if err != nil || info.Kind != "hls" || info.Container != "mp4" {
		t.Fatalf("%+v %v", info, err)
	}
	if hd := s.hdrs["/m.m3u8"]; hd.Get("Cookie") != "c=1" || hd.Get("Range") != "" {
		t.Fatalf("başlıklar: %v", hd)
	}
	for path, want := range map[string]string{"/m.m3u8": "hls", "/x": "dash"} {
		p, err := Probe(context.Background(), srv.URL+path, http.Header{}, nil)
		if err != nil || p.Kind != want {
			t.Errorf("%s: Probe Kind %q %v", path, p.Kind, err)
		}
	}
}

func TestMediaKind(t *testing.T) {
	for _, c := range []struct{ ct, path, want string }{
		{"application/x-mpegURL", "/a", "hls"},
		{"audio/mpegurl; charset=utf-8", "/a", "hls"},
		{"application/dash+xml", "/a", "dash"},
		{"application/octet-stream", "/v/index.m3u8", "hls"},
		{"", "/v/manifest.mpd", "dash"},
		{"text/plain", "/v/x.M3U8", "hls"},
		{"text/html", "/v/index.m3u8", ""},
		{"video/mp4", "/v/a.mp4", ""},
	} {
		if got := mediaKind(c.ct, c.path); got != c.want {
			t.Errorf("%q %q: %q", c.ct, c.path, got)
		}
	}
}

func TestDefaultDepsStartsStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p.m3u8" {
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n")
			return
		}
		fmt.Fprint(w, "ts")
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "v.ts")
	j, err := DefaultDeps().Start(context.Background(), Request{URL: srv.URL + "/p.m3u8", Path: path, Stream: &StreamSpec{Kind: "hls"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := j.(*StreamJob); !ok {
		t.Fatalf("akış işi değil: %T", j)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "ts" {
		t.Fatalf("içerik %q", b)
	}
}

// V4 L3: kapanış birleştirmeyi video izi birleştikten sonra keserse devam o izi yeniden indirmez, birleşik iz dosyasını kullanır.
func TestStreamResumeAfterInterruptedMergeKeepsMergedTrack(t *testing.T) {
	var s streamSrv
	block := make(chan struct{})
	srv := dashServer(t, &s, testTrack(1000, "avc1", "V", 0x020000), testTrack(48000, "mp4a", "A", 0x020000), block)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "video.mp4")
	spec := StreamSpec{Kind: "dash"}
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for j.StreamProgress().SegDone < 5 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	j.Pause()
	if err := j.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait: %v", err)
	}
	close(block)
	// Kesilen birleştirmenin bıraktığı durum: video izi v.mp4'te, parçaları silinmiş.
	dir := path + ".octo-parts"
	var v []byte
	for _, n := range []string{"vinit.seg", "v000000.seg", "v000001.seg"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		v = append(v, b...)
		os.Remove(filepath.Join(dir, n))
	}
	if err := os.WriteFile(filepath.Join(dir, "v.mp4"), v, 0o600); err != nil {
		t.Fatal(err)
	}
	before := s.count("/v-1.m4s") + s.count("/v-2.m4s") + s.count("/v-init.mp4")
	j, err = StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	if after := s.count("/v-1.m4s") + s.count("/v-2.m4s") + s.count("/v-init.mp4"); after != before {
		t.Fatalf("birleşmiş video izi yeniden indirildi: %d → %d istek", before, after)
	}
	var mdats string
	b, _ := os.ReadFile(path)
	for _, x := range topBoxes(t, b) {
		if x.typ == "mdat" {
			mdats += string(x.body)
		}
	}
	if mdats != "V0A0V1A1" {
		t.Fatalf("birleşik dosya %q", mdats)
	}
	if p := j.StreamProgress(); p.SegDone != p.Segments {
		t.Fatalf("ilerleme %+v", p)
	}
	noTemp(t, path)
}

// V5 L1: yarım kalmış birleşik iz (ör. güç kesintisi, fsync'siz) tam sayılmaz: kutu yapısı dosya boyuna uymuyorsa
// iz birleşmemiş sayılır, parçaları yeniden iner, çıktı tamdır.
func TestStreamResumeRejectsTruncatedMergedTrack(t *testing.T) {
	var s streamSrv
	block := make(chan struct{})
	srv := dashServer(t, &s, testTrack(1000, "avc1", "V", 0x020000), testTrack(48000, "mp4a", "A", 0x020000), block)
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "video.mp4")
	spec := StreamSpec{Kind: "dash"}
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); j.StreamProgress().SegDone < 5 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	j.Pause()
	if err := j.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait: %v", err)
	}
	close(block)
	dir := path + ".octo-parts"
	var v []byte
	for _, n := range []string{"vinit.seg", "v000000.seg", "v000001.seg"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		v = append(v, b...)
		os.Remove(filepath.Join(dir, n))
	}
	if err := os.WriteFile(filepath.Join(dir, "v.mp4"), v[:len(v)-5], 0o600); err != nil { // son kutu kesik
		t.Fatal(err)
	}
	j, err = StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	var mdats string
	b, _ := os.ReadFile(path)
	for _, x := range topBoxes(t, b) {
		if x.typ == "mdat" {
			mdats += string(x.body)
		}
	}
	if mdats != "V0A0V1A1" || j.StreamProgress().Note != "" {
		t.Fatalf("birleşik dosya %q, not %q", mdats, j.StreamProgress().Note)
	}
	noTemp(t, path)
}

// V5 L2: iki iz aynı dosyada bayt aralıklarıyla durur ve sunucu Range'i yok sayar (200). Birleşmiş izin parçaları
// bölmede yeniden yazılmaz; ilerleme toplamı aşmaz.
func TestStreamSplitSkipsMergedTrackSiblings(t *testing.T) {
	vb := topBoxesRaw(testTrack(1000, "avc1", "V", 0x020000))
	ab := topBoxesRaw(testTrack(48000, "mp4a", "A", 0x020000))
	// dosya düzeni: ses önce, görüntü sonra (sesin son parçası istenince görüntü parçaları da gövdede gelir)
	parts := [][]byte{bytes.Join(ab[:2], nil), bytes.Join(ab[2:4], nil), bytes.Join(ab[4:6], nil),
		bytes.Join(vb[:2], nil), bytes.Join(vb[2:4], nil), bytes.Join(vb[4:6], nil)}
	var all []byte
	rng := make([]string, len(parts))
	for i, p := range parts {
		rng[i] = fmt.Sprintf("%d@%d", len(p), len(all))
		all = append(all, p...)
	}
	media := func(o int) string {
		return "#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXT-X-MAP:URI=\"all.mp4\",BYTERANGE=\"" + rng[o] + "\"\n" +
			"#EXTINF:1,\n#EXT-X-BYTERANGE:" + rng[o+1] + "\nall.mp4\n#EXTINF:1,\n#EXT-X-BYTERANGE:" + rng[o+2] + "\nall.mp4\n#EXT-X-ENDLIST\n"
	}
	var s streamSrv
	var whole atomic.Bool // ikinci koşuda sunucu Range'i yok sayar
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.note(r)
		switch r.URL.Path {
		case "/m.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID=\"a\",NAME=\"en\",DEFAULT=YES,URI=\"a.m3u8\"\n#EXT-X-STREAM-INF:BANDWIDTH=100,AUDIO=\"a\"\nv.m3u8\n")
		case "/v.m3u8":
			fmt.Fprint(w, media(3))
		case "/a.m3u8":
			fmt.Fprint(w, media(0))
		case "/all.mp4":
			a, b, ok := parseRange(r.Header.Get("Range"))
			if whole.Load() || !ok {
				w.Write(all)
				return
			}
			if a == int64(len(parts[0])+len(parts[1])) {
				<-block // sesin son parçası ilk koşuda inmez
			}
			w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", a, b, len(all)))
			w.WriteHeader(http.StatusPartialContent)
			w.Write(all[a : b+1])
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	path := filepath.Join(t.TempDir(), "video.mp4")
	spec := StreamSpec{Kind: "hls"}
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/m.m3u8", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); j.StreamProgress().SegDone < 5 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	j.Pause()
	if err := j.Wait(); !errors.Is(err, ErrPaused) {
		t.Fatalf("Wait: %v (ilerleme %+v)", err, j.StreamProgress())
	}
	close(block)
	dir := path + ".octo-parts"
	var v []byte
	for _, n := range []string{"vinit.seg", "v000000.seg", "v000001.seg"} {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		v = append(v, b...)
		os.Remove(filepath.Join(dir, n))
	}
	if err := os.WriteFile(filepath.Join(dir, "v.mp4"), v, 0o600); err != nil {
		t.Fatal(err)
	}
	whole.Store(true)
	j, err = StartStream(context.Background(), Request{URL: srv.URL + "/m.m3u8", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	if p := j.StreamProgress(); p.SegDone != p.Segments {
		t.Fatalf("ilerleme toplamı aştı/eksik: %+v", p)
	}
	noTemp(t, path)
}

// V5 L3: tek iz geri dönüşünde son dosya yerine konamazsa birleşik veri silinmez; devam yeniden indirmeden bitirir.
func TestStreamSingleTrackPlaceFailureKeepsMerged(t *testing.T) {
	mdat := func(p string) []byte {
		return append([]byte{0, 0, 0, byte(8 + len(p)), 'm', 'd', 'a', 't'}, p...)
	}
	mpd := `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video"><SegmentTemplate timescale="1" duration="1" initialization="init.mp4" media="$Number$.m4s"/>
<Representation id="v" bandwidth="100" width="160" height="90"/></AdaptationSet></Period></MPD>`
	init := testInit(1, 1000, "avc1")
	files := map[string][]byte{"/m.mpd": []byte(mpd), "/init.mp4": init, "/1.m4s": mdat("ham1"), "/2.m4s": mdat("ham2")} // parçasız MP4: birleşmez
	var s streamSrv
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.note(r)
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	old := placeFile
	placeFile = func(string, bool, func(string) error) (string, error) { return "", errors.New("hedef yazılamadı") }
	t.Cleanup(func() { placeFile = old })
	path, _, err := runStream(t, srv.URL+"/m.mpd", StreamSpec{Kind: "dash"}, nil)
	if err == nil {
		t.Fatal("yerine koyma hatası dönmedi")
	}
	placeFile = old
	spec := StreamSpec{Kind: "dash"}
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/m.mpd", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(init)+string(mdat("ham1"))+string(mdat("ham2")) {
		t.Fatalf("içerik %q", b)
	}
	if n := s.count("/1.m4s") + s.count("/2.m4s"); n != 2 {
		t.Fatalf("birleşik veri kayboldu, parçalar yeniden indi: %d istek", n)
	}
	noTemp(t, path)
}
