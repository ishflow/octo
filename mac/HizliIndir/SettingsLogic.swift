import Foundation

// Pure helpers for the Settings and Scheduler windows (unit-tested; no UI, no engine).

enum TimeOfDay {
    /// Minutes since midnight for a strict "HH:mm" string (00:00...23:59).
    static func minutes(_ s: String) -> Int? {
        guard let m = s.wholeMatch(of: /([0-9]{2}):([0-9]{2})/), let h = Int(m.output.1), let mi = Int(m.output.2),
              h < 24, mi < 60 else { return nil }
        return h * 60 + mi
    }

    static func string(minutes m: Int) -> String? {
        guard (0..<1440).contains(m) else { return nil }
        return String(format: "%02d:%02d", m / 60, m % 60)
    }

    /// Today at that time, for a DatePicker.
    static func date(_ s: String, calendar: Calendar = .current, on ref: Date = Date()) -> Date? {
        guard let m = minutes(s) else { return nil }
        return calendar.date(bySettingHour: m / 60, minute: m % 60, second: 0, of: ref)
    }

    static func string(from d: Date, calendar: Calendar = .current) -> String {
        String(format: "%02d:%02d", calendar.component(.hour, from: d), calendar.component(.minute, from: d))
    }
}

enum DaySet {
    /// Day 1...7 = Monday...Sunday.
    static let labels = ["Pzt", "Sal", "Çar", "Per", "Cum", "Cmt", "Paz"]

    /// Adds or removes `day`; sorted, unique, in range. The last remaining day cannot be removed (a schedule needs a day).
    static func toggle(_ days: [Int], _ day: Int) -> [Int] {
        var set = Set(days.filter { (1...7).contains($0) })
        guard (1...7).contains(day) else { return set.sorted() }
        if set.contains(day) { if set.count > 1 { set.remove(day) } } else { set.insert(day) }
        return set.sorted()
    }
}

enum SpeedLimit {
    static let presets: [(label: String, kbps: Int?)] = [("1 MB/sn", 1000), ("5 MB/sn", 5000), ("10 MB/sn", 10000), ("Sınırsız", nil)]

    /// Upper bound for a typed limit: 10 GB/s is already "unlimited" and keeps KB/s → bytes/s far from overflow.
    static let maxKBps = 10_000_000
    static let rangeMessage = "Hız sınırı 1 ile 10 000 000 KB/s arasında olmalı."

    static func problem(_ kbps: Int) -> String? { (1...maxKBps).contains(kbps) ? nil : rangeMessage }

    /// Whole KB/s in 1...maxKBps. Spaces and dots are digit separators ("5 000", "5.000").
    static func parseKBps(_ s: String) -> Int? {
        let t = s.filter { $0 != " " && $0 != "." }
        guard !t.isEmpty, t.allSatisfy(\.isASCII), t.allSatisfy(\.isNumber), let n = Int(t), problem(n) == nil else { return nil }
        return n
    }

    /// For display (1 KB = 1000 bytes). Clamped, so a corrupt value from the engine cannot trap.
    static func bytesPerSecond(kbps: Int) -> Int64 { Int64(min(max(kbps, 0), maxKBps)) * 1000 }
}

enum HostPattern {
    /// `example.com`, `*.example.com`, `localhost`, IPv4. No scheme, path, port or inner wildcard.
    static func isValid(_ s: String) -> Bool {
        let host = s.hasPrefix("*.") ? String(s.dropFirst(2)) : s
        let labels = host.split(separator: ".", omittingEmptySubsequences: false)
        guard !host.isEmpty, host.count <= 253 else { return false }
        return labels.allSatisfy { l in
            !l.isEmpty && l.count <= 63 && !l.hasPrefix("-") && !l.hasSuffix("-")
                && l.allSatisfy { $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == "_") }
        }
    }

    /// `*.example.com` also matches `example.com` itself.
    static func matches(_ pattern: String, host: String) -> Bool {
        let p = pattern.lowercased(), h = host.lowercased()
        guard p.hasPrefix("*.") else { return p == h }
        let base = String(p.dropFirst(2))
        return h == base || h.hasSuffix("." + base)
    }
}

