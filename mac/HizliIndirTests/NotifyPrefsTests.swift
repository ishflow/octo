import Foundation
import Testing

@Suite struct NotifyPrefsTests {
    private func defaults() -> UserDefaults { UserDefaults(suiteName: "NotifyPrefsTests-\(UUID().uuidString)")! }

    @Test func allOnByDefault() {
        let d = defaults()
        #expect(NotifyPrefs.allows(.done, d) && NotifyPrefs.allows(.error, d) && NotifyPrefs.soundOn(d))
    }
    @Test func doneAndErrorSwitchIndependently() {
        let d = defaults()
        d.set(false, forKey: NotifyPrefs.doneKey)
        #expect(!NotifyPrefs.allows(.done, d) && NotifyPrefs.allows(.error, d))
        d.set(true, forKey: NotifyPrefs.doneKey)
        d.set(false, forKey: NotifyPrefs.errorKey)
        #expect(NotifyPrefs.allows(.done, d) && !NotifyPrefs.allows(.error, d))
    }
    @Test func soundSwitch() {
        let d = defaults()
        d.set(false, forKey: NotifyPrefs.soundKey)
        #expect(!NotifyPrefs.soundOn(d))
    }
    @Test func otherStatesAreNeverPosted() {
        #expect(!NotifyPrefs.allows(.paused, defaults()))
    }
}
