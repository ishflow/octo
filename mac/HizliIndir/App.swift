import SwiftUI

@main
struct HizliIndirApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var store: Store
    @State private var route = MainRoute()
    @State private var listIO = ListIORoute()
    private let countdown: CountdownPanel
    private let dock: DockBadge
    @AppStorage("showMenuBarIcon") private var showMenuBarIcon = true

    init() {
        // The run loop belongs to the app, not to a window: closing/reopening the window must not restart it.
        let s = Store()
        s.onFinished = { FinishActions.run($0) }
        s.confirmPause = { Alerts.confirmPause($0) }
        _store = State(initialValue: s)
        Task { await s.run() }
        // M5: notifications + countdown panel
        Notifier.shared.setup()
        let prevFinished = s.onFinished
        s.onFinished = { prevFinished?($0); Notifier.shared.post($0) }
        s.onFailed = { Notifier.shared.post($0) }
        s.onRefreshOffer = { Notifier.shared.postRefreshOffer($0) }
        countdown = CountdownPanel(store: s)
        dock = DockBadge(store: s)
    }

    var body: some Scene {
        Window("Octo", id: "main") {
            MainWindow().modifier(OnboardingHost()).modifier(OpenURLHost(route: route)).modifier(ListIOHost(route: listIO)).environment(store).environment(route).tint(.brand)  // onboarding sheet needs the store
        }
        .defaultSize(width: 1200, height: 728)  // first launch, as the design: every toolbar group visible, no overflow menu
        .commands { DownloadCommands(store: store, route: route, listIO: listIO) }
        Window("Zamanlayıcı", id: "scheduler") {
            SchedulerWindow().environment(store).tint(.brand)
        }
        SwiftUI.Settings {
            SettingsView().environment(store).tint(.brand)
        }
        WindowGroup("İndirme", id: "progress", for: String.self) { $id in
            if let id {
                // "<id>#limit" opens the window on the speed-limit tab (row menu "Hız sınırı…").
                let limit = id.hasSuffix("#limit")
                ProgressWindow(id: limit ? String(id.dropLast(6)) : id, tab: limit ? .limit : .status).environment(store).tint(.brand)
            }
        }
        .windowResizability(.contentSize)
        // M5: menu-bar extra
        MenuBarExtra(isInserted: $showMenuBarIcon) {
            MenuExtraView().environment(store).environment(route).environment(listIO).tint(.brand)
        } label: {
            MenuExtraLabel(store: store)
        }
        .menuBarExtraStyle(.window)
    }
}

/// A link dropped on the Dock icon or opened with `open -a Octo <url>` opens the add sheet with it filled in.
private struct OpenURLHost: ViewModifier {
    let route: MainRoute
    @Environment(\.openWindow) private var openWindow

    func body(content: Content) -> some View {
        content.onOpenURL { url in
            guard let link = URLCheck.normalized(url.absoluteString) else { return }
            route.requestAdd(url: link)
            openWindow(id: "main")
        }
    }
}

/// Dosya › Yeni indirme and the İndirmeler menu, as in the design's menu bar.
private struct DownloadCommands: Commands {
    let store: Store
    let route: MainRoute
    let listIO: ListIORoute
    @FocusedValue(\.mainActions) private var main
    @Environment(\.openWindow) private var openWindow
    @Environment(\.openSettings) private var openSettings

    var body: some Commands {
        CommandGroup(replacing: .appInfo) {
            Button("Octo Hakkında") { NSApp.orderFrontStandardAboutPanel(options: AboutInfo.options()) }
        }
        CommandGroup(replacing: .newItem) {
            Button("Yeni indirme") {
                route.requestAdd()
                openWindow(id: "main")
            }
            .keyboardShortcut("n")
            Divider()
            Button("URL listesini içe aktar…") {
                if ListPanels.importList(into: listIO) { openWindow(id: "main") }
            }
            Button("Seçili adresleri dışa aktar…") { ListPanels.export((main?.selected ?? []).map(\.url)) }
                .disabled(main?.selected.isEmpty ?? true)
        }
        CommandMenu("İndirmeler") {
            let sel = main?.selected ?? []
            Button("Devam et") { Task { await store.resume(sel.map(\.id)) } }
                .keyboardShortcut("r").disabled(!ItemActions.canResume(sel))
            Button("Durdur") { Task { await store.pause(sel.map(\.id)) } }
                .keyboardShortcut(".").disabled(!ItemActions.canPause(sel))
            Button("Tümünü durdur") { Task { await store.stopAll() } }
                .keyboardShortcut(".", modifiers: [.command, .option]).disabled(!ItemActions.canPause(store.items))
            Divider()
            Button("Bağlantıyı yenile…") { if let one = sel.first { main?.refresh(one) } }.disabled(sel.count != 1)
            Button("Taşı veya yeniden adlandır…") { if let one = sel.first { main?.move(one) } }
                .disabled(sel.count != 1 || sel.first?.state == .downloading)
            Button("Listeden kaldır…") { main?.delete(sel.map(\.id)) }.keyboardShortcut(.delete).disabled(sel.isEmpty)
            Button("Tamamlananları kaldır") {
                Task { await store.remove(store.items.filter { $0.state == .done }.map(\.id)) }  // list only: files stay
            }
            .disabled(!store.items.contains { $0.state == .done })
            Divider()
            Button("Bul…") { main?.find() }.keyboardShortcut("f").disabled(main == nil)
            Divider()
            Button("Zamanlayıcı…") { openWindow(id: "scheduler") }.keyboardShortcut("j")
            Menu("Kuyruğu başlat") {
                ForEach(store.queues) { q in Button(q.name) { Task { await store.startQueue(q.id) } }.disabled(q.running) }
            }
            .disabled(store.queues.isEmpty)
            Menu("Kuyruğu durdur") {
                ForEach(store.queues) { q in Button(q.name) { Task { await store.stopQueue(q.id) } }.disabled(!q.running) }
            }
            .disabled(store.queues.isEmpty)
            Divider()
            Menu("Hız sınırı") {
                Picker("Hız sınırı", selection: Binding(get: { store.settings?.speedLimitOn ?? false },
                                                        set: { on in Task { await store.update { $0.speedLimitOn = on } } })) {
                    Text("Açık").tag(true)
                    Text("Kapalı").tag(false)
                }
                .pickerStyle(.inline).labelsHidden()
                .disabled(store.settings == nil)
                Divider()
                Button("Ayarlar…") { openSettings() }
            }
        }
    }
}