enum TokenList {
    /// Splits on whitespace and commas, drops empties and duplicates, keeps order. Extensions lose a leading dot.
    static func parse(_ s: String, extensions: Bool) -> [String] {
        var seen = Set<String>(), out: [String] = []
        for raw in s.split(whereSeparator: { $0.isWhitespace || $0 == "," }) {
            var t = raw.lowercased()
            if extensions { t = String(t.drop { $0 == "." }) }
            if !t.isEmpty, seen.insert(t).inserted { out.append(t) }
        }
        return out
    }

    static func text(_ l: [String]) -> String { l.joined(separator: " ") }
}

enum ConnChoices {
    static let values = [1, 2, 4, 8, 16, 24, 32]

    /// The picker list, plus the engine's current value when it is not one of the standard steps.
    static func values(including v: Int) -> [Int] { Array(Set(values + [v])).sorted() }
}

enum SettingsCheck {
    /// Turkish message for the first invalid field, nil when the whole object may be sent to `settings.set`.
    static func problem(_ s: Settings) -> String? {
        if !(1...32).contains(s.maxConns) { return "Bağlantı sayısı 1 ile 32 arasında olmalı." }
        if s.downloadDir.trimmingCharacters(in: .whitespaces).isEmpty { return "Kaydetme yeri boş olamaz." }
        if let p = SpeedLimit.problem(s.speedLimitKBps) { return p }
        if let bad = s.logins.first(where: { !HostPattern.isValid($0.host) }) { return "Geçersiz sunucu adı: \(bad.host)" }
        return nil
    }
}

/// Settings > Tarayıcı: the Octo button over web videos. The engine stores the inverse (`hideVideoButton`), so a missing key = shown.
enum VideoButton {
    static func shown(_ s: Settings) -> Bool { s.hideVideoButton != true }
    static func set(_ s: inout Settings, shown: Bool) { s.hideVideoButton = !shown }
}

enum QueueCheck {
    static let atOnceRange = 1...10

    static func cleanName(_ s: String) -> String? {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? nil : t
    }

    static func clampAtOnce(_ n: Int) -> Int { min(max(n, atOnceRange.lowerBound), atOnceRange.upperBound) }

    /// The engine requires a non-empty id on `queue.save`; new queues get `q<number>`.
    static func newID(existing: [String], random: () -> Int = { Int.random(in: 1000...999_999) }) -> String {
        var id = "q\(random())"
        while existing.contains(id) { id = "q\(random() &+ 1)" }
        return id
    }

    static func problem(_ s: Schedule) -> String? {
        if TimeOfDay.minutes(s.start) == nil || TimeOfDay.minutes(s.stop) == nil { return "Saat SS:dd biçiminde olmalı." }
        if s.days.isEmpty { return "En az bir gün seçin." }
        return nil
    }
}

/// Settings tabs in the design canvas order; `title` is the window title while the tab is shown.
enum SettingsTab: String, CaseIterable, Hashable {
    case genel, kategoriler, baglanti, kaydetme, indirmeler, tarayici, giris, gelismis

    var label: String {
        switch self {
        case .genel: "Genel"
        case .kategoriler: "Kategoriler"
        case .baglanti: "Bağlantı"
        case .kaydetme: "Kaydetme"
        case .indirmeler: "İndirmeler"
        case .tarayici: "Tarayıcı"
        case .giris: "Girişler"
        case .gelismis: "Gelişmiş"
        }
    }

    var title: String {
        switch self {
        case .kaydetme: "Kaydetme yeri"
        case .tarayici: "Tarayıcı entegrasyonu"
        case .giris: "Site girişleri"
        default: label
        }
    }

