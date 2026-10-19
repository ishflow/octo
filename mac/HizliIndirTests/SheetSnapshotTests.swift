import AppKit
import SwiftUI
import Testing
import WebKit

/// Renders DZ4's sheets with sample data (light + dark). PNGs go to $SHOTS_DIR when set
/// (`TEST_RUNNER_SHOTS_DIR=… xcodebuild test`). Offscreen windows: Liquid Glass may render flat here.
@MainActor @Suite(.serialized) struct SheetSnapshotTests {
    static let sample = #"""
    {"items":[{"id":"d1","url":"https://software.download.prss.microsoft.com/dbazure/Windows11_Client_arm64_en-gb_26300_9457.iso","name":"Windows11_Client_arm64_en-gb.iso","dir":"/tmp","category":"Programlar","queue":"main","state":"paused","size":8000000000,"got":4880000000,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:45:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]}],
     "queues":[{"id":"main","name":"Ana kuyruk","atOnce":3,"retries":3,"items":[],"running":false,"schedule":{"startOn":false,"start":"","stopOn":false,"stop":"","days":[],"onDone":"none"}},
               {"id":"sync","name":"Senkronizasyon","atOnce":1,"retries":3,"items":[],"running":false,"schedule":{"startOn":false,"start":"","stopOn":false,"stop":"","days":[],"onDone":"none"}}],
     "settings":{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":true,"duplicate":"ask","speedLimitOn":false,"speedLimitKBps":0,
       "categories":[{"name":"Programlar","exts":["iso","dmg"],"dir":"~/Downloads/Programlar"},{"name":"Arşiv","exts":["zip"],"dir":""}],
       "captureExts":[],"excludedSites":[],"logins":[]}}
    """#

    let store: Store

    init() throws {
        _ = NSApplication.shared
        store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        store.apply(try JSONDecoder().decode(ListReply.self, from: Data(Self.sample.utf8)))
    }

    private func save(_ view: NSView, _ name: String) throws {
        let rep = try #require(view.bitmapImageRepForCachingDisplay(in: view.bounds))
        view.cacheDisplay(in: view.bounds, to: rep)
        let png = try #require(rep.representation(using: .png, properties: [:]))
        #expect(png.count > 1000)
        if let dir = ProcessInfo.processInfo.environment["SHOTS_DIR"] {
            try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
        }
    }

    private func shot(_ name: String, _ view: some View) throws {
        for dark in [false, true] {
            let host = NSHostingView(rootView: view.environment(store).background(Color(nsColor: .windowBackgroundColor)))
            host.frame.size = host.fittingSize
            let w = NSWindow(contentRect: host.frame, styleMask: [.titled], backing: .buffered, defer: false)
            w.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
            w.contentView = host
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: Date().addingTimeInterval(0.3))  // let .task/.onAppear settle
            try save(host, name + (dark ? "-dark" : "-light"))
        }
    }

    private func shot(_ name: String, _ alert: NSAlert) throws {
        for dark in [false, true] {
            alert.window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
            alert.layout()
            // shortcut: offscreen the alert's effect view paints white, so the dark PNG shows white text on white.
            try save(try #require(alert.window.contentView), name + (dark ? "-dark" : "-light"))
        }
    }

    @Test func addURL() throws {
        try shot("AddURL", AddDownloadSheet(url: "https://software.download.prss.microsoft.com/dbazure/Windows11_Client_arm64_en-gb_26300_9457.iso?t=b00de353",
                                            fromClipboard: true, onBatch: {}))
    }

    @Test func newDownload() throws {
        try shot("NewDownload", NewDownloadForm(url: store.items[0].url, settings: store.settings, queues: store.queues,
                                                probe: ProbeReply(name: "Windows11_Client_arm64_en-gb.iso", size: 8_000_000_000, resumable: true)))
    }

    @Test func newDownloadVideo() throws {
        let info = try JSONDecoder().decode(StreamInfo.self, from: Data(#"""
        {"kind":"dash","container":"mp4","live":false,"protected":false,"duration":252.5,
         "variants":[{"id":"v1","label":"1080p","bandwidth":4500000},{"id":"v0","label":"480p","bandwidth":850000}],
         "audios":[{"id":"a0","name":"Türkçe","lang":"tr","default":true},{"id":"a1","name":"English","lang":"en","default":false}]}
        """#.utf8))
        try shot("NewDownloadVideo", NewDownloadForm(url: "https://v.example.com/film/manifest.mpd", settings: store.settings, queues: store.queues,
                                                     probe: ProbeReply(name: "manifest.mpd", size: -1, resumable: false, kind: "dash"), streamInfo: info))
    }

    @Test func batch() throws {
        try shot("Batch", BatchSheet(pattern: "https://example.com/galeri/resim*.jpg", from: "1", to: "120", digits: "3"))
    }

    @Test func onboarding() throws {
        try shot("Onboarding", OnboardingView {})
    }

    @Test func duplicate() throws {
        try shot("Duplicate", DuplicatePrompt.alert(name: "Windows11_Client_arm64_en-gb.iso", url: store.items[0].url, inList: true,
                                                    numbered: "Windows11_Client_arm64_en-gb (2).iso"))
    }

    @Test func deleteConfirm() throws {
        try shot("DeleteConfirm", DeletePrompt.alert(count: 2, unfinished: 1))
    }
}

/// The extension's popup and link picker rendered by WebKit (no chrome.* here: scripts stop early, sample rows are injected).
@MainActor @Suite(.serialized) struct ExtensionSnapshotTests {
    static let dir = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("../../extension/chrome").standardized

    private final class Loaded: NSObject, WKNavigationDelegate {
        var done = false
        func webView(_ w: WKWebView, didFinish _: WKNavigation!) { done = true }
    }

    private func shot(_ page: String, size: CGSize, js: String) async throws {
        for dark in [false, true] {
            let web = WKWebView(frame: CGRect(origin: .zero, size: size))
            web.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
            let nav = Loaded()
            web.navigationDelegate = nav
            web.loadFileURL(Self.dir.appendingPathComponent(page), allowingReadAccessTo: Self.dir)
            // 60 s ceiling, returns on didFinish: a cold WebContent launch on a loaded machine took ~4 s of the old 5 s.
            for _ in 0..<1200 where !nav.done { try await Task.sleep(for: .milliseconds(50)) }
            #expect(nav.done)
            _ = try? await web.evaluateJavaScript(js)
            let img = try await web.takeSnapshot(configuration: nil)
            let rep = try #require(img.tiffRepresentation.flatMap(NSBitmapImageRep.init(data:)))
            let png = try #require(rep.representation(using: .png, properties: [:]))
            #expect(png.count > 1000)
            if let out = ProcessInfo.processInfo.environment["SHOTS_DIR"] {
                try png.write(to: URL(fileURLWithPath: out).appendingPathComponent("\(page.split(separator: ".")[0])-\(dark ? "dark" : "light").png"))
            }
        }
    }

    @Test func popup() async throws {
        try await shot("popup.html", size: CGSize(width: 320, height: 250), js: """
            document.getElementById('enabled').checked = true;
            const s = document.getElementById('status'); s.className = 'ok'; s.textContent = '● Uygulamaya bağlı'; 1
            """)
    }

    @Test func links() async throws {
        try await shot("links.html", size: CGSize(width: 900, height: 640), js: """
            document.getElementById('page').textContent = 'https://example.com/galeri';
            document.getElementById('count').textContent = '4 bağlantıdan 3 tanesi seçili';
            document.getElementById('add').textContent = "3 dosyayı Octo'ya ekle";
            for (const [t, u, c] of [['Kurulum (macOS)', 'https://example.com/dl/octo-1.0.dmg', 1], ['Kaynak kod', 'https://example.com/dl/octo-1.0.tar.gz', 1],
                                     ['Kılavuz', 'https://example.com/docs/kilavuz.pdf', 1], ['Hakkında', 'https://example.com/hakkinda', 0]]) {
              const li = document.createElement('li');
              li.innerHTML = `<label><input type="checkbox" ${c ? 'checked' : ''}><span class="text">${t}</span><span class="url">${u}</span></label>`;
              document.getElementById('list').append(li);
            }; 1
            """)
    }
}
