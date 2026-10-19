import AppKit
import Foundation
import SwiftUI
import Testing

@Suite struct FormatTests {
    @Test func bytesTurkishThreeDigits() {
        #expect(Format.bytes(8_543_594_496) == "8,54 GB")
        #expect(Format.bytes(4_880_000_000) == "4,88 GB")
        #expect(Format.bytes(8_000_000_000) == "8,00 GB")   // one style, never "8 GB" next to "8,0 GB"
        #expect(Format.bytes(5_800_000_000) == "5,80 GB")
        #expect(Format.bytes(412_000_000) == "412 MB")
        #expect(Format.bytes(86_400_000) == "86,4 MB")
        #expect(Format.bytes(34_500_000) == "34,5 MB")
        #expect(Format.bytes(512_000) == "512 KB")
        #expect(Format.bytes(999_600) == "1,00 MB")
        #expect(Format.bytes(999) == "999 bayt")
        #expect(Format.bytes(-1) == "—")
    }
    @Test func speedTurkish() {
        #expect(Format.speed(34_500_000) == "34,5 MB/s")
        #expect(Format.speed(0) == "")
        #expect(Format.speed(0, none: "—") == "—")
    }
    @Test func percentSignFirst() {
        #expect(Format.percent(0.61) == "%61")
        #expect(Format.percent(0.444) == "%44")
        #expect(Format.percent(0.61, digits: 1) == "%61,0")
        #expect(Format.percent(0.613, digits: 0) == "%61")
    }
    @Test func eta() {
        #expect(Format.eta(-1) == "")
        #expect(Format.eta(-1, none: "—") == "—")
        #expect(Format.eta(45) == "45 sn")
        #expect(Format.eta(52) == "52 sn")
        #expect(Format.eta(112) == "1 dk 52 sn")
        #expect(Format.eta(185) == "3 dk 05 sn")
        #expect(Format.eta(3_900) == "1 sa 05 dk")
        #expect(Format.eta(7500) == "2 sa 05 dk")
    }
    @Test func groupedAndFolder() {
        #expect(Format.grouped(8_543_594_496) == "8.543.594.496")
        #expect(Format.folder("/Users/u/Downloads/Programlar", home: "/Users/u") == "İndirilenler › Programlar")
        #expect(Format.folder("/Users/u/Belgelerim", home: "/Users/u") == "Belgelerim")
        #expect(Format.folder("/Volumes/Disk/x", home: "/Users/u") == "/Volumes/Disk/x")
        #expect(Format.folder("/Users/uba/x", home: "/Users/u") == "/Users/uba/x")
    }
    @Test func relativeDates() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = try #require(TimeZone(identifier: "Europe/Istanbul"))
        let now = try #require(cal.date(from: DateComponents(year: 2026, month: 10, day: 19, hour: 21, minute: 14)))
        func iso(_ d: Int, _ h: Int, _ m: Int, year: Int = 2026, month: Int = 10) throws -> String {
            ISO8601DateFormatter().string(from: try #require(cal.date(from: DateComponents(year: year, month: month, day: d, hour: h, minute: m))))
        }
        #expect(Format.date(try iso(19, 20, 45), now: now, calendar: cal) == "Bugün 20:45")
        #expect(Format.date(try iso(18, 22, 4), now: now, calendar: cal) == "Dün 22:04")
        #expect(Format.date(try iso(17, 9, 0), now: now, calendar: cal) == "17 Eki")
        #expect(Format.date(try iso(17, 9, 0, year: 2025), now: now, calendar: cal) == "17 Eki 2025")
        #expect(Format.date("2026-10-19T17:45:00.123Z", now: now, calendar: cal) == "Bugün 20:45")  // fractional seconds
        #expect(Format.date(nil) == "")
        #expect(Format.date("dün") == "dün")
    }
}

@Suite struct StatusLabelTests {
    private func item(_ state: ItemState, size: Int64 = 100, got: Int64 = 44, error: String = "") -> Item {
        Item(id: "1", url: "https://x/a.zip", name: "a.zip", dir: "/tmp", category: "", queue: "main", state: state,
             size: size, got: got, speed: 0, eta: -1, resumable: true, error: error, description: "",
             added: "2026-10-19T20:00:00Z", finished: nil, lastTry: nil, maxConns: 8, conns: OrEmpty())
    }

    @Test func textsAndTones() {
        #expect(StatusLabel.of(item(.done))! == ("Tamamlandı", .done))
        #expect(StatusLabel.of(item(.queued))! == ("Kuyrukta", .secondary))
        #expect(StatusLabel.of(item(.paused))! == ("Durduruldu · %44", .paused))
        #expect(StatusLabel.of(item(.paused, size: -1))! == ("Durduruldu", .paused))
        #expect(StatusLabel.of(item(.error, error: "zaman aşımı"))! == ("Hata: zaman aşımı", .error))
        #expect(StatusLabel.of(item(.error))! == ("Hata", .error))
        #expect(StatusLabel.of(item(.downloading)) == nil)  // bar
        #expect(StatusLabel.of(item(.downloading, size: -1))! == ("Veri alınıyor…", .secondary))
    }

