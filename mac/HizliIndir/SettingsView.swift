import AppKit
import ServiceManagement
import SwiftUI

// Settings scene: toolbar tabs in the design canvas order; every engine edit goes through `Store.update` -> `settings.set {settings}` (whole object).

private extension Store {
    func bind<T: Sendable>(_ kp: WritableKeyPath<Settings, T> & Sendable, _ fallback: T) -> Binding<T> {
        Binding(get: { self.settings?[keyPath: kp] ?? fallback },
                set: { v in Task { await self.update { $0[keyPath: kp] = v } } })
    }
}

extension Category: Identifiable { var id: String { name } }
extension Login: Identifiable { var id: String { host } }
extension HostConn: Identifiable { var id: String { host } }

@MainActor private func chooseFolder(start: String) -> String? {
    let p = NSOpenPanel()
    p.canChooseDirectories = true
    p.canChooseFiles = false
    p.canCreateDirectories = true
    p.directoryURL = URL(fileURLWithPath: (start as NSString).expandingTildeInPath)
    return p.runModal() == .OK ? p.url.map { ($0.path as NSString).abbreviatingWithTildeInPath } : nil
}

struct SettingsView: View {
    @Environment(Store.self) private var store
    @State private var tab: SettingsTab

    init(tab: SettingsTab = .genel) { _tab = State(initialValue: tab) }

    var body: some View {
        TabView(selection: $tab) {
            ForEach(SettingsTab.allCases, id: \.self) { t in
                Tab(t.label, systemImage: t.icon, value: t) { Self.content(t).navigationTitle(t.title) }
            }
        }
        .frame(width: 720, height: 560)
        .safeAreaInset(edge: .bottom) {
            if let e = store.settingsError {
                Text(e).foregroundStyle(.red).font(.callout).padding(.bottom, 6)
            }
        }
    }

    @ViewBuilder static func content(_ t: SettingsTab) -> some View {
        switch t {
        case .genel: GeneralTab()
        case .kategoriler: CategoriesTab()
        case .baglanti: ConnectionTab()
        case .kaydetme: SaveToTab()
        case .indirmeler: DownloadsTab()
        case .tarayici: BrowserTab()
        case .giris: LoginsTab()
        case .gelismis: AdvancedTab()
        }
    }
}

/// Settings come from the engine; until then the tabs say so instead of showing made-up values.
private struct NeedsEngine<Content: View>: View {
    @Environment(Store.self) private var store
    @ViewBuilder var content: (Settings) -> Content

    var body: some View {
        if let s = store.settings {
            Form { content(s) }.formStyle(.grouped)
        } else {
            ContentUnavailableView("Ayarlar yüklenemedi", systemImage: "bolt.horizontal.circle",
                                   description: Text("Motora bağlanınca ayarlar burada görünür."))
        }
    }
}

/// Single-line field that saves on Return or when focus leaves; `commit` returns an error message or nil.
private struct CommitField: View {
    let title: String
    var hint: String?
    let value: String
    var format: (String) -> String = { $0 }
    let commit: (String) -> String?
    @State private var text = ""
    @State private var error: String?
    @FocusState private var focused: Bool

    var body: some View {
        TextField(text: $text) {
            Text(title)
            if let sub = error ?? hint { Text(sub).foregroundStyle(error == nil ? Color.secondary : Color.red) }
        }
        .focused($focused)
        .onSubmit(save)
        .onAppear { text = value }
        .onChange(of: value) { if !focused { text = value } }
        .onChange(of: focused) { if !focused { save() } }
    }

    private func save() {
        guard text != value else { error = nil; return }
        error = commit(text)
        if error == nil { text = format(text) }
    }
}

/// Number field + stepper, clamped to `range` (Bağlantı and the scheduler).
struct NumberField: View {
    let title: String
    var unit: String?
    let value: Int
    let range: ClosedRange<Int>
    let commit: (Int) -> Void

    var body: some View {
        let b = Binding(get: { value }, set: { commit(min(max($0, range.lowerBound), range.upperBound)) })
        LabeledContent(title) {
            HStack(spacing: 6) {
                TextField(title, value: b, format: .number).labelsHidden().multilineTextAlignment(.center).frame(width: 56)
                Stepper(title, value: b, in: range).labelsHidden()
                if let unit { Text(unit).foregroundStyle(.secondary) }
            }
        }
    }
}

