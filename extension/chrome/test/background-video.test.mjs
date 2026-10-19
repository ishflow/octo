// background.js video wiring: manifest detection, video.probe / video.get / video.off. Fake chrome.* and native host.
import test from "node:test";
import assert from "node:assert/strict";

const ev = () => { const fns = []; return { addListener: (f) => fns.push(f), fire: (...a) => fns.map((f) => f(...a)) }; };
const store = (init = {}) => {
  const d = { ...init };
  return {
    d,
    get: async (q) => typeof q === "string" ? { [q]: d[q] } : q == null ? { ...d }
      : Object.fromEntries(Object.entries(q).map(([k, v]) => [k, d[k] ?? v])),
    set: async (o) => void Object.assign(d, o),
    remove: async (k) => void [k].flat().forEach((x) => delete d[x]),
  };
};
const host = []; // messages sent to the native host
let reply = () => ({ type: "ack", accepted: true });
let hostDown = false;
const chrome = (globalThis.chrome = {
  runtime: {
    lastError: null, getURL: (p) => `chrome-extension://x/${p}`,
    sendNativeMessage: (_h, msg, cb) => {
      host.push(msg);
      chrome.runtime.lastError = hostDown ? { message: "host not found" } : null;
      cb(hostDown ? undefined : reply(msg));
      chrome.runtime.lastError = null;
    },
    onInstalled: ev(), onStartup: ev(), onMessage: ev(),
  },
  storage: {
    local: store({ captureExts: ["zip"], excludedSites: [], helloAt: Date.now() + 1e9, hostFeatures: ["stream"] }),
    session: store(),
  },
  action: { setBadgeText: () => {} },
  downloads: { onCreated: ev(), onDeterminingFilename: ev(), search: async () => [], pause: async () => {}, resume: async () => {}, cancel: async () => {}, erase: async () => {} },
  webRequest: { onBeforeRequest: ev(), onHeadersReceived: ev() },
  contextMenus: { onClicked: ev(), removeAll: (cb) => cb(), create: () => {} },
  scripting: { executeScript: async () => [] },
  tabs: { create: async () => {}, query: async () => [], onRemoved: ev() },
  cookies: { getAll: async ({ url }) => (url.startsWith("https://cdn.test/") ? [{ name: "s", value: "1" }] : []) },
});
await import("../background.js");
const tick = () => new Promise((r) => setTimeout(r, 20));

const tab = { id: 4, url: "https://site.test/watch?v=1", title: "Kedi: komik" };
const sender = (frameId = 0, url = tab.url) => ({ tab, frameId, url });
async function send(msg, from = sender()) {
  let out;
  const r = chrome.runtime.onMessage.fire(msg, from, (x) => (out = x));
  await tick();
  return { out, async: r.includes(true) };
}
function sawManifest(url, extra = {}) {
  chrome.webRequest.onHeadersReceived.fire({
    url, tabId: 4, frameId: 0, statusCode: 200, documentUrl: "https://site.test/watch?v=1", type: "xmlhttprequest",
    responseHeaders: [{ name: "Content-Type", value: "application/vnd.apple.mpegurl" }], ...extra,
  });
}
const info = {
  kind: "hls", container: "mp4", live: false, protected: false, duration: 10,
  variants: [{ id: "v1", label: "1080p", height: 1080, bandwidth: 4500000, audio: "" }, { id: "v0", label: "360p", height: 360, bandwidth: 800000, audio: "" }],
  audios: [],
};
const reset = () => { host.length = 0; reply = () => ({ type: "ack", accepted: true }); hostDown = false; };

test("a manifest response is recorded per tab; HTML errors and worker requests are not", async () => {
  sawManifest("https://cdn.test/v/master.m3u8?t=1");
  sawManifest("https://cdn.test/v/master.m3u8?t=1");
  sawManifest("https://cdn.test/v/err.m3u8", { responseHeaders: [{ name: "content-type", value: "text/html" }] });
  sawManifest("https://cdn.test/v/sw.m3u8", { tabId: -1 });
  await tick();
  assert.deepEqual(chrome.storage.session.d["media:4"].map((r) => [r.url, r.kind, r.frameId, r.documentUrl]),
    [["https://cdn.test/v/master.m3u8?t=1", "hls", 0, "https://site.test/watch?v=1"]]);
  assert.equal(chrome.storage.session.d["media:-1"], undefined);
});

