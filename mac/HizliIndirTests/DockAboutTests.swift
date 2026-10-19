import AppKit
import Foundation
import Testing

private func item(_ id: String, _ state: String, got: Int64, size: Int64) -> Item {
    let json = """
    {"id":"\(id)","url":"https://a/x","name":"x","dir":"/tmp","category":"","queue":"main","state":"\(state)",
     "size":\(size),"got":\(got),"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
     "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null}
    """
    return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
}

@Suite struct DockProgressTests {
    @Test func nothingRunningLeavesTheIcon() {
        #expect(DockProgress.make([]) == nil)
        #expect(DockProgress.make([item("a", "paused", got: 5, size: 10), item("b", "done", got: 10, size: 10),
                                   item("c", "queued", got: 0, size: 10)]) == nil)
    }

    @Test func sumsGotOverSizeOfRunningOnes() {
        let p = DockProgress.make([item("a", "downloading", got: 50, size: 100), item("b", "downloading", got: 150, size: 300),
                                   item("c", "done", got: 999, size: 999)])
        #expect(p == DockProgress(fraction: 0.5, count: 2))
    }

    @Test func unknownSizeCountsButNotInTheBar() {
        #expect(DockProgress.make([item("a", "downloading", got: 7, size: -1)]) == DockProgress(fraction: 0, count: 1))
        let p = DockProgress.make([item("a", "downloading", got: 25, size: 100), item("b", "downloading", got: 7, size: -1)])
        #expect(p == DockProgress(fraction: 0.25, count: 2))
    }

    @Test func gotPastSizeIsClamped() {
        #expect(DockProgress.make([item("a", "downloading", got: 500, size: 100)])?.fraction == 1)
    }
}

@Suite struct AboutInfoTests {
    @Test func usesBundleVersionAndFixedTexts() {
        let o = AboutInfo.options(["CFBundleShortVersionString": "1.0", "CFBundleVersion": "7"])
        #expect(o[.applicationName] as? String == "Octo")
        #expect(o[.applicationVersion] as? String == "1.0")
        #expect(o[.version] as? String == "7")
        #expect((o[.credits] as? NSAttributedString)?.string == "Mac için hızlı indirme yöneticisi")
        #expect(o[NSApplication.AboutPanelOptionKey(rawValue: "Copyright")] as? String == "© 2026 Uba")
    }
}

@MainActor @Suite struct MainRouteURLTests {
    @Test func urlTravelsWithTheRequestOnce() {
        let r = MainRoute()
        r.requestAdd(url: "https://x.com/a.zip")
        #expect(r.takeAdd())
        #expect(r.takeURL() == "https://x.com/a.zip")
        #expect(r.takeURL() == "")
        r.requestAdd()
        #expect(r.takeURL() == "")
    }
}
