import AppKit
import SwiftUI
import UniformTypeIdentifiers

private func expand(_ p: String) -> String { (p as NSString).expandingTildeInPath }
private func abbreviate(_ p: String) -> String { (p as NSString).abbreviatingWithTildeInPath }

private func defaultQueue(_ qs: [Queue]) -> String { (qs.first { $0.id == "main" } ?? qs.first)?.id ?? "" }

// MARK: - Yeni indirme: step 1 (address) -> step 2 (file info)

struct AddDownloadSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var url: String
    @State private var fromClipboard: Bool
    @State private var confirmed: String?
    @State private var invalid = false
    @State private var user = ""
    @State private var password = ""
    var onBatch: () -> Void

    /// A non-empty `url` skips the clipboard (screenshots pass sample data).
    init(url: String = "", fromClipboard: Bool = false, onBatch: @escaping () -> Void) {
        _url = State(initialValue: url)
        _fromClipboard = State(initialValue: fromClipboard)
        self.onBatch = onBatch
    }

    var body: some View {
        if let confirmed {
            NewDownloadForm(url: confirmed, settings: store.settings, queues: store.queues,
                            headers: SiteAuth.headers(user: user, password: password, url: confirmed))
        } else {
            step1
        }
    }

    private var recent: [String] {
        var seen = Set<String>()
        return Array(store.items.sorted { $0.added > $1.added }.map(\.url).filter { seen.insert($0).inserted }.prefix(10))
    }

    private var step1: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Yeni indirme").font(.headline)
            Text(fromClipboard ? "Panoda bir bağlantı bulundu ve adrese yerleştirildi." : "İndirilecek dosyanın adresini yazın ya da yapıştırın.")
                .foregroundStyle(.secondary).padding(.top, 4).padding(.bottom, 16)
            Form {
                LabeledContent("Adres:") {
                    HStack(spacing: 6) {
                        TextField("Adres", text: $url, prompt: Text("https://…"))
                            .labelsHidden()
                            .onSubmit(next)
                            .onChange(of: url) { invalid = false }
                        Menu {
                            ForEach(recent, id: \.self) { u in Button(u) { url = u } }
                        } label: {
                            Image(systemName: "chevron.down")
                        }
                        .menuIndicator(.hidden).fixedSize().buttonStyle(.glass)
                        .disabled(recent.isEmpty).help("Geçmiş adresler").accessibilityLabel("Geçmiş adresler")
                    }
                }
                AuthSection(user: $user, password: $password, url: url)
                if invalid {
                    LabeledContent("") { Text("Geçersiz adres. http veya https adresi girin.").foregroundStyle(.red) }
                }
            }
            .formStyle(.columns)
            HStack(spacing: 8) {
                Button("Toplu indirme…", action: onBatch).buttonStyle(.glass)
                Spacer()
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Button("Devam", action: next).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent)
            }
            .padding(.top, 20)
        }
        .padding(22)
        .frame(width: 540)
        .onAppear {
            if url.isEmpty, let c = URLCheck.clipboardURL(NSPasteboard.general.string(forType: .string)) {
                url = c
                fromClipboard = true
            }
        }
    }

    private func next() {
        if let u = URLCheck.normalized(url) { confirmed = u } else { invalid = true }
    }
}

enum DupChoice { case resume, number, overwrite }

/// Texts and buttons of the "already exists" alert (NSAlert: stacked buttons + "remember" checkbox, like the canvas).
enum DuplicatePrompt {
    static func title(inList: Bool) -> String { inList ? "Bu dosya listede zaten var" : "Bu dosya kaydetme yerinde zaten var" }

    static func buttons(inList: Bool, numbered: String) -> [(choice: DupChoice?, title: String)] {
        (inList ? [(DupChoice.resume, "Kaldığı yerden devam et")] : [])
            + [(.number, "Numaralı adla ekle (\(numbered))"), (.overwrite, "Üzerine yaz"), (nil, "İptal")]
    }

    /// `Settings.duplicate` value to remember; nil for resume (the engine has no such policy).
    static func setting(for c: DupChoice) -> String? {
        switch c {
        case .number: "number"
        case .overwrite: "overwrite"
        case .resume: nil
        }
    }

    @MainActor static func alert(name: String, url: String, inList: Bool, numbered: String) -> NSAlert {
        let a = NSAlert()
        a.messageText = title(inList: inList)
        let u = URL(string: url)
        let short = u.map { "\($0.host ?? "")/…/\($0.lastPathComponent)" } ?? url
        a.informativeText = (inList ? "“\(name)” listede bekliyor ve henüz tamamlanmadı. Ne yapılsın?"
                                    : "“\(name)” bu klasörde zaten var. Ne yapılsın?") + "\n\n" + short
        for b in buttons(inList: inList, numbered: numbered) {
            let btn = a.addButton(withTitle: b.title)
            if b.choice == nil { btn.keyEquivalent = "\u{1b}" }
        }
        a.showsSuppressionButton = true
        a.suppressionButton?.title = "Seçimimi hatırla, bir daha sorma"
        return a
    }

