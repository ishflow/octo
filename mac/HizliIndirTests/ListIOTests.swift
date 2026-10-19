import Foundation
import Testing

@Suite struct URLListTests {
    @Test func parseKeepsOrderAndSkipsBlankCommentInvalidAndDuplicate() {
        let text = """
        https://a.example/1.zip

        # not a link
        ftp://a.example/2.zip
        not a url
          http://b.example/3.iso  
        https://a.example/1.zip
        """
        let r = URLList.parse(text)
        #expect(r.urls == ["https://a.example/1.zip", "http://b.example/3.iso"])
        #expect(r.skipped == 3)  // ftp, "not a url", duplicate; blank and # lines are not counted
    }

    @Test func parseHandlesCRLFAndEmptyInput() {
        #expect(URLList.parse("https://a/x\r\nhttps://a/y\r\n").urls == ["https://a/x", "https://a/y"])
        let empty = URLList.parse("")
        #expect(empty.urls.isEmpty && empty.skipped == 0)
    }

    @Test func textIsOneURLPerLineWithTrailingNewline() {
        #expect(URLList.text(["https://a/x", "https://a/y"]) == "https://a/x\nhttps://a/y\n")
        #expect(URLList.text([]) == "")
    }

    @Test func exportThenImportRoundTrips() throws {
        let urls = ["https://a.example/1.zip", "https://b.example/2.iso?x=1"]
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("octo-list-\(UUID().uuidString).txt")
        defer { try? FileManager.default.trashItem(at: file, resultingItemURL: nil) }
        try URLList.write(urls, to: file)
        #expect(URLList.parse(try URLList.read(file)).urls == urls)
    }

    @Test func readRejectsNonTextFile() throws {
        let file = FileManager.default.temporaryDirectory.appendingPathComponent("octo-bin-\(UUID().uuidString).txt")
        try Data([0xff, 0xfe, 0x00, 0xd8]).write(to: file)
        defer { try? FileManager.default.trashItem(at: file, resultingItemURL: nil) }
        #expect(throws: (any Error).self) { try URLList.read(file) }
    }
}