/// Glass + / − pair under a table; `trailing` sits on the right (e.g. "Düzenle…").
private struct AddRemove<Trailing: View>: View {
    let add: String
    let remove: String
    let canRemove: Bool
    let onAdd: () -> Void
    let onRemove: () -> Void
    @ViewBuilder var trailing: Trailing

    var body: some View {
        HStack {
            GlassEffectContainer(spacing: 8) {
                HStack(spacing: 8) {
                    Button(action: onAdd) { Label(add, systemImage: "plus") }.help(add)
                    Button(action: onRemove) { Label(remove, systemImage: "minus") }.help(remove).disabled(!canRemove)
                }
                .labelStyle(.iconOnly)
                .buttonStyle(.glass)
            }
            Spacer()
            trailing
        }
    }
}

// MARK: - Genel

struct GeneralTab: View {
    @AppStorage("showMenuBarIcon") private var showMenuBarIcon = true
    @AppStorage(NotifyPrefs.doneKey) private var notifyDone = true
    @AppStorage(NotifyPrefs.errorKey) private var notifyError = true
    @AppStorage(NotifyPrefs.soundKey) private var notifySound = true
    @AppStorage(ClipboardWatch.key) private var watchClipboard = false
    @State private var status = SMAppService.mainApp.status
    @State private var error: String?