    static func choice(_ r: NSApplication.ModalResponse, inList: Bool, numbered: String) -> DupChoice? {
        let b = buttons(inList: inList, numbered: numbered)
        let i = r.rawValue - NSApplication.ModalResponse.alertFirstButtonReturn.rawValue
        return b.indices.contains(i) ? b[i].choice : nil
    }
}

/// Folder shown as Finder names, at most the last two levels: "Downloads › Programlar".
enum PathCrumb {
    static func parts(_ path: String, home: String = NSHomeDirectory()) -> [String] {
        var u = URL(fileURLWithPath: path).standardizedFileURL
        var out: [String] = []
        while out.count < 2, u.path != "/", u.path != home || out.isEmpty {
            out.insert(FileManager.default.displayName(atPath: u.path), at: 0)
            if u.path == home { break }
            u.deleteLastPathComponent()
        }
        return out
    }
}

struct NewDownloadForm: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let url: String
    let settings: Settings?
    let queues: [Queue]
    let headers: [String: String]

    @State private var name: String
    @State private var guessed: String
    @State private var category: String
    @State private var dir: String
    @State private var dirChosen = false
    @State private var remember = false
    @State private var desc = ""
    @State private var queueID: String
    @State private var probe: ProbeReply?
    @State private var probing = true
    /// Manifest address (probe `kind`): the qualities and audio tracks, once `stream.probe` answered.
    @State private var streamInfo: StreamInfo?
    @State private var streamError: String?
    @State private var video: String?
    @State private var audioPick: String?
    @State private var pickingFolder = false
    @State private var addingCategory = false
    @State private var newCategory = ""
    @State private var error: String?
    @State private var busy = false

    init(url: String, settings: Settings?, queues: [Queue], probe: ProbeReply? = nil, streamInfo: StreamInfo? = nil, headers: [String: String] = [:]) {
        self.url = url
        self.headers = headers
        self.settings = settings
        self.queues = queues
        let cat = CategoryPick.category(forURL: url, in: settings?.categories ?? [])
        let g = streamInfo.map(StreamPick.fileName)
            ?? probe.map(\.name).flatMap { $0.isEmpty ? nil : $0 } ?? URLCheck.fileName(from: url)
        _guessed = State(initialValue: g)
        _name = State(initialValue: g)
        _category = State(initialValue: cat?.name ?? "")
        _dir = State(initialValue: expand(CategoryPick.saveDir(cat, defaultDir: settings?.downloadDir ?? "~/Downloads", categoryFolders: settings?.categoryFolders ?? false)))
        _queueID = State(initialValue: defaultQueue(queues))
        _probe = State(initialValue: probe)
        _probing = State(initialValue: probe == nil)
        _streamInfo = State(initialValue: streamInfo)
    }

    private var isStream: Bool { probe?.kind != nil || streamInfo != nil }
    /// A manifest cannot be added as a plain file: wait for its qualities, and refuse live or protected videos.
    private var canAdd: Bool { !isStream || (streamInfo.map { StreamPick.blocker($0) == nil } ?? false) }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header.padding(.bottom, 18)
            Form {
                TextField("Adres:", text: .constant(url)).disabled(true)
                TextField("Dosya adı:", text: $name)
                if let info = streamInfo, StreamPick.blocker(info) == nil { streamPickers(info) }
                LabeledContent("Kategori:") {
                    HStack(spacing: 6) {
                        Picker("Kategori", selection: $category) {
                            Text("Genel").tag("")
                            ForEach(store.settings?.categories ?? [], id: \.name) { Text($0.name).tag($0.name) }
                        }
                        .labelsHidden().fixedSize()
                        Button { addingCategory = true } label: { Image(systemName: "plus") }
                            .buttonStyle(.glass).help("Yeni kategori").accessibilityLabel("Yeni kategori").disabled(store.settings == nil)
                        Spacer(minLength: 0)
                    }
                }
                .onChange(of: category) { if !dirChosen { dir = categoryDir } }
                LabeledContent("Kaydetme yeri:") {
                    HStack(spacing: 6) {
                        HStack(spacing: 6) {
                            Image(systemName: "folder.fill").foregroundStyle(.tint).accessibilityHidden(true)
                            Text(PathCrumb.parts(dir).joined(separator: "  ›  ")).lineLimit(1).truncationMode(.head)
                        }
                        .padding(.horizontal, 9)
                        .frame(maxWidth: .infinity, minHeight: 28, alignment: .leading)
                        .background(.background.opacity(0.6), in: .rect(cornerRadius: 10))
                        .overlay(RoundedRectangle(cornerRadius: 10).strokeBorder(.separator))
                        .help(abbreviate(dir))
                        Button("Değiştir…") { pickingFolder = true }.buttonStyle(.glass)
                    }
                }
                if !category.isEmpty {
                    Toggle("Bu konumu “\(category)” kategorisi için hatırla", isOn: $remember).toggleStyle(.checkbox)
                }
                TextField("Açıklama:", text: $desc, prompt: Text("İsteğe bağlı"))
            }
            .formStyle(.columns)
            if let error { Text(error).foregroundStyle(.red).padding(.top, 10) }
            HStack(spacing: 8) {
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Spacer()
                Button("Sonra indir") { submit(start: false) }.buttonStyle(.glass).disabled(queues.isEmpty || !canAdd)
                Picker("Kuyruk", selection: $queueID) {
                    ForEach(queues) { Text($0.name).tag($0.id) }
                }
                .labelsHidden().frame(width: 140).disabled(queues.isEmpty)
                Button("Şimdi indir") { submit(start: true) }.keyboardShortcut(.defaultAction).buttonStyle(.glassProminent).disabled(!canAdd)
            }
            .padding(.top, 22)
            .disabled(busy)
        }
        .padding(22)
        .frame(width: 620)
        .task {
            guard probe == nil else {
                if probe?.kind != nil, streamInfo == nil { await loadStream() }
                return
            }
            if let p = try? await store.probe(url: url, headers: headers) {
                probe = p
                if !p.name.isEmpty, name == guessed { name = p.name; guessed = p.name }
                if p.kind != nil { await loadStream() }
            }
            probing = false
        }
        .fileImporter(isPresented: $pickingFolder, allowedContentTypes: [.folder]) { r in
            if case .success(let u) = r { dir = u.path; dirChosen = true }
        }
        .alert("Yeni kategori", isPresented: $addingCategory) {
            TextField("Kategori adı", text: $newCategory)
            Button("Ekle", action: addCategory)
            Button("İptal", role: .cancel) { newCategory = "" }
        }
    }

    private var header: some View {
        HStack(spacing: 14) {
            Image(nsImage: NSWorkspace.shared.icon(for: UTType(filenameExtension: (name as NSString).pathExtension) ?? .data))
                .resizable().frame(width: 56, height: 56)
            VStack(alignment: .leading, spacing: 3) {
                Text(name).font(.headline).lineLimit(1).truncationMode(.middle)
                Text([probe.flatMap { $0.size >= 0 ? Format.bytes($0.size) : nil }, URL(string: url)?.host].compactMap { $0 }.joined(separator: " · "))
                    .foregroundStyle(.secondary)
                if let probe, probe.kind != nil {
                    streamStatus
                } else if let probe {
                    Text(probe.resumable ? "Devam ettirilebilir · \(settings?.maxConns ?? 8) bağlantıyla indirilecek"
                                         : "Devam ettirilemez · tek bağlantıyla indirilecek")
                        .font(.caption).foregroundStyle(probe.resumable ? .green : .orange)
                } else if probing {
                    Text("Dosya bilgisi alınıyor…").font(.caption).foregroundStyle(.secondary)
                }
            }
        }
    }

    @ViewBuilder private var streamStatus: some View {
        if let info = streamInfo {
            if let why = StreamPick.blocker(info) {
                Text(why).font(.caption).foregroundStyle(.red)
            } else {
                Text(["Video (\(info.kind == "dash" ? "DASH" : "HLS"))", StreamPick.duration(info.duration),
                      "parçalar \(settings?.maxConns ?? 8) bağlantıyla indirilecek"].compactMap { $0 }.joined(separator: " · "))
                    .font(.caption).foregroundStyle(.green)
            }
        } else if let streamError {
            Text(streamError).font(.caption).foregroundStyle(.red)
        } else {
            Text("Video bilgisi alınıyor…").font(.caption).foregroundStyle(.secondary)
        }
    }

    @ViewBuilder private func streamPickers(_ info: StreamInfo) -> some View {
        if info.variants.count > 1 {
            Picker("Kalite:", selection: Binding(get: { StreamPick.variant(info, video)?.id ?? "" }, set: { video = $0 })) {
                ForEach(info.variants, id: \.id) { Text(StreamPick.title($0)).tag($0.id) }
            }
            .fixedSize()
        }
        if StreamPick.showsAudioPicker(info, video: video) {
            Picker("Ses:", selection: Binding(get: { StreamPick.audio(info, video: video, wanted: audioPick) }, set: { audioPick = $0 })) {
                ForEach(info.audios, id: \.id) { Text(StreamPick.title($0)).tag($0.id) }
            }
            .fixedSize()
        }
    }

    private func loadStream() async {
        do {
            let info = try await store.probeStream(url: url, headers: headers)
            streamInfo = info
            video = StreamPick.variant(info, nil)?.id
            if name == guessed { name = StreamPick.fileName(info); guessed = name }
        } catch {
            streamError = error.localizedDescription
        }
    }

    private var categoryDir: String {
        let cat = store.settings?.categories.first { $0.name == category }
        return expand(CategoryPick.saveDir(cat, defaultDir: settings?.downloadDir ?? "~/Downloads", categoryFolders: settings?.categoryFolders ?? false))
    }

    private func addCategory() {
        let n = newCategory.trimmingCharacters(in: .whitespaces)
        newCategory = ""
        guard !n.isEmpty else { return }
        Task {
            let known = store.settings?.categories.contains { $0.name == n } == true
            if known { category = n; return }
            if await store.update({ $0.categories.append(Category(name: n, exts: OrEmpty(), dir: "")) }) {
                category = n
            } else {
                error = store.settingsError
            }
        }
    }

    private func exists(_ n: String) -> Bool { FileManager.default.fileExists(atPath: (dir as NSString).appendingPathComponent(n)) }
    private func taken(_ n: String) -> Bool { exists(n) || store.items.contains { $0.dir == dir && $0.name == n } }

    private func submit(start: Bool, dup: DupChoice? = nil) {
        var finalName = name.trimmingCharacters(in: .whitespaces)
        guard !finalName.isEmpty else { error = "Dosya adı boş olamaz."; return }
        error = nil
        let listed = store.items.first { $0.url == url && $0.state != .done }
        if dup == nil, settings?.duplicate == "ask", listed != nil || exists(finalName) {
            let numbered = DuplicateName.numbered(finalName, exists: taken)
            let a = DuplicatePrompt.alert(name: finalName, url: url, inList: listed != nil, numbered: numbered)
            guard let c = DuplicatePrompt.choice(a.runModal(), inList: listed != nil, numbered: numbered) else { return }
            if a.suppressionButton?.state == .on, let v = DuplicatePrompt.setting(for: c) {
                Task { await store.update { $0.duplicate = v } }
            }
            if c == .resume, let listed {
                Task { await store.resume([listed.id]); dismiss() }
                return
            }
            return submit(start: start, dup: c)
        }
        switch dup {
        case .number: finalName = DuplicateName.numbered(finalName, exists: taken)
        case .overwrite where exists(finalName):
            // Recoverable: the old file goes to the Trash instead of being unlinked; on failure do not add.
            let old = URL(fileURLWithPath: (dir as NSString).appendingPathComponent(finalName))
            if let msg = Overwrite.trashExisting(old, using: { try FileManager.default.trashItem(at: $0, resultingItemURL: nil) }) {
                error = msg
                return
            }
        default: break
        }
        busy = true
        let explicitName = dup != nil || finalName != guessed
        let replaced = dup == .overwrite ? listed?.id : nil
        let remembered = remember && !category.isEmpty ? (category, abbreviate(dir)) : nil
        Task {
            do {
                if let replaced { await store.remove([replaced]) }
                if let (cat, d) = remembered {
                    await store.update { s in
                        if let i = s.categories.firstIndex(where: { $0.name == cat }) { s.categories[i].dir = d }
                    }
                }
                try await store.add(url: url, dir: dir, name: explicitName ? finalName : nil, category: category,
                                    queue: start ? nil : queueID, description: desc, headers: headers, start: start,
                                    stream: streamInfo.map { StreamPick.spec($0, video: video, audio: audioPick) })
                dismiss()
            } catch {
                self.error = error.localizedDescription
                busy = false
            }
        }
    }
}

