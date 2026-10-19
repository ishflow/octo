import AppKit
import SwiftUI
import UniformTypeIdentifiers

// shortcut: brand accent until the AccentColor asset lands (DZ1); then use Color.accentColor instead.
/// Octo indigo: #5B3FD6 light, #6E59EE dark (the logo's #7A66F2 darkened to 4.85:1 against white text).
let brandAccent = Color(nsColor: NSColor(name: nil) { a in
    a.bestMatch(from: [.aqua, .darkAqua]) == .darkAqua
        ? NSColor(srgbRed: 0x6E / 255, green: 0x59 / 255, blue: 0xEE / 255, alpha: 1)
        : NSColor(srgbRed: 0x5B / 255, green: 0x3F / 255, blue: 0xD6 / 255, alpha: 1)
})

/// Distinct, accessible connection colors (system colors adapt to light/dark and Increase Contrast).
private let connColors: [Color] = [brandAccent, .green, .orange, .blue, .pink, .teal, .yellow, .brown]
private func connColor(_ i: Int) -> Color { connColors[i % connColors.count] }

private func statusText(_ i: Item) -> String {
    switch i.state {
    case .queued: "Kuyrukta"
    case .downloading: StreamProgress.merging(i) ? "Birleştiriliyor…" : "Veri alınıyor…"
    case .paused: "Durduruldu"
    case .done: "Tamamlandı"
    case .error: i.error.isEmpty ? "Hata" : i.error
    }
}

/// Status tone: green done, orange paused, red error.
func statusColor(_ s: ItemState) -> Color {
    switch s {
    case .done: .green
    case .paused: .orange
    case .error: .red
    case .queued, .downloading: .primary
    }
}

extension FileIcon {
    /// The file's own Finder icon once it exists, else the icon of its type.
    @MainActor static func image(_ i: Item) -> NSImage {
        let p = FileActions.path(i).path
        if FileManager.default.fileExists(atPath: p) { return NSWorkspace.shared.icon(forFile: p) }
        return NSWorkspace.shared.icon(for: UTType(filenameExtension: (i.name as NSString).pathExtension) ?? .data)
    }
}

extension FileActions {
    @MainActor static func openWith(_ i: Item) {
        let p = NSOpenPanel()
        p.directoryURL = URL(fileURLWithPath: "/Applications")
        p.allowedContentTypes = [.application]
        p.prompt = "Aç"
        p.message = "“\(i.name)” dosyasını açacak uygulamayı seçin"
        guard p.runModal() == .OK, let app = p.url else { return }
        NSWorkspace.shared.open([path(i)], withApplicationAt: app, configuration: NSWorkspace.OpenConfiguration())
    }
}

/// Thick rounded progress bar; indeterminate (native) when the fraction is unknown.
struct ThickBar: View {
    let fraction: Double?
    var height: CGFloat = 10
    /// Spoken after the percent, e.g. "5,21 GB / 8,54 GB".
    var detail: String?

    var body: some View {
        if let f = fraction {
            Capsule().fill(.quaternary)
                .overlay(alignment: .leading) {
                    GeometryReader { g in Capsule().fill(.tint).frame(width: g.size.width * min(max(f, 0), 1)) }
                }
                .frame(height: height)
                .accessibilityElement()
                .accessibilityLabel("İlerleme")
                .accessibilityValue([Format.percent(f), detail].compactMap { $0 }.joined(separator: ", "))
        } else {
            ProgressView().progressViewStyle(.linear)
        }
    }
}

// MARK: - Bitince (per download, kept in the app: the engine's onDone is per queue)

// The completion notification (with Aç / Finder'da göster) is always posted by Notifier; only "open" is per download.
extension FinishActions {
    @MainActor static func run(_ item: Item) {
        if opensOnFinish(item, wanted: UserDefaults.standard.bool(forKey: openKey(item.id))) { FileActions.open(item) }
    }

