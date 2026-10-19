package engine

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"net/url"
	"strconv"
	"strings"
)

// HLS (RFC 8216) ayrıştırıcısı: ağsız, saf. base yönlendirme sonrası manifest adresidir.

type hlsVariant struct {
	StreamVariant
	URI   string
	Group string // AUDIO grup kimliği
}

type hlsAudio struct {
	StreamAudio
	Group string
	URI   string
}

type hlsDoc struct {
	Master   bool
	Variants []hlsVariant // master: listedeki sırayla (kimlik özelliklerden: attrID)
	Audios   []hlsAudio   // master: URI'li ses rendition'ları (kimlik özelliklerden: attrID)

	// medya listesi
	Init      *segment
	Segs      []segment
	Container string // "ts" | "mp4"
	Duration  float64

	Live      bool // EXT-X-ENDLIST yok
	Protected bool // EXT-X-KEY / EXT-X-SESSION-KEY METHOD≠NONE (AES-128 dahil, K31)
}

// hlsAttrs `A=B,C="d,e"` öznitelik listesini okur.
func hlsAttrs(s string) map[string]string {
	out := map[string]string{}
	for s != "" {
		k, rest, ok := strings.Cut(s, "=")
		if !ok {
			break
		}
		var v string
		if strings.HasPrefix(rest, `"`) {
			end := strings.IndexByte(rest[1:], '"')
			if end < 0 {
				v, rest = rest[1:], ""
			} else {
				v, rest = rest[1:end+1], rest[end+2:]
			}
			rest = strings.TrimPrefix(rest, ",")
		} else {
			v, rest, _ = strings.Cut(rest, ",")
		}
		out[strings.TrimSpace(k)] = v
		s = rest
	}
	return out
}

// attrID konumdan bağımsız kimliktir: sırası değişen listede (Adresi yenile, CDN) aynı özellikler aynı kimliği
// verir, devam başka kaliteyle karışmaz (VGR M5). Aynı özellikli girdiler sırayla -2, -3 alır.
func attrID(prefix string, taken map[string]bool, parts ...string) string {
	h := fnv.New32a()
	for _, p := range parts {
		h.Write([]byte(p + "\x00"))
	}
	id := fmt.Sprintf("%s%08x", prefix, h.Sum32())
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s%08x-%d", prefix, h.Sum32(), n)
	}
	taken[id] = true
	return id
}

// hlsRange "n[@o]" → bytes=o-(o+n-1); o yoksa prev (önceki parçanın sonu, -1 = yok).
func hlsRange(s string, prev int64) (rng string, end int64, err error) {
	ns, offs, hasOff := strings.Cut(s, "@")
	n, err := strconv.ParseInt(ns, 10, 64)
	if err != nil || n <= 0 {
		return "", 0, fmt.Errorf("geçersiz BYTERANGE: %q", s)
	}
	o := prev
	if hasOff {
		if o, err = strconv.ParseInt(offs, 10, 64); err != nil || o < 0 || n > math.MaxInt64-o {
			return "", 0, fmt.Errorf("geçersiz BYTERANGE: %q", s)
		}
	} else if prev < 0 {
		return "", 0, fmt.Errorf("BYTERANGE ofseti yok: %q", s)
	} else if n > math.MaxInt64-o {
		return "", 0, fmt.Errorf("geçersiz BYTERANGE: %q", s)
	}
	return fmt.Sprintf("bytes=%d-%d", o, o+n-1), o + n, nil
}