// MARK: - Silme onayı

/// "N indirme listeden kaldırılsın mı?" alert. MainWindow calls `ask` before `store.remove`.
enum DeletePrompt {
    static let skipKey = "deleteConfirmSkip", trashKey = "deleteMoveToTrash"

    /// The Settings switch is the inverse of `skipKey`: on = ask.
    static func asks(_ d: UserDefaults = .standard) -> Bool { !d.bool(forKey: skipKey) }
    static func setAsks(_ on: Bool, _ d: UserDefaults = .standard) {
        d.set(!on, forKey: skipKey)
        if on { d.removeObject(forKey: trashKey) }  // the old "don't ask" answer must not come back when the switch is turned off again
    }

    static func text(count: Int, unfinished: Int) -> (title: String, message: String) {
        let title = "\(count) indirme listeden kaldırılsın mı?"
        let tail = "Yarım kalan parçaları silinir ve"
        let message = switch (count, unfinished) {
        case (_, 0): "İndirilen dosyalar diskte kalır."
        case (1, _): "Bu indirme tamamlanmadı. \(tail) baştan başlamak zorunda kalır."
        case (_, 1): "Seçilenlerden biri tamamlanmadı. \(tail) bu indirme baştan başlamak zorunda kalır."
        default: "Seçilenlerden \(unfinished) tanesi tamamlanmadı. \(tail) bu indirmeler baştan başlamak zorunda kalır."
        }
        return (title, message)
    }

