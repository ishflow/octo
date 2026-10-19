package engine

import (
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Dosya adı üst sınırı bayt cinsinden. Klasik yöneticiler 256 karakter der; macOS 255 bayt sınırı ve
// eklenen ".part" ile " (NN)" için pay bırakılır.
const maxNameBytes = 240

const fallbackName = "indirilen"

// illegalMap yasak karakter eşlemesidir; "/" ve "\" da dahil.
var illegalMap = map[rune]rune{':': '-', '*': '-', '?': '.', '|': '-', '<': '-', '>': '-', '"': '\'', '\\': '-', '/': '-'}

// sanitizeName adı güvenli tek bir dosya adına çevirir; yol ayracı içermez, yalnız noktadan ibaret olamaz.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '‎' || r == '‏' || (r >= '‪' && r <= '‮') || (r >= '⁦' && r <= '⁩'):
			// çift yönlü yazı işaretleri kaldırılır (evil‮gpj.exe sahteciliği)
		case r < 0x20 || r == 0x7f:
			b.WriteRune('-')
		case illegalMap[r] != 0:
			b.WriteRune(illegalMap[r])
		default:
			b.WriteRune(r)
		}
	}
	s = strings.TrimSpace(b.String())
	if strings.Trim(s, ".") == "" {
		return fallbackName
	}
	return capName(s)
}

// capName adı maxNameBytes'a kısaltır; kısa (≤10) uzantıyı korur, UTF-8 karakteri bölmez.
func capName(s string) string {
	if len(s) <= maxNameBytes {
		return s
	}
	ext := path.Ext(s)
	if len(ext) > 10 {
		ext = ""
	}
	base := s[:len(s)-len(ext)]
	n := maxNameBytes - len(ext)
	for n > 0 && !utf8.RuneStart(base[n]) {
		n--
	}
	return base[:n] + ext
}

