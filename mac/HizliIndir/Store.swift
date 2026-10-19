import AppKit
import Foundation
import Observation

/// Settings > Genel switch (UserDefaults, off by default): ask about links copied to the clipboard.
enum ClipboardWatch {
    static let key = "watchClipboard"

    /// The clipboard text as a link worth asking about: http(s) and a file type some category knows. Anything else stays silent.
    static func link(_ s: String?, in cats: [Category]) -> String? {
        guard let u = URLCheck.clipboardURL(s), CategoryPick.category(forURL: u, in: cats) != nil else { return nil }
        return u
    }
}

/// Settings > Gelişmiş switch (UserDefaults, on by default): the Mac does not idle-sleep while a download runs.
enum KeepAwake {
    static let key = "keepAwakeWhileDownloading"
    static func enabled(_ d: UserDefaults = .standard) -> Bool { d.object(forKey: key) as? Bool ?? true }
}

@MainActor @Observable
final class Store {
    var items: [Item] = []
    var queues: [Queue] = []
    var settings: Settings?
    var connected = false
    var lastError: String?
    /// Last failed `update` (validation or engine refusal); the Settings window shows it.
    var settingsError: String?

    @ObservationIgnored let client: EngineClient
    /// Where the clipboard switch and the per-item "open when done" flags live; tests pass a throwaway suite.
    @ObservationIgnored let defaults: UserDefaults
    /// Called once when a known item turns `done` (not for items that arrive already finished).
    @ObservationIgnored var onFinished: ((Item) -> Void)?
    /// Called once when a known item turns `error`.
    @ObservationIgnored var onFailed: ((Item) -> Void)?
    /// Called once per item that arrives with a `refreshOf` offer (the sheet only exists while the main window is open).
    @ObservationIgnored var onRefreshOffer: ((Item) -> Void)?
    @ObservationIgnored private var offered = Set<String>()
    /// Countdown started by the engine's `action` event (sleep/shutdown after the queue); nil when none.
    var pendingAction: PendingAction?
    /// Asked before pausing downloads that cannot resume (they restart from zero); true = pause anyway. The app shows an alert.
    @ObservationIgnored var confirmPause: ([Item]) -> Bool = { _ in true }
    @ObservationIgnored var now: () -> Date = Date.init
    /// Clipboard change count and a lazy text read; the text is only read once the count has moved.
    @ObservationIgnored var pasteboard: () -> (count: Int, string: () -> String?) = {
        let p = NSPasteboard.general
        return (p.changeCount, { p.string(forType: .string) })
    }
    /// A new link appeared on the clipboard. Default: hand it to the app itself, which opens the add sheet filled in (as a Dock drop does).
    @ObservationIgnored var onClipboardLink: (String) -> Void = {
        guard let u = URL(string: $0) else { return }
        NSWorkspace.shared.open([u], withApplicationAt: Bundle.main.bundleURL, configuration: NSWorkspace.OpenConfiguration())
    }
    /// Idle-sleep hold while something downloads. Idle sleep only: a scheduled "sleep" queue action or the lid still sleep the Mac.
    @ObservationIgnored var keepAwake: () -> Bool = { KeepAwake.enabled() }
    @ObservationIgnored var beginActivity: () -> NSObjectProtocol = {
        ProcessInfo.processInfo.beginActivity(options: .idleSystemSleepDisabled, reason: "Octo indiriyor")
    }
    @ObservationIgnored var endActivity: (NSObjectProtocol) -> Void = { ProcessInfo.processInfo.endActivity($0) }
    @ObservationIgnored private var activity: NSObjectProtocol?
    @ObservationIgnored private var seenClipboard: Int?
    /// id → position in `items`; checked on every use and rebuilt when stale (removals, direct writes).
    @ObservationIgnored private var slot: [String: Int] = [:]

    init(client: EngineClient = EngineClient(), defaults: UserDefaults = .standard) {
        self.client = client
        self.defaults = defaults
    }

    /// Connects, loads the snapshot, then applies pushed events; reconnects when the link drops.
    func run() async {
        async let _: Void = watchClipboard()
        while !Task.isCancelled {
            do {
                try await client.connect()
                let events = await client.eventStream()
                let _: Ack = try await client.send("subscribe")
                apply(try await client.send("list") as ListReply)
                connected = true
                lastError = nil
                for await e in events {
                    apply(e)
                    if case .disconnected = e { break }
                }
            } catch {
                lastError = error.localizedDescription
            }
            connected = false
            releaseSleepGuard()  // the item list is stale until the next snapshot
            try? await Task.sleep(for: .seconds(2))
        }
    }

