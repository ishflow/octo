import Foundation

// Codable mirrors of the shared engine contract (Go daemon <-> app).

/// Go encodes nil slices as `null`; a missing key or `null` decodes as `[]` instead of failing the whole message.
@propertyWrapper
struct OrEmpty<T: Codable & Hashable & Sendable>: Codable, Hashable, Sendable {
    var wrappedValue: [T]
    init(wrappedValue: [T]) { self.wrappedValue = wrappedValue }
    init() { wrappedValue = [] }
    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        wrappedValue = c.decodeNil() ? [] : try c.decode([T].self)
    }
    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(wrappedValue)
    }
}

/// The engine sends `speed` as a float (bytes/s); any JSON number is accepted and rounded to whole bytes.
@propertyWrapper
struct WholeNumber: Codable, Hashable, Sendable {
    var wrappedValue: Int64
    init(wrappedValue: Int64) { self.wrappedValue = wrappedValue }
    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if let n = try? c.decode(Int64.self) { wrappedValue = n; return }
        guard let n = Int64(exactly: try c.decode(Double.self).rounded()) else {
            throw DecodingError.dataCorruptedError(in: c, debugDescription: "number out of Int64 range")
        }
        wrappedValue = n
    }
    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(wrappedValue)
    }
}

/// Older engines send no `userAgent`: a missing key or `null` decodes as "" (= default `octo/1.0`).
@propertyWrapper
struct OrBlank: Codable, Hashable, Sendable {
    var wrappedValue: String
    init(wrappedValue: String) { self.wrappedValue = wrappedValue }
    init() { wrappedValue = "" }
    init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        wrappedValue = c.decodeNil() ? "" : try c.decode(String.self)
    }
    func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        try c.encode(wrappedValue)
    }
}

extension KeyedDecodingContainer {
    func decode<T>(_ type: OrEmpty<T>.Type, forKey key: Key) throws -> OrEmpty<T> {
        try decodeIfPresent(type, forKey: key) ?? OrEmpty()
    }
    func decode(_ type: OrBlank.Type, forKey key: Key) throws -> OrBlank {
        try decodeIfPresent(type, forKey: key) ?? OrBlank()
    }
}

enum ItemState: String, Codable, Sendable {
    case queued, downloading, paused, done, error
}

struct Conn: Codable, Hashable, Sendable {
    var start: Int64
    var pos: Int64
    var end: Int64
    var status: String?  // engine's last event on this connection ("Zaman aşımı…", "Yeni parça aldı"); nil = receiving
}

/// A video item (HLS/DASH): what to fetch (`kind`, `video`/`audio` ids from `stream.probe`) and the engine's segment progress.
/// Every field but `kind` is optional on the wire; an older engine sends no `stream` at all.
struct StreamSpec: Codable, Hashable, Sendable {
    var kind: String  // "hls" | "dash"
    var video: String?  // variant id; nil = media playlist / single track
    var audio: String?  // audio rendition id; nil = embedded or none
    var label: String?  // "1080p", display only
    var segments: Int?  // all tracks
    var segDone: Int?
    var phase: String?  // nil/"" | "merging"
}

struct StreamVariant: Codable, Hashable, Sendable {
    var id: String
    var label = ""
    var codecs = ""
    var width = 0
    var height = 0
    var bandwidth: Int64 = 0  // bits/s
    var audio = ""  // the variant's own audio rendition; "" = embedded
}

struct StreamAudio: Codable, Hashable, Sendable {
    var id: String
    var name: String
    var lang: String
    var `default`: Bool
}

/// `stream.probe` reply: what the manifest offers, best quality first.
struct StreamInfo: Codable, Hashable, Sendable {
    var kind: String
    var container = "mp4"  // "ts" | "mp4"
    var live = false
    var protected = false
    var duration: Double = 0  // seconds, 0 = unknown
    var variants: [StreamVariant] = []
    var audios: [StreamAudio] = []
}

// Lenient decoding: Go sends `null` for empty lists and may drop fields; none of that may fail the whole reply.
extension StreamVariant {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        label = try c.decodeIfPresent(String.self, forKey: .label) ?? ""
        codecs = try c.decodeIfPresent(String.self, forKey: .codecs) ?? ""
        width = try c.decodeIfPresent(Int.self, forKey: .width) ?? 0
        height = try c.decodeIfPresent(Int.self, forKey: .height) ?? 0
        bandwidth = try c.decodeIfPresent(Int64.self, forKey: .bandwidth) ?? 0
        audio = try c.decodeIfPresent(String.self, forKey: .audio) ?? ""
    }
}

extension StreamInfo {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = try c.decode(String.self, forKey: .kind)
        container = try c.decodeIfPresent(String.self, forKey: .container) ?? "mp4"
        live = try c.decodeIfPresent(Bool.self, forKey: .live) ?? false
        protected = try c.decodeIfPresent(Bool.self, forKey: .protected) ?? false
        duration = try c.decodeIfPresent(Double.self, forKey: .duration) ?? 0
        variants = try c.decodeIfPresent([StreamVariant].self, forKey: .variants) ?? []
        audios = try c.decodeIfPresent([StreamAudio].self, forKey: .audios) ?? []
    }
}

