import Darwin
import Foundation
import os

enum EngineError: Error, Equatable, LocalizedError {
    case notConnected
    case disconnected
    case launchFailed
    case server(String)

    var errorDescription: String? {
        switch self {
        case .notConnected: "Motora bağlanılamadı."
        case .disconnected: "Motor bağlantısı koptu."
        case .launchFailed: "Motor başlatılamadı."
        case .server(let m): m
        }
    }
}

private struct ReplyHead: Decodable { var ok: Bool; var error: String? }

/// Client of the Go engine daemon: JSON lines over a unix socket.
/// One connection carries both request/reply pairs and (after `subscribe`) pushed events.
actor EngineClient {
    private var sink: AsyncStream<EngineEvent>.Continuation?
    private let socketPath: String
    private let helper: URL?
    private var fd: Int32 = -1
    private var nextID = 1
    private var pending: [Int: CheckedContinuation<Data, Error>] = [:]
    private var process: Process?
    private nonisolated static let log = Logger(subsystem: "com.uba.octo", category: "engine")

    /// Mirrors the engine's `DataDir`: $OCTO_DATA_DIR, legacy $HIZLI_DATA_DIR, else ~/Library/Application Support/Octo.
    static func dataDir(env: [String: String] = ProcessInfo.processInfo.environment, home: String = NSHomeDirectory()) -> String {
        for key in ["OCTO_DATA_DIR", "HIZLI_DATA_DIR"] {
            if let d = env[key], !d.isEmpty { return d }
        }
        return home + "/Library/Application Support/Octo"
    }

    static func defaultSocketPath() -> String { dataDir() + "/engine.sock" }

    /// `helper` is the bundled engine binary to launch with `serve` when the socket is unreachable (nil: never launch).
    init(socketPath: String = EngineClient.defaultSocketPath(),
         helper: URL? = Bundle.main.url(forAuxiliaryExecutable: "octo")) {
        self.socketPath = socketPath
        self.helper = helper
    }

    /// Events of the *current* connection; ends with `.disconnected` when the link drops.
    /// Each call replaces the previous stream (so a cancelled consumer can simply ask again) and
    /// must be made after `connect()` and before commands whose events you need. Call again after reconnecting.
    func eventStream() -> AsyncStream<EngineEvent> {
        sink?.finish()
        let (stream, cont) = AsyncStream.makeStream(of: EngineEvent.self)
        if fd < 0 {
            cont.yield(.disconnected)
            cont.finish()
        } else {
            sink = cont
        }
        return stream
    }

    func connect() async throws {
        guard fd < 0 else { return }
        if let s = Self.dial(socketPath) { attach(s); return }
        guard let helper else { throw EngineError.notConnected }
        if process?.isRunning != true {
            let p = Process()
            p.executableURL = helper
            p.arguments = ["serve"]
            p.standardInput = FileHandle.nullDevice
            p.standardOutput = FileHandle.nullDevice
            p.standardError = FileHandle.nullDevice
            do { try p.run() } catch { throw EngineError.launchFailed }
            process = p
        }
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline {
            try await Task.sleep(for: .milliseconds(100))
            if fd >= 0 { return }  // another caller connected while we slept
            if let s = Self.dial(socketPath) { attach(s); return }
            if process?.isRunning != true { throw EngineError.launchFailed }
        }
        throw EngineError.notConnected
    }

    func send<T: Decodable & Sendable>(_ cmd: String, _ args: sending [String: Any] = [:]) async throws -> T {
        try await connect()
        let id = nextID
        nextID += 1
        var obj = args
        obj["id"] = id
        obj["cmd"] = cmd
        var line = try JSONSerialization.data(withJSONObject: obj)
        line.append(10)
        let reply: Data = try await withCheckedThrowingContinuation { cont in
            pending[id] = cont
            if !writeAll(line) {
                pending[id] = nil
                cont.resume(throwing: EngineError.disconnected)
            }
        }
        let head = try JSONDecoder().decode(ReplyHead.self, from: reply)
        guard head.ok else { throw EngineError.server(head.error ?? "") }
        return try JSONDecoder().decode(T.self, from: reply)
    }

    /// When the native host last said `hello` (the Chrome extension is alive); nil if it has not since the engine started.
    func lastHello() async throws -> Date? {
        struct Reply: Decodable, Sendable { var lastHello: Double? }
        let r: Reply = try await send("list")
        return r.lastHello.map(Date.init(timeIntervalSince1970:))
    }

    /// Terminal: drops the connection and ends `events`.
    func close() {
        if fd >= 0 { shutdown(fd, SHUT_RDWR) }  // unblocks the reader, which triggers `detach()`
        sink?.finish()
        sink = nil
    }

    // MARK: - internals

    private func attach(_ newFD: Int32) {
        fd = newFD
        let (lines, cont) = AsyncStream.makeStream(of: Data.self)
        Thread.detachNewThread { Self.readLoop(newFD, cont) }
        Task {
            for await l in lines { handle(l) }  // sequential: keeps event order
            detach(newFD)
        }
    }

    private func detach(_ gone: Int32) {
        guard fd == gone else { return }
        Darwin.close(fd)
        fd = -1
        for (_, c) in pending { c.resume(throwing: EngineError.disconnected) }
        pending.removeAll()
        sink?.yield(.disconnected)
        sink?.finish()
        sink = nil
    }

    private func handle(_ line: Data) {
        struct Head: Decodable { var id: Int?; var event: String? }
        guard let head = try? JSONDecoder().decode(Head.self, from: line) else {
            Self.log.error("undecodable line: \(String(decoding: line.prefix(200), as: UTF8.self), privacy: .public)")
            return
        }
        if let id = head.id {
            pending.removeValue(forKey: id)?.resume(returning: line)
        } else if head.event != nil {
            do { sink?.yield(try JSONDecoder().decode(EngineEvent.self, from: line)) } catch {
                // Keep the connection alive: one bad event must not stop the stream.
                Self.log.error("event decode failed: \(error.localizedDescription, privacy: .public) line: \(String(decoding: line.prefix(200), as: UTF8.self), privacy: .public)")
            }
        }
    }

    private func writeAll(_ d: Data) -> Bool {
        d.withUnsafeBytes { p in
            var off = 0
            while off < p.count {
                let n = Darwin.write(fd, p.baseAddress! + off, p.count - off)
                if n <= 0 { return false }
                off += n
            }
            return true
        }
    }

    private nonisolated static func dial(_ path: String) -> Int32? {
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        guard path.utf8.count < MemoryLayout.size(ofValue: addr.sun_path) else { return nil }
        let s = socket(AF_UNIX, SOCK_STREAM, 0)
        guard s >= 0 else { return nil }
        var one: Int32 = 1
        setsockopt(s, SOL_SOCKET, SO_NOSIGPIPE, &one, socklen_t(MemoryLayout<Int32>.size))
        withUnsafeMutableBytes(of: &addr.sun_path) { $0.copyBytes(from: path.utf8) }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(s, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        if rc != 0 { Darwin.close(s); return nil }
        return s
    }

    private nonisolated static func readLoop(_ fd: Int32, _ out: AsyncStream<Data>.Continuation) {
        var buf = Data()
        var chunk = [UInt8](repeating: 0, count: 64 * 1024)
        while true {
            let n = read(fd, &chunk, chunk.count)
            if n <= 0 { break }
            buf.append(contentsOf: chunk[0..<n])
            while let nl = buf.firstIndex(of: 10) {
                out.yield(Data(buf[buf.startIndex..<nl]))
                buf.removeSubrange(buf.startIndex...nl)
            }
        }
        out.finish()
    }
}
