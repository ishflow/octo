import Foundation
import Testing

private func item(_ id: String, _ state: String, speed: Int64 = 0, error: String = "", name: String = "x.iso",
                  resumable: Bool = true) -> Item {
    let json = """
    {"id":"\(id)","url":"https://a/\(name)","name":"\(name)","dir":"/tmp","category":"","queue":"main","state":"\(state)",
     "size":100,"got":10,"speed":\(speed),"eta":-1,"resumable":\(resumable),"error":"\(error)","description":"",
     "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null}
    """
    return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
}

@Suite struct ActiveSummaryTests {
    @Test func onlyDownloadingCountAndSpeedSums() {
        let s = ActiveSummary.make([item("a", "downloading", speed: 100), item("b", "paused", speed: 50),
                                    item("c", "downloading", speed: 25), item("d", "done"), item("e", "queued")])
        #expect(s.active.map(\.id) == ["a", "c"])
        #expect(s.totalSpeed == 125)
        #expect(s.count == 2)
    }
    @Test func emptyWhenIdle() {
        let s = ActiveSummary.make([item("d", "done")])
        #expect(s.count == 0 && s.totalSpeed == 0 && s.active.isEmpty)
    }
}

@Suite struct CountdownTextTests {
    @Test func shutdown() { #expect(CountdownText.text(action: "shutdown", seconds: 35) == "Mac 35 sn içinde kapanacak") }
    @Test func sleep() { #expect(CountdownText.text(action: "sleep", seconds: 7) == "Mac 7 sn içinde uyku moduna geçecek") }
    @Test func negativeClampsToZero() { #expect(CountdownText.text(action: "shutdown", seconds: -3) == "Mac 0 sn içinde kapanacak") }
    @Test func remainingRoundsUp() {
        let now = Date(timeIntervalSince1970: 1000)
        #expect(CountdownText.remaining(until: now.addingTimeInterval(34.2), now: now) == 35)
        #expect(CountdownText.remaining(until: now.addingTimeInterval(-1), now: now) == 0)
    }
}

@Suite struct NotificationMapTests {
    @Test func doneHasOpenAndRevealCategory() {
        let n = NotificationMap.content(for: item("d1", "done", name: "a.zip"))
        #expect(n?.title == "İndirme tamamlandı")
        #expect(n?.body == "a.zip · 100 bayt")  // no timestamps: no duration
        #expect(n?.category == "done")
        #expect(n?.identifier == "done-d1")
        #expect(n?.path == "/tmp/a.zip")
    }
    @Test func doneBodyIsNameSizeDuration() {
        var i = item("d5", "done", name: "Windows11.iso")
        i.size = 8_543_594_496
        i.lastTry = "2026-10-19T20:45:00Z"
        i.finished = "2026-10-19T20:48:58.123456Z"  // engine writes fractional seconds
        #expect(NotificationMap.content(for: i)?.body == "Windows11.iso · 8,54 GB · 3 dk 58 sn")
    }
    @Test func durationFallsBackToAdded() {
        var i = item("d6", "done", name: "a.zip")
        i.finished = "2026-10-19T20:00:45Z"  // added 20:00:00
        #expect(NotificationMap.content(for: i)?.body == "a.zip · 100 bayt · 45 sn")
    }
    @Test func errorCarriesErrorText() {
        let n = NotificationMap.content(for: item("d2", "error", error: "Sunucu 404 döndürdü", name: "b.iso"))
        #expect(n?.title == "İndirme başarısız")
        #expect(n?.subtitle == "b.iso")
        #expect(n?.body == "Sunucu 404 döndürdü")
        #expect(n?.category == nil)
    }
    @Test func errorWithoutTextFallsBack() { #expect(NotificationMap.content(for: item("d3", "error"))?.body == "Bilinmeyen hata") }
    @Test func otherStatesProduceNothing() { #expect(NotificationMap.content(for: item("d4", "paused")) == nil) }
}

@Suite struct StoreM5Tests {
    @MainActor @Test func failedAndActionHooks() {
        let store = Store()
        store.items = [item("a", "downloading")]
        var failed: [String] = []
        store.onFailed = { failed.append($0.id) }
        let bad = item("a", "error", error: "x")
        store.apply(.item(bad))
        store.apply(.item(bad)) // already error: no second hook
        #expect(failed == ["a"])

        let now = Date(timeIntervalSince1970: 500)
        store.now = { now }
        store.apply(.action("shutdown", seconds: 35))
        #expect(store.pendingAction?.kind == "shutdown")
        #expect(store.pendingAction?.deadline == now.addingTimeInterval(35))
    }
}

@Suite struct RefreshOfferHookTests {
    private func offer(_ id: String, refreshOf: String?) -> Item {
        let extra = refreshOf.map { #","refreshOf":"\#($0)""# } ?? ""
        let json = """
        {"id":"\(id)","url":"https://a/x.iso","name":"x.iso","dir":"/tmp","category":"","queue":"main","state":"paused",
         "size":100,"got":10,"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
         "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null\(extra)}
        """
        return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
    }

    /// The offer sheet lives in the main window; with the window closed the hook is the only way to tell the user.
    @MainActor @Test func announcesEachOfferOnce() {
        let store = Store()
        var seen: [String] = []
        store.onRefreshOffer = { seen.append($0.id) }
        store.apply(.item(offer("a", refreshOf: nil)))
        store.apply(.item(offer("b", refreshOf: "")))
        store.apply(.item(offer("c", refreshOf: "a")))
        store.apply(.item(offer("c", refreshOf: "a")))  // same item again: no second banner
        #expect(seen == ["c"])
    }
}

@Suite struct PauseWarningTests {
    @MainActor @Test func asksOnlyWhenADownloadCannotResume() async {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        store.items = [item("a", "downloading", resumable: false), item("b", "downloading"), item("c", "paused", resumable: false)]
        var asked: [[String]] = []
        store.confirmPause = { asked.append($0.map(\.id)); return false }
        #expect(await store.pause(["b", "c"]) == true)  // nothing to lose: sent without asking (fails: no engine)
        #expect(asked.isEmpty && store.lastError != nil)
        store.lastError = nil
        #expect(await store.pause(["a", "b"]) == false)  // declined: nothing sent
        #expect(await store.stopAll() == false)
        store.queues = [Queue(id: "q", name: "Q", atOnce: 1, retries: 0, items: OrEmpty(wrappedValue: ["a"]), running: true,
                              schedule: Schedule(startOn: false, start: "23:00", stopOn: false, stop: "07:30",
                                                 days: OrEmpty(wrappedValue: [1]), onDone: "none"))]
        #expect(await store.stopQueue("q") == false)
        #expect(asked == [["a"], ["a"], ["a"]] && store.lastError == nil)
        store.confirmPause = { _ in true }
        #expect(await store.pause(["a"]) == true && store.lastError != nil)
    }
}

@Suite struct FinishOpenFlagTests {
    private let settings = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: false, duplicate: "number",
                                    speedLimitOn: false, speedLimitKBps: 500)

    @MainActor @Test func flagsOfGoneItemsAreDropped() {
        let d = UserDefaults(suiteName: "FinishOpenFlagTests-\(UUID().uuidString)")!, gone = FinishActions.openKey("t-gone"), live = FinishActions.openKey("t-live")
        d.set(true, forKey: gone); d.set(true, forKey: live)
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil), defaults: d)
        store.apply(ListReply(items: OrEmpty(wrappedValue: [item("t-live", "paused")]), queues: OrEmpty(), settings: settings))
        #expect(d.object(forKey: gone) == nil && d.bool(forKey: live))
        store.apply(.removed(["t-live"]))
        #expect(d.object(forKey: live) == nil)
    }

    /// A parallel xcodebuild run must never see (or purge) another run's keys in the shared com.uba.octo plist.
    @MainActor @Test func injectedDefaultsLeaveStandardAlone() {
        let std = UserDefaults.standard, key = FinishActions.openKey("t-std-\(UUID().uuidString)")
        std.set(true, forKey: key)
        defer { std.removeObject(forKey: key) }
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil), defaults: UserDefaults(suiteName: "FinishOpenFlagTests-\(UUID().uuidString)")!)
        store.apply(ListReply(items: OrEmpty(), queues: OrEmpty(), settings: settings))
        store.apply(.removed([key.replacingOccurrences(of: FinishActions.openKey(""), with: "")]))
        #expect(std.bool(forKey: key))
    }
}

@Suite struct ListPendingActionTests {
    private let settings = #""settings":{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500}"#

