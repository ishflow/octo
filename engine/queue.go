package engine

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"time"
)

// Kuyruk motoru: klasik indirme yöneticisi kuyruk davranışı.
// Öğeler yukarıdan aşağı atOnce'a kadar çalışır; başarısız öğe sona taşınır, deneme sayısı
// bellekte tutulur (0 = sınırsız). Kuyruk kendiliğinden biterse onDone çalışır, elle durdurulursa çalışmaz.

// eligible: kuyruk bu öğeyi başlatabilir mi. Duraklatılan öğe kullanıcının kararıdır, atlanır.
func (m *Manager) eligible(q *Queue, it *Item) bool {
	switch it.State {
	case StateQueued:
		return true
	case StateError:
		return q.Retries == 0 || m.retries[it.ID] < q.Retries
	}
	return false
}

// pump çalışan kuyruğu doldurur; bittiyse durdurup onDone'u tetikler (m.mu tutulurken).
func (m *Manager) pump(q *Queue) {
	if !q.Running || m.closed {
		return
	}
	active := 0
	for _, id := range slices.Clone(q.Items) {
		if active >= max(q.AtOnce, 1) {
			break
		}
		it := m.item(id)
		if it == nil {
			continue
		}
		if r := m.runs[id]; r != nil {
			if r.byQueue { // elle başlatılan öğe kuyruğun atOnce yerini yemez
				active++
			}
			continue
		}
		if m.cool[id] || !m.eligible(q, it) {
			continue
		}
		m.start(it, true)
		m.ran[q.ID] = true
		m.emitItem(it)
		active++
	}
	if m.finished(q) {
		m.stopQueue(q, "done")
	}
}

func (m *Manager) pumpAll() {
	for _, q := range m.queues {
		m.pump(q)
	}
}

// finished: çalışan ya da başlatılabilecek öğe kalmadı.
func (m *Manager) finished(q *Queue) bool {
	for _, id := range q.Items {
		if it := m.item(id); it != nil && (m.runs[id] != nil || m.eligible(q, it)) {
			return false
		}
	}
	return true
}

// failed kuyruğun başlattığı öğe hata verince: sayaç artar, öğe sona gider, sonraki tick'e kadar bekler.
func (m *Manager) failed(it *Item) bool {
	q := m.queue(it.Queue)
	if q == nil {
		return false
	}
	i := slices.Index(q.Items, it.ID)
	if i < 0 {
		return false
	}
	m.retries[it.ID]++
	m.cool[it.ID] = true // başarısız öğe ancak sonraki tick'te döner (anında hata döngüsü olmaz)
	q.Items = append(slices.Delete(q.Items, i, i+1), it.ID)
	return true
}

// stopQueue kuyruğu durdurur; kuyruğun başlattığı indirmeler yeniden "sırada" olur.
// why: "done" (kuyruk bitti → onDone çalışır), "user" ya da "schedule" (durma saati: devam
// ettirilemeyen iş kesilirse baştan iner, bu yüzden bitene dek sürer; kuyruk yine de durur).
func (m *Manager) stopQueue(q *Queue, why string) {
	q.Running = false
	for _, id := range q.Items {
		if r := m.runs[id]; r != nil && r.byQueue && (why != "schedule" || r.job == nil || r.job.Snapshot().Resumable) {
			m.stopRun(r, "queue")
		}
	}
	logErr("kuyruk kaydı", m.saveQueues())
	m.emitQueues()
	ran, cut, last := m.ran[q.ID], m.cut[q.ID], m.lastFile[q.ID]
	delete(m.ran, q.ID)
	delete(m.cut, q.ID)
	delete(m.lastFile, q.ID)
	// onDone yalnız kuyruğun bütün öğeleri bitince (biten öğe kuyruktan çıkar → Items boş).
	// Duraklatılan/başarısız öğe kalırsa ya da kullanıcı bitmemiş öğeyi çıkardıysa kuyruk kullanıcıca durmuş sayılır.
	if why != "done" || !ran || cut || len(q.Items) > 0 {
		return
	}
	switch q.Schedule.OnDone {
	case "open":
		if last != "" {
			go func() { logErr("açma", m.d.Run("open", last)) }()
		}
	case "sleep":
		m.startAction("sleep", "pmset", "sleepnow")
	case "shutdown":
		m.startAction("shutdown", "osascript", "-e", `tell application "System Events" to shut down`)
	}
}

