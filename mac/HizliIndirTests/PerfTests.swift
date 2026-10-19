import AppKit
import SwiftUI
import Testing

// Cost of a 1,000-item list while 8 downloads push progress (~5 events/s, 8 updates each).
// Timings print as "PERF ..." lines; the bounds are loose so a slow CI box does not fail them.

@MainActor private func bigStore() -> Store {
    let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
    store.connected = true
    let cats = ["Programlar", "Arşiv", "Video", "Müzik", "Belgeler"]
    store.items = (0..<1000).map { n in
        // Newest (active) downloads sit at the end, where a linear id lookup is slowest.
        let state: ItemState = n >= 992 ? .downloading : n >= 960 ? .queued : n >= 940 ? .paused : .done
        return Item(id: "i\(n)", url: "https://example.com/f\(n).zip", name: "dosya-\(n).zip", dir: "/tmp",
                    category: cats[n % cats.count], queue: "main", state: state, size: 1_000_000, got: state == .done ? 1_000_000 : 0,
                    speed: 0, eta: -1, resumable: true, error: "", description: "", added: String(format: "%06d", n),
                    finished: nil, lastTry: "2026-10-1\(n % 10)T12:00:00Z", maxConns: 8, conns: OrEmpty())
    }
    return store
}

private func progress(_ tick: Int) -> EngineEvent {
    .progress((0..<8).map { k in
        ProgressUpdate(id: "i\(992 + k)", state: .downloading, got: Int64(tick * 1000 + k), size: 1_000_000, speed: 1_000_000 + Int64(k),
                       eta: 100 - tick % 100, conns: OrEmpty(wrappedValue: [Conn(start: 0, pos: Int64(tick), end: 999_999)]))
    })
}

private func micros(_ d: Duration) -> Double { Double(d.components.seconds) * 1e6 + Double(d.components.attoseconds) / 1e12 }

@MainActor @Suite(.serialized) struct PerfTests {
    @Test func storeProgressEvent() {
        let store = bigStore()
        let n = 500
        let t = ContinuousClock().measure { for i in 0..<n { store.apply(progress(i)) } }
        print(String(format: "PERF store.apply(progress×8), 1000 öğe: %.1f µs/olay", micros(t) / Double(n)))
        #expect(store.items[995].got == Int64((n - 1) * 1000 + 3))
        #expect(micros(t) / Double(n) < 250)  // linear id lookup: ~800 µs (debug build)
    }

    /// The id→index cache must follow removals, appends and direct `items` writes.
    @Test func progressFindsItemAfterListChanges() {
        let store = bigStore()
        store.apply(progress(1))
        store.apply(.removed(["i0", "i500"]))
        store.apply(progress(2))
        #expect(store.items.first { $0.id == "i995" }?.got == 2003)
        store.items.insert(store.items.removeLast(), at: 0)  // i999 now first
        store.apply(progress(3))
        #expect(store.items[0].got == 3007)
        var late = store.items[1]
        late.id = "new"
        store.apply(.item(late))
        store.apply(.progress([ProgressUpdate(id: "new", state: .paused, got: 9, size: 10, speed: 0, eta: -1, conns: OrEmpty())]))
        #expect(store.items.last?.got == 9)
        #expect(store.items.count == 999)
    }

    /// Whole main window (sidebar badges, subtitle, filter/sort, table) redrawn after each event.
    @Test func mainWindowRedrawPerEvent() throws {
        _ = NSApplication.shared
        UserDefaults.standard.set(true, forKey: "onboardingDone")
        let store = bigStore()
        let root = MainWindow().environment(store).environment(MainRoute())
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1200, height: 728),
                              styleMask: [.titled, .resizable, .fullSizeContentView], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentViewController = NSHostingController(rootView: root)
        defer { window.close() }
        RunLoop.main.run(until: Date().addingTimeInterval(1))
        let view = try #require(window.contentView)
        // Best of 5 rounds: other processes on the machine only ever add time.
        let n = 20
        var tick = 0
        let rounds = (0..<5).map { _ in
            ContinuousClock().measure {
                for _ in 0..<n {
                    tick += 1
                    store.apply(progress(tick))
                    RunLoop.main.run(until: Date())  // let SwiftUI pick up the change
                    view.layoutSubtreeIfNeeded()
                    window.displayIfNeeded()
                }
            }
        }
        let t = rounds.min()!
        print(String(format: "PERF ana pencere güncellemesi, 1000 öğe: %.2f ms/olay (en iyi tur)", micros(t) / Double(n) / 1000))
        #expect(micros(t) / Double(n) < 200_000)
    }
}

@Suite struct ListCostTests {
    /// The "Son deneme" column formats every visible row on each redraw; building formatters per call cost ~450 µs.
    @Test func dateFormatIsCheap() {
        let n = 200
        var len = 0
        let t = ContinuousClock().measure { for i in 0..<n { len += Format.date("2026-10-1\(i % 10)T12:00:00.5Z").count } }
        print(String(format: "PERF Format.date: %.1f µs/çağrı", micros(t) / Double(n)))
        #expect(len > 0)
        #expect(micros(t) / Double(n) < 100)
    }

    @Test func sidebarCountsInOnePass() {
        func item(_ id: String, _ state: ItemState, _ cat: String, _ queue: String) -> Item {
            Item(id: id, url: "https://x/\(id)", name: id, dir: "/tmp", category: cat, queue: queue, state: state, size: 1, got: 0,
                 speed: 0, eta: -1, resumable: true, error: "", description: "", added: id, finished: nil, lastTry: nil, maxConns: 8,
                 conns: OrEmpty())
        }
        let c = SidebarCount.of([item("a", .done, "Video", "main"), item("b", .downloading, "Video", "q2"),
                                 item("c", .error, "", "main")])
        #expect(c[.all] == 3)
        #expect(c[.finished] == 1)
        #expect(c[.unfinished] == 2)
        #expect(c[.category("Video")] == 2)
        #expect(c[.category("Müzik")] == nil)
        #expect(c[.queue("main")] == 2)
        #expect(c[.queue("q2")] == 1)
        #expect(SidebarCount.of([])[.all] == 0)
    }
}
