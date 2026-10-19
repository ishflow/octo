package engine

import (
	"encoding/json"
	"time"
)

// Servis sözleşmesinin JSON tipleri (plan: "Shared contract"). Uygulama ve eklenti bunlara göre konuşur;
// alan adlarını değiştirme. Boş diziler null değil [] olarak yazılmalı (Swift tarafı yine de tolerans gösterir).

// BytesPerSec hızdır (bayt/sn); JSON'da tam sayı gider (Swift Int64 kesirli sayıyı çözemez).
type BytesPerSec int64

// UnmarshalJSON önceki sürümün kaydettiği kesirli değeri de okur.
func (b *BytesPerSec) UnmarshalJSON(p []byte) error {
	var f float64
	if err := json.Unmarshal(p, &f); err != nil {
		return err
	}
	*b = BytesPerSec(f)
	return nil
}

type ItemState string

const (
	StateQueued      ItemState = "queued"
	StateDownloading ItemState = "downloading"
	StatePaused      ItemState = "paused"
	StateDone        ItemState = "done"
	StateError       ItemState = "error"
)

// Conn bir bağlantının aralığıdır; End dahil (engine chunk ile aynı).
type Conn struct {
	Start int64 `json:"start"`
	Pos   int64 `json:"pos"`
	End   int64 `json:"end"`
	// Status: bağlantının son olayı ("Zaman aşımı, yeniden bağlanıyor", "Yeni parça aldı"); boş/eksik = veri alınıyor.
	Status string `json:"status,omitempty"`
}

type Item struct {
	ID          string      `json:"id"`
	URL         string      `json:"url"`
	Name        string      `json:"name"`
	Dir         string      `json:"dir"`
	Category    string      `json:"category"`
	Queue       string      `json:"queue"`
	State       ItemState   `json:"state"`
	Size        int64       `json:"size"` // bilinmiyorsa -1
	Got         int64       `json:"got"`
	Speed       BytesPerSec `json:"speed"`
	ETA         int64       `json:"eta"` // saniye, bilinmiyorsa -1
	Resumable   bool        `json:"resumable"`
	Error       string      `json:"error"`
	Description string      `json:"description"`
	// RestartReason: devam edilemedi, indirme 0'dan yeniden başladı (spec Özellik 1: kullanıcıya söylenir).
	RestartReason string            `json:"restartReason"`
	Added         time.Time         `json:"added"`
	Finished      *time.Time        `json:"finished"`
	LastTry       *time.Time        `json:"lastTry"`
	MaxConns      int               `json:"maxConns"`
	Conns         []Conn            `json:"conns"`
	Headers       map[string]string `json:"headers,omitempty"` // tarayıcıdan gelen Referer/Cookie/User-Agent; yalnız servis içinde kalır
	// Authorization: uygulamadan gelen tek seferlik Basic kimliği; yalnız bellekte durur, diske/olaylara yazılmaz.
	// AuthNeeded (diskte): kimlikle eklendi; motor yeniden başlarsa bellek boşalır, öğe "Giriş gerekli" hatasıyla durur.
	Authorization string `json:"-"`
	AuthNeeded    bool   `json:"authNeeded,omitempty"`
	// com.apple.quarantine yazılamadı; uygulama "Bitince aç"ı atlar. Eksik = false (eski motor).
	QuarantineFailed bool `json:"quarantineFailed,omitempty"`
	// Tarayıcıdan aynı ad ve boyutla geldi: bu öğe yarım RefreshOf öğesi için "adresi yenile" önerisidir,
	// kendiliğinden başlamaz; başlatılınca düşer. Eksik = öneri değil (eski motor).
	RefreshOf string `json:"refreshOf,omitempty"`
	// Referrer (Yönlendiren): gönderilen Referer başlığı; dışarı giden kopyada Headers'tan doldurulur, diske yazılmaz.
	Referrer string `json:"referrer,omitempty"`
	PageURL  string `json:"pageUrl,omitempty"` // Kaynak sayfa: indirmenin geldiği sayfanın tam adresi (eklenti)

	SpeedLimitKBps int64 `json:"speedLimitKBps"` // dosya başına hız sınırı; 0 = yok (eski kayıtlarda alan yok)

	// Stream: HLS/DASH video öğesi (VG1 §4); nil = sıradan dosya (eski motor). Note: bitişte kullanıcıya not.
	Stream *StreamSpec `json:"stream,omitempty"`
	Note   string      `json:"note,omitempty"`
}

// StreamSpec akış öğesinin seçimi ve ilerlemesidir. Eksik alan = eski davranış.
type StreamSpec struct {
	Kind     string `json:"kind"`               // "hls" | "dash"
	Video    string `json:"video,omitempty"`    // "v2" (HLS sıra) | Representation@id; boş = medya listesi / tek
	Audio    string `json:"audio,omitempty"`    // "a0" | Representation@id; boş = gömülü ya da yok
	Label    string `json:"label,omitempty"`    // "1080p" (gösterim)
	Segments int    `json:"segments,omitempty"` // toplam parça (tüm izler)
	SegDone  int    `json:"segDone,omitempty"`
	Phase    string `json:"phase,omitempty"` // "" | "merging"
}