    var body: some View {
        Form {
            HStack(spacing: 12) {
                Image(nsImage: NSApp?.applicationIconImage ?? NSImage()).resizable().frame(width: 40, height: 40)
                VStack(alignment: .leading, spacing: 2) {
                    Text("Octo").font(.headline)
                    Text("Sürüm \(Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "—")")
                        .font(.caption).foregroundStyle(.secondary)
                }
            }
            Section("Başlangıç") {
                Toggle("Oturum açınca başlat", isOn: Binding(get: { status == .enabled || status == .requiresApproval }, set: { setLogin($0) }))
                if status == .requiresApproval {
                    LabeledContent("Sistem Ayarları'nda onay bekliyor.") {
                        Button("Giriş Öğeleri…") { SMAppService.openSystemSettingsLoginItems() }.buttonStyle(.glass)
                    }
                }
                if let error { Text(error).foregroundStyle(.red) }
                Toggle("Menü çubuğunda göster", isOn: $showMenuBarIcon)
            }
            Section("Bildirimler") {
                Toggle("İndirme bitince bildirim göster", isOn: $notifyDone)
                Toggle("İndirme başarısız olunca bildirim göster", isOn: $notifyError)
                Toggle("Bildirim sesi", isOn: $notifySound)
            }
            Section {
                Toggle("Panodaki bağlantıları izle", isOn: $watchClipboard)
            } header: {
                Text("Pano")
            } footer: {
                Text("Kopyaladığınız http(s) bağlantısının dosya türü Kategoriler'de varsa Octo yeni indirme penceresini açar.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .onAppear { status = SMAppService.mainApp.status }
    }

    private func setLogin(_ on: Bool) {
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
            error = nil
        } catch {
            self.error = "Girişte başlatma değiştirilemedi: \(error.localizedDescription)"
        }
        status = SMAppService.mainApp.status
    }
}

// MARK: - Kategoriler

struct CategoriesTab: View {
    @Environment(Store.self) private var store
    @State var selection: String?

    var body: some View {
        NeedsEngine { s in
            Section {
                Table(s.categories, selection: $selection) {
                    TableColumn("Kategori", value: \.name).width(110)
                    TableColumn("Dosya türleri") { Text(TokenList.text($0.exts)) }
                    TableColumn("Klasör") { Text(FolderLabel.display(CategoryDir.resolved($0, in: s))) }.width(180)
                }
                .frame(height: 180)
                AddRemove(add: "Kategori ekle", remove: "Kategoriyi sil", canRemove: selection != nil,
                          onAdd: { add(s) }, onRemove: remove) { EmptyView() }
            } header: {
                Text("Kategoriler")
            } footer: {
                Text("Listede olmayan türler varsayılan klasöre iner.").font(.caption).foregroundStyle(.secondary)
            }
            if let c = s.categories.first(where: { $0.name == selection }) {
                CategoryDetail(cat: c, s: s) { selection = $0 }.id(c.name)
            }
        }
    }

    private func add(_ s: Settings) {
        let names = s.categories.map(\.name)
        var n = "Yeni kategori", i = 2
        while names.contains(n) { n = "Yeni kategori \(i)"; i += 1 }
        let cat = Category(name: n, exts: OrEmpty(), dir: "")
        Task { if await store.update({ $0.categories.append(cat) }) { selection = n } }
    }

    private func remove() {
        guard let name = selection else { return }
        selection = nil
        Task { await store.update { $0.categories.removeAll { $0.name == name } } }
    }
}

private struct CategoryDetail: View {
    @Environment(Store.self) private var store
    let cat: Category
    let s: Settings
    let renamed: (String) -> Void

    var body: some View {
        Section("“\(cat.name)” kategorisi") {
            CommitField(title: "Ad", value: cat.name) { v in
                guard let n = QueueCheck.cleanName(v) else { return "Ad boş olamaz." }
                if n != cat.name, s.categories.contains(where: { $0.name == n }) { return "Bu adda bir kategori var." }
                change { $0.name = n }
                renamed(n)
                return nil
            }
            CommitField(title: "Dosya türleri", hint: "Boşlukla ayırın.", value: TokenList.text(cat.exts),
                        format: { TokenList.text(TokenList.parse($0, extensions: true)) }) { v in
                let l = TokenList.parse(v, extensions: true)
                if l != cat.exts { change { $0.exts = l } }
                return nil
            }
            LabeledContent {
                HStack {
                    Text(FolderLabel.display(CategoryDir.resolved(cat, in: s)))
                    Button("Değiştir…") {
                        if let d = chooseFolder(start: CategoryDir.resolved(cat, in: s)) { change { $0.dir = d } }
                    }
                    .buttonStyle(.glass)
                }
            } label: {
                Text("Klasör")
                if !s.categoryFolders { Text("Kaydetme sekmesinde “Kategorilere göre alt klasör kullan” açıkken geçerli.") }
            }
        }
    }

    private func change(_ edit: @escaping (inout Category) -> Void) {
        let name = cat.name
        Task {
            await store.update { s in
                if let i = s.categories.firstIndex(where: { $0.name == name }) { edit(&s.categories[i]) }
            }
        }
    }
}

// MARK: - Bağlantı

struct ConnectionTab: View {
    @Environment(Store.self) private var store
    @State var selection: String?
    @State private var editing: HostConnEdit?

    var body: some View {
        NeedsEngine { s in
            Section("Bağlantılar") {
                Picker(selection: store.bind(\.maxConns, 8)) {
                    ForEach(ConnChoices.values(including: s.maxConns), id: \.self) { Text("\($0)").tag($0) }
                } label: {
                    Text("Dosya başına en fazla bağlantı")
                    Text("1 bağlantıyla başlar, 2 saniyede bir ikiye katlar.")
                }
                if let main = store.queues.first(where: { $0.id == "main" }) {
                    NumberField(title: "Aynı anda en fazla indirme", value: main.atOnce, range: QueueCheck.atOnceRange) { n in
                        guard n != main.atOnce else { return }
                        var q = main
                        q.atOnce = n
                        Task {
                            do { try await store.saveQueue(q); store.settingsError = nil } catch { store.settingsError = error.localizedDescription }
                        }
                    }
                }
                LabeledContent("Veri gelmezse yeniden bağlan", value: "30 saniye")
            }
            Section {
                Table(s.hostConns, selection: $selection) {
                    TableColumn("Sunucu", value: \.host)
                    TableColumn("En fazla bağlantı") { Text("\($0.conns)") }.width(130)
                }
                .frame(height: 120)
                AddRemove(add: "İstisna ekle", remove: "İstisnayı sil", canRemove: selection != nil,
                          onAdd: { editing = HostConnEdit(old: nil) }, onRemove: { remove(selection) }) {
                    Button("Düzenle…") { if let h = s.hostConns.first(where: { $0.host == selection }) { editing = HostConnEdit(old: h) } }
                        .buttonStyle(.glass).disabled(selection == nil)
                }
            } header: {
                Text("Sunucu istisnaları")
            } footer: {
                Text("Çok bağlantıyı kesen sunucular için bağlantı sayısını sınırlar.").font(.caption).foregroundStyle(.secondary)
            }
        }
        .sheet(item: $editing) { HostConnSheet(edit: $0) }
    }

    private func remove(_ host: String?) {
        guard let host else { return }
        selection = nil
        Task { await store.update { $0.hostConns.removeAll { $0.host == host } } }
    }
}

private struct HostConnEdit: Identifiable {
    var old: HostConn?
    var id: String { old?.host ?? "new" }
}

private struct HostConnSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let edit: HostConnEdit
    @State private var host = ""
    @State private var conns = 2
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(edit.old == nil ? "Yeni istisna" : "İstisnayı düzenle").font(.headline)
            TextField("Sunucu", text: $host, prompt: Text("*.example.com"))
            Picker("En fazla bağlantı", selection: $conns) {
                ForEach(ConnChoices.values(including: conns), id: \.self) { Text("\($0)").tag($0) }
            }
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Button("Kaydet", action: save).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent)
            }
        }
        .padding(20)
        .frame(width: 420)
        .onAppear { host = edit.old?.host ?? ""; conns = edit.old?.conns ?? 2 }
    }

    private func save() {
        let h = host.trimmingCharacters(in: .whitespaces)
        guard HostPattern.isValid(h) else { error = "Geçersiz sunucu adı. Örnek: example.com ya da *.example.com"; return }
        if h != edit.old?.host, store.settings?.hostConns.contains(where: { $0.host == h }) == true {
            error = "Bu sunucu için zaten bir istisna var."
            return
        }
        Task {
            let ok = await store.update { s in
                let r = HostConn(host: h, conns: conns)
                if let i = s.hostConns.firstIndex(where: { $0.host == edit.old?.host }) { s.hostConns[i] = r } else { s.hostConns.append(r) }
            }
            if ok { dismiss() } else { error = store.settingsError }
        }
    }
}

