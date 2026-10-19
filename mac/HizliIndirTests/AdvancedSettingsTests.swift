import Foundation
import Testing

@Suite struct CaptureSettingTests {
    private let json = #"{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500}"#

    @Test func olderEngineWithoutFieldsMeansCaptureOn() throws {
        let s = try JSONDecoder().decode(Settings.self, from: Data(json.utf8))
        #expect(s.captureOff == nil && s.captureMinKB == nil)
    }

    @Test func switchAndThresholdReachTheEngine() throws {
        var s = try JSONDecoder().decode(Settings.self, from: Data(json.utf8))
        s.captureOff = true
        s.captureMinKB = 256
        let obj = try JSONSerialization.jsonObject(with: JSONEncoder().encode(s)) as? [String: Any]
        #expect(obj?["captureOff"] as? Bool == true && obj?["captureMinKB"] as? Int == 256)
    }
}

@Suite struct UserAgentSettingTests {
    private let json = #"{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500}"#

    @Test func missingKeyDecodesAsDefault() throws {
        let s = try JSONDecoder().decode(Settings.self, from: Data(json.utf8))
        #expect(s.userAgent == "")
    }

    @Test func nullDecodesAsDefault() throws {
        let s = try JSONDecoder().decode(Settings.self, from: Data(json.dropLast().appending(#","userAgent":null}"#).utf8))
        #expect(s.userAgent == "")
    }

    @Test func roundTripsAsPlainString() throws {
        var s = try JSONDecoder().decode(Settings.self, from: Data(json.utf8))
        s.userAgent = "Ozel/2.0"
        let obj = try JSONSerialization.jsonObject(with: JSONEncoder().encode(s)) as? [String: Any]
        #expect(obj?["userAgent"] as? String == "Ozel/2.0")
    }

    @Test func cleanTrimsAndAllowsBlank() {
        #expect(UserAgentText.clean("  Ozel/2.0 ") == "Ozel/2.0")
        #expect(UserAgentText.clean("   ") == "")
        #expect(UserAgentText.clean("") == "")
    }

    @Test func cleanRejectsLineBreaks() {
        #expect(UserAgentText.clean("A\r\nX: y") == nil)
        #expect(UserAgentText.clean("A\u{7f}") == nil)
    }
}

@Suite struct SettingsResetTests {
    private func edited() -> Settings {
        Settings(maxConns: 16, downloadDir: "/Volumes/X", categoryFolders: true, duplicate: "ask", speedLimitOn: true, speedLimitKBps: 9000,
                 categories: OrEmpty(wrappedValue: [Category(name: "Özel", exts: OrEmpty(wrappedValue: ["abc"]), dir: "/Volumes/X/Özel")]),
                 captureExts: OrEmpty(wrappedValue: ["abc"]), excludedSites: OrEmpty(wrappedValue: ["a.com"]),
                 logins: OrEmpty(wrappedValue: [Login(host: "*.a.com", user: "u")]), userAgent: OrBlank(wrappedValue: "Ozel/2.0"))
    }

    @Test func resetsPreferencesButKeepsCategoriesAndLogins() {
        let old = edited()
        let s = SettingsReset.apply(to: old)
        #expect(s.maxConns == 8 && s.downloadDir == "~/Downloads" && !s.categoryFolders && s.duplicate == "number")
        #expect(!s.speedLimitOn && s.speedLimitKBps == 500)
        #expect(s.captureExts == SettingsReset.captureExts && s.excludedSites.isEmpty && s.userAgent == "")
        #expect(s.categories == old.categories && s.logins == old.logins)
        #expect(SettingsCheck.problem(s) == nil)
    }

    @Test func resetTurnsCaptureBackOn() {
        var old = edited()
        old.captureOff = true
        old.captureMinKB = 4096
        old.hideVideoButton = true
        let s = SettingsReset.apply(to: old)
        #expect(s.captureOff == false && s.captureMinKB == 0 && s.hideVideoButton == false)
    }

    /// The engine owns the default list; the Chrome extension carries a copy. All three must agree.
    @Test func captureExtsMatchTheExtensionDefaults() throws {
        let src = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("extension/chrome/lib/decide.mjs")
        let text = try String(contentsOf: src, encoding: .utf8)
        let block = try #require(text.firstMatch(of: /DEFAULT_EXTS = \(([^)]*)\)\.split/)?.output.1)
        let words = block.matches(of: /"([^"]*)"/).flatMap { $0.output.1.split(separator: " ").map(String.init) }
        #expect(SettingsReset.captureExts == words)
    }
}
