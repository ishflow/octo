import Foundation
import Testing

@MainActor @Suite struct ClipboardWatchTests {
    private let cats = [Category(name: "Sıkıştırılmış", exts: OrEmpty(wrappedValue: ["zip", ".rar"]), dir: "")]
    private func defaults(on: Bool? = nil) -> UserDefaults {
        let d = UserDefaults(suiteName: "ClipboardWatchTests-\(UUID().uuidString)")!
        if let on { d.set(on, forKey: ClipboardWatch.key) }
        return d
    }

    private func store(_ d: UserDefaults) -> Store {
        let s = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil), defaults: d)
        s.settings = Settings(maxConns: 8, downloadDir: "/tmp", categoryFolders: true, duplicate: "ask", speedLimitOn: false,
                              speedLimitKBps: 0, categories: OrEmpty(wrappedValue: cats), captureExts: OrEmpty(), excludedSites: OrEmpty(), logins: OrEmpty())
        return s
    }

    private func item(url: String) -> Item {
        let json = """
        {"id":"a","url":"\(url)","name":"a.zip","dir":"/tmp","category":"","queue":"main","state":"done",
         "size":1,"got":1,"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
         "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null}
        """
        return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
    }

    @Test func linkNeedsHttpAndKnownExtension() {
        #expect(ClipboardWatch.link("https://x.com/a.zip", in: cats) == "https://x.com/a.zip")
        #expect(ClipboardWatch.link(" http://x.com/B.RAR\n", in: cats) == "http://x.com/B.RAR")
        #expect(ClipboardWatch.link("ftp://x.com/a.zip", in: cats) == nil)
        #expect(ClipboardWatch.link("https://x.com/a.xyz", in: cats) == nil)
        #expect(ClipboardWatch.link("https://x.com/sayfa", in: cats) == nil)
        #expect(ClipboardWatch.link("merhaba a.zip", in: cats) == nil)
        #expect(ClipboardWatch.link(nil, in: cats) == nil)
    }

    @Test func offByDefaultNeverReads() {
        var reads = 0
        var found: [String] = []
        let s = store(defaults())
        s.pasteboard = { (count: 0, string: { reads += 1; return "https://x.com/a.zip" }) }
        s.onClipboardLink = { found.append($0) }
        s.pollClipboard()
        s.pollClipboard()
        #expect(reads == 0 && found.isEmpty)
    }

    @Test func asksOnceAfterChangeCountMoves() {
        var count = 5, text = "https://x.com/old.zip"
        var reads = 0
        var found: [String] = []
        let d = defaults(on: true)
        let s = store(d)
        s.pasteboard = { (count: count, string: { reads += 1; return text }) }
        s.onClipboardLink = { found.append($0) }
        s.pollClipboard()              // switched on: whatever is already there is not asked about
        s.pollClipboard()
        #expect(reads == 0 && found.isEmpty)
        count = 6; text = "https://x.com/new.zip"
        s.pollClipboard()
        s.pollClipboard()              // same changeCount: no second read, no second question
        #expect(reads == 1 && found == ["https://x.com/new.zip"])
        count = 7; text = "https://x.com/not-a-file"
        s.pollClipboard()
        #expect(found.count == 1)
    }

    @Test func listedLinkIsNotAskedAgain() {
        var count = 1
        var found: [String] = []
        let d = defaults(on: true)
        let s = store(d)
        s.items = [item(url: "https://x.com/a.zip")]
        s.pasteboard = { (count: count, string: { "https://x.com/a.zip" }) }
        s.onClipboardLink = { found.append($0) }
        s.pollClipboard()
        count = 2
        s.pollClipboard()
        #expect(found.isEmpty)
    }
}
