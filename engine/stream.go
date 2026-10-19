package engine

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Akış (HLS/DASH) öğeleri: manifest motorda çözülür, parçalar indirilip tek dosyada birleştirilir (VG1 §3).

const (
	maxManifest = 8 << 20 // manifest gövdesi sınırı
	maxSegments = 50000   // iz başına parça sınırı
)

// Kullanıcıya giden sabit metinler (eklenti ve uygulama bunlarla durum gösterir).
const (
	errProtected = "Bu video korumalı (DRM), indirilemez"
	errLive      = "Canlı yayın indirilemez"
	errExpired   = "Akış adresinin süresi dolmuş olabilir. Sayfayı yenileyip videoyu yeniden ekleyin ya da Adresi yenile'yi kullanın."
	streamRedo   = "Akış değişti, baştan indiriliyor"
)

var (
	errNotManifest = errors.New("Adres geçerli bir video akışı döndürmedi")
	errTooManySegs = errors.New("Akışta çok fazla parça var")
	errSegURL      = errors.New("Akışta geçersiz parça adresi var")
	errPrivateSeg  = errors.New("Akış yerel ağdaki bir adrese yönlendiriyor; güvenlik için indirilmedi")
	errNoVariant   = errors.New("Seçilen kalite akışta yok")
	errNoAudio     = errors.New("Seçilen ses akışta yok")
	errTSAudio     = errors.New("Ayrı ses izli TS akışı desteklenmiyor")
)

// segRetryDelay: parça yeniden denemesinin ilk beklemesi (1-2-4 sn). Test için değiştirilebilir.
var segRetryDelay = time.Second

const segRetries = 3

// segment bir parçanın adresidir; Range "" = tüm gövde, yoksa "bytes=a-b".
type segment struct {
	URL   string
	Range string
}

// track bir izin init + parça listesidir; Init nil ise parçalar tek başına tam dosyadır.
type track struct {
	Init *segment
	Segs []segment
}

type streamPlan struct {
	Tracks    []track // görüntü (ya da tek iz), varsa ayrı ses
	Container string  // "ts" | "mp4"
	Label     string
	AudioOnly bool
}

// Segments init dahil indirilecek parça sayısıdır.
func (p streamPlan) Segments() int {
	n := 0
	for _, t := range p.Tracks {
		n += len(t.Segs)
		if t.Init != nil {
			n++
		}
	}
	return n
}

// fetchFunc manifest gövdesini ve yönlendirme sonrası adresini verir.
type fetchFunc func(u string) ([]byte, *url.URL, error)

// resolveRef parça/alt liste adresini base'e göre çözer: yalnız http/https; dış ağdaki manifest yerel ağ
// adresine (IP harfi ya da localhost) yönlendiremez (Chrome PNA benzeri; DNS çözülmez).
func resolveRef(base *url.URL, ref string) (string, error) {
	r, err := base.Parse(strings.TrimSpace(ref))
	if err != nil || (r.Scheme != "http" && r.Scheme != "https") || r.Host == "" {
		return "", errSegURL
	}
	if localHost(r.Hostname()) && !localHost(base.Hostname()) {
		return "", errPrivateSeg
	}
	return r.String(), nil
}

func localHost(h string) bool {
	h = strings.ToLower(h)
	if h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	a, err := netip.ParseAddr(h)
	return err == nil && localIP(a)
}

var (
	thisNet = netip.MustParsePrefix("0.0.0.0/8")
	cgnat   = netip.MustParsePrefix("100.64.0.0/10") // RFC 6598 (Tailscale)
	nat64   = netip.MustParsePrefix("64:ff9b::/96")
)

// localIP yerel ağ adresidir: loopback, özel (ULA dahil), link-local, multicast, 0/8, 100.64/10 ve NAT64
// içine gömülü yerel IPv4. Test için değiştirilebilir.
var localIP = func(a netip.Addr) bool {
	a = a.Unmap().WithZone("")
	if nat64.Contains(a) {
		b := a.As16()
		a = netip.AddrFrom4([4]byte(b[12:]))
	}
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() ||
		thisNet.Contains(a) || cgnat.Contains(a)
}

// localGuard yerel ağ reddini bağlantı anında, çözülen IP'de uygular (VGR M1): yönlendirme, DNS adı (rebinding
// dahil) ve sayısal IP biçimleri (2130706433, 127.1) aynı denetimden geçer. İşin ilk bağlantısı manifest'edir;
// o yerel ağdaysa iş serbesttir (manifest yerelse serbest kuralı), değilse hiçbir istek yerel ağa bağlanamaz.
// shortcut: proxy üzerinden giden isteklerde hedef IP görülmez, yalnız resolveRef'in adres denetimi kalır.
type localGuard struct {
	mu             sync.Mutex
	decided, local bool
	proxies        sync.Map // proxy host:port'ları
}

func (g *localGuard) control(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	g.mu.Lock()
	defer g.mu.Unlock()
	if err == nil && g.decided && !g.local && localIP(ap.Addr()) {
		return errPrivateSeg
	}
	return nil
}

// connected kararı kurulan ilk bağlantıya göre verir (denenen ama kurulamayan adres karar vermez).
func (g *localGuard) connected(c net.Conn) {
	ap, err := netip.ParseAddrPort(c.RemoteAddr().String())
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.decided && err == nil {
		g.decided, g.local = true, localIP(ap.Addr())
	}
}

// pageLocal Referer'deki sayfa yerel ağda mı: IP harfi, localhost ya da bütün adresleri yerel olan ad. Çözülemezse dışarıda.
// Adreslerden biri dışsa dışarıda: saldırgan kendi adının kaydına yerel bir adres ekleyerek sayfasını yerel gösteremez.
// shortcut: zamanlı DNS rebinding (sayfa açıldıktan sonra yalnız yerel adres dönen ad) bunu yine aşar; o durumda etki çerezsiz tek GET.
func pageLocal(ctx context.Context, ref string) bool {
	u, err := url.Parse(ref)
	if err != nil || u.Hostname() == "" {
		return false
	}
	if localHost(u.Hostname()) {
		return true
	}
	ips, err := pageLookup(ctx, u.Hostname())
	return err == nil && len(ips) > 0 && !slices.ContainsFunc(ips, func(a netip.Addr) bool { return !localIP(a) })
}

