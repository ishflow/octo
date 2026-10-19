import AppKit
import SwiftUI
import UniformTypeIdentifiers

private enum ActiveSheet: Identifiable {
    case add, batch
    case refresh(Item)
    case move(Item)
    case refreshOffer(Item, old: Item)
    var id: String {
        switch self {
        case .add: "add"
        case .batch: "batch"
        case .refresh(let i): "refresh-\(i.id)"
        case .move(let i): "move-\(i.id)"
        case .refreshOffer(let o, _): "offer-\(o.id)"
        }
    }
}

/// What the İndirmeler menu needs from the focused main window.
struct MainActions {
    var selected: [Item]
    var refresh: (Item) -> Void
    var move: (Item) -> Void
    var delete: ([String]) -> Void
    var find: () -> Void
}

extension FocusedValues {
    @Entry var mainActions: MainActions?
}

enum SidebarItem: Hashable {
    case all, unfinished, finished
    case category(String)
    case queue(String)
}

struct MainWindow: View {
    @Environment(Store.self) private var store
    @Environment(MainRoute.self) private var route
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings
    @FocusState private var searchFocused: Bool
    @State private var showSpeed = false
    @State private var showInfo = false
    @State private var sidebar: SidebarItem? = .all
    @State private var selection = Set<Item.ID>()
    @State private var sortOrder: [KeyPathComparator<Item>]
    @AppStorage(TableSort.defaultsKey) private var savedSort = ""
    @AppStorage("mainTable.columns") private var columns: TableColumnCustomization<Item>
    @Environment(ListIORoute.self) private var listIO: ListIORoute?
    @State private var query = ""
    @State private var sheet: ActiveSheet?
    @State private var addURL = ""
    @State private var offered = Set<String>()  // refresh offers already shown in this window
    @AppStorage("onboardingDone") private var onboardingDone = false

    init() { _sortOrder = State(initialValue: TableSort.decode(UserDefaults.standard.string(forKey: TableSort.defaultsKey) ?? "")) }

    /// Only what is on screen: a selection hidden by a search or another sidebar section is never acted on.
    private var selectedItems: [Item] { selection.isEmpty ? [] : filtered.filter { selection.contains($0.id) } }
    private var rows: [Item] { filtered.sorted(using: sortOrder) }

    private func items(in s: SidebarItem) -> [Item] {
        switch s {
        case .all: store.items
        case .unfinished: store.items.filter { $0.state != .done }
        case .finished: store.items.filter { $0.state == .done }
        case .category(let c): store.items.filter { $0.category == c }
        case .queue(let q): store.items.filter { $0.queue == q }
        }
    }

    private var title: String {
        switch sidebar ?? .all {
        case .all: "Tüm İndirmeler"
        case .unfinished: "Bitmemiş"
        case .finished: "Bitenler"
        case .category(let c): c
        case .queue(let id): store.queues.first { $0.id == id }?.name ?? "Kuyruk"
        }
    }