// MARK: - Kaydetme yeri

struct SaveToTab: View {
    @Environment(Store.self) private var store

    var body: some View {
        NeedsEngine { s in
            Section("Konum") {
                LabeledContent("Varsayılan klasör") {
                    HStack {
                        Text(s.downloadDir)
                        Button("Değiştir…") {
                            if let d = chooseFolder(start: s.downloadDir) { Task { await store.update { $0.downloadDir = d } } }
                        }
                        .buttonStyle(.glass)
                    }
                }
                Toggle(isOn: store.bind(\.categoryFolders, false)) {
                    Text("Kategorilere göre alt klasör kullan")
                    Text("Örnek: \(FolderLabel.display(s.downloadDir)) › Video")
                }
            }
            Section("Dosya") {
                Toggle(isOn: Binding(get: { s.useServerDate ?? false },
                                     set: { v in Task { await store.update { $0.useServerDate = v } } })) {
                    Text("Sunucu tarihini kullan")
                    Text("Biten dosyanın değişiklik tarihi sunucunun bildirdiği (Last-Modified) tarih olur.")
                }
                LabeledContent {
                    Text("Her zaman").foregroundStyle(.secondary)
                } label: {
                    Text("Gatekeeper karantina işareti")
                    Text("Biten dosyaya Safari ve Chrome'daki gibi com.apple.quarantine eklenir.")
                }
                LabeledContent {
                    EmptyView()
                } label: {
                    Text("Yarım kalan dosyalar")
                    Text("Hedef klasörde “.part” uzantısıyla tutulur, bitince adı düzelir.")
                }
            }
        }
    }
}

// MARK: - İndirmeler

struct DownloadsTab: View {
    @Environment(Store.self) private var store
    @AppStorage("showCompleteWindow") private var showCompleteWindow = true