// pageLookup sayfa adını çözer (test için değiştirilebilir).
var pageLookup = func(ctx context.Context, host string) ([]netip.Addr, error) {
	return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
}

// streamClient akış işinin istemcisidir: newClient + bağlantı anında yerel ağ denetimi. ref (Referer, yoksa sayfa) dış bir
// sayfaysa iş baştan "yerel değil" sayılır: sayfanın seçtiği yerel manifest'e de istek gitmez (V3s M1).
// İkisi de yoksa (eski öğe, eski eklenti) ilk bağlantı karar verir.
func streamClient(ref string) *http.Client {
	c := newClient()
	tr := c.Transport.(*http.Transport)
	g := &localGuard{}
	var page sync.Once // ad çözümü ilk bağlantıda, işin kendi bağlamında
	proxy := tr.Proxy
	tr.Proxy = func(r *http.Request) (*url.URL, error) {
		u, err := proxy(r)
		if u != nil {
			port := cmpOr(u.Port(), map[string]string{"http": "80", "https": "443", "socks5": "1080", "socks5h": "1080"}[u.Scheme])
			g.proxies.Store(net.JoinHostPort(u.Hostname(), port), true)
		}
		return u, err
	}
	plain, guarded := newDialer(), newDialer()
	guarded.Control = g.control
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if _, ok := g.proxies.Load(addr); ok {
			return plain.DialContext(ctx, network, addr)
		}
		page.Do(func() {
			if ref != "" && !pageLocal(ctx, ref) {
				g.mu.Lock()
				g.decided = true
				g.mu.Unlock()
			}
		})
		conn, err := guarded.DialContext(ctx, network, addr)
		if err == nil {
			g.connected(conn)
		}
		return conn, err
	}
	return c
}

// manifestTimeout: bir manifest çekmenin (ve stream.probe'un) toplam süresi. Damlatan sunucu probe'u native
// host'un 30 sn'sinden uzun tutarsa eklenti "eklemedi" der, motor sonradan ekler: öğe ikilenir (VGR M6).
// Test için değiştirilebilir.
var manifestTimeout = 20 * time.Second

// mediaKind yanıt türünden (yoksa uzantıdan) akış manifest'ini tanır: "hls" | "dash" | "".
func mediaKind(contentType, path string) string {
	mt, _, _ := mime.ParseMediaType(contentType)
	switch strings.ToLower(mt) {
	case "application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl":
		return "hls"
	case "application/dash+xml":
		return "dash"
	case "", "application/octet-stream", "binary/octet-stream", "text/plain":
		switch strings.ToLower(filepath.Ext(path)) {
		case ".m3u8":
			return "hls"
		case ".mpd":
			return "dash"
		}
	}
	return ""
}

// ProbeStream manifest'i öğenin başlıklarıyla çeker ve çözer (Deps.ProbeStream, stream.probe).
func ProbeStream(ctx context.Context, u string, h http.Header, page string, auth AuthFunc) (StreamInfo, error) {
	mu, err := url.Parse(u)
	if err != nil {
		return StreamInfo{}, errSegURL
	}
	hh := h.Clone()
	if hh == nil {
		hh = http.Header{}
	}
	j := &StreamJob{r: Request{Auth: auth}, manifest: mu, header: hh, client: streamClient(cmpOr(hh.Get("Referer"), page))}
	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()
	return probeStream(u, fetchWith(ctx, j.get))
}

func manifestKind(body []byte) string {
	b := bytes.TrimSpace(bytes.TrimPrefix(body, []byte("\xef\xbb\xbf")))
	switch {
	case bytes.HasPrefix(b, []byte("#EXTM3U")):
		return "hls"
	case bytes.Contains(b[:min(len(b), 4096)], []byte("<MPD")):
		return "dash"
	}
	return ""
}

// byQuality yüksekten düşüğe (çözünürlük, sonra bant genişliği).
func byQuality(h1 int, b1 int64, h2 int, b2 int64) int {
	if h1 != h2 {
		return h2 - h1
	}
	switch {
	case b1 > b2:
		return -1
	case b1 < b2:
		return 1
	}
	return 0
}

func sortedHLS(vs []hlsVariant) []hlsVariant {
	out := slices.Clone(vs)
	slices.SortStableFunc(out, func(a, b hlsVariant) int { return byQuality(a.Height, a.Bandwidth, b.Height, b.Bandwidth) })
	return out
}

func sortedMPD(rs []mpdRepr) []mpdRepr {
	out := slices.Clone(rs)
	slices.SortStableFunc(out, func(a, b mpdRepr) int { return byQuality(a.Height, a.Bandwidth, b.Height, b.Bandwidth) })
	return out
}

// groupAudios varyantın AUDIO grubundaki URI'li sesler.
func groupAudios(d *hlsDoc, group string) []hlsAudio {
	var out []hlsAudio
	for _, a := range d.Audios {
		if group != "" && a.Group == group {
			out = append(out, a)
		}
	}
	return out
}

// pickAudio: id verilmişse o (grupta yoksa tüm listede), yoksa grubun DEFAULT'u, o da yoksa ilki.
func pickAudio(d *hlsDoc, group, id string) (hlsAudio, error) {
	g := groupAudios(d, group)
	if id != "" {
		for _, a := range append(g, d.Audios...) {
			if a.ID == id {
				return a, nil
			}
		}
		return hlsAudio{}, errNoAudio
	}
	for _, a := range g {
		if a.Default {
			return a, nil
		}
	}
	return g[0], nil
}

func defaultDASHAudio(d *mpdDoc) string {
	best := ""
	var bw int64 = -1
	for _, a := range d.Audio {
		if a.Bandwidth > bw {
			best, bw = a.ID, a.Bandwidth
		}
	}
	return best
}