    @MainActor static func alert(count: Int, unfinished: Int) -> NSAlert {
        let a = NSAlert()
        let t = text(count: count, unfinished: unfinished)
        a.messageText = t.title
        a.informativeText = t.message
        a.alertStyle = .critical
        a.addButton(withTitle: "Kaldır").hasDestructiveAction = true
        a.addButton(withTitle: "İptal").keyEquivalent = "\u{1b}"
        if unfinished < count {  // finished files exist: offer the Trash
            a.accessoryView = NSButton(checkboxWithTitle: "İndirilen dosyaları da Çöp Kutusu'na taşı", target: nil, action: nil)
        }
        a.showsSuppressionButton = true
        a.suppressionButton?.title = "Bir daha sorma"
        return a
    }

    /// nil = cancelled; otherwise whether finished files go to the Trash. "Bir daha sorma" reuses that answer.
    @MainActor static func ask(_ items: [Item]) -> Bool? {
        let d = UserDefaults.standard
        if !asks(d) { return d.bool(forKey: trashKey) }
        let a = alert(count: items.count, unfinished: items.filter { $0.state != .done }.count)
        guard a.runModal() == .alertFirstButtonReturn else { return nil }
        let trash = (a.accessoryView as? NSButton)?.state == .on
        if a.suppressionButton?.state == .on {
            d.set(true, forKey: skipKey)
            d.set(trash, forKey: trashKey)
        }
        return trash
    }
}

