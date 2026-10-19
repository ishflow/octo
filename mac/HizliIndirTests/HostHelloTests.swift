import Foundation
import Testing

@Suite struct HostHelloTests {
    private func lastHello(_ extra: String) async throws -> Date? {
        let server = try ScriptedEngine(); defer { server.stop() }
        server.script = { cmd, id in
            cmd == "list" ? .lines([#"{"id":\#(id),"ok":true,"items":[],"queues":[],"settings":{}\#(extra)}"#]) : nil
        }
        let client = EngineClient(socketPath: server.path, helper: nil)
        defer { Task { await client.close() } }
        return try await client.lastHello()
    }

    @Test func engineClientReadsLastHelloFromList() async throws {
        #expect(try await lastHello(#","lastHello":1760922000"#) == Date(timeIntervalSince1970: 1_760_922_000))
    }

    @Test func noHelloYetIsNil() async throws {
        #expect(try await lastHello("") == nil)
    }

    @Test func textSaysWhenOrThatItNeverConnected() {
        let now = Date(timeIntervalSince1970: 1_760_922_000)
        #expect(HostHello.text(nil, now: now) == "Henüz bağlanmadı")
        #expect(HostHello.text(now.addingTimeInterval(-180), now: now).contains("3"))
        #expect(HostHello.text(now.addingTimeInterval(-180), now: now).contains("önce"))
    }
}
