import Foundation
import SwiftUI

// Pure helpers for the add/batch sheets (unit-tested; no UI, no engine).

enum CategoryPick {
    /// First category whose extension list contains the URL file's extension (case-insensitive, leading dot ignored).
    static func category(forURL url: String, in cats: [Category]) -> Category? {
        let ext = (URL(string: url)?.pathExtension ?? "").lowercased()
        guard !ext.isEmpty else { return nil }
        return cats.first { $0.exts.contains { $0.lowercased().drop { $0 == "." } == ext[...] } }
    }

    /// The folder manager.go would pick for `c`. Callers send it as `dir`, which the engine treats as explicit,
    /// so with `categoryFolders` off it must be the default folder. Relative = under `defaultDir`; empty = category name (not "Genel").
    static func saveDir(_ c: Category?, defaultDir: String, categoryFolders: Bool) -> String {
        guard categoryFolders, let c else { return defaultDir }
        let d = c.dir.isEmpty && c.name != "Genel" ? c.name : c.dir
        guard !d.isEmpty else { return defaultDir }
        return (d as NSString).expandingTildeInPath.hasPrefix("/") ? d : (defaultDir as NSString).appendingPathComponent(d)
    }
}

enum URLCheck {
    /// Trimmed URL string if it is http(s) (all the engine downloads) with a host and no inner whitespace.
    static func normalized(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty, t.rangeOfCharacter(from: .whitespacesAndNewlines) == nil,
              let u = URL(string: t), let scheme = u.scheme?.lowercased(),
              ["http", "https"].contains(scheme), let host = u.host, !host.isEmpty else { return nil }
        return t
    }

    static func clipboardURL(_ s: String?) -> String? { s.flatMap(normalized) }

    /// Every distinct http(s) link in dropped text, in order. Punctuation wrapped around a link (`<…>`, quotes, trailing `.,;`) is
    /// dropped; shortcut: a link that legitimately ends in `)` or `.` loses it, upgrade to a balanced-paren check if that shows up.
    static func extract(from text: String) -> [String] {
        let wrap = CharacterSet(charactersIn: "<>()[]{}\"'“”‘’«».,;:!?")
        var seen = Set<String>()
        return text.split(whereSeparator: \.isWhitespace)
            .compactMap { normalized($0.trimmingCharacters(in: wrap)) }
            .filter { seen.insert($0).inserted }
    }

    static func fileName(from url: String) -> String {
        let last = URL(string: url)?.path.split(separator: "/").last.map(String.init) ?? ""
        return last.isEmpty ? "indirme" : last
    }
}

enum DuplicateName {
    /// `a.iso` -> `a (2).iso`, `a (3).iso`, ... until `exists` is false.
    static func numbered(_ name: String, exists: (String) -> Bool) -> String {
        let ext = (name as NSString).pathExtension
        let stem = (name as NSString).deletingPathExtension
        var n = 2
        while true {
            let c = ext.isEmpty ? "\(stem) (\(n))" : "\(stem) (\(n)).\(ext)"
            if !exists(c) { return c }
            n += 1
        }
    }
}

enum Overwrite {
    /// Moves the file to be replaced out of the way. Returns a Turkish error message on failure; the caller must then abort.
    static func trashExisting(_ url: URL, using trash: (URL) throws -> Void) -> String? {
        do { try trash(url); return nil } catch {
            return "“\(url.lastPathComponent)” Çöp Kutusu'na taşınamadı, üzerine yazılmadı: \(error.localizedDescription)"
        }
    }
}

/// One connection's range on the bar, as fractions of the file size (0...1): start <= pos <= end.
struct ConnSlice: Equatable {
    var start: Double
    var pos: Double
    var end: Double
}

enum ConnBar {
    /// Geometry of the connection bar. `Conn.end` is inclusive and `pos` is the next byte to write (engine chunk).
    /// Each segment is clamped on its own (overlaps are drawn as-is); empty when the size is unknown.
    static func slices(_ conns: [Conn], size: Int64) -> [ConnSlice] {
        guard size > 0 else { return [] }
        let total = Double(size)
        func f(_ v: Int64) -> Double { min(max(Double(v) / total, 0), 1) }
        return conns.map { c in
            let start = f(c.start), end = max(start, f(c.end + 1))
            return ConnSlice(start: start, pos: min(max(f(c.pos), start), end), end: end)
        }
    }
}