    /// A file the engine could not quarantine is never opened on its own: Gatekeeper would not ask first.
    static func opensOnFinish(_ item: Item, wanted: Bool) -> Bool { wanted && item.quarantineFailed != true }

    /// Off: the progress window closes itself when the download finishes (the notification remains).
    static let showCompleteKey = "showCompleteWindow"
}

// MARK: - İndirme penceresi

enum ProgressTab: Hashable { case status, limit, finish }

struct ProgressWindow: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    @AppStorage(FinishActions.showCompleteKey) private var showComplete = true
    let id: String
    @State private var tab: ProgressTab
    @State private var details = true

    init(id: String, tab: ProgressTab = .status) {
        self.id = id
        _tab = State(initialValue: tab)
    }

    var body: some View {
        if let item = store.items.first(where: { $0.id == id }) {
            Group {
                if item.state == .done { CompleteView(item: item) } else { content(item) }
            }
            .navigationTitle(title(item))
            .tint(brandAccent)
            .onChange(of: item.state) { _, s in if s == .done && !showComplete { dismiss() } }
        } else {
            ContentUnavailableView("İndirme bulunamadı", systemImage: "questionmark.circle",
                                   description: Text("Bu indirme listeden kaldırılmış."))
                .frame(width: 560, height: 300)
        }
    }

    private func title(_ i: Item) -> String {
        guard i.state != .done, let f = i.fraction else { return i.name }
        return "\(Format.percent(f)) · \(i.name)"
    }

    private func content(_ item: Item) -> some View {
        VStack(spacing: 0) {
            Picker("Sekme", selection: $tab) {
                Text("Durum").tag(ProgressTab.status)
                Text("Hız sınırı").tag(ProgressTab.limit)
                Text("Bitince").tag(ProgressTab.finish)
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .fixedSize()
            .padding(.bottom, 14)

            Group {
                switch tab {
                case .status: StatusTab(item: item)
                case .limit: SpeedLimitTab(item: item).padding(.horizontal, 40)
                case .finish: FinishTab(item: item).padding(.horizontal, 40)
                }
            }
            .frame(maxWidth: .infinity, minHeight: 196, alignment: .topLeading)
            .padding(.horizontal, 28)

            VStack(spacing: 16) {
                ThickBar(fraction: item.fraction, detail: item.size > 0 ? "\(Format.bytes(item.got)) / \(Format.bytes(item.size))" : nil)
                buttons(item)
            }
            .padding(.horizontal, 28)

            if details {
                Divider().padding(.top, 16)
                DetailsPanel(item: item).padding(.horizontal, 28).padding(.top, 14)
            }
        }
        .padding(.top, 8)
        .padding(.bottom, 20)
        .frame(width: 600)
    }

    private func buttons(_ item: Item) -> some View {
        HStack(spacing: 8) {
            Button(details ? "Ayrıntıları gizle" : "Ayrıntıları göster",
                   systemImage: details ? "chevron.down" : "chevron.right") { details.toggle() }
                .buttonStyle(.borderless)
                .accessibilityValue(details ? "açık" : "kapalı")
            Spacer()
            GlassEffectContainer(spacing: 8) {
                HStack(spacing: 8) {
                    if item.state == .paused || item.state == .error {
                        Button("Devam") { Task { await store.resume([item.id]) } }.buttonStyle(.glassProminent)
                    } else {
                        Button("Durdur") { Task { await store.pause([item.id]) } }.buttonStyle(.glass)
                    }
                    // Cancel: stop the download and close the window; the item stays in the list.
                    Button("İptal") {
                        guard item.state == .downloading || item.state == .queued else { dismiss(); return }
                        Task { if await store.pause([item.id]) { dismiss() } }
                    }
                    .buttonStyle(.glass)
                    .keyboardShortcut(.cancelAction)
                }
            }
        }
    }
}

private struct StatusTab: View {
    let item: Item

