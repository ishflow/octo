package engine

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// DASH (ISO/IEC 23009-1) ayrıştırıcısı: ağsız, saf. encoding/xml dış varlık çözmez.

// DASH bellek sınırları (VGR H1): temsil sayısı ve bir temsilin parça adreslerinin toplam boyu.
const (
	maxReps      = 64
	maxPlanBytes = 32 << 20
)

type mpdRepr struct {
	ID        string
	Width     int
	Height    int
	Bandwidth int64
	Codecs    string
	Lang      string
	Name      string
	Init      *segment // nil: parçalar tek başına tam dosya (SegmentBase / yalnız BaseURL); load doldurur
	Segs      []segment
	segs      func() (*segment, []segment, error)
}

// load parça listesini açar. Yalnız seçilen temsil için çağrılır: hepsini açmak 4 KB'lık MPD'de GB'larca
// bellek demekti (VGR H1).
func (r *mpdRepr) load() (err error) {
	r.Init, r.Segs, err = r.segs()
	return err
}

type mpdDoc struct {
	Live      bool // MPD@type="dynamic"
	Protected bool // herhangi bir düzeyde ContentProtection
	Duration  float64
	Video     []mpdRepr
	Audio     []mpdRepr
}

type mpdURL struct {
	Source string `xml:"sourceURL,attr"`
	Range  string `xml:"range,attr"`
}

type mpdSegs struct {
	Timescale string `xml:"timescale,attr"`
	Duration  string `xml:"duration,attr"`
	Start     string `xml:"startNumber,attr"`
	Init      string `xml:"initialization,attr"`
	Media     string `xml:"media,attr"`
	Timeline  *struct {
		S []struct {
			T *int64 `xml:"t,attr"`
			D int64  `xml:"d,attr"`
			R int64  `xml:"r,attr"`
		} `xml:"S"`
	} `xml:"SegmentTimeline"`
	InitEl *mpdURL `xml:"Initialization"` // SegmentList / SegmentBase
	URLs   []struct {
		Media string `xml:"media,attr"`
		Range string `xml:"mediaRange,attr"`
	} `xml:"SegmentURL"`
}

type mpdLevel struct {
	BaseURL  []string `xml:"BaseURL"`
	Template *mpdSegs `xml:"SegmentTemplate"`
	List     *mpdSegs `xml:"SegmentList"`
	Base     *mpdSegs `xml:"SegmentBase"`
	MimeType string   `xml:"mimeType,attr"`
	Codecs   string   `xml:"codecs,attr"`
	Width    int      `xml:"width,attr"`
	Height   int      `xml:"height,attr"`
}

type mpdXML struct {
	XMLName  xml.Name `xml:"MPD"`
	Type     string   `xml:"type,attr"`
	Duration string   `xml:"mediaPresentationDuration,attr"`
	BaseURL  []string `xml:"BaseURL"`
	Periods  []struct {
		mpdLevel
		Duration string `xml:"duration,attr"`
		Sets     []struct {
			mpdLevel
			ContentType string `xml:"contentType,attr"`
			Lang        string `xml:"lang,attr"`
			Label       string `xml:"Label"`
			Reps        []struct {
				mpdLevel
				ID        string `xml:"id,attr"`
				Bandwidth int64  `xml:"bandwidth,attr"`
			} `xml:"Representation"`
		} `xml:"AdaptationSet"`
	} `xml:"Period"`
}

func ParseMPD(body []byte, base *url.URL) (*mpdDoc, error) {
	if len(body) > maxManifest {
		return nil, errNotManifest
	}
	var x mpdXML
	if err := xml.Unmarshal(body, &x); err != nil {
		return nil, errNotManifest
	}
	d := &mpdDoc{Live: x.Type == "dynamic", Protected: hasContentProtection(body)}
	if d.Live || d.Protected { // indirilmeyecek; parça planı gerekmez
		return d, nil
	}
	if len(x.Periods) != 1 {
		return nil, errors.New("Çok bölümlü DASH desteklenmiyor")
	}
	p := x.Periods[0]
	dur, err := isoDuration(p.Duration)
	if p.Duration == "" {
		dur, err = isoDuration(x.Duration)
	}
	if err != nil {
		dur = 0
	}
	d.Duration = dur
	b, err := baseChain(base, x.BaseURL, p.BaseURL)
	if err != nil {
		return nil, err
	}
	webm := false
	seen := map[string]bool{}
	for _, as := range p.Sets {
		asBase, err := baseChain(b, as.BaseURL)
		if err != nil {
			return nil, err
		}
		for _, r := range as.Reps {
			mime := first(r.MimeType, as.MimeType)
			kind := first(as.ContentType, strings.Split(mime, "/")[0])
			if strings.Contains(mime, "webm") {
				webm = true
				continue
			}
			if kind != "video" && kind != "audio" || seen[r.ID] { // yinelenen kimlikte ilki (seçim tutarlı kalsın)
				continue
			}
			if seen[r.ID] = true; len(seen) > maxReps {
				return nil, errors.New("Akışta çok fazla kalite var")
			}
			rb, err := baseChain(asBase, r.BaseURL)
			if err != nil {
				return nil, err
			}
			rep := mpdRepr{ID: r.ID, Bandwidth: r.Bandwidth, Codecs: first(r.Codecs, as.Codecs), Width: firstInt(r.Width, as.Width),
				Height: firstInt(r.Height, as.Height), Lang: as.Lang, Name: strings.TrimSpace(as.Label)}
			rep.segs = func() (*segment, []segment, error) {
				return mpdSegments(base, rb, &r.mpdLevel, &as.mpdLevel, &p.mpdLevel, r.ID, r.Bandwidth, dur)
			}
			if kind == "video" {
				d.Video = append(d.Video, rep)
			} else {
				d.Audio = append(d.Audio, rep)
			}
		}
	}
	if len(d.Video)+len(d.Audio) == 0 {
		if webm {
			return nil, errors.New("WebM biçimli DASH videoları desteklenmiyor")
		}
		return nil, errNotManifest
	}
	return d, nil
}

