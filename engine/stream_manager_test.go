package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type fakeStreamJob struct {
	*fakeJob
	pmu sync.Mutex
	p   StreamProgress
}

func (j *fakeStreamJob) StreamProgress() StreamProgress {
	j.pmu.Lock()
	defer j.pmu.Unlock()
	return j.p
}
func (j *fakeStreamJob) setProgress(p StreamProgress) { j.pmu.Lock(); j.p = p; j.pmu.Unlock() }

// streamJobs işleri akış işiyle sarar; başlayan her iş kanala düşer.
func streamJobs(h *harness) chan *fakeStreamJob {
	ch := make(chan *fakeStreamJob, 4)
	inner := h.m.d.Start
	h.m.d.Start = func(ctx context.Context, r Request, resume bool) (Job, error) {
		j, err := inner(ctx, r, resume)
		if err != nil {
			return nil, err
		}
		sj := &fakeStreamJob{fakeJob: j.(*fakeJob), p: StreamProgress{ETA: -1}}
		ch <- sj
		return sj, nil
	}
	return ch
}

func TestManagerAddStream(t *testing.T) {
	h := newHarness(t)
	h.stream = StreamInfo{Kind: "hls", Container: "ts", Variants: []StreamVariant{{ID: "v0", Label: "720p"}}}
	spec := &StreamSpec{Kind: "hls", Video: "v0", Label: "720p"}
	it, err := h.m.Add(AddArgs{URL: "https://x.example/m.m3u8", Name: "Film. Bölüm 1 (720p).mp4", Stream: spec, Headers: map[string]string{"Cookie": "a=1"}, PageURL: "https://site.x/izle", Start: true})
	if err != nil {
		t.Fatal(err)
	}
	if it.Name != "Film. Bölüm 1 (720p).ts" || it.Stream == nil || it.Stream.Video != "v0" || it.Stream.Label != "720p" || it.Size != -1 || !it.Resumable {
		t.Fatalf("öğe: %+v %+v", it, it.Stream)
	}
	if len(h.probed) != 1 || h.probed[0].Get("Cookie") != "a=1" || h.pages[0] != "https://site.x/izle" {
		t.Fatalf("ProbeStream başlıkları: %v %v", h.probed, h.pages)
	}
	h.job(it.Name)
	h.mu.Lock()
	req := h.reqs[0]
	h.mu.Unlock()
	if req.Stream == nil || req.Stream.Video != "v0" || req.Stream == spec || req.Page != "https://site.x/izle" {
		t.Fatalf("istek akış seçimi: %+v", req.Stream)
	}

	h.stream = StreamInfo{Kind: "dash", Container: "mp4", Variants: []StreamVariant{}}
	it, err = h.m.Add(AddArgs{URL: "https://x.example/m.mpd", Stream: &StreamSpec{Kind: "dash", Audio: "a"}})
	if err != nil || it.Name != "video.m4a" {
		t.Fatalf("yalnız ses: %v %+v", err, it)
	}
	h.stream.Variants = []StreamVariant{{ID: "1"}}
	if it, err = h.m.Add(AddArgs{URL: "https://x.example/m.mpd", Stream: &StreamSpec{Kind: "dash"}}); err != nil || it.Name != "video.mp4" {
		t.Fatalf("adsız: %v %+v", err, it)
	}
}

func TestManagerAddStreamRejects(t *testing.T) {
	h := newHarness(t)
	for want, info := range map[string]StreamInfo{
		errProtected: {Kind: "hls", Protected: true},
		errLive:      {Kind: "hls", Live: true},
	} {
		h.stream = info
		if _, err := h.m.Add(AddArgs{URL: "https://x.example/m.m3u8", Stream: &StreamSpec{Kind: "hls"}}); err == nil || err.Error() != want {
			t.Errorf("%s bekleniyordu: %v", want, err)
		}
	}
	h.stream = StreamInfo{Kind: "hls", Container: "ts"}
	if _, err := h.m.Add(AddArgs{URL: "https://x.example/m.m3u8", Stream: &StreamSpec{Kind: "rtmp"}}); err == nil {
		t.Error("bilinmeyen akış türü kabul edildi")
	}
	h.streamErr = errors.New("sunucu 404 döndü")
	if _, err := h.m.Add(AddArgs{URL: "https://x.example/m.m3u8", Stream: &StreamSpec{Kind: "hls"}}); err == nil {
		t.Error("çözülemeyen manifest kabul edildi")
	}
	if items, _, _ := h.m.List(); len(items) != 0 {
		t.Fatalf("reddedilen öğe eklendi: %d", len(items))
	}
}

