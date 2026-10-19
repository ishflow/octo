import Foundation
import Testing

@Suite struct InfoEditTests {
    @Test func itemDecodesReferrerAndPageURLAndToleratesOldEngine() throws {
        let old = ScriptedEngine.item("d1", state: "queued", got: 0)
        let new = old.replacingOccurrences(of: #""maxConns":8"#,
                                           with: #""maxConns":8,"referrer":"https://a.org/","pageUrl":"https://a.org/p?q=1""#)
        let a = try JSONDecoder().decode(Item.self, from: Data(old.utf8))
        let b = try JSONDecoder().decode(Item.self, from: Data(new.utf8))
        #expect(a.referrer == nil && a.pageUrl == nil)
        #expect(b.referrer == "https://a.org/" && b.pageUrl == "https://a.org/p?q=1")
    }

    @Test func onlyWebPagesAreClickable() {
        #expect(InfoPanel.pageLink("https://a.org/p?q=1")?.host == "a.org")
        #expect(InfoPanel.pageLink("javascript:alert(1)") == nil)
        #expect(InfoPanel.pageLink(nil) == nil && InfoPanel.pageLink("") == nil)
    }

    @Test @MainActor func describeSendsContractCommandAndAppliesReply() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let reply = ScriptedEngine.item("d17", state: "downloading", got: 250)
            .replacingOccurrences(of: #""description":"""#, with: #""description":"not""#)
        server.script = { cmd, id in cmd == "item.describe" ? .lines([#"{"id":\#(id),"ok":true,"item":\#(reply)}"#]) : nil }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected && store.items.first?.id == "d17" })
        try await store.describe(id: "d17", text: "not")
        #expect(server.received.contains("item.describe"))
        #expect(store.items.first?.description == "not")
    }
}
