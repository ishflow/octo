import Foundation
import Testing

@MainActor @Suite struct KeepAwakeTests {
    private final class Spy {
        var begun = 0, ended = 0
        var on = true
        var held: Int { begun - ended }
    }

    private func store(_ spy: Spy) -> Store {
        let s = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        s.keepAwake = { spy.on }
        s.beginActivity = { spy.begun += 1; return NSObject() }
        s.endActivity = { _ in spy.ended += 1 }
        return s
    }

    private func item(_ state: ItemState, id: String = "a") -> Item {
        let json = """
        {"id":"\(id)","url":"https://x.com/a.zip","name":"a.zip","dir":"/tmp","category":"","queue":"main","state":"\(state.rawValue)",
         "size":1,"got":1,"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
         "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null}
        """
        return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
    }

    private func update(_ id: String, _ state: ItemState) -> EngineEvent {
        .progress([ProgressUpdate(id: id, state: state, got: 1, size: 1, speed: 0, eta: -1, conns: OrEmpty())])
    }

    @Test func holdsWhileSomethingDownloadsAndReleasesAfter() {
        let spy = Spy(), s = store(spy)
        s.apply(.item(item(.queued)))
        #expect(spy.held == 0)
        s.apply(update("a", .downloading))
        #expect(spy.held == 1)
        s.apply(update("a", .downloading))
        s.apply(.item(item(.downloading, id: "b")))
        #expect(spy.begun == 1)  // one hold covers every download
        s.apply(update("a", .done))
        #expect(spy.held == 1)  // b still downloads
        s.apply(update("b", .paused))
        #expect(spy.held == 0)
    }

    @Test func removingTheLastDownloadReleases() {
        let spy = Spy(), s = store(spy)
        s.apply(.item(item(.downloading)))
        #expect(spy.held == 1)
        s.apply(.removed(["a"]))
        #expect(spy.held == 0)
    }

    @Test func offSwitchNeverHolds() {
        let spy = Spy(), s = store(spy)
        spy.on = false
        s.apply(.item(item(.downloading)))
        #expect(spy.begun == 0)
    }

    @Test func switchingOffWhileDownloadingReleases() {
        let spy = Spy(), s = store(spy)
        s.apply(.item(item(.downloading)))
        #expect(spy.held == 1)
        spy.on = false
        s.syncSleepGuard()
        #expect(spy.held == 0)
        spy.on = true
        s.syncSleepGuard()
        #expect(spy.held == 1)
    }

    @Test func onByDefault() {
        let d = UserDefaults(suiteName: "KeepAwakeTests-\(UUID().uuidString)")!
        #expect(KeepAwake.enabled(d))
        d.set(false, forKey: KeepAwake.key)
        #expect(!KeepAwake.enabled(d))
    }
}