    private func watchClipboard() async {
        while !Task.isCancelled {
            pollClipboard()
            try? await Task.sleep(for: .seconds(1))
        }
    }

    /// One look at the clipboard. Switching the setting on only records the current count, so what was copied earlier is not asked about.
    func pollClipboard() {
        guard defaults.bool(forKey: ClipboardWatch.key) else { seenClipboard = nil; return }
        let p = pasteboard()
        defer { seenClipboard = p.count }
        guard let seen = seenClipboard, seen != p.count, let cats = settings?.categories,
              let link = ClipboardWatch.link(p.string(), in: cats), !items.contains(where: { $0.url == link }) else { return }
        onClipboardLink(link)
    }

    func apply(_ r: ListReply) {
        items = r.items
        queues = r.queues
        settings = r.settings
        if let a = r.pendingAction { apply(.action(a.action, seconds: a.seconds)) }
        syncSleepGuard()
        let prefix = FinishActions.openKey("")
        for k in defaults.dictionaryRepresentation().keys where k.hasPrefix(prefix) {
            let id = String(k.dropFirst(prefix.count))
            if !items.contains(where: { $0.id == id }) { defaults.removeObject(forKey: k) }
        }
    }

    func apply(_ e: EngineEvent) {
        defer { if case .progress = e {} else { syncSleepGuard() } }
        switch e {
        case .progress(let ups):
            for u in ups {
                guard let i = index(of: u.id) else { continue }
                let was = items[i].state
                items[i].state = u.state
                items[i].got = u.got
                items[i].size = u.size
                items[i].speed = u.speed
                items[i].eta = u.eta
                items[i].conns = u.conns
                if items[i].stream != nil {
                    items[i].stream?.segDone = u.segDone ?? 0
                    let segments = u.segments ?? items[i].stream?.segments  // read first: one statement reading and writing items[i] aborts (exclusivity)
                    items[i].stream?.segments = segments
                    items[i].stream?.phase = u.phase  // omitted once merging is over
                }
                finished(was, items[i])
                if was != u.state { syncSleepGuard() }
            }
        case .item(let it):
            if let i = index(of: it.id) {
                let was = items[i].state
                items[i] = it
                finished(was, it)
            } else {
                items.append(it)
            }
            if it.refreshOf?.isEmpty == false, offered.insert(it.id).inserted { onRefreshOffer?(it) }
        case .removed(let ids):
            items.removeAll { ids.contains($0.id) }
            ids.forEach { defaults.removeObject(forKey: FinishActions.openKey($0)) }
        case .queues(let q):
            queues = q
        case .action(let kind, let seconds):
            pendingAction = PendingAction(kind: kind, deadline: now().addingTimeInterval(TimeInterval(seconds)))
        case .unknown, .disconnected:
            break
        }
    }

    /// Holds or drops the idle-sleep assertion to match "switch on and something downloading". Cheap when nothing changed.
    func syncSleepGuard() {
        if keepAwake() && items.contains(where: { $0.state == .downloading }) {
            if activity == nil { activity = beginActivity() }
        } else {
            releaseSleepGuard()
        }
    }

    private func releaseSleepGuard() {
        if let a = activity { endActivity(a); activity = nil }
    }

    /// Same answer as `items.firstIndex { $0.id == id }`, without the linear scan per progress update.
    private func index(of id: String) -> Int? {
        if let i = slot[id], i < items.count, items[i].id == id { return i }
        slot = Dictionary(items.indices.map { (items[$0].id, $0) }, uniquingKeysWith: { first, _ in first })
        return slot[id]
    }

    private func finished(_ was: ItemState, _ now: Item) {
        if was != .done && now.state == .done { onFinished?(now) }
        if was != .error && now.state == .error { onFailed?(now) }
    }

    // MARK: - commands (state arrives back as events)

    /// false when the user kept a download that cannot resume running.
    @discardableResult
    func pause(_ ids: [String]) async -> Bool {
        guard mayStop(ids) else { return false }
        await run("pause", ["ids": ids])
        return true
    }

    private func mayStop(_ ids: [String]) -> Bool {
        let lost = items.filter { ids.contains($0.id) && $0.state == .downloading && !$0.resumable }
        return lost.isEmpty || confirmPause(lost)
    }

