import AppKit

/// A sheet left open (add dialog, onboarding) made Quit abort (-128): close them first, then let the app go.
enum QuitSheets {
    @MainActor static func dismissAll(in windows: [NSWindow]) {
        for w in windows { for s in w.sheets { w.endSheet(s) } }
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    @MainActor func applicationShouldTerminate(_ app: NSApplication) -> NSApplication.TerminateReply {
        QuitSheets.dismissAll(in: app.windows)
        return .terminateNow
    }
}

/// Downloads sits behind a macOS permission prompt. Touching the folder once in a calm moment (first run) puts the prompt
/// in front of the user; otherwise it first appears while the engine writes a .part file, possibly unattended, and stalls it.
enum DownloadsAccess {
    static func touch(_ dir: String) -> Bool {
        let probe = (dir as NSString).expandingTildeInPath + "/.octo-erisim-\(UUID().uuidString)"
        guard FileManager.default.createFile(atPath: probe, contents: nil) else { return false }
        try? FileManager.default.removeItem(atPath: probe)
        return true
    }
}