extension Conn {
    /// Bytes this connection has written; pos past an end pulled back by a split is not counted twice.
    var downloaded: Int64 {
        let stop = end >= start ? min(pos, end + 1) : pos  // end < start: open-ended (size unknown)
        return max(0, stop - start)
    }
}

private final class BundleToken {}

extension Color {
    /// Brand accent (AccentColor asset: #5B3FD6 light, #7461E6 dark). macOS gives the asset accent only to users on the
    /// "multicolor" system accent, so scene roots also set it as the tint. Bundle lookup also finds it in the test bundle.
    static let brand = Color("AccentColor", bundle: Bundle(for: BundleToken.self))
}

// MARK: - Turkish display formats (shared by every window)

enum Format {
    static let tr = Locale(identifier: "tr_TR")

    /// Decimal units, three significant digits: "8,54 GB", "34,5 MB", "512 KB", "999 bayt"; "—" when the size is unknown.
    static func bytes(_ n: Int64) -> String {
        guard n >= 0 else { return "—" }
        guard n >= 1000 else { return "\(n) bayt" }
        let units = ["KB", "MB", "GB", "TB", "PB"]
        var v = Double(n) / 1000, i = 0
        while v >= 999.5 && i < units.count - 1 { v /= 1000; i += 1 }
        let digits = v < 9.995 ? 2 : v < 99.95 ? 1 : 0
        return v.formatted(.number.locale(tr).precision(.fractionLength(digits))) + " " + units[i]
    }
    /// "34,5 MB/s"; `none` when idle (empty in tables, "—" where a value is always shown).
    static func speed(_ bps: Int64, none: String = "") -> String { bps <= 0 ? none : bytes(bps) + "/s" }
    /// "%61", "%61,3" (Turkish puts the sign first).
    static func percent(_ f: Double, digits: Int = 0) -> String {
        "%" + (f * 100).formatted(.number.locale(tr).precision(.fractionLength(digits)))
    }
    /// "52 sn", "1 dk 52 sn", "2 sa 05 dk"; `none` when unknown.
    static func eta(_ s: Int, none: String = "") -> String {
        guard s >= 0 else { return none }
        if s < 60 { return "\(s) sn" }
        if s < 3600 { return "\(s / 60) dk " + String(format: "%02d sn", s % 60) }
        return "\(s / 3600) sa " + String(format: "%02d dk", s % 3600 / 60)
    }
    /// "8.543.594.496".
    static func grouped(_ n: Int64) -> String { n.formatted(.number.locale(tr)) }
    /// Folder under home as a breadcrumb: "İndirilenler › Programlar". Elsewhere the plain path.
    static func folder(_ dir: String, home: String = NSHomeDirectory()) -> String {
        guard dir.hasPrefix(home + "/") else { return dir }
        let names = ["Downloads": "İndirilenler", "Desktop": "Masaüstü", "Documents": "Belgeler"]
        var parts = dir.dropFirst(home.count + 1).split(separator: "/").map(String.init)
        if let f = parts.first, let n = names[f] { parts[0] = n }
        return parts.joined(separator: " › ")
    }
    /// "Bugün 20:45", "Dün 22:04", "17 Eki" (this year), "17 Eki 2025".
    static func date(_ iso: String?, now: Date = Date(), calendar: Calendar = .current) -> String {
        // Format styles, not ISO8601DateFormatter/DateFormatter: those cost ~450 µs a call, once per visible table row.
        guard let iso, let d = (try? Date.ISO8601FormatStyle(includingFractionalSeconds: true).parse(iso))
            ?? (try? Date.ISO8601FormatStyle().parse(iso)) else { return iso ?? "" }
        func fmt(_ f: Date.FormatString) -> String {
            d.formatted(Date.VerbatimFormatStyle(format: f, locale: tr, timeZone: calendar.timeZone, calendar: calendar))
        }
        let time: Date.FormatString = "\(hour: .twoDigits(clock: .twentyFourHour, hourCycle: .zeroBased)):\(minute: .twoDigits)"
        if calendar.isDate(d, inSameDayAs: now) { return "Bugün " + fmt(time) }
        if let y = calendar.date(byAdding: .day, value: -1, to: now), calendar.isDate(d, inSameDayAs: y) { return "Dün " + fmt(time) }
        return calendar.isDate(d, equalTo: now, toGranularity: .year)
            ? fmt("\(day: .defaultDigits) \(month: .abbreviated)") : fmt("\(day: .defaultDigits) \(month: .abbreviated) \(year: .padded(4))")
    }
}