    var body: some View {
        Form {
            LabeledContent("Adres") { Text(item.url).lineLimit(1).truncationMode(.middle).textSelection(.enabled) }
            LabeledContent("Durum") { Text(statusText(item)).foregroundStyle(statusColor(item.state)) }
            LabeledContent("Dosya boyutu") { Text(item.size < 0 ? "(bilinmiyor)" : Format.bytes(item.size)) }
            LabeledContent("İndirilen") {
                if let f = item.fraction {
                    Text("\(Format.bytes(item.got)) (\(Format.percent(f, digits: 1)))")
                } else {
                    Text(Format.bytes(item.got))
                }
            }
            LabeledContent("Hız") { Text(Format.speed(item.speed, none: "—")) }
            LabeledContent("Kalan süre") { Text(item.eta < 0 ? "(bilinmiyor)" : Format.eta(item.eta)) }
            if let s = item.stream {
                if let q = s.label { LabeledContent("Kalite") { Text(q) } }
                if let line = StreamProgress.line(s) { LabeledContent("Parça") { Text(line) } }
            }
            LabeledContent("Devam ettirilebilir") {
                Text(item.resumable ? "Evet" : "Hayır").foregroundStyle(item.resumable ? .green : .orange)
            }
            if let n = item.note, !n.isEmpty { LabeledContent("Not") { Text(n).foregroundStyle(.orange) } }
        }
        .formStyle(.columns)
        .monospacedDigit()
    }
}

/// Per-file limit (`item.limit`); the global limit from the toolbar popover still applies on top of it.
private struct SpeedLimitTab: View {
    @Environment(Store.self) private var store
    let item: Item
    @State private var kbps = 0
    @State private var error: String?

    private var limit: Int { item.speedLimitKBps ?? 0 }
    private var global: Int? { store.settings.flatMap { $0.speedLimitOn ? $0.speedLimitKBps : nil } }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Toggle(isOn: Binding(get: { limit > 0 }, set: { on in save(on ? max(kbps, 1) : 0) })) {
                Text("Bu dosya için hız sınırı").frame(maxWidth: .infinity, alignment: .leading)
            }
            .toggleStyle(.switch)
            HStack(spacing: 8) {
                Text("En fazla:").foregroundStyle(.secondary).frame(width: 90, alignment: .leading)
                TextField("KB/s", value: $kbps, format: .number.locale(Format.tr))
                    .frame(width: 100).multilineTextAlignment(.trailing)
                    .onSubmit { kbps >= 1 ? save(kbps) : (error = SpeedLimit.rangeMessage) }
                    .accessibilityLabel("Bu dosya için en fazla hız, KB/s")
                Text("KB/s").foregroundStyle(.secondary)
            }
            .disabled(limit == 0)
            HStack(spacing: 8) {
                Text("Şu anki hız:").foregroundStyle(.secondary).frame(width: 90, alignment: .leading)
                Text(Format.speed(item.speed, none: "—") + ((limit > 0 || global != nil) && item.speed > 0 ? " (sınırlı)" : "")).monospacedDigit()
            }
            if let error { Text(error).foregroundStyle(.red) }
            Group {
                if let global {
                    Text("Tüm indirmelerin toplamına \(global.formatted(.number.locale(Format.tr))) KB/s sınırı da uygulanır.")
                }
                Text("Çok düşük hızlarda bazı sunucular bağlantıyı kesebilir.")
            }
            .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(.top, 6)
        .onAppear { kbps = limit > 0 ? limit : (store.settings?.speedLimitKBps ?? 500) }
        .onChange(of: limit) { if limit > 0 { kbps = limit } }
    }

    private func save(_ k: Int) {
        error = nil
        if k > 0, let p = SpeedLimit.problem(k) { error = p; return }
        Task {
            do { try await store.setItemLimit(item.id, kbps: k) } catch { self.error = error.localizedDescription }
        }
    }
}

private struct FinishTab: View {
    let item: Item
    @AppStorage private var openFile: Bool

