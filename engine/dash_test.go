package engine

import (
	"net/url"
	"strings"
	"testing"
)

const mpdTimeline = `<?xml version="1.0" encoding="utf-8"?>
<MPD xmlns="urn:mpeg:dash:schema:mpd:2011" type="static" mediaPresentationDuration="PT4.0S" profiles="urn:mpeg:dash:profile:isoff-live:2011">
  <Period id="0" start="PT0.0S">
    <AdaptationSet id="0" contentType="video" segmentAlignment="true">
      <SegmentTemplate timescale="1000" initialization="init-$RepresentationID$.m4s" media="chunk-$RepresentationID$-$Number%05d$.m4s" startNumber="1">
        <SegmentTimeline>
          <S t="0" d="1000" r="2" />
          <S d="1000" />
        </SegmentTimeline>
      </SegmentTemplate>
      <Representation id="0" mimeType="video/mp4" codecs="avc1.64000c" bandwidth="200000" width="160" height="90" />
      <Representation id="2" mimeType="video/mp4" codecs="avc1.640028" bandwidth="4000000" width="1920" height="1080" />
    </AdaptationSet>
    <AdaptationSet id="1" contentType="audio" lang="tr">
      <SegmentTemplate timescale="48000" initialization="a/init-$RepresentationID$.m4s" media="a/$Time$-$Bandwidth$$$.m4s">
        <SegmentTimeline><S t="0" d="48000" r="-1" /></SegmentTimeline>
      </SegmentTemplate>
      <Representation id="1" mimeType="audio/mp4" codecs="mp4a.40.2" bandwidth="128000" />
    </AdaptationSet>
  </Period>
</MPD>`

// parseLoaded ParseMPD + bütün temsillerin parça listesi (ayrıştırıcı testleri için; motor yalnız seçileni açar).
func parseLoaded(body string, base *url.URL) (*mpdDoc, error) {
	d, err := ParseMPD([]byte(body), base)
	for i := 0; err == nil && i < len(d.Video)+len(d.Audio); i++ {
		if i < len(d.Video) {
			err = d.Video[i].load()
		} else {
			err = d.Audio[i-len(d.Video)].load()
		}
	}
	return d, err
}