test("video.probe on an MSE video probes the manifest with its own cookies and offers qualities", async () => {
  reset();
  reply = (m) => (m.type === "stream.probe" ? { type: "stream", info } : { type: "ack", accepted: true });
  const { out, async } = await send({ type: "video.probe", src: "blob:https://site.test/1", protected: false });
  assert.equal(async, true);
  assert.equal(out.state, "ok");
  assert.deepEqual(out.options, [{ id: "v1", label: "1080p · 4,5 Mb/sn" }, { id: "v0", label: "360p · 0,8 Mb/sn" }]);
  assert.equal(JSON.stringify(out).includes("cdn.test"), false); // addresses stay in the worker
  const p = host.find((m) => m.type === "stream.probe");
  assert.equal(p.url, "https://cdn.test/v/master.m3u8?t=1");
  assert.equal(p.cookies, "s=1");
  assert.equal(p.referrer, "https://site.test/");
  assert.equal(p.pageUrl, tab.url);

  host.length = 0;
  const got = await send({ type: "video.get", token: out.token, id: "v1" });
  assert.deepEqual(got.out, { accepted: true });
  const d = host.find((m) => m.type === "download");
  assert.equal(d.url, "https://cdn.test/v/master.m3u8?t=1");
  assert.deepEqual(d.stream, { kind: "hls", video: "v1", audio: "", label: "1080p" });
  assert.equal(d.filename, "Kedi komik (1080p).mp4");
  assert.equal(d.cookies, "s=1");
  assert.equal(d.size, -1);

  const again = await send({ type: "video.get", token: out.token, id: "v1" }); // a token is used once
  assert.equal(again.out.accepted, false);
});

test("a token works only in the tab and frame it was given to, for an offered option", async () => {
  reset();
  reply = () => ({ type: "stream", info });
  const { out } = await send({ type: "video.probe", src: "blob:https://site.test/1" });
  const other = { tab: { ...tab, id: 9 }, frameId: 0, url: tab.url };
  assert.equal((await send({ type: "video.get", token: out.token, id: "v1" }, other)).out.accepted, false);
  assert.equal((await send({ type: "video.get", token: out.token, id: "v1" }, sender(2))).out.accepted, false);
  assert.equal((await send({ type: "video.get", token: out.token, id: "v7" })).out.accepted, false);
  assert.equal(host.some((m) => m.type === "download"), false);
});

test("protected and live streams, refused candidates and a missing host", async () => {
  reset();
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1", protected: true })).out.state, "protected");
  assert.deepEqual(host, []); // no request at all for a DRM video
  reply = () => ({ type: "stream", info: { ...info, live: true } });
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out.state, "live");
  reply = () => ({ type: "error", error: "Bu video korumalı (DRM), indirilemez" });
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out.state, "protected");
  reply = () => ({ type: "error", error: "Ayrı ses izli TS akışı desteklenmiyor" });
  assert.deepEqual((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out,
    { state: "error", error: "Ayrı ses izli TS akışı desteklenmiyor" });
  reply = () => ({ type: "ack", accepted: false }); // host could not reach the service
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out.state, "offline");
  hostDown = true;
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out.state, "offline");
});

test("an older host without the stream feature: update hint, after a fresh hello", async () => {
  reset();
  chrome.storage.local.d.hostFeatures = [];
  reply = (m) => (m.type === "hello" ? { type: "hello", version: "1.0", captureExts: ["zip"], excludedSites: [] } : null);
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out.state, "old");
  assert.deepEqual(host.map((m) => m.type), ["hello"]);
  reset();
  reply = (m) => (m.type === "hello"
    ? { type: "hello", version: "1.1", features: ["stream"], hideVideoButton: true, captureExts: ["zip"], excludedSites: [] }
    : { type: "stream", info });
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/1" })).out.state, "ok");
  assert.deepEqual(chrome.storage.local.d.hostFeatures, ["stream"]);
  assert.equal(chrome.storage.local.d.hideVideoButton, true);
});