    var body: some View {
        NeedsEngine { s in
            Section("İndirme sırasında") {
                Toggle(isOn: $showCompleteWindow) {
                    Text("İndirme bitince bitti penceresini göster")
                    Text("Kapalıyken yalnız bildirim gelir.")
                }
                Toggle(isOn: Binding(get: { DeletePrompt.asks() }, set: { DeletePrompt.setAsks($0) })) {
                    Text("Listeden kaldırmadan önce sor")
                    Text("Kapalıyken indirmeler sormadan listeden kaldırılır.")
                }
            }
            Section("Yeni indirme") {
                Picker("Aynı adlı dosya eklenince", selection: store.bind(\.duplicate, "number")) {
                    Text("Sor").tag("ask")
                    Text("Numaralı ad ver (dosya (2).zip)").tag("number")
                    Text("Üzerine yaz").tag("overwrite")
                    if !["ask", "number", "overwrite"].contains(s.duplicate) { Text(s.duplicate).tag(s.duplicate) }
                }
            }
        }
    }
}

// MARK: - Tarayıcı

/// "Son bağlantı" text: when the extension's native host last reached the engine.
enum HostHello {
    static func text(_ date: Date?, now: Date = .now) -> String {
        guard let date else { return "Henüz bağlanmadı" }
        let f = RelativeDateTimeFormatter()
        f.locale = Locale(identifier: "tr_TR")
        f.unitsStyle = .abbreviated
        return f.localizedString(for: min(date, now), relativeTo: now)
    }
}

struct BrowserTab: View {
    @Environment(Store.self) private var store
    @State private var installing = false
    @State private var output: String?
    @State private var checked = 0  // bumped after an install so the status rows re-read the manifests
    @State private var ext: ExtensionCheck.State?  // nil: no supported browser (or not checked yet)
    @State private var hello: Date?  // the native host's last `hello`, from the engine (memory only)

