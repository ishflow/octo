package engine

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func respWith(t *testing.T, rawURL, cd string) *http.Response {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	if cd != "" {
		h.Set("Content-Disposition", cd)
	}
	return &http.Response{Header: h, Request: &http.Request{URL: u}}
}

func TestNameFrom(t *testing.T) {
	long := strings.Repeat("a", 300)
	cases := []struct{ name, url, cd, want string }{
		{"plain", "http://h/x", `attachment; filename="a.zip"`, "a.zip"},
		{"rfc5987", "http://h/x", `attachment; filename*=UTF-8''%C3%B6zel%20dosya.zip`, "özel dosya.zip"},
		{"rfc5987 wins after plain", "http://h/x", `attachment; filename="fallback.zip"; filename*=UTF-8''%C3%B6zel.zip`, "özel.zip"},
		{"rfc5987 wins before plain", "http://h/x", `attachment; filename*=UTF-8''%C3%B6zel.zip; filename="fallback.zip"`, "özel.zip"},
		{"encoded word B", "http://h/x", `attachment; filename="=?UTF-8?B?w7ZaZWwuemlw?="`, "öZel.zip"},
		{"encoded word Q", "http://h/x", `attachment; filename="=?UTF-8?Q?=C3=B6zel.zip?="`, "özel.zip"},
		{"filename* decoded once", "http://h/x", `attachment; filename*=UTF-8''a%2520b.zip`, "a%20b.zip"},
		{"filename* latin1", "http://h/x", `attachment; filename*=ISO-8859-1''%FCber.zip`, "über.zip"},
		{"unquoted raw utf8", "http://h/x", "attachment; filename=ö.zip", "ö.zip"},
		{"unquoted with space", "http://h/x", "attachment; filename=a b.zip", "a b.zip"},
		{"duplicate filename keeps first", "http://h/x", `attachment; filename="one.zip"; filename="two.zip"`, "one.zip"},
		{"duplicate with quoted semicolon", "http://h/x", `attachment; filename="a;b.zip"; filename=c.zip`, "a;b.zip"},
		{"blank name falls to url", "http://h/real.iso", `attachment; filename=" "`, "real.iso"},
		{"percent in plain, once", "http://h/x", `attachment; filename="a%20b.zip"`, "a b.zip"},
		{"double-encoded slash in header", "http://h/x", `attachment; filename="%252F..%252Fetc%252Fpasswd"`, "%2F..%2Fetc%2Fpasswd"},
		{"traversal in header", "http://h/x", `attachment; filename="../../etc/passwd"`, "..-..-etc-passwd"},
		{"backslash traversal", "http://h/x", `attachment; filename="..\..\win.ini"`, "..-..-win.ini"},
		{"illegal chars", "http://h/x", `attachment; filename="a:b*c?d|e<f>g\"h.txt"`, "a-b-c.d-e-f-g'h.txt"},
		{"control chars and bidi", "http://h/x", "attachment; filename=\"a\x01b‮gpj.exe\"", "a-bgpj.exe"},
		{"dots only", "http://h/x", `attachment; filename=".."`, "indirilen"},
		{"nul", "http://h/x", "attachment; filename=\"a\x00b\"", "a-b"},
		{"url fallback", "http://h/dir/file%20name.iso?x=1", "", "file name.iso"},
		{"url fallback bad header", "http://h/dir/f.iso", `garbage;;;`, "f.iso"},
		{"url double-encoded slash", "http://h/%252F..%252Fetc%252Fpasswd", "", "%2F..%2Fetc%2Fpasswd"},
		{"url encoded slash", "http://h/a%2Fb.zip", "", "a-b.zip"},
		{"url encoded dotdot", "http://h/%2e%2e", "", "indirilen"},
		{"url root", "http://h/", "", "indirilen"},
		{"url no path", "http://h", "", "indirilen"},
		{"no extension", "http://h/download", "", "download"},
		{"nil request", "", "", "indirilen"},
		{"long keeps ext", "http://h/x", `attachment; filename="` + long + `.zip"`, strings.Repeat("a", maxNameBytes-4) + ".zip"},
		{"long no ext", "http://h/x", `attachment; filename="` + long + `"`, strings.Repeat("a", maxNameBytes)},
		{"long multibyte not split", "http://h/x", `attachment; filename="` + strings.Repeat("ö", 200) + `.zip"`, strings.Repeat("ö", (maxNameBytes-4)/2) + ".zip"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := &http.Response{Header: http.Header{}}
			if c.url != "" {
				resp = respWith(t, c.url, c.cd)
			}
			got := NameFrom(resp)
			if got != c.want {
				t.Errorf("NameFrom = %q, want %q", got, c.want)
			}
			if strings.ContainsAny(got, "/\\\x00") || got == "." || got == ".." || len(got) > maxNameBytes {
				t.Errorf("unsafe name %q", got)
			}
			if FileNameFromResponse(resp) != got {
				t.Errorf("FileNameFromResponse must delegate to NameFrom")
			}
		})
	}
}

func TestNameFromURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://h/a/b/setup.exe?token=1#f", "setup.exe"},
		{"https://h/%C3%B6zel%20dosya.zip", "özel dosya.zip"},
		{"https://h/%252F..%252Fetc%252Fpasswd", "%2F..%2Fetc%2Fpasswd"},
		{"https://h/..%2F..%2Fetc%2Fpasswd", "..-..-etc-passwd"},
		{"https://h/a/..", "indirilen"},
		{"https://h/", "indirilen"},
		{"", "indirilen"},
		{"://bad", "indirilen"},
		{"https://h/bad%zzname", "indirilen"},
	}
	for _, c := range cases {
		if got := NameFromURL(c.in); got != c.want {
			t.Errorf("NameFromURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDefaultCategoriesAndCategoryFor(t *testing.T) {
	cats := DefaultCategories()
	var names []string
	for _, c := range cats {
		names = append(names, c.Name)
	}
	if got := strings.Join(names, ","); got != "Genel,Arşiv,Belgeler,Müzik,Programlar,Video" {
		t.Fatalf("names = %s", got)
	}
	cases := map[string]string{
		"a.zip": "Arşiv", "a.ZIP": "Arşiv", "a.r07": "Arşiv", "a.r15": "Arşiv", "a.7z": "Arşiv", "a.tar.gz": "Arşiv",
		"x.pdf": "Belgeler", "x.docx": "Belgeler",
		"s.mp3": "Müzik", "s.m4a": "Müzik",
		"i.exe": "Programlar", "i.msi": "Programlar", "i.dmg": "Programlar", "i.pkg": "Programlar",
		"v.mkv": "Video", "v.mp4": "Video", "v.ts": "Video",
		"noext": "Genel", "a.unknown": "Genel", "trailingdot.": "Genel", "": "Genel", "dosya.iso": "Genel",
	}
	for n, want := range cases {
		if got := CategoryFor(n, cats); got != want {
			t.Errorf("CategoryFor(%q) = %q, want %q", n, got, want)
		}
	}
	custom := []Category{{Name: "Kitap", Exts: []string{"epub"}}}
	if got := CategoryFor("a.epub", custom); got != "Kitap" {
		t.Errorf("custom = %q", got)
	}
	if got := CategoryFor("a.zip", custom); got != "Genel" {
		t.Errorf("fallback = %q", got)
	}
	if got := CategoryFor("a.zip", nil); got != "Genel" {
		t.Errorf("nil cats = %q", got)
	}
}

func TestUniquePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.zip")
	touch := func(f string) {
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got := UniquePath(p, "number", nil); got != p {
		t.Errorf("free path changed: %s", got)
	}
	touch(p)
	if got, want := UniquePath(p, "number", nil), filepath.Join(dir, "x (2).zip"); got != want {
		t.Errorf("got %s want %s", got, want)
	}
	touch(filepath.Join(dir, "x (2).zip.part")) // .part counts as taken
	if got, want := UniquePath(p, "number", nil), filepath.Join(dir, "x (3).zip"); got != want {
		t.Errorf("got %s want %s", got, want)
	}
	inUse := func(s string) bool { return s == filepath.Join(dir, "x (3).zip") }
	if got, want := UniquePath(p, "number", inUse), filepath.Join(dir, "x (4).zip"); got != want {
		t.Errorf("inUse: got %s want %s", got, want)
	}
	// only .part exists
	q := filepath.Join(dir, "y")
	touch(q + ".part")
	if got, want := UniquePath(q, "number", nil), filepath.Join(dir, "y (2)"); got != want {
		t.Errorf("no ext: got %s want %s", got, want)
	}
	// overwrite keeps existing file path but never a path that is in use
	if got := UniquePath(p, "overwrite", nil); got != p {
		t.Errorf("overwrite: %s", got)
	}
	if got, want := UniquePath(p, "overwrite", func(s string) bool { return s == p }), filepath.Join(dir, "x (3).zip"); got != want {
		t.Errorf("overwrite+inUse: got %s want %s", got, want)
	}
}

// ISO-8859-9 (Latin-5) Türkçe harfleri Latin-1'den farklı konumlarda tutar.
func TestNameFromLatin5(t *testing.T) {
	r := respWith(t, "http://x/y", `attachment; filename*=iso-8859-9''%DE%FDk%20%F0%DD%FE%D0.txt`)
	if got := NameFrom(r); got != "Şık ğİşĞ.txt" {
		t.Fatalf("%q", got)
	}
	r = respWith(t, "http://x/y", `attachment; filename*=iso-8859-1''%E9t%E9.txt`)
	if got := NameFrom(r); got != "été.txt" {
		t.Fatalf("%q", got)
	}
}

// RFC 2231 devamları ve UTF-8 dışı charset'ler.
func TestNameFromContinuationsAndCharsets(t *testing.T) {
	cases := []struct{ name, cd, want string }{
		{"continuation encoded", `attachment; filename*0*=UTF-8''%C3%B6zel%20; filename*1*=dosya; filename*2=".zip"`, "özel dosya.zip"},
		{"continuation percent split inside char", `attachment; filename*0*=utf-8''%C3; filename*1*=%B6.zip`, "ö.zip"},
		{"continuation literal part not decoded", `attachment; filename*0*=utf-8''a; filename*1="%41.zip"`, "a%41.zip"},
		{"continuation order independent", `attachment; filename*1*=b.zip; filename*0*=utf-8''a`, "ab.zip"},
		{"continuation plain", `attachment; filename*0="rapor-"; filename*1="2026.pdf"`, "rapor-2026.pdf"},
		{"continuation stops at gap", `attachment; filename*0*=utf-8''a; filename*2*=c.zip`, "a"},
		{"filename* beats continuation", `attachment; filename*0*=utf-8''a.zip; filename*=utf-8''b.zip`, "b.zip"},
		{"continuation beats filename", `attachment; filename="c.zip"; filename*0*=utf-8''a.zip`, "a.zip"},
		{"continuation latin1", `attachment; filename*0*=iso-8859-1''%FC; filename*1*=ber.zip`, "über.zip"},
		{"windows-1252 euro and quotes", `attachment; filename*=windows-1252''%80%93x%94.txt`, "€“x”.txt"},
		{"windows-1254 turkish", `attachment; filename*=windows-1254''%DE%FD%F0.txt`, "Şığ.txt"},
		{"latin1 keeps C1 as is", `attachment; filename*=ISO-8859-1''a%80.txt`, "a\u0080.txt"},
		{"invalid utf-8 falls to windows-1252", `attachment; filename*=utf-8''%FCber.zip`, "über.zip"},
		{"unknown charset valid utf-8 kept", `attachment; filename*=x-foo''%C3%B6.zip`, "ö.zip"},
		{"empty ext value falls to filename", `attachment; filename*=utf-8''; filename="f.zip"`, "f.zip"},
	}
	for _, c := range cases {
		if got := NameFrom(respWith(t, "http://h/x", c.cd)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
