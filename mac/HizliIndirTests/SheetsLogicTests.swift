import Foundation
import Testing

@Suite struct StarBatchTests {
    @Test func numbersZeroPaddedToDigits() {
        let r = StarBatch.expand("https://example.com/galeri/resim*.jpg", letters: false, from: "1", to: "120", digits: 3)
        #expect(r?.count == 120)
        #expect(r?.first == "https://example.com/galeri/resim001.jpg")
        #expect(r?[1] == "https://example.com/galeri/resim002.jpg")
        #expect(r?.last == "https://example.com/galeri/resim120.jpg")
    }
    @Test func numbersWithoutPadding() {
        #expect(StarBatch.expand("https://x.com/a*", letters: false, from: "9", to: "10", digits: 1)
                == ["https://x.com/a9", "https://x.com/a10"])
    }
    @Test func letters() {
        #expect(StarBatch.expand("https://x.com/f*.zip", letters: true, from: "a", to: "c", digits: 3)
                == ["https://x.com/fa.zip", "https://x.com/fb.zip", "https://x.com/fc.zip"])
        #expect(StarBatch.expand("https://x.com/f*", letters: true, from: "A", to: "B", digits: 1) == ["https://x.com/fA", "https://x.com/fB"])
    }
    @Test func rejectsInvalid() {
        let u = "https://x.com/f*.jpg"
        #expect(StarBatch.expand("https://x.com/f.jpg", letters: false, from: "1", to: "3", digits: 1) == nil)  // no star
        #expect(StarBatch.expand("https://x.com/*/f*", letters: false, from: "1", to: "3", digits: 1) == nil)  // two stars
        #expect(StarBatch.expand(u, letters: false, from: "5", to: "1", digits: 1) == nil)  // reversed
        #expect(StarBatch.expand(u, letters: false, from: "x", to: "3", digits: 1) == nil)
        #expect(StarBatch.expand(u, letters: false, from: "1", to: "1001", digits: 1) == nil)  // > 1000
        #expect(StarBatch.expand(u, letters: true, from: "a", to: "Z", digits: 1) == nil)  // mixed case
        #expect(StarBatch.expand(u, letters: true, from: "ab", to: "c", digits: 1) == nil)
        #expect(StarBatch.expand(u, letters: true, from: "1", to: "3", digits: 1) == nil)
        #expect(StarBatch.expand("ftp://x.com/f*", letters: false, from: "1", to: "3", digits: 1) == nil)
    }
    @Test func limitInclusive() {
        #expect(StarBatch.expand("https://x.com/f*", letters: false, from: "1", to: "1000", digits: 1)?.count == 1000)
    }
}

@Suite struct BatchAddTests {
    private let cats = [Category(name: "Programlar", exts: OrEmpty(wrappedValue: ["zip"]), dir: "Programlar")]
    private func urls(_ n: Int) -> [String] { (1...n).map { "https://x.com/f\($0).zip" } }
    private struct Boom: Error {}