type StreamVariant struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	Codecs    string `json:"codecs"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	Bandwidth int64  `json:"bandwidth"`
	Audio     string `json:"audio"`
}

type StreamAudio struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Lang    string `json:"lang"`
	Default bool   `json:"default"`
}

// StreamInfo stream.probe yanıtıdır. Variants yüksekten düşüğe; boş dizi null değil [] yazılır.
type StreamInfo struct {
	Kind      string          `json:"kind"`
	Container string          `json:"container"` // "ts" | "mp4"
	Live      bool            `json:"live"`
	Protected bool            `json:"protected"`
	Duration  float64         `json:"duration"` // sn, bilinmiyorsa 0
	Variants  []StreamVariant `json:"variants"`
	Audios    []StreamAudio   `json:"audios"`
}

type Category struct {
	Name string   `json:"name"`
	Exts []string `json:"exts"`
	Dir  string   `json:"dir"`
}

type Login struct {
	Host string `json:"host"`
	User string `json:"user"`
}

// HostConn: Host'a ("*.example.com" alt alanları da tutar) en fazla Conns bağlantı.
type HostConn struct {
	Host  string `json:"host"`
	Conns int    `json:"conns"`
}

type Settings struct {
	MaxConns        int        `json:"maxConns"`
	DownloadDir     string     `json:"downloadDir"`
	CategoryFolders bool       `json:"categoryFolders"`
	Duplicate       string     `json:"duplicate"` // number | ask | overwrite
	SpeedLimitOn    bool       `json:"speedLimitOn"`
	SpeedLimitKBps  int64      `json:"speedLimitKBps"`
	Categories      []Category `json:"categories"`
	CaptureExts     []string   `json:"captureExts"`
	ExcludedSites   []string   `json:"excludedSites"`
	CaptureOff      bool       `json:"captureOff"`   // true: eklenti hiçbir indirmeyi yakalamaz
	CaptureMinKB    int64      `json:"captureMinKB"` // bu boyuttan (KB) küçük indirmeler tarayıcıda kalır; 0 = eşik yok
	Logins          []Login    `json:"logins"`
	HostConns       []HostConn `json:"hostConns"`     // sunucu istisnası; yok = herkese MaxConns
	UseServerDate   bool       `json:"useServerDate"` // biten dosyanın tarihi sunucunun Last-Modified'ı olsun
	UserAgent       string     `json:"userAgent"`     // boş = varsayılan (octo/1.0); tarayıcıdan gelen indirmelerde tarayıcınki kullanılır
	// HideVideoButton: eklentinin video düğmesi kapalı; sıfır değer = açık (eski kayıt uyumlu).
	HideVideoButton bool `json:"hideVideoButton"`
}

type Schedule struct {
	StartOn bool   `json:"startOn"`
	Start   string `json:"start"` // "23:00"
	StopOn  bool   `json:"stopOn"`
	Stop    string `json:"stop"`
	Days    []int  `json:"days"`   // 1=Pazartesi … 7=Pazar
	OnDone  string `json:"onDone"` // none | open | sleep | shutdown
}

type Queue struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	AtOnce   int      `json:"atOnce"`
	Retries  int      `json:"retries"` // 0 = sınırsız
	Items    []string `json:"items"`
	Running  bool     `json:"running"`
	Schedule Schedule `json:"schedule"`
}

type ProgressItem struct {
	ID    string      `json:"id"`
	State ItemState   `json:"state"`
	Got   int64       `json:"got"`
	Size  int64       `json:"size"`
	Speed BytesPerSec `json:"speed"`
	ETA   int64       `json:"eta"`
	Conns []Conn      `json:"conns"`
	// Akış öğesi: parça ilerlemesi ve evre ("merging"); sıradan dosyada eksik.
	SegDone  int    `json:"segDone,omitempty"`
	Segments int    `json:"segments,omitempty"`
	Phase    string `json:"phase,omitempty"`
}

// Event abonelere giden olaydır: progress | item | removed | queues | action.
type Event struct {
	Event   string         `json:"event"`
	Items   []ProgressItem `json:"items,omitempty"`
	Item    *Item          `json:"item,omitempty"`
	IDs     []string       `json:"ids,omitempty"`
	Queues  []Queue        `json:"queues,omitempty"`
	Action  string         `json:"action,omitempty"`
	Seconds int            `json:"seconds,omitempty"`
}

type AddArgs struct {
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Dir         string            `json:"dir,omitempty"`
	Name        string            `json:"name,omitempty"`
	Category    string            `json:"category,omitempty"`
	Queue       string            `json:"queue,omitempty"`
	Description string            `json:"description,omitempty"`
	PageURL     string            `json:"pageUrl,omitempty"`
	Start       bool              `json:"start"`
	Size        int64             `json:"size,omitempty"`   // tarayıcının bildirdiği boyut; aynı yoldaki yarım öğeyle eşleşirse öneri olur
	Stream      *StreamSpec       `json:"stream,omitempty"` // URL bir HLS/DASH manifest'idir; kind/video/audio/label
}
