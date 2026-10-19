package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// drain: workers goroutine'i toplam total bayt tüketene dek 32 KB'lık Wait çağırır.
func drain(l *Limiter, workers int, total int64) {
	var left atomic.Int64
	left.Store(total)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for left.Add(-32<<10) > -(32 << 10) {
				if l.Wait(context.Background(), 32<<10) != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestLimiterSharedAcrossReaders(t *testing.T) {
	l := NewLimiter()
	l.Set(1 << 20)
	start := time.Now()
	drain(l, 3, 4<<20)
	if el := time.Since(start); el < 3500*time.Millisecond || el > 5*time.Second {
		t.Fatalf("4 MB @ 1 MB/s took %v, want 3.5-5 s", el)
	}
}

func TestLimiterSetZeroUnblocks(t *testing.T) {
	l := NewLimiter()
	l.Set(1 << 10) // 1 KB/s: 1 MB would take ~17 min
	done := make(chan struct{})
	go func() { drain(l, 3, 1<<20); close(done) }()
	time.Sleep(200 * time.Millisecond)
	l.Set(0)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Set(0) bekleyenleri çözmedi")
	}
}

func TestLimiterCtxCancel(t *testing.T) {
	l := NewLimiter()
	l.Set(1 << 10)
	ctx, cancel := context.WithCancel(context.Background())
	res := make(chan error, 1)
	go func() { res <- l.Wait(ctx, 1<<20) }()
	time.Sleep(100 * time.Millisecond)
	t0 := time.Now()
	cancel()
	select {
	case err := <-res:
		if err != context.Canceled || time.Since(t0) > 50*time.Millisecond {
			t.Fatalf("err=%v after %v", err, time.Since(t0))
		}
	case <-time.After(time.Second):
		t.Fatal("Wait iptalde dönmedi")
	}
}

func TestLimiterNil(t *testing.T) {
	var l *Limiter
	l.Set(5)
	if err := l.Wait(context.Background(), 1<<30); err != nil || l.NearLimit(1e12) {
		t.Fatal("nil Limiter no-op olmalı")
	}
}

func TestLimiterNearLimit(t *testing.T) {
	l := NewLimiter()
	if l.NearLimit(1e12) {
		t.Fatal("kapalıyken false olmalı")
	}
	l.Set(1000)
	for bps, want := range map[float64]bool{0: false, 849: false, 850: true, 1000: true, 5000: true} {
		if got := l.NearLimit(bps); got != want {
			t.Errorf("NearLimit(%v)=%v, want %v", bps, got, want)
		}
	}
}

// İki indirme ayrı ayrı sınırın altında ama toplamda sınırda: NearLimit toplamı görmeli.
func TestLimiterNearLimitAggregate(t *testing.T) {
	l := NewLimiter()
	l.Set(100_000)
	ctx, cancel := context.WithTimeout(context.Background(), 1300*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for l.Wait(ctx, 5000) == nil {
			}
		}()
	}
	wg.Wait()
	if !l.NearLimit(0) {
		t.Fatalf("toplam hız sınırdayken NearLimit false (ölçülen %v)", l.total)
	}
}

// Dosya başına sınır: kendi kovası ve bağlı olduğu küresel kova birlikte uygulanır.
func TestLimiterWithinParent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		parent, child int64
	}{
		{"yalnız dosya", 0, 1 << 20},
		{"yalnız küresel", 1 << 20, 0},
		{"küresel daha düşük", 1 << 20, 8 << 20},
		{"dosya daha düşük", 8 << 20, 1 << 20},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, c := NewLimiter(), NewLimiter()
			p.Set(tc.parent)
			c.Set(tc.child)
			l := c.within(p)
			start := time.Now()
			drain(l, 2, 2<<20)
			if el := time.Since(start); el < 1500*time.Millisecond || el > 3*time.Second {
				t.Fatalf("2 MB @ 1 MB/s took %v, want 1.5-3 s", el)
			}
		})
	}
}

// İki dosyanın kendi sınırı var; küresel kova ikisinin toplamını sınırlar.
func TestLimiterChildrenShareParent(t *testing.T) {
	p := NewLimiter()
	p.Set(1 << 20)
	a, b := NewLimiter(), NewLimiter()
	a.Set(8 << 20)
	b.Set(8 << 20)
	start := time.Now()
	var wg sync.WaitGroup
	for _, l := range []*Limiter{a.within(p), b.within(p)} {
		wg.Add(1)
		go func() { defer wg.Done(); drain(l, 1, 1<<20) }()
	}
	wg.Wait()
	if el := time.Since(start); el < 1500*time.Millisecond || el > 3*time.Second {
		t.Fatalf("2×1 MB @ toplam 1 MB/s took %v, want 1.5-3 s", el)
	}
}

func TestLimiterWithinNilAndNearLimit(t *testing.T) {
	p := NewLimiter()
	var none *Limiter
	if none.within(p) != p {
		t.Fatal("nil dosya sınırı küresele düşmeli")
	}
	c := NewLimiter().within(p)
	if c.NearLimit(1e12) {
		t.Fatal("ikisi de kapalıyken false olmalı")
	}
	p.Set(1000)
	if !c.NearLimit(900) {
		t.Fatal("küresel sınıra yakınken true olmalı")
	}
	p.Set(0)
	c.Set(1000)
	if !c.NearLimit(900) || c.NearLimit(800) {
		t.Fatal("dosya sınırına göre karar vermeli")
	}
}
