import Foundation
import Testing

/// Tiny in-test engine: accepts connections on a unix socket and speaks the JSON-lines contract.
final class FakeEngine: @unchecked Sendable {
    let path: String
    private let listenFD: Int32
    private let lock = NSLock()
    private var conns: [Int32] = []
    private var subscribes = 0

    init() throws {
        path = NSTemporaryDirectory() + "hi-\(UUID().uuidString.prefix(8)).sock"
        listenFD = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        withUnsafeMutableBytes(of: &addr.sun_path) { $0.copyBytes(from: path.utf8.prefix($0.count - 1)) }
        let ok = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(listenFD, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard ok == 0, listen(listenFD, 4) == 0 else { throw POSIXError(.EADDRINUSE) }
        let fd = listenFD
        Thread.detachNewThread { [self] in
            while true {
                let c = accept(fd, nil, nil)
                if c < 0 { return }
                self.track(c)
                Thread.detachNewThread { self.serve(c) }
            }
        }
    }

    private func track(_ c: Int32) { lock.lock(); conns.append(c); lock.unlock() }
    private func nextSubscribe() -> Int { lock.lock(); defer { lock.unlock() }; subscribes += 1; return subscribes }

    /// Simulates a daemon restart: closes every client connection but keeps listening.
    func dropConnections() { lock.lock(); conns.forEach { shutdown($0, SHUT_RDWR) }; conns = []; lock.unlock() }

    func pushToAll(_ line: String) { lock.lock(); conns.forEach { FakeEngine.send($0, line) }; lock.unlock() }

    func stop() { shutdown(listenFD, SHUT_RDWR); close(listenFD); unlink(path) }

    private static func send(_ fd: Int32, _ s: String) { let b = Array((s + "\n").utf8); _ = write(fd, b, b.count) }

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
                switch cmd {
                case "list":
                    FakeEngine.send(fd, #"{"id":\#(id),"ok":true,"items":[{"id":"d17","url":"https://x/y.iso","name":"y.iso","dir":"/tmp","category":"Programlar","queue":"main","state":"downloading","size":1000,"got":250,"speed":50,"eta":15,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[{"start":0,"pos":250,"end":1000}]}],"queues":[{"id":"main","name":"Ana kuyruk","atOnce":4,"retries":0,"items":["d17"],"running":false,"schedule":{"startOn":false,"start":"23:00","stopOn":false,"stop":"07:30","days":[1,2,3],"onDone":"none"}}],"settings":{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500,"categories":[{"name":"Arşiv","exts":["zip"],"dir":""}],"captureExts":["zip"],"excludedSites":[],"logins":[{"host":"*.example.com","user":"u"}]}}"#)
                case "subscribe":
                    let n = nextSubscribe()  // got = 600, 700, ... so tests can tell which subscription's event arrived
                    FakeEngine.send(fd, #"{"id":\#(id),"ok":true}"#)
                    FakeEngine.send(fd, #"{"event":"progress","items":[{"id":"d17","state":"downloading","got":\#(500 + 100 * n),"size":1000,"speed":100,"eta":4,"conns":[{"start":0,"pos":600,"end":1000}]}]}"#)
                case "settings.set":
                    let ok = (req["settings"] as? [String: Any])?["speedLimitKBps"] as? Int == 123
                    FakeEngine.send(fd, ok ? #"{"id":\#(id),"ok":true}"# : #"{"id":\#(id),"ok":false,"error":"Geçersiz ayar"}"#)
                case "remove":
                    let ok = req["deleteFile"] as? Bool != true
                    FakeEngine.send(fd, ok ? #"{"id":\#(id),"ok":true}"# : #"{"id":\#(id),"ok":false,"error":"Çöp'e taşınamadı: başka birim"}"#)
                case "queue.move":
                    let ok = req["ids"] as? [String] == ["d17"] && req["queue"] as? String == "night"
                    FakeEngine.send(fd, ok ? #"{"id":\#(id),"ok":true}"# : #"{"id":\#(id),"ok":false,"error":"Geçersiz kuyruk"}"#)
                case "queue.save":
                    let q = req["queue"] as? [String: Any]
                    let ok = q?["id"] as? String == "night" && (q?["items"] as? [String]) == ["d17"]
                        && (q?["schedule"] as? [String: Any])?["days"] as? [Int] == [1, 2]
                    FakeEngine.send(fd, ok ? #"{"id":\#(id),"ok":true}"# : #"{"id":\#(id),"ok":false,"error":"Geçersiz kuyruk"}"#)
                case "item.limit":
                    guard req["item"] as? String == "d17", let k = req["kbps"] as? Int else {
                        FakeEngine.send(fd, #"{"id":\#(id),"ok":false,"error":"indirme bulunamadı"}"#); continue
                    }
                    FakeEngine.send(fd, #"{"id":\#(id),"ok":true,"item":{"id":"d17","url":"https://x/y.iso","name":"y.iso","dir":"/tmp","category":"","queue":"main","state":"downloading","size":1000,"got":250,"speed":50,"eta":15,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"speedLimitKBps":\#(k),"conns":[]}}"#)
                case "stream.probe":
                    guard (req["url"] as? String)?.hasSuffix("/master.m3u8") == true else {
                        FakeEngine.send(fd, #"{"id":\#(id),"ok":false,"error":"Bilinmeyen komut"}"#); continue
                    }
                    FakeEngine.send(fd, #"{"id":\#(id),"ok":true,"info":{"kind":"hls","container":"mp4","live":false,"protected":false,"duration":12,"variants":[{"id":"v0","label":"720p","codecs":"avc1","width":1280,"height":720,"bandwidth":2000000,"audio":""}],"audios":null}}"#)
                case "add":
                    // Echoes the requested stream spec back on the new item, like the engine does.
                    let spec = (req["stream"] as? [String: Any]).flatMap { try? JSONSerialization.data(withJSONObject: $0) }
                        .flatMap { String(data: $0, encoding: .utf8) }.map { #","stream":\#($0)"# } ?? ""
                    FakeEngine.send(fd, #"{"id":\#(id),"ok":true,"item":{"id":"d18","url":"u","name":"video.mp4","dir":"/tmp","category":"","queue":"main","state":"queued","size":-1,"got":0,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-20T10:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]\#(spec)}}"#)
                case "queue.delete", "queue.start", "queue.stop":
                    // The request id lives in "id", so the target queue travels as "queue" (the engine accepts id|queue|item).
                    let ok = req["queue"] as? String == "night"
                    FakeEngine.send(fd, ok ? #"{"id":\#(id),"ok":true}"# : #"{"id":\#(id),"ok":false,"error":"Geçersiz kuyruk"}"#)
                default:
                    FakeEngine.send(fd, #"{"id":\#(id),"ok":false,"error":"Bulunamadı"}"#)
                }
            }
        }
    }
}

@Suite struct EngineClientTests {
    @Test func listDecodesContractModels() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        let r: ListReply = try await client.send("list")
        #expect(r.items.count == 1)
        #expect(r.items[0].state == .downloading && r.items[0].got == 250 && r.items[0].lastTry == nil)
        #expect(r.items[0].conns == [Conn(start: 0, pos: 250, end: 1000)])
        #expect(r.queues[0].schedule.days == [1, 2, 3] && r.queues[0].schedule.onDone == "none")
        #expect(r.settings.categories[0].name == "Arşiv" && r.settings.logins[0].host == "*.example.com")
        await client.close()
    }

    @Test func subscribeDeliversProgressEvent() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        let events = await client.eventStream()
        let _: Ack = try await client.send("subscribe")
        let event = await withTaskGroup(of: EngineEvent?.self) { g in
            g.addTask { var it = events.makeAsyncIterator(); return await it.next() }
            g.addTask { try? await Task.sleep(for: .seconds(3)); return nil }
            let first = await g.next()!
            g.cancelAll()
            return first
        }
        guard case .progress(let ups)? = event else { Issue.record("no progress event: \(String(describing: event))"); return }
        #expect(ups.count == 1 && ups[0].got == 600 && ups[0].speed == 100 && ups[0].eta == 4)
        await client.close()
    }

    @Test func connDecodesOptionalStatus() throws {
        let json = Data(#"[{"start":0,"pos":5,"end":9,"status":"Yeni parça aldı"},{"start":10,"pos":10,"end":19}]"#.utf8)
        let conns = try JSONDecoder().decode([Conn].self, from: json)
        #expect(conns[0].status == "Yeni parça aldı" && conns[1].status == nil)
    }

    @Test func errorReplyThrowsServerText() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let client = EngineClient(socketPath: server.path, helper: nil)
        await #expect(throws: EngineError.server("Bulunamadı")) { let _: Ack = try await client.send("pause", ["ids": ["nope"]]) }
        await client.close()
    }

    @Test func missingSocketWithoutHelperThrows() async {
        let client = EngineClient(socketPath: NSTemporaryDirectory() + "yok.sock", helper: nil)
        await #expect(throws: EngineError.notConnected) { try await client.connect() }
    }

    @Test @MainActor func storeAppliesEvents() throws {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        let json = Data(#"{"id":"d1","url":"u","name":"n","dir":"/","category":"","queue":"main","state":"queued","size":-1,"got":0,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]}"#.utf8)
        store.apply(.item(try JSONDecoder().decode(Item.self, from: json)))
        store.apply(.progress([ProgressUpdate(id: "d1", state: .downloading, got: 5, size: 10, speed: 1, eta: 5, conns: OrEmpty())]))
        #expect(store.items.count == 1 && store.items[0].state == .downloading && store.items[0].size == 10)
        store.apply(.removed(["d1"]))
        #expect(store.items.isEmpty)
    }

    @Test @MainActor func storeReportsFinishOnceOnTransitionToDone() throws {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        var finished: [String] = []
        store.onFinished = { finished.append($0.id) }
        let json = Data(#"{"id":"d1","url":"u","name":"n","dir":"/","category":"","queue":"main","state":"done","size":10,"got":10,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]}"#.utf8)
        var done = try JSONDecoder().decode(Item.self, from: json)
        store.apply(.item(done))  // new item already done (snapshot-like): not a transition
        #expect(finished.isEmpty)
        done.id = "d2"; done.state = .downloading
        store.apply(.item(done))
        store.apply(.progress([ProgressUpdate(id: "d2", state: .done, got: 10, size: 10, speed: 0, eta: -1, conns: OrEmpty())]))
        done.state = .done
        store.apply(.item(done))  // engine also sends the item event: must not fire twice
        #expect(finished == ["d2"])
    }

    @Test @MainActor func saveSettingsSendsAndUpdatesLocalCopy() async throws {
        let server = try FakeEngine()
        defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        var s = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: false, duplicate: "number",
                         speedLimitOn: true, speedLimitKBps: 123, categories: OrEmpty(), captureExts: OrEmpty(),
                         excludedSites: OrEmpty(), logins: OrEmpty())
        try await store.saveSettings(s)
        #expect(store.settings?.speedLimitKBps == 123 && store.settings?.speedLimitOn == true)
        s.speedLimitKBps = 7
        await #expect(throws: EngineError.server("Geçersiz ayar")) { try await store.saveSettings(s) }
        #expect(store.settings?.speedLimitKBps == 123)
    }

    @Test @MainActor func updateSettingsValidatesDiffsAndRevertsOnFailure() async throws {
        let server = try FakeEngine()
        defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        store.settings = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: false, duplicate: "number",
                                  speedLimitOn: false, speedLimitKBps: 500)
        // invalid: not sent, not applied, message set
        #expect(await store.update { $0.speedLimitKBps = 0 } == false)
        #expect(store.settings?.speedLimitKBps == 500 && store.settingsError != nil)
        // unchanged: nothing to send (the fake would reject 500)
        #expect(await store.update { $0.speedLimitOn = false } == true)
        #expect(store.settingsError == nil)
        // engine refuses (fake only accepts 123): local copy goes back, message is the engine's
        #expect(await store.update { $0.speedLimitKBps = 7 } == false)
        #expect(store.settings?.speedLimitKBps == 500 && store.settingsError == "Geçersiz ayar")
        #expect(await store.update { $0.speedLimitKBps = 123 } == true)
        #expect(store.settings?.speedLimitKBps == 123 && store.settingsError == nil)
    }

    @Test @MainActor func failedUpdateKeepsALaterLocalChange() async throws {
        let server = try FakeEngine()
        defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        store.settings = Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: false, duplicate: "number",
                                  speedLimitOn: false, speedLimitKBps: 500)
        let a = Task { await store.update { $0.speedLimitKBps = 7 } }  // the fake refuses 7
        while store.settings?.speedLimitKBps != 7 { await Task.yield() }
        store.settings?.speedLimitOn = true  // stands in for change B applied while A is in flight
        #expect(await a.value == false)
        #expect(store.settings?.speedLimitOn == true && store.settingsError == "Geçersiz ayar")
    }

    @Test @MainActor func removeFailureSurfacesAsLastError() async throws {
        let server = try FakeEngine()
        defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        await store.remove(["d17"])
        #expect(store.lastError == nil)
        await store.remove(["d17"], deleteFile: true)  // the engine keeps the item and answers ok:false
        #expect(store.lastError == "Çöp'e taşınamadı: başka birim")
    }

    @Test @MainActor func queueCommandsSendTargetUnderQueueKey() async throws {
        let server = try FakeEngine()
        defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        try await store.moveToQueue(["d17"], queue: "night")
        await #expect(throws: EngineError.server("Geçersiz kuyruk")) { try await store.moveToQueue(["d17"], queue: "x") }
        try await store.deleteQueue("night")
        await store.startQueue("night")
        await store.stopQueue("night")
        let q = Queue(id: "night", name: "Gece", atOnce: 2, retries: 0, items: OrEmpty(wrappedValue: ["d17"]), running: false,
                      schedule: Schedule(startOn: true, start: "02:00", stopOn: false, stop: "07:30",
                                         days: OrEmpty(wrappedValue: [1, 2]), onDone: "none"))
        try await store.saveQueue(q)
        #expect(store.lastError == nil)
        #expect(store.queues.map(\.id) == ["night"])
    }

    @Test @MainActor func itemLimitSendsTargetAndAppliesReply() async throws {
        let server = try FakeEngine()
        defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        try await store.setItemLimit("d17", kbps: 300)
        #expect(store.items.first?.speedLimitKBps == 300)
        await #expect(throws: EngineError.server("indirme bulunamadı")) { try await store.setItemLimit("zz", kbps: 1) }
    }

    @Test func nullAndMissingArraysDecodeAsEmpty() throws {
        let item = Data(#"{"id":"d1","url":"u","name":"n","dir":"/","category":"","queue":"main","state":"queued","size":-1,"got":0,"speed":0,"eta":-1,"resumable":true,"error":"","description":"","added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null}"#.utf8)
        #expect(try JSONDecoder().decode(Item.self, from: item).conns.isEmpty)
        let list = Data(#"{"id":1,"ok":true,"items":null,"queues":null,"settings":{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500,"categories":null,"excludedSites":null,"logins":null}}"#.utf8)
        let r = try JSONDecoder().decode(ListReply.self, from: list)
        #expect(r.items.isEmpty && r.queues.isEmpty && r.settings.categories.isEmpty && r.settings.captureExts.isEmpty && r.settings.hostConns.isEmpty)
        let ev = Data(#"{"event":"progress","items":[{"id":"d1","state":"paused","got":1,"size":2,"speed":0,"eta":-1,"conns":null}]}"#.utf8)
        guard case .progress(let u) = try JSONDecoder().decode(EngineEvent.self, from: ev) else { Issue.record("not progress"); return }
        #expect(u[0].conns.isEmpty)
    }

    @Test func badEventDoesNotKillConnection() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let client = EngineClient(socketPath: server.path, helper: nil)
        try await client.connect()
        // "state":"bogus" fails to decode: must be logged and skipped, later requests/events keep working.
        server.pushToAll(#"{"event":"item","item":{"id":"x","state":"bogus"}}"#)
        let r: ListReply = try await client.send("list")
        #expect(r.items.count == 1)
        await client.close()
    }

    @Test @MainActor func storeRecoversEventsAfterReconnect() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let run = Task { await store.run() }
        defer { run.cancel() }
        #expect(await eventually { store.items.first?.got == 600 })
        server.dropConnections()
        #expect(await eventually { !store.connected })
        // Fresh connection => fresh event stream: the second subscription's progress (700) must be applied.
        #expect(await eventually(timeout: 10) { store.connected && store.items.first?.got == 700 })
    }

    @Test @MainActor func storeRunRestartAfterCancelKeepsEvents() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let first = Task { await store.run() }
        #expect(await eventually { store.items.first?.got == 600 })
        first.cancel()  // e.g. window closed
        await first.value
        let second = Task { await store.run() }
        defer { second.cancel() }
        #expect(await eventually { store.items.first?.got == 700 })
    }
}

// 15 sn: tam koşuda yük altında @MainActor meşgulken 5 sn yetmiyordu (T3F); başarıda erken döner.
@MainActor func eventually(timeout: Double = 15, _ cond: () -> Bool) async -> Bool {
    let end = Date().addingTimeInterval(timeout)
    while Date() < end {
        if cond() { return true }
        try? await Task.sleep(for: .milliseconds(50))
    }
    return cond()
}

@Suite struct DataDirTests {
    @Test func prefersOctoEnvThenLegacyEnvThenDefault() {
        #expect(EngineClient.dataDir(env: [:], home: "/h") == "/h/Library/Application Support/Octo")
        #expect(EngineClient.dataDir(env: ["OCTO_DATA_DIR": "", "HIZLI_DATA_DIR": ""], home: "/h") == "/h/Library/Application Support/Octo")
        #expect(EngineClient.dataDir(env: ["HIZLI_DATA_DIR": "/eski"], home: "/h") == "/eski")
        #expect(EngineClient.dataDir(env: ["OCTO_DATA_DIR": "/yeni", "HIZLI_DATA_DIR": "/eski"], home: "/h") == "/yeni")
    }
}