// startAction geri sayımı başlatır; CancelAction gelmezse süre sonunda komutu çalıştırır.
func (m *Manager) startAction(action, name string, args ...string) {
	m.cancelAction()
	gen := m.actionGen
	m.actionName, m.actionAt = action, time.Now().Add(m.actionDelay)
	m.emit(Event{Event: "action", Action: action, Seconds: int(actionDelay.Seconds())})
	if len(m.subs) == 0 { // uygulama kapalı: açılınca geri sayımı subscribe/list'ten görür
		go func() { logErr("uygulamayı açma", m.d.Run("open", "-b", "com.uba.octo")) }()
	}
	m.actionTimer = time.AfterFunc(m.actionDelay, func() {
		m.mu.Lock()
		ok := m.actionGen == gen && !m.closed
		if ok {
			m.actionTimer, m.actionName = nil, ""
		}
		m.mu.Unlock()
		if ok {
			logErr(action, m.d.Run(name, args...))
		}
	})
}

func (m *Manager) cancelAction() {
	m.actionGen++
	m.actionName = ""
	if m.actionTimer != nil {
		m.actionTimer.Stop()
		m.actionTimer = nil
	}
}

// PendingAction süren geri sayımı ve kalan saniyeyi verir (yoksa "").
func (m *Manager) PendingAction() (string, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pendingAction()
}

func (m *Manager) pendingAction() (string, int) {
	if m.actionName == "" {
		return "", 0
	}
	return m.actionName, max(1, int(math.Ceil(time.Until(m.actionAt).Seconds())))
}

// CancelAction uyku/kapatma geri sayımını iptal eder.
func (m *Manager) CancelAction() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cancelAction()
}

// StartQueue kuyruğu başlatır ve deneme sayaçlarını sıfırlar.
func (m *Manager) StartQueue(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if q := m.queue(id); q != nil {
		m.startQueue(q)
	}
}

func (m *Manager) startQueue(q *Queue) {
	q.Running = true
	for _, id := range q.Items {
		delete(m.retries, id)
		delete(m.cool, id)
	}
	delete(m.cut, q.ID)
	logErr("kuyruk kaydı", m.saveQueues())
	m.emitQueues()
	m.pump(q)
}

// StopQueue kuyruğu elle durdurur (onDone çalışmaz).
func (m *Manager) StopQueue(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if q := m.queue(id); q != nil && q.Running {
		m.stopQueue(q, "user")
	}
}

