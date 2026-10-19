package engine

import (
	"slices"
	"testing"
	"time"
)

// Geri sayım BAŞLARKEN abone yoksa (uygulama kapalı) motor uygulamayı açar; abone varsa dokunmaz.
func TestCountdownStartOpensAppWhenNoSubscriber(t *testing.T) {
	open := []string{"open", "-b", "com.uba.octo"}
	countdown := func(t *testing.T, subscribed bool) *harness {
		h := newHarness(t)
		h.m.actionDelay = 10 * time.Second
		t.Cleanup(h.m.CancelAction)
		h.setMain(func(q *Queue) { q.Schedule.OnDone = "sleep" })
		it := h.add("a.bin", false)
		if subscribed {
			_, unsub := h.m.Subscribe()
			t.Cleanup(unsub)
		}
		h.m.StartQueue(mainQueue)
		h.job("a.bin").end(nil)
		h.waitState(it.ID, StateDone)
		if a, _ := h.m.PendingAction(); a != "sleep" {
			t.Fatalf("geri sayım başlamadı: %q", a)
		}
		return h
	}
	opened := func(h *harness) bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.ContainsFunc(h.runs, func(r []string) bool { return slices.Equal(r, open) })
	}

	t.Run("abone yok", func(t *testing.T) {
		h := countdown(t, false)
		eventually(t, "open -b com.uba.octo", func() bool { return opened(h) })
	})
	t.Run("abone var", func(t *testing.T) {
		h := countdown(t, true)
		time.Sleep(100 * time.Millisecond)
		if opened(h) {
			t.Fatal("abone varken uygulama açılmamalı")
		}
	})
	t.Run("geri sayım yok", func(t *testing.T) {
		h := newHarness(t)
		h.m.Tick()
		time.Sleep(100 * time.Millisecond)
		if opened(h) {
			t.Fatal("geri sayım yokken uygulama açılmamalı")
		}
	})
}

// Durma saati: devam ettirilebilen iş "sırada"ya döner; devam ettirilemeyen iş bitene dek sürer,
// kuyruk durmuş olur, yeni dosya başlamaz ve onDone çalışmaz.
func TestSchedulerStopKeepsUnresumable(t *testing.T) {
	h := newHarness(t)
	h.m.actionDelay = time.Millisecond
	h.setMain(func(q *Queue) { q.AtOnce = 2; q.Schedule.StopOn = true; q.Schedule.OnDone = "shutdown" })
	a := h.add("a.bin", false)
	b := h.add("b.bin", false)
	c := h.add("c.bin", false)
	h.m.StartQueue(mainQueue)
	h.job("a.bin").set(Snapshot{Got: 10, Size: 100, Resumable: true})
	h.job("b.bin").set(Snapshot{Got: 10, Size: -1, Resumable: false})
	h.setNow(time.Date(2026, 10, 20, 7, 30, 10, 0, time.Local))
	h.m.Tick()
	if h.queue(mainQueue).Running {
		t.Fatal("07:30'da kuyruk durmalı")
	}
	h.waitState(a.ID, StateQueued)
	if s := h.item(b.ID).State; s != StateDownloading {
		t.Fatalf("devam ettirilemeyen iş kesilmemeli: %s", s)
	}
	h.job("b.bin").end(nil)
	h.waitState(b.ID, StateDone)
	time.Sleep(50 * time.Millisecond)
	if s := h.item(c.ID).State; s != StateQueued {
		t.Fatalf("durmuş kuyrukta yeni dosya başlamamalı: %s", s)
	}
	if h.activeCount() != 0 || h.runCount() != 0 {
		t.Fatalf("iş ya da onDone kalmamalı: active=%d runs=%d", h.activeCount(), h.runCount())
	}
}