    private var filtered: [Item] {
        let base = items(in: sidebar ?? .all)
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return base }
        return base.filter {
            $0.name.localizedCaseInsensitiveContains(q) || $0.url.localizedCaseInsensitiveContains(q)
                || $0.description.localizedCaseInsensitiveContains(q)
        }
    }

    /// Queue the start/stop buttons act on: the selected one, else the main queue.
    private var activeQueue: Queue? {
        if case .queue(let id)? = sidebar { return store.queues.first { $0.id == id } }
        return store.queues.first { $0.id == "main" }
    }

    var body: some View {
        NavigationSplitView {
            sidebarList.navigationSplitViewColumnWidth(min: 170, ideal: 220, max: 280).toolbar(removing: .sidebarToggle)
        } detail: {
            // In the detail column: on the split view the (closed) inspector reserves toolbar width and pushes buttons into ">>".
            content(rows).inspector(isPresented: $showInfo) {
                InfoPanel(item: selectedItems.count == 1 ? selectedItems.first : nil)
                    .inspectorColumnWidth(min: 260, ideal: 320, max: 420)
            }
        }
        .navigationTitle(title)
        .navigationSubtitle(Summary.subtitle(items(in: sidebar ?? .all)))
        .searchable(text: $query, prompt: "Ara")
        .searchFocused($searchFocused)
        .toolbar { toolbar }
        .background { shortcuts }
        .background(WindowFrameRestorer())
        .frame(minWidth: 940, minHeight: 520)
        .dropDestination(for: DroppedLinks.self) { drops, _ in
            DropLinks.handle(drops, single: { route.requestAdd(url: $0) }, multiple: { listIO?.pending = .init(urls: $0, skipped: 0) })
        }
        .onChange(of: TableSort.encode(sortOrder)) { savedSort = $1 }
        .onChange(of: route.addRequested, initial: true) { if route.takeAdd() { addURL = route.takeURL(); sheet = .add } }
        .focusedSceneValue(\.mainActions, MainActions(selected: selectedItems, refresh: { sheet = .refresh($0) }, move: { sheet = .move($0) },
                                                      delete: { confirmDelete($0) }, find: { searchFocused = true }))
        .onChange(of: store.items.filter { $0.refreshOf?.isEmpty == false }.map(\.id), initial: true) { showOffer() }
        .sheet(item: $sheet, onDismiss: { addURL = ""; showOffer() }) { s in
            switch s {
            case .add: AddDownloadSheet(url: addURL) { sheet = .batch }
            case .batch: BatchSheet()
            case .refresh(let item): RefreshSheet(item: item)
            case .move(let item): MoveSheet(item: item)
            case .refreshOffer(let offer, let old): RefreshOfferSheet(offer: offer, old: old)
            }
        }
    }

    private func showOffer() {
        guard sheet == nil, let p = RefreshOffer.pending(in: store.items, except: offered) else { return }
        offered.insert(p.offer.id)
        sheet = .refreshOffer(p.offer, old: p.old)
    }

    /// Window-wide shortcuts the design keeps out of the toolbar.
    private var shortcuts: some View {
        Group {
            Button("Bilgi") { showInfo.toggle() }.keyboardShortcut("i")
        }
        .opacity(0)
        .frame(width: 0, height: 0)
        .accessibilityHidden(true)
    }

    private func confirmDelete(_ ids: [String]) {
        let its = store.items.filter { ids.contains($0.id) }
        if let trash = DeletePrompt.ask(its) { delete(ids, deleteFile: trash) }
    }

    private func move(_ ids: [String], to queue: String) {
        Task { do { try await store.moveToQueue(ids, queue: queue) } catch { store.lastError = error.localizedDescription } }
    }

    private func delete(_ ids: [String], deleteFile: Bool) {
        selection.subtract(ids)
        Task { await store.remove(ids, deleteFile: deleteFile) }
    }

    // MARK: row actions

    @ViewBuilder private func rowMenu(_ ids: Set<Item.ID>) -> some View {
        let its = store.items.filter { ids.contains($0.id) }
        if let one = its.first {
            Button("Aç") { FileActions.open(one) }
                .keyboardShortcut("o")
                .disabled(its.count != 1 || one.state != .done || !FileManager.default.fileExists(atPath: FileActions.path(one).path))
            Button("Finder'da göster") { FileActions.reveal(one) }.disabled(its.count != 1)
            Divider()
            Button("Devam et") { Task { await store.resume(its.map(\.id)) } }
                .disabled(!its.contains { $0.state == .paused || $0.state == .error })
            Button("Durdur") { Task { await store.pause(its.map(\.id)) } }
                .disabled(!its.contains { $0.state == .downloading || $0.state == .queued })
            Button("Yeniden indir") { Task { await store.redownload(its.map(\.id)) } }
            Button("Hız sınırı…") { openWindow(id: "progress", value: one.id + "#limit") }
                .disabled(its.count != 1 || one.state == .done)
            Divider()
            Button("Adresi kopyala") {
                NSPasteboard.general.clearContents()
                NSPasteboard.general.setString(its.map(\.url).joined(separator: "\n"), forType: .string)
            }
            Button("Bağlantıyı yenile…") { sheet = .refresh(one) }.disabled(its.count != 1)
            Button("Taşı veya yeniden adlandır…") { sheet = .move(one) }.disabled(its.count != 1 || one.state == .downloading)
            Menu("Kuyruğa taşı") {
                ForEach(store.queues) { q in
                    Button(q.name) { move(its.map(\.id), to: q.id) }.disabled(its.allSatisfy { $0.queue == q.id })
                }
            }
            .disabled(store.queues.isEmpty)
            Divider()
            Button("Bilgi") { selection = [one.id]; showInfo = true }
                .keyboardShortcut("i")
                .disabled(its.count != 1)
            Divider()
            Button("Listeden kaldır…", role: .destructive) { confirmDelete(its.map(\.id)) }
                .keyboardShortcut(.delete, modifiers: [])
        }
    }

    // MARK: sidebar

    private var sidebarList: some View {
        let counts = SidebarCount.of(store.items)
        func row(_ title: String, _ icon: String, _ tag: SidebarItem) -> some View {
            Label(title, systemImage: icon).badge(counts[tag] ?? 0).tag(tag)
        }
        return List(selection: $sidebar) {
            Section("İndirmeler") {
                row("Tüm İndirmeler", "arrow.down.circle", .all)
                row("Bitmemiş", "clock", .unfinished)
                row("Bitenler", "checkmark.circle", .finished)
            }
            if let cats = store.settings?.categories, !cats.isEmpty {
                Section("Kategoriler") {
                    ForEach(cats, id: \.name) { c in
                        row(c.name, c.exts.first.map(FileIcon.symbol(forExt:)) ?? "doc", .category(c.name))
                    }
                }
            }
            if !store.queues.isEmpty {
                Section("Kuyruklar") {
                    ForEach(store.queues) { q in row(q.name, q.running ? "play.circle.fill" : "list.bullet", .queue(q.id)) }
                }
            }
        }
        .listStyle(.sidebar)
        .safeAreaInset(edge: .top, spacing: 0) {
            HStack(spacing: 8) {
                Image(nsImage: NSApp.applicationIconImage).resizable().frame(width: 22, height: 22)
                Text("Octo").font(.headline)
                Spacer()
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 6)
            .accessibilityElement(children: .combine)
        }
    }

    // MARK: table

    /// `rows` is passed in: computing it filters and sorts the whole list, once per redraw is enough.
    private func content(_ rows: [Item]) -> some View {
        Group {
            if rows.isEmpty {
                if !query.isEmpty {
                    ContentUnavailableView.search(text: query)
                } else if !store.connected {
                    ContentUnavailableView("Motora bağlanılamadı", systemImage: "bolt.horizontal.circle",
                                           description: Text(store.lastError ?? "Bağlanılıyor…"))
                } else {
                    emptyState
                }
            } else {
                table(rows)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .safeAreaInset(edge: .bottom) { statusPill(rows) }
        .onPasteCommand(of: [.url, .plainText]) { _ in sheet = .add }  // the add sheet picks the link up from the clipboard
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(nsImage: NSApp.applicationIconImage).resizable().frame(width: 72, height: 72).accessibilityHidden(true)
            Text("Henüz indirme yok").font(.title3.weight(.semibold)).padding(.top, 6)
            Text("Bir bağlantıyı bu pencereye ya da Dock simgesine sürükleyin. Tarayıcıda bir dosyaya tıkladığınızda da burada görünür.")
                .foregroundStyle(.secondary)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 380)
            HStack(spacing: 8) {
                Button("URL ekle") { sheet = .add }.buttonStyle(.glassProminent)
                Button("Tarayıcı eklentisini kur") { onboardingDone = false }.buttonStyle(.glass)
            }
            .padding(.top, 8)
            Text("⌘N yeni indirme · ⌘V panodaki bağlantıyı ekler").font(.caption).foregroundStyle(.secondary).padding(.top, 4)
        }
        .padding(.bottom, 60)
    }

    private func statusPill(_ rows: [Item]) -> some View {
        HStack(spacing: 14) {
            Text(Summary.counts(rows))
            if store.settings != nil { Text(Summary.limits(store.settings)) }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
        .monospacedDigit()
        .padding(.horizontal, 14)
        .frame(height: 28)
        .glassEffect(.regular, in: .capsule)
        .padding(.bottom, 12)
    }

    private func table(_ rows: [Item]) -> some View {
        // Header right-click: show or hide columns. Q is only a badge in the sidebar, there is no Sıra / Sunucu column.
        Table(rows, selection: $selection, sortOrder: $sortOrder, columnCustomization: $columns) {
            TableColumn("Dosya adı", value: \.name) { item in
                Label(item.name, systemImage: FileIcon.symbol(forName: item.name))
            }
            .width(min: 160, ideal: 280)
            .customizationID("name")
            .disabledCustomizationBehavior(.visibility)  // the row would have no label
            TableColumn("Boyut", value: \.size) { Text(Format.bytes($0.size)).monospacedDigit() }
                .width(min: 60, ideal: 80)
                .customizationID("size")
            TableColumn("Durum", value: \.stateSort) { StatusCell(item: $0) }
                .width(min: 110, ideal: 150)
                .customizationID("state")
            TableColumn("Kalan süre", value: \.eta) { Text(Format.eta($0.eta)).monospacedDigit() }
                .width(min: 70, ideal: 90)
                .customizationID("eta")
            TableColumn("Hız", value: \.speed) { Text(Format.speed($0.speed)).monospacedDigit() }
                .width(min: 70, ideal: 90)
                .customizationID("speed")
            TableColumn("Son deneme", value: \.lastTrySort) { Text(Format.date($0.lastTry)) }
                .width(min: 90, ideal: 130)
                .customizationID("lastTry")
            TableColumn("Açıklama", value: \.description) { Text($0.description).foregroundStyle(.secondary) }
                .customizationID("description")
        }
        .contextMenu(forSelectionType: Item.ID.self) { ids in
            rowMenu(ids)
        } primaryAction: { ids in
            guard let i = store.items.first(where: { ids.contains($0.id) }) else { return }
            if i.state == .done { FileActions.open(i) } else { openWindow(id: "progress", value: i.id) }
        }
        .onDeleteCommand { if !selectedItems.isEmpty { confirmDelete(selectedItems.map(\.id)) } }
    }

    // MARK: toolbar

    @ToolbarContentBuilder private var toolbar: some ToolbarContent {
        let sel = selectedItems
        ToolbarItem {
            Menu {
                Button("Toplu indirme…") { sheet = .batch }
            } label: {
                Label("URL ekle", systemImage: "plus")
            } primaryAction: { sheet = .add }
            .help("URL ekle (⌘N)")
        }
        ToolbarSpacer(.fixed)
        ToolbarItemGroup {
            Button { Task { await store.resume(sel.map(\.id)) } } label: { Label("Devam", systemImage: "play.fill") }
                .help("Seçilenleri başlat / devam et")
                .disabled(!ItemActions.canResume(sel))
            Button { Task { await store.pause(sel.map(\.id)) } } label: { Label("Durdur", systemImage: "pause.fill") }
                .help("Seçilenleri durdur")
                .disabled(!ItemActions.canPause(sel))
            Button { Task { await store.stopAll() } } label: { Label("Tümünü durdur", systemImage: "stop.fill") }
                .help("Tüm indirmeleri durdur")
                .disabled(!ItemActions.canPause(store.items))
        }
        ToolbarSpacer(.fixed)
        ToolbarItemGroup {
            Button { confirmDelete(sel.map(\.id)) } label: { Label("Kaldır", systemImage: "trash") }
                .help("Seçilenleri listeden kaldır")
                .disabled(sel.isEmpty)
            Button {
                let done = store.items.filter { $0.state == .done }.map(\.id)
                selection.subtract(done)
                Task { await store.remove(done) }  // list only: the finished files stay on disk
            } label: { Label("Bitenleri kaldır", systemImage: "xmark.bin") }
                .help("Tamamlananları listeden kaldır")
                .disabled(!store.items.contains { $0.state == .done })
        }
        ToolbarSpacer(.fixed)
        ToolbarItemGroup {
            Button { openWindow(id: "scheduler") } label: { Label("Zamanlayıcı", systemImage: "calendar") }
                .help("Zamanlayıcı ve kuyruklar")
            if activeQueue?.running == true {
                Button { if let q = activeQueue { Task { await store.stopQueue(q.id) } } } label: {
                    Label("Kuyruğu durdur", systemImage: "stop.circle")
                }
                .help("\(activeQueue?.name ?? "Kuyruğu") durdur")
            } else {
                Button { if let q = activeQueue { Task { await store.startQueue(q.id) } } } label: {
                    Label("Kuyruğu başlat", systemImage: "play.circle")
                }
                .help("\(activeQueue?.name ?? "Kuyruğu") başlat")
                .disabled(activeQueue == nil)
            }
            Button { showSpeed.toggle() } label: {
                Label("Hız sınırı", systemImage: store.settings?.speedLimitOn == true ? "gauge.with.needle.fill" : "gauge.with.needle")
            }
            .help("Hız sınırı")
            .popover(isPresented: $showSpeed, arrowEdge: .bottom) { SpeedPopover() }
        }
        ToolbarSpacer(.fixed)
        ToolbarItem {
            Button { openSettings() } label: { Label("Ayarlar", systemImage: "gearshape") }
                .help("Ayarlar (⌘,)")
        }
    }
}

