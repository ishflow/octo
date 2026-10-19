import AppKit
import SwiftUI

/// Menu-bar glyph: the Octo silhouette (template image) and, while downloading, the total speed.
struct MenuExtraLabel: View {
    let store: Store
    var body: some View {
        let s = ActiveSummary.make(store.items)
        HStack(spacing: 4) {
            Image("MenuBarIcon")
            if s.count > 0 { Text(Format.speed(s.totalSpeed, none: "—")).monospacedDigit() }
        }
        .accessibilityLabel("Octo")
    }
}

struct MenuExtraView: View {
    @Environment(Store.self) private var store
    @Environment(MainRoute.self) private var route
    @Environment(ListIORoute.self) private var listIO: ListIORoute?
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings
    @State private var addError: String?

    /// Downloading first, then queued.
    private var rows: [Item] {
        store.items.filter { $0.state == .downloading } + store.items.filter { $0.state == .queued }
    }

    var body: some View {
        let s = ActiveSummary.make(store.items)
        let rows = rows
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(nsImage: NSApplication.shared.applicationIconImage).resizable().frame(width: 28, height: 28)
                VStack(alignment: .leading, spacing: 1) {
                    Text("Octo").font(.headline)
                    Text(subtitle(s)).font(.caption).foregroundStyle(.secondary).monospacedDigit()
                }
                Spacer()
                Button("Tümünü durdur", systemImage: "pause.fill") { Task { await store.stopAll() } }
                    .labelStyle(.iconOnly).buttonStyle(.glass).buttonBorderShape(.circle)
                    .disabled(rows.isEmpty).help("Tümünü durdur")
                Button("URL ekle", systemImage: "plus") {
                    route.requestAdd()
                    openWindow(id: "main")
                    NSApp.activate()
                }
                .labelStyle(.iconOnly).buttonStyle(.glass).buttonBorderShape(.circle).help("URL ekle")
            }
            .padding(.horizontal, 10).padding(.top, 4).padding(.bottom, 8)

            if rows.isEmpty {
                Text(store.connected ? "Etkin indirme yok" : "Motora bağlanılıyor…")
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, minHeight: 56)
            } else {
                ForEach(Array(rows.prefix(6).enumerated()), id: \.element.id) { n, i in row(i, highlighted: n == 0) }
                if rows.count > 6 {
                    Text("+\(rows.count - 6) indirme daha").font(.caption).foregroundStyle(.secondary).padding(.horizontal, 10)
                }
            }

            Divider().padding(.vertical, 6).padding(.horizontal, 10)
            if let st = store.settings {
                HStack {
                    Text("Hız sınırı")
                    Spacer()
                    Text(Format.speed(SpeedLimit.bytesPerSecond(kbps: st.speedLimitKBps), none: "—")).font(.callout).foregroundStyle(.secondary)
                    Toggle("Hız sınırı", isOn: Binding(get: { st.speedLimitOn },
                                                      set: { on in Task { await store.update { $0.speedLimitOn = on } } }))
                        .toggleStyle(.switch).controlSize(.mini).labelsHidden()
                }
                .padding(.horizontal, 10).frame(height: 26)
            }
            MenuRow("Panodaki bağlantıyı indir", key: "⌘V", action: addClipboard).keyboardShortcut("v")
            if let addError { Text(addError).font(.caption).foregroundStyle(.red).padding(.horizontal, 10) }
            Divider().padding(.vertical, 6).padding(.horizontal, 10)
            MenuRow("Octo penceresini aç") {
                openWindow(id: "main")
                NSApp.activate()
            }
            MenuRow("Ayarlar…", key: "⌘,") {
                openSettings()
                NSApp.activate()
            }
            MenuRow("Çık", key: "⌘Q") { NSApp.terminate(nil) }
        }
        .padding(10)
        .frame(width: 340)
        .dropDestination(for: DroppedLinks.self) { drops, _ in
            let ok = DropLinks.handle(drops, single: { route.requestAdd(url: $0) }, multiple: { listIO?.pending = .init(urls: $0, skipped: 0) })
            if ok {
                openWindow(id: "main")
                NSApp.activate()
            }
            return ok
        }
        .tint(brandAccent)
    }

    private func subtitle(_ s: ActiveSummary) -> String {
        guard s.count > 0 else { return store.connected ? "Etkin indirme yok" : "Bağlanılıyor…" }
        return "\(s.count) indiriliyor · \(Format.speed(s.totalSpeed, none: "—"))"
    }

    private func row(_ i: Item, highlighted: Bool) -> some View {
        Button { openWindow(id: "progress", value: i.id) } label: {
            HStack(spacing: 10) {
                Image(nsImage: FileIcon.image(i)).resizable().frame(width: 24, height: 24)
                VStack(alignment: .leading, spacing: 4) {
                    Text(i.name).font(.callout).lineLimit(1).truncationMode(.middle)
                    if i.state == .downloading {
                        ThickBar(fraction: i.fraction, height: 5, detail: i.size > 0 ? "\(Format.bytes(i.got)) / \(Format.bytes(i.size))" : nil)
                        Text(progressLine(i)).font(.caption2).foregroundStyle(.secondary).monospacedDigit()
                    } else {
                        Text(queuedLine(i)).font(.caption2).foregroundStyle(.secondary)
                    }
                }
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .background(highlighted ? AnyShapeStyle(.fill.quaternary) : AnyShapeStyle(.clear), in: .rect(cornerRadius: 12))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .help("İndirme penceresini aç")
    }

    private func progressLine(_ i: Item) -> String {
        var parts = [i.size > 0 ? "\(Format.bytes(i.got)) / \(Format.bytes(i.size))" : Format.bytes(i.got),
                     Format.speed(i.speed, none: "—")]
        if i.eta >= 0 { parts.append(Format.eta(i.eta)) }
        return parts.joined(separator: " · ")
    }

    private func queuedLine(_ i: Item) -> String {
        guard let q = store.queues.first(where: { $0.items.contains(i.id) }) else { return "Kuyrukta" }
        return q.schedule.startOn ? "\(q.name) · başlama \(q.schedule.start)" : "\(q.name) · kuyrukta"
    }

    private func addClipboard() {
        guard let url = URLCheck.clipboardURL(NSPasteboard.general.string(forType: .string)) else {
            addError = "Panoda indirilebilir bir bağlantı yok."
            return
        }
        addError = nil
        Task {
            do { try await store.add(url: url, start: true) } catch { addError = error.localizedDescription }
        }
    }
}

/// Full-width menu-like row with hover highlight and a shortcut hint.
private struct MenuRow: View {
    let title: String
    var key = ""
    let action: () -> Void
    @State private var hover = false

    init(_ title: String, key: String = "", action: @escaping () -> Void) {
        self.title = title
        self.key = key
        self.action = action
    }

    var body: some View {
        Button(action: action) {
            HStack {
                Text(title)
                Spacer()
                Text(key).foregroundStyle(.secondary)
            }
            .padding(.horizontal, 10)
            .frame(height: 26)
            .background(hover ? AnyShapeStyle(.fill.tertiary) : AnyShapeStyle(.clear), in: .rect(cornerRadius: 8))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .onHover { hover = $0 }
    }
}