/// Status column text and tone; nil while a download of known size runs (the row shows a bar instead).
enum StatusLabel {
    enum Tone { case secondary, done, paused, error }

    static func of(_ i: Item) -> (text: String, tone: Tone)? {
        switch i.state {
        case .done: ("Tamamlandı", .done)
        case .queued: ("Kuyrukta", .secondary)
        case .error: (i.error.isEmpty ? "Hata" : "Hata: \(i.error)", .error)
        case .paused: (i.fraction.map { "Durduruldu · \(Format.percent($0))" } ?? "Durduruldu", .paused)
        case .downloading: StreamProgress.merging(i) ? ("Birleştiriliyor…", .secondary) : i.fraction == nil ? ("Veri alınıyor…", .secondary) : nil
        }
    }
}

extension StatusLabel.Tone {
    var color: Color {
        switch self {
        case .secondary: .secondary
        case .done: .green
        case .paused: .orange
        case .error: .red
        }
    }
}

/// SF Symbol for a file by extension (table rows, sidebar categories).
enum FileIcon {
    static func symbol(forName name: String) -> String { symbol(forExt: (name as NSString).pathExtension) }

    static func symbol(forExt ext: String) -> String {
        switch ext.lowercased() {
        case "iso", "img", "cdr", "toast": "opticaldisc"
        case "dmg", "pkg", "xip", "app", "exe", "msi": "square.grid.2x2"
        case "zip", "rar", "7z", "gz", "tgz", "bz2", "xz", "tar", "arj", "sit", "sitx", "ace": "archivebox"
        case "pdf", "doc", "docx", "txt", "rtf", "md", "pages", "ppt", "pptx", "pps", "xls", "xlsx", "csv": "doc.text"
        case "mp3", "wav", "aac", "m4a", "flac", "aif", "aiff", "wma", "mpa", "ra", "ram": "music.note"
        case "mp4", "mov", "mkv", "avi", "webm", "m4v", "wmv", "flv", "mpg", "mpeg", "mpe", "qt", "ogv", "ogg", "asf", "ts": "film"
        case "jpg", "jpeg", "png", "gif", "heic", "webp", "tiff", "svg": "photo"
        default: "doc"
        }
    }
}

/// Window subtitle and bottom status pill texts.
enum Summary {
    /// "2 indiriliyor · 52,7 MB/s", "10 indirme", "İndirme yok".
    static func subtitle(_ items: [Item]) -> String {
        let active = items.filter { $0.state == .downloading }
        if items.isEmpty { return "İndirme yok" }
        if active.isEmpty { return "\(items.count) indirme" }
        let speed = Format.speed(active.reduce(0) { $0 + $1.speed })
        return speed.isEmpty ? "\(active.count) indiriliyor" : "\(active.count) indiriliyor · \(speed)"
    }

    /// "10 öğe · 2 indiriliyor · 1 kuyrukta" (zero parts left out).
    static func counts(_ items: [Item]) -> String {
        let down = items.filter { $0.state == .downloading }.count, queued = items.filter { $0.state == .queued }.count
        var parts = ["\(items.count) öğe"]
        if down > 0 { parts.append("\(down) indiriliyor") }
        if queued > 0 { parts.append("\(queued) kuyrukta") }
        return parts.joined(separator: " · ")
    }

    /// "Hız sınırı kapalı · dosya başına 8 bağlantı"; the limit is in KB/s (1000 bytes, as the speed popover shows it).
    static func limits(_ s: Settings?) -> String {
        guard let s else { return "" }
        let limit = s.speedLimitOn ? "Hız sınırı \(Format.speed(SpeedLimit.bytesPerSecond(kbps: s.speedLimitKBps)))" : "Hız sınırı kapalı"
        return "\(limit) · dosya başına \(s.maxConns) bağlantı"
    }
}

enum SidebarCount {
    /// Badge count of every sidebar entry in one pass (entries with no items are missing).
    static func of(_ items: [Item]) -> [SidebarItem: Int] {
        var c: [SidebarItem: Int] = [.all: items.count]
        for i in items {
            c[i.state == .done ? .finished : .unfinished, default: 0] += 1
            c[.category(i.category), default: 0] += 1
            c[.queue(i.queue), default: 0] += 1
        }
        return c
    }
}