    var icon: String {
        switch self {
        case .genel: "gearshape"
        case .kategoriler: "square.grid.2x2"
        case .baglanti: "bolt"
        case .kaydetme: "folder"
        case .indirmeler: "arrow.down.circle"
        case .tarayici: "globe"
        case .giris: "key"
        case .gelismis: "slider.horizontal.3"
        }
    }
}

enum FolderLabel {
    private static let homeNames = ["Downloads": "İndirilenler", "Desktop": "Masaüstü", "Documents": "Belgeler",
                                    "Movies": "Filmler", "Music": "Müzik", "Pictures": "Resimler"]

    /// Finder-style label: "~/Downloads/Video" -> "İndirilenler › Video". Paths outside home keep their components.
    static func display(_ path: String, home: String = NSHomeDirectory()) -> String {
        var p = path == "~" || path.hasPrefix("~/") ? home + path.dropFirst() : path
        var parts: [String]
        if p == home || p.hasPrefix(home + "/") {
            p = String(p.dropFirst(home.count))
            parts = p.split(separator: "/").map(String.init)
            if let first = parts.first, let tr = homeNames[first] { parts[0] = tr }
            if parts.isEmpty { return "Ev klasörü" }
        } else {
            parts = p.split(separator: "/").map(String.init)
        }
        return parts.joined(separator: " › ")
    }
}

enum CategoryDir {
    /// Where the engine saves files of `c` (same rule as the new-download sheet: `CategoryPick.saveDir`).
    static func resolved(_ c: Category, in s: Settings) -> String {
        CategoryPick.saveDir(c, defaultDir: s.downloadDir, categoryFolders: s.categoryFolders)
    }
}

enum BrowserHost {
    static let chrome = ["Google/Chrome"]
    static let others = ["Arc/User Data", "BraveSoftware/Brave-Browser", "Microsoft Edge"]

    /// nil: none of these browsers is installed; else whether each installed one has Octo's native-host manifest
    /// (same paths as install-native-host.sh).
    static func installed(_ browsers: [String], home: String = NSHomeDirectory()) -> Bool? {
        let fm = FileManager.default, root = home + "/Library/Application Support/"
        let present = browsers.filter { fm.fileExists(atPath: root + $0) }
        guard !present.isEmpty else { return nil }
        return present.allSatisfy { fm.fileExists(atPath: root + $0 + "/NativeMessagingHosts/com.uba.octo.json") }
    }
}

enum QueueOrder {
    /// Moves `id` one step up (-1) or down (+1); unchanged at the edges or when absent.
    static func move(_ items: [String], _ id: String, by step: Int) -> [String] {
        guard let i = items.firstIndex(of: id), items.indices.contains(i + step) else { return items }
        var out = items
        out.swapAt(i, i + step)
        return out
    }
}

enum QueueStatus {
    private static let weekdays = ["Pazartesi", "Salı", "Çarşamba", "Perşembe", "Cuma", "Cumartesi", "Pazar"]

    /// Scheduler header line: "Çalışıyor", "Bekliyor · başlangıç bugün 02:00", "Duruyor".
    static func subtitle(_ q: Queue, now: Date = Date(), calendar: Calendar = .current) -> String {
        let s = q.schedule
        if q.running { return s.stopOn ? "Çalışıyor · bitiş \(s.stop)" : "Çalışıyor" }
        guard s.startOn, let m = TimeOfDay.minutes(s.start) else { return "Duruyor" }
        let today = calendar.startOfDay(for: now)
        for offset in 0...7 {
            guard let day = calendar.date(byAdding: .day, value: offset, to: today),
                  let at = calendar.date(bySettingHour: m / 60, minute: m % 60, second: 0, of: day), at > now else { continue }
            let weekday = (calendar.component(.weekday, from: day) + 5) % 7 + 1  // Calendar: 1 = Sunday; ours: 1 = Monday
            guard s.days.contains(weekday) else { continue }
            let when = offset == 0 ? "bugün" : offset == 1 ? "yarın" : weekdays[weekday - 1]
            return "Bekliyor · başlangıç \(when) \(s.start)"
        }
        return "Bekliyor"
    }
}
