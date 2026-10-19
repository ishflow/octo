import test from "node:test";
import assert from "node:assert/strict";
import {
  classifyMedia, addRecord, pickCandidates, variantOptions, streamSpec, videoFilename, isVideoSrc, MAX_RECORDS, isLocalHost, localBlocked,
} from "../lib/media.mjs";

test("classifyMedia: content type wins, then the extension; HTML and errors are never media", () => {
  assert.equal(classifyMedia("https://a.test/x", "application/vnd.apple.mpegurl"), "hls");
  assert.equal(classifyMedia("https://a.test/x", "audio/x-mpegURL; charset=utf-8"), "hls");
  assert.equal(classifyMedia("https://a.test/x", "application/dash+xml"), "dash");
  assert.equal(classifyMedia("https://a.test/v/master.m3u8?sig=1", ""), "hls");
  assert.equal(classifyMedia("https://a.test/v/master.m3u8", "application/octet-stream"), "hls");
  assert.equal(classifyMedia("https://a.test/v/stream.mpd", "text/plain"), "dash");
  assert.equal(classifyMedia("https://a.test/play?format=mpd", undefined), "dash");
  assert.equal(classifyMedia("https://a.test/v/master.m3u8", "text/html"), null);
  assert.equal(classifyMedia("https://a.test/v/master.m3u8", "application/vnd.apple.mpegurl", 404), null);
  assert.equal(classifyMedia("https://a.test/v/master.m3u8", "", 302), null);
  assert.equal(classifyMedia("https://a.test/m3u8-player.js", "application/javascript"), null);
  assert.equal(classifyMedia("https://a.test/api?q=m3u8", ""), null);
});

test("addRecord: one entry per URL; over a cap the oldest goes, from the frame with most records", () => {
  let list = addRecord([], { url: "https://a.test/1.m3u8", kind: "hls", frameId: 0 });
  list = addRecord(list, { url: "https://a.test/1.m3u8", kind: "hls", frameId: 3 });
  assert.equal(list.length, 1);
  assert.equal(list[0].frameId, 0);
  for (let i = 0; i < 40; i++) list = addRecord(list, { url: `https://a.test/${i}.mpd`, kind: "dash", frameId: 0 });
  assert.equal(list.length, 10); // per frame
  assert.equal(list.at(-1).url, "https://a.test/39.mpd"); // an SPA's newest video still gets in
  // an ad frame (or many) flooding records cannot push out the player's
  let tab = addRecord([], { url: "https://p.test/master.m3u8", kind: "hls", frameId: 0 });
  for (let f = 1; f <= 5; f++) for (let i = 0; i < 20; i++) tab = addRecord(tab, { url: `https://ad.test/${f}/${i}.m3u8`, kind: "hls", frameId: f });
  assert.equal(tab.length, MAX_RECORDS);
  assert.equal(tab[0].url, "https://p.test/master.m3u8");
});

test("addRecord: a huge address is not stored (storage.session is shared by every tab)", () => {
  const long = `https://a.test/${"x".repeat(5000)}.m3u8`;
  assert.deepEqual(addRecord([], { url: long, kind: "hls", frameId: 0 }), []);
  assert.deepEqual(addRecord([], { url: "https://a.test/1.m3u8", kind: "hls", frameId: 0, documentUrl: long }), []);
});

test("pickCandidates: the frame's own records first, the first of each kind leads, at most 3", () => {
  const recs = [
    { url: "m1", kind: "hls", frameId: 0 },
    { url: "m2", kind: "hls", frameId: 0 },
    { url: "d1", kind: "dash", frameId: 0 },
    { url: "f1", kind: "hls", frameId: 5 },
    { url: "m3", kind: "hls", frameId: 0 },
  ];
  assert.deepEqual(pickCandidates(recs, 0).map((r) => r.url), ["m1", "d1", "m2"]);
  assert.deepEqual(pickCandidates(recs, 5).map((r) => r.url), ["f1"]);
  assert.deepEqual(pickCandidates(undefined, 0), []);
});

test("pickCandidates: a frame that saw nothing gets only same-origin frames' records", () => {
  const recs = [
    { url: "top", kind: "hls", frameId: 0, documentUrl: "https://site.test/watch" },
    { url: "ad", kind: "hls", frameId: 4, documentUrl: "https://ads.test/x" },
  ];
  assert.deepEqual(pickCandidates(recs, 7, "https://site.test").map((r) => r.url), ["top"]);
  assert.deepEqual(pickCandidates(recs, 9, "https://evil.test"), []); // a third-party iframe cannot offer the page's stream
  assert.deepEqual(pickCandidates(recs, 9, "null"), []);
  assert.deepEqual(pickCandidates(recs, 9), []);
});

test("pickCandidates: records fetched by the current document lead (an SPA's next video)", () => {
  const recs = [
    { url: "old", kind: "hls", frameId: 0, documentUrl: "https://site.test/watch?v=1" },
    { url: "new", kind: "hls", frameId: 0, documentUrl: "https://site.test/watch?v=2" },
  ];
  assert.deepEqual(pickCandidates(recs, 0, "https://site.test", "https://site.test/watch?v=2#t=3").map((r) => r.url), ["new", "old"]);
  assert.deepEqual(pickCandidates(recs, 0, "https://site.test", "https://site.test/other").map((r) => r.url), ["old", "new"]);
});