// MARK: - Kimlik doğrulama

/// One-off HTTP Basic credentials for a single add. They travel only as an `Authorization` header on `add`/`probe` (the engine's
/// socket never echoes headers back); nothing is written to the Keychain. Never sent to a plain `http` address.
enum SiteAuth {
    static func problem(user: String, password: String, url: String) -> String? {
        guard !user.trimmingCharacters(in: .whitespaces).isEmpty else { return nil }
        if url.trimmingCharacters(in: .whitespaces).lowercased().hasPrefix("http://") {
            return "Bu adres şifresiz (http): kullanıcı adı ve parola gönderilmez. Göndermek için https adresi kullanın."
        }
        if user.contains(":") { return "Kullanıcı adında “:” olamaz." }
        return nil
    }

    static func headers(user: String, password: String, url: String) -> [String: String] {
        let u = user.trimmingCharacters(in: .whitespaces)
        guard !u.isEmpty, problem(user: user, password: password, url: url) == nil else { return [:] }
        return ["Authorization": "Basic " + Data("\(u):\(password)".utf8).base64EncodedString()]
    }
}

/// "Kimlik doğrulama" disclosure shared by the add and batch sheets.
struct AuthSection: View {
    @Binding var user: String
    @Binding var password: String
    let url: String
    @State private var open = false

    var body: some View {
        DisclosureGroup("Kimlik doğrulama", isExpanded: $open) {
            VStack(alignment: .leading, spacing: 8) {
                TextField("Kullanıcı adı:", text: $user)
                SecureField("Parola:", text: $password)
                Text("Yalnız bu indirme için kullanılır; Anahtarlık'a ve diske kaydedilmez, motor yeniden başlarsa yeniden girilmesi gerekir.").font(.caption).foregroundStyle(.secondary)
                if let p = SiteAuth.problem(user: user, password: password, url: url) {
                    Text(p).font(.caption).foregroundStyle(.orange)
                }
            }
            .padding(.top, 6)
        }
    }
}

extension Store {
    nonisolated static func addArgs(url: String, dir: String? = nil, name: String? = nil, category: String? = nil, queue: String? = nil,
                        description: String? = nil, headers: [String: String] = [:], start: Bool) -> [String: Any] {
        var args: [String: Any] = ["url": url, "start": start]
        if let dir { args["dir"] = dir }
        if let name { args["name"] = name }
        if let category, !category.isEmpty { args["category"] = category }
        if let queue { args["queue"] = queue }
        if let description, !description.isEmpty { args["description"] = description }
        if !headers.isEmpty { args["headers"] = headers }
        return args
    }

