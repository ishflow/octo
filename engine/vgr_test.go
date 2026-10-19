package engine

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// VGR incelemesinin bulguları (H1, M1, M2, M4, M5, M6, L6) için gerileme testleri.

func manyReps(n int, dur, media string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<MPD mediaPresentationDuration="%s"><Period><AdaptationSet contentType="video"><SegmentTemplate duration="1" timescale="1" media="%s"/>`, dur, media)
	for i := range n {
		fmt.Fprintf(&b, `<Representation id="r%d" bandwidth="%d"/>`, i, i+1)
	}
	b.WriteString(`</AdaptationSet></Period></MPD>`)
	return b.String()
}

// H1: parça planı yalnız seçilen temsil için açılır (100 temsil × 50 000 parça eskiden ~5,6 GB'tı).
func TestMPDPlanOnlyChosenRep(t *testing.T) {
	base := mustURL(t, "https://cdn.example/a/x.mpd")
	body := []byte(manyReps(maxReps, "PT49999S", "s$Number$.m4s"))
	d, err := ParseMPD(body, base)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range d.Video {
		if r.Segs != nil {
			t.Fatal("ParseMPD parça listesini açtı")
		}
	}
	start := time.Now()
	if _, err := probeStream("https://cdn.example/a/x.mpd", func(string) ([]byte, *url.URL, error) { return body, base, nil }); err != nil {
		t.Fatal(err)
	}
	p, err := planDASH(body, base, StreamSpec{Kind: "dash", Video: "r3"})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Tracks) != 1 || len(p.Tracks[0].Segs) != 49999 || p.Tracks[0].Segs[0].URL != "https://cdn.example/a/s1.m4s" {
		t.Fatalf("plan: %d iz", len(p.Tracks))
	}
	if d := time.Since(start); d > 5*time.Second { // 64 temsilin hepsi açılsa ~3,5 GB, saniyeler
		t.Fatalf("probe + plan %v sürdü", d)
	}
}

func TestMPDLimits(t *testing.T) {
	base := mustURL(t, "https://cdn.example/a/x.mpd")
	if _, err := ParseMPD([]byte(manyReps(maxReps+1, "PT2S", "s$Number$.m4s")), base); err == nil {
		t.Error("temsil sınırı yok")
	}
	if _, err := planDASH([]byte(manyReps(1, "PT2S", "s$Number%016d$.m4s")), base, StreamSpec{Kind: "dash"}); err != nil {
		t.Errorf("%%016d reddedildi: %v", err)
	}
	if _, err := planDASH([]byte(manyReps(1, "PT2S", "s$Number%017d$.m4s")), base, StreamSpec{Kind: "dash"}); err == nil {
		t.Error("%017d kabul edildi (genişlik sınırı yok)")
	}
	long := strings.Repeat("x", 4096) + "$Number$.m4s" // 20 000 × 4 KB = 80 MB adres
	if _, err := planDASH([]byte(manyReps(1, "PT20000S", long)), base, StreamSpec{Kind: "dash"}); err == nil {
		t.Error("toplam adres boyu sınırı yok")
	}
	dup := `<MPD mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video"><BaseURL>v.mp4</BaseURL><Representation id="1" height="720"/><Representation id="1" height="1080"/></AdaptationSet></Period></MPD>`
	if d, err := ParseMPD([]byte(dup), base); err != nil || len(d.Video) != 1 {
		t.Errorf("yinelenen Representation@id: %v", err)
	}
}

// M1: yerel ağ denetimi bağlantı anında. Testte 127.0.0.1 "dış ağ", ::1 "yerel ağ" sayılır.
func TestLocalIP(t *testing.T) {
	for s, want := range map[string]bool{
		"127.0.0.1": true, "::1": true, "::ffff:127.0.0.1": true, "10.1.2.3": true, "192.168.1.1": true, "169.254.1.1": true,
		"100.64.0.1": true, "100.100.100.100": true, "100.127.255.255": true, "100.128.0.0": false, "0.0.0.0": true, "0.1.2.3": true,
		"64:ff9b::7f00:1": true, "64:ff9b::808:808": false, "fd7a:115c:a1e0::1": true, "fe80::1": true, "224.0.0.1": true,
		"8.8.8.8": false, "2606:4700::1111": false,
	} {
		if got := localIP(netip.MustParseAddr(s)); got != want {
			t.Errorf("localIP(%s)=%v", s, got)
		}
	}
	if !localHost("100.100.100.100") || !localHost("64:ff9b::a00:1") {
		t.Error("localHost CGNAT/NAT64 görmüyor")
	}
}

