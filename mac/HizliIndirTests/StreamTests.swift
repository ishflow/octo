import Foundation
import Testing

// Video capture (VG1-design §4): decoding, quality/audio pick, segment progress, hideVideoButton. Old and new JSON both decode.

private func decode<T: Decodable>(_ json: String) throws -> T { try JSONDecoder().decode(T.self, from: Data(json.utf8)) }

private func item(_ extra: String = "", state: String = "downloading", size: Int64 = -1, got: Int = 0) throws -> Item {
    try decode("""
    {"id":"s1","url":"https://v.example/master.m3u8","name":"video.mp4","dir":"/tmp","category":"Video","queue":"main","state":"\(state)",
     "size":\(size),"got":\(got),"speed":0,"eta":-1,"resumable":true,"error":"","description":"",
     "added":"2026-10-20T10:00:00Z","finished":null,"lastTry":null,"maxConns":8,"conns":[]\(extra)}
    """)
}

private let infoJSON = """
{"kind":"dash","container":"mp4","live":false,"protected":false,"duration":252.5,
 "variants":[{"id":"v1","label":"1080p","codecs":"avc1.640028","width":1920,"height":1080,"bandwidth":4500000,"audio":""},
             {"id":"v0","label":"480p","codecs":"avc1.4d401e","width":854,"height":480,"bandwidth":850000,"audio":""}],
 "audios":[{"id":"a0","name":"Türkçe","lang":"tr","default":false},{"id":"a1","name":"English","lang":"en","default":true}]}
"""

