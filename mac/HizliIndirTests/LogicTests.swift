import Foundation
import Testing

@Suite struct CategoryPickTests {
    let cats = [
        Category(name: "Arşiv", exts: OrEmpty(wrappedValue: ["zip", "RAR"]), dir: ""),
        Category(name: "Programlar", exts: OrEmpty(wrappedValue: [".iso", "dmg"]), dir: "/Volumes/Big/Apps"),
    ]
    @Test func picksByExtensionCaseInsensitive() {
        #expect(CategoryPick.category(forURL: "https://x.com/a/B.ZIP?x=1", in: cats)?.name == "Arşiv")
        #expect(CategoryPick.category(forURL: "https://x.com/a/win.rar", in: cats)?.name == "Arşiv")
        #expect(CategoryPick.category(forURL: "https://x.com/win.ISO", in: cats)?.name == "Programlar")
    }
    @Test func noMatch() {
        #expect(CategoryPick.category(forURL: "https://x.com/readme.txt", in: cats) == nil)
        #expect(CategoryPick.category(forURL: "https://x.com/", in: cats) == nil)
    }
    @Test func saveDirFollowsEngineRules() {
        let d = "/Users/u/Downloads"
        // categoryFolders on: absolute dir as is, relative under the default folder, empty -> category name (Genel stays in the default folder)
        #expect(CategoryPick.saveDir(cats[1], defaultDir: d, categoryFolders: true) == "/Volumes/Big/Apps")
        #expect(CategoryPick.saveDir(cats[0], defaultDir: d, categoryFolders: true) == "/Users/u/Downloads/Arşiv")
        #expect(CategoryPick.saveDir(Category(name: "Video", exts: OrEmpty(), dir: "Filmler"), defaultDir: d, categoryFolders: true) == "/Users/u/Downloads/Filmler")
        #expect(CategoryPick.saveDir(Category(name: "Genel", exts: OrEmpty(), dir: ""), defaultDir: d, categoryFolders: true) == d)
        #expect(CategoryPick.saveDir(nil, defaultDir: d, categoryFolders: true) == d)
        // categoryFolders off: the engine would treat any dir we send as explicit, so always the default folder
        #expect(CategoryPick.saveDir(cats[1], defaultDir: d, categoryFolders: false) == d)
        #expect(CategoryPick.saveDir(cats[0], defaultDir: d, categoryFolders: false) == d)
        #expect(CategoryPick.saveDir(nil, defaultDir: d, categoryFolders: false) == d)
    }
}

@Suite struct URLCheckTests {
    @Test func accepts() {
        #expect(URLCheck.normalized("  https://a.com/x.zip \n") == "https://a.com/x.zip")
        #expect(URLCheck.normalized("HTTP://a.com") == "HTTP://a.com")
    }
    @Test func rejects() {
        #expect(URLCheck.normalized("") == nil)
        #expect(URLCheck.normalized("a.com/x") == nil)
        #expect(URLCheck.normalized("file:///etc/passwd") == nil)
        #expect(URLCheck.normalized("ftp://h/f.bin") == nil)  // the engine only downloads http(s)
        #expect(URLCheck.normalized("https://") == nil)
        #expect(URLCheck.normalized("https://a.com/x y") == nil)
    }
    @Test func clipboardNeedsSingleURL() {
        #expect(URLCheck.clipboardURL("https://a.com/f.iso") == "https://a.com/f.iso")
        #expect(URLCheck.clipboardURL("see https://a.com/f.iso") == nil)
        #expect(URLCheck.clipboardURL("https://a.com/a\nhttps://a.com/b") == nil)
        #expect(URLCheck.clipboardURL(nil) == nil)
    }
    @Test func extractFindsEveryLinkInDroppedText() {
        let text = "bak: https://a.com/a.zip, ve <https://b.com/b.iso>.\n(\"http://c.com/c?x=1&y=2\")\tftp://d.com/d mailto:x@y.z https://a.com/a.zip"
        #expect(URLCheck.extract(from: text) == ["https://a.com/a.zip", "https://b.com/b.iso", "http://c.com/c?x=1&y=2"])
    }
    @Test func extractReturnsNothingWithoutLinks() {
        #expect(URLCheck.extract(from: "").isEmpty)
        #expect(URLCheck.extract(from: "merhaba dünya a.com/x file:///etc/passwd").isEmpty)
    }
    @Test func fileNameFromURL() {
        #expect(URLCheck.fileName(from: "https://a.com/d/My%20File.iso?t=1") == "My File.iso")
        #expect(URLCheck.fileName(from: "https://a.com/") == "indirme")
    }
}