    /// `add` with request headers (the Basic credentials of the sheets).
    @discardableResult
    func add(url: String, dir: String? = nil, name: String? = nil, category: String? = nil, queue: String? = nil,
             description: String? = nil, headers: [String: String], start: Bool, stream: StreamSpec? = nil) async throws -> Item {
        var args = Store.addArgs(url: url, dir: dir, name: name, category: category, queue: queue, description: description, headers: headers, start: start)
        if let stream { args["stream"] = try JSONSerialization.jsonObject(with: JSONEncoder().encode(stream)) }
        return try await (client.send("add", args) as ItemReply).item
    }

    func probe(url: String, headers: [String: String]) async throws -> ProbeReply {
        headers.isEmpty ? try await probe(url: url) : try await client.send("probe", ["url": url, "headers": headers])
    }

    func probeStream(url: String, headers: [String: String]) async throws -> StreamInfo {
        headers.isEmpty ? try await probeStream(url: url)
                        : try await (client.send("stream.probe", ["url": url, "headers": headers]) as StreamProbeReply).info
    }
}

// MARK: - Toplu indirme

/// Replaces the single `*` in an http(s) URL with a number range (zero-padded to `digits`) or a letter range (same case).
enum StarBatch {
    static let maxCount = 1000

    static func expand(_ url: String, letters: Bool, from: String, to: String, digits: Int) -> [String]? {
        let parts = url.split(separator: "*", omittingEmptySubsequences: false)
        guard parts.count == 2 else { return nil }
        let values: [String]
        if letters {
            guard from.count == 1, to.count == 1, let a = from.unicodeScalars.first, let b = to.unicodeScalars.first,
                  a.properties.isAlphabetic, b.properties.isAlphabetic, a.isASCII, b.isASCII,
                  from.first?.isLowercase == to.first?.isLowercase, a.value <= b.value else { return nil }
            values = (a.value...b.value).compactMap { Unicode.Scalar($0).map(String.init) }
        } else {
            guard let a = Int(from), let b = Int(to), a >= 0, a <= b, b - a < maxCount else { return nil }
            values = (a...b).map { String(format: "%0\(max(1, min(digits, 12)))d", $0) }
        }
        let urls = values.map { String(parts[0]) + $0 + String(parts[1]) }
        guard let first = urls.first, URLCheck.normalized(first) != nil else { return nil }
        return urls
    }
}

/// What `BatchSheet` sends to the engine for each URL, and the retry loop around it.
enum BatchAdd {
    struct Entry: Equatable { let url: String; let dir: String; let category: String? }

    static func entries(_ urls: [String], cats: [Category], baseDir: String, oneFolder: Bool, categoryFolders: Bool) -> [Entry] {
        urls.map { u in
            let cat = CategoryPick.category(forURL: u, in: cats)
            let dir = oneFolder ? baseDir : CategoryPick.saveDir(cat, defaultDir: baseDir, categoryFolders: categoryFolders)
            return Entry(url: u, dir: expand(dir), category: cat?.name)
        }
    }

    /// Adds the entries after the first `done` (a retry after an error skips what was already added).
    /// Returns the new done count and the error that stopped the run; cancelling the task stops it quietly.
    @MainActor static func run(_ entries: [Entry], done: Int, progress: (Int) -> Void,
                               add: (Entry) async throws -> Void) async -> (done: Int, error: Error?) {
        var done = done
        for e in entries.dropFirst(done) {
            if Task.isCancelled { break }
            do {
                try await add(e)
                done += 1
                progress(done)
            } catch {
                return (done, error)
            }
        }
        return (done, nil)
    }

    /// `run`, then `start` once when asked and everything went in. Entries only ever join the queue (`add` must not pass
    /// `start: true`, which ignores the queue limit); starting the queue honours its "at once" setting.
    @MainActor static func addToQueue(_ entries: [Entry], done: Int, startQueue: Bool, progress: (Int) -> Void,
                                      add: (Entry) async throws -> Void, start: () async -> Void) async -> (done: Int, error: Error?) {
        let r = await run(entries, done: done, progress: progress, add: add)
        if startQueue, r.error == nil, !Task.isCancelled { await start() }
        return r
    }
}

