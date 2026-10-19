import CryptoKit
import Foundation

/// Whether the Octo Chrome extension is loaded in a Chromium browser, read from its profile Preferences.
/// (The native host cannot tell the app a `hello` arrived: it only talks to the engine over the socket.)
enum ExtensionCheck {
    enum State: Int, Comparable, Sendable {
        case missing, disabled, enabled
        static func < (a: State, b: State) -> Bool { a.rawValue < b.rawValue }
    }

    /// Chrome derives an extension's ID from the manifest `key`: first 16 bytes of SHA-256(DER), nibbles mapped to a–p.
    static func id(manifest: Data) -> String? {
        guard let m = try? JSONSerialization.jsonObject(with: manifest) as? [String: Any],
              let key = m["key"] as? String, let der = Data(base64Encoded: key) else { return nil }
        return SHA256.hash(data: der).prefix(16).map {
            String(UnicodeScalar(97 + $0 >> 4)) + String(UnicodeScalar(97 + $0 & 15))
        }.joined()
    }

    /// The ID of the extension bundled in the app.
    static var bundledID: String? {
        Bundle.main.url(forResource: "ChromeExtension", withExtension: nil)
            .flatMap { try? Data(contentsOf: $0.appendingPathComponent("manifest.json")) }.flatMap(id(manifest:))
    }

    /// Chrome wrote `disable_reasons` as a bit mask; newer builds write a list of reasons.
    private static func noDisableReasons(_ v: Any?) -> Bool {
        if let list = v as? [Any] { return list.isEmpty }
        return (v as? Int ?? 0) == 0
    }

    /// nil: none of `browsers` is installed; else the best state over all their profiles.
    /// shortcut: Chrome flushes Preferences lazily, so a fresh install can show up a few seconds late; recheck on demand.
    static func state(id: String, browsers: [String], home: String = NSHomeDirectory()) -> State? {
        let fm = FileManager.default, root = home + "/Library/Application Support/"
        let present = browsers.map { root + $0 }.filter { fm.fileExists(atPath: $0) }
        guard !present.isEmpty else { return nil }
        var best = State.missing
        for dir in present {
            for profile in (try? fm.contentsOfDirectory(atPath: dir)) ?? [] where profile == "Default" || profile.hasPrefix("Profile ") {
                for file in ["Secure Preferences", "Preferences"] {
                    guard let data = fm.contents(atPath: "\(dir)/\(profile)/\(file)"),
                          let j = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                          let e = ((j["extensions"] as? [String: Any])?["settings"] as? [String: Any])?[id] as? [String: Any] else { continue }
                    let on = (e["state"] as? Int ?? 1) != 0 && Self.noDisableReasons(e["disable_reasons"])
                    best = max(best, on ? .enabled : .disabled)
                }
            }
        }
        return best
    }
}