func ipv6Local(t *testing.T, h http.Handler) *httptest.Server {
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback yok")
	}
	s := httptest.NewUnstartedServer(h)
	s.Listener.Close()
	s.Listener = ln
	s.Start()
	old := localIP
	localIP = func(a netip.Addr) bool { return a == netip.IPv6Loopback() }
	t.Cleanup(func() { localIP = old; s.Close() })
	return s
}

func TestStreamRedirectToLocalBlocked(t *testing.T) {
	var hit atomic.Int32
	local := ipv6Local(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns.ts\n#EXT-X-ENDLIST\n")
	}))
	pub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/p.m3u8":
			fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns.ts\n#EXT-X-ENDLIST\n")
		default: // parça da manifest de yerel ağa yönlenir
			http.Redirect(w, r, local.URL+"/admin", http.StatusFound)
		}
	}))
	defer pub.Close()
	start := time.Now()
	if _, _, err := runStream(t, pub.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, nil); err == nil || err.Error() != errPrivateSeg.Error() {
		t.Fatalf("parça yönlendirmesi: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("yerel ağ reddi yeniden denendi (kalıcı olmalı)")
	}
	if _, err := ProbeStream(context.Background(), pub.URL+"/r.m3u8", nil, "", nil); err == nil || err.Error() != errPrivateSeg.Error() {
		t.Fatalf("manifest yönlendirmesi: %v", err)
	}
	if hit.Load() != 0 {
		t.Fatalf("yerel ağa %d istek gitti", hit.Load())
	}
	// Manifest yerel ağdaysa kural serbest: aynı yerel sunucudan parça iner.
	if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", nil, "", nil); err != nil {
		t.Fatalf("yerel manifest: %v", err)
	}
}

// M2: init ayrıştırılamazsa korumalı say (fail-closed); 64 bit kutu boyu okunur.
func TestEncryptedInitFailClosed(t *testing.T) {
	large := func(b []byte) []byte { // ftyp (16 B) + moov → moov'u 64 bit boylu yaz
		moov := b[16:]
		h := binary.BigEndian.AppendUint32(append([]byte{}, b[:16]...), 1)
		h = binary.BigEndian.AppendUint64(append(h, "moov"...), uint64(len(moov)+8))
		return append(h, moov[8:]...)
	}
	if !encryptedInit(append(testInit(1, 1000, "encv"), 0, 0, 0, 0)) {
		t.Error("sonda 4 bayt artıklı init şifresiz sayıldı")
	}
	if !encryptedInit(large(testInit(1, 1000, "encv"))) {
		t.Error("64 bit moov'daki encv görülmedi")
	}
	if encryptedInit(large(testInit(1, 1000, "avc1"))) {
		t.Error("64 bit moov'lu şifresiz init korumalı sayıldı")
	}
}

// M4: Range'i yok sayan sunucuda kaynak bölünerek okunur (eskiden 40 parçada ~20 kat). Eşzamanlı ilk
// isteklerin kapatılan gövdeleri soket arabelleği kadar ek trafik yapar: sabit, parça sayısıyla büyümez.
func TestStreamByteRangeIgnoredReadsOnce(t *testing.T) {
	const n, seg = 40, 256 << 10
	var served atomic.Int64
	data := make([]byte, n*seg)
	for i := range data {
		data[i] = byte(i / 7)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/p.m3u8" {
			var b strings.Builder
			b.WriteString("#EXTM3U\n#EXT-X-TARGETDURATION:1\n")
			for i := range n {
				fmt.Fprintf(&b, "#EXTINF:1,\n#EXT-X-BYTERANGE:%d@%d\nall.ts\n", seg, i*seg)
			}
			b.WriteString("#EXT-X-ENDLIST\n")
			fmt.Fprint(w, b.String())
			return
		}
		k, _ := w.Write(data) // Range yok sayılır
		served.Add(int64(k))
	}))
	defer srv.Close()
	path, _, err := runStream(t, srv.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != string(data) {
		t.Fatalf("içerik bozuk (%d bayt)", len(b))
	}
	t.Logf("sunulan %.1f kat", float64(served.Load())/float64(len(data)))
	if got := served.Load(); got > 5*int64(len(data)) {
		t.Fatalf("sunulan %d KB, dosya %d KB (%.1f kat)", got>>10, len(data)>>10, float64(got)/float64(len(data)))
	}
}