func ParseHLS(body []byte, base *url.URL) (*hlsDoc, error) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 64<<10), maxManifest)
	if !sc.Scan() || strings.TrimSpace(strings.TrimPrefix(sc.Text(), "\ufeff")) != "#EXTM3U" {
		return nil, errNotManifest
	}
	d := &hlsDoc{Container: "ts"}
	ended, mapSeen := false, false
	var pendingVar *hlsVariant
	var pendingRange string
	byteEnd := int64(-1)
	ids := map[string]bool{}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		tag, val, _ := strings.Cut(line, ":")
		switch {
		case line == "":
		case tag == "#EXT-X-STREAM-INF":
			a := hlsAttrs(val)
			v := hlsVariant{Group: a["AUDIO"]}
			v.ID = attrID("v", ids, a["BANDWIDTH"], a["RESOLUTION"], a["CODECS"], a["AUDIO"], a["FRAME-RATE"])
			v.Bandwidth, _ = strconv.ParseInt(a["BANDWIDTH"], 10, 64)
			v.Codecs = a["CODECS"]
			if w, h, ok := strings.Cut(a["RESOLUTION"], "x"); ok {
				v.Width, _ = strconv.Atoi(w)
				v.Height, _ = strconv.Atoi(h)
			}
			pendingVar = &v
		case tag == "#EXT-X-MEDIA":
			a := hlsAttrs(val)
			if a["TYPE"] != "AUDIO" {
				continue
			}
			if a["URI"] == "" { // gömülü ses: ayrı iz değil
				continue
			}
			id := attrID("a", ids, a["GROUP-ID"], a["NAME"], a["LANGUAGE"])
			u, err := resolveRef(base, a["URI"])
			if err != nil {
				return nil, err
			}
			d.Audios = append(d.Audios, hlsAudio{StreamAudio: StreamAudio{ID: id, Name: a["NAME"], Lang: a["LANGUAGE"], Default: a["DEFAULT"] == "YES"}, Group: a["GROUP-ID"], URI: u})
		case tag == "#EXT-X-KEY" || tag == "#EXT-X-SESSION-KEY":
			if m := hlsAttrs(val)["METHOD"]; m != "" && m != "NONE" {
				d.Protected = true
			}
		case tag == "#EXT-X-ENDLIST":
			ended = true
		case tag == "#EXT-X-MAP":
			a := hlsAttrs(val)
			u, err := resolveRef(base, a["URI"])
			if err != nil {
				return nil, err
			}
			seg := segment{URL: u}
			if a["BYTERANGE"] != "" {
				if seg.Range, _, err = hlsRange(a["BYTERANGE"], -1); err != nil {
					return nil, err
				}
			}
			if mapSeen && *d.Init != seg { // farklı init: kodek/çözünürlük değişiyor, tek dosyada birleşmez
				return nil, errors.New("Akış ortasında biçim değişiyor; desteklenmiyor")
			}
			mapSeen, d.Init, d.Container = true, &seg, "mp4"
		case tag == "#EXTINF":
			f, _, _ := strings.Cut(val, ",")
			dur, _ := strconv.ParseFloat(strings.TrimSpace(f), 64)
			d.Duration += dur
		case tag == "#EXT-X-BYTERANGE":
			var err error
			if pendingRange, byteEnd, err = hlsRange(val, byteEnd); err != nil {
				return nil, err
			}
		case strings.HasPrefix(line, "#"):
		default: // URI satırı
			u, err := resolveRef(base, line)
			if err != nil {
				return nil, err
			}
			if pendingVar != nil {
				pendingVar.URI = u
				pendingVar.Label = variantLabel(pendingVar.Height, pendingVar.Bandwidth)
				d.Variants, pendingVar, d.Master = append(d.Variants, *pendingVar), nil, true
				continue
			}
			if len(d.Segs) >= maxSegments {
				return nil, errTooManySegs
			}
			d.Segs = append(d.Segs, segment{URL: u, Range: pendingRange})
			pendingRange = ""
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	d.Master = d.Master || len(d.Variants) > 0
	d.Live = !d.Master && !ended
	return d, nil
}

// variantLabel: "1080p"; çözünürlük yoksa bant genişliği.
func variantLabel(height int, bw int64) string {
	if height > 0 {
		return fmt.Sprintf("%dp", height)
	}
	if bw > 0 {
		return fmt.Sprintf("%d kb/sn", bw/1000)
	}
	return "Video"
}
