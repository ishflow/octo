import AppKit
import SwiftUI
import Testing

/// Renders the Settings and Scheduler windows with sample data to PNGs for comparing with the design canvas.
/// Runs only when SHOTS_DIR is set (`TEST_RUNNER_SHOTS_DIR=… xcodebuild test`); windows are never shown on screen.
@MainActor @Suite struct DesignShotTests {
    static let dir = ProcessInfo.processInfo.environment["SHOTS_DIR"]

    static func store() -> Store {
        let s = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        s.connected = true
        let cats: [(String, [String], String)] = [
            ("Arşiv", ["zip", "rar", "7z", "gz", "bz2", "tar", "xz"], "Arşiv"), ("Belgeler", ["pdf", "doc", "docx", "ppt", "pptx", "xls", "xlsx"], "Belgeler"),
            ("Müzik", ["mp3", "wav", "aac", "m4a", "flac", "ogg"], "Müzik"), ("Programlar", ["dmg", "pkg", "app", "iso", "xip", "exe", "msi"], "Programlar"),
            ("Video", ["mp4", "mov", "mkv", "avi", "webm", "m4v"], "Video"),
        ]
        var st = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: true, duplicate: "ask", speedLimitOn: false, speedLimitKBps: 5000)
        st.categories = cats.map { Category(name: $0.0, exts: OrEmpty(wrappedValue: $0.1), dir: $0.2) }
        st.captureExts = ["zip", "rar", "7z", "dmg", "pkg", "iso", "mp4", "mkv", "mov", "mp3", "pdf", "exe", "msi"]
        st.excludedSites = ["*.icloud.com", "drive.google.com"]
        st.logins = [Login(host: "ftp.example.com", user: "uba"), Login(host: "files.example.org", user: "tasarim"), Login(host: "*.example.net", user: "indirme")]
        s.settings = st
        let files: [(String, Int64)] = [("ubuntu-26.04-desktop-arm64.iso", 5_800_000_000), ("Xcode_27_beta.xip", 4_100_000_000),
                                        ("tanitim-ham-kayit.mov", 12_400_000_000), ("yedek-2026-10.tar.gz", 2_200_000_000), ("ses-kutuphanesi.zip", 890_000_000)]
        s.items = files.enumerated().map { i, f in
            Item(id: "d\(i)", url: "https://x/\(f.0)", name: f.0, dir: "~/Downloads", category: "", queue: "main", state: i == 1 ? .paused : .queued,
                 size: f.1, got: 0, speed: 0, eta: -1, resumable: true, error: "", description: "", added: "2026-10-19T20:00:00Z",
                 finished: nil, lastTry: nil, maxConns: 8, conns: OrEmpty())
        }
        let sch = Schedule(startOn: true, start: "02:00", stopOn: true, stop: "07:00", days: OrEmpty(wrappedValue: [1, 2, 3, 4, 5]), onDone: "none")
        s.queues = [
            Queue(id: "main", name: "Ana kuyruk", atOnce: 3, retries: 10, items: OrEmpty(wrappedValue: s.items.map(\.id)), running: false, schedule: sch),
            Queue(id: "q1", name: "Senkronizasyon", atOnce: 1, retries: 0, items: OrEmpty(), running: false, schedule: sch),
            Queue(id: "q2", name: "Gece indirmeleri", atOnce: 1, retries: 0, items: OrEmpty(), running: false, schedule: sch),
        ]
        return s
    }

    func snap(_ name: String, _ size: CGSize, _ view: some View) throws {
        guard let dir = Self.dir else { return }
        _ = NSApplication.shared
        for dark in [false, true] {
            let host = NSHostingView(rootView: view.environment(Self.store()).frame(width: size.width, height: size.height)
                .background(.background).environment(\.colorScheme, dark ? .dark : .light))
            host.frame = CGRect(origin: .zero, size: size)
            let win = NSWindow(contentRect: host.frame, styleMask: [.titled, .fullSizeContentView], backing: .buffered, defer: false)
            win.isReleasedWhenClosed = false
            win.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
            win.contentView = host
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.4))
            host.layoutSubtreeIfNeeded()
            let rep = try #require(host.bitmapImageRepForCachingDisplay(in: host.bounds))
            host.cacheDisplay(in: host.bounds, to: rep)
            let png = try #require(rep.representation(using: .png, properties: [:]))
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)\(dark ? "-dark" : "").png"))
            win.close()
        }
    }

    @Test func settingsTabs() throws {
        let size = CGSize(width: 720, height: 560)
        try snap("Settings-window", CGSize(width: 720, height: 640), SettingsView(tab: .genel))
        try snap("Settings", size, GeneralTab())
        try snap("SettingsKategoriler", size, CategoriesTab(selection: "Programlar"))
        try snap("SettingsBaglanti", size, ConnectionTab())
        try snap("SettingsKaydetme", size, SaveToTab())
        try snap("SettingsIndirmeler", size, DownloadsTab())
        try snap("SettingsTarayici", size, BrowserTab())
        try snap("SettingsGiris", size, LoginsTab(selection: "files.example.org"))
        try snap("SettingsGelismis", size, AdvancedTab())
    }

    @Test func scheduler() throws {
        try snap("Scheduler", CGSize(width: 920, height: 700), SchedulerWindow())
        try snap("SchedulerFiles", CGSize(width: 920, height: 700), SchedulerWindow(tab: .files))
    }
}