func TestParseMPDTimeline(t *testing.T) {
	d, err := parseLoaded(mpdTimeline, mustURL(t, "https://cdn.example/v/manifest.mpd?tok=1"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Live || d.Protected || d.Duration != 4 || len(d.Video) != 2 || len(d.Audio) != 1 {
		t.Fatalf("doc: live=%v prot=%v dur=%v v=%d a=%d", d.Live, d.Protected, d.Duration, len(d.Video), len(d.Audio))
	}
	v := d.Video[1]
	if v.ID != "2" || v.Height != 1080 || v.Bandwidth != 4000000 || v.Codecs != "avc1.640028" {
		t.Fatalf("video: %+v", v)
	}
	if v.Init == nil || v.Init.URL != "https://cdn.example/v/init-2.m4s" || len(v.Segs) != 4 ||
		v.Segs[0].URL != "https://cdn.example/v/chunk-2-00001.m4s" || v.Segs[3].URL != "https://cdn.example/v/chunk-2-00004.m4s" {
		t.Fatalf("video segs: init=%+v segs=%+v", v.Init, v.Segs)
	}
	a := d.Audio[0]
	if a.Lang != "tr" || len(a.Segs) != 4 || a.Segs[1].URL != "https://cdn.example/v/a/48000-128000$.m4s" {
		t.Fatalf("audio: %+v", a)
	}
}

func TestParseMPDDurationTemplateAndBaseURL(t *testing.T) {
	body := `<MPD type="static" mediaPresentationDuration="PT1M0.5S"><BaseURL>https://media.example/root/</BaseURL>
<Period><AdaptationSet mimeType="video/mp4"><BaseURL>vid/</BaseURL>
<SegmentTemplate timescale="10" duration="20" startNumber="0" initialization="$RepresentationID$/i.mp4" media="$RepresentationID$/$Number$.m4s"/>
<Representation id="hd" bandwidth="1000" width="1280" height="720"/></AdaptationSet></Period></MPD>`
	d, err := parseLoaded(body, mustURL(t, "https://site.example/m.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	v := d.Video[0]
	// 60,5 sn × 10 / 20 = 30,25 → 31 parça; numara 0'dan.
	if len(v.Segs) != 31 || v.Segs[0].URL != "https://media.example/root/vid/hd/0.m4s" || v.Segs[30].URL != "https://media.example/root/vid/hd/30.m4s" ||
		v.Init.URL != "https://media.example/root/vid/hd/i.mp4" {
		t.Fatalf("segs=%d first=%v init=%v", len(v.Segs), v.Segs[0], v.Init)
	}
}

func TestParseMPDSegmentListAndBase(t *testing.T) {
	body := `<MPD type="static" mediaPresentationDuration="PT2S"><Period>
<AdaptationSet contentType="video"><Representation id="v" bandwidth="1"><BaseURL>v.mp4</BaseURL>
<SegmentList><Initialization range="0-799"/><SegmentURL mediaRange="800-1999"/><SegmentURL media="other.mp4" mediaRange="0-99"/></SegmentList>
</Representation></AdaptationSet>
<AdaptationSet contentType="audio"><Representation id="a" bandwidth="1"><BaseURL>a.mp4</BaseURL><SegmentBase indexRange="0-100"/></Representation></AdaptationSet>
</Period></MPD>`
	d, err := parseLoaded(body, mustURL(t, "https://x.example/d/m.mpd"))
	if err != nil {
		t.Fatal(err)
	}
	v := d.Video[0]
	if *v.Init != (segment{"https://x.example/d/v.mp4", "bytes=0-799"}) || len(v.Segs) != 2 ||
		v.Segs[0] != (segment{"https://x.example/d/v.mp4", "bytes=800-1999"}) || v.Segs[1] != (segment{"https://x.example/d/other.mp4", "bytes=0-99"}) {
		t.Fatalf("list: %+v %+v", v.Init, v.Segs)
	}
	a := d.Audio[0]
	if a.Init != nil || len(a.Segs) != 1 || a.Segs[0] != (segment{"https://x.example/d/a.mp4", ""}) {
		t.Fatalf("base: %+v %+v", a.Init, a.Segs)
	}
}

func TestParseMPDFlagsAndRejects(t *testing.T) {
	base := mustURL(t, "https://x.example/m.mpd")
	d, err := ParseMPD([]byte(`<MPD type="dynamic"><Period><AdaptationSet contentType="video"><Representation id="1"/></AdaptationSet></Period></MPD>`), base)
	if err != nil || !d.Live {
		t.Fatalf("dynamic canlı sayılmadı: %v", err)
	}
	prot := strings.Replace(mpdTimeline, `<Representation id="1"`, `<ContentProtection schemeIdUri="urn:mpeg:dash:mp4protection:2011" value="cenc"/><Representation id="1"`, 1)
	if d, err := ParseMPD([]byte(prot), base); err != nil || !d.Protected {
		t.Fatalf("ContentProtection korumalı sayılmadı: %v", err)
	}
	for name, body := range map[string]string{
		"iki bölüm": `<MPD type="static" mediaPresentationDuration="PT2S"><Period/><Period/></MPD>`,
		"webm":      `<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet mimeType="video/webm"><Representation id="1"><BaseURL>v.webm</BaseURL></Representation></AdaptationSet></Period></MPD>`,
		"xml değil": `{"a":1}`,
		"yerel ağ":  `<MPD type="static" mediaPresentationDuration="PT2S"><Period><AdaptationSet contentType="video"><Representation id="1"><BaseURL>http://10.0.0.5/v.mp4</BaseURL></Representation></AdaptationSet></Period></MPD>`,
		"süresiz":   `<MPD type="static"><Period><AdaptationSet contentType="video"><SegmentTemplate duration="2" media="$Number$.m4s"/><Representation id="1"/></AdaptationSet></Period></MPD>`,
	} {
		if _, err := parseLoaded(body, base); err == nil {
			t.Errorf("%s: hata bekleniyordu", name)
		}
	}
}

func TestISODuration(t *testing.T) {
	for in, want := range map[string]float64{"PT1H2M3.5S": 3723.5, "PT4S": 4, "P1DT1S": 86401, "PT0.25S": 0.25} {
		if got, err := isoDuration(in); err != nil || got != want {
			t.Errorf("%s: %v %v", in, got, err)
		}
	}
	if _, err := isoDuration("1:00"); err == nil {
		t.Error("geçersiz süre kabul edildi")
	}
}
