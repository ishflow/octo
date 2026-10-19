import AppKit
import SwiftUI

/// Shared offscreen-snapshot helper: light + dark, @2x, PNGs go to `$SHOTS_DIR` (`TEST_RUNNER_SHOTS_DIR=… xcodebuild test`).
/// Nothing is written when it is unset; the views are still laid out and checked, so the suite doubles as a smoke test.
/// Windows are never ordered in. Glass is not composited offscreen (no window server backdrop), so it renders flat.
enum Snapshot {
    struct Failure: Error, CustomStringConvertible { let description: String }

    static let scale = 2
    static var dir: URL? { ProcessInfo.processInfo.environment["SHOTS_DIR"].map { URL(fileURLWithPath: $0) } }

    static func appearance(dark: Bool) -> NSAppearance { NSAppearance(named: dark ? .darkAqua : .aqua)! }

    /// Renders `view` at `scale`x into PNG data; writes `<name>-light|dark.png` when `$SHOTS_DIR` is set.
    @MainActor static func save(_ name: String, dark: Bool, _ view: NSView) throws {
        let b = view.bounds
        guard b.width > 50, b.height > 50 else { throw Failure(description: "\(name): no size \(b.size)") }
        guard let rep = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: Int(b.width) * scale, pixelsHigh: Int(b.height) * scale,
                                         bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true, isPlanar: false,
                                         colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
        else { throw Failure(description: "\(name): no bitmap") }
        rep.size = b.size  // pixels > points makes cacheDisplay draw at @2x
        view.cacheDisplay(in: b, to: rep)
        guard let png = rep.representation(using: .png, properties: [:]), png.count > 1000 else { throw Failure(description: "\(name): empty PNG") }
        guard let dir else { return }
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        try png.write(to: dir.appendingPathComponent("\(name)-\(dark ? "dark" : "light").png"))
    }

    /// A SwiftUI view in an offscreen window, in both appearances. `size` nil = the view's fitting size.
    @MainActor static func shoot(_ name: String, size: CGSize? = nil, store: Store, _ view: some View) throws {
        _ = NSApplication.shared
        for dark in [false, true] {
            let root = view.environment(store).environment(MainRoute()).background(Color(nsColor: .windowBackgroundColor))
            let host = NSHostingView(rootView: size.map { AnyView(root.frame(width: $0.width, height: $0.height)) } ?? AnyView(root))
            host.frame.size = host.fittingSize
            let win = NSWindow(contentRect: host.frame, styleMask: [.titled, .fullSizeContentView], backing: .buffered, defer: false)
            win.isReleasedWhenClosed = false
            win.appearance = appearance(dark: dark)
            win.contentView = host
            host.layoutSubtreeIfNeeded()
            RunLoop.main.run(until: .now.addingTimeInterval(0.4))  // let Table / .task / AppKit-backed controls settle
            host.layoutSubtreeIfNeeded()
            try save(name, dark: dark, host)
            win.close()
        }
    }

    /// The full main window including title bar and toolbar.
    @MainActor static func shootWindow(_ name: String, size: CGSize, store: Store, _ view: some View) throws {
        _ = NSApplication.shared
        for dark in [false, true] {
            let win = NSWindow(contentRect: CGRect(origin: .zero, size: size),
                               styleMask: [.titled, .closable, .miniaturizable, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
            win.isReleasedWhenClosed = false
            win.appearance = appearance(dark: dark)
            win.contentViewController = NSHostingController(rootView: view.environment(store).environment(MainRoute()).tint(.brand))
            win.setContentSize(size)
            RunLoop.main.run(until: .now.addingTimeInterval(1.5))
            guard let frame = win.contentView?.superview else { throw Failure(description: "\(name): no frame view") }
            frame.layoutSubtreeIfNeeded()
            win.display()
            RunLoop.main.run(until: .now.addingTimeInterval(0.5))
            try save(name, dark: dark, frame)
            win.close()
        }
    }

    /// An `NSAlert` (duplicate / delete prompts). shortcut: offscreen its effect view paints white, so the dark PNG shows white text on white.
    @MainActor static func shoot(_ name: String, alert: NSAlert) throws {
        _ = NSApplication.shared
        for dark in [false, true] {
            alert.window.appearance = appearance(dark: dark)
            alert.layout()
            guard let content = alert.window.contentView else { throw Failure(description: "\(name): no alert view") }
            try save(name, dark: dark, content)
        }
    }
}