// hasContentProtection: DRM işareti hangi düzeyde olursa olsun (K31).
func hasContentProtection(body []byte) bool {
	dec := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := dec.Token()
		if err != nil {
			return false
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "ContentProtection" {
			return true
		}
	}
}

func first(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstInt(a, b int) int {
	if a != 0 {
		return a
	}
	return b
}

// baseChain düzeylerin ilk BaseURL'ini sırayla çözer.
func baseChain(base *url.URL, levels ...[]string) (*url.URL, error) {
	for _, l := range levels {
		if len(l) == 0 || strings.TrimSpace(l[0]) == "" {
			continue
		}
		n, err := base.Parse(strings.TrimSpace(l[0]))
		if err != nil {
			return nil, errSegURL
		}
		base = n
	}
	return base, nil
}

// mergeSegs: alt düzeyin dolu alanları üst düzeyinkini ezer (öznitelik kalıtımı).
func mergeSegs(levels ...*mpdSegs) *mpdSegs {
	var out *mpdSegs
	for _, l := range levels { // üstten alta
		if l == nil {
			continue
		}
		if out == nil {
			c := *l
			out = &c
			continue
		}
		out.Timescale, out.Duration, out.Start = first(l.Timescale, out.Timescale), first(l.Duration, out.Duration), first(l.Start, out.Start)
		out.Init, out.Media = first(l.Init, out.Init), first(l.Media, out.Media)
		if l.Timeline != nil {
			out.Timeline = l.Timeline
		}
		if l.InitEl != nil {
			out.InitEl = l.InitEl
		}
		if len(l.URLs) > 0 {
			out.URLs = l.URLs
		}
	}
	return out
}

// mpdSegments temsilin init + parça listesini çıkarır. manifest yerel ağ denetiminin dayanağıdır.
func mpdSegments(manifest, b *url.URL, rep, as, per *mpdLevel, id string, bw int64, dur float64) (*segment, []segment, error) {
	budget := maxPlanBytes
	ref := func(s, rng string) (segment, error) {
		r, err := b.Parse(strings.TrimSpace(s))
		if err != nil {
			return segment{}, errSegURL
		}
		u, err := resolveRef(manifest, r.String())
		if err != nil {
			return segment{}, err
		}
		if budget -= len(u) + len(rng); budget < 0 { // uzun BaseURL/RepresentationID × parça sayısı
			return segment{}, errTooManySegs
		}
		if rng != "" {
			rng = "bytes=" + rng
		}
		return segment{URL: u, Range: rng}, nil
	}
	if t := mergeSegs(per.Template, as.Template, rep.Template); t != nil {
		return templateSegs(t, ref, id, bw, dur)
	}
	if l := mergeSegs(per.List, as.List, rep.List); l != nil {
		var init *segment
		if l.InitEl != nil {
			s, err := ref(l.InitEl.Source, l.InitEl.Range)
			if err != nil {
				return nil, nil, err
			}
			init = &s
		}
		if len(l.URLs) > maxSegments {
			return nil, nil, errTooManySegs
		}
		segs := make([]segment, 0, len(l.URLs))
		for _, su := range l.URLs {
			s, err := ref(su.Media, su.Range)
			if err != nil {
				return nil, nil, err
			}
			segs = append(segs, s)
		}
		return init, segs, nil
	}
	s, err := ref("", "") // SegmentBase ya da yalnız BaseURL: tüm dosya tek parça
	if err != nil {
		return nil, nil, err
	}
	return nil, []segment{s}, nil
}

func templateSegs(t *mpdSegs, ref func(s, rng string) (segment, error), id string, bw int64, dur float64) (*segment, []segment, error) {
	scale := int64(1)
	if t.Timescale != "" {
		var err error
		if scale, err = strconv.ParseInt(t.Timescale, 10, 64); err != nil || scale <= 0 {
			return nil, nil, errNotManifest
		}
	}
	if !tmplWidthOK(t.Media) || !tmplWidthOK(t.Init) {
		return nil, nil, errNotManifest
	}
	num := int64(1)
	if t.Start != "" {
		var err error
		if num, err = strconv.ParseInt(t.Start, 10, 64); err != nil {
			return nil, nil, errNotManifest
		}
	}
	var init *segment
	if t.Init != "" {
		s, err := ref(expandTemplate(t.Init, id, bw, 0, 0), "")
		if err != nil {
			return nil, nil, err
		}
		init = &s
	}
	var segs []segment
	add := func(n, tm int64) error {
		if len(segs) >= maxSegments {
			return errTooManySegs
		}
		s, err := ref(expandTemplate(t.Media, id, bw, n, tm), "")
		segs = append(segs, s)
		return err
	}
	end := int64(math.Round(dur * float64(scale))) // dönem sonu (timescale biriminde)
	if t.Timeline != nil {
		var tm int64
		for i, s := range t.Timeline.S {
			if s.T != nil {
				tm = *s.T
			}
			if s.D <= 0 {
				return nil, nil, errNotManifest
			}
			reps := s.R
			if reps < 0 { // -1: sonraki S'e ya da dönem sonuna kadar
				stop := end
				if i+1 < len(t.Timeline.S) && t.Timeline.S[i+1].T != nil {
					stop = *t.Timeline.S[i+1].T
				}
				if stop <= tm {
					return nil, nil, errors.New("DASH süresi bilinmiyor")
				}
				reps = (stop-tm+s.D-1)/s.D - 1
			}
			for k := int64(0); k <= reps; k++ {
				if err := add(num, tm); err != nil {
					return nil, nil, err
				}
				num, tm = num+1, tm+s.D
			}
		}
		return init, segs, nil
	}
	d, err := strconv.ParseInt(t.Duration, 10, 64)
	if err != nil || d <= 0 || end <= 0 {
		return nil, nil, errors.New("DASH süresi bilinmiyor")
	}
	count := (end + d - 1) / d
	if count > maxSegments {
		return nil, nil, errTooManySegs
	}
	for k := int64(0); k < count; k++ {
		if err := add(num+k, k*d); err != nil {
			return nil, nil, err
		}
	}
	return init, segs, nil
}

var tmplVar = regexp.MustCompile(`\$(RepresentationID|Number|Time|Bandwidth|)(%0(\d+)d)?\$`)

// tmplWidthOK: %0Nd genişliği ≤ 16 (fmt 1e6'ya izin verir: parça başına 1 MB adres).
func tmplWidthOK(s string) bool {
	for _, m := range tmplVar.FindAllStringSubmatch(s, -1) {
		if w, err := strconv.Atoi(m[3]); m[3] != "" && (err != nil || w > 16) {
			return false
		}
	}
	return true
}

// expandTemplate $RepresentationID$, $Number%05d$, $Time$, $Bandwidth$, $$ yer tutucularını doldurur.
func expandTemplate(s, id string, bw, num, tm int64) string {
	return tmplVar.ReplaceAllStringFunc(s, func(m string) string {
		p := tmplVar.FindStringSubmatch(m)
		var v int64
		switch p[1] {
		case "":
			return "$"
		case "RepresentationID":
			return id
		case "Number":
			v = num
		case "Time":
			v = tm
		case "Bandwidth":
			v = bw
		}
		w, _ := strconv.Atoi(p[3])
		return fmt.Sprintf("%0*d", w, v)
	})
}

var isoDur = regexp.MustCompile(`^P(?:(\d+(?:\.\d+)?)D)?(?:T(?:(\d+(?:\.\d+)?)H)?(?:(\d+(?:\.\d+)?)M)?(?:(\d+(?:\.\d+)?)S)?)?$`)

// isoDuration ISO 8601 süresini (PnDTnHnMnS) saniyeye çevirir.
func isoDuration(s string) (float64, error) {
	m := isoDur.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || s == "P" || s == "PT" {
		return 0, fmt.Errorf("geçersiz süre: %q", s)
	}
	var total float64
	for i, mul := range []float64{86400, 3600, 60, 1} {
		if m[i+1] != "" {
			f, _ := strconv.ParseFloat(m[i+1], 64)
			total += f * mul
		}
	}
	return total, nil
}