    @Test func summaries() {
        var a = item(.downloading), b = item(.downloading), c = item(.queued)
        a.speed = 34_500_000
        b.speed = 18_200_000
        c.speed = 0
        #expect(Summary.subtitle([a, b, c, item(.done)]) == "2 indiriliyor · 52,7 MB/s")
        #expect(Summary.subtitle([item(.done)]) == "1 indirme")
        #expect(Summary.subtitle([]) == "İndirme yok")
        #expect(Summary.counts([a, b, c, item(.done)]) == "4 öğe · 2 indiriliyor · 1 kuyrukta")
        #expect(Summary.counts([]) == "0 öğe")
    }

    @Test func limits() throws {
        let json = #"{"maxConns":8,"downloadDir":"~","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":5000}"#
        var s = try JSONDecoder().decode(Settings.self, from: Data(json.utf8))
        #expect(Summary.limits(s) == "Hız sınırı kapalı · dosya başına 8 bağlantı")
        s.speedLimitOn = true
        #expect(Summary.limits(s) == "Hız sınırı 5,00 MB/s · dosya başına 8 bağlantı")
        #expect(Summary.limits(nil) == "")
        s.speedLimitKBps = Int.max  // a corrupt value from the engine must not trap
        #expect(Summary.limits(s).hasPrefix("Hız sınırı "))
    }

    @Test func fileIcons() {
        #expect(FileIcon.symbol(forName: "Windows11.ISO") == "opticaldisc")
        #expect(FileIcon.symbol(forName: "Xcode_27_beta.xip") == "square.grid.2x2")
        #expect(FileIcon.symbol(forName: "kit.zip") == "archivebox")
        #expect(FileIcon.symbol(forName: "rapor.pdf") == "doc.text")
        #expect(FileIcon.symbol(forName: "bolum.mp3") == "music.note")
        #expect(FileIcon.symbol(forName: "tanitim.mp4") == "film")
        #expect(FileIcon.symbol(forName: "README") == "doc")
        for s in ["opticaldisc", "square.grid.2x2", "archivebox", "doc.text", "music.note", "film", "photo", "doc", "xmark.bin", "calendar"] {
            #expect(NSImage(systemSymbolName: s, accessibilityDescription: nil) != nil, "missing SF Symbol \(s)")
        }
    }
}

@Suite struct SpeedDecodingTests {
    /// The engine encodes speed as float64 (bytes/s); one fractional value used to drop every progress event.
    @Test func progressAcceptsFractionalSpeed() throws {
        let json = #"{"event":"progress","items":[{"id":"a","state":"downloading","got":1,"size":2,"speed":19020899.29184468,"eta":3,"conns":null}]}"#
        guard case .progress(let ups) = try JSONDecoder().decode(EngineEvent.self, from: Data(json.utf8)) else {
            Issue.record("not a progress event"); return
        }
        #expect(ups.first?.speed == 19_020_899)
    }
    @Test func itemAcceptsIntegerAndFractionalSpeed() throws {
        for (raw, want): (String, Int64) in [("50", 50), ("50.6", 51), ("0", 0)] {
            let json = #"{"id":"d","url":"u","name":"n","dir":"/","category":"","queue":"main","state":"done","size":1,"got":1,"speed":\#(raw),"eta":-1,"resumable":true,"error":"","description":"","added":"","maxConns":8}"#
            #expect(try JSONDecoder().decode(Item.self, from: Data(json.utf8)).speed == want)
        }
    }
    @Test func rejectsOutOfRange() {
        let json = #"{"id":"a","state":"downloading","got":1,"size":2,"speed":1e30,"eta":3}"#
        #expect(throws: DecodingError.self) { try JSONDecoder().decode(ProgressUpdate.self, from: Data(json.utf8)) }
    }
}

@MainActor @Suite struct BrandColorTests {
    private func contrastWithWhite(_ c: NSColor) -> Double {
        func lin(_ x: CGFloat) -> Double { let x = Double(x); return x <= 0.03928 ? x / 12.92 : pow((x + 0.055) / 1.055, 2.4) }
        let s = c.usingColorSpace(.sRGB)!
        let l = 0.2126 * lin(s.redComponent) + 0.7152 * lin(s.greenComponent) + 0.0722 * lin(s.blueComponent)
        return 1.05 / (l + 0.05)
    }

    /// White text on the accent (selection, prominent buttons) must stay readable: WCAG AA 4.5:1 in both modes.
    @Test func accentContrastBothModes() throws {
        let accent = NSColor(Color.brand)
        for name in [NSAppearance.Name.aqua, .darkAqua] {
            var ratio = 0.0
            try #require(NSAppearance(named: name)).performAsCurrentDrawingAppearance { ratio = contrastWithWhite(accent) }
            #expect(ratio >= 4.5, "\(name.rawValue): \(ratio)")
        }
    }
}