struct BatchSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    @State private var pattern: String
    @State private var letters = false
    @State private var from: String
    @State private var to: String
    @State private var digits: String
    @State private var oneFolder = false
    @State private var user = ""
    @State private var password = ""
    @State private var error: String?
    @State private var done = 0
    @State private var busy = false
    @State private var job: Task<Void, Never>?

    init(pattern: String = "", from: String = "1", to: String = "10", digits: String = "1") {
        _pattern = State(initialValue: pattern)
        _from = State(initialValue: from)
        _to = State(initialValue: to)
        _digits = State(initialValue: digits)
    }

    private var urls: [String]? { StarBatch.expand(pattern, letters: letters, from: from, to: to, digits: Int(digits) ?? 1) }
    private var baseDir: String { store.settings?.downloadDir ?? "~/Downloads" }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("Toplu indirme").font(.headline)
            Text("Sıralı dosya adları için adreste yıldız (*) kullanın; yıldız aşağıdaki aralıkla değiştirilir.")
                .foregroundStyle(.secondary).padding(.top, 4).padding(.bottom, 16)
            Form {
                TextField("Adres:", text: $pattern, prompt: Text("https://example.com/galeri/resim*.jpg"))
                Picker("Yıldız yerine:", selection: $letters) {
                    Text("Sayılar").tag(false)
                    Text("Harfler").tag(true)
                }
                .pickerStyle(.segmented).fixedSize()
                .onChange(of: letters) { from = letters ? "a" : "1"; to = letters ? "z" : "10" }
                LabeledContent("Aralık:") {
                    HStack(spacing: 8) {
                        TextField("Başlangıç", text: $from).labelsHidden().frame(width: 70)
                        Text("ile").foregroundStyle(.secondary)
                        TextField("Bitiş", text: $to).labelsHidden().frame(width: 70)
                        Text("Basamak:").foregroundStyle(.secondary).padding(.leading, 12)
                        TextField("Basamak sayısı", text: $digits).labelsHidden().frame(width: 56).disabled(letters)
                    }
                }
                LabeledContent("Önizleme:") { preview }
                Picker("Kaydetme yeri:", selection: $oneFolder) {
                    Text("Her dosyayı kendi kategorisinin klasörüne").tag(false)
                    (Text("Hepsini tek klasöre: ") + Text(FileManager.default.displayName(atPath: expand(baseDir))).foregroundStyle(.secondary))
                        .tag(true)
                }
                .pickerStyle(.radioGroup)
                AuthSection(user: $user, password: $password, url: pattern)
            }
            .formStyle(.columns)
            if let error { Text(error).foregroundStyle(.red).padding(.top, 10) }
            HStack(spacing: 8) {
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Spacer()
                if busy { ProgressView(value: Double(done), total: Double(urls?.count ?? 1)).frame(width: 120)
                    .accessibilityLabel("Eklenen dosya").accessibilityValue("\(done) / \(urls?.count ?? 1)") }
                Button("Yalnız kuyruğa ekle") { addAll(start: false) }
                    .buttonStyle(.glass).disabled(urls == nil || store.queues.isEmpty || busy)
                Button(urls.map { "\($0.count) dosyayı kuyruğa ekle ve başlat" } ?? "Kuyruğa ekle ve başlat") { addAll(start: true) }
                    .keyboardShortcut(.defaultAction).buttonStyle(.glassProminent).disabled(urls == nil || store.queues.isEmpty || busy)
            }
            .padding(.top, 22)
        }
        .padding(22)
        .frame(width: 620)
        .onChange(of: urls) { done = 0; error = nil }
        .onDisappear { job?.cancel() }  // İptal / Esc must stop the loop, not only hide the sheet
    }

    @ViewBuilder private var preview: some View {
        if let urls {
            let names = urls.map(URLCheck.fileName(from:))
            VStack(alignment: .leading, spacing: 2) {
                ForEach(names.count > 3 ? Array(names.prefix(2)) : names, id: \.self) { Text($0) }
                if names.count > 3 {
                    Text("…").foregroundStyle(.secondary)
                    Text(names[names.count - 1])
                }
                Text("\(urls.count) dosya").font(.caption).foregroundStyle(.secondary).fontDesign(.default)
            }
            .font(.system(size: 12, design: .monospaced)).lineLimit(1).truncationMode(.middle)
            .frame(maxWidth: .infinity, alignment: .leading).padding(.vertical, 8).padding(.horizontal, 10)
            .accessibleGlass(cornerRadius: 12)
        } else if pattern.isEmpty {
            Text("Adresi yazınca ilk ve son dosya burada görünür.").foregroundStyle(.secondary)
        } else {
            Text("Adreste tek bir yıldız (*) ve geçerli bir aralık gerekli.").foregroundStyle(.red)
        }
    }

    private func addAll(start: Bool) {
        guard let urls else { return }
        busy = true
        error = nil
        let entries = BatchAdd.entries(urls, cats: store.settings?.categories ?? [], baseDir: baseDir, oneFolder: oneFolder,
                                       categoryFolders: store.settings?.categoryFolders ?? false)
        let queue = defaultQueue(store.queues)
        let headers = SiteAuth.headers(user: user, password: password, url: pattern)
        job = Task {
            let r = await BatchAdd.addToQueue(entries, done: done, startQueue: start, progress: { done = $0 }, add: {
                try await store.add(url: $0.url, dir: $0.dir, category: $0.category, queue: queue, headers: headers, start: false)
            }, start: { await store.startQueue(queue) })
            if Task.isCancelled { return }
            if let err = r.error {
                self.error = "\(done) dosya eklendi, sonra hata: \(err.localizedDescription)"
                busy = false
                return
            }
            dismiss()
        }
    }
}

