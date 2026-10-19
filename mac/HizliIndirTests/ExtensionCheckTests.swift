import Foundation
import Testing

@Suite struct ExtensionCheckTests {
    private func home() throws -> String {
        let h = NSTemporaryDirectory() + "extcheck-" + UUID().uuidString
        try FileManager.default.createDirectory(atPath: h, withIntermediateDirectories: true)
        return h
    }

    /// Writes `<home>/Library/Application Support/<root>/<profile>/<file>` with `extensions.settings`.
    private func prefs(_ h: String, _ root: String, profile: String = "Default", file: String = "Secure Preferences",
                       settings: [String: Any]) throws {
        let dir = h + "/Library/Application Support/" + root + "/" + profile
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        let json = try JSONSerialization.data(withJSONObject: ["extensions": ["settings": settings]])
        try json.write(to: URL(fileURLWithPath: dir + "/" + file))
    }

    @Test func idMatchesManifestKey() throws {
        let manifest = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("extension/chrome/manifest.json")
        #expect(ExtensionCheck.id(manifest: try Data(contentsOf: manifest)) == "elhjlbdkhcpjihiapkeoeceopennmkab")
        #expect(ExtensionCheck.id(manifest: Data("{}".utf8)) == nil)
        #expect(ExtensionCheck.id(manifest: Data("{\"key\":\"!!\"}".utf8)) == nil)
    }

    @Test func noBrowserMeansNil() throws {
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: try home()) == nil)
    }

    @Test func browserWithoutExtensionIsMissing() throws {
        let h = try home()
        try prefs(h, "Google/Chrome", settings: ["other": ["state": 1]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .missing)
    }

    @Test func enabledInAnyProfile() throws {
        let h = try home()
        try prefs(h, "Google/Chrome", settings: [:])
        try prefs(h, "Google/Chrome", profile: "Profile 2", file: "Preferences", settings: ["abc": ["state": 1]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .enabled)
    }

    @Test func disabledByStateOrReason() throws {
        let h = try home()
        try prefs(h, "Google/Chrome", settings: ["abc": ["state": 0]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .disabled)
        try prefs(h, "Google/Chrome", settings: ["abc": ["state": 1, "disable_reasons": 1]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .disabled)
    }

    @Test func disableReasonsAsListWithoutState() throws {
        let h = try home()
        try prefs(h, "Google/Chrome", settings: ["abc": ["disable_reasons": [1]]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .disabled)
        try prefs(h, "Google/Chrome", settings: ["abc": ["disable_reasons": [Int]()]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .enabled)
    }

    @Test func bestStateAcrossBrowsers() throws {
        let h = try home()
        try prefs(h, "Arc/User Data", settings: ["abc": ["state": 0]])
        try prefs(h, "BraveSoftware/Brave-Browser", settings: ["abc": ["state": 1]])
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome + BrowserHost.others, home: h) == .enabled)
    }

    @Test func unreadablePreferencesCountAsMissing() throws {
        let h = try home()
        let dir = h + "/Library/Application Support/Google/Chrome/Default"
        try FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try Data("not json".utf8).write(to: URL(fileURLWithPath: dir + "/Preferences"))
        #expect(ExtensionCheck.state(id: "abc", browsers: BrowserHost.chrome, home: h) == .missing)
    }
}