    init(item: Item) {
        self.item = item
        _openFile = AppStorage(wrappedValue: false, FinishActions.openKey(item.id))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 8) {
                Text("Kaydetme yeri:").foregroundStyle(.secondary).frame(width: 110, alignment: .leading)
                Text(Format.folder(item.dir)).lineLimit(1).truncationMode(.head)
            }
            Toggle("Bitince dosyayı aç", isOn: $openFile).toggleStyle(.checkbox)
            Text("Bitince bildirim her zaman gösterilir. Bitince Mac'in uyuması ya da kapanması kuyruğun ayarından gelir (Zamanlayıcı).")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
        }
        .padding(.top, 6)
    }
}

// MARK: - Bitti penceresi (the progress window turns into it)

private struct CompleteView: View {
    @Environment(\.dismiss) private var dismiss
    @AppStorage(FinishActions.showCompleteKey) private var showComplete = true
    let item: Item

    var body: some View {
        let size = item.size >= 0 ? item.size : item.got
        VStack(alignment: .leading, spacing: 16) {
            HStack(spacing: 16) {
                Image(nsImage: FileIcon.image(item)).resizable().frame(width: 64, height: 64)
                    .onDrag { NSItemProvider(contentsOf: FileActions.path(item)) ?? NSItemProvider() }
                    .help("Dosyayı sürükleyip bırakabilirsiniz")
                    .accessibilityLabel(item.name)
                VStack(alignment: .leading, spacing: 2) {
                    Text("İndirme tamamlandı").font(.title3.weight(.semibold))
                    Text("\(Format.bytes(size)) indirildi (\(Format.grouped(size)) bayt)").foregroundStyle(.secondary)
                    Text("Simgeyi bir uygulamaya ya da klasöre sürükleyebilirsiniz.").font(.caption).foregroundStyle(.secondary)
                    if let n = item.note, !n.isEmpty { Text(n).font(.caption).foregroundStyle(.orange) }
                }
            }
            Form {
                LabeledContent("Adres") { Text(item.url).lineLimit(1).truncationMode(.middle).textSelection(.enabled) }
                LabeledContent("Kaydedildi") {
                    Text((FileActions.path(item).path as NSString).abbreviatingWithTildeInPath)
                        .lineLimit(1).truncationMode(.middle).textSelection(.enabled)
                }
            }
            .formStyle(.columns)
            .font(.callout)
            HStack(spacing: 8) {
                Toggle("Bir daha gösterme", isOn: Binding(get: { !showComplete }, set: { showComplete = !$0 }))
                    .toggleStyle(.checkbox).font(.callout)
                    .help("İndirme bitince pencere kendiliğinden kapanır; bildirim gelmeye devam eder.")
                Spacer()
                GlassEffectContainer(spacing: 8) {
                    HStack(spacing: 8) {
                        Button("Klasörü aç") { FileActions.reveal(item) }.buttonStyle(.glass)
                        Button("Birlikte aç…") { FileActions.openWith(item) }.buttonStyle(.glass)
                        Button("Aç") { FileActions.open(item); dismiss() }
                            .buttonStyle(.glassProminent).keyboardShortcut(.defaultAction)
                    }
                }
            }
        }
        .padding(.horizontal, 22)
        .padding(.top, 8)
        .padding(.bottom, 22)
        .frame(width: 460)
    }
}

// MARK: - Ayrıntılar: bağlantı çubuğu ve tablosu

private struct ConnRow: Identifiable {
    var id: Int
    var conn: Conn
}