    @Test func eachFileGoesToItsCategoryFolder() {
        let e = BatchAdd.entries(["https://x.com/a.zip", "https://x.com/b.txt"], cats: cats, baseDir: "/Users/u/Downloads",
                                 oneFolder: false, categoryFolders: true)
        #expect(e == [.init(url: "https://x.com/a.zip", dir: "/Users/u/Downloads/Programlar", category: "Programlar"),
                      .init(url: "https://x.com/b.txt", dir: "/Users/u/Downloads", category: nil)])
    }
    @Test func oneFolderIgnoresCategoryFoldersAndExpandsTilde() {
        let e = BatchAdd.entries(urls(1), cats: cats, baseDir: "~/Toplu", oneFolder: true, categoryFolders: true)
        #expect(e == [.init(url: "https://x.com/f1.zip", dir: NSHomeDirectory() + "/Toplu", category: "Programlar")])
    }
    @MainActor @Test func addsEverythingInOrderAndReportsProgress() async {
        let es = BatchAdd.entries(urls(3), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var added: [String] = [], seen: [Int] = []
        let r = await BatchAdd.run(es, done: 0, progress: { seen.append($0) }) { added.append($0.url) }
        #expect(added == urls(3) && seen == [1, 2, 3] && r.done == 3 && r.error == nil)
    }
    @MainActor @Test func retryAfterAnErrorSkipsWhatWasAdded() async {
        let es = BatchAdd.entries(urls(5), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var added: [String] = [], failOn: String? = urls(5)[2]
        let add: (BatchAdd.Entry) async throws -> Void = { if $0.url == failOn { throw Boom() }; added.append($0.url) }
        let first = await BatchAdd.run(es, done: 0, progress: { _ in }, add: add)
        #expect(first.done == 2 && first.error is Boom && added == Array(urls(5).prefix(2)))
        failOn = nil
        let second = await BatchAdd.run(es, done: first.done, progress: { _ in }, add: add)
        #expect(second.done == 5 && second.error == nil && added == urls(5))  // no duplicates
    }
    @MainActor @Test func cancellingStopsTheLoop() async {
        let es = BatchAdd.entries(urls(5), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var added: [String] = []
        let r = await Task { @MainActor in
            await BatchAdd.run(es, done: 0, progress: { _ in }) { e in
                added.append(e.url)
                if added.count == 2 { withUnsafeCurrentTask { $0?.cancel() } }
            }
        }.value
        #expect(added == Array(urls(5).prefix(2)) && r.done == 2 && r.error == nil)
    }
    @MainActor @Test func startsTheQueueOnceAfterEverythingIsAdded() async {
        let es = BatchAdd.entries(urls(3), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var log: [String] = []
        let r = await BatchAdd.addToQueue(es, done: 0, startQueue: true, progress: { _ in },
                                          add: { log.append("add " + $0.url) }, start: { log.append("start") })
        #expect(log == urls(3).map { "add " + $0 } + ["start"] && r.done == 3 && r.error == nil)
    }
    @MainActor @Test func addOnlyDoesNotStartTheQueue() async {
        let es = BatchAdd.entries(urls(2), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var started = false
        _ = await BatchAdd.addToQueue(es, done: 0, startQueue: false, progress: { _ in }, add: { _ in }, start: { started = true })
        #expect(!started)
    }
    @MainActor @Test func anErrorOrACancelDoesNotStartTheQueue() async {
        let es = BatchAdd.entries(urls(3), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var started = false
        let failed = await BatchAdd.addToQueue(es, done: 0, startQueue: true, progress: { _ in },
                                               add: { if $0.url == urls(3)[1] { throw Boom() } }, start: { started = true })
        #expect(failed.error is Boom && !started)
        _ = await Task { @MainActor in
            await BatchAdd.addToQueue(es, done: 0, startQueue: true, progress: { _ in },
                                      add: { _ in withUnsafeCurrentTask { $0?.cancel() } }, start: { started = true })
        }.value
        #expect(!started)
    }
    @MainActor @Test func doneBeyondTheEndAddsNothing() async {
        let es = BatchAdd.entries(urls(2), cats: [], baseDir: "/d", oneFolder: true, categoryFolders: false)
        var called = false
        let r = await BatchAdd.run(es, done: 9, progress: { _ in }) { _ in called = true }
        #expect(!called && r.error == nil)
    }
}

@Suite struct PathCrumbTests {
    @Test func lastTwoFoldersUnderHome() {
        #expect(PathCrumb.parts("/Users/u/Downloads/Programlar", home: "/Users/u") == ["Downloads", "Programlar"])
        #expect(PathCrumb.parts("/Users/u/Downloads", home: "/Users/u") == ["Downloads"])
    }
    @Test func homeItselfAndOutsideHome() {
        #expect(PathCrumb.parts("/Users/u", home: "/Users/u") == ["u"])
        #expect(PathCrumb.parts("/Volumes/Big/Apps/Iso", home: "/Users/u") == ["Apps", "Iso"])
    }
}

@Suite struct DeletePromptTests {
    @Test func oneUnfinishedAmongMany() {
        let t = DeletePrompt.text(count: 2, unfinished: 1)
        #expect(t.title == "2 indirme listeden kaldırılsın mı?")
        #expect(t.message == "Seçilenlerden biri tamamlanmadı. Yarım kalan parçaları silinir ve bu indirme baştan başlamak zorunda kalır.")
    }
    @Test func severalUnfinished() {
        #expect(DeletePrompt.text(count: 3, unfinished: 2).message
                == "Seçilenlerden 2 tanesi tamamlanmadı. Yarım kalan parçaları silinir ve bu indirmeler baştan başlamak zorunda kalır.")
    }
    @Test func singleUnfinished() {
        #expect(DeletePrompt.text(count: 1, unfinished: 1).message
                == "Bu indirme tamamlanmadı. Yarım kalan parçaları silinir ve baştan başlamak zorunda kalır.")
    }
    @Test func settingsSwitchIsInverseOfSkip() {
        let d = UserDefaults(suiteName: "DeletePromptTests-\(UUID().uuidString)")!
        #expect(DeletePrompt.asks(d))
        DeletePrompt.setAsks(false, d)
        #expect(d.bool(forKey: DeletePrompt.skipKey) && !DeletePrompt.asks(d))
        DeletePrompt.setAsks(true, d)
        #expect(!d.bool(forKey: DeletePrompt.skipKey))
    }
    @Test func askingAgainForgetsTheSavedTrashAnswer() {
        let d = UserDefaults(suiteName: "DeletePromptTests-\(UUID().uuidString)")!
        d.set(true, forKey: DeletePrompt.trashKey)  // "Bir daha sorma" + Çöp'e taşı, saved earlier
        DeletePrompt.setAsks(true, d)
        DeletePrompt.setAsks(false, d)
        #expect(!d.bool(forKey: DeletePrompt.trashKey))  // switching off later must not resurrect it
    }
    @Test func allFinished() {
        #expect(DeletePrompt.text(count: 2, unfinished: 0).message == "İndirilen dosyalar diskte kalır.")
    }
}

@Suite struct DuplicatePromptTests {
    @Test func inListOffersResumeFirst() {
        let b = DuplicatePrompt.buttons(inList: true, numbered: "a (2).iso")
        #expect(b.map(\.title) == ["Kaldığı yerden devam et", "Numaralı adla ekle (a (2).iso)", "Üzerine yaz", "İptal"])
        #expect(b.map(\.choice) == [.resume, .number, .overwrite, nil])
        #expect(DuplicatePrompt.title(inList: true) == "Bu dosya listede zaten var")
    }
    @Test func onDiskHasNoResume() {
        #expect(DuplicatePrompt.buttons(inList: false, numbered: "a (2).iso").map(\.choice) == [.number, .overwrite, nil])
        #expect(DuplicatePrompt.title(inList: false) == "Bu dosya kaydetme yerinde zaten var")
    }
    @Test func rememberedSettingValue() {
        #expect(DuplicatePrompt.setting(for: .number) == "number")
        #expect(DuplicatePrompt.setting(for: .overwrite) == "overwrite")
        #expect(DuplicatePrompt.setting(for: .resume) == nil)  // the engine has no "resume" policy
    }
}

@Suite struct SiteAuthTests {
    @Test func basicHeaderFromUserAndPassword() {
        let h = SiteAuth.headers(user: "ali", password: "gizli", url: "https://x.com/a.zip")
        #expect(h == ["Authorization": "Basic " + Data("ali:gizli".utf8).base64EncodedString()])
    }
    @Test func utf8Credentials() {
        let h = SiteAuth.headers(user: "şükrü", password: "parola:ğ", url: "https://x.com/a.zip")
        #expect(h["Authorization"] == "Basic " + Data("şükrü:parola:ğ".utf8).base64EncodedString())
    }
    @Test func nothingWithoutUser() {
        #expect(SiteAuth.headers(user: "", password: "x", url: "https://x.com/a").isEmpty)
        #expect(SiteAuth.headers(user: "  ", password: "", url: "https://x.com/a").isEmpty)
        #expect(SiteAuth.problem(user: "", password: "x", url: "http://x.com/a") == nil)
    }
    @Test func plainHTTPNeverSendsPassword() {
        for u in ["http://x.com/a.zip", " HTTP://x.com/a*.zip"] {
            #expect(SiteAuth.headers(user: "ali", password: "gizli", url: u).isEmpty)
            #expect(SiteAuth.problem(user: "ali", password: "gizli", url: u)?.contains("http") == true)
        }
        #expect(SiteAuth.problem(user: "ali", password: "gizli", url: "https://x.com/a") == nil)
    }
    @Test func colonInUserIsRefused() {
        #expect(SiteAuth.headers(user: "a:b", password: "x", url: "https://x.com/a").isEmpty)
        #expect(SiteAuth.problem(user: "a:b", password: "x", url: "https://x.com/a") != nil)
    }
    @Test func addArgsCarryHeadersOnlyWhenGiven() {
        let h = ["Authorization": "Basic YTpi"]
        #expect(Store.addArgs(url: "https://x.com/a", headers: h, start: true)["headers"] as? [String: String] == h)
        #expect(Store.addArgs(url: "https://x.com/a", headers: [:], start: true)["headers"] == nil)
        #expect(Store.addArgs(url: "https://x.com/a", start: false)["start"] as? Bool == false)
    }
}

@Suite struct RefreshOfferTests {
    private func item(_ id: String, _ state: String, refreshOf: String? = nil) -> Item {
        let extra = refreshOf.map { #","refreshOf":"\#($0)""# } ?? ""
        let json = """
        {"id":"\(id)","url":"https://x/\(id)","name":"a.zip","dir":"/tmp","category":"","queue":"main","state":"\(state)",
         "size":100,"got":40,"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
         "added":"2026-10-19T20:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":null\(extra)}
        """
        return try! JSONDecoder().decode(Item.self, from: Data(json.utf8))
    }

    @Test func pendingPairsOfferWithUnfinishedItem() {
        let old = item("d1", "paused"), offer = item("d2", "paused", refreshOf: "d1")
        let p = RefreshOffer.pending(in: [old, item("d3", "queued"), offer], except: [])
        #expect(p?.offer.id == "d2" && p?.old.id == "d1")
        #expect(RefreshOffer.pending(in: [old, offer], except: ["d2"]) == nil)  // asked once
        #expect(RefreshOffer.pending(in: [offer], except: []) == nil)  // unfinished item was removed
        #expect(RefreshOffer.pending(in: [old, item("d2", "paused", refreshOf: "")], except: []) == nil)
        #expect(item("d1", "paused").refreshOf == nil)  // older engine: no field
    }
}