    var body: some View {
        NeedsEngine { s in
            Section("Tarayıcı eklentileri") {
                browserRow("Google Chrome", nil, BrowserHost.chrome)
                browserRow("Arc, Brave, Edge", "Chrome eklentisini kullanır.", BrowserHost.others)
                extensionRow
                LabeledContent("Son bağlantı") { Text(HostHello.text(hello)).font(.callout).foregroundStyle(.secondary) }
                if let output { Text(output).font(.caption).foregroundStyle(.secondary).textSelection(.enabled) }
            }
            .id(checked)
            .task(id: checked) { await recheckExtension(); await recheckHello() }
            .onReceive(NotificationCenter.default.publisher(for: NSApplication.didBecomeActiveNotification)) { _ in
                Task { await recheckExtension(); await recheckHello() }  // back from chrome://extensions
            }
            Section("Video") {
                Toggle("Videoların üstünde Octo düğmesini göster", isOn: Binding(
                    get: { VideoButton.shown(s) },
                    set: { on in Task { await store.update { VideoButton.set(&$0, shown: on) } } }))
                Text("Tarayıcıda oynayan videoların köşesinde indirme düğmesi çıkar. Korumalı (DRM) ve canlı yayınlar indirilmez.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            Section("Yakalama") {
                Toggle(isOn: Binding(get: { s.captureOff != true }, set: { v in Task { await store.update { $0.captureOff = !v } } })) {
                    Text("Tarayıcı indirmelerini yakala")
                    Text("Kapalıyken tüm indirmeler tarayıcıda kalır.")
                }
                NumberField(title: "Küçük dosya eşiği", unit: "KB (altındakiler tarayıcıda kalır)", value: s.captureMinKB ?? 0, range: 0...1_048_576) { n in
                    if n != s.captureMinKB ?? 0 { Task { await store.update { $0.captureMinKB = n } } }
                }
                CommitField(title: "Yakalanacak dosya türleri", hint: "Boşlukla ayırın: zip rar iso",
                            value: TokenList.text(s.captureExts), format: { TokenList.text(TokenList.parse($0, extensions: true)) }) { v in
                    let l = TokenList.parse(v, extensions: true)
                    if l != s.captureExts { Task { await store.update { $0.captureExts = l } } }
                    return nil
                }
                CommitField(title: "Bu sitelerden yakalama", hint: "Boşlukla ayırın. *.example.com, example.com'u da kapsar.",
                            value: TokenList.text(s.excludedSites), format: { TokenList.text(TokenList.parse($0, extensions: false)) }) { v in
                    let l = TokenList.parse(v, extensions: false)
                    if let bad = l.first(where: { !HostPattern.isValid($0) }) { return "Geçersiz site adı: \(bad)" }
                    if l != s.excludedSites { Task { await store.update { $0.excludedSites = l } } }
                    return nil
                }
            }
        }
    }

    private func browserRow(_ name: String, _ sub: String?, _ dirs: [String]) -> some View {
        let state = BrowserHost.installed(dirs)
        return LabeledContent {
            HStack {
                switch state {
                case true?: Text("✓ Kurulu").foregroundStyle(.green)
                case false?: Text("Kurulu değil").foregroundStyle(.secondary)
                case nil: Text("Tarayıcı bulunamadı").foregroundStyle(.secondary)
                }
                Button(state == true ? "Yeniden kur…" : "Kur…", action: install).buttonStyle(.glass).disabled(installing || state == nil)
            }
            .font(.callout)
        } label: {
            Text(name)
            if let sub { Text(sub) }
        }
    }

    private var extensionRow: some View {
        LabeledContent {
            HStack {
                switch ext {
                case .enabled?: Text("✓ Yüklü").foregroundStyle(.green)
                case .disabled?: Text("Kapalı · chrome://extensions'dan açın").foregroundStyle(.orange)
                case .missing?:
                    Text("Yüklü değil").foregroundStyle(.secondary)
                    Button("Yükle…", action: revealExtension).buttonStyle(.glass)
                case nil: Text("Tarayıcı bulunamadı").foregroundStyle(.secondary)
                }
            }
            .font(.callout)
        } label: {
            Text("Octo eklentisi")
            Text("Profillerde aranır; yükledikten sonra buraya dönün.")
        }
    }

    private func recheckExtension() async {
        guard let id = ExtensionCheck.bundledID else { return }
        ext = await Task.detached { ExtensionCheck.state(id: id, browsers: BrowserHost.chrome + BrowserHost.others) }.value
    }

    private func recheckHello() async { hello = (try? await store.client.lastHello()) ?? nil }

    /// Chrome only loads unpacked extensions by hand: reveal the folder and copy chrome://extensions.
    private func revealExtension() {
        if let d = Bundle.main.url(forResource: "ChromeExtension", withExtension: nil) { NSWorkspace.shared.activateFileViewerSelecting([d]) }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString("chrome://extensions", forType: .string)
        output = "Adres çubuğuna yapıştırın (chrome://extensions panoda), sağ üstten “Geliştirici modu”nu açın, “Paketlenmemiş öğe yükle” ile Finder'da seçili ChromeExtension klasörünü seçin."
    }

    private func install() {
        installing = true
        Task {
            let r = await HostInstaller.installBundled()
            output = r.output.isEmpty ? (r.ok ? "Tamam" : "Hata") : r.output
            installing = false
            checked += 1
        }
    }
}

// MARK: - Site girişleri

private struct LoginEdit: Identifiable {
    var old: Login?
    var id: String { old?.host ?? "new" }
}

struct LoginsTab: View {
    @Environment(Store.self) private var store
    @State var selection: String?
    @State private var editing: LoginEdit?

    var body: some View {
        NeedsEngine { s in
            Section {
                Table(s.logins, selection: $selection) {
                    TableColumn("Site", value: \.host)
                    TableColumn("Kullanıcı adı", value: \.user).width(150)
                    TableColumn("Parola") { _ in Text("••••••••").foregroundStyle(.secondary) }.width(110)
                }
                .frame(height: 170)
                AddRemove(add: "Giriş ekle", remove: "Girişi sil", canRemove: selection != nil,
                          onAdd: { editing = LoginEdit(old: nil) }, onRemove: { remove(selection) }) {
                    Button("Düzenle…") { if let l = s.logins.first(where: { $0.host == selection }) { editing = LoginEdit(old: l) } }
                        .buttonStyle(.glass).disabled(selection == nil)
                }
            } header: {
                Text("Site girişleri")
            } footer: {
                Text("Parolalar Mac'in Anahtar Zinciri'nde saklanır.").font(.caption).foregroundStyle(.secondary)
            }
        }
        .sheet(item: $editing) { LoginSheet(edit: $0) }
    }

