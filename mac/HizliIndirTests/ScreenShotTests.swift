import AppKit
import SwiftUI
import Testing

// Offscreen renders of the progress / complete / info / speed / menu-bar screens with sample data.
// PNGs are written only when the runner gets DZ2_SHOTS (xcodebuild: TEST_RUNNER_DZ2_SHOTS=<dir>).

private func item(_ id: String, _ name: String, _ state: ItemState, size: Int64, got: Int64, speed: Int64 = 0, eta: Int = -1,
                  conns: [Conn] = []) -> Item {
    Item(id: id, url: "https://software.download.prss.microsoft.com/dbazure/\(name)", name: name,
         dir: NSHomeDirectory() + "/Downloads/Programlar", category: "Programlar", queue: "main", state: state,
         size: size, got: got, speed: speed, eta: eta, resumable: true, error: "", description: "Mac'teki sanal makine için",
         added: ISO8601DateFormatter().string(from: .now.addingTimeInterval(-1800)), finished: nil,
         lastTry: ISO8601DateFormatter().string(from: .now.addingTimeInterval(-60)), maxConns: 8, conns: OrEmpty(wrappedValue: conns))
}

@MainActor
private func sampleStore() -> Store {
    let size: Int64 = 8_543_594_496
    let part = size / 8
    let done = [1.0, 0.82, 0.66, 0.60, 0.55, 0.52, 0.48, 0.45]
    let conns: [Conn] = (0..<8).map { (i: Int) -> Conn in
        let start = Int64(i) * part
        let end: Int64 = i == 7 ? size - 1 : start + part - 1
        let pos: Int64 = i == 0 ? end + 1 : start + Int64(Double(part) * done[i])
        return Conn(start: start, pos: pos, end: end)
    }
    let s = Store()
    s.items = [
        item("iso", "Windows11_Client_arm64_en-gb.iso", .downloading, size: size, got: 5_211_592_642,
             speed: 34_500_000, eta: 112, conns: conns),
        item("xip", "Xcode_27_beta.xip", .downloading, size: 4_100_000_000, got: 943_000_000, speed: 18_200_000, eta: 185),
        item("ubu", "ubuntu-26.04-desktop-arm64.iso", .queued, size: 6_200_000_000, got: 0),
        item("fin", "Windows11_Client_arm64_en-gb.iso", .done, size: size, got: size),
    ]
    s.queues = [Queue(id: "main", name: "Ana kuyruk", atOnce: 2, retries: 3, items: OrEmpty(wrappedValue: ["ubu"]), running: false,
                      schedule: Schedule(startOn: true, start: "02:00", stopOn: false, stop: "", days: OrEmpty(wrappedValue: [1]), onDone: "none"))]
    s.settings = Settings(maxConns: 8, downloadDir: NSHomeDirectory() + "/Downloads", categoryFolders: true,
                          duplicate: "ask", speedLimitOn: true, speedLimitKBps: 5000, categories: OrEmpty(), captureExts: OrEmpty(),
                          excludedSites: OrEmpty(), logins: OrEmpty())
    s.connected = true
    return s
}

/// Lays the view out in an offscreen window and snapshots it. Glass is not composited this way (no window server backdrop).
@MainActor
private func render(_ name: String, dark: Bool, _ view: some View) throws {
    _ = NSApplication.shared
    let host = NSHostingView(rootView: view.environment(sampleStore()).environment(MainRoute()).background(Color(nsColor: .windowBackgroundColor)))
    let size = host.fittingSize
    #expect(size.width > 100 && size.height > 100, "\(name) has no size")
    let win = NSWindow(contentRect: CGRect(origin: .zero, size: size), styleMask: [.borderless], backing: .buffered, defer: false)
    win.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
    win.contentView = host
    host.layoutSubtreeIfNeeded()
    RunLoop.main.run(until: .now.addingTimeInterval(0.3))  // let Table / AppKit-backed controls draw
    let rep = try #require(host.bitmapImageRepForCachingDisplay(in: host.bounds))
    host.cacheDisplay(in: host.bounds, to: rep)
    let png = try #require(rep.representation(using: .png, properties: [:]))
    #expect(png.count > 1000)
    if let dir = ProcessInfo.processInfo.environment["DZ2_SHOTS"] {
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)\(dark ? "-dark" : "").png"))
    }
}

@MainActor @Suite(.serialized) struct ScreenShotTests {
    @Test(arguments: [false, true]) func progress(dark: Bool) throws {
        try render("Progress", dark: dark, ProgressWindow(id: "iso"))
    }
    @Test func progressTabs() throws {
        try render("ProgressSpeed", dark: false, ProgressWindow(id: "iso", tab: .limit))
        try render("ProgressDone", dark: false, ProgressWindow(id: "iso", tab: .finish))
    }
    @Test(arguments: [false, true]) func complete(dark: Bool) throws {
        try render("Complete", dark: dark, ProgressWindow(id: "fin"))
    }
    @Test(arguments: [false, true]) func info(dark: Bool) throws {
        try render("Info", dark: dark, InfoPanel(item: sampleStore().items[0]).frame(width: 440, height: 720))
    }
    @Test(arguments: [false, true]) func speedPopover(dark: Bool) throws {
        try render("SpeedPopover", dark: dark, SpeedPopover())
    }
    @Test(arguments: [false, true]) func menuExtra(dark: Bool) throws {
        try render("MenuExtra", dark: dark, MenuExtraView())
        try render("MenuBar", dark: dark, MenuExtraLabel(store: sampleStore()).padding(8).frame(minWidth: 120, minHeight: 120))
    }
}