func fetchMedia(u string, fetch fetchFunc) (*hlsDoc, error) {
	body, base, err := fetch(u)
	if err != nil {
		return nil, err
	}
	d, err := ParseHLS(body, base)
	if err != nil {
		return nil, err
	}
	if d.Master {
		return nil, errNotManifest
	}
	return d, nil
}

// probeStream manifest'i çözer: kalite/ses listesi, kap, canlı/korumalı (stream.probe). Canlı ve korumalı
// akış hata değildir; eklenti durumu gösterir.
func probeStream(u string, fetch fetchFunc) (StreamInfo, error) {
	body, base, err := fetch(u)
	if err != nil {
		return StreamInfo{}, err
	}
	info := StreamInfo{Variants: []StreamVariant{}, Audios: []StreamAudio{}}
	switch manifestKind(body) {
	case "hls":
		info.Kind = "hls"
		d, err := ParseHLS(body, base)
		if err != nil {
			return StreamInfo{}, err
		}
		media := d
		if d.Master {
			vs := sortedHLS(d.Variants)
			for _, v := range vs {
				sv := v.StreamVariant
				if g := groupAudios(d, v.Group); len(g) > 0 {
					a, _ := pickAudio(d, v.Group, "")
					sv.Audio = a.ID
				}
				info.Variants = append(info.Variants, sv)
			}
			for _, a := range d.Audios {
				info.Audios = append(info.Audios, a.StreamAudio)
			}
			if d.Protected {
				return info, nil
			}
			if media, err = fetchMedia(vs[0].URI, fetch); err != nil {
				return StreamInfo{}, err
			}
		} else {
			info.Variants = append(info.Variants, StreamVariant{Label: "Video"})
		}
		info.Container, info.Live, info.Duration = media.Container, media.Live, media.Duration
		info.Protected = d.Protected || media.Protected
	case "dash":
		info.Kind, info.Container = "dash", "mp4"
		d, err := ParseMPD(body, base)
		if err != nil {
			return StreamInfo{}, err
		}
		if !d.Live && !d.Protected { // varsayılan seçimin parça planı: hatalar eklemeden önce görünsün
			if _, err := pickDASH(d, StreamSpec{}); err != nil {
				return StreamInfo{}, err
			}
		}
		info.Live, info.Protected, info.Duration = d.Live, d.Protected, d.Duration
		def := defaultDASHAudio(d)
		for _, r := range sortedMPD(d.Video) {
			info.Variants = append(info.Variants, StreamVariant{ID: r.ID, Label: variantLabel(r.Height, r.Bandwidth), Codecs: r.Codecs,
				Width: r.Width, Height: r.Height, Bandwidth: r.Bandwidth, Audio: def})
		}
		for _, a := range d.Audio {
			info.Audios = append(info.Audios, StreamAudio{ID: a.ID, Name: first(a.Name, a.Lang, a.ID), Lang: a.Lang, Default: a.ID == def})
		}
	default:
		return StreamInfo{}, errNotManifest
	}
	return info, nil
}

// planStream seçime göre iz başına init + parça listesini çıkarır; canlı/korumalı akış hatadır.
func planStream(u string, spec StreamSpec, fetch fetchFunc) (streamPlan, error) {
	body, base, err := fetch(u)
	if err != nil {
		return streamPlan{}, err
	}
	switch manifestKind(body) {
	case "hls":
		return planHLS(body, base, spec, fetch)
	case "dash":
		return planDASH(body, base, spec)
	}
	return streamPlan{}, errNotManifest
}

func usable(d *hlsDoc) error {
	switch {
	case d.Protected:
		return errors.New(errProtected)
	case d.Live:
		return errors.New(errLive)
	case len(d.Segs) == 0:
		return errNotManifest
	}
	return nil
}

func planHLS(body []byte, base *url.URL, spec StreamSpec, fetch fetchFunc) (streamPlan, error) {
	d, err := ParseHLS(body, base)
	if err != nil {
		return streamPlan{}, err
	}
	if !d.Master {
		if err := usable(d); err != nil {
			return streamPlan{}, err
		}
		return streamPlan{Tracks: []track{{d.Init, d.Segs}}, Container: d.Container, Label: "Video"}, nil
	}
	if d.Protected {
		return streamPlan{}, errors.New(errProtected)
	}
	vs := sortedHLS(d.Variants)
	i := 0
	if spec.Video != "" {
		if i = slices.IndexFunc(vs, func(v hlsVariant) bool { return v.ID == spec.Video }); i < 0 {
			return streamPlan{}, errNoVariant
		}
	}
	v := vs[i]
	media, err := fetchMedia(v.URI, fetch)
	if err != nil {
		return streamPlan{}, err
	}
	if err := usable(media); err != nil {
		return streamPlan{}, err
	}
	p := streamPlan{Tracks: []track{{media.Init, media.Segs}}, Container: media.Container, Label: v.Label}
	if len(groupAudios(d, v.Group)) == 0 {
		return p, nil
	}
	if media.Container == "ts" { // TS→MP4 yeniden paketleme yok: gömülü sesli varyanta düş
		for _, alt := range vs {
			if len(groupAudios(d, alt.Group)) > 0 {
				continue
			}
			m, err := fetchMedia(alt.URI, fetch)
			if err != nil {
				return streamPlan{}, err
			}
			if err := usable(m); err != nil {
				return streamPlan{}, err
			}
			return streamPlan{Tracks: []track{{m.Init, m.Segs}}, Container: m.Container, Label: alt.Label}, nil
		}
		return streamPlan{}, errTSAudio
	}
	a, err := pickAudio(d, v.Group, spec.Audio)
	if err != nil {
		return streamPlan{}, err
	}
	am, err := fetchMedia(a.URI, fetch)
	if err != nil {
		return streamPlan{}, err
	}
	if err := usable(am); err != nil {
		return streamPlan{}, err
	}
	if am.Container != "mp4" {
		return streamPlan{}, errTSAudio
	}
	p.Tracks = append(p.Tracks, track{am.Init, am.Segs})
	return p, nil
}