private struct DetailsPanel: View {
    let item: Item

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let s = item.stream {
                SegmentBar(spec: s)
            } else {
                connections
            }
        }
    }

    @ViewBuilder private var connections: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Bağlantılara göre başlangıç noktaları ve ilerleme").font(.caption).foregroundStyle(.secondary)
            ConnectionBar(conns: item.conns, size: item.size)
            Table(item.conns.enumerated().map { ConnRow(id: $0.offset, conn: $0.element) }) {
                TableColumn("N.") { r in
                    HStack(spacing: 6) {
                        RoundedRectangle(cornerRadius: 2).fill(connColor(r.id)).frame(width: 8, height: 8)
                        Text("\(r.id + 1)")
                    }
                }
                .width(60)
                TableColumn("İndirilen") { r in Text(Format.bytes(r.conn.downloaded)).monospacedDigit() }
                    .width(120)
                TableColumn("Durum") { r in
                    let done = finished(r.conn)
                    Text(done ? "Bitti" : status(r.conn)).foregroundStyle(done ? .green : statusColor(item.state))
                }
            }
            .alternatingRowBackgrounds()
            .frame(height: 220)
        }
    }

    private func finished(_ c: Conn) -> Bool { c.end >= c.start && c.pos > c.end }

    /// The engine's per-connection event only means something while downloading.
    private func status(_ c: Conn) -> String {
        if item.state == .downloading, let s = c.status, !s.isEmpty { return s }
        return info
    }

    private var info: String {
        switch item.state {
        case .downloading: "Veri alınıyor…"
        case .queued: "Bekliyor"
        case .paused: "Durduruldu"
        case .done: "Bitti"
        case .error: item.error.isEmpty ? "Hata" : item.error
        }
    }
}

/// Videos download in segments, not byte ranges: one bar of finished segments instead of the connection bar.
private struct SegmentBar: View {
    let spec: StreamSpec

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Parçalara göre ilerleme").font(.caption).foregroundStyle(.secondary)
            ThickBar(fraction: StreamProgress.fraction(spec), height: 18, detail: StreamProgress.line(spec))
            Text(StreamProgress.line(spec) ?? "Parça sayısı henüz bilinmiyor")
                .font(.caption2).foregroundStyle(.secondary).monospacedDigit()
        }
    }
}

private struct ConnectionBar: View {
    let conns: [Conn]
    let size: Int64

    var body: some View {
        let slices = ConnBar.slices(conns, size: size)
        VStack(spacing: 4) {
            Canvas { ctx, sz in
                ctx.fill(Path(roundedRect: CGRect(origin: .zero, size: sz), cornerRadius: 5), with: .color(.secondary.opacity(0.15)))
                for (i, s) in slices.enumerated() {
                    let c = connColor(i)
                    let x = s.start * sz.width
                    ctx.fill(Path(CGRect(x: x, y: 0, width: (s.end - s.start) * sz.width, height: sz.height)), with: .color(c.opacity(0.25)))
                    ctx.fill(Path(CGRect(x: x, y: 0, width: (s.pos - s.start) * sz.width, height: sz.height)), with: .color(c))
                    ctx.fill(Path(CGRect(x: x, y: 0, width: 1, height: sz.height)), with: .color(.primary.opacity(0.35)))  // start mark
                }
            }
            .frame(height: 18)
            .clipShape(.rect(cornerRadius: 5))
            .accessibilityElement()
            .accessibilityLabel("Bağlantı parçaları")
            .accessibilityValue("\(slices.count) bağlantı")
            HStack {
                if size > 0 {
                    ForEach(0..<5) { q in
                        if q > 0 { Spacer() }
                        Text(q == 0 ? "0" : Format.bytes(size * Int64(q) / 4))
                    }
                } else {
                    Text("0")
                    Spacer()
                    Text("(boyut bilinmiyor)")
                }
            }
            .font(.caption2).foregroundStyle(.secondary).monospacedDigit()
        }
    }
}

// MARK: - Bilgi paneli (Adres → `refresh`, Açıklama → `item.describe`)

struct InfoPanel: View {
    @Environment(Store.self) private var store
    let item: Item?