    private func remove(_ host: String?) {
        guard let host else { return }
        selection = nil
        Task { if await store.update({ $0.logins.removeAll { $0.host == host } }) { LoginKeychain.delete(host: host) } }
    }
}

private struct LoginSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let edit: LoginEdit
    @State private var host = ""
    @State private var user = ""
    @State private var password = ""
    @State private var error: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(edit.old == nil ? "Yeni giriş" : "Girişi düzenle").font(.headline)
            TextField("Sunucu", text: $host, prompt: Text("*.example.com"))
            TextField("Kullanıcı adı", text: $user)
            SecureField("Parola", text: $password, prompt: Text(edit.old == nil ? "" : "Değiştirmek için yazın"))
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Button("Kaydet", action: save).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent)
            }
        }
        .padding(20)
        .frame(width: 420)
        .onAppear { host = edit.old?.host ?? ""; user = edit.old?.user ?? "" }
    }

    private func save() {
        let h = host.trimmingCharacters(in: .whitespaces), u = user.trimmingCharacters(in: .whitespaces)
        guard HostPattern.isValid(h) else { error = "Geçersiz sunucu adı. Örnek: example.com ya da *.example.com"; return }
        guard !u.isEmpty else { error = "Kullanıcı adı boş olamaz."; return }
        if h != edit.old?.host, store.settings?.logins.contains(where: { $0.host == h }) == true {
            error = "Bu sunucu için zaten bir giriş var."
            return
        }
        if edit.old == nil, password.isEmpty { error = "Parola boş olamaz."; return }
        // Keep the old password when the field stays empty (also across a host rename).
        let pw = password.isEmpty ? edit.old.flatMap { LoginKeychain.password(for: $0.host) } : password
        guard let pw else { error = "Kayıtlı parola Anahtar Zinciri'nden okunamadı; parolayı yeniden yazın."; return }
        if let msg = LoginKeychain.set(pw, for: h) { error = msg; return }
        Task {
            let ok = await store.update { s in
                let l = Login(host: h, user: u)
                if let i = s.logins.firstIndex(where: { $0.host == edit.old?.host }) { s.logins[i] = l } else { s.logins.append(l) }
            }
            guard ok else {
                if h != edit.old?.host { LoginKeychain.delete(host: h) }  // new host: no orphaned password
                error = store.settingsError
                return
            }
            if let o = edit.old?.host, o != h { LoginKeychain.delete(host: o) }
            dismiss()
        }
    }
}

// MARK: - Gelişmiş

enum UserAgentText {
    /// Trimmed value ("" = engine default `octo/1.0`); nil when it holds control characters (a header cannot carry them).
    static func clean(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.unicodeScalars.contains { ($0.value < 0x20 && $0 != "\t") || $0.value == 0x7f } ? nil : t
    }
}

enum SettingsReset {
    /// The engine's default `captureExts` (classic manager list); `decide.mjs` in the extension carries the same words.
    static let captureExts = "3gp 7z aac ace aif apk arj asf avi bin bz2 exe gz gzip img iso lzh m4a m4v mkv mov mp3 mp4 mpa mpe mpeg mpg msi msu ogg ogv pdf plj pps ppt qt r0* r1* ra rar rm rmvb sea sit sitx tar tif tiff wav wma wmv z zip"
        .split(separator: " ").map(String.init)

    /// Engine defaults for every preference; categories and site logins stay as they are.
    static func apply(to s: Settings) -> Settings {
        var n = s
        n.maxConns = 8
        n.downloadDir = "~/Downloads"
        n.categoryFolders = false
        n.duplicate = "number"
        n.speedLimitOn = false
        n.speedLimitKBps = 500
        n.captureExts = captureExts
        n.captureOff = false
        n.captureMinKB = 0
        n.hideVideoButton = false
        n.excludedSites = []
        n.userAgent = ""
        return n
    }
}

struct AdvancedTab: View {
    @Environment(Store.self) private var store
    @State private var kbps = ""
    @State private var error: String?
    @State private var confirmReset = false
    @AppStorage(KeepAwake.key) private var keepAwake = true
    private let dataDir = EngineClient.dataDir()

