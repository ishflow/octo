import SwiftUI

enum SchedulerTab: String, CaseIterable, Identifiable {
    case schedule = "Zamanlama"
    case files = "Dosyalar"
    var id: String { rawValue }
}

private enum QueueNaming: Identifiable {
    case new
    case rename(Queue)
    var id: String { if case .rename(let q) = self { "rename-\(q.id)" } else { "new" } }
}

/// Scheduler / queues window: queue list on the left, schedule and file order on the right. Edits apply live (`queue.save`).
struct SchedulerWindow: View {
    @Environment(Store.self) private var store
    @State private var selected: String? = "main"
    @State private var tab: SchedulerTab
    @State private var naming: QueueNaming?
    @State private var nameText = ""
    @State private var error: String?
    @State private var confirmDelete = false

    init(tab: SchedulerTab = .schedule) { _tab = State(initialValue: tab) }

    private var queue: Queue? { store.queues.first { $0.id == selected } ?? store.queues.first }

    var body: some View {
        NavigationSplitView {
            List(selection: $selected) {
                Section("Kuyruklar") {
                    ForEach(store.queues) { q in
                        Label(q.name, systemImage: q.running ? "play.circle.fill" : "list.bullet")
                            .badge(q.items.count)
                            .accessibilityValue(q.running ? "çalışıyor" : "")
                            .tag(q.id)
                            .contextMenu {
                                Button("Yeniden adlandır…") { nameText = q.name; naming = .rename(q) }
                            }
                    }
                }
            }
            .navigationSplitViewColumnWidth(min: 180, ideal: 210, max: 280)
            .safeAreaInset(edge: .bottom) {
                GlassEffectContainer(spacing: 8) {
                    HStack(spacing: 8) {
                        Button { nameText = ""; naming = .new } label: { Label("Yeni kuyruk", systemImage: "plus") }
                            .help("Yeni kuyruk")
                        Button { confirmDelete = true } label: { Label("Kuyruğu sil", systemImage: "minus") }
                            .help("Kuyruğu sil").disabled(queue == nil || queue?.id == "main")
                        Spacer()
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.glass)
                }
                .padding(10)
            }
        } detail: {
            if let q = queue {
                detail(q)
            } else {
                ContentUnavailableView("Kuyruk yok", systemImage: "list.bullet",
                                       description: Text(store.connected ? "Yeni bir kuyruk eklemek için + düğmesini kullanın." : "Motora bağlanılamadı."))
            }
        }
        .frame(minWidth: 760, idealWidth: 920, minHeight: 520, idealHeight: 700)
        .navigationTitle("Zamanlayıcı")
        .alert(naming.map { if case .new = $0 { "Yeni kuyruk" } else { "Kuyruğu yeniden adlandır" } } ?? "",
               isPresented: Binding(get: { naming != nil }, set: { if !$0 { naming = nil } }), presenting: naming) { n in
            TextField("Kuyruk adı", text: $nameText)
            Button("Tamam") { commitName(n) }
            Button("İptal", role: .cancel) {}
        } message: { _ in Text("Kuyruk için bir ad girin.") }
        .confirmationDialog("“\(queue?.name ?? "")” kuyruğu silinsin mi?", isPresented: $confirmDelete, titleVisibility: .visible) {
            Button("Kuyruğu sil", role: .destructive) { deleteSelected() }
            Button("İptal", role: .cancel) {}
        } message: { Text("Kuyruktaki dosyalar silinmez.") }
    }