func planDASH(body []byte, base *url.URL, spec StreamSpec) (streamPlan, error) {
	d, err := ParseMPD(body, base)
	if err != nil {
		return streamPlan{}, err
	}
	return pickDASH(d, spec)
}

// pickDASH seçilen temsillerin parça listesini açar (yalnız onların: VGR H1).
func pickDASH(d *mpdDoc, spec StreamSpec) (streamPlan, error) {
	if d.Protected {
		return streamPlan{}, errors.New(errProtected)
	}
	if d.Live {
		return streamPlan{}, errors.New(errLive)
	}
	p := streamPlan{Container: "mp4", AudioOnly: len(d.Video) == 0}
	if vs := sortedMPD(d.Video); len(vs) > 0 {
		i := 0
		if spec.Video != "" {
			if i = slices.IndexFunc(vs, func(r mpdRepr) bool { return r.ID == spec.Video }); i < 0 {
				return streamPlan{}, errNoVariant
			}
		}
		if err := vs[i].load(); err != nil {
			return streamPlan{}, err
		}
		p.Tracks, p.Label = []track{{vs[i].Init, vs[i].Segs}}, variantLabel(vs[i].Height, vs[i].Bandwidth)
	} else if spec.Video != "" {
		return streamPlan{}, errNoVariant
	}
	if len(d.Audio) > 0 {
		id := cmpOr(spec.Audio, defaultDASHAudio(d))
		i := slices.IndexFunc(d.Audio, func(r mpdRepr) bool { return r.ID == id })
		if i < 0 {
			return streamPlan{}, errNoAudio
		}
		if err := d.Audio[i].load(); err != nil {
			return streamPlan{}, err
		}
		p.Tracks = append(p.Tracks, track{d.Audio[i].Init, d.Audio[i].Segs})
		if p.Label == "" {
			p.Label = "Ses"
		}
	} else if spec.Audio != "" {
		return streamPlan{}, errNoAudio
	}
	return p, nil
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// ---- indirme ----

// StreamProgress akış işinin Snapshot dışındaki ilerlemesidir (Manager applySnap'te okur).
type StreamProgress struct {
	SegDone, Segments int
	Phase             string // "" | "merging"
	ETA               int64  // sn, bilinmiyorsa -1 (parça hızından)
	Note              string // bitişte kullanıcıya not (ayrı kalan ses)
}

// streamState: Path+".stream.json"; devam aynı seçimi ve iz başına parça sayısını karşılaştırır.
type streamState struct {
	URL      string       `json:"url"`
	Spec     StreamSpec   `json:"spec"`
	Tracks   []stateTrack `json:"tracks"`
	SegCount int          `json:"segCount"`
}

type stateTrack struct {
	N    int    `json:"n"`
	Init bool   `json:"init"`
	Key  string `json:"key,omitempty"` // trackKey; eski durum dosyasında yok: yalnız N/Init karşılaştırılır
}

// trackKey parça adreslerinin (sorgusuz yol + aralık) özetidir: imzası yenilenen aynı akış eşleşir, sırası
// değişen başka varyant ya da aynı uzunlukta başka video eşleşmez (VGR M5).
func trackKey(t track) string {
	segs := t.Segs
	if t.Init != nil {
		segs = append([]segment{*t.Init}, segs...)
	}
	h := sha256.New()
	for _, s := range segs {
		p := s.URL
		if u, err := url.Parse(s.URL); err == nil {
			p = u.Path
		}
		fmt.Fprintf(h, "%s\x00%s\n", p, s.Range)
	}
	return hex.EncodeToString(h.Sum(nil)[:8])
}

func sameTracks(old, cur []stateTrack) bool {
	return slices.EqualFunc(old, cur, func(a, b stateTrack) bool {
		return a.N == b.N && a.Init == b.Init && (a.Key == "" || a.Key == b.Key)
	})
}

// StreamJob çalışan bir akış indirmesidir (Job arayüzü).
type StreamJob struct {
	r        Request
	lim      *Limiter
	manifest *url.URL
	header   http.Header
	client   *http.Client
	ua       string

	mu                sync.Mutex
	authHdrs          map[string]string // 401'de eklenen site girişleri (host:port); parçalar arasında paylaşılır
	got, size         int64
	speed             float64
	segDone, segments int
	segBusy           time.Duration // biten parçaların toplam süresi (ETA)
	segTimed          int
	phase, note       string
	path              string
	restarted, paused bool
	ctx               context.Context
	cancel            context.CancelFunc
	done              chan struct{}
	result            error

	sibs  map[string][]unit      // aralıklı parçalar kaynak adresine göre, başlangıç sırasıyla (work kurar)
	whole map[string]*sync.Mutex // Range'i yok sayan kaynaklar: gövde bir kez okunup bölünür (VGR M4)
}

// StartStream akış indirmesini arka planda başlatır; var olan parçalardan devam eder.
func StartStream(ctx context.Context, r Request, lim *Limiter) (*StreamJob, error) {
	if r.Stream == nil {
		return nil, errors.New("akış seçimi yok")
	}
	mu, err := url.Parse(r.URL)
	if err != nil || (mu.Scheme != "http" && mu.Scheme != "https") {
		return nil, errors.New("yalnız http ve https adresleri indirilebilir")
	}
	if r.Path == "" {
		return nil, errors.New("akış için dosya yolu gerekli")
	}
	h := r.Header.Clone()
	if h == nil {
		h = http.Header{}
	}
	j := &StreamJob{r: r, lim: lim, manifest: mu, header: h, client: streamClient(cmpOr(h.Get("Referer"), r.Page)), ua: cleanUserAgent(r.UserAgent),
		size: -1, path: r.Path, done: make(chan struct{})}
	j.ctx, j.cancel = context.WithCancel(ctx)
	go j.run()
	return j, nil
}

func (j *StreamJob) run() {
	defer close(j.done)
	defer j.cancel()
	stop := make(chan struct{})
	go j.measure(stop)
	err := j.work()
	close(stop)
	j.mu.Lock()
	defer j.mu.Unlock()
	if err != nil && j.paused {
		err = ErrPaused
	}
	j.result, j.speed = err, 0
}

// measure saniyede bir hızı günceller (Download.watch ile aynı yumuşatma).
func (j *StreamJob) measure(stop chan struct{}) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	j.mu.Lock()
	last, at := j.got, time.Now()
	j.mu.Unlock()
	for {
		select {
		case <-stop:
			return
		case now := <-t.C:
			j.mu.Lock()
			inst := float64(j.got-last) / now.Sub(at).Seconds()
			j.speed = 0.7*j.speed + 0.3*inst
			last, at = j.got, now
			j.mu.Unlock()
		}
	}
}