    func resume(_ ids: [String]) async { await run("resume", ["ids": ids]) }
    /// Starts from zero: the engine drops the `.part`; a finished file stays where it is.
    func redownload(_ ids: [String]) async { await run("redownload", ["ids": ids]) }
    func remove(_ ids: [String], deleteFile: Bool = false) async { await run("remove", ["ids": ids, "deleteFile": deleteFile]) }
    // The request id owns the JSON key "id", so the target queue travels as "queue" (the engine accepts id|queue|item).
    func startQueue(_ id: String) async { await run("queue.start", ["queue": id]) }
    @discardableResult
    func stopQueue(_ id: String) async -> Bool {
        guard mayStop(queues.first { $0.id == id }?.items ?? []) else { return false }
        await run("queue.stop", ["queue": id])
        return true
    }

    /// `queue.save`; the `queues` event brings the stored copy back.
    func saveQueue(_ q: Queue) async throws {
        let obj = try JSONSerialization.jsonObject(with: JSONEncoder().encode(q))
        let _: Ack = try await client.send("queue.save", ["queue": obj])
        if let i = queues.firstIndex(where: { $0.id == q.id }) { queues[i] = q } else { queues.append(q) }
    }

    func deleteQueue(_ id: String) async throws { let _: Ack = try await client.send("queue.delete", ["queue": id]) }

    func moveToQueue(_ ids: [String], queue: String) async throws {
        let _: Ack = try await client.send("queue.move", ["ids": ids, "queue": queue])
    }

    func cancelAction() async {
        pendingAction = nil
        await run("action.cancel", [:])
    }

    @discardableResult
    func stopAll() async -> Bool {
        let active = items.filter { $0.state == .downloading || $0.state == .queued }.map(\.id)
        return active.isEmpty ? true : await pause(active)
    }

    /// `add`; the new item arrives as an event too. Throws the engine's Turkish error text.
    @discardableResult
    func add(url: String, dir: String? = nil, name: String? = nil, category: String? = nil,
             queue: String? = nil, description: String? = nil, start: Bool, stream: StreamSpec? = nil) async throws -> Item {
        var args: [String: Any] = ["url": url, "start": start]
        if let dir { args["dir"] = dir }
        if let name { args["name"] = name }
        if let category, !category.isEmpty { args["category"] = category }
        if let queue { args["queue"] = queue }
        if let description, !description.isEmpty { args["description"] = description }
        if let stream { args["stream"] = try JSONSerialization.jsonObject(with: JSONEncoder().encode(stream)) }
        return try await (client.send("add", args) as ItemReply).item
    }

    /// `settings.set`; the contract has no settings event, so the local copy is updated on success.
    func saveSettings(_ s: Settings) async throws {
        let obj = try JSONSerialization.jsonObject(with: JSONEncoder().encode(s))
        let _: Ack = try await client.send("settings.set", ["settings": obj])
        settings = s
    }

    /// Live-apply one edit: validated, skipped when nothing changed, shown at once and reverted if the engine refuses.
    @discardableResult
    func update(_ change: (inout Settings) -> Void) async -> Bool {
        guard let old = settings else { return false }
        var new = old
        change(&new)
        if new == old { settingsError = nil; return true }
        if let p = SettingsCheck.problem(new) { settingsError = p; return false }
        settings = new
        do { try await saveSettings(new); settingsError = nil; return true } catch {
            if settings == new { settings = old }  // a later edit already replaced it: keep that one
            settingsError = error.localizedDescription
            return false
        }
    }

    /// `probe`: name, size and resumability without adding. Throws the engine's Turkish error text.
    func probe(url: String) async throws -> ProbeReply { try await client.send("probe", ["url": url]) }

    /// `stream.probe`: the qualities and audio tracks of an HLS/DASH manifest. Throws the engine's Turkish error text.
    func probeStream(url: String) async throws -> StreamInfo { try await (client.send("stream.probe", ["url": url]) as StreamProbeReply).info }

    func refresh(id: String, url: String) async throws {
        let _: ItemReply = try await client.send("refresh", ["item": id, "url": url])
    }

    /// `item.limit`: per-file speed limit in KB/s (0 = off), applied live by the engine. Throws its Turkish error text.
    func setItemLimit(_ id: String, kbps: Int) async throws {
        let r: ItemReply = try await client.send("item.limit", ["item": id, "kbps": kbps])
        apply(.item(r.item))
    }

    /// `item.describe`: the info panel's description edit; the reply is applied at once so the field does not flash back.
    func describe(id: String, text: String) async throws {
        let r: ItemReply = try await client.send("item.describe", ["item": id, "description": text])
        apply(.item(r.item))
    }

    private func run(_ cmd: String, _ args: sending [String: Any]) async {
        do { let _: Ack = try await client.send(cmd, args) } catch { lastError = error.localizedDescription }
    }
}