/// Renders the main window with sample data to PNG (light/dark, rows/empty). Set TEST_RUNNER_SHOTS_DIR to keep the files.
@MainActor @Suite(.serialized) struct MainWindowShots {
    private static func sampleStore(empty: Bool) throws -> Store {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        let s = #"{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":5000,"categories":[{"name":"Genel","exts":[],"dir":""},{"name":"Arşiv","exts":["zip"],"dir":""},{"name":"Belgeler","exts":["pdf"],"dir":""},{"name":"Müzik","exts":["mp3"],"dir":""},{"name":"Programlar","exts":["dmg"],"dir":""},{"name":"Video","exts":["mp4"],"dir":""}]}"#
        store.settings = try JSONDecoder().decode(Settings.self, from: Data(s.utf8))
        store.connected = true
        let q = #"{"id":"main","name":"Ana kuyruk","atOnce":3,"retries":0,"items":[],"running":false,"schedule":{"startOn":false,"start":"","stopOn":false,"stop":"","days":[],"onDone":"none"}}"#
        store.queues = [try JSONDecoder().decode(Queue.self, from: Data(q.utf8))]
        guard !empty else { return store }
        let cal = Calendar.current, today = cal.startOfDay(for: Date())
        func at(_ dayOffset: Int, _ h: Int, _ m: Int) -> String {
            ISO8601DateFormatter().string(from: cal.date(byAdding: DateComponents(day: dayOffset, hour: h, minute: m), to: today)!)
        }
        func item(_ n: Int, _ name: String, _ cat: String, _ state: ItemState, _ size: Int64, _ pct: Double, _ speed: Int64,
                  _ eta: Int, _ when: String, _ desc: String = "", _ err: String = "") -> Item {
            Item(id: "\(n)", url: "https://example.com/\(name)", name: name, dir: "/tmp", category: cat, queue: "main",
                 state: state, size: size, got: Int64(Double(size) * pct), speed: speed, eta: eta, resumable: true, error: err,
                 description: desc, added: "\(n)", finished: nil, lastTry: when, maxConns: 8, conns: OrEmpty())
        }
        store.items = [
            item(0, "Windows11_Client_arm64_en-gb.iso", "Programlar", .downloading, 8_000_000_000, 0.61, 34_500_000, 112, at(0, 20, 45)),
            item(1, "Xcode_27_beta.xip", "Programlar", .downloading, 4_100_000_000, 0.23, 18_200_000, 185, at(0, 20, 47)),
            item(2, "ubuntu-26.04-desktop-arm64.iso", "Programlar", .queued, 5_800_000_000, 0, 0, -1, at(0, 20, 50), "Ana kuyruk"),
            item(3, "podcast-bolum-42.mp3", "Müzik", .paused, 86_400_000, 0.44, 0, -1, at(0, 19, 12)),
            item(4, "davinci-resolve-21.dmg", "Programlar", .error, 3_200_000_000, 0.1, 0, -1, at(0, 18, 30), "", "zaman aşımı"),
            item(5, "figma-tasarim-kiti.zip", "Arşiv", .done, 412_000_000, 1, 0, -1, at(-1, 22, 4)),
            item(6, "rapor-2026-Q3.pdf", "Belgeler", .done, 2_400_000, 1, 0, -1, at(-1, 16, 40), "Muhasebe"),
            item(7, "tanitim-4K.mp4", "Video", .done, 1_900_000_000, 1, 0, -1, at(-2, 12, 0)),
            item(8, "ekran-kayitlari.zip", "Arşiv", .done, 640_000_000, 1, 0, -1, at(-3, 12, 0)),
            item(9, "font-paketi.zip", "Arşiv", .done, 18_600_000, 1, 0, -1, at(-4, 12, 0)),
        ]
        return store
    }

    private func render(empty: Bool, dark: Bool) throws -> NSBitmapImageRep {
        _ = NSApplication.shared
        UserDefaults.standard.set(true, forKey: "onboardingDone")
        let store = try Self.sampleStore(empty: empty)
        let root = MainWindow().environment(store).environment(MainRoute()).tint(.brand)
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1200, height: 728),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView],
                              backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
        window.contentViewController = NSHostingController(rootView: root)
        window.setContentSize(NSSize(width: 1200, height: 728))
        // Never ordered in: nothing shows up on the user's screen.
        RunLoop.main.run(until: Date().addingTimeInterval(1.5))
        let view = try #require(window.contentView?.superview)  // frame view: includes title bar and toolbar
        view.layoutSubtreeIfNeeded()
        window.display()
        RunLoop.main.run(until: Date().addingTimeInterval(0.5))
        let rep = try #require(view.bitmapImageRepForCachingDisplay(in: view.bounds))
        view.cacheDisplay(in: view.bounds, to: rep)
        window.close()
        return rep
    }

    @Test(arguments: [(false, false), (false, true), (true, false), (true, true)])
    func shot(empty: Bool, dark: Bool) throws {
        let rep = try render(empty: empty, dark: dark)
        #expect(rep.pixelsWide >= 1200)
        let png = try #require(rep.representation(using: .png, properties: [:]))
        if let dir = ProcessInfo.processInfo.environment["SHOTS_DIR"] {
            try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            let name = "Main\(empty ? "Empty" : "")\(dark ? "Dark" : "Light").png"
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name))
        }
    }
}