private struct StatusCell: View {
    let item: Item
    @Environment(\.backgroundProminence) private var prominence  // selected row: keep text readable on the accent

    var body: some View {
        // Tooltip: the engine restarted this download from zero / a video's segment count / a remark on the finished file.
        cell.help(InfoPanel.restartNote(item) ?? (item.state == .downloading ? StreamProgress.line(item.stream) : item.note) ?? "")
    }

    @ViewBuilder private var cell: some View {
        if let s = StatusLabel.of(item) {
            Text(s.text).foregroundStyle(prominence == .increased ? AnyShapeStyle(.primary) : AnyShapeStyle(s.tone.color)).lineLimit(1)
        } else if let f = item.fraction {
            HStack(spacing: 8) {
                ProgressView(value: f).progressViewStyle(.linear).frame(minWidth: 50)
                Text(Format.percent(f)).monospacedDigit().frame(width: 38, alignment: .trailing)
            }
        }
    }
}

/// Re-applies the saved frame: SwiftUI cascades the reopened window away from it on every launch.
private struct WindowFrameRestorer: NSViewRepresentable {
    final class Probe: NSView {
        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            guard let w = window else { return }
            DispatchQueue.main.async {  // after SwiftUI has placed the window
                w.setFrameUsingName("main")
                w.setFrameAutosaveName("main")
            }
        }
    }
    func makeNSView(context: Context) -> NSView { Probe() }
    func updateNSView(_ nsView: NSView, context: Context) {}
}