@Suite struct StreamDecodingTests {
    @Test func oldItemHasNoStreamOrNote() throws {
        let i = try item()
        #expect(i.stream == nil && i.note == nil)
    }
    @Test func newItemCarriesStreamAndNote() throws {
        let i = try item(#","stream":{"kind":"hls","video":"v2","audio":"a0","label":"720p","segments":340,"segDone":120,"phase":"merging"},"note":"Ses ayrı dosyada kaldı (birleştirilemedi): x""#)
        #expect(i.stream == StreamSpec(kind: "hls", video: "v2", audio: "a0", label: "720p", segments: 340, segDone: 120, phase: "merging"))
        #expect(i.note == "Ses ayrı dosyada kaldı (birleştirilemedi): x")
    }
    @Test func streamSpecEncodesOnlyFilledFields() throws {
        let data = try JSONEncoder().encode(StreamSpec(kind: "dash", video: "v1", audio: nil, label: "1080p"))
        let o = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        #expect(o["kind"] as? String == "dash" && o["video"] as? String == "v1" && o["label"] as? String == "1080p")
        #expect(o["audio"] == nil && o["segments"] == nil && o["phase"] == nil)
    }
    @Test func progressWithAndWithoutSegments() throws {
        let old: ProgressUpdate = try decode(#"{"id":"a","state":"downloading","got":5,"size":10,"speed":1,"eta":5,"conns":null}"#)
        #expect(old.segDone == nil && old.segments == nil && old.phase == nil)
        let new: ProgressUpdate = try decode(#"{"id":"a","state":"downloading","got":5,"size":-1,"speed":1,"eta":5,"conns":[],"segDone":120,"segments":340,"phase":"merging"}"#)
        #expect(new.segDone == 120 && new.segments == 340 && new.phase == "merging")
    }
    @Test func probeReplyKind() throws {
        let plain: ProbeReply = try decode(#"{"name":"a.zip","size":5,"resumable":true}"#)
        #expect(plain.kind == nil)
        let hls: ProbeReply = try decode(#"{"name":"master.m3u8","size":-1,"resumable":false,"kind":"hls"}"#)
        #expect(hls.kind == "hls")
    }
    @Test func streamInfoDecodes() throws {
        let info: StreamInfo = try decode(infoJSON)
        #expect(info.kind == "dash" && info.container == "mp4" && !info.live && !info.protected && info.duration == 252.5)
        #expect(info.variants.map(\.id) == ["v1", "v0"] && info.variants[0].bandwidth == 4_500_000 && info.variants[0].height == 1080)
        #expect(info.audios.map(\.id) == ["a0", "a1"] && info.audios[1].default && info.audios[0].lang == "tr")
    }
    @Test func streamInfoNullListsAreEmpty() throws {
        let info: StreamInfo = try decode(#"{"kind":"hls","container":"ts","live":true,"protected":false,"duration":0,"variants":null}"#)
        #expect(info.variants.isEmpty && info.audios.isEmpty && info.live)
    }
}

@Suite struct HideVideoButtonTests {
    private let base = #"{"maxConns":8,"downloadDir":"~/Downloads","categoryFolders":false,"duplicate":"number","speedLimitOn":false,"speedLimitKBps":500"#

    @Test func missingMeansButtonShown() throws {
        let s: Settings = try decode(base + "}")
        #expect(s.hideVideoButton == nil && VideoButton.shown(s))
    }
    @Test func trueHidesTheButton() throws {
        let s: Settings = try decode(base + #","hideVideoButton":true}"#)
        #expect(!VideoButton.shown(s))
    }
    @Test func settingSwitchWritesTheInverse() throws {
        var s: Settings = try decode(base + "}")
        VideoButton.set(&s, shown: false)
        #expect(s.hideVideoButton == true)
        VideoButton.set(&s, shown: true)
        #expect(s.hideVideoButton == false && VideoButton.shown(s))
    }
    @Test func engineValueSurvivesTheWriteBack() throws {
        let s: Settings = try decode(base + #","hideVideoButton":true}"#)
        let o = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(s)) as? [String: Any])
        #expect(o["hideVideoButton"] as? Bool == true)
    }
    @Test func oldEngineGetsNoNewKey() throws {
        let s: Settings = try decode(base + "}")
        let o = try #require(try JSONSerialization.jsonObject(with: JSONEncoder().encode(s)) as? [String: Any])
        #expect(o["hideVideoButton"] == nil)
    }
}

@Suite struct StreamPickTests {
    private func info(_ json: String = infoJSON) throws -> StreamInfo { try decode(json) }

    @Test func defaultsToHighestQualityAndDefaultAudio() throws {
        let i = try info()
        let s = StreamPick.spec(i, video: nil, audio: nil)
        #expect(s == StreamSpec(kind: "dash", video: "v1", audio: "a1", label: "1080p"))
    }
    @Test func chosenVariantAndAudioAreUsed() throws {
        let i = try info()
        let s = StreamPick.spec(i, video: "v0", audio: "a0")
        #expect(s.video == "v0" && s.audio == "a0" && s.label == "480p")
    }
    @Test func hlsVariantUsesItsOwnAudioGroup() throws {
        let i = try info(#"{"kind":"hls","container":"mp4","live":false,"protected":false,"duration":10,"variants":[{"id":"v0","label":"720p","bandwidth":2000000,"audio":"a1"},{"id":"v1","label":"360p","bandwidth":500000,"audio":""}],"audios":[{"id":"a0","name":"Ana","lang":"tr","default":true},{"id":"a1","name":"Yorum","lang":"tr","default":false}]}"#)
        #expect(StreamPick.spec(i, video: "v0", audio: nil).audio == "a1")
        #expect(StreamPick.spec(i, video: "v1", audio: nil).audio == nil)  // embedded sound: audio left empty
        #expect(!StreamPick.showsAudioPicker(i, video: "v1"))
        #expect(StreamPick.showsAudioPicker(i, video: "v0"))
    }
    @Test func noVariantsMeansNoVideoId() throws {
        let i = try info(#"{"kind":"hls","container":"ts","live":false,"protected":false,"duration":0}"#)
        let s = StreamPick.spec(i, video: nil, audio: nil)
        #expect(s == StreamSpec(kind: "hls") && s.video == nil && s.audio == nil)
    }
    @Test func singleAudioNeedsNoPicker() throws {
        let i = try info(#"{"kind":"dash","container":"mp4","live":false,"protected":false,"duration":9,"variants":[{"id":"v0","label":"720p","bandwidth":1,"audio":""}],"audios":[{"id":"a0","name":"","lang":"","default":true}]}"#)
        #expect(!StreamPick.showsAudioPicker(i, video: "v0"))
        #expect(StreamPick.spec(i, video: nil, audio: nil).audio == "a0")
    }
    @Test func blockersAreTurkishAndLiveWins() throws {
        #expect(StreamPick.blocker(try info()) == nil)
        let drm = try info(#"{"kind":"hls","container":"mp4","live":false,"protected":true,"duration":0}"#)
        #expect(StreamPick.blocker(drm) == "Bu video korumalı (DRM), indirilemez")
        let live = try info(#"{"kind":"hls","container":"ts","live":true,"protected":false,"duration":0}"#)
        #expect(StreamPick.blocker(live) == "Canlı yayın indirilemez")
    }
    @Test func variantAndAudioTitles() throws {
        let i = try info()
        #expect(StreamPick.title(i.variants[0]) == "1080p · 4,5 Mb/sn")
        #expect(StreamPick.title(i.variants[1]) == "480p · 850 kb/sn")
        #expect(StreamPick.title(StreamVariant(id: "v3", label: "", height: 360)) == "360p")
        #expect(StreamPick.title(StreamVariant(id: "v3")) == "Varyant v3")
        #expect(StreamPick.title(i.audios[0]) == "Türkçe (tr)")
        #expect(StreamPick.title(StreamAudio(id: "a2", name: "", lang: "de", default: false)) == "de")
        #expect(StreamPick.title(StreamAudio(id: "a2", name: "", lang: "", default: false)) == "Ses a2")
    }
    @Test func fileNameFollowsContainer() throws {
        #expect(StreamPick.fileName(try info()) == "video.mp4")
        #expect(StreamPick.fileName(try info(#"{"kind":"hls","container":"ts","live":false,"protected":false,"duration":0}"#)) == "video.ts")
        let audioOnly = try info(#"{"kind":"dash","container":"mp4","live":false,"protected":false,"duration":0,"audios":[{"id":"a0","name":"","lang":"","default":true}]}"#)
        #expect(StreamPick.fileName(audioOnly) == "video.m4a")
    }
    @Test func durationText() {
        #expect(StreamPick.duration(0) == nil)
        #expect(StreamPick.duration(252.5) == "4:13")
        #expect(StreamPick.duration(3723) == "1:02:03")
    }
    @Test func absurdDurationsFromHostileManifestsAreUnknown() {
        for s in [1e300, .infinity, -.infinity, .nan, 1e7] { #expect(StreamPick.duration(s) == nil) }
    }
}

@Suite struct StreamProgressTests {
    @Test func segmentLine() {
        #expect(StreamProgress.line(StreamSpec(kind: "hls", segments: 340, segDone: 120)) == "Parça 120/340")
        #expect(StreamProgress.line(StreamSpec(kind: "hls", segments: 340, segDone: 340, phase: "merging")) == "Birleştiriliyor…")
        #expect(StreamProgress.line(StreamSpec(kind: "hls")) == nil)
    }
    @Test func fractionFromSegmentsWhenSizeUnknown() throws {
        var i = try item(#","stream":{"kind":"hls","segments":200,"segDone":50}"#)
        #expect(i.fraction == 0.25)
        i.size = 1000; i.got = 500  // known size: bytes win
        #expect(i.fraction == 0.5)
        #expect(try item().fraction == nil)
    }
    @Test @MainActor func progressEventsFillTheItemsStream() throws {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        store.apply(.item(try item(#","stream":{"kind":"hls","label":"720p","segments":200}"#)))
        store.apply(.progress([ProgressUpdate(id: "s1", state: .downloading, got: 900, size: -1, speed: 1, eta: 4, conns: OrEmpty(),
                                              segDone: 120, segments: 340)]))
        #expect(store.items[0].stream?.segDone == 120 && store.items[0].stream?.segments == 340 && store.items[0].stream?.label == "720p")
        store.apply(.progress([ProgressUpdate(id: "s1", state: .downloading, got: 900, size: -1, speed: 0, eta: -1, conns: OrEmpty(),
                                              segDone: 340, segments: 340, phase: "merging")]))
        #expect(store.items[0].stream?.phase == "merging")
        #expect(StatusLabel.of(store.items[0])?.text == "Birleştiriliyor…")
        // phase is omitted once merging is over: it must clear
        store.apply(.progress([ProgressUpdate(id: "s1", state: .done, got: 900, size: 900, speed: 0, eta: -1, conns: OrEmpty(),
                                              segDone: 340, segments: 340)]))
        #expect(store.items[0].stream?.phase == nil || store.items[0].stream?.phase == "")
    }
    @Test @MainActor func progressWithoutSegmentsKeepsKnownSegments() throws {
        // overlapping access to items[i] (read + write in one statement) aborts in Debug
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        store.apply(.item(try item(#","stream":{"kind":"hls","segments":200}"#)))
        store.apply(.progress([ProgressUpdate(id: "s1", state: .downloading, got: 5, size: -1, speed: 1, eta: 1, conns: OrEmpty())]))
        #expect(store.items[0].stream?.segments == 200)
    }
    @Test @MainActor func progressLeavesPlainItemsAlone() throws {
        let store = Store(client: EngineClient(socketPath: "/nonexistent", helper: nil))
        store.apply(.item(try item()))
        store.apply(.progress([ProgressUpdate(id: "s1", state: .downloading, got: 5, size: 10, speed: 1, eta: 1, conns: OrEmpty())]))
        #expect(store.items[0].stream == nil)
    }
    @Test func downloadingLabelWithoutFractionStaysEngineText() throws {
        #expect(StatusLabel.of(try item())?.text == "Veri alınıyor…")
    }
}

@Suite struct StreamCommandTests {
    @Test @MainActor func probeStreamAndAddWithStream() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let info = try await store.probeStream(url: "https://v.example/master.m3u8")
        #expect(info.kind == "hls" && info.variants.map(\.id) == ["v0"] && info.audios.isEmpty)
        let spec = StreamPick.spec(info, video: nil, audio: nil)
        let added = try await store.add(url: "https://v.example/master.m3u8", start: true, stream: spec)
        #expect(added.stream?.video == "v0" && added.stream?.kind == "hls")
    }
    @Test @MainActor func addWithoutStreamSendsNoStreamKey() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        let added = try await store.add(url: "https://x/y.iso", start: true)
        #expect(added.stream == nil)
    }
    @Test @MainActor func probeStreamErrorIsEngineText() async throws {
        let server = try FakeEngine(); defer { server.stop() }
        let store = Store(client: EngineClient(socketPath: server.path, helper: nil))
        await #expect(throws: EngineError.server("Bilinmeyen komut")) { _ = try await store.probeStream(url: "https://v.example/old.m3u8") }
    }
}
