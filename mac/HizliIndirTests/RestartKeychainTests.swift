import Foundation
import Security
import Testing

@Suite struct RestartReasonTests {
    private func item(_ extra: String) throws -> Item {
        try JSONDecoder().decode(Item.self, from: Data(#"{"id":"d1","url":"u","name":"n","dir":"/","category":"","queue":"main","state":"downloading","size":10,"got":1,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]\#(extra)}"#.utf8))
    }
    @Test func shownWhenEngineRestarted() throws {
        #expect(InfoPanel.restartNote(try item(#","restartReason":"dosya sunucuda değişti (ETag)""#))
                == "Baştan başladı: dosya sunucuda değişti (ETag)")
    }
    @Test func hiddenWhenEmptyOrMissing() throws {
        #expect(InfoPanel.restartNote(try item(#","restartReason":"""#)) == nil)
        #expect(InfoPanel.restartNote(try item("")) == nil)  // older engine: no key
    }
}

/// The Keychain itself is faked: `read` and `add` are injected, so nothing touches the real one.
@Suite struct LoginKeychainReplaceTests {
    private final class Adds {
        var dicts: [[CFString: Any]] = []
        var results: [OSStatus]
        init(_ r: [OSStatus]) { results = r }
        func add(_ d: CFDictionary) -> OSStatus { dicts.append(d as! [CFString: Any]); return results.removeFirst() }
    }
    private func password(_ d: [CFString: Any]) -> String? { (d[kSecValueData] as? Data).flatMap { String(data: $0, encoding: .utf8) } }

    @Test func failedAddPutsTheOldPasswordBack() {
        let adds = Adds([errSecInteractionNotAllowed, errSecSuccess])
        let err = LoginKeychain.set("yeni", for: "octo-test-host", read: { _ in "eski" }, add: adds.add)
        #expect(err != nil)
        guard adds.dicts.count == 2 else { Issue.record("expected a second add"); return }
        #expect(password(adds.dicts[1]) == "eski" && adds.dicts[1][kSecAttrAccess] == nil)
    }

    @Test func failedAddWithNothingBeforeRestoresNothing() {
        let adds = Adds([errSecInteractionNotAllowed])
        #expect(LoginKeychain.set("yeni", for: "octo-test-host", read: { _ in nil }, add: adds.add) != nil)
        #expect(adds.dicts.count == 1)
    }

    @Test func successAddsOnce() {
        let adds = Adds([errSecSuccess])
        #expect(LoginKeychain.set("yeni", for: "octo-test-host", read: { _ in "eski" }, add: adds.add) == nil)
        #expect(adds.dicts.count == 1 && password(adds.dicts[0]) == "yeni")
    }
}

/// Builds the access object in memory only; nothing is written to the real Keychain.
@Suite struct LoginKeychainAccessTests {
    private func trustedPaths(_ access: SecAccess) -> [String] {
        var acls: CFArray?
        guard SecAccessCopyACLList(access, &acls) == errSecSuccess, let acls = acls as? [SecACL] else { return [] }
        return acls.flatMap { acl -> [String] in
            var apps: CFArray?, desc: CFString?
            var prompt = SecKeychainPromptSelector()
            guard SecACLCopyContents(acl, &apps, &desc, &prompt) == errSecSuccess,
                  let apps = apps as? [SecTrustedApplication] else { return [] }
            return apps.compactMap { app in
                var data: CFData?
                guard SecTrustedApplicationCopyData(app, &data) == errSecSuccess, let d = data as Data? else { return nil }
                return String(decoding: d.prefix { $0 != 0 }, as: UTF8.self)
            }
        }
    }
    @Test func trustsEngineAndSelf() throws {
        let access = try #require(LoginKeychain.access(trusting: ["/bin/ls"]))
        let paths = Set(trustedPaths(access))
        #expect(paths.contains("/bin/ls"))
        #expect(paths.count == 2)  // plus the creating process: Octo itself must keep reading without a prompt
    }
}