// MARK: - Adresi yenile

struct RefreshSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let item: Item
    @State private var url: String
    @State private var error: String?
    @State private var busy = false

    init(item: Item) {
        self.item = item
        _url = State(initialValue: item.url)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Adresi yenile").font(.headline)
            Text("“\(item.name)” için yeni indirme adresini girin.").foregroundStyle(.secondary)
            TextField("Adres:", text: $url).onSubmit(save)
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Button("Yenile", action: save).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent).disabled(busy)
            }
        }
        .padding(20)
        .frame(width: 540)
    }

    private func save() {
        guard let u = URLCheck.normalized(url) else { error = "Geçersiz adres. http veya https adresi girin."; return }
        busy = true
        Task {
            do { try await store.refresh(id: item.id, url: u); dismiss() }
            catch { self.error = error.localizedDescription; busy = false }
        }
    }
}

// MARK: - Taşı veya yeniden adlandır

struct MoveSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let item: Item
    @State private var name: String
    @State private var dir: String
    @State private var pickingFolder = false
    @State private var error: String?
    @State private var busy = false

    init(item: Item) {
        self.item = item
        _name = State(initialValue: item.name)
        _dir = State(initialValue: item.dir)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Taşı veya yeniden adlandır").font(.headline)
            Form {
                TextField("Dosya adı:", text: $name).onSubmit(save)
                LabeledContent("Klasör:") {
                    HStack(spacing: 6) {
                        Text(PathCrumb.parts(dir).joined(separator: "  ›  ")).lineLimit(1).truncationMode(.head)
                            .frame(maxWidth: .infinity, alignment: .leading).help(abbreviate(dir))
                        Button("Değiştir…") { pickingFolder = true }.buttonStyle(.glass)
                    }
                }
            }
            .formStyle(.columns)
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Spacer()
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Button("Taşı", action: save).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent)
                    .disabled(busy || name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 540)
        .fileImporter(isPresented: $pickingFolder, allowedContentTypes: [.folder]) { r in
            if case .success(let u) = r { dir = u.path }
        }
    }

    private func save() {
        busy = true
        Task {
            do {
                let _: ItemReply = try await store.client.send("move", ["item": item.id, "dir": dir, "name": name])
                dismiss()
            } catch { self.error = error.localizedDescription; busy = false }
        }
    }
}

// MARK: - Adres yenileme önerisi (tarayıcıdan aynı ad ve boyut)

/// Spec §2: a browser download with the same name and size as an unfinished item arrives paused with `refreshOf`.
enum RefreshOffer {
    /// The first offer not yet asked about, with the unfinished item it belongs to.
    static func pending(in items: [Item], except asked: Set<String>) -> (offer: Item, old: Item)? {
        for o in items where !asked.contains(o.id) && o.state != .done {
            guard let id = o.refreshOf, !id.isEmpty, let old = items.first(where: { $0.id == id }) else { continue }
            return (o, old)
        }
        return nil
    }
}

struct RefreshOfferSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let offer: Item
    let old: Item
    @State private var error: String?
    @State private var busy = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Yarım kalan indirme").font(.headline)
            Text("“\(old.name)” listede yarım kaldı. Tarayıcıdan aynı ad ve boyutla yeni bir adres geldi. Yeni adresle kaldığı yerden sürdürülsün mü?")
                .foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            Text(offer.url).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            if let error { Text(error).foregroundStyle(.red) }
            HStack {
                Button("Öneriyi sil", role: .destructive) { act { await store.remove([offer.id]) } }.buttonStyle(.glass)  // Esc only closes the sheet; the item stays paused
                Spacer()
                Button("Yeni dosya olarak indir") { act { await store.resume([offer.id]) } }.buttonStyle(.glass)
                Button("Adresi yenile ve sürdür", action: refresh).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent)
            }
            .disabled(busy)
        }
        .padding(20)
        .frame(width: 540)
    }

    private func act(_ f: @escaping () async -> Void) {
        busy = true
        Task { await f(); dismiss() }
    }

    /// The engine takes the browser's fresh headers from the offer and drops the offer item.
    private func refresh() {
        busy = true
        Task {
            do {
                try await store.refresh(id: old.id, url: offer.url)
                await store.resume([old.id])
                dismiss()
            } catch { self.error = error.localizedDescription; busy = false }
        }
    }
}
