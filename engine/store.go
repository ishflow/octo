package engine

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Kalıcı durum: downloads.json, settings.json, queues.json (veri dizininde).
// Her değişiklikte geçici dosyaya yazılıp rename ile atomik olarak değiştirilir.

const (
	downloadsFile = "downloads.json"
	settingsFile  = "settings.json"
	queuesFile    = "queues.json"
	mainQueue     = "main"
)

// downloadsDoc: Headers burada kalır (yeniden başlatmada tarayıcı çerezleri gerekir), olaylara gitmez; Authorization hiç yazılmaz (Item.Authorization json:"-").
// QueueRuns: kaydedildiği anda kuyruğun yürüttüğü öğeler; çökmeden sonra bunlar "sırada"ya döner.
type downloadsDoc struct {
	NextID    int      `json:"nextId"`
	Items     []*Item  `json:"items"`
	QueueRuns []string `json:"queueRuns,omitempty"`
}

// defaultCaptureExts: klasik indirme yöneticilerinin varsayılan yakalama listesi.
var defaultCaptureExts = strings.Fields(strings.ToLower("3GP 7Z AAC ACE AIF APK ARJ ASF AVI BIN BZ2 EXE GZ GZIP IMG ISO LZH M4A M4V MKV MOV MP3 MP4 MPA MPE MPEG MPG MSI MSU OGG OGV PDF PLJ PPS PPT QT R0* R1* RA RAR RM RMVB SEA SIT SITX TAR TIF TIFF WAV WMA WMV Z ZIP"))

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

func defaultSettings(d Deps) Settings {
	return Settings{
		MaxConns: 8, DownloadDir: expandHome("~/Downloads"), Duplicate: "number", SpeedLimitKBps: 500,
		Categories: d.DefaultCategories(), CaptureExts: defaultCaptureExts,
		ExcludedSites: []string{}, Logins: []Login{},
	}
}

func defaultSchedule() Schedule {
	return Schedule{Start: "23:00", Stop: "07:30", Days: []int{1, 2, 3, 4, 5, 6, 7}, OnDone: "none"}
}

func defaultMainQueue() *Queue {
	return &Queue{ID: mainQueue, Name: "Ana kuyruk", AtOnce: 4, Items: []string{}, Schedule: defaultSchedule()}
}

// readJSON: dosya yoksa v'ye dokunmaz; bozuk dosya hata verir (kullanıcı verisinin üzerine yazmamak için).
func readJSON(dir, name string, v any) error {
	b, err := os.ReadFile(filepath.Join(dir, name))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(dir, name string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, name+".tmp*")
	if err != nil {
		return err
	}
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), filepath.Join(dir, name))
	}
	if err != nil {
		os.Remove(f.Name())
	}
	return err
}

// load verileri okur; eksik dosyalar varsayılanlarla dolar. İndirilmekte olanlar duraklatılmış olur.
func (m *Manager) load() error {
	var doc downloadsDoc
	if err := readJSON(m.dir, downloadsFile, &doc); err != nil {
		return err
	}
	m.items, m.nextID = doc.Items, doc.NextID
	scrubbed := false
	for _, it := range m.items {
		if it.State == StateDownloading { // kuyruğun işi kuyruğa, elle başlatılan duraklatılmışa
			it.State = StatePaused
			if slices.Contains(doc.QueueRuns, it.ID) {
				it.State = StateQueued
			}
		}
		it.Speed, it.ETA = 0, -1
		if h, auth := splitAuth(it.Headers); auth != "" { // eski kayıtta diske yazılmış kimlik: sil, yeniden giriş iste
			it.Headers, it.AuthNeeded, scrubbed = h, true, true
		}
	}
	if scrubbed {
		logErr("indirme kaydı", m.saveItems())
	}

	m.settings = defaultSettings(m.d)
	if err := readJSON(m.dir, settingsFile, &m.settings); err != nil {
		return err
	}
	m.settings.DownloadDir = expandHome(m.settings.DownloadDir)

	var qs []*Queue
	if err := readJSON(m.dir, queuesFile, &qs); err != nil {
		return err
	}
	m.queues = qs
	if m.queue(mainQueue) == nil {
		m.queues = append([]*Queue{defaultMainQueue()}, qs...)
	}
	for _, q := range m.queues {
		q.Running = false // yeniden başlatmada hiçbir kuyruk kendiliğinden sürmez
		if q.Items == nil {
			q.Items = []string{}
		}
	}
	return nil
}

func (m *Manager) saveItems() error {
	doc := downloadsDoc{NextID: m.nextID, Items: m.items}
	for id, r := range m.runs {
		if r.byQueue {
			doc.QueueRuns = append(doc.QueueRuns, id)
		}
	}
	return m.write(downloadsFile, doc)
}

func (m *Manager) saveSettings() error { return m.write(settingsFile, m.settings) }

func (m *Manager) saveQueues() error { return m.write(queuesFile, m.queues) }

// write: Block kipinde Octo klasörüne durum yazılmaz (eski servis kapanınca göç yapılabilsin).
func (m *Manager) write(name string, v any) error {
	if m.blocked != "" {
		return nil
	}
	return writeJSON(m.dir, name, v)
}