// dispParams Content-Disposition parametrelerini anahtar (küçük harf) → değer olarak ayırır. mime.ParseMediaType
// tırnaksız/ham UTF-8 adlarda ve tekrarlı anahtarlarda tüm başlığı reddettiği için hoşgörülü elle ayrıştırılır
// (klasik yöneticiler de böyle okur). Tekrarlanan anahtarda ilk değer kalır.
func dispParams(cd string) map[string]string {
	out := map[string]string{}
	var cur strings.Builder
	inQuote, esc := false, false
	flush := func() {
		k, v, ok := strings.Cut(cur.String(), "=")
		cur.Reset()
		if !ok {
			return
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if _, dup := out[k]; !dup {
			out[k] = v
		}
	}
	rs := []rune(cd)
	for i, r := range rs {
		switch {
		case esc:
			esc = false
			cur.WriteRune(r)
		case inQuote && r == '\\' && i+1 < len(rs) && (rs[i+1] == '"' || rs[i+1] == '\\'): // IE yol hatası: diğer \ olduğu gibi kalır
			esc = true
		case r == '"':
			inQuote = !inQuote
		case r == ';' && !inQuote:
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// latin5: ISO-8859-9'un Latin-1'den ayrıldığı altı bayt (Türkçe harfler).
var latin5 = map[byte]rune{0xD0: 'Ğ', 0xDD: 'İ', 0xDE: 'Ş', 0xF0: 'ğ', 0xFD: 'ı', 0xFE: 'ş'}

// cp1252 windows-1252'nin 0x80–0x9F bölümüdür (Latin-1'de burası denetim karakteridir).
var cp1252 = []rune("€\u0081‚ƒ„…†‡ˆ‰Š‹Œ\u008dŽ\u008f\u0090‘’“”•–—˜™š›œ\u009džŸ")

// decodeCharset yüzde çözülmüş baytları charset'e göre dizgeye çevirir. UTF-8 (ya da bilinmeyen) charset
// geçersiz bayt içeriyorsa windows-1252 sayılır.
// shortcut: öteki iso-8859-* kümeleri Latin-1 gibi çözülür, çok baytlı eski kümeler (shift_jis…) yok; gerekirse golang.org/x/text.
func decodeCharset(cs, raw string) string {
	cs = strings.ToLower(cs)
	turkish := cs == "iso-8859-9" || cs == "windows-1254" || cs == "latin5"
	win := cs == "windows-1252" || cs == "cp1252" || cs == "windows-1254"
	if !turkish && !win && !strings.HasPrefix(cs, "iso-8859") && cs != "latin1" && cs != "l1" && utf8.ValidString(raw) {
		return raw
	}
	rs := make([]rune, len(raw))
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		rs[i] = rune(c)
		if r, ok := latin5[c]; ok && turkish {
			rs[i] = r
		} else if c >= 0x80 && c <= 0x9f && !strings.HasPrefix(cs, "iso-8859") && cs != "latin1" && cs != "l1" {
			rs[i] = cp1252[c-0x80]
		}
	}
	return string(rs)
}

// extName RFC 5987/2231 genişletilmiş adı çözer: filename*=cs'dil'%xx ya da devamlar
// (filename*0*=cs'dil'%xx; filename*1*=%xx; filename*2=düz…). Yıldızlı parça yüzde çözülür, yıldızsız parça
// olduğu gibi katılır; charset yalnız ilk parçada olur. Devamlar 0'dan kesintisiz sıralanır.
// Yıldız yoksa (düz devam: filename*0="a"; filename*1="b") ext false döner ve birleşik düz ad verilir.
func extName(p map[string]string) (name string, ext bool) {
	if v := p["filename*"]; v != "" {
		if parts := strings.SplitN(v, "'", 3); len(parts) == 3 {
			if raw, err := url.PathUnescape(parts[2]); err == nil {
				return decodeCharset(parts[0], raw), true
			}
		}
	}
	var raw strings.Builder
	cs := ""
	for i := 0; ; i++ {
		k := "filename*" + strconv.Itoa(i)
		if v, ok := p[k+"*"]; ok {
			ext = true
			if parts := strings.SplitN(v, "'", 3); i == 0 && len(parts) == 3 {
				cs, v = parts[0], parts[2]
			}
			if d, err := url.PathUnescape(v); err == nil {
				v = d
			}
			raw.WriteString(v)
		} else if v, ok := p[k]; ok {
			raw.WriteString(v)
		} else {
			break
		}
	}
	if ext {
		return decodeCharset(cs, raw.String()), true
	}
	return raw.String(), false
}

// headerName Content-Disposition'dan adı çıkarır (filename* > devamlar > filename); yoksa "". % tam bir kez çözülür.
func headerName(cd string) string {
	p := dispParams(cd)
	n, ext := extName(p)
	if ext {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
		n = ""
	}
	if n == "" {
		n = p["filename"]
	}
	if d, err := new(mime.WordDecoder).DecodeHeader(n); err == nil {
		n = d
	}
	if u, err := url.PathUnescape(n); err == nil {
		n = u
	}
	return strings.TrimSpace(n)
}

// NameFrom: Content-Disposition (filename* > filename), yoksa adresin son parçası, yoksa "indirilen".
func NameFrom(resp *http.Response) string {
	if n := headerName(resp.Header.Get("Content-Disposition")); n != "" {
		return sanitizeName(n)
	}
	if resp.Request == nil || resp.Request.URL == nil {
		return fallbackName
	}
	return NameFromURL(resp.Request.URL.String())
}

// NameFromURL: adres yolunun son parçası, tam bir kez % çözülür (%252F → %2F, yol geçişi olmaz).
func NameFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return fallbackName
	}
	base := path.Base(u.EscapedPath()) // ayrılmadan önce: %2F ayraç sayılmaz
	if base == "/" {
		return fallbackName
	}
	if n, err := url.PathUnescape(base); err == nil {
		base = n
	}
	return sanitizeName(base)
}

// FileNameFromResponse eski ad; NameFrom'a yönlenir.
func FileNameFromResponse(resp *http.Response) string { return NameFrom(resp) }

// DefaultCategories varsayılan sınıflardır; Genel diğer her şeyi karşılar.
// Programlar'a macOS için dmg ve pkg eklendi (envanterin Mac önerisi).
func DefaultCategories() []Category {
	return []Category{
		{Name: "Genel", Exts: []string{}},
		{Name: "Arşiv", Exts: []string{"zip", "rar", "r0*", "r1*", "arj", "gz", "sit", "sitx", "sea", "ace", "bz2", "7z"}},
		{Name: "Belgeler", Exts: []string{"doc", "pdf", "ppt", "pps", "docx", "pptx"}},
		{Name: "Müzik", Exts: []string{"mp3", "wav", "wma", "mpa", "ram", "ra", "aac", "aif", "m4a", "tsa"}},
		{Name: "Programlar", Exts: []string{"exe", "msi", "dmg", "pkg"}},
		{Name: "Video", Exts: []string{"avi", "mpg", "mpe", "mpeg", "asf", "wmv", "mov", "qt", "rm", "mp4", "flv", "m4v", "webm", "ogv", "ogg", "mkv", "ts", "tsv"}},
	}
}

// CategoryFor uzantıya göre ilk eşleşen sınıfın adını verir (desen: "r0*"); yoksa "Genel".
func CategoryFor(name string, cats []Category) string {
	ext := strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))
	if ext != "" {
		for _, c := range cats {
			for _, pat := range c.Exts {
				if ok, _ := path.Match(strings.ToLower(pat), ext); ok {
					return c.Name
				}
			}
		}
	}
	return "Genel"
}

// UniquePath: "number" kipinde dolu yol için "x (2).zip", "x (3).zip"… verir. Diskte olan, ".part" eşi olan
// ya da inUse'un dolu saydığı yol alınmış sayılır. "overwrite" kipinde yol aynen döner; ancak inUse ise numaralanır
// (çalışan bir indirmenin dosyası ezilmez).
func UniquePath(p, mode string, inUse func(string) bool) string {
	taken := func(s string) bool {
		if inUse != nil && inUse(s) {
			return true
		}
		if mode == "overwrite" && s == p {
			return false
		}
		for _, f := range []string{s, s + ".part"} {
			if _, err := os.Lstat(f); err == nil {
				return true
			}
		}
		return false
	}
	if !taken(p) {
		return p
	}
	ext := filepath.Ext(p)
	stem := strings.TrimSuffix(p, ext)
	for n := 2; ; n++ {
		if c := stem + " (" + strconv.Itoa(n) + ")" + ext; !taken(c) {
			return c
		}
	}
}