// M5: varyant kimliği listedeki sıradan değil özelliklerden gelir.
func TestHLSIDsStableAcrossReorder(t *testing.T) {
	base := mustURL(t, "https://site.example/v/master.m3u8")
	lines := strings.Split(strings.TrimSpace(hlsMaster), "\n")
	// sıra değişir: ses satırları ve varyant blokları ters
	reordered := strings.Join([]string{lines[0], lines[2], lines[1], lines[3], lines[7], lines[8], lines[6], lines[4], lines[5]}, "\n")
	a, err := ParseHLS([]byte(hlsMaster), base)
	if err != nil {
		t.Fatal(err)
	}
	b, err := ParseHLS([]byte(reordered), base)
	if err != nil {
		t.Fatal(err)
	}
	id := func(d *hlsDoc, h int) string {
		for _, v := range d.Variants {
			if v.Height == h {
				return v.ID
			}
		}
		return ""
	}
	aud := func(d *hlsDoc, lang string) string {
		for _, x := range d.Audios {
			if x.Lang == lang {
				return x.ID
			}
		}
		return ""
	}
	if id(a, 1080) == "" || id(a, 1080) != id(b, 1080) || id(a, 360) != id(b, 360) || id(a, 1080) == id(a, 360) {
		t.Fatalf("varyant kimlikleri: %q/%q %q/%q", id(a, 1080), id(b, 1080), id(a, 360), id(b, 360))
	}
	if aud(a, "tr") == "" || aud(a, "tr") != aud(b, "tr") || aud(a, "tr") == aud(a, "en") {
		t.Fatalf("ses kimlikleri: %q/%q", aud(a, "tr"), aud(b, "tr"))
	}
}

// M5: devamda parça sayısı aynı ama parçalar başka akışınsa eskiler kullanılmaz.
func TestStreamResumeDifferentStreamRestarts(t *testing.T) {
	var gen atomic.Int32
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g := gen.Load()
		if r.URL.Path == "/p.m3u8" {
			fmt.Fprintf(w, "#EXTM3U\n#EXTINF:1,\ng%d/s0.ts?sig=%d\n#EXTINF:1,\ng%d/s1.ts\n#EXT-X-ENDLIST\n", g, g, g)
			return
		}
		if r.URL.Path == "/g0/s1.ts" {
			select {
			case <-block:
			case <-r.Context().Done():
				return
			}
		}
		fmt.Fprint(w, strings.TrimPrefix(r.URL.Path, "/"))
	}))
	defer srv.Close()
	path := t.TempDir() + "/v.ts"
	spec := StreamSpec{Kind: "hls"}
	j, err := StartStream(context.Background(), Request{URL: srv.URL + "/p.m3u8", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(5 * time.Second); j.StreamProgress().SegDone < 1 && time.Now().Before(deadline); {
		time.Sleep(10 * time.Millisecond)
	}
	j.Pause()
	j.Wait()
	gen.Store(1)
	close(block)
	j, err = StartStream(context.Background(), Request{URL: srv.URL + "/p.m3u8", Path: path, Stream: &spec}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := j.Wait(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(path); string(b) != "g1/s0.tsg1/s1.ts" || !j.Snapshot().Restarted {
		t.Fatalf("içerik %q, restarted=%v", b, j.Snapshot().Restarted)
	}
}

// M6: manifest gövdesi damla damla gelirse probe süre sınırında döner.
func TestProbeStreamSlowDripTimesOut(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		fmt.Fprint(w, "#EXTM3U\n")
		for {
			w.(http.Flusher).Flush()
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
			fmt.Fprint(w, "#\n")
		}
	}))
	defer srv.Close()
	old := manifestTimeout
	manifestTimeout = 300 * time.Millisecond
	defer func() { manifestTimeout = old }()
	done := make(chan error, 1)
	go func() { _, err := ProbeStream(context.Background(), srv.URL+"/x.m3u8", nil, "", nil); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("damlatan manifest kabul edildi")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ProbeStream süre sınırında dönmedi")
	}
}

// L6: BYTERANGE taşması.
func TestHLSRangeOverflow(t *testing.T) {
	if _, _, err := hlsRange("9223372036854775807@10", -1); err == nil {
		t.Fatal("taşan BYTERANGE kabul edildi")
	}
}