// SaveQueue kuyruğu ekler (ID boş ya da bilinmiyorsa) ya da günceller. Running buradan değişmez;
// Items yalnız sıralama içindir (üyelik MoveToQueue ile değişir).
func (m *Manager) SaveQueue(in Queue) error {
	if msg := m.blockedMsg(); msg != "" { // store yazmaz: "ok" dönüp açılışta kaybolmasın
		return errors.New(msg)
	}
	def := defaultSchedule()
	if in.Schedule.Start == "" {
		in.Schedule.Start = def.Start
	}
	if in.Schedule.Stop == "" {
		in.Schedule.Stop = def.Stop
	}
	if in.Schedule.OnDone == "" {
		in.Schedule.OnDone = def.OnDone
	}
	if in.Schedule.Days == nil {
		in.Schedule.Days = def.Days
	}
	if _, _, ok := parseHM(in.Schedule.Start); !ok {
		return errors.New("geçersiz başlangıç saati")
	}
	if _, _, ok := parseHM(in.Schedule.Stop); !ok {
		return errors.New("geçersiz bitiş saati")
	}
	if !slices.Contains([]string{"none", "open", "sleep", "shutdown"}, in.Schedule.OnDone) {
		return errors.New("geçersiz bitince seçeneği")
	}
	in.AtOnce = min(max(in.AtOnce, 1), 10) // özel kuyruk varsayılanı 1; uygulama da 1–10 sınırlar
	if in.Retries < 0 {
		in.Retries = 0
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queue(in.ID)
	if q == nil { // yeni kuyruk: uygulama kimliği kendisi seçer (SchedulerWindow), boşsa servis verir
		id := in.ID
		for n := len(m.queues); id == "" || m.queue(id) != nil; n++ {
			id = fmt.Sprintf("q%d", n)
		}
		q = &Queue{ID: id, Items: []string{}}
		m.queues = append(m.queues, q)
	}
	order := slices.DeleteFunc(slices.Clone(in.Items), func(id string) bool { return !slices.Contains(q.Items, id) })
	for _, id := range q.Items {
		if !slices.Contains(order, id) {
			order = append(order, id)
		}
	}
	q.Name, q.AtOnce, q.Retries, q.Schedule, q.Items = in.Name, in.AtOnce, in.Retries, in.Schedule, order
	if err := m.saveQueues(); err != nil {
		return err
	}
	m.emitQueues()
	m.pump(q)
	return nil
}

// DeleteQueue özel kuyruğu siler; üyeleri ana kuyruğun sonuna taşınır (dosyalar kalır).
func (m *Manager) DeleteQueue(id string) error {
	if id == mainQueue {
		return errors.New("ana kuyruk silinemez")
	}
	if msg := m.blockedMsg(); msg != "" { // store yazmaz: "ok" dönüp açılışta kaybolmasın
		return errors.New(msg)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	q := m.queue(id)
	if q == nil {
		return errors.New("kuyruk bulunamadı")
	}
	if q.Running {
		m.stopQueue(q, "user")
	}
	mq := m.queue(mainQueue)
	mq.Items = append(mq.Items, q.Items...)
	for _, it := range m.items {
		if it.Queue == id {
			it.Queue = mainQueue
			m.emitItem(it)
		}
	}
	m.queues = slices.DeleteFunc(m.queues, func(x *Queue) bool { return x == q })
	logErr("indirme kaydı", m.saveItems())
	if err := m.saveQueues(); err != nil {
		return err
	}
	m.emitQueues()
	m.pumpAll()
	return nil
}

// MoveToQueue öğeleri başka kuyruğun sonuna taşır ("Kuyruğa taşı"). Bitmiş öğe yalnız etiket alır.
func (m *Manager) MoveToQueue(ids []string, queue string) error {
	if msg := m.blockedMsg(); msg != "" { // store yazmaz: "ok" dönüp açılışta kaybolmasın
		return errors.New(msg)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	to := m.queue(queue)
	if to == nil {
		return errors.New("kuyruk bulunamadı")
	}
	for _, id := range ids {
		it := m.item(id)
		if it == nil {
			continue
		}
		for _, q := range m.queues {
			if i := slices.Index(q.Items, id); i >= 0 {
				q.Items = slices.Delete(q.Items, i, i+1)
				if q.Running && q != to && m.runs[id] != nil { // hiç başlamamış öğeyi taşımak onDone'u engellemez
					m.cut[q.ID] = true // kullanıcı bitmemiş öğeyi çalışan kuyruktan aldı
				}
			}
		}
		delete(m.retries, id)
		delete(m.cool, id)
		it.Queue = to.ID
		if it.State != StateDone {
			to.Items = append(to.Items, id)
		}
		m.emitItem(it)
	}
	logErr("indirme kaydı", m.saveItems())
	if err := m.saveQueues(); err != nil {
		return err
	}
	m.emitQueues()
	m.pumpAll()
	return nil
}
