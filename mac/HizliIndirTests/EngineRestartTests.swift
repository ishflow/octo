import Foundation
import Testing

/// Scriptable in-test engine for restart / broken-socket cases. Unlike `FakeEngine` it can really go away and come back
/// on the same socket path (a daemon restart), records the commands it received and can hang up on a request.
final class ScriptedEngine: @unchecked Sendable {
    enum Reply { case lines([String]), hangUp, silent }

    let path = NSTemporaryDirectory() + "sc-\(UUID().uuidString.prefix(8)).sock"
    private let lock = NSLock()
    private var listenFD: Int32 = -1
    private var conns: [Int32] = []
    private var cmds: [String] = []
    private var lists = 0
    /// Overrides the default replies; return nil to fall back to them.
    var script: (@Sendable (_ cmd: String, _ id: Int) -> Reply?)?
    /// Item JSON served by `list`, and the optional `pendingAction` JSON (e.g. `{"action":"sleep","seconds":30}`).
    var listItems = [ScriptedEngine.item("d17", state: "downloading", got: 250)]
    var listPending: String?

    init() throws { try start() }

    var received: [String] { lock.lock(); defer { lock.unlock() }; return cmds }
    var listCount: Int { lock.lock(); defer { lock.unlock() }; return lists }

    static func item(_ id: String, state: String, got: Int) -> String {
        #"{"id":"\#(id)","url":"https://x/y.iso","name":"y.iso","dir":"/tmp","category":"","queue":"main","state":"\#(state)","size":1000,"got":\#(got),"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]}"#
    }

    func start() throws {
        let s = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { $0.copyBytes(from: path.utf8.prefix($0.count - 1)) }
        let ok = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard ok == 0, listen(s, 4) == 0 else { close(s); throw POSIXError(.EADDRINUSE) }
        lock.lock(); listenFD = s; lock.unlock()
        Thread.detachNewThread { [self] in
            while true {
                let c = accept(s, nil, nil)
                if c < 0 { return }
                lock.lock()
                // stop() ran between accept and here: it never saw `c`, so hang up on it or the client waits forever.
                if listenFD != s { lock.unlock(); close(c); return }
                conns.append(c); lock.unlock()
                Thread.detachNewThread { self.serve(c) }
            }
        }
    }

    /// The daemon exits: listener and every client connection go away, the socket file is removed.
    func stop() {
        lock.lock()
        if listenFD >= 0 { shutdown(listenFD, SHUT_RDWR); close(listenFD); listenFD = -1 }
        conns.forEach { shutdown($0, SHUT_RDWR) }
        conns = []
        lock.unlock()
        unlink(path)
    }

    /// The daemon crashed: the socket file stays behind but nobody listens on it.
    func stopLeavingSocketFile() {
        lock.lock()
        if listenFD >= 0 { shutdown(listenFD, SHUT_RDWR); close(listenFD); listenFD = -1 }
        conns.forEach { shutdown($0, SHUT_RDWR) }
        conns = []
        lock.unlock()
    }

    func push(_ line: String) { lock.lock(); conns.forEach { Self.send($0, line) }; lock.unlock() }

    private static func send(_ fd: Int32, _ s: String) { let b = Array((s + "\n").utf8); _ = write(fd, b, b.count) }

    private static let settings = #""settings":{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500}"#

    private func serve(_ fd: Int32) {
        var buf = Data()
        var chunk = [UInt8](repeating: 0, count: 4096)
        while true {
            let n = read(fd, &chunk, chunk.count)
            if n <= 0 { close(fd); return }
            buf.append(contentsOf: chunk[0..<n])
            while let nl = buf.firstIndex(of: 10) {
                let line = buf[buf.startIndex..<nl]
                buf.removeSubrange(buf.startIndex...nl)
                guard let req = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
                      let id = req["id"] as? Int, let cmd = req["cmd"] as? String else { continue }
                lock.lock(); cmds.append(cmd); if cmd == "list" { lists += 1 }; lock.unlock()
                switch script?(cmd, id) ?? defaultReply(cmd, id) {
                case .lines(let ls): ls.forEach { Self.send(fd, $0) }
                case .hangUp: shutdown(fd, SHUT_RDWR)
                case .silent: break
                }
            }
        }
    }

