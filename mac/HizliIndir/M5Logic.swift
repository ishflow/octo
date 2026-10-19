import Foundation

// Pure helpers for the menu-bar extra, notifications and the sleep/shutdown countdown (unit-tested).

struct ActiveSummary {
    var active: [Item]
    var totalSpeed: Int64
    var count: Int { active.count }

    static func make(_ items: [Item]) -> ActiveSummary {
        let a = items.filter { $0.state == .downloading }
        return ActiveSummary(active: a, totalSpeed: a.reduce(0) { $0 + max($1.speed, 0) })
    }
}

enum CountdownText {
    static func text(action: String, seconds: Int) -> String {
        let s = max(seconds, 0)
        return action == "sleep" ? "Mac \(s) sn içinde uyku moduna geçecek" : "Mac \(s) sn içinde kapanacak"
    }

    /// Whole seconds left, rounded up so the label never shows 0 while time remains.
    static func remaining(until deadline: Date, now: Date) -> Int {
        max(0, Int(deadline.timeIntervalSince(now).rounded(.up)))
    }
}

/// Engine `action` event: Mac will `kind` (sleep|shutdown) at `deadline` unless cancelled.
struct PendingAction: Equatable, Sendable {
    var kind: String
    var deadline: Date
}

struct NoteContent: Equatable {
    var identifier: String
    var title: String
    var subtitle: String
    var body: String
    var category: String?
    var path: String
}

enum NotificationMap {
    static func content(for item: Item) -> NoteContent? {
        let path = URL(fileURLWithPath: item.dir).appendingPathComponent(item.name).path
        switch item.state {
        case .done:
            return NoteContent(identifier: "done-\(item.id)", title: "İndirme tamamlandı", subtitle: "", body: doneBody(item),
                               category: "done", path: path)
        case .error:
            return NoteContent(identifier: "error-\(item.id)", title: "İndirme başarısız", subtitle: item.name,
                               body: item.error.isEmpty ? "Bilinmeyen hata" : item.error, category: nil, path: path)
        default:
            return nil
        }
    }

    /// "<name> · <size> · <time taken>"; the time is left out when the timestamps are missing.
    static func doneBody(_ i: Item) -> String {
        let took = date(i.finished).flatMap { end in date(i.lastTry ?? i.added).map { Int(end.timeIntervalSince($0).rounded()) } } ?? -1
        return [i.name, Format.bytes(i.size >= 0 ? i.size : i.got), Format.eta(took)].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    private static func date(_ iso: String?) -> Date? {
        guard let iso else { return nil }
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f.date(from: iso) ?? ISO8601DateFormatter().date(from: iso)
    }
}

// Per-download "open when done" flag (UserDefaults); the Store drops flags of items that are gone.
enum FinishActions {
    static func openKey(_ id: String) -> String { "onDone.open.\(id)" }
}

/// Which selection-wide actions apply (toolbar and the İndirmeler menu).
enum ItemActions {
    static func canResume(_ items: [Item]) -> Bool { items.contains { $0.state == .paused || $0.state == .error } }
    static func canPause(_ items: [Item]) -> Bool { items.contains { $0.state == .downloading || $0.state == .queued } }
}

/// Lets the menu bar extra and the app menu open the main window's add sheet, even before that window exists.
@MainActor @Observable final class MainRoute {
    var addRequested = false
    /// URL the add sheet opens with (dropped on the Dock icon, `open -a Octo <url>`); empty = the sheet checks the clipboard.
    var addURL = ""
    func requestAdd(url: String = "") {
        addURL = url
        addRequested = true
    }
    func takeURL() -> String { defer { addURL = "" }; return addURL }
    /// True once per request; the main window then shows the sheet.
    func takeAdd() -> Bool {
        defer { addRequested = false }
        return addRequested
    }
}
