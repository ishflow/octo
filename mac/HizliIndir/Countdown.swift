import AppKit
import SwiftUI

/// Floating glass window for the engine's sleep/shutdown countdown. Not a scene: it must appear even when the main window is closed.
@MainActor
final class CountdownPanel {
    private let store: Store
    private var panel: NSPanel?

    init(store: Store) {
        self.store = store
        track()
    }

    private func track() {
        withObservationTracking { _ = store.pendingAction } onChange: {
            Task { @MainActor in self.sync(); self.track() }
        }
    }

    private func sync() {
        guard store.pendingAction != nil else { panel?.close(); panel = nil; return }
        guard panel == nil else { return }
        let p = NSPanel(contentViewController: NSHostingController(rootView: CountdownView().environment(store)))
        p.styleMask = [.titled, .fullSizeContentView]
        p.titleVisibility = .hidden
        p.titlebarAppearsTransparent = true
        p.isMovableByWindowBackground = true
        p.level = .floating
        p.isOpaque = false
        p.backgroundColor = .clear
        p.center()
        p.makeKeyAndOrderFront(nil)
        NSApp.activate()
        panel = p
    }
}

struct CountdownView: View {
    @Environment(Store.self) private var store

    var body: some View {
        TimelineView(.periodic(from: .now, by: 0.25)) { ctx in
            if let a = store.pendingAction {
                CountdownBody(action: a, remaining: CountdownText.remaining(until: a.deadline, now: ctx.date))
            }
        }
    }
}

private struct CountdownBody: View {
    @Environment(Store.self) private var store
    let action: PendingAction
    let remaining: Int

    var body: some View {
        VStack(spacing: 14) {
            Image(systemName: action.kind == "sleep" ? "moon.zzz.fill" : "power").font(.system(size: 34)).foregroundStyle(.secondary)
                .accessibilityHidden(true)
            Text(CountdownText.text(action: action.kind, seconds: remaining)).font(.title3.weight(.semibold))
            Text("İndirmeler tamamlandı.").foregroundStyle(.secondary)
            Button("İptal") { Task { await store.cancelAction() } }
                .buttonStyle(.glassProminent).keyboardShortcut(.cancelAction)
        }
        .padding(28)
        .frame(width: 340)
        .accessibleGlass(cornerRadius: 28)
        .onChange(of: remaining) { _, r in if r == 0 { store.pendingAction = nil } }
    }
}

/// Liquid Glass surface; an opaque window-colored fill with a hairline under Reduce Transparency or Increase Contrast.
private struct AccessibleGlass: ViewModifier {
    @Environment(\.accessibilityReduceTransparency) private var reduceTransparency
    @Environment(\.colorSchemeContrast) private var contrast
    let cornerRadius: CGFloat

    func body(content: Content) -> some View {
        if reduceTransparency || contrast == .increased {
            let shape = RoundedRectangle(cornerRadius: cornerRadius)
            content.background(Color(nsColor: .windowBackgroundColor), in: shape).overlay(shape.strokeBorder(.separator))
        } else {
            content.glassEffect(.regular, in: .rect(cornerRadius: cornerRadius))
        }
    }
}

extension View {
    func accessibleGlass(cornerRadius: CGFloat) -> some View { modifier(AccessibleGlass(cornerRadius: cornerRadius)) }
}
