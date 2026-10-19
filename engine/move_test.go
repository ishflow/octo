package engine

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(p string) string { b, _ := os.ReadFile(p); return string(b) }

// Bitmiş dosya başka klasöre yeni adla taşınır; öğe yeni yolu gösterir ve kaydedilir.
func TestMoveDoneFile(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.zip", true)
	h.job("a.zip").end(nil)
	h.waitState(it.ID, StateDone)
	writeFile(t, filepath.Join(h.dl, "a.zip"), "veri")
	to := t.TempDir()
	got, err := h.m.Move(it.ID, to, "b.zip")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "b.zip" || got.Dir != to || readFile(filepath.Join(to, "b.zip")) != "veri" {
		t.Fatalf("%+v", got)
	}
	if _, err := os.Lstat(filepath.Join(h.dl, "a.zip")); !os.IsNotExist(err) {
		t.Fatalf("eski dosya kaldı: %v", err)
	}
	h.m.Close()
	h.open()
	if s := h.item(it.ID); s.Name != "b.zip" || s.Dir != to {
		t.Fatalf("kaydedilmedi: %+v", s)
	}
}

// Var olan dosya ezilmez; iki dosya da yerinde kalır.
func TestMoveKeepsExistingFile(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.zip", true)
	h.job("a.zip").end(nil)
	h.waitState(it.ID, StateDone)
	writeFile(t, filepath.Join(h.dl, "a.zip"), "yeni")
	writeFile(t, filepath.Join(h.dl, "b.zip"), "eski")
	if _, err := h.m.Move(it.ID, h.dl, "b.zip"); err == nil || !strings.Contains(err.Error(), "zaten var") {
		t.Fatalf("Move=%v", err)
	}
	if readFile(filepath.Join(h.dl, "a.zip")) != "yeni" || readFile(filepath.Join(h.dl, "b.zip")) != "eski" || h.item(it.ID).Name != "a.zip" {
		t.Fatal("dosya ezildi ya da öğe değişti")
	}
}

// Yarım indirmede .part ve .part.json taşınır; devam yeni yoldan sürer.
func TestMovePausedCarriesPart(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.zip", false)
	old := filepath.Join(h.dl, "a.zip")
	writeFile(t, old+".part", "yarım")
	writeFile(t, old+".part.json", "{}")
	if _, err := h.m.Move(it.ID, h.dl, "b.zip"); err != nil {
		t.Fatal(err)
	}
	nu := filepath.Join(h.dl, "b.zip")
	if readFile(nu+".part") != "yarım" || readFile(nu+".part.json") != "{}" {
		t.Fatal(".part taşınmadı")
	}
	h.m.Resume([]string{it.ID})
	h.job("b.zip")
}

// Yarım video (akış) öğesinde parça klasörü ve .stream.json da taşınır; yoksa devam baştan iner, eski parçalar sahipsiz kalır.
func TestMovePausedStreamCarriesParts(t *testing.T) {
	h := newHarness(t)
	it := h.add("v.mp4", false)
	old := filepath.Join(h.dl, "v.mp4")
	writeFile(t, old+".stream.json", "{}")
	if err := os.Mkdir(old+".octo-parts", 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(old+".octo-parts", "0-0"), "parça")
	if _, err := h.m.Move(it.ID, h.dl, "w.mp4"); err != nil {
		t.Fatal(err)
	}
	nu := filepath.Join(h.dl, "w.mp4")
	if readFile(nu+".stream.json") != "{}" || readFile(filepath.Join(nu+".octo-parts", "0-0")) != "parça" {
		t.Fatal("akış parçaları taşınmadı")
	}
	if _, err := os.Lstat(old + ".octo-parts"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("eski parça klasörü kaldı")
	}
}

func TestMoveRejects(t *testing.T) {
	h := newHarness(t)
	run := h.add("a.zip", true)
	h.job("a.zip")
	if _, err := h.m.Move(run.ID, h.dl, "b.zip"); err == nil || !strings.Contains(err.Error(), "duraklatın") {
		t.Fatalf("çalışan: %v", err)
	}
	q := h.add("c.zip", false)
	for _, tc := range [][2]string{{h.dl, ""}, {"goreli", "d.zip"}, {h.dl, "a.zip"}} {
		if _, err := h.m.Move(q.ID, tc[0], tc[1]); err == nil {
			t.Fatalf("%v kabul edildi", tc)
		}
	}
	if _, err := h.m.Move("yok", h.dl, "d.zip"); err == nil {
		t.Fatal("bilinmeyen öğe")
	}
}

// Harf duyarsız diskte yalnız büyük/küçük harf değişimi "zaten var" sayılmaz.
func TestMoveCaseOnly(t *testing.T) {
	h := newHarness(t)
	it := h.add("a.zip", true)
	h.job("a.zip").end(nil)
	h.waitState(it.ID, StateDone)
	writeFile(t, filepath.Join(h.dl, "a.zip"), "veri")
	if _, err := os.Lstat(filepath.Join(h.dl, "A.ZIP")); err != nil {
		t.Skip("harf duyarlı disk")
	}
	if _, err := h.m.Move(it.ID, h.dl, "A.zip"); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(h.dl); len(es) != 1 || es[0].Name() != "A.zip" {
		t.Fatalf("%v", es)
	}
}