func (j *StreamJob) Pause() {
	j.mu.Lock()
	j.paused = true
	j.mu.Unlock()
	j.cancel()
}

func (j *StreamJob) Wait() error {
	<-j.done
	return j.result
}

func (j *StreamJob) Snapshot() Snapshot {
	j.mu.Lock()
	defer j.mu.Unlock()
	s := Snapshot{Got: j.got, Size: j.size, Speed: j.speed, Conns: []Seg{}, Resumable: true, Restarted: j.restarted, Path: j.path}
	if j.restarted {
		s.RestartReason = streamRedo
	}
	return s
}

func (j *StreamJob) StreamProgress() StreamProgress {
	j.mu.Lock()
	defer j.mu.Unlock()
	p := StreamProgress{SegDone: j.segDone, Segments: j.segments, Phase: j.phase, ETA: -1, Note: j.note}
	if j.segTimed > 0 && j.segments > j.segDone {
		conns := float64(cmp0(j.r.MaxConns, 8))
		p.ETA = int64(float64(j.segBusy) / float64(j.segTimed) * float64(j.segments-j.segDone) / conns / float64(time.Second))
	}
	return p
}

func cmp0(a, b int) int {
	if a > 0 {
		return a
	}
	return b
}

// get bir isteği öğenin başlıklarıyla yollar: başlıklar manifest adresine aittir, başka siteye ya da
// https → http'ye Cookie/Authorization gitmez, Referer kırpılır (stripOnRedirect). MSE oynatıcıları CORS
// isteği yapar: Referer'ın kökeni hedeften farklıysa Origin eklenir (genel tarayıcı davranışı, K32).
func (j *StreamJob) get(ctx context.Context, u, rng string) (*http.Response, error) {
	t, err := url.Parse(u)
	if err != nil {
		return nil, errSegURL
	}
	h := j.header.Clone()
	stripOnRedirect(h, j.manifest, t)
	if ref, err := url.Parse(h.Get("Referer")); err == nil && ref.Host != "" {
		if o := ref.Scheme + "://" + strings.ToLower(ref.Host); o != t.Scheme+"://"+strings.ToLower(t.Host) {
			h.Set("Origin", o)
		}
	}
	j.mu.Lock()
	d := &Download{header: h, auth: j.r.Auth, client: j.client, userAgent: j.ua, authHdrs: maps.Clone(j.authHdrs)}
	j.mu.Unlock()
	resp, err := d.do(ctx, u, rng)
	if errors.Is(err, errPrivateSeg) { // yeniden denemenin anlamı yok
		return nil, permanent{errPrivateSeg}
	}
	if len(d.authHdrs) > 0 {
		j.mu.Lock()
		if j.authHdrs == nil {
			j.authHdrs = map[string]string{}
		}
		maps.Copy(j.authHdrs, d.authHdrs)
		j.mu.Unlock()
	}
	return resp, err
}

// fetchWith manifest çeken fetchFunc'tır (gövde ≤ maxManifest).
func fetchWith(ctx context.Context, get func(ctx context.Context, u, rng string) (*http.Response, error)) fetchFunc {
	return func(u string) ([]byte, *url.URL, error) {
		ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
		defer cancel()
		late := func(err error) error {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errors.New("Sunucu akış listesini zamanında göndermedi")
			}
			return err
		}
		resp, err := get(ctx, u, "")
		if err != nil {
			return nil, nil, late(err)
		}
		defer resp.Body.Close()
		switch {
		case resp.StatusCode == 401 || resp.StatusCode == 403 || resp.StatusCode == 404 || resp.StatusCode == 410:
			return nil, nil, errors.New(errExpired)
		case resp.StatusCode != http.StatusOK:
			return nil, nil, fmt.Errorf("sunucu %s döndü", resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, maxManifest+1))
		if err != nil {
			return nil, nil, late(err)
		}
		if len(b) > maxManifest {
			return nil, nil, errors.New("Akış listesi çok büyük")
		}
		return b, resp.Request.URL, nil
	}
}

// unit indirilecek bir parça ve diskteki adıdır.
type unit struct {
	seg  segment
	file string
}

