import AppKit
import SwiftUI
import UniformTypeIdentifiers

// Plain-text URL list import/export (one http(s) URL per line); no proprietary file format.

enum URLList {
    /// Valid http(s) URLs in file order. Blank and `#` lines are ignored silently; invalid lines and repeats count as `skipped`.
    static func parse(_ text: String) -> (urls: [String], skipped: Int) {
        var urls: [String] = [], seen = Set<String>(), skipped = 0
        for line in text.split(whereSeparator: \.isNewline) {
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.isEmpty || t.hasPrefix("#") { continue }
            guard let u = URLCheck.normalized(t), seen.insert(u).inserted else { skipped += 1; continue }
            urls.append(u)
        }
        return (urls, skipped)
    }

    static func text(_ urls: [String]) -> String { urls.map { $0 + "\n" }.joined() }

    static func write(_ urls: [String], to file: URL) throws {
        try text(urls).write(to: file, atomically: true, encoding: .utf8)
    }

    static func read(_ file: URL) throws -> String { try String(contentsOf: file, encoding: .utf8) }
}

/// Carries a parsed import from the Dosya menu to the main window, which shows the sheet.
@MainActor @Observable final class ListIORoute {
    struct Pending: Identifiable {
        let id = UUID()
        let urls: [String]
        let skipped: Int
    }
    var pending: Pending?
}

@MainActor enum ListPanels {
    /// Returns false when the user cancelled.
    static func importList(into route: ListIORoute) -> Bool {
        let panel = NSOpenPanel()
        panel.message = "Satır başına bir adres içeren metin dosyasını seçin."
        panel.prompt = "İçe aktar"
        panel.allowedContentTypes = [.plainText]
        guard panel.runModal() == .OK, let file = panel.url else { return false }
        do {
            let r = URLList.parse(try URLList.read(file))
            route.pending = .init(urls: r.urls, skipped: r.skipped)
        } catch {
            let a = NSAlert()
            a.messageText = "Dosya okunamadı"
            a.informativeText = "“\(file.lastPathComponent)” UTF-8 metin dosyası olmalı."
            a.runModal()
            return false
        }
        return true
    }

    static func export(_ urls: [String]) {
        let panel = NSSavePanel()
        panel.message = "Seçili indirmelerin adresleri satır başına bir adres olarak kaydedilir."
        panel.prompt = "Dışa aktar"
        panel.nameFieldStringValue = "Octo adresleri.txt"
        panel.allowedContentTypes = [.plainText]
        guard panel.runModal() == .OK, let file = panel.url else { return }
        do { try URLList.write(urls, to: file) } catch {
            let a = NSAlert(error: error)
            a.messageText = "Adresler kaydedilemedi"
            a.runModal()
        }
    }
}

struct ListIOHost: ViewModifier {
    @Bindable var route: ListIORoute
    func body(content: Content) -> some View {
        content.environment(route).sheet(item: $route.pending) { ImportListSheet(urls: $0.urls, skipped: $0.skipped) }  // env: the main window drops several links here
    }
}

struct ImportListSheet: View {
    @Environment(Store.self) private var store
    @Environment(\.dismiss) private var dismiss
    let urls: [String]
    let skipped: Int
    @State private var error: String?
    @State private var done = 0
    @State private var busy = false
    @State private var job: Task<Void, Never>?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("URL listesini içe aktar").font(.headline)
            Text(urls.isEmpty ? "Dosyada geçerli bir http veya https adresi yok." : "\(urls.count) adres bulundu."
                 + (skipped > 0 ? " \(skipped) satır geçersiz ya da tekrar olduğu için atlandı." : ""))
                .foregroundStyle(.secondary).padding(.top, 4).padding(.bottom, 12)
            if !urls.isEmpty {
                List(urls, id: \.self) { Text($0).font(.system(size: 12, design: .monospaced)).lineLimit(1).truncationMode(.middle) }
                    .frame(height: 180)
            }
            if let error { Text(error).foregroundStyle(.red).padding(.top, 10) }
            HStack(spacing: 8) {
                Button("İptal", role: .cancel) { dismiss() }.keyboardShortcut(.cancelAction).buttonStyle(.glass)
                Spacer()
                if busy { ProgressView(value: Double(done), total: Double(urls.count)).frame(width: 120)
                    .accessibilityLabel("Eklenen dosya").accessibilityValue("\(done) / \(urls.count)") }
                Button("Yalnız kuyruğa ekle") { addAll(start: false) }
                    .buttonStyle(.glass).disabled(urls.isEmpty || store.queues.isEmpty || busy)
                Button("\(urls.count) dosyayı kuyruğa ekle ve başlat") { addAll(start: true) }
                    .keyboardShortcut(.defaultAction).buttonStyle(.glassProminent).disabled(urls.isEmpty || store.queues.isEmpty || busy)
            }
            .padding(.top, 22)
        }
        .padding(22)
        .frame(width: 620)
        .onDisappear { job?.cancel() }  // İptal / Esc must stop the loop, not only hide the sheet
    }

    private func addAll(start: Bool) {
        busy = true
        error = nil
        let entries = BatchAdd.entries(urls, cats: store.settings?.categories ?? [], baseDir: store.settings?.downloadDir ?? "~/Downloads",
                                       oneFolder: false, categoryFolders: store.settings?.categoryFolders ?? false)
        let queue = (store.queues.first { $0.id == "main" } ?? store.queues.first)?.id ?? ""
        job = Task {
            let r = await BatchAdd.addToQueue(entries, done: done, startQueue: start, progress: { done = $0 }, add: {
                try await store.add(url: $0.url, dir: $0.dir, category: $0.category, queue: queue, start: false)
            }, start: { await store.startQueue(queue) })
            if Task.isCancelled { return }
            if let err = r.error {
                self.error = "\(done) dosya eklendi, sonra hata: \(err.localizedDescription)"
                busy = false
                return
            }
            dismiss()
        }
    }
}