    var body: some View {
        if let item {
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    header(item)
                    InfoSection("Genel") {
                        InfoRow("Tür") { Text(kind(item)) }
                        InfoRow("Durum") { Text(state(item)).foregroundStyle(statusColor(item.state)) }
                        if let r = Self.restartNote(item) { InfoRow("Devam") { Text(r).foregroundStyle(.orange) } }
                        if let n = item.note, !n.isEmpty { InfoRow("Not") { Text(n).foregroundStyle(.orange) } }
                        InfoRow("Boyut") {
                            Text(item.size < 0 ? "(bilinmiyor)"
                                 : "\(Format.grouped(item.size)) bayt (\(Format.bytes(item.size)))")
                        }
                        InfoRow("Eklenme") { Text(Format.date(item.added)) }
                        if let f = item.finished { InfoRow("Bitiş") { Text(Format.date(f)) } }
                        InfoRow("Son deneme") { Text(item.lastTry.map { Format.date($0) } ?? "—") }
                    }
                    InfoSection("Kaynak") {
                        InfoRow("Adres") {
                            HStack(alignment: .firstTextBaseline, spacing: 4) {
                                // Same rule as "Bağlantıyı yenile…": the new address must report the same size.
                                EditField(value: item.url) { try await store.refresh(id: item.id, url: $0) }.id(item.id)
                                Button("Kopyala", systemImage: "doc.on.doc") {
                                    NSPasteboard.general.clearContents()
                                    NSPasteboard.general.setString(item.url, forType: .string)
                                }
                                .labelStyle(.iconOnly)
                                .buttonStyle(.borderless)
                                .help("Adresi kopyala")
                            }
                        }
                        InfoRow("Yönlendiren") { Text(item.referrer ?? "—").lineLimit(1).truncationMode(.middle).textSelection(.enabled) }
                        InfoRow("Kaynak sayfa") {
                            if let page = Self.pageLink(item.pageUrl) {
                                Link(destination: page) { Text(page.absoluteString).lineLimit(1).truncationMode(.middle) }
                                    .help(page.absoluteString)
                            } else {
                                Text("Bilinmiyor").foregroundStyle(.secondary)
                            }
                        }
                    }
                    InfoSection("Kaydetme yeri") {
                        InfoRow("Klasör") {
                            Text((item.dir as NSString).abbreviatingWithTildeInPath)
                                .lineLimit(1).truncationMode(.head).textSelection(.enabled)
                        }
                    }
                    InfoSection("Açıklama") {
                        EditField(value: item.description, prompt: "Açıklama ekleyin", multiline: true) {
                            try await store.describe(id: item.id, text: $0)
                        }
                        .id(item.id)  // a draft never follows the selection to another download
                    }
                    InfoSection("Kimlik doğrulama", expanded: false) {
                        Text(login(item).map { "Bu site için kayıtlı giriş: \($0.user)" } ?? "Bu site için kayıtlı giriş yok.")
                            .foregroundStyle(.secondary)
                    }
                    InfoSection("Bağlantılar", trailing: "dosya başına en fazla \(item.maxConns)") {
                        Text(item.state == .downloading ? "\(item.conns.count) bağlantı açık" : "Etkin bağlantı yok")
                            .foregroundStyle(.secondary)
                    }
                    GlassEffectContainer(spacing: 8) {
                        HStack(spacing: 8) {
                            Spacer()
                            Button("Finder'da göster") { FileActions.reveal(item) }.buttonStyle(.glass)
                            Button("Aç") { FileActions.open(item) }.buttonStyle(.glass).disabled(item.state != .done)
                        }
                    }
                    .padding(.top, 10)
                }
                .font(.callout)
                .padding(16)
            }
            .tint(brandAccent)
        } else {
            ContentUnavailableView("Seçim yok", systemImage: "info.circle",
                                   description: Text("Bilgisini görmek için tek bir indirme seçin."))
        }
    }

    private func header(_ i: Item) -> some View {
        HStack(spacing: 12) {
            Image(nsImage: FileIcon.image(i)).resizable().frame(width: 44, height: 44)
            VStack(alignment: .leading, spacing: 2) {
                Text(i.name).font(.headline).lineLimit(1).truncationMode(.middle).textSelection(.enabled)
                Text(summary(i)).font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding(.bottom, 12)
    }

    /// Only web pages open from the panel; anything else stays plain text.
    static func pageLink(_ s: String?) -> URL? {
        guard let s, let u = URL(string: s), ["http", "https"].contains(u.scheme?.lowercased() ?? "") else { return nil }
        return u
    }

    static func restartNote(_ i: Item) -> String? {
        guard let r = i.restartReason, !r.isEmpty else { return nil }
        return "Baştan başladı: \(r)"
    }

    private func summary(_ i: Item) -> String {
        let size = i.size < 0 ? "Boyut bilinmiyor" : Format.bytes(i.size)
        if i.state == .done { return "\(size) · tamamlandı" }
        return i.fraction.map { "\(size) · \(Format.percent($0, digits: 0)) indirildi" } ?? size
    }

    private func kind(_ i: Item) -> String {
        let t = UTType(filenameExtension: (i.name as NSString).pathExtension)?.localizedDescription ?? "Dosya"
        return "\(t) (\(i.category.isEmpty ? "Genel" : i.category))"
    }

    private func state(_ i: Item) -> String {
        guard i.state == .downloading else { return statusText(i) }
        if let line = StreamProgress.line(i.stream) { return "İndiriliyor · \(Format.speed(i.speed, none: "—")) · \(line)" }
        return "İndiriliyor · \(Format.speed(i.speed, none: "—")) · \(i.conns.count) bağlantı"
    }

    private func login(_ i: Item) -> Login? {
        guard let host = URL(string: i.url)?.host else { return nil }
        return store.settings?.logins.first { HostPattern.matches($0.host, host: host) }
    }
}

private struct InfoSection<Content: View>: View {
    let title: String
    let trailing: String
    @State private var expanded: Bool
    @ViewBuilder let content: Content

    init(_ title: String, trailing: String = "", expanded: Bool = true, @ViewBuilder content: () -> Content) {
        self.title = title
        self.trailing = trailing
        _expanded = State(initialValue: expanded)
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Divider()
            DisclosureGroup(isExpanded: $expanded) {
                VStack(alignment: .leading, spacing: 6) { content }.padding(.leading, 14).padding(.top, 6)
            } label: {
                HStack {
                    Text(title).fontWeight(.semibold)
                    Spacer()
                    Text(trailing).foregroundStyle(.secondary)
                }
            }
            .padding(.vertical, 8)
        }
    }
}

