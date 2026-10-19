import AppKit
import SwiftUI

/// Total progress of the running downloads, for the Dock icon (pure, unit-tested).
struct DockProgress: Equatable {
    /// 0...1 over the downloads whose size is known; 0 while none of them knows it.
    var fraction: Double
    /// Downloads running now (the badge).
    var count: Int

    /// nil when nothing is downloading: the Dock icon is left alone.
    static func make(_ items: [Item]) -> DockProgress? {
        let active = items.filter { $0.state == .downloading }
        guard !active.isEmpty else { return nil }
        let sized = active.filter { $0.size > 0 }
        let size = sized.reduce(Int64(0)) { $0 + $1.size }
        let got = sized.reduce(Int64(0)) { $0 + min(max($1.got, 0), $1.size) }
        // Whole percents: the tile is redrawn only when the picture would change.
        let f = size > 0 ? (Double(got) / Double(size) * 100).rounded(.down) / 100 : 0
        return DockProgress(fraction: f, count: active.count)
    }
}

/// App icon with a thin brand-colored bar along its lower edge.
private struct DockTileView: View {
    let fraction: Double
    var body: some View {
        ZStack(alignment: .bottom) {
            Image(nsImage: NSApp.applicationIconImage).resizable()
            GeometryReader { g in
                let w = g.size.width * 0.8
                ZStack(alignment: .leading) {
                    Capsule().fill(.black.opacity(0.35))
                    Capsule().fill(Color.brand).frame(width: w * fraction)
                }
                .frame(width: w, height: g.size.height * 0.07)
                .position(x: g.size.width / 2, y: g.size.height * 0.93)
            }
        }
    }
}

/// Keeps the Dock tile in step with the Store: bar and running-download badge while downloading, untouched otherwise.
@MainActor
final class DockBadge {
    private let store: Store
    private let tile = NSApplication.shared.dockTile
    private var host: NSHostingView<DockTileView>?
    private var shown: DockProgress?

    init(store: Store) {
        self.store = store
        track()
    }

    private func track() {
        withObservationTracking { _ = DockProgress.make(store.items) } onChange: {
            Task { @MainActor in self.sync(); self.track() }
        }
    }

    private func sync() {
        let now = DockProgress.make(store.items)
        guard now != shown else { return }
        shown = now
        if let now {
            let view = DockTileView(fraction: now.fraction)
            if let host { host.rootView = view } else {
                let h = NSHostingView(rootView: view)
                h.frame = NSRect(origin: .zero, size: tile.size)
                host = h
                tile.contentView = h
            }
            tile.badgeLabel = String(now.count)
        } else {
            tile.contentView = nil
            tile.badgeLabel = nil
            host = nil
        }
        tile.display()
    }
}

/// The standard About panel's options (icon is the app's own).
enum AboutInfo {
    static func options(_ info: [String: Any]? = Bundle.main.infoDictionary) -> [NSApplication.AboutPanelOptionKey: Any] {
        [
            .applicationName: "Octo",
            .applicationVersion: info?["CFBundleShortVersionString"] as? String ?? "",
            .version: info?["CFBundleVersion"] as? String ?? "",
            .credits: NSAttributedString(string: "Mac için hızlı indirme yöneticisi",
                                         attributes: [.font: NSFont.systemFont(ofSize: 11), .foregroundColor: NSColor.secondaryLabelColor,
                                                      .paragraphStyle: centered()]),
            NSApplication.AboutPanelOptionKey(rawValue: "Copyright"): "© 2026 Uba",
        ]
    }

    private static func centered() -> NSParagraphStyle {
        let p = NSMutableParagraphStyle()
        p.alignment = .center
        return p
    }
}
