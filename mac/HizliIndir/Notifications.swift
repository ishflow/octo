import AppKit
import UserNotifications

/// Settings > Genel switches for notifications (UserDefaults, all on by default).
enum NotifyPrefs {
    static let doneKey = "notifyDone", errorKey = "notifyError", soundKey = "notifySound"

    /// Only finished and failed downloads ever notify; each has its own switch.
    static func allows(_ state: ItemState, _ d: UserDefaults = .standard) -> Bool {
        switch state {
        case .done: d.object(forKey: doneKey) as? Bool ?? true
        case .error: d.object(forKey: errorKey) as? Bool ?? true
        default: false
        }
    }

    static func soundOn(_ d: UserDefaults = .standard) -> Bool { d.object(forKey: soundKey) as? Bool ?? true }
}

/// Completion/failure notifications with "Aç" / "Finder'da göster" actions (category "done").
@MainActor
final class Notifier: NSObject, UNUserNotificationCenterDelegate {
    static let shared = Notifier()
    private static let openID = "open", revealID = "reveal"

    /// Delegate + category only; authorization is requested lazily on the first notification.
    func setup() {
        let c = UNUserNotificationCenter.current()
        c.delegate = self
        let done = UNNotificationCategory(identifier: "done", actions: [
            UNNotificationAction(identifier: Self.openID, title: "Aç", options: [.foreground]),
            UNNotificationAction(identifier: Self.revealID, title: "Finder'da göster", options: []),
        ], intentIdentifiers: [])
        c.setNotificationCategories([done])
    }

    func post(_ item: Item) {
        guard NotifyPrefs.allows(item.state), let n = NotificationMap.content(for: item) else { return }
        Task {
            let c = UNUserNotificationCenter.current()
            guard (try? await c.requestAuthorization(options: [.alert, .sound])) == true else { return }
            let content = UNMutableNotificationContent()
            content.title = n.title
            content.subtitle = n.subtitle
            content.body = n.body
            content.userInfo = ["path": n.path]
            if NotifyPrefs.soundOn() { content.sound = .default }
            if let cat = n.category { content.categoryIdentifier = cat }
            try? await c.add(UNNotificationRequest(identifier: n.identifier, content: content, trigger: nil))
        }
    }

    /// The refresh-address question lives in the main window, which may be closed: tell the user and reopen it on click.
    func postRefreshOffer(_ item: Item) {
        Task {
            let c = UNUserNotificationCenter.current()
            guard (try? await c.requestAuthorization(options: [.alert, .sound])) == true else { return }
            let content = UNMutableNotificationContent()
            content.title = "Yarım kalan indirme için yeni adres"
            content.subtitle = item.name
            content.body = "Tarayıcıdan aynı ad ve boyutla yeni bir adres geldi. Octo'yu açıp karar verin."
            content.userInfo = ["offer": true]
            if NotifyPrefs.soundOn() { content.sound = .default }
            try? await c.add(UNNotificationRequest(identifier: "offer-\(item.id)", content: content, trigger: nil))
        }
    }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification) async
        -> UNNotificationPresentationOptions { NotifyPrefs.soundOn() ? [.banner, .sound] : [.banner] }

    nonisolated func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse) async {
        let info = response.notification.request.content.userInfo
        if info["offer"] != nil {
            // Opening the app again brings the main window back (it may have been closed).
            await MainActor.run { NSWorkspace.shared.openApplication(at: Bundle.main.bundleURL, configuration: .init()) }
            return
        }
        guard let path = info["path"] as? String else { return }
        let action = response.actionIdentifier
        await MainActor.run {
            let url = URL(fileURLWithPath: path)
            if action == Self.openID { NSWorkspace.shared.open(url) } else { NSWorkspace.shared.activateFileViewerSelecting([url]) }
        }
    }
}