func (j *StreamJob) work() error {
	plan, err := planStream(j.r.URL, *j.r.Stream, fetchWith(j.ctx, j.get))
	if err != nil {
		return err
	}
	dir := j.r.Path + ".octo-parts"
	stPath := j.r.Path + ".stream.json"
	st := streamState{URL: j.r.URL, Spec: StreamSpec{Kind: j.r.Stream.Kind, Video: j.r.Stream.Video, Audio: j.r.Stream.Audio}, SegCount: plan.Segments()}
	var inits []unit
	var units [][]unit // iz başına
	for i, t := range plan.Tracks {
		pre := []string{"v", "a"}[i]
		st.Tracks = append(st.Tracks, stateTrack{N: len(t.Segs), Init: t.Init != nil, Key: trackKey(t)})
		var us []unit
		if t.Init != nil {
			u := unit{*t.Init, filepath.Join(dir, pre+"init.seg")}
			inits, us = append(inits, u), append(us, u)
		}
		for k, s := range t.Segs {
			us = append(us, unit{s, filepath.Join(dir, fmt.Sprintf("%s%06d.seg", pre, k))})
		}
		units = append(units, us)
	}
	if err := j.prepare(dir, stPath, st); err != nil {
		return err
	}
	merged := make([]bool, len(units)) // iz önceki koşuda birleşti (kapanış birleştirmeyi kesti): parçaları yeniden inmez
	for i := range units {
		if plan.Container != "ts" {
			merged[i] = wholeTrack(trackFile(dir, i))
		}
	}
	var all, todo []unit
	size := int64(0)
	sibs := map[string][]unit{}
	for i, us := range units {
		for _, u := range us {
			all = append(all, u)
			if !merged[i] {
				todo = append(todo, u)
			}
			if a, b, ok := parseRange(u.seg.Range); ok {
				if !merged[i] { // bölme birleşmiş izin parçalarını yeniden yazmasın (V5 L2)
					sibs[u.seg.URL] = append(sibs[u.seg.URL], u)
				}
				if size >= 0 {
					size += b - a + 1
				}
			} else {
				size = -1
			}
		}
	}
	for _, us := range sibs {
		slices.SortStableFunc(us, func(x, y unit) int {
			a, _, _ := parseRange(x.seg.Range)
			b, _, _ := parseRange(y.seg.Range)
			return cmp.Compare(a, b)
		})
	}
	j.mu.Lock()
	j.sibs = sibs
	j.segments, j.size = len(all), size
	for i, us := range units {
		if merged[i] {
			if fi, err := os.Stat(trackFile(dir, i)); err == nil {
				j.got += fi.Size()
			}
			j.segDone += len(us)
			continue
		}
		for _, u := range us {
			if fi, err := os.Stat(u.file); err == nil {
				j.got += fi.Size()
				j.segDone++
			}
		}
	}
	j.mu.Unlock()
	inits = slices.DeleteFunc(inits, func(u unit) bool { // birleşmiş izin init'i denetlenmişti, yeniden inmez
		return !slices.ContainsFunc(todo, func(x unit) bool { return x.file == u.file })
	})
	// Init önce: şifreli örnek girdisi (encv/enca/pssh) görülürse medya parçası hiç istenmez (K31).
	if err := j.fetchAll(inits); err != nil {
		return err
	}
	for _, u := range inits {
		if b, err := os.ReadFile(u.file); err == nil && encryptedInit(b) {
			j.cleanup(dir, stPath)
			return errors.New(errProtected)
		}
	}
	if err := j.fetchAll(todo); err != nil {
		return err
	}
	j.mu.Lock()
	j.phase = "merging"
	j.mu.Unlock()
	if err := j.merge(plan, units, dir); err != nil {
		os.Remove(j.r.Path + ".part")
		if err.Error() == errProtected {
			j.cleanup(dir, stPath)
		}
		return err
	}
	j.cleanup(dir, stPath)
	return nil
}