enum FileActions {
    static func path(_ i: Item) -> URL { URL(fileURLWithPath: i.dir).appendingPathComponent(i.name) }

    @MainActor static func open(_ i: Item) {
        if FileManager.default.fileExists(atPath: path(i).path) { NSWorkspace.shared.open(path(i)) } else { Alerts.missing(i) }
    }

    static func reveal(_ i: Item) {
        if FileManager.default.fileExists(atPath: path(i).path) {
            NSWorkspace.shared.activateFileViewerSelecting([path(i)])
        } else {
            NSWorkspace.shared.open(URL(fileURLWithPath: i.dir))
        }
    }
}

/// Text or a URL dragged from a browser, the Finder or an editor; only its http(s) links matter.
struct DroppedLinks: Transferable {
    let text: String
    static var transferRepresentation: some TransferRepresentation {
        DataRepresentation(importedContentType: .url) { DroppedLinks(text: String(decoding: $0, as: UTF8.self)) }
        DataRepresentation(importedContentType: .plainText) { DroppedLinks(text: String(decoding: $0, as: UTF8.self)) }
    }
}

enum DropLinks {
    /// One link goes to `single` (the add sheet), several to `multiple`. False (drop rejected) when the drop holds no http(s) link.
    static func handle(_ drops: [DroppedLinks], single: (String) -> Void, multiple: ([String]) -> Void) -> Bool {
        let links = URLCheck.extract(from: drops.map(\.text).joined(separator: "\n"))
        switch links.count {
        case 0: return false
        case 1: single(links[0])
        default: multiple(links)
        }
        return true
    }
}

