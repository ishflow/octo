package engine

import (
	"net/url"
	"strings"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

const hlsMaster = `#EXTM3U
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="English",LANGUAGE="en",DEFAULT=YES,URI="audio/en.m3u8"
#EXT-X-MEDIA:TYPE=AUDIO,GROUP-ID="aud",NAME="Türkçe, dublaj",LANGUAGE="tr",URI="audio/tr.m3u8"
#EXT-X-MEDIA:TYPE=SUBTITLES,GROUP-ID="sub",NAME="en",URI="sub.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=800000,RESOLUTION=640x360,CODECS="avc1.4d401e,mp4a.40.2",AUDIO="aud"
low/index.m3u8
#EXT-X-I-FRAME-STREAM-INF:BANDWIDTH=100000,URI="iframe.m3u8"
#EXT-X-STREAM-INF:BANDWIDTH=4500000,RESOLUTION=1920x1080,CODECS="avc1.640028,mp4a.40.2",AUDIO="aud"
https://cdn.example.com/hi/index.m3u8
`

// masterIDs listedeki sırayla varyant ve ses kimlikleri (kimlik özelliklerden türer).
func masterIDs(t *testing.T, body string) (vs, as []string) {
	t.Helper()
	d, err := ParseHLS([]byte(body), mustURL(t, "https://x.example/m.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range d.Variants {
		vs = append(vs, v.ID)
	}
	for _, a := range d.Audios {
		as = append(as, a.ID)
	}
	return vs, as
}

func TestParseHLSMaster(t *testing.T) {
	d, err := ParseHLS([]byte(hlsMaster), mustURL(t, "https://site.example/v/master.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if !d.Master || len(d.Variants) != 2 || len(d.Audios) != 2 {
		t.Fatalf("master=%v variants=%d audios=%d", d.Master, len(d.Variants), len(d.Audios))
	}
	v := d.Variants[1]
	if !strings.HasPrefix(v.ID, "v") || v.ID == d.Variants[0].ID || v.Height != 1080 || v.Width != 1920 || v.Bandwidth != 4500000 || v.Codecs != "avc1.640028,mp4a.40.2" ||
		v.URI != "https://cdn.example.com/hi/index.m3u8" || v.Group != "aud" || v.Label != "1080p" {
		t.Fatalf("variant: %+v", v)
	}
	if d.Variants[0].URI != "https://site.example/v/low/index.m3u8" {
		t.Fatalf("relative uri: %s", d.Variants[0].URI)
	}
	a := d.Audios[1]
	if !strings.HasPrefix(a.ID, "a") || a.ID == d.Audios[0].ID || a.Name != "Türkçe, dublaj" || a.Lang != "tr" || a.Default || a.URI != "https://site.example/v/audio/tr.m3u8" {
		t.Fatalf("audio: %+v", a)
	}
	if !d.Audios[0].Default {
		t.Fatal("DEFAULT=YES okunmadı")
	}
}

func TestParseHLSMediaByteRangeAndMap(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:2
#EXT-X-MEDIA-SEQUENCE:5
#EXT-X-MAP:URI="init.mp4",BYTERANGE="700@0"
#EXTINF:2.0,
#EXT-X-BYTERANGE:1000@700
media.mp4
#EXTINF:1.5,
#EXT-X-BYTERANGE:500
media.mp4
#EXT-X-ENDLIST
`
	d, err := ParseHLS([]byte(body), mustURL(t, "https://x.example/a/p.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Master || d.Live || d.Protected || d.Container != "mp4" {
		t.Fatalf("doc: %+v", d)
	}
	if d.Init == nil || d.Init.URL != "https://x.example/a/init.mp4" || d.Init.Range != "bytes=0-699" {
		t.Fatalf("init: %+v", d.Init)
	}
	want := []segment{{"https://x.example/a/media.mp4", "bytes=700-1699"}, {"https://x.example/a/media.mp4", "bytes=1700-2199"}}
	if len(d.Segs) != 2 || d.Segs[0] != want[0] || d.Segs[1] != want[1] {
		t.Fatalf("segs: %+v", d.Segs)
	}
	if d.Duration != 3.5 {
		t.Fatalf("duration %v", d.Duration)
	}
}

func TestParseHLSTSAndLive(t *testing.T) {
	vod := "#EXTM3U\n#EXTINF:1,\ns0.ts\n#EXT-X-DISCONTINUITY\n#EXTINF:1,\ns1.ts\n#EXT-X-ENDLIST\n"
	d, err := ParseHLS([]byte(vod), mustURL(t, "http://x.example/p.m3u8"))
	if err != nil || d.Container != "ts" || len(d.Segs) != 2 || d.Segs[1].Range != "" || d.Live {
		t.Fatalf("ts: %+v %v", d, err)
	}
	for _, live := range []string{
		"#EXTM3U\n#EXTINF:1,\ns0.ts\n",
		"#EXTM3U\n#EXT-X-PLAYLIST-TYPE:EVENT\n#EXTINF:1,\ns0.ts\n",
	} {
		d, err := ParseHLS([]byte(live), mustURL(t, "http://x.example/p.m3u8"))
		if err != nil || !d.Live {
			t.Fatalf("canlı sayılmadı: %q %+v %v", live, d, err)
		}
	}
}

func TestParseHLSProtected(t *testing.T) {
	for _, body := range []string{
		"#EXTM3U\n#EXT-X-KEY:METHOD=AES-128,URI=\"k.key\"\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-KEY:METHOD=SAMPLE-AES,URI=\"skd://x\",KEYFORMAT=\"com.apple.streamingkeydelivery\"\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n",
		"#EXTM3U\n#EXT-X-SESSION-KEY:METHOD=SAMPLE-AES,URI=\"skd://x\"\n#EXT-X-STREAM-INF:BANDWIDTH=1\nv.m3u8\n",
	} {
		d, err := ParseHLS([]byte(body), mustURL(t, "https://x.example/p.m3u8"))
		if err != nil || !d.Protected {
			t.Fatalf("korumalı sayılmadı: %q %v", body, err)
		}
	}
	d, err := ParseHLS([]byte("#EXTM3U\n#EXT-X-KEY:METHOD=NONE\n#EXTINF:1,\ns0.ts\n#EXT-X-ENDLIST\n"), mustURL(t, "https://x.example/p.m3u8"))
	if err != nil || d.Protected {
		t.Fatalf("METHOD=NONE korumalı sayıldı: %v", err)
	}
}

func TestParseHLSRejects(t *testing.T) {
	base := mustURL(t, "https://x.example/p.m3u8")
	cases := map[string]string{
		"değil":           "<html>not a playlist</html>",
		"şema":            "#EXTM3U\n#EXTINF:1,\nfile:///etc/passwd\n#EXT-X-ENDLIST\n",
		"loopback":        "#EXTM3U\n#EXTINF:1,\nhttp://127.0.0.1:8080/s.ts\n#EXT-X-ENDLIST\n",
		"localhost":       "#EXTM3U\n#EXTINF:1,\nhttp://localhost/s.ts\n#EXT-X-ENDLIST\n",
		"özel ağ":         "#EXTM3U\n#EXTINF:1,\nhttp://192.168.1.1/s.ts\n#EXT-X-ENDLIST\n",
		"ikinci MAP":      "#EXTM3U\n#EXT-X-MAP:URI=\"a.mp4\"\n#EXTINF:1,\ns0.m4s\n#EXT-X-DISCONTINUITY\n#EXT-X-MAP:URI=\"b.mp4\"\n#EXTINF:1,\ns1.m4s\n#EXT-X-ENDLIST\n",
		"bozuk aralık":    "#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:abc\ns.ts\n#EXT-X-ENDLIST\n",
		"aralık ofsetsiz": "#EXTM3U\n#EXTINF:1,\n#EXT-X-BYTERANGE:100\ns.ts\n#EXT-X-ENDLIST\n",
	}
	for name, body := range cases {
		if _, err := ParseHLS([]byte(body), base); err == nil {
			t.Errorf("%s: hata bekleniyordu", name)
		}
	}
	// Yerel sayfadaki yerel parça serbest (yerel test sunucusu, ev ağı).
	if _, err := ParseHLS([]byte(cases["loopback"]), mustURL(t, "http://127.0.0.1:9/p.m3u8")); err != nil {
		t.Fatalf("yerel manifest yerel parça: %v", err)
	}
	many := "#EXTM3U\n" + strings.Repeat("#EXTINF:1,\ns.ts\n", maxSegments+1) + "#EXT-X-ENDLIST\n"
	if _, err := ParseHLS([]byte(many), base); err == nil {
		t.Fatal("parça sınırı aşıldı ama kabul edildi")
	}
}