@Suite struct DuplicateNameTests {
    @Test func numbersUntilFree() {
        #expect(DuplicateName.numbered("a.iso", exists: { _ in false }) == "a (2).iso")
        #expect(DuplicateName.numbered("a.iso", exists: { ["a (2).iso", "a (3).iso"].contains($0) }) == "a (4).iso")
        #expect(DuplicateName.numbered("README", exists: { _ in false }) == "README (2)")
    }
}

@Suite struct OverwriteTests {
    struct Boom: Error, LocalizedError { var errorDescription: String? { "izin yok" } }
    @Test func trashesAndReturnsNilOnSuccess() {
        nonisolated(unsafe) var got: URL?
        let u = URL(fileURLWithPath: "/tmp/a.iso")
        #expect(Overwrite.trashExisting(u) { got = $0 } == nil)
        #expect(got == u)
    }
    @Test func failureYieldsTurkishError() {
        let msg = Overwrite.trashExisting(URL(fileURLWithPath: "/tmp/a.iso")) { _ in throw Boom() }
        #expect(msg?.contains("a.iso") == true)
        #expect(msg?.contains("üzerine yazılmadı") == true)
        #expect(msg?.contains("izin yok") == true)
    }
}

@Suite struct ConnBarTests {
    @Test func emptyConns() {
        #expect(ConnBar.slices([], size: 1000).isEmpty)
    }
    @Test func unknownSizeDrawsNothing() {
        #expect(ConnBar.slices([Conn(start: 0, pos: 50, end: -1)], size: -1).isEmpty)
        #expect(ConnBar.slices([Conn(start: 0, pos: 50, end: 99)], size: 0).isEmpty)
    }
    @Test func fractionsUseInclusiveEnd() {
        // end is inclusive (engine chunk): bytes 0...249 and 250...999 of 1000.
        let s = ConnBar.slices([Conn(start: 0, pos: 100, end: 249), Conn(start: 250, pos: 250, end: 999)], size: 1000)
        #expect(s == [ConnSlice(start: 0, pos: 0.1, end: 0.25), ConnSlice(start: 0.25, pos: 0.25, end: 1)])
    }
    @Test func completeSegmentFillsToEnd() {
        let s = ConnBar.slices([Conn(start: 500, pos: 1000, end: 999)], size: 1000)
        #expect(s == [ConnSlice(start: 0.5, pos: 1, end: 1)])
    }
    @Test func posBeyondEndIsClamped() {
        // A split can pull end back below pos; the overshoot is the same data, never drawn past end.
        let s = ConnBar.slices([Conn(start: 0, pos: 900, end: 399)], size: 1000)
        #expect(s == [ConnSlice(start: 0, pos: 0.4, end: 0.4)])
    }
    @Test func overlappingAndOutOfRangeAreClampedNotMerged() {
        let s = ConnBar.slices([Conn(start: 0, pos: 600, end: 799), Conn(start: 400, pos: 500, end: 5000)], size: 1000)
        #expect(s == [ConnSlice(start: 0, pos: 0.6, end: 0.8), ConnSlice(start: 0.4, pos: 0.5, end: 1)])
    }
    @Test func downloadedPerConnection() {
        #expect(Conn(start: 250, pos: 300, end: 999).downloaded == 50)
        #expect(Conn(start: 0, pos: 900, end: 399).downloaded == 400)   // pos beyond end
        #expect(Conn(start: 0, pos: 77, end: -1).downloaded == 77)      // unknown size: open-ended
        #expect(Conn(start: 10, pos: 5, end: 99).downloaded == 0)
    }
}
