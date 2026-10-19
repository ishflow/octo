package engine

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

var errExists = errors.New("bu adda bir dosya zaten var")

// Move öğenin dosyasını taşır ya da yeniden adlandırır. Bitmemiş öğede
// geçici dosyaları taşınır (durum dosyası yolu tutmaz), indirme yeni yoldan sürer. Var olan dosya ezilmez.
func (m *Manager) Move(id, dir, name string) (Item, error) {
	if msg := m.blockedMsg(); msg != "" {
		return Item{}, errors.New(msg)
	}
	name, err := cleanName(name)
	if err != nil {
		return Item{}, err
	}
	name = sanitizeName(name)
	if dir = expandHome(dir); !filepath.IsAbs(dir) {
		return Item{}, errors.New("geçersiz klasör")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	it := m.item(id)
	switch {
	case it == nil:
		return Item{}, errors.New("indirme bulunamadı")
	case m.runs[id] != nil:
		return Item{}, errors.New("önce indirmeyi duraklatın")
	}
	from, to := itemPath(it), filepath.Join(filepath.Clean(dir), name)
	if from == to {
		return public(it), nil
	}
	for _, o := range m.items {
		if o != it && o.State != StateDone && itemPath(o) == to {
			return Item{}, errors.New("bu ad listedeki başka bir indirmede kullanılıyor")
		}
	}
	switch _, serr := os.Lstat(to); {
	case it.State == StateDone:
		if err = moveFile(from, to); errors.Is(err, errNoSource) {
			return Item{}, errors.New("dosya bulunamadı")
		}
	case serr == nil: // bitince bu ada konacak
		err = errExists
	default:
		err = moveSidecars(from, to)
	}
	if err != nil && !errors.Is(err, errNoSource) { // hiç başlamamış öğede .part yok: yalnız yol değişir
		return Item{}, err
	}
	it.Dir, it.Name = filepath.Dir(to), name
	logErr("indirme kaydı", m.saveItems())
	m.emitItem(it)
	return public(it), nil
}

var errNoSource = errors.New("kaynak yok")

// moveSidecars yarım öğenin geçici dosyalarını taşır (sıradan: .part.json, .part; akış: .stream.json, .octo-parts).
// Biri taşınamazsa taşınanlar geri alınır (yarım taşıma bırakma).
func moveSidecars(from, to string) error {
	var done []string
	for _, s := range []string{".part.json", ".stream.json", ".octo-parts", ".part"} {
		switch err := moveFile(from+s, to+s); {
		case err == nil:
			done = append(done, s)
		case !errors.Is(err, errNoSource):
			for _, d := range done {
				os.Rename(to+d, from+d)
			}
			return err
		}
	}
	return nil
}

// moveFile from'u to'ya ezmeden taşır (place gibi: Link hedef varsa EEXIST verir). Hatalar Türkçe.
func moveFile(from, to string) error {
	if _, err := os.Lstat(from); errors.Is(err, os.ErrNotExist) {
		return errNoSource
	}
	err := os.Link(from, to)
	if err == nil {
		os.Remove(from)
		return nil
	}
	if a, aerr := os.Lstat(from); errors.Is(err, os.ErrExist) && aerr == nil {
		if b, berr := os.Lstat(to); berr == nil && os.SameFile(a, b) { // büyük/küçük harf değişimi (APFS harf duyarsız)
			err = os.Rename(from, to)
		}
	}
	if err != nil && !errors.Is(err, os.ErrExist) && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, syscall.EXDEV) {
		// shortcut: sabit bağ desteklemeyen birimde (exFAT) denetle-taşı; arada küçük yarış kalır (place ile aynı)
		if _, serr := os.Lstat(to); serr == nil {
			return errExists
		}
		err = os.Rename(from, to)
	}
	switch {
	case err == nil:
		return nil
	case errors.Is(err, os.ErrExist):
		return errExists
	case errors.Is(err, os.ErrNotExist):
		return errors.New("klasör bulunamadı")
	case errors.Is(err, syscall.EXDEV):
		// shortcut: başka diske kopyalayıp taşıma yok (kilit altında dakikalar sürer); gerekirse arka planda kopya
		return errors.New("başka bir diske taşıma desteklenmiyor; aynı disk içinde bir klasör seçin")
	}
	return errors.New("dosya taşınamadı: " + err.Error())
}
