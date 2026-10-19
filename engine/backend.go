package engine

// ProbeResult HEAD/Range ile sorgulanan dosya bilgisidir.
type ProbeResult struct {
	Name      string `json:"name"`
	Size      int64  `json:"size"`
	Resumable bool   `json:"resumable"`
	Kind      string `json:"kind,omitempty"` // "hls" | "dash": adres bir akış manifest'i, uygulama stream.probe'a geçer
}

var _ Backend = (*Manager)(nil)

// Backend sunucunun konuştuğu yöneticidir (Manager bunu sağlar).
type Backend interface {
	Add(AddArgs) (Item, error)
	Probe(url string, headers map[string]string) (ProbeResult, error)
	ProbeStream(url string, headers map[string]string, page string) (StreamInfo, error) // manifest'i çözer; canlı/korumalı da bilgi döner
	Pause(ids []string)
	Resume(ids []string)
	Redownload(ids []string)
	Remove(ids []string, deleteFile bool) error // dosyası Çöp'e taşınamayan öğe listede kalır, hata döner
	Refresh(id, url string) (Item, error)
	Move(id, dir, name string) (Item, error)          // "Taşı veya yeniden adlandır"
	SetItemLimit(id string, kbps int64) (Item, error) // dosya başına hız sınırı; 0 = kapalı
	Describe(id, description string) (Item, error)
	List() ([]Item, []Queue, Settings)
	SetSettings(Settings) error
	SaveQueue(Queue) error
	DeleteQueue(id string) error
	StartQueue(id string)
	StopQueue(id string)
	MoveToQueue(ids []string, queue string) error
	CancelAction()
	PendingAction() (action string, seconds int) // süren uyku/kapatma geri sayımı; yoksa ""
	// Subscribe olay kanalı verir. Gönderimler bloklamamalı (dolu kanalda olay atılır); unsub kanalı kapatmalıdır,
	// sunucu bağlantı bitince unsub çağırır.
	Subscribe() (<-chan Event, func())
}
