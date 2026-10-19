import Foundation
import Testing

@Suite struct SettingsTabTests {
    @Test func canvasOrderAndNames() {
        #expect(SettingsTab.allCases.map(\.label) == ["Genel", "Kategoriler", "Bağlantı", "Kaydetme", "İndirmeler", "Tarayıcı", "Girişler", "Gelişmiş"])
        #expect(SettingsTab.kaydetme.title == "Kaydetme yeri")
        #expect(SettingsTab.tarayici.title == "Tarayıcı entegrasyonu")
        #expect(SettingsTab.giris.title == "Site girişleri")
        #expect(SettingsTab.genel.title == "Genel")
    }
}

@Suite struct FolderLabelTests {
    @Test func homeFoldersInTurkish() {
        #expect(FolderLabel.display("~/Downloads", home: "/Users/u") == "İndirilenler")
        #expect(FolderLabel.display("/Users/u/Downloads/Video", home: "/Users/u") == "İndirilenler › Video")
        #expect(FolderLabel.display("/Users/u/Belge", home: "/Users/u") == "Belge")
        #expect(FolderLabel.display("/Users/u", home: "/Users/u") == "Ev klasörü")
        #expect(FolderLabel.display("/Volumes/Disk/x", home: "/Users/u") == "Volumes › Disk › x")
        #expect(FolderLabel.display("/Users/uba2/Downloads", home: "/Users/u") == "Users › uba2 › Downloads")
    }
}

@Suite struct CategoryDirTests {
    let s = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: true, duplicate: "number",
                     speedLimitOn: false, speedLimitKBps: 500)

    @Test func followsEngineRules() {
        let rel = Category(name: "Video", exts: OrEmpty(wrappedValue: ["mp4"]), dir: "Video")
        let abs = Category(name: "Arşiv", exts: OrEmpty(), dir: "/Volumes/D/Arşiv")
        let none = Category(name: "X", exts: OrEmpty(), dir: "")
        #expect(CategoryDir.resolved(rel, in: s) == "~/Downloads/Video")
        #expect(CategoryDir.resolved(abs, in: s) == "/Volumes/D/Arşiv")
        #expect(CategoryDir.resolved(none, in: s) == "~/Downloads/X")
        #expect(CategoryDir.resolved(Category(name: "Genel", exts: OrEmpty(), dir: ""), in: s) == "~/Downloads")
        var off = s
        off.categoryFolders = false
        #expect(CategoryDir.resolved(rel, in: off) == "~/Downloads")
    }
}

@Suite struct BrowserHostTests {
    @Test func detectsManifest() throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).path
        defer { try? FileManager.default.removeItem(atPath: home) }
        let root = home + "/Library/Application Support/"
        #expect(BrowserHost.installed(BrowserHost.chrome, home: home) == nil)
        try FileManager.default.createDirectory(atPath: root + "Google/Chrome/NativeMessagingHosts", withIntermediateDirectories: true)
        #expect(BrowserHost.installed(BrowserHost.chrome, home: home) == false)
        FileManager.default.createFile(atPath: root + "Google/Chrome/NativeMessagingHosts/com.uba.octo.json", contents: Data("{}".utf8))
        #expect(BrowserHost.installed(BrowserHost.chrome, home: home) == true)
        try FileManager.default.createDirectory(atPath: root + "Microsoft Edge", withIntermediateDirectories: true)
        #expect(BrowserHost.installed(BrowserHost.others, home: home) == false)
    }
}

@Suite struct QueueOrderTests {
    @Test func movesWithinBounds() {
        #expect(QueueOrder.move(["a", "b", "c"], "b", by: -1) == ["b", "a", "c"])
        #expect(QueueOrder.move(["a", "b", "c"], "b", by: 1) == ["a", "c", "b"])
        #expect(QueueOrder.move(["a", "b"], "a", by: -1) == ["a", "b"])
        #expect(QueueOrder.move(["a", "b"], "b", by: 1) == ["a", "b"])
        #expect(QueueOrder.move(["a"], "z", by: 1) == ["a"])
    }
}

@Suite struct QueueStatusTests {
    var cal: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }
    // Monday 2026-10-19 20:00 UTC
    let now = ISO8601DateFormatter().date(from: "2026-10-19T20:00:00Z")!

    func q(start: String, days: [Int], startOn: Bool = true, running: Bool = false, stopOn: Bool = false) -> Queue {
        Queue(id: "main", name: "Ana kuyruk", atOnce: 3, retries: 0, items: OrEmpty(), running: running,
              schedule: Schedule(startOn: startOn, start: start, stopOn: stopOn, stop: "07:00", days: OrEmpty(wrappedValue: days), onDone: "none"))
    }

    @Test func nextStart() {
        let all = [1, 2, 3, 4, 5, 6, 7]
        #expect(QueueStatus.subtitle(q(start: "23:00", days: all), now: now, calendar: cal) == "Bekliyor · başlangıç bugün 23:00")
        #expect(QueueStatus.subtitle(q(start: "02:00", days: all), now: now, calendar: cal) == "Bekliyor · başlangıç yarın 02:00")
        #expect(QueueStatus.subtitle(q(start: "02:00", days: [5]), now: now, calendar: cal) == "Bekliyor · başlangıç Cuma 02:00")
        #expect(QueueStatus.subtitle(q(start: "20:00", days: [1]), now: now, calendar: cal) == "Bekliyor · başlangıç Pazartesi 20:00")
    }

    @Test func runningAndOff() {
        #expect(QueueStatus.subtitle(q(start: "02:00", days: [1], startOn: false), now: now, calendar: cal) == "Duruyor")
        #expect(QueueStatus.subtitle(q(start: "02:00", days: [1], running: true), now: now, calendar: cal) == "Çalışıyor")
        #expect(QueueStatus.subtitle(q(start: "02:00", days: [1], running: true, stopOn: true), now: now, calendar: cal) == "Çalışıyor · bitiş 07:00")
    }
}