// V3s M1: dış sayfanın (Referer) seçtiği yerel manifest'e hiç istek gitmez, çerez de gitmez. Sayfa yerelse
// ya da Referer yoksa (eski davranış) ilk bağlantı karar verir.
func TestStreamExternalPageLocalManifestBlocked(t *testing.T) {
	var hit atomic.Int32
	local := ipv6Local(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns.ts\n#EXT-X-ENDLIST\n")
	}))
	ext := http.Header{"Referer": {"http://127.0.0.1:9/"}, "Cookie": {"router_session=1"}} // ipv6Local'da 127.0.0.1 dış ağ
	if _, err := ProbeStream(context.Background(), local.URL+"/cgi-bin/reboot.m3u8", ext, "", nil); err == nil || err.Error() != errPrivateSeg.Error() {
		t.Fatalf("probe: %v", err)
	}
	if _, _, err := runStream(t, local.URL+"/p.m3u8", StreamSpec{Kind: "hls"}, ext); err == nil || err.Error() != errPrivateSeg.Error() {
		t.Fatalf("indirme: %v", err)
	}
	if hit.Load() != 0 {
		t.Fatalf("yerel ağa %d istek gitti", hit.Load())
	}
	own := http.Header{"Referer": {local.URL + "/izle"}}
	if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", own, "", nil); err != nil {
		t.Fatalf("yerel sayfa: %v", err)
	}
}

// V3s M1 artığı: https sayfa → http yerel hedefte Referer kırpılır; motor sayfa adresine bakar (pageUrl).
func TestStreamPageURLWithoutReferer(t *testing.T) {
	var hit atomic.Int32
	local := ipv6Local(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns.ts\n#EXT-X-ENDLIST\n")
	}))
	ext := "https://127.0.0.1:9/izle" // ipv6Local'da 127.0.0.1 dış ağ
	if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", nil, ext, nil); err == nil || err.Error() != errPrivateSeg.Error() {
		t.Fatalf("probe: %v", err)
	}
	spec := StreamSpec{Kind: "hls"}
	j, err := StartStream(context.Background(), Request{URL: local.URL + "/p.m3u8", Path: filepath.Join(t.TempDir(), "v.ts"), Page: ext, Stream: &spec}, nil)
	if err == nil {
		err = j.Wait()
	}
	if err == nil || err.Error() != errPrivateSeg.Error() {
		t.Fatalf("indirme: %v", err)
	}
	if hit.Load() != 0 {
		t.Fatalf("yerel ağa %d istek gitti", hit.Load())
	}
	if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", nil, local.URL+"/izle", nil); err != nil {
		t.Fatalf("yerel sayfa: %v", err)
	}
	own := http.Header{"Referer": {local.URL + "/"}} // Referer varsa o karar verir
	if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", own, ext, nil); err != nil {
		t.Fatalf("Referer önce: %v", err)
	}
}

// V4 L1: sayfanın adı DNS'te dış bir adresin yanında yerel bir adres de dönerse (saldırganın DNS'i) sayfa dışarıda
// sayılır; yalnız bütün adresleri yerel olan ad yerel sayfadır.
func TestStreamPageResolvingAlsoLocalIsExternal(t *testing.T) {
	var hit atomic.Int32
	local := ipv6Local(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit.Add(1)
		fmt.Fprint(w, "#EXTM3U\n#EXTINF:1,\ns.ts\n#EXT-X-ENDLIST\n")
	}))
	zone := map[string][]netip.Addr{ // ipv6Local'da yalnız ::1 yerel, 127.0.0.1 dış
		"evil.test": {netip.MustParseAddr("127.0.0.1"), netip.IPv6Loopback()},
		"nas.test":  {netip.IPv6Loopback()},
	}
	old := pageLookup
	pageLookup = func(_ context.Context, host string) ([]netip.Addr, error) {
		if a, ok := zone[host]; ok {
			return a, nil
		}
		return nil, &net.DNSError{Err: "yok", Name: host, IsNotFound: true}
	}
	t.Cleanup(func() { pageLookup = old })
	for _, c := range []struct{ ref, page string }{{"https://evil.test/", ""}, {"", "https://evil.test/izle"}} {
		h := http.Header{}
		if c.ref != "" {
			h.Set("Referer", c.ref)
		}
		if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", h, c.page, nil); err == nil || err.Error() != errPrivateSeg.Error() {
			t.Fatalf("%+v: %v", c, err)
		}
	}
	if hit.Load() != 0 {
		t.Fatalf("yerel ağa %d istek gitti", hit.Load())
	}
	if _, err := ProbeStream(context.Background(), local.URL+"/p.m3u8", http.Header{"Referer": {"http://nas.test/"}}, "", nil); err != nil {
		t.Fatalf("bütünüyle yerel ad: %v", err)
	}
}
