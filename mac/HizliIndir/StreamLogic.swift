import Foundation

// Pure helpers for video (HLS/DASH) downloads: the quality/audio pick in the new-download sheet and the segment progress texts.

enum StreamPick {
    static let protectedText = "Bu video korumalı (DRM), indirilemez"
    static let liveText = "Canlı yayın indirilemez"

    static func variant(_ info: StreamInfo, _ id: String?) -> StreamVariant? {
        info.variants.first { $0.id == id } ?? info.variants.first
    }

    /// The audio picker only exists when there is a real choice: an HLS variant with its own embedded sound has none.
    static func showsAudioPicker(_ info: StreamInfo, video: String?) -> Bool {
        info.audios.count > 1 && audio(info, video: video, wanted: nil) != ""
    }

    /// Audio id to send: the user's pick, else the variant's own rendition, else (DASH) the default track. "" = embedded or none.
    static func audio(_ info: StreamInfo, video: String?, wanted: String?) -> String {
        if let wanted, info.audios.contains(where: { $0.id == wanted }) { return wanted }
        if let own = variant(info, video)?.audio, info.audios.contains(where: { $0.id == own }) { return own }
        if info.kind == "hls" && !info.variants.isEmpty { return "" }
        return (info.audios.first(where: \.default) ?? info.audios.first)?.id ?? ""
    }

    static func spec(_ info: StreamInfo, video: String?, audio wanted: String?) -> StreamSpec {
        let v = variant(info, video)
        let a = audio(info, video: v?.id, wanted: wanted)
        return StreamSpec(kind: info.kind, video: v?.id, audio: a.isEmpty ? nil : a, label: v.flatMap { $0.label.isEmpty ? nil : $0.label })
    }

    /// Why this video cannot be added (the engine refuses it too); nil = fine. Live wins over protected.
    static func blocker(_ info: StreamInfo) -> String? { info.live ? liveText : info.protected ? protectedText : nil }

    /// "1080p · 4,5 Mb/sn".
    static func title(_ v: StreamVariant) -> String {
        let name = !v.label.isEmpty ? v.label : v.height > 0 ? "\(v.height)p" : "Varyant \(v.id)"
        guard v.bandwidth > 0 else { return name }
        let rate = v.bandwidth >= 1_000_000
            ? (Double(v.bandwidth) / 1_000_000).formatted(.number.locale(Format.tr).precision(.fractionLength(0...1))) + " Mb/sn"
            : "\(v.bandwidth / 1000) kb/sn"
        return "\(name) · \(rate)"
    }

    /// "Türkçe (tr)".
    static func title(_ a: StreamAudio) -> String {
        switch (a.name.isEmpty, a.lang.isEmpty) {
        case (true, true): "Ses \(a.id)"
        case (true, false): a.lang
        case (false, true): a.name
        case (false, false): "\(a.name) (\(a.lang))"
        }
    }

    /// Same default as the engine when no name is sent.
    static func fileName(_ info: StreamInfo) -> String {
        if info.container == "ts" { return "video.ts" }
        return info.variants.isEmpty && !info.audios.isEmpty ? "video.m4a" : "video.mp4"
    }

    /// "4:13", "1:02:03"; nil when unknown.
    static func duration(_ seconds: Double) -> String? {
        guard seconds.isFinite, seconds >= 1, seconds < 1e7 else { return nil }  // manifest values are untrusted; ~115 days
        let t = Int(seconds.rounded()), h = t / 3600, m = t % 3600 / 60, s = t % 60
        return h > 0 ? String(format: "%d:%02d:%02d", h, m, s) : String(format: "%d:%02d", m, s)
    }
}

enum StreamProgress {
    /// "Parça 120/340", "Birleştiriliyor…"; nil for ordinary files and before the part count is known.
    static func line(_ s: StreamSpec?) -> String? {
        guard let s else { return nil }
        if s.phase == "merging" { return "Birleştiriliyor…" }
        guard let total = s.segments, total > 0 else { return nil }
        return "Parça \(min(s.segDone ?? 0, total))/\(total)"
    }

    static func fraction(_ s: StreamSpec?) -> Double? {
        guard let total = s?.segments, total > 0 else { return nil }
        return min(1, Double(s?.segDone ?? 0) / Double(total))
    }

    static func merging(_ i: Item) -> Bool { i.stream?.phase == "merging" }
}
