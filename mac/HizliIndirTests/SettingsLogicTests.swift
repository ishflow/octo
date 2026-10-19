import Foundation
import Testing

@Suite struct TimeOfDayTests {
    @Test func parsesStrictHHmm() {
        #expect(TimeOfDay.minutes("23:00") == 1380)
        #expect(TimeOfDay.minutes("07:30") == 450)
        #expect(TimeOfDay.minutes("00:00") == 0)
        #expect(TimeOfDay.minutes("23:59") == 1439)
    }
    @Test func rejectsBad() {
        for s in ["", "7:30", "24:00", "12:60", "12-30", "ab:cd", "12:3", "123:00", " 12:00"] {
            #expect(TimeOfDay.minutes(s) == nil, "\(s)")
        }
    }
    @Test func formats() {
        #expect(TimeOfDay.string(minutes: 450) == "07:30")
        #expect(TimeOfDay.string(minutes: 1439) == "23:59")
        #expect(TimeOfDay.string(minutes: -1) == nil)
        #expect(TimeOfDay.string(minutes: 1440) == nil)
    }
    @Test func dateRoundTrip() {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = TimeZone(identifier: "UTC")!
        let d = TimeOfDay.date("07:30", calendar: cal)
        #expect(d != nil)
        #expect(TimeOfDay.string(from: d!, calendar: cal) == "07:30")
        #expect(TimeOfDay.date("nope", calendar: cal) == nil)
    }
}

@Suite struct DaySetTests {
    @Test func togglesAddAndRemoveSorted() {
        #expect(DaySet.toggle([1, 2, 3], 5) == [1, 2, 3, 5])
        #expect(DaySet.toggle([1, 2, 3], 2) == [1, 3])
        #expect(DaySet.toggle([7, 1], 3) == [1, 3, 7])
    }
    @Test func neverEmpties() {
        #expect(DaySet.toggle([4], 4) == [4])
    }
    @Test func ignoresOutOfRange() {
        #expect(DaySet.toggle([1], 0) == [1])
        #expect(DaySet.toggle([1], 8) == [1])
    }
    @Test func labels() {
        #expect(DaySet.labels.count == 7)
        #expect(DaySet.labels.first == "Pzt")
        #expect(DaySet.labels.last == "Paz")
    }
}

