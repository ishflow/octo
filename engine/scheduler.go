package engine

import (
	"slices"
	"strconv"
	"strings"
	"time"
)

// Zamanlayıcı: klasik indirme yöneticisi başlat/durdur davranışı.
// 30 sn'de bir tick; başlangıç/bitiş tam dakikada eşleşir, 59 sn içinde ikinci başlatma yok.

const fireGuard = 59 * time.Second

func parseHM(s string) (h, m int, ok bool) {
	a, b, found := strings.Cut(s, ":")
	h, err1 := strconv.Atoi(a)
	m, err2 := strconv.Atoi(b)
	return h, m, found && err1 == nil && err2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60
}

func atMinute(now time.Time, hm string) bool {
	h, m, ok := parseHM(hm)
	return ok && now.Hour() == h && now.Minute() == m
}

func (m *Manager) shouldStart(q *Queue, now time.Time) bool {
	s := q.Schedule
	if !s.StartOn {
		return false
	}
	if last, ok := m.lastFired[q.ID]; ok && now.Sub(last) < fireGuard {
		return false
	}
	wd := int(now.Weekday()) // Pazar=0 → 7
	if wd == 0 {
		wd = 7
	}
	if !slices.Contains(s.Days, wd) || !atMinute(now, s.Start) {
		return false
	}
	m.lastFired[q.ID] = now
	return true
}

// Tick zamanlayıcı adımıdır (30 sn'de bir çağrılır; testler sahte saatle doğrudan çağırır).
// Başarısız öğelerin beklemesi de burada biter.
func (m *Manager) Tick() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return
	}
	now := m.d.Now()
	for _, q := range m.queues {
		if m.shouldStart(q, now) {
			if !q.Running {
				m.startQueue(q)
			}
		} else if q.Running && q.Schedule.StopOn && atMinute(now, q.Schedule.Stop) {
			m.stopQueue(q, "schedule")
		}
	}
	clear(m.cool)
	m.pumpAll()
}