// prepare parça klasörünü ve durum dosyasını hazırlar: aynı seçim ve parça sayısıysa inenler kullanılır,
// değilse (akış değişti) parçalar atılıp baştan başlanır. Yarım .tmp'ler her zaman atılır.
func (j *StreamJob) prepare(dir, stPath string, st streamState) error {
	var old streamState
	b, err := os.ReadFile(stPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		os.RemoveAll(dir) // durum dosyası yoksa parçaların hangi akışa ait olduğu bilinmez
	case err != nil:
		return fileErr("durum dosyası", stPath, err)
	case json.Unmarshal(b, &old) != nil || old.Spec != st.Spec || !sameTracks(old.Tracks, st.Tracks):
		j.cleanup(dir, stPath)
		j.mu.Lock()
		j.restarted = true
		j.mu.Unlock()
	}
	if err := os.MkdirAll(filepath.Dir(j.r.Path), 0o755); err != nil { // üst klasörler sıradan indirme gibi; yalnız parça klasörü 0700
		return fileErr("klasör oluşturma", filepath.Dir(j.r.Path), err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fileErr("klasör oluşturma", dir, err)
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if strings.HasSuffix(e.Name(), ".tmp") {
				os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	out, _ := json.Marshal(st)
	if err := os.WriteFile(stPath, out, 0o600); err != nil {
		return fileErr("durum dosyası", stPath, err)
	}
	return nil
}

// cleanup yalnız kendi geçici dosyalarımızı siler (adı .octo-parts ile biten klasör ve durum dosyası).
func (j *StreamJob) cleanup(dir, stPath string) {
	if strings.HasSuffix(dir, ".octo-parts") {
		os.RemoveAll(dir)
	}
	os.Remove(stPath)
}

func parseRange(r string) (a, b int64, ok bool) {
	s, found := strings.CutPrefix(r, "bytes=")
	as, bs, dash := strings.Cut(s, "-")
	if !found || !dash {
		return 0, 0, false
	}
	a, err1 := strconv.ParseInt(as, 10, 64)
	b, err2 := strconv.ParseInt(bs, 10, 64)
	return a, b, err1 == nil && err2 == nil && b >= a
}

// fetchAll eksik parçaları MaxConns eşzamanlı indirir; ilk kalıcı hata hepsini durdurur.
func (j *StreamJob) fetchAll(us []unit) error {
	ctx, cancel := context.WithCancel(j.ctx)
	defer cancel()
	ch := make(chan unit)
	var wg sync.WaitGroup
	var once sync.Once
	var first error
	for range min(cmp0(j.r.MaxConns, 8), max(len(us), 1)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range ch {
				if err := j.fetchUnit(ctx, u); err != nil {
					once.Do(func() { first = err; cancel() })
				}
			}
		}()
	}
	for _, u := range us {
		if _, err := os.Stat(u.file); err == nil {
			continue
		}
		select {
		case ch <- u:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
	}
	close(ch)
	wg.Wait()
	if j.ctx.Err() != nil {
		return j.ctx.Err()
	}
	return first
}

// permanent: yeniden denemenin anlamı olmayan hata.
type permanent struct{ error }

func (j *StreamJob) fetchUnit(ctx context.Context, u unit) error {
	delay := segRetryDelay
	for attempt := 0; ; attempt++ {
		start := time.Now()
		err := j.getUnit(ctx, u)
		if errors.Is(err, errSwept) { // başka bir bölme yazdı ve saydı
			return nil
		}
		if err == nil {
			j.mu.Lock()
			j.segDone++
			j.segBusy += time.Since(start)
			j.segTimed++
			j.mu.Unlock()
			return nil
		}
		var p permanent
		if ctx.Err() != nil || errors.As(err, &p) || attempt == segRetries {
			if errors.As(err, &p) {
				return p.error
			}
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
		delay *= 2
	}
}

// getUnit bir parçayı .tmp'ye yazar, tam inince .seg'e çevirir. Okuma readTimeout boyunca veri gelmezse kesilir.
func (j *StreamJob) getUnit(ctx context.Context, u unit) error {
	if mu := j.wholeLock(u.seg.URL, false); mu != nil { // süren bölme bu parçayı da yazıyor olabilir
		mu.Lock()
		mu.Unlock()
		if _, err := os.Stat(u.file); err == nil {
			return errSwept
		}
	}
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	idle := time.AfterFunc(readTimeout, rcancel)
	defer idle.Stop()
	resp, err := j.get(rctx, u.seg.URL, u.seg.Range)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch c := resp.StatusCode; {
	case c == 401 || c == 403 || c == 404 || c == 410:
		return permanent{errors.New(errExpired)}
	case c >= 500 || c == http.StatusTooManyRequests || c == http.StatusRequestTimeout: // geçici; shortcut: Retry-After okunmaz, 1-2-4 sn beklenir
		return fmt.Errorf("sunucu %s döndü", resp.Status)
	case webPage(resp, u.file):
		return permanent{errors.New("Sunucu video parçası yerine web sayfası döndü")}
	}
	var body io.Reader = idleReader{resp.Body, idle}
	want := int64(-1)
	if a, b, ok := parseRange(u.seg.Range); ok {
		want = b - a + 1
		switch resp.StatusCode {
		case http.StatusPartialContent:
			if start, _, ok := contentRange(resp.Header.Get("Content-Range")); !ok || start != a {
				return permanent{errors.New("Sunucu yanlış parça aralığı döndü")}
			}
		case http.StatusOK: // aralığı yok saydı: tüm gövde bir kez okunup aynı kaynağın parçalarına bölünür
			return j.split(rctx, idle, body, resp.Body, u)
		default:
			return permanent{fmt.Errorf("sunucu %s döndü", resp.Status)}
		}
		body = io.LimitReader(body, want)
	} else if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return permanent{fmt.Errorf("sunucu %s döndü", resp.Status)}
	}
	return j.save(rctx, body, u.file, ".tmp", want)
}

// errSwept: parçayı aynı kaynağın tek gövdesini bölen başka bir istek yazdı.
var errSwept = errors.New("parça bölmeyle yazıldı")

// wholeLock Range'i yok sayan kaynağın kilidi; mark false ise yalnız var olanı verir.
func (j *StreamJob) wholeLock(src string, mark bool) *sync.Mutex {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.whole[src] == nil && mark {
		if j.whole == nil {
			j.whole = map[string]*sync.Mutex{}
		}
		j.whole[src] = &sync.Mutex{}
	}
	return j.whole[src]
}

// split 200 dönen aralık isteğinin gövdesini u'dan itibaren aynı kaynağın eksik parçalarına böler: her parça
// için dosyayı baştan okumak O(n²) trafikti (VGR M4). Örtüşen ya da u'dan önceki parçalar kendi isteğiyle iner.
func (j *StreamJob) split(ctx context.Context, idle *time.Timer, body io.Reader, c io.Closer, u unit) error {
	mu := j.wholeLock(u.seg.URL, true)
	waited := !mu.TryLock()
	if waited { // başka istek bölüyor: bu gövde beklerken boşuna akmasın
		c.Close()
		idle.Stop()
		mu.Lock()
	}
	defer mu.Unlock()
	if _, err := os.Stat(u.file); err == nil {
		return errSwept
	}
	if waited { // önceki bölme u'dan sonradan başladı ya da yarım kaldı: kendi gövdemizi baştan isteriz
		resp, err := j.get(ctx, u.seg.URL, "")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("sunucu %s döndü", resp.Status)
		}
		idle.Reset(readTimeout)
		body = idleReader{resp.Body, idle}
	}
	var todo []unit
	from, end := false, int64(-1)
	for _, s := range j.sibs[u.seg.URL] {
		a, b, _ := parseRange(s.seg.Range)
		if from = from || s.file == u.file; !from || a < end {
			continue
		}
		if _, err := os.Stat(s.file); err != nil {
			todo, end = append(todo, s), b+1
		}
	}
	var pos int64
	for _, s := range todo {
		a, b, _ := parseRange(s.seg.Range)
		if _, err := io.CopyN(io.Discard, body, a-pos); err != nil {
			return err
		}
		// u'dan başka parça: aynı adı 206 alan bir istek de yazıyor olabilir, geçici dosyalar ayrı kalsın
		if err := j.save(ctx, io.LimitReader(body, b-a+1), s.file, ".w.tmp", b-a+1); err != nil {
			return err
		}
		pos = b + 1
		if s.file != u.file {
			j.mu.Lock()
			j.segDone++
			j.mu.Unlock()
		}
	}
	return nil
}

// idleReader her okumada boşta kalma zamanlayıcısını yeniden kurar.
type idleReader struct {
	r    io.Reader
	idle *time.Timer
}

func (r idleReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	if n > 0 {
		r.idle.Reset(readTimeout)
	}
	return n, err
}

// save gövdeyi file+suffix'e yazar, tam inince (want ≥ 0 ise o kadar) file'a çevirir.
func (j *StreamJob) save(ctx context.Context, body io.Reader, file, suffix string, want int64) (err error) {
	tmp := file + suffix
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return permanent{fileErr("yazma", tmp, err)}
	}
	var n int64
	defer func() {
		if err != nil {
			f.Close()
			os.Remove(tmp)
			j.mu.Lock()
			j.got -= n
			j.mu.Unlock()
		}
	}()
	buf := make([]byte, 32<<10)
	for {
		k, rerr := body.Read(buf)
		if k > 0 {
			if err := j.lim.Wait(ctx, k); err != nil {
				return err
			}
			if _, err := f.Write(buf[:k]); err != nil {
				return permanent{fileErr("yazma", tmp, err)}
			}
			n += int64(k)
			j.mu.Lock()
			j.got += int64(k)
			j.mu.Unlock()
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if want >= 0 && n != want {
		return io.ErrUnexpectedEOF
	}
	if err := f.Close(); err != nil {
		return permanent{fileErr("yazma", tmp, err)}
	}
	return os.Rename(tmp, file)
}

// concat parçaları sırayla dst'ye yazar. Parçaları çağıran, sonuç tamamlanınca removeUnits ile siler: disk en çok
// iki kat dolar (VGR L5) ve kesilen birleştirme inen veriyi kaybettirmez.
func concat(dst string, us []unit) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fileErr("yazma", dst, err)
	}
	for _, u := range us {
		in, err := os.Open(u.file)
		if err != nil {
			f.Close()
			return err
		}
		_, err = io.Copy(f, in)
		in.Close()
		if err != nil {
			f.Close()
			return fileErr("yazma", dst, err)
		}
	}
	if err := f.Sync(); err != nil { // adlandırılan iz dosyası güç kesintisinden sonra da tam olsun (V5 L1)
		f.Close()
		return fileErr("yazma", dst, err)
	}
	if err := f.Close(); err != nil {
		return fileErr("yazma", dst, err)
	}
	return nil
}

func removeUnits(us []unit) {
	for _, u := range us {
		os.Remove(u.file)
	}
}

// trackFile izin birleşik dosyasıdır (concat .tmp'ye yazar, Sync eder, sonra adlandırır).
func trackFile(dir string, i int) string { return filepath.Join(dir, []string{"v.mp4", "a.mp4"}[i]) }

// wholeTrack birleşik iz dosyası var ve kutuları dosya boyuna tam oturuyor mu. Değilse (yarım yazılmış: eski derleme,
// diske inmemiş veri) silinir, iz parçalarıyla yeniden iner. shortcut: kutu sınırında kesilmiş dosya tam görünür;
// Sync'li yeni yazımda bu olmaz.
func wholeTrack(p string) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	bs, err := scanBoxes(f)
	f.Close()
	if err != nil || len(bs) == 0 {
		os.Remove(p)
		return false
	}
	return true
}