    var body: some View {
        Form {
            if let s = store.settings {
                Section("İstek") {
                    CommitField(title: "User-Agent", hint: "Boşsa octo/1.0 gönderilir. Tarayıcıdan gelen indirmelerde tarayıcınınki kullanılır.",
                                value: s.userAgent, format: { UserAgentText.clean($0) ?? $0 }) { v in
                        guard let ua = UserAgentText.clean(v) else { return "User-Agent satır sonu içeremez." }
                        if ua != s.userAgent { Task { await store.update { $0.userAgent = ua } } }
                        return nil
                    }
                    Toggle(isOn: store.bind(\.speedLimitOn, false)) {
                        Text("Hız sınırı")
                        Text("Açık kalırsa uygulama yeniden açıldığında da geçerlidir.")
                    }
                    LabeledContent("En fazla") {
                        HStack {
                            TextField("KB/s", text: $kbps).labelsHidden().multilineTextAlignment(.trailing).frame(width: 90)
                                .onSubmit { commit(s) }
                            Text("KB/s").foregroundStyle(.secondary)
                            Button("Uygula") { commit(s) }.buttonStyle(.glass)
                                .disabled(SpeedLimit.parseKBps(kbps) == s.speedLimitKBps)
                        }
                    }
                    if let error { Text(error).foregroundStyle(.red) }
                }
                .onAppear { kbps = "\(s.speedLimitKBps)" }
                .onChange(of: s.speedLimitKBps) { kbps = "\(s.speedLimitKBps)" }
            }
            Section("Uyku") {
                Toggle(isOn: $keepAwake) {
                    Text("İndirme sürerken Mac uyumasın")
                    Text("Yalnız indirme sürerken ve Octo açıkken geçerlidir. Kuyruğun \"bitince uyut\" işlemi ve kapağı kapatmak yine uyutur.")
                }
                .onChange(of: keepAwake) { store.syncSleepGuard() }
            }
            Section("Motor") {
                LabeledContent("Durum") {
                    Label(store.connected ? "Bağlı" : "Bağlı değil", systemImage: store.connected ? "checkmark.circle.fill" : "xmark.circle")
                        .foregroundStyle(store.connected ? .green : .secondary)
                }
                if let e = store.lastError, !store.connected { Text(e).foregroundStyle(.secondary) }
                LabeledContent {
                    Text("11 680 bayt").foregroundStyle(.secondary)
                } label: {
                    Text("En küçük parça")
                    Text("Varsayılan: 11 680 bayt; 23 363 bayttan az kalan parça bölünmez.")
                }
                LabeledContent {
                    Text("HTTP/1.1").foregroundStyle(.secondary)
                } label: {
                    Text("Her bağlantı ayrı TCP bağlantısı açar")
                    Text("HTTP/2 tek bağlantıda birleştirdiği için kapalıdır.")
                }
            }
            Section("Bakım") {
                LabeledContent {
                    Button("Klasörü aç") { NSWorkspace.shared.open(URL(fileURLWithPath: dataDir)) }.buttonStyle(.glass)
                } label: {
                    Text("Veri klasörü")
                    Text((dataDir as NSString).abbreviatingWithTildeInPath).textSelection(.enabled)
                }
                LabeledContent {
                    Button("Ayarları sıfırla…", role: .destructive) { confirmReset = true }.buttonStyle(.glass)
                        .disabled(store.settings == nil)
                } label: {
                    Text("Ayarlar")
                    Text("Tercihleri varsayılana döndürür.")
                }
            }
        }
        .formStyle(.grouped)
        .confirmationDialog("Ayarlar varsayılana döndürülsün mü?", isPresented: $confirmReset, titleVisibility: .visible) {
            Button("Ayarları sıfırla", role: .destructive) { Task { await store.update { $0 = SettingsReset.apply(to: $0) } } }
            Button("İptal", role: .cancel) {}
        } message: {
            Text("Bağlantı sayısı, kaydetme yeri, hız sınırı, yakalama ve User-Agent varsayılana döner. Kategoriler, site girişleri, kuyruklar ve indirmeler korunur.")
        }
    }

    private func commit(_ s: Settings) {
        guard let n = SpeedLimit.parseKBps(kbps) else { error = SpeedLimit.rangeMessage; return }
        error = nil
        Task { await store.update { $0.speedLimitKBps = n } }
    }
}