    private func detail(_ q: Queue) -> some View {
        VStack(spacing: 0) {
            HStack(spacing: 10) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(q.name).font(.headline)
                    Text(QueueStatus.subtitle(q)).font(.caption).foregroundStyle(.secondary)
                }
                Spacer()
                Picker("Bölüm", selection: $tab) {
                    ForEach(SchedulerTab.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented).labelsHidden().frame(width: 200)
                GlassEffectContainer(spacing: 8) {
                    HStack(spacing: 8) {
                        Button { Task { await store.startQueue(q.id) } } label: { Label("Şimdi başlat", systemImage: "play.fill") }
                            .buttonStyle(.glassProminent).disabled(q.running)
                        Button { Task { await store.stopQueue(q.id) } } label: { Label("Durdur", systemImage: "stop.fill") }
                            .buttonStyle(.glass).disabled(!q.running)
                    }
                }
            }
            .padding(.horizontal, 16).padding(.vertical, 12)
            Divider()
            if let error { Text(error).foregroundStyle(.red).font(.callout).padding(.bottom, 4) }
            switch tab {
            case .schedule: ScheduleForm(queue: q, save: save)
            case .files: QueueFiles(queue: q, save: save)
            }
        }
    }

    /// Applies `change` to a copy of the queue and sends the whole object.
    private func save(_ q: Queue, _ change: (inout Queue) -> Void) {
        var n = q
        change(&n)
        guard n != q else { return }
        if let p = QueueCheck.problem(n.schedule) { error = p; return }
        error = nil
        Task { do { try await store.saveQueue(n) } catch { self.error = error.localizedDescription } }
    }

    private func commitName(_ n: QueueNaming) {
        guard let name = QueueCheck.cleanName(nameText) else { error = "Kuyruk adı boş olamaz."; return }
        switch n {
        case .new:
            let id = QueueCheck.newID(existing: store.queues.map(\.id))
            // Same defaults as the main queue's schedule: 23:00 / 07:30, every day, one file at a time (classic managers: custom queues = 1).
            let q = Queue(id: id, name: name, atOnce: 1, retries: 0, items: OrEmpty(), running: false,
                          schedule: Schedule(startOn: false, start: "23:00", stopOn: false, stop: "07:30",
                                             days: OrEmpty(wrappedValue: [1, 2, 3, 4, 5, 6, 7]), onDone: "none"))
            Task {
                do { try await store.saveQueue(q); selected = id; error = nil } catch { self.error = error.localizedDescription }
            }
        case .rename(let q):
            save(q) { $0.name = name }
        }
    }

    private func deleteSelected() {
        guard let q = queue, q.id != "main" else { return }
        Task {
            do {
                try await store.deleteQueue(q.id)
                store.queues.removeAll { $0.id == q.id }
                selected = "main"
                error = nil
            } catch { self.error = error.localizedDescription }
        }
    }
}

// MARK: - Zamanlama

private struct ScheduleForm: View {
    let queue: Queue
    let save: (Queue, (inout Queue) -> Void) -> Void

    private func time(_ get: @escaping (Schedule) -> String, _ set: @escaping (inout Schedule, String) -> Void) -> Binding<Date> {
        Binding(get: { TimeOfDay.date(get(queue.schedule)) ?? Date() },
                set: { d in save(queue) { set(&$0.schedule, TimeOfDay.string(from: d)) } })
    }

    var body: some View {
        let sch = queue.schedule
        Form {
            LabeledContent {
                DatePicker("Başlama saati", selection: time({ $0.start }, { $0.start = $1 }), displayedComponents: .hourAndMinute)
                    .labelsHidden().disabled(!sch.startOn)
            } label: {
                Toggle("Başlama saati:", isOn: Binding(get: { sch.startOn }, set: { v in save(queue) { $0.schedule.startOn = v } }))
            }
            LabeledContent("Tekrar:") {
                GlassEffectContainer(spacing: 6) {
                    HStack(spacing: 6) {
                        ForEach(Array(DaySet.labels.enumerated()), id: \.offset) { i, label in
                            dayButton(label, on: sch.days.contains(i + 1), day: i + 1)
                        }
                    }
                }
            }
            .disabled(!sch.startOn && !sch.stopOn)
            LabeledContent {
                DatePicker("Bitiş saati", selection: time({ $0.stop }, { $0.stop = $1 }), displayedComponents: .hourAndMinute)
                    .labelsHidden().disabled(!sch.stopOn)
            } label: {
                Toggle("Bitiş saati:", isOn: Binding(get: { sch.stopOn }, set: { v in save(queue) { $0.schedule.stopOn = v } }))
            }
            NumberField(title: "Aynı anda indirilecek dosya:", unit: "(1–10)", value: queue.atOnce, range: QueueCheck.atOnceRange) { v in
                save(queue) { $0.atOnce = v }
            }
            NumberField(title: "Başarısız dosyada deneme:", unit: "kez (0 = sınırsız)", value: queue.retries, range: 0...99) { v in
                save(queue) { $0.retries = v }
            }
            Picker("Kuyruk bitince:", selection: Binding(get: { sch.onDone }, set: { v in save(queue) { $0.schedule.onDone = v } })) {
                Text("Bir şey yapma").tag("none")
                Text("Son dosyayı aç").tag("open")
                Text("Mac'i uyut").tag("sleep")
                Text("Mac'i kapat").tag("shutdown")
            }
            .frame(maxWidth: 360)
        }
        .formStyle(.columns)
        .environment(\.locale, Format.tr)
        .padding(.vertical, 22).padding(.trailing, 30)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .top)
    }

    @ViewBuilder private func dayButton(_ label: String, on: Bool, day: Int) -> some View {
        let b = Button(label) { save(queue) { $0.schedule.days = DaySet.toggle($0.schedule.days, day) } }
            .accessibilityLabel(["Pazartesi", "Salı", "Çarşamba", "Perşembe", "Cuma", "Cumartesi", "Pazar"][day - 1])
            .accessibilityValue(on ? "seçili" : "seçili değil")
        if on { b.buttonStyle(.glassProminent) } else { b.buttonStyle(.glass) }
    }
}