// merge parçaları Path+".part"a birleştirir ve yerine koyar. İki iz birleştirilemezse veri kaybolmaz:
// görüntü ve ses yan yana ayrı dosya olarak konur, öğeye not düşülür.
func (j *StreamJob) merge(plan streamPlan, units [][]unit, dir string) error {
	part := j.r.Path + ".part"
	if plan.Container == "ts" {
		all := slices.Concat(units...)
		if err := concat(part, all); err != nil {
			return err
		}
		removeUnits(all)
		return j.place(j.r.Path)
	}
	var files []string
	for i, us := range units {
		p := trackFile(dir, i)
		if _, err := os.Stat(p); err != nil { // varsa önceki koşu bu izi birleştirmişti
			if err := concat(p+".tmp", us); err != nil {
				return err
			}
			if err := os.Rename(p+".tmp", p); err != nil {
				return fileErr("taşıma", p, err)
			}
			removeUnits(us)
		}
		files = append(files, p)
	}
	err := writeFMP4File(part, files)
	if err == nil {
		return j.place(j.r.Path)
	}
	if err.Error() == errProtected {
		return err
	}
	os.Remove(part)
	if len(files) < 2 { // tek iz: birleşik parçalar zaten dosyanın kendisi olabilir (ör. parçasız MP4); inen veri atılmaz
		if rerr := os.Rename(files[0], part); rerr != nil {
			return fileErr("taşıma", j.r.Path, rerr)
		}
		if perr := j.place(j.r.Path); perr != nil {
			os.Rename(part, files[0]) // birleşik veri kalsın: work .part'ı siler, devam izi yeniden kullanır (V5 L3)
			return perr
		}
		j.mu.Lock()
		j.note = "Video olduğu gibi kaydedildi (birleştirilemedi): " + err.Error()
		j.mu.Unlock()
		return nil
	}
	base := strings.TrimSuffix(j.r.Path, filepath.Ext(j.r.Path))
	vpath, apath := base+".video.mp4", base+".audio.m4a"
	for i, p := range []string{vpath, apath} {
		if err := os.Rename(files[i], p+".part"); err != nil {
			return fileErr("taşıma", p, err)
		}
	}
	final, perr := place(vpath, j.r.Overwrite, j.r.Trash)
	if perr != nil {
		return fileErr("taşıma", vpath, perr)
	}
	if _, perr := place(apath, j.r.Overwrite, j.r.Trash); perr != nil {
		return fileErr("taşıma", apath, perr)
	}
	j.mu.Lock()
	j.path, j.note = final, "Ses ayrı dosyada kaldı (birleştirilemedi): "+err.Error()
	j.mu.Unlock()
	return nil
}

// placeFile place'tir (test için değiştirilebilir).
var placeFile = place

func (j *StreamJob) place(path string) error {
	final, err := placeFile(path, j.r.Overwrite, j.r.Trash)
	if err != nil {
		return fileErr("taşıma", path, err)
	}
	j.mu.Lock()
	j.path = final
	j.mu.Unlock()
	return nil
}

func writeFMP4File(dst string, srcs []string) error {
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fileErr("yazma", dst, err)
	}
	var rs []io.ReadSeeker
	for _, s := range srcs {
		f, err := os.Open(s)
		if err != nil {
			out.Close()
			return err
		}
		defer f.Close()
		rs = append(rs, f)
	}
	if err := WriteFMP4(out, rs...); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
