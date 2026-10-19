package engine

import (
	"context"
	"sync"
	"time"
)

// Limiter: tüm indirmelerin paylaştığı tek jeton kovası (indirme başına ortalama hız
// uyuyor; biz bilerek küresel kova kullanıyoruz). Kova boş başlar: patlama payı (1 sn'lik hız)
// yalnız boşta birikir, böylece sınır baştan itibaren tutar. nil Limiter her yerde no-op.
// Dosya başına sınır ayrı bir Limiter'dır; within ile küresele bağlanır, ikisi birlikte uygulanır.
type Limiter struct {
	parent *Limiter // küresel kova; başlamadan önce bağlanır, sonra değişmez
	mu     sync.Mutex
	rate   float64 // bayt/sn; 0 = kapalı
	tokens float64 // eksiye düşebilir: n > burst isteği borç olarak ödenir
	last   time.Time
	change chan struct{} // Set'te kapanır, bekleyenleri uyandırır
	// Toplam (tüm indirmeler) hız ölçümü: NearLimit indirme başına değil toplam hıza bakar.
	win      float64   // winStart'tan beri verilen bayt
	winStart time.Time // ölçüm penceresinin başı
	total    float64   // son tamamlanan pencerenin hızı (bayt/sn)
}

func NewLimiter() *Limiter { return &Limiter{change: make(chan struct{})} }

// within dosya başına sınırı (l) küresel sınırın (p) altına bağlar; l nil ise p döner.
func (l *Limiter) within(p *Limiter) *Limiter {
	if l == nil {
		return p
	}
	l.parent = p
	return l
}

// Set hızı ayarlar (0 = kapalı); hemen etkili olur.
func (l *Limiter) Set(bytesPerSec int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.refill()
	if bytesPerSec <= 0 {
		l.rate = 0
	} else {
		if l.rate == 0 {
			l.tokens = 0
		}
		l.rate = float64(bytesPerSec)
		l.tokens = min(l.tokens, l.rate)
	}
	close(l.change)
	l.change = make(chan struct{})
	l.mu.Unlock()
}

// refill: kilit altında çağrılır.
func (l *Limiter) refill() {
	now := time.Now()
	if l.rate > 0 {
		l.tokens = min(l.tokens+now.Sub(l.last).Seconds()*l.rate, l.rate)
	}
	l.last = now
}

// measure: kilit altında; 1 sn'lik pencerelerle toplam hızı ölçer.
func (l *Limiter) measure(n int) {
	l.win += float64(n)
	if el := l.last.Sub(l.winStart); el >= time.Second {
		l.total, l.win, l.winStart = l.win/el.Seconds(), 0, l.last
	}
}

// Wait n baytlık jeton alana dek bloklar (önce kendi kovası, sonra küresel); ctx iptalinde ctx.Err() döner.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if l == nil {
		return nil
	}
	if err := l.take(ctx, n); err != nil {
		return err
	}
	return l.parent.Wait(ctx, n)
}

func (l *Limiter) take(ctx context.Context, n int) error {
	for {
		l.mu.Lock()
		l.refill()
		if l.rate == 0 {
			l.mu.Unlock()
			return ctx.Err()
		}
		need := min(float64(n), l.rate) // burst'ten büyük istek, kova doluyken borçla geçer
		if l.tokens >= need {
			l.tokens -= float64(n)
			l.measure(n)
			l.mu.Unlock()
			return ctx.Err()
		}
		wait := time.Duration((need - l.tokens) / l.rate * float64(time.Second))
		ch := l.change
		l.mu.Unlock()
		t := time.NewTimer(wait)
		select {
		case <-t.C:
		case <-ch:
			t.Stop()
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		}
	}
}

// NearLimit: toplam hız (ölçülen ya da verilen, hangisi büyükse) bu ya da küresel sınırın %85'ine
// ulaştıysa true (bu durumda bağlantı artırılmaz).
func (l *Limiter) NearLimit(currentBps float64) bool {
	if l == nil {
		return false
	}
	return l.near(currentBps) || l.parent.NearLimit(currentBps)
}

func (l *Limiter) near(currentBps float64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.winStart) < 2*time.Second { // eski ölçüm sayılmaz
		currentBps = max(currentBps, l.total)
	}
	return l.rate > 0 && currentBps >= 0.85*l.rate
}
