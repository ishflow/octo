import AppKit
import Foundation
import Testing

@Suite struct FirstRunTests {
    @Test func onboardingShowsHomeAsTilde() {
        #expect(Onboarding.shortPath(NSHomeDirectory() + "/Downloads") == "~/Downloads")
        #expect(Onboarding.shortPath("/Volumes/Disk/Indirilenler") == "/Volumes/Disk/Indirilenler")
        #expect(Onboarding.shortPath("~/Downloads") == "~/Downloads")
    }

    @Test func downloadsProbeLeavesNoTraceAndReportsAccess() throws {
        let dir = FileManager.default.temporaryDirectory.appendingPathComponent("octo-probe-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: dir) }
        #expect(DownloadsAccess.touch(dir.path) == true)
        #expect(try FileManager.default.contentsOfDirectory(atPath: dir.path).isEmpty)
        #expect(DownloadsAccess.touch(dir.path + "/yok") == false)
    }

    @Test @MainActor func quitDismissesOpenSheets() {
        let win = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 200, height: 100), styleMask: [.titled], backing: .buffered, defer: true)
        let sheet = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 100, height: 50), styleMask: [.titled], backing: .buffered, defer: true)
        win.isReleasedWhenClosed = false  // ARC owns it; the default over-releases on close()
        sheet.isReleasedWhenClosed = false
        win.orderFront(nil)  // an unshown window takes no sheet
        defer { win.close() }
        win.beginSheet(sheet)
        #expect(!win.sheets.isEmpty)
        QuitSheets.dismissAll(in: [win])
        #expect(win.sheets.isEmpty)
    }
}