    @MainActor @Test func countdownInListOpensPanelAfterConnect() throws {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        let now = Date(timeIntervalSince1970: 900)
        store.now = { now }
        store.apply(try JSONDecoder().decode(ListReply.self, from: Data(#"{"items":[],"queues":[],\#(settings)}"#.utf8)))
        #expect(store.pendingAction == nil)
        let json = #"{"items":[],"queues":[],\#(settings),"pendingAction":{"action":"sleep","seconds":20}}"#
        store.apply(try JSONDecoder().decode(ListReply.self, from: Data(json.utf8)))
        #expect(store.pendingAction == PendingAction(kind: "sleep", deadline: now.addingTimeInterval(20)))
    }
}

@Suite struct HostInstallerTests {
    private func repoScript() -> URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("HizliIndir/Resources/install-native-host.sh")
    }

    @Test func writesManifestIntoTempHomeOnly() async throws {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("hi-home-\(UUID().uuidString)")
        let chrome = home.appendingPathComponent("Library/Application Support/Google/Chrome")
        try FileManager.default.createDirectory(at: chrome, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: home) }
        let bin = home.appendingPathComponent("octo")
        try "#!/bin/sh\n".write(to: bin, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: bin.path)

        let r = await HostInstaller.run(script: repoScript(), binary: bin, home: home.path)
        #expect(r.ok)
        #expect(r.output.contains("Kuruldu: Google/Chrome"))
        let manifest = try String(contentsOf: chrome.appendingPathComponent("NativeMessagingHosts/com.uba.octo.json"), encoding: .utf8)
        #expect(manifest.contains(bin.path))
    }

    @Test func missingBinaryFailsWithOutput() async {
        let home = FileManager.default.temporaryDirectory.appendingPathComponent("hi-home-\(UUID().uuidString)")
        try? FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: home) }
        let r = await HostInstaller.run(script: repoScript(), binary: home.appendingPathComponent("yok"), home: home.path)
        #expect(!r.ok)
        #expect(r.output.contains("ikili dosya bulunamadı"))
    }
}