/// Main table sort, saved as `name+,size-` (column, then + ascending or - descending) so it survives a relaunch.
enum TableSort {
    static let defaultsKey = "mainTable.sort"
    private typealias Maker = (SortOrder) -> KeyPathComparator<Item>
    private static var columns: [(key: String, path: PartialKeyPath<Item>, make: Maker)] { [
        ("name", \Item.name, { .init(\.name, order: $0) }),
        ("size", \Item.size, { .init(\.size, order: $0) }),
        ("state", \Item.stateSort, { .init(\.stateSort, order: $0) }),
        ("eta", \Item.eta, { .init(\.eta, order: $0) }),
        ("speed", \Item.speed, { .init(\.speed, order: $0) }),
        ("lastTry", \Item.lastTrySort, { .init(\.lastTrySort, order: $0) }),
        ("description", \Item.description, { .init(\.description, order: $0) }),
        ("added", \Item.added, { .init(\.added, order: $0) }),
    ] }

    static func keyName(_ path: PartialKeyPath<Item>) -> String? { columns.first { $0.path == path }?.key }

    static func encode(_ order: [KeyPathComparator<Item>]) -> String {
        order.compactMap { c in keyName(c.keyPath).map { $0 + (c.order == .reverse ? "-" : "+") } }.joined(separator: ",")
    }