test("a direct file needs no probe; the download is the plain one", async () => {
  reset();
  const { out } = await send({ type: "video.probe", src: "https://media.test/clip.mp4" });
  assert.equal(out.state, "ok");
  assert.deepEqual(out.options, [{ id: "direct", label: "Videoyu indir" }]);
  assert.deepEqual(host, []);
  await send({ type: "video.get", token: out.token, id: "direct" });
  const d = host.find((m) => m.type === "download");
  assert.equal(d.url, "https://media.test/clip.mp4");
  assert.equal(d.stream, undefined);
  assert.equal(d.referrer, "https://site.test/");
});

test("no manifest seen in the tab: 'none'; bad sources and senders without a tab are refused", async () => {
  reset();
  const other = { tab: { ...tab, id: 11 }, frameId: 0, url: "https://x.test/" };
  assert.equal((await send({ type: "video.probe", src: "blob:https://x.test/1" }, other)).out.state, "none");
  assert.equal((await send({ type: "video.probe", src: "javascript:alert(1)" })).out.state, "none");
  const r = await send({ type: "video.probe", src: "blob:https://site.test/1" }, { url: "https://site.test/" });
  assert.equal(r.out, undefined);
  assert.deepEqual(host, []);
});

test("a cross-origin frame that saw no manifest is not offered the page's stream; a same-origin one is", async () => {
  reset();
  reply = () => ({ type: "stream", info });
  assert.equal((await send({ type: "video.probe", src: "blob:https://ads.test/1" }, sender(6, "https://ads.test/slot"))).out.state, "none");
  assert.deepEqual(host, []);
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/2" }, sender(7, "https://site.test/embed"))).out.state, "ok");
  assert.equal(host.find((m) => m.type === "stream.probe").url, "https://cdn.test/v/master.m3u8?t=1");
});

test("video.off closes the sender's own site, whatever the message says", async () => {
  await send({ type: "video.off", host: "evil.test" }, sender(3, "https://player.test/embed/1"));
  assert.deepEqual(chrome.storage.local.d.videoOffSites, ["player.test"]);
});

test("records are dropped when the tab navigates or closes", async () => {
  sawManifest("https://cdn.test/a.m3u8");
  await tick();
  chrome.webRequest.onBeforeRequest.fire({ method: "GET", type: "main_frame", tabId: 4, url: "https://site.test/next" });
  await tick();
  assert.equal(chrome.storage.session.d["media:4"], undefined);
  sawManifest("https://cdn.test/b.m3u8");
  await tick();
  chrome.tabs.onRemoved.fire(4);
  await tick();
  assert.equal(chrome.storage.session.d["media:4"], undefined);
});

test("V3s M1: a local address picked by an outside page is never probed or offered; a local page keeps it", async () => {
  reset();
  reply = () => ({ type: "stream", info });
  assert.equal((await send({ type: "video.probe", src: "http://192.168.1.1/cgi-bin/reboot.mp4" })).out.state, "none");
  assert.equal((await send({ type: "video.probe", src: "http://[::1]:8080/reboot.m3u8" })).out.state, "none");
  sawManifest("http://10.0.0.1/live.m3u8"); // the outside page's player fetched it
  await tick();
  assert.equal((await send({ type: "video.probe", src: "blob:https://site.test/9" })).out.state, "none");
  assert.deepEqual(host, []);
  const nas = { tab: { id: 12, url: "http://192.168.1.5/video", title: "NAS" }, frameId: 0, url: "http://192.168.1.5/video" };
  assert.equal((await send({ type: "video.probe", src: "http://192.168.1.5/a.mp4" }, nas)).out.state, "ok");
  assert.equal((await send({ type: "video.probe", src: "http://192.168.1.5/a.m3u8" }, nas)).out.state, "ok");
  assert.equal(host.find((m) => m.type === "stream.probe").url, "http://192.168.1.5/a.m3u8");
});

test("L2: an incognito tab gets no video option and nothing reaches Octo", async () => {
  reset();
  const priv = { tab: { ...tab, id: 13, incognito: true }, frameId: 0, url: tab.url };
  const r = await send({ type: "video.probe", src: "https://media.test/clip.mp4" }, priv);
  assert.equal(r.out?.state ?? "none", "none");
  assert.deepEqual(host, []);
});