    private func defaultReply(_ cmd: String, _ id: Int) -> Reply {
        switch cmd {
        case "list":
            lock.lock(); let items = listItems.joined(separator: ","), pending = listPending; lock.unlock()
            let p = pending.map { #","pendingAction":\#($0)"# } ?? ""
            return .lines([#"{"id":\#(id),"ok":true,"items":[\#(items)],"queues":[],\#(Self.settings)\#(p)}"#])
        default:
            return .lines([#"{"id":\#(id),"ok":true}"#])
        }
    }
}

@Suite(.serialized) struct EngineRestartTests {
    // MARK: - EngineClient on a broken socket

    @Test func requestFailsWithDisconnectedWhenEngineHangsUpMidRequest() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        server.script = { cmd, _ in cmd == "pause" ? .hangUp : nil }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        await #expect(throws: EngineError.disconnected) { let _: Ack = try await client.send("pause", ["ids": ["d17"]]) }
        await client.close()
    }

    @Test func inFlightRequestIsFailedWhenEngineDiesWithoutReply() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        server.script = { cmd, _ in cmd == "pause" ? .silent : nil }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        let req = Task { () -> EngineError? in
            do { let _: Ack = try await client.send("pause", ["ids": ["d17"]]); return nil } catch { return error as? EngineError }
        }
        while !server.received.contains("pause") { try await Task.sleep(for: .milliseconds(20)) }
        server.stop()
        let err = await withTaskGroup(of: EngineError?.self) { g in
            g.addTask { await req.value }
            g.addTask { try? await Task.sleep(for: .seconds(3)); return .launchFailed }  // hang marker
            let first = await g.next()!
            g.cancelAll()
            return first
        }
        #expect(err == .disconnected)
        await client.close()
    }

    @Test func eventStreamEndsWithDisconnectedWhenLinkDrops() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        let events = await client.eventStream()
        server.stop()
        var seen: [EngineEvent] = []
        for await e in events { seen.append(e) }  // finishes only if the client noticed the drop
        guard case .disconnected? = seen.last else { Issue.record("no .disconnected: \(seen)"); return }
        await client.close()
    }

    @Test func sameClientReconnectsToRestartedEngine() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        let events = await client.eventStream()
        server.stop()
        for await _ in events {}  // wait until the client has dropped the dead link
        await #expect(throws: EngineError.notConnected) { try await client.connect() }  // engine still down
        try server.start()
        try await client.connect()
        let r: ListReply = try await client.send("list")
        #expect(r.items.count == 1)
        await client.close()
    }

    @Test func staleSocketFileWithoutListenerThrowsNotConnected() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        server.stopLeavingSocketFile()
        #expect(FileManager.default.fileExists(atPath: server.path))
        let client = EngineClient(socketPath: server.path, helper: nil)
        await #expect(throws: EngineError.notConnected) { try await client.connect() }
    }

    /// A client that connects right before `stop()` must never be left on a live connection (accept racing stop).
    @Test func stopHangsUpClientsConnectedJustBeforeIt() async throws {
        for i in 0..<300 {
            let server = try ScriptedEngine(); defer { server.stop() }
            let fd = socket(AF_UNIX, SOCK_STREAM, 0); defer { close(fd) }
            var addr = sockaddr_un()
            addr.sun_family = sa_family_t(AF_UNIX)
            withUnsafeMutableBytes(of: &addr.sun_path) { $0.copyBytes(from: server.path.utf8.prefix($0.count - 1)) }
            let ok = withUnsafePointer(to: &addr) {
                $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
            }
            try #require(ok == 0)
            server.stop()
            var p = pollfd(fd: fd, events: Int16(POLLIN), revents: 0)
            #expect(poll(&p, 1, 2_000) == 1, "client still connected after stop (round \(i))")
        }
    }

    // MARK: - Store across an engine restart