    /// Unreadable or empty input gives the order of addition, like classic download managers.
    static func decode(_ s: String) -> [KeyPathComparator<Item>] {
        let parsed = s.split(separator: ",").compactMap { part -> KeyPathComparator<Item>? in
            guard let sign = part.last, let order: SortOrder = sign == "+" ? .forward : sign == "-" ? .reverse : nil,
                  let col = columns.first(where: { $0.key == part.dropLast() }) else { return nil }
            return col.make(order)
        }
        return parsed.isEmpty ? [KeyPathComparator(\Item.added)] : parsed
    }
}

enum Alerts {
    /// Pausing a download the server cannot resume throws its progress away; true = pause anyway.
    @MainActor static func confirmPause(_ items: [Item]) -> Bool {
        let a = NSAlert()
        a.messageText = items.count == 1 ? "“\(items[0].name)” devam ettirilemez" : "\(items.count) indirme devam ettirilemez"
        a.informativeText = "Sunucu kaldığı yerden devam etmeyi desteklemiyor. Durdurursanız indirme baştan başlar."
        a.addButton(withTitle: "İndirmeye devam et")
        a.addButton(withTitle: "Yine de durdur")
        NSApp.activate()  // also asked from the menu-bar extra, where the app may be in the background
        return a.runModal() == .alertSecondButtonReturn
    }

    @MainActor static func missing(_ i: Item) {
        let a = NSAlert()
        a.messageText = "“\(i.name)” bulunamadı"
        a.informativeText = "Dosya taşınmış ya da silinmiş olabilir: \(FileActions.path(i).path)"
        a.runModal()
    }
}