// MARK: - Kuyruktaki dosyalar

private struct QueueFiles: View {
    @Environment(Store.self) private var store
    let queue: Queue
    let save: (Queue, (inout Queue) -> Void) -> Void
    @State private var selection: String?
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Text("Aynı anda")
                TextField("Aynı anda", value: Binding(get: { queue.atOnce }, set: { v in save(queue) { $0.atOnce = QueueCheck.clampAtOnce(v) } }),
                          format: .number)
                    .labelsHidden().multilineTextAlignment(.center).frame(width: 56)
                Text("dosya indir")
                Spacer()
                GlassEffectContainer(spacing: 6) {
                    HStack(spacing: 6) {
                        Button { step(-1) } label: { Label("Yukarı taşı", systemImage: "arrow.up") }.help("Yukarı taşı")
                        Button { step(1) } label: { Label("Aşağı taşı", systemImage: "arrow.down") }.help("Aşağı taşı")
                        Button { takeOut() } label: { Label("Kuyruktan çıkar", systemImage: "minus") }
                            .help(queue.id == "main" ? "Ana kuyruktaki dosya başka bir kuyruğa taşınarak çıkarılır" : "Ana kuyruğa geri taşır")
                            .disabled(queue.id == "main" || selection == nil)
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.glass)
                    .disabled(selection == nil)
                }
            }
            if let error { Text(error).foregroundStyle(.red).font(.callout) }
            if queue.items.isEmpty {
                ContentUnavailableView("Kuyruk boş", systemImage: "tray",
                                       description: Text("Dosyaları ana listeden sağ tık ▸ Kuyruğa taşı ile ekleyin."))
            } else {
                List(selection: $selection) {
                    Section {
                        ForEach(Array(queue.items.enumerated()), id: \.element) { i, id in
                            let item = store.items.first { $0.id == id }
                            HStack {
                                Text("\(i + 1)").monospacedDigit().foregroundStyle(.secondary).frame(width: 28, alignment: .leading)
                                Text(item?.name ?? id).lineLimit(1).truncationMode(.middle)
                                Spacer()
                                Text(item.map { Format.bytes($0.size) } ?? "").monospacedDigit().frame(width: 80, alignment: .leading)
                                Text(label(item?.state)).foregroundStyle(tint(item?.state)).frame(width: 130, alignment: .leading)
                            }
                            .tag(id)
                        }
                        .onMove { from, to in save(queue) { $0.items.move(fromOffsets: from, toOffset: to) } }
                    } header: {
                        HStack {
                            Text("#").frame(width: 28, alignment: .leading)
                            Text("Dosya adı")
                            Spacer()
                            Text("Boyut").frame(width: 80, alignment: .leading)
                            Text("Durum").frame(width: 130, alignment: .leading)
                        }
                    }
                }
                .listStyle(.bordered(alternatesRowBackgrounds: true))
                Text("Dosyaları sürükleyerek sıralayabilirsiniz; kuyruk yukarıdan aşağıya çalışır.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.leading, 24).padding(.trailing, 30).padding(.vertical, 22)
    }

    private func step(_ by: Int) {
        guard let id = selection else { return }
        save(queue) { $0.items = QueueOrder.move($0.items, id, by: by) }
    }

    private func takeOut() {
        guard let id = selection, queue.id != "main" else { return }
        selection = nil
        error = nil
        Task { do { try await store.moveToQueue([id], queue: "main") } catch { self.error = error.localizedDescription } }
    }

    private func tint(_ s: ItemState?) -> Color {
        switch s {
        case .downloading: .accentColor
        case .paused: .orange
        case .done: .green
        case .error: .red
        default: .secondary
        }
    }

    private func label(_ s: ItemState?) -> String {
        switch s {
        case .queued: "Kuyrukta"
        case .downloading: "Veri alınıyor…"
        case .paused: "Durduruldu"
        case .done: "Tamamlandı"
        case .error: "Hata"
        case nil: ""
        }
    }
}