const info = {
  kind: "hls", container: "mp4", live: false, protected: false, duration: 60,
  variants: [
    { id: "v0", label: "360p", width: 640, height: 360, bandwidth: 800000, audio: "" },
    { id: "v2", label: "1080p", width: 1920, height: 1080, bandwidth: 4500000, audio: "a1" },
    { id: "v1", label: "", width: 1280, height: 720, bandwidth: 2000000, audio: "" },
  ],
  audios: [{ id: "a0", name: "English", lang: "en", default: false }, { id: "a1", name: "Türkçe", lang: "tr", default: true }],
};

test("variantOptions: highest first, Turkish decimal comma, at most 8", () => {
  assert.deepEqual(variantOptions(info), [
    { id: "v2", label: "1080p · 4,5 Mb/sn" },
    { id: "v1", label: "720p · 2 Mb/sn" },
    { id: "v0", label: "360p · 0,8 Mb/sn" },
  ]);
  const many = { ...info, variants: Array.from({ length: 12 }, (_, i) => ({ id: `v${i}`, height: 100 + i, bandwidth: 0 })) };
  const opts = variantOptions(many);
  assert.equal(opts.length, 8);
  assert.deepEqual(opts[0], { id: "v11", label: "111p" });
  assert.deepEqual(variantOptions({ ...info, variants: [] }), [{ id: "", label: "Videoyu indir" }]);
});

test("streamSpec: the variant's own audio, else the default rendition", () => {
  assert.deepEqual(streamSpec(info, "v2"), { kind: "hls", video: "v2", audio: "a1", label: "1080p" });
  assert.deepEqual(streamSpec(info, "v1"), { kind: "hls", video: "v1", audio: "a1", label: "720p" });
  assert.deepEqual(streamSpec({ ...info, audios: [] }, "v0"), { kind: "hls", video: "v0", audio: "", label: "360p" });
  assert.deepEqual(streamSpec({ ...info, kind: "dash", variants: [] }, ""), { kind: "dash", video: "", audio: "a1", label: "" });
  assert.equal(streamSpec(info, "v9"), null);
});

test("videoFilename: tab title, cleaned and cut, quality and container extension", () => {
  assert.equal(videoFilename("Kedi: komik/video", "1080p", "mp4"), "Kedi komik video (1080p).mp4");
  assert.equal(videoFilename("", "", "ts"), "video.ts");
  assert.equal(videoFilename("a".repeat(100) + "\0", "720p", "mp4"), "a".repeat(80) + " (720p).mp4");
});

test("isVideoSrc: only http(s) and blob addresses", () => {
  assert.equal(isVideoSrc("https://a.test/v.mp4"), true);
  assert.equal(isVideoSrc("blob:https://a.test/5f1c"), true);
  assert.equal(isVideoSrc("data:video/mp4;base64,AA"), false);
  assert.equal(isVideoSrc("javascript:alert(1)"), false);
  assert.equal(isVideoSrc(42), false);
});

test("isLocalHost: loopback, private, link-local, CGNAT, multicast, mapped/NAT64 IPv6 and local names", () => {
  const h = (u) => new URL(u).hostname;
  for (const u of [
    "http://127.0.0.1/", "http://2130706433/", "http://127.1/", "http://10.0.0.1/", "http://172.16.0.1/", "http://172.31.255.1/",
    "http://192.168.1.1/", "http://169.254.1.1/", "http://100.64.0.1/", "http://100.127.1.1/", "http://0.0.0.0/", "http://224.0.0.1/",
    "http://[::1]/", "http://[::]/", "http://[fd00::1]/", "http://[fc00::1]/", "http://[fe80::1]/", "http://[ff02::1]/",
    "http://[::ffff:192.168.1.1]/", "http://[64:ff9b::10.0.0.1]/", "http://localhost/", "http://localhost./", "http://a.localhost/",
    "http://nas.local/", "http://router/", "http://router.lan/", "http://x.home.arpa/", "http://x.internal/",
  ]) assert.equal(isLocalHost(h(u)), true, u);
  for (const u of [
    "https://site.test/", "http://8.8.8.8/", "http://172.32.0.1/", "http://100.128.0.1/", "http://192.169.1.1/", "http://[2001:db8::1]/",
    "http://[::ffff:8.8.8.8]/", "http://[64:ff9b::808:808]/", "http://localhost.example/", "http://local.example/",
  ]) assert.equal(isLocalHost(h(u)), false, u);
  assert.equal(isLocalHost(""), false);
});

test("localBlocked: a local address is used only when every page it came from is local too (V3s M1)", () => {
  assert.equal(localBlocked("http://192.168.1.1/a.m3u8", ["https://site.test/w", "https://site.test/"]), true);
  assert.equal(localBlocked("http://192.168.1.1/a.m3u8", ["http://192.168.1.1/", "https://site.test/"]), true); // local frame, outside tab
  assert.equal(localBlocked("http://192.168.1.1/a.m3u8", ["about:blank", ""]), true);
  assert.equal(localBlocked("http://192.168.1.1/a.m3u8", []), true);
  assert.equal(localBlocked("http://192.168.1.1/a.m3u8", ["http://nas.local/v", ""]), false);
  assert.equal(localBlocked("http://[::1]:8080/a.mp4", ["http://localhost:3000/", "http://localhost:3000/"]), false);
  assert.equal(localBlocked("https://cdn.test/a.m3u8", ["https://site.test/"]), false);
});