/// Sent on Return or when focus leaves; a rejected edit keeps the draft and shows the engine's reason.
private struct EditField: View {
    let value: String
    var prompt = ""
    var multiline = false
    let commit: (String) async throws -> Void
    @State private var draft = ""
    @State private var error: String?
    @FocusState private var focused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            TextField("", text: $draft, prompt: Text(prompt), axis: multiline ? .vertical : .horizontal)
                .textFieldStyle(.plain)
                .lineLimit(multiline ? 6 : 1)
                .focused($focused)
                .onSubmit(save)
                .onChange(of: focused) { if !focused { save() } }
                .padding(.horizontal, 8).padding(.vertical, multiline ? 8 : 4)
                .frame(maxWidth: .infinity, minHeight: multiline ? 52 : 26, alignment: multiline ? .topLeading : .leading)
                .background(.fill.quaternary, in: .rect(cornerRadius: 9))
            if let error { Text(error).font(.caption).foregroundStyle(.red) }
        }
        .onAppear { draft = value }
        .onChange(of: value) { draft = value; error = nil }
    }

    private func save() {
        let new = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard new != value else { return }
        Task {
            do { try await commit(new); error = nil } catch { self.error = error.localizedDescription }
        }
    }
}

private struct InfoRow<Value: View>: View {
    let key: String
    @ViewBuilder let value: Value

    init(_ key: String, @ViewBuilder value: () -> Value) {
        self.key = key
        self.value = value()
    }

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Text(key + ":").foregroundStyle(.secondary).frame(width: 90, alignment: .trailing)
            value.frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}