    @Test @MainActor func itemsStayVisibleWhileEngineIsDownAndSnapshotReplacesThemOnReturn() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        server.listItems = [ScriptedEngine.item("d17", state: "downloading", got: 250)]
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected && store.items.first?.id == "d17" })

        server.stop()
        #expect(await eventually { !store.connected })
        // No blank list while the engine restarts, and the failed retry is reported.
        #expect(store.items.map(\.id) == ["d17"] && store.items[0].state == .downloading)
        #expect(await eventually(timeout: 6) { store.lastError == EngineError.notConnected.errorDescription })
        #expect(store.items.map(\.id) == ["d17"])

        // The new engine restored the item as paused at its saved offset (L1: pause → shutdown → restart keeps `got`).
        server.listItems = [ScriptedEngine.item("d17", state: "paused", got: 900), ScriptedEngine.item("d18", state: "queued", got: 0)]
        try server.start()
        #expect(await eventually(timeout: 10) { store.connected && store.items.count == 2 })
        #expect(store.items[0].state == .paused && store.items[0].got == 900)
        #expect(store.items[1].id == "d18" && store.lastError == nil)
    }

    @Test @MainActor func reconnectResubscribesAndReloadsEverySession() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected })
        server.stop()
        #expect(await eventually { !store.connected })
        try server.start()
        #expect(await eventually(timeout: 10) { store.connected && server.listCount == 2 })
        // One subscribe + one list per session, in that order.
        #expect(server.received == ["subscribe", "list", "subscribe", "list"])
    }

    @Test @MainActor func requestsMadeDuringDowntimeSetLastErrorAndAreNotSent() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        server.stop()
        await store.resume(["d17"])
        #expect(store.lastError == EngineError.notConnected.errorDescription)
        #expect(server.received.isEmpty)
    }

    // MARK: - pendingAction (countdown)

    @Test @MainActor func actionEventStartsCountdownFromInjectedClock() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let now = Date(timeIntervalSince1970: 1_000)
        store.now = { now }
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected })
        server.push(#"{"event":"action","action":"shutdown","seconds":30}"#)
        #expect(await eventually { store.pendingAction != nil })
        #expect(store.pendingAction == PendingAction(kind: "shutdown", deadline: now.addingTimeInterval(30)))
    }

    @Test @MainActor func cancelActionClearsCountdownAndTellsEngine() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected })
        server.push(#"{"event":"action","action":"sleep","seconds":30}"#)
        #expect(await eventually { store.pendingAction != nil })
        await store.cancelAction()
        #expect(store.pendingAction == nil)
        #expect(server.received.last == "action.cancel" && store.lastError == nil)
    }

    @Test @MainActor func redownloadSendsCommand() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected })
        await store.redownload(["d1"])
        #expect(server.received.last == "redownload" && store.lastError == nil)
    }

    @Test @MainActor func cancelActionClearsCountdownEvenWhenEngineIsGone() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        store.apply(.action("sleep", seconds: 30))
        server.stop()
        await store.cancelAction()
        #expect(store.pendingAction == nil)  // the panel must close even if the engine cannot be told
        #expect(store.lastError != nil)
    }

    @Test @MainActor func countdownRunningInRestartedEngineReappearsFromSnapshot() async throws {
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let now = Date(timeIntervalSince1970: 2_000)
        store.now = { now }
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected })
        #expect(store.pendingAction == nil)
        server.stop()
        #expect(await eventually { !store.connected })
        server.listPending = #"{"action":"sleep","seconds":20}"#  // the new engine already counts down
        try server.start()
        #expect(await eventually(timeout: 10) { store.connected && store.pendingAction != nil })
        #expect(store.pendingAction == PendingAction(kind: "sleep", deadline: now.addingTimeInterval(20)))
    }

    @Test @MainActor func snapshotWithoutCountdownLeavesRunningOneAlone() async throws {
        // Ruling (F1M M17): a missing `pendingAction` field means "no news", so a reconnect must not close the panel.
        let server = try ScriptedEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let now = Date(timeIntervalSince1970: 3_000)
        store.now = { now }
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.connected })
        server.push(#"{"event":"action","action":"shutdown","seconds":40}"#)
        #expect(await eventually { store.pendingAction != nil })
        server.stop()
        #expect(await eventually { !store.connected })
        try server.start()
        #expect(await eventually(timeout: 10) { store.connected && server.listCount == 2 })
        #expect(store.pendingAction == PendingAction(kind: "shutdown", deadline: now.addingTimeInterval(40)))
    }
}
