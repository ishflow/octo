import Foundation
import Testing

@Suite struct QuarantineFailedTests {
    private func item(_ extra: String) throws -> Item {
        try JSONDecoder().decode(Item.self, from: Data(#"{"id":"d1","url":"u","name":"n","dir":"/","category":"","queue":"main","state":"done","size":10,"got":10,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]\#(extra)}"#.utf8))
    }
    @Test func skippedWhenQuarantineFailed() throws {
        #expect(!FinishActions.opensOnFinish(try item(#","quarantineFailed":true"#), wanted: true))
    }
    @Test func opensWhenQuarantinedOrFieldMissing() throws {
        #expect(FinishActions.opensOnFinish(try item(#","quarantineFailed":false"#), wanted: true))
        #expect(FinishActions.opensOnFinish(try item(""), wanted: true))  // older engine: no key
        #expect(!FinishActions.opensOnFinish(try item(""), wanted: false))
    }
}
