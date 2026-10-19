import Foundation
import Testing

private func item(_ id: String, _ state: String) -> Item {
    let json = """
    {"id":"\(id)","url":"https://a/x","name":"x","dir":"/tmp","category":"","queue":"main","state":"\(state)",
     "size":100,"got":10,"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
     "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null}
    """
    return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
}

@Suite struct ItemActionsTests {
    @Test func resumeOnlyForPausedOrFailed() {
        #expect(ItemActions.canResume([item("a", "paused")]))
        #expect(ItemActions.canResume([item("a", "done"), item("b", "error")]))
        #expect(!ItemActions.canResume([item("a", "downloading"), item("b", "done")]))
        #expect(!ItemActions.canResume([]))
    }
    @Test func pauseOnlyForRunningOrQueued() {
        #expect(ItemActions.canPause([item("a", "queued")]))
        #expect(ItemActions.canPause([item("a", "done"), item("b", "downloading")]))
        #expect(!ItemActions.canPause([item("a", "paused"), item("b", "error")]))
    }
}

@Suite struct MainRouteTests {
    @MainActor @Test func addRequestIsTakenOnce() {
        let r = MainRoute()
        #expect(!r.takeAdd())
        r.requestAdd()
        #expect(r.addRequested)
        #expect(r.takeAdd())
        #expect(!r.takeAdd())
    }
}