struct Item: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var url: String
    var name: String
    var dir: String
    var category: String
    var queue: String
    var state: ItemState
    var size: Int64   // -1 = unknown
    var got: Int64
    @WholeNumber var speed: Int64  // bytes/s
    var eta: Int      // seconds, -1 = unknown
    var resumable: Bool
    var error: String
    var restartReason: String?  // engine could not resume and started over; "" or missing = no restart
    var description: String
    var quarantineFailed: Bool?  // com.apple.quarantine could not be written; missing = false (older engine)
    var refreshOf: String?  // browser sent the same name and size: offer to refresh this unfinished item's address; missing = no offer
    var referrer: String?  // "Yönlendiren": the Referer header the engine sends; missing = none (or an older engine)
    var pageUrl: String?   // "Kaynak sayfa": the page the browser download came from
    var added: String
    var finished: String?
    var lastTry: String?
    var maxConns: Int
    var speedLimitKBps: Int?  // per-file speed limit; nil (older engine) or 0 = off
    var stream: StreamSpec?  // video from a manifest (HLS/DASH); nil = ordinary file
    var note: String?  // finished with a remark, e.g. audio left in its own file; nil/"" = none
    @OrEmpty var conns: [Conn]
}

struct ProgressUpdate: Codable, Hashable, Sendable {
    var id: String
    var state: ItemState
    var got: Int64
    var size: Int64
    @WholeNumber var speed: Int64
    var eta: Int
    @OrEmpty var conns: [Conn]
    var segDone: Int?  // stream items only; absent = 0
    var segments: Int?
    var phase: String?  // "merging" while the parts are joined
}

struct Schedule: Codable, Hashable, Sendable {
    var startOn: Bool
    var start: String
    var stopOn: Bool
    var stop: String
    @OrEmpty var days: [Int]
    var onDone: String  // none|open|sleep|shutdown
}

struct Queue: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var name: String
    var atOnce: Int
    var retries: Int
    @OrEmpty var items: [String]
    var running: Bool
    var schedule: Schedule
}

struct Category: Codable, Hashable, Sendable {
    var name: String
    @OrEmpty var exts: [String]
    var dir: String
}

struct Login: Codable, Hashable, Sendable {
    var host: String
    var user: String
}

struct HostConn: Codable, Hashable, Sendable {
    var host: String
    var conns: Int
}

struct Settings: Codable, Hashable, Sendable {
    var maxConns: Int
    var downloadDir: String
    var categoryFolders: Bool
    var duplicate: String
    var speedLimitOn: Bool
    var speedLimitKBps: Int
    @OrEmpty var categories: [Category]
    @OrEmpty var captureExts: [String]
    @OrEmpty var excludedSites: [String]
    var captureOff: Bool?  // nil: older engine without the field (= capture on)
    var captureMinKB: Int?  // smaller browser downloads stay in the browser; nil/0 = no threshold
    @OrEmpty var logins: [Login]
    /// Per-host connection cap (`*.example.com` -> 1...32); missing = none.
    @OrEmpty var hostConns: [HostConn]
    /// Motor yeni; eski motor alanı göndermez (nil = kapalı).
    var useServerDate: Bool?
    @OrBlank var userAgent: String
    var hideVideoButton: Bool?  // true = no Octo button over videos; nil (older engine) = shown. Kept as sent when written back.
}

struct ListReply: Decodable, Sendable {
    @OrEmpty var items: [Item]
    @OrEmpty var queues: [Queue]
    var settings: Settings
    /// Countdown already running when the app connects (same fields as the `action` event); absent when none.
    var pendingAction: ActionNote?
}

struct ActionNote: Decodable, Sendable {
    var action: String
    var seconds: Int
}

/// Reply with no payload we care about (`subscribe`, `pause`, ...).
struct Ack: Decodable, Sendable {}

struct ItemReply: Decodable, Sendable { var item: Item }

/// `probe` reply: what the engine would download (size -1 = unknown).
struct ProbeReply: Decodable, Sendable {
    var name: String
    var size: Int64
    var resumable: Bool
    var kind: String?  // "hls" | "dash": the address is a manifest, go on with `stream.probe`
}

struct StreamProbeReply: Decodable, Sendable { var info: StreamInfo }

enum EngineEvent: Sendable, Decodable {
    case progress([ProgressUpdate])
    case item(Item)
    case removed([String])
    case queues([Queue])
    case action(String, seconds: Int)
    case unknown
    case disconnected  // synthesized by the client, never on the wire

    private enum Keys: String, CodingKey { case event, items, item, ids, queues, action, seconds }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: Keys.self)
        switch try c.decode(String.self, forKey: .event) {
        case "progress": self = .progress(try c.decodeIfPresent([ProgressUpdate].self, forKey: .items) ?? [])
        case "item": self = .item(try c.decode(Item.self, forKey: .item))
        case "removed": self = .removed(try c.decodeIfPresent([String].self, forKey: .ids) ?? [])
        case "queues": self = .queues(try c.decodeIfPresent([Queue].self, forKey: .queues) ?? [])
        case "action": self = .action(try c.decode(String.self, forKey: .action), seconds: try c.decode(Int.self, forKey: .seconds))
        default: self = .unknown
        }
    }
}

extension Item {
    /// Fraction 0...1; a video of unknown size counts its segments. nil when neither is known.
    var fraction: Double? {
        if size > 0 { return min(1, Double(got) / Double(size)) }
        return StreamProgress.fraction(stream)
    }
    var stateSort: String { state.rawValue }
    var lastTrySort: String { lastTry ?? "" }
}
