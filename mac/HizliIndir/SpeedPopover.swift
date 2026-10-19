import SwiftUI

/// Toolbar popover: speed limit switch, KB/s field and presets. Applies live through `settings.set`.
struct SpeedPopover: View {
    @Environment(Store.self) private var store
    @State private var text = ""
    @State private var error: String?

    private var total: Int64 { store.items.filter { $0.state == .downloading }.reduce(0) { $0 + $1.speed } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            if let s = store.settings {
                Toggle(isOn: Binding(get: { s.speedLimitOn }, set: { on in apply { $0.speedLimitOn = on } })) {
                    Text("Hız sınırı").font(.headline).frame(maxWidth: .infinity, alignment: .leading)
                }
                .toggleStyle(.switch)
                HStack(spacing: 8) {
                    Text("En fazla:").foregroundStyle(.secondary)
                    TextField("KB/s", text: $text).multilineTextAlignment(.trailing).frame(width: 90).onSubmit(commit)
                        .accessibilityLabel("En fazla hız, KB/s")
                    Text("KB/s").foregroundStyle(.secondary)
                }
                .disabled(!s.speedLimitOn)
                .padding(.top, 14)
                Picker("Hazır değerler", selection: Binding(
                    get: { s.speedLimitOn ? s.speedLimitKBps : 0 },
                    set: { k in apply { if k > 0 { $0.speedLimitOn = true; $0.speedLimitKBps = k } else { $0.speedLimitOn = false } } }
                )) {
                    // 0 = Sınırsız; a custom limit matches no segment.
                    ForEach(SpeedLimit.presets, id: \.label) { p in
                        Text(p.kbps.map { "\($0 / 1000) MB/s" } ?? p.label).tag(p.kbps ?? 0)
                    }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .padding(.top, 12)
                if let msg = error ?? store.settingsError {
                    Text(msg).font(.caption).foregroundStyle(.red).padding(.top, 8)
                }
                Text("Sınır tüm indirmelerin toplamına uygulanır; tek dosya için indirme penceresindeki Hız sınırı sekmesini kullanın. Çok düşük hızlarda bazı sunucular bağlantıyı kesebilir.")
                    .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    .padding(.top, 10)
                Divider().padding(.top, 12)
                HStack {
                    Text("Şu anki hız").foregroundStyle(.secondary)
                    Spacer()
                    Text(Format.speed(total, none: "—") + (s.speedLimitOn && total > 0 ? " (sınırlı)" : "")).monospacedDigit()
                }
                .font(.callout)
                .padding(.top, 10)
            } else {
                Text("Ayarlar motordan alınamadı.").foregroundStyle(.secondary)
            }
        }
        .padding(18)
        .frame(width: 300)
        .tint(brandAccent)
        .onAppear { text = store.settings.map { "\($0.speedLimitKBps)" } ?? "" }
        .onChange(of: store.settings?.speedLimitKBps) { text = store.settings.map { "\($0.speedLimitKBps)" } ?? text }
    }

    private func commit() {
        guard let n = SpeedLimit.parseKBps(text) else { error = SpeedLimit.rangeMessage; return }
        apply { $0.speedLimitKBps = n }
    }

    private func apply(_ change: @escaping (inout Settings) -> Void) {
        error = nil
        Task { await store.update(change) }
    }
}