@Suite struct SpeedLimitTests {
    @Test func parsesPositiveInts() {
        #expect(SpeedLimit.parseKBps("500") == 500)
        #expect(SpeedLimit.parseKBps(" 5 000 ") == 5000)
        #expect(SpeedLimit.parseKBps("5.000") == 5000)
    }
    @Test func rejectsNonPositiveAndGarbage() {
        for s in ["", "0", "-5", "abc", "1,5", "1e3"] { #expect(SpeedLimit.parseKBps(s) == nil, "\(s)") }
    }
    @Test func rejectsAboveTheCap() {
        #expect(SpeedLimit.parseKBps("10000000") == 10_000_000)
        for s in ["10000001", "9223372036854775807", "99999999999999999999"] { #expect(SpeedLimit.parseKBps(s) == nil, "\(s)") }
    }
    @Test func bytesPerSecondNeverOverflows() {
        #expect(SpeedLimit.bytesPerSecond(kbps: 500) == 500_000)
        #expect(SpeedLimit.bytesPerSecond(kbps: Int.max) == Int64(SpeedLimit.maxKBps) * 1000)  // a corrupt engine value
        #expect(SpeedLimit.bytesPerSecond(kbps: -3) == 0)
    }
    @Test func problemNamesTheRange() {
        #expect(SpeedLimit.problem(1) == nil && SpeedLimit.problem(SpeedLimit.maxKBps) == nil)
        #expect(SpeedLimit.problem(0) != nil && SpeedLimit.problem(SpeedLimit.maxKBps + 1) != nil)
    }
}

@Suite struct HostPatternTests {
    @Test func accepts() {
        for s in ["example.com", "*.example.com", "files.example.co.uk", "localhost", "192.168.1.5", "my-host.example.com"] {
            #expect(HostPattern.isValid(s), "\(s)")
        }
    }
    @Test func rejects() {
        for s in ["", "*", "*.", "*example.com", "a.*.com", "https://example.com", "example.com/path", "exa mple.com",
                  "-bad.com", "bad-.com", "a..b", ".example.com", "example.com:8080", "**.example.com"] {
            #expect(!HostPattern.isValid(s), "\(s)")
        }
    }
    @Test func matchesWildcard() {
        #expect(HostPattern.matches("*.example.com", host: "a.example.com"))
        #expect(HostPattern.matches("*.example.com", host: "example.com"))
        #expect(!HostPattern.matches("*.example.com", host: "badexample.com"))
        #expect(HostPattern.matches("example.com", host: "EXAMPLE.com"))
        #expect(!HostPattern.matches("example.com", host: "a.example.com"))
    }
}

@Suite struct TokenListTests {
    @Test func extensionsNormalize() {
        #expect(TokenList.parse("ZIP, .rar  7z\niso,zip", extensions: true) == ["zip", "rar", "7z", "iso"])
    }
    @Test func sitesKeepWildcards() {
        #expect(TokenList.parse("*.a.com b.com\n*.a.com", extensions: false) == ["*.a.com", "b.com"])
    }
    @Test func emptyAndText() {
        #expect(TokenList.parse("  ,, ", extensions: true).isEmpty)
        #expect(TokenList.text(["zip", "rar"]) == "zip rar")
    }
}

@Suite struct SettingsCheckTests {
    func base() -> Settings {
        Settings(maxConns: 8, downloadDir: "~/Downloads", categoryFolders: false, duplicate: "number",
                 speedLimitOn: false, speedLimitKBps: 500)
    }
    @Test func okByDefault() { #expect(SettingsCheck.problem(base()) == nil) }
    @Test func rejectsBadSpeed() {
        var s = base(); s.speedLimitKBps = 0
        #expect(SettingsCheck.problem(s) != nil)
        s.speedLimitKBps = Int.max
        #expect(SettingsCheck.problem(s) != nil)
    }
    @Test func rejectsMaxConnsOutsideChoices() {
        var s = base(); s.maxConns = 33
        #expect(SettingsCheck.problem(s) != nil)
        s.maxConns = 3  // engine may send any 1...32; only the range matters
        #expect(SettingsCheck.problem(s) == nil)
    }
    @Test func rejectsEmptyDir() {
        var s = base(); s.downloadDir = "  "
        #expect(SettingsCheck.problem(s) != nil)
    }
    @Test func rejectsBadLoginHost() {
        var s = base(); s.logins = [Login(host: "http://x", user: "u")]
        #expect(SettingsCheck.problem(s) != nil)
        s.logins = [Login(host: "*.x.com", user: "u")]
        #expect(SettingsCheck.problem(s) == nil)
    }
    @Test func connChoicesIncludeCurrent() {
        #expect(ConnChoices.values == [1, 2, 4, 8, 16, 24, 32])
        #expect(ConnChoices.values(including: 8) == [1, 2, 4, 8, 16, 24, 32])
        #expect(ConnChoices.values(including: 6) == [1, 2, 4, 6, 8, 16, 24, 32])
    }
}

@Suite struct QueueCheckTests {
    @Test func cleanName() {
        #expect(QueueCheck.cleanName("  Gece  ") == "Gece")
        #expect(QueueCheck.cleanName("   ") == nil)
    }
    @Test func newIDIsUnique() {
        let id = QueueCheck.newID(existing: ["main", "q2"], random: { 2 })
        #expect(id != "q2" && id != "main" && !id.isEmpty)
    }
    @Test func clamps() {
        #expect(QueueCheck.clampAtOnce(0) == 1)
        #expect(QueueCheck.clampAtOnce(99) == 10)
        #expect(QueueCheck.clampAtOnce(4) == 4)
    }
    @Test func validatesSchedule() {
        var s = Schedule(startOn: true, start: "23:00", stopOn: true, stop: "07:30", days: OrEmpty(wrappedValue: [1]), onDone: "none")
        #expect(QueueCheck.problem(s) == nil)
        s.start = "25:00"
        #expect(QueueCheck.problem(s) != nil)
        s.start = "23:00"; s.days = []
        #expect(QueueCheck.problem(s) != nil)
    }
}