func TestManagerStreamProgressAndFinish(t *testing.T) {
	h := newHarness(t)
	jobs := streamJobs(h)
	h.stream = StreamInfo{Kind: "dash", Container: "mp4", Variants: []StreamVariant{{ID: "1"}}}
	events, unsub := h.m.Subscribe()
	defer unsub()
	it, err := h.m.Add(AddArgs{URL: "https://x.example/m.mpd", Name: "a.mp4", Stream: &StreamSpec{Kind: "dash"}, Start: true})
	if err != nil {
		t.Fatal(err)
	}
	j := <-jobs
	h.job(it.Name) // r.job yazıldı
	j.set(Snapshot{Got: 300, Size: -1, Speed: 100, Path: filepath.Join(h.dl, "a.mp4")})
	j.setProgress(StreamProgress{SegDone: 3, Segments: 10, ETA: 42})
	h.m.progress()
	got := h.item(it.ID)
	if got.Stream.SegDone != 3 || got.Stream.Segments != 10 || got.ETA != 42 {
		t.Fatalf("öğe ilerlemesi: %+v eta=%d", got.Stream, got.ETA)
	}
	var p ProgressItem
	for ev := range events {
		if ev.Event == "progress" && len(ev.Items) > 0 {
			p = ev.Items[0]
			break
		}
	}
	if p.SegDone != 3 || p.Segments != 10 || p.Phase != "" {
		t.Fatalf("progress olayı: %+v", p)
	}
	j.setProgress(StreamProgress{SegDone: 10, Segments: 10, Phase: "merging", ETA: -1})
	h.m.progress()
	if got := h.item(it.ID); got.Stream.Phase != "merging" {
		t.Fatalf("evre: %+v", got.Stream)
	}
	os.WriteFile(filepath.Join(h.dl, "a.video.mp4"), []byte("12345"), 0o600)
	j.set(Snapshot{Got: 400, Size: -1, Path: filepath.Join(h.dl, "a.video.mp4")})
	j.setProgress(StreamProgress{SegDone: 10, Segments: 10, Phase: "merging", Note: "Ses ayrı dosyada kaldı (birleştirilemedi): x"})
	j.end(nil)
	h.waitState(it.ID, StateDone)
	got = h.item(it.ID)
	if got.Note == "" || got.Stream.Phase != "" || got.Name != "a.video.mp4" || got.Size != 5 || got.Got != 5 {
		t.Fatalf("bitiş: %+v %+v", got, got.Stream)
	}
	h.m.Redownload([]string{it.ID}) // eski koşunun notu yeni koşuda kalmasın
	<-jobs
	if got := h.item(it.ID); got.Note != "" {
		t.Fatalf("not yeniden indirmede kaldı: %q", got.Note)
	}
}

func TestManagerRemoveStreamTemp(t *testing.T) {
	h := newHarness(t)
	h.stream = StreamInfo{Kind: "hls", Container: "mp4", Variants: []StreamVariant{{ID: "v0"}}}
	it, err := h.m.Add(AddArgs{URL: "https://x.example/m.m3u8", Name: "b.mp4", Stream: &StreamSpec{Kind: "hls"}})
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(h.dl, "b.mp4")
	os.MkdirAll(p+".octo-parts", 0o700)
	os.WriteFile(filepath.Join(p+".octo-parts", "v000000.seg"), []byte("x"), 0o600)
	os.WriteFile(p+".stream.json", []byte("{}"), 0o600)
	keep := filepath.Join(h.dl, "b.mp4.octo-parts-başka")
	os.MkdirAll(keep, 0o700)
	if err := h.m.Remove([]string{it.ID}, false); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{p + ".octo-parts", p + ".stream.json"} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("geçici kaldı: %s", f)
		}
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("başka klasör silindi")
	}
}
