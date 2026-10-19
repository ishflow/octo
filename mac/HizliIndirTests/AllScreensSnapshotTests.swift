import AppKit
import SwiftUI
import Testing

/// Every main screen in one run, via `Snapshot` (light + dark, @2x):
/// `TEST_RUNNER_SHOTS_DIR=… xcodebuild test -only-testing:OctoTests/AllScreensSnapshotTests` (see scripts/snapshots.sh).
@MainActor @Suite(.serialized) struct AllScreensSnapshotTests {
    static let isoURL = "https://software.download.prss.microsoft.com/dbazure/Windows11_Client_arm64_en-gb_26300_9457.iso"

    static func store(empty: Bool = false) -> Store {
        let s = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        s.connected = true
        let cats: [(String, [String])] = [("Arşiv", ["zip", "rar", "7z", "gz", "tar"]), ("Belgeler", ["pdf", "doc", "docx", "xlsx"]),
                                          ("Müzik", ["mp3", "wav", "flac"]), ("Programlar", ["dmg", "pkg", "iso", "xip"]), ("Video", ["mp4", "mov", "mkv"])]
        var st = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: true, duplicate: "ask", speedLimitOn: true, speedLimitKBps: 5000)
        st.categories = cats.map { Category(name: $0.0, exts: OrEmpty(wrappedValue: $0.1), dir: $0.0) }
        st.captureExts = ["zip", "rar", "7z", "dmg", "pkg", "iso", "mp4", "mkv", "mov", "mp3", "pdf"]
        st.excludedSites = ["*.icloud.com", "drive.google.com"]
        st.logins = [Login(host: "ftp.example.com", user: "uba"), Login(host: "files.example.org", user: "tasarim")]
        s.settings = st
        let sch = Schedule(startOn: true, start: "02:00", stopOn: true, stop: "07:00", days: OrEmpty(wrappedValue: [1, 2, 3, 4, 5]), onDone: "none")
        let cal = Calendar.current, today = cal.startOfDay(for: .now), iso = ISO8601DateFormatter()
        func at(_ day: Int, _ h: Int, _ m: Int) -> String { iso.string(from: cal.date(byAdding: DateComponents(day: day, hour: h, minute: m), to: today)!) }
        let size: Int64 = 8_543_594_496, part = size / 8
        let done = [1.0, 0.82, 0.66, 0.60, 0.55, 0.52, 0.48, 0.45]
        let conns = (0..<8).map { i -> Conn in
            let start = Int64(i) * part, end = i == 7 ? size - 1 : start + part - 1
            return Conn(start: start, pos: i == 0 ? end + 1 : start + Int64(Double(part) * done[i]), end: end)
        }
        func item(_ id: String, _ name: String, _ cat: String, _ state: ItemState, _ size: Int64, _ pct: Double, _ speed: Int64, _ eta: Int,
                  _ when: String, desc: String = "", err: String = "", conns: [Conn] = []) -> Item {
            Item(id: id, url: "https://example.com/\(name)", name: name, dir: NSHomeDirectory() + "/Downloads/\(cat)", category: cat, queue: "main",
                 state: state, size: size, got: Int64(Double(size) * pct), speed: speed, eta: eta, resumable: true, error: err, description: desc,
                 added: when, finished: nil, lastTry: when, maxConns: 8, conns: OrEmpty(wrappedValue: conns))
        }
        s.items = empty ? [] : [
            item("iso", "Windows11_Client_arm64_en-gb.iso", "Programlar", .downloading, size, 0.61, 34_500_000, 112, at(0, 20, 45), desc: "Mac'teki sanal makine için", conns: conns),
            item("xip", "Xcode_27_beta.xip", "Programlar", .downloading, 4_100_000_000, 0.23, 18_200_000, 185, at(0, 20, 47)),
            item("ubu", "ubuntu-26.04-desktop-arm64.iso", "Programlar", .queued, 5_800_000_000, 0, 0, -1, at(0, 20, 50)),
            item("pod", "podcast-bolum-42.mp3", "Müzik", .paused, 86_400_000, 0.44, 0, -1, at(0, 19, 12)),
            item("dav", "davinci-resolve-21.dmg", "Programlar", .error, 3_200_000_000, 0.1, 0, -1, at(0, 18, 30), err: "zaman aşımı"),
            item("fin", "figma-tasarim-kiti.zip", "Arşiv", .done, 412_000_000, 1, 0, -1, at(-1, 22, 4)),
            item("pdf", "rapor-2026-Q3.pdf", "Belgeler", .done, 2_400_000, 1, 0, -1, at(-1, 16, 40), desc: "Muhasebe"),
            item("mp4", "tanitim-4K.mp4", "Video", .done, 1_900_000_000, 1, 0, -1, at(-2, 12, 0)),
            item("zip", "ekran-kayitlari.zip", "Arşiv", .done, 640_000_000, 1, 0, -1, at(-3, 12, 0)),
        ]
        s.queues = [
            Queue(id: "main", name: "Ana kuyruk", atOnce: 3, retries: 3, items: OrEmpty(wrappedValue: s.items.filter { $0.state == .queued }.map(\.id)), running: false, schedule: sch),
            Queue(id: "sync", name: "Senkronizasyon", atOnce: 1, retries: 0, items: OrEmpty(), running: false, schedule: sch),
        ]
        return s
    }

    @Test func mainWindow() throws {
        UserDefaults.standard.set(true, forKey: "onboardingDone")
        let size = CGSize(width: 1200, height: 728)
        try Snapshot.shootWindow("Main", size: size, store: Self.store(), MainWindow())
        try Snapshot.shootWindow("MainEmpty", size: size, store: Self.store(empty: true), MainWindow())
    }

    @Test func progressAndInfo() throws {
        let s = Self.store()
        try Snapshot.shoot("Progress", store: s, ProgressWindow(id: "iso"))
        try Snapshot.shoot("ProgressSpeed", store: s, ProgressWindow(id: "iso", tab: .limit))
        try Snapshot.shoot("ProgressFinish", store: s, ProgressWindow(id: "iso", tab: .finish))
        try Snapshot.shoot("Complete", store: s, ProgressWindow(id: "fin"))
        try Snapshot.shoot("Info", size: CGSize(width: 440, height: 720), store: s, InfoPanel(item: s.items[0]))
    }

    @Test func speedAndMenu() throws {
        let s = Self.store()
        try Snapshot.shoot("SpeedPopover", store: s, SpeedPopover())
        try Snapshot.shoot("MenuExtra", store: s, MenuExtraView())
        try Snapshot.shoot("MenuBar", store: s, MenuExtraLabel(store: s).padding(8).frame(minWidth: 120, minHeight: 120))
    }

    @Test(arguments: SettingsTab.allCases) func settings(tab: SettingsTab) throws {
        try Snapshot.shoot("Settings-\(tab.rawValue)", size: CGSize(width: 720, height: 640), store: Self.store(), SettingsView(tab: tab))
    }

    @Test func scheduler() throws {
        try Snapshot.shoot("Scheduler", size: CGSize(width: 920, height: 700), store: Self.store(), SchedulerWindow())
        try Snapshot.shoot("SchedulerFiles", size: CGSize(width: 920, height: 700), store: Self.store(), SchedulerWindow(tab: .files))
    }

    @Test func sheets() throws {
        let s = Self.store()
        try Snapshot.shoot("SheetAddURL", store: s, AddDownloadSheet(url: Self.isoURL + "?t=b00de353", fromClipboard: true, onBatch: {}))
        try Snapshot.shoot("SheetNewDownload", store: s, NewDownloadForm(url: Self.isoURL, settings: s.settings, queues: s.queues,
                                                                         probe: ProbeReply(name: "Windows11_Client_arm64_en-gb.iso", size: 8_000_000_000, resumable: true)))
        try Snapshot.shoot("SheetBatch", store: s, BatchSheet(pattern: "https://example.com/galeri/resim*.jpg", from: "1", to: "120", digits: "3"))
        try Snapshot.shoot("SheetRefresh", store: s, RefreshSheet(item: s.items[4]))
        try Snapshot.shoot("Onboarding", store: s, OnboardingView {})
        s.pendingAction = PendingAction(kind: "sleep", deadline: .now.addingTimeInterval(60))
        try Snapshot.shoot("Countdown", store: s, CountdownView())
        try Snapshot.shoot("AlertDuplicate", alert: DuplicatePrompt.alert(name: "Windows11_Client_arm64_en-gb.iso", url: Self.isoURL, inList: true,
                                                                          numbered: "Windows11_Client_arm64_en-gb (2).iso"))
        try Snapshot.shoot("AlertDelete", alert: DeletePrompt.alert(count: 2, unfinished: 1))
    }
}
