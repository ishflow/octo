import AppKit
import ServiceManagement
import SwiftUI

/// First-run sheet (one page, three steps). Stored in UserDefaults "onboardingDone".
struct OnboardingHost: ViewModifier {
    @AppStorage("onboardingDone") private var done = false

    func body(content: Content) -> some View {
        content.sheet(isPresented: Binding(get: { !done }, set: { _ in })) {
            OnboardingView { done = true }.interactiveDismissDisabled()
        }
    }
}

enum Onboarding {
    /// "/Users/<name>/Downloads" reads as "~/Downloads" (shorter, and no user name in shared screenshots).
    static func shortPath(_ p: String) -> String { (p as NSString).abbreviatingWithTildeInPath }
}

struct OnboardingView: View {
    // Optional: App.swift attaches the store inside this modifier; without it the folder row is read-only.
    @Environment(Store.self) private var store: Store?
    @AppStorage("showMenuBarIcon") private var showMenuBarIcon = true
    @State private var loginOn = SMAppService.mainApp.status == .enabled
    @State private var output: String?
    @State private var installing = false
    @State private var installed = false
    @State private var pickingFolder = false
    let finish: () -> Void

    private var extensionDir: URL? { Bundle.main.url(forResource: "ChromeExtension", withExtension: nil) }

    var body: some View {
        VStack(spacing: 0) {
            VStack(spacing: 6) {
                Image(nsImage: NSApplication.shared.applicationIconImage).resizable().frame(width: 76, height: 76)
                Text("Octo'ya hoş geldiniz").font(.system(size: 24, weight: .bold)).padding(.top, 10)
                Text("Dosyaları tek bağlantı yerine \(store?.settings?.maxConns ?? 8) bağlantıyla, parça parça indirir. Başlamak için üç kısa adım.")
                    .foregroundStyle(.secondary).multilineTextAlignment(.center).frame(maxWidth: 460)
            }
            .padding(.top, 28)
            Form {
                Section {
                    step(1, "Tarayıcıdaki indirmeleri yakala", "Bağlantıya tıkladığınızda dosya Octo'ya gelir; ⌥ basılıysa tarayıcı indirir.")
                    LabeledContent("Chrome, Arc, Brave, Edge") {
                        HStack {
                            if installed { Label("Kuruldu", systemImage: "checkmark").foregroundStyle(.green) }
                            Button(installing ? "Kuruluyor…" : "Eklentiyi kur…", action: install)
                                .buttonStyle(.glassProminent).disabled(installing)
                        }
                    }
                    .padding(.leading, 36)
                    if let output {
                        Text(output).font(.callout).foregroundStyle(.secondary).textSelection(.enabled).padding(.leading, 36)
                    }
                }
                Section {
                    LabeledContent {
                        HStack {
                            Text(Onboarding.shortPath(store?.settings?.downloadDir ?? "~/Downloads")).font(.callout)
                            Button("Değiştir…") { pickingFolder = true }.buttonStyle(.glass).disabled(store?.settings == nil)
                        }
                    } label: {
                        step(2, "İndirme klasörü", "Dosyalar bu klasöre iner. macOS klasör erişimi için izin sorarsa “İzin ver” deyin. Türe göre alt klasör için Ayarlar › Kaydetme'ye bakın.")
                    }
                }
                Section {
                    step(3, "Arka planda çalışma", nil)
                    Toggle(isOn: $loginOn) {
                        Text("Oturum açınca başlat")
                        Text("Zamanlanmış kuyruklar için gerekir.")
                    }
                    .padding(.leading, 36)
                    .onChange(of: loginOn) { setLogin(loginOn) }
                    Toggle("Menü çubuğunda göster", isOn: $showMenuBarIcon).padding(.leading, 36)
                }
            }
            .formStyle(.grouped)
            .scrollContentBackground(.hidden)
            HStack {
                Button("Atla", action: leave).buttonStyle(.glass)
                Spacer()
                Button("Başla", action: leave).keyboardShortcut(.defaultAction).buttonStyle(.glassProminent)
            }
            .padding(.horizontal, 40).padding(.bottom, 24)
        }
        .frame(width: 700, height: 660)
        .fileImporter(isPresented: $pickingFolder, allowedContentTypes: [.folder]) { r in
            if case .success(let u) = r {
                Task { await store?.update { $0.downloadDir = (u.path as NSString).abbreviatingWithTildeInPath } }
            }
        }
    }

    /// Closing the first run is the calm moment for macOS's folder prompt: touch the folder now, off the main thread (it waits for the answer).
    private func leave() {
        let dir = store?.settings?.downloadDir ?? "~/Downloads"
        Task.detached { _ = DownloadsAccess.touch(dir) }
        finish()
    }

    private func step(_ n: Int, _ title: String, _ sub: String?) -> some View {
        HStack(alignment: .top, spacing: 12) {
            Text("\(n)").font(.caption.weight(.semibold)).foregroundStyle(.white)
                .frame(width: 24, height: 24).background(.tint, in: .circle)
                .accessibilityLabel("Adım \(n)")
            VStack(alignment: .leading, spacing: 2) {
                Text(title).fontWeight(.semibold)
                if let sub { Text(sub).font(.caption).foregroundStyle(.secondary) }
            }
        }
    }

    private func setLogin(_ on: Bool) {
        guard on != (SMAppService.mainApp.status == .enabled) else { return }
        do {
            if on { try SMAppService.mainApp.register() } else { try SMAppService.mainApp.unregister() }
        } catch {
            output = "Oturum açınca başlatma ayarlanamadı: \(error.localizedDescription)"
            loginOn = SMAppService.mainApp.status == .enabled
        }
    }

    /// Native host first, then the manual part Chrome requires: reveal the folder and copy chrome://extensions.
    private func install() {
        installing = true
        Task {
            let r = await HostInstaller.installBundled()
            installing = false
            installed = r.ok
            guard r.ok else { output = r.output.isEmpty ? "Yerel köprü kurulamadı." : r.output; return }
            if let d = extensionDir { NSWorkspace.shared.activateFileViewerSelecting([d]) }
            NSPasteboard.general.clearContents()
            NSPasteboard.general.setString("chrome://extensions", forType: .string)
            output = "Son adım tarayıcıda: adres çubuğuna yapıştırın (chrome://extensions panoda), sağ üstten “Geliştirici modu”nu açın, “Paketlenmemiş öğe yükle” ile Finder'da seçili ChromeExtension klasörünü seçin."
        }
    }
}
