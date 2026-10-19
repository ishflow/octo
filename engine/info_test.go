package engine

import (
	"encoding/json"
	"strings"
	"testing"
)

// Bilgi paneli: Yönlendiren (gönderilen Referer) ve Kaynak sayfa öğeyle gelir; açıklama sonradan düzenlenir.
func TestInfoReferrerPageAndDescribe(t *testing.T) {
	h := newHarness(t)
	a, err := h.m.Add(AddArgs{URL: "http://x/a.bin", Name: "a.bin", Headers: map[string]string{"Referer": "http://x/"}, PageURL: "http://x/page?q=1"})
	if err != nil {
		t.Fatal(err)
	}
	if a.Referrer != "http://x/" || a.PageURL != "http://x/page?q=1" || a.Headers != nil {
		t.Fatalf("%+v", a)
	}
	ev, unsub := h.m.Subscribe()
	defer unsub()
	d, err := h.m.Describe(a.ID, "yeni not")
	if err != nil || d.Description != "yeni not" || d.Referrer != "http://x/" {
		t.Fatal(d, err)
	}
	if e := waitEvent(t, ev, "item"); e.Item.Description != "yeni not" || e.Item.Referrer != "http://x/" {
		t.Fatalf("%+v", e.Item)
	}
	if _, err := h.m.Describe("yok", "x"); err == nil {
		t.Fatal("bilinmeyen kimlik hata vermeli")
	}
	h.m.Close()
	h.open()
	items, _, _ := h.m.List()
	if items[0].Description != "yeni not" || items[0].PageURL != "http://x/page?q=1" || items[0].Referrer != "http://x/" {
		t.Fatalf("yeniden yükleme: %+v", items[0])
	}
}

// Referer'siz eklenen öğede alan boş kalır ve JSON'a hiç yazılmaz (eski uygulama/eklenti ile uyumlu).
func TestInfoFieldsOmittedWhenEmpty(t *testing.T) {
	h := newHarness(t)
	a := h.add("b.bin", false)
	b, _ := json.Marshal(a)
	if strings.Contains(string(b), "referrer") || strings.Contains(string(b), "pageUrl") {
		t.Fatal(string(b))
	}
}

// item.describe sözleşme komutu: hedef "item" (ya da "id"), yanıt güncel öğe; açıklama alanı yoksa reddedilir.
func TestServerItemDescribe(t *testing.T) {
	sock, _, _ := startServer(t, &fakeBackend{})
	c := dial(t, sock)
	r := c.call(t, `{"id":1,"cmd":"item.describe","item":"d1","description":"not"}`)
	if r["ok"] != true || r["item"].(map[string]any)["description"] != "not" {
		t.Fatal(r)
	}
	if r := c.call(t, `{"id":2,"cmd":"item.describe","item":"d1","description":""}`); r["ok"] != true {
		t.Fatal("boş açıklama silmeye izin vermeli:", r)
	}
	if r := c.call(t, `{"id":3,"cmd":"item.describe","item":"d1"}`); r["ok"] != false {
		t.Fatal(r)
	}
	if r := c.call(t, `{"id":4,"cmd":"item.describe","item":"yok","description":"x"}`); r["ok"] != false || r["error"] != "indirme bulunamadı" {
		t.Fatal(r)
	}
}
