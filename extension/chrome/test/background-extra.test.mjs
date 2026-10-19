// background.js wiring: Option bypass, non-web URLs, excluded sites, POST, offline service, uppercase ext. Fake chrome.*.
import test from "node:test";
import assert from "node:assert/strict";

const ev = () => { const fns = []; return { addListener: (f) => fns.push(f), fire: (...a) => fns.map((f) => f(...a)) }; };
const store = (init = {}) => {
  const d = { ...init };
  return {
    d,
    get: async (q) => typeof q === "string" ? { [q]: d[q] } : Object.fromEntries(Object.entries(q || {}).map(([k, v]) => [k, d[k] ?? v])),
    set: async (o) => void Object.assign(d, o),
    remove: async (k) => void delete d[k],
  };
};
const calls = { pause: [], resume: [], cancel: [], host: [], badge: [] };
let hostReply = () => ({ type: "ack", accepted: true });
let hostError = null;
const jar = {}; // url -> cookies the browser would send there
let activeTab = [];
const chrome = (globalThis.chrome = {
  runtime: {
    lastError: null, getURL: (p) => `chrome-extension://x/${p}`,
    sendNativeMessage: (_h, msg, cb) => {
      calls.host.push(msg);
      chrome.runtime.lastError = hostError ? { message: hostError } : null;
      cb(hostError ? undefined : hostReply(msg));
      chrome.runtime.lastError = null;
    },
    onInstalled: ev(), onStartup: ev(), onMessage: ev(),
  },
  // helloAt is fresh so decide() never refreshes the lists mid-test
  storage: { local: store({ captureExts: ["zip", "iso"], excludedSites: ["*.example.com", "bank.test"], helloAt: Date.now() + 1e9 }), session: store() },
  action: { setBadgeText: ({ text }) => calls.badge.push(text) },
  downloads: {
    onCreated: ev(), onDeterminingFilename: ev(), search: async () => [],
    pause: async (id) => calls.pause.push(id), resume: async (id) => calls.resume.push(id),
    cancel: async (id) => calls.cancel.push(id), erase: async () => {},
  },
  webRequest: { onBeforeRequest: ev(), onHeadersReceived: ev() },
  contextMenus: { onClicked: ev(), removeAll: (cb) => cb(), create: () => {} },
  scripting: { executeScript: async () => [{ result: [] }] },
  tabs: { create: async () => {}, query: async () => activeTab, onRemoved: ev() },
  cookies: { getAll: async ({ url }) => jar[url] || [] },
});
await import("../background.js");
const tick = () => new Promise((r) => setTimeout(r, 20));
let nextId = 100;
const reset = () => { for (const k of Object.keys(calls)) calls[k].length = 0; hostReply = () => ({ type: "ack", accepted: true }); hostError = null; };

// Fires a download the way Chrome does: created, then filename determination.
async function download(url, filename, extra = {}) {
  const id = nextId++;
  chrome.downloads.onCreated.fire({ id, url, ...extra });
  chrome.downloads.onDeterminingFilename.fire({ id, url, filename, referrer: "", ...extra });
  await tick();
  return id;
}

test("listed download is paused, sent to the service and taken over on ack", async () => {
  reset();
  const id = await download("https://a.org/x.zip", "x.zip");
  assert.deepEqual(calls.pause, [id]);
  assert.deepEqual(calls.host.map((m) => [m.type, m.url, m.filename]), [["download", "https://a.org/x.zip", "x.zip"]]);
  assert.deepEqual(calls.cancel, [id]);
  assert.deepEqual(calls.resume, []);
});

test("Option held: nothing is paused or sent, the browser keeps the download", async () => {
  reset();
  chrome.runtime.onMessage.fire({ type: "alt", held: true }, {});
  const id = await download("https://a.org/alt.zip", "alt.zip");
  assert.deepEqual(calls.pause, []);
  assert.deepEqual(calls.host, []);
  assert.deepEqual(calls.cancel, []);
  chrome.runtime.onMessage.fire({ type: "alt", held: false }, {});
  const id2 = await download("https://a.org/after.zip", "after.zip");
  assert.deepEqual(calls.pause, [id2]); // released again
  assert.notEqual(id, id2);
});

test("blob: and data: downloads are left alone", async () => {
  reset();
  await download("blob:https://a.org/1111", "x.zip");
  await download("data:application/zip;base64,AAAA", "x.zip");
  await download("https://a.org/x.zip", "x.zip", { finalUrl: "blob:https://a.org/2222" });
  assert.deepEqual(calls.pause, []);
  assert.deepEqual(calls.host, []);
});

test("excluded site: *.example.com covers the root and subdomains; paused then resumed, never sent", async () => {
  reset();
  const ids = [
    await download("https://example.com/x.zip", "x.zip"),
    await download("https://dl.example.com/x.zip", "x.zip"),
    await download("https://bank.test/x.zip", "x.zip"),
  ];
  assert.deepEqual(calls.host, []);
  assert.deepEqual(calls.resume, ids);
  assert.deepEqual(calls.cancel, []);
});

test("a look-alike host is still captured", async () => {
  reset();
  await download("https://notexample.com/x.zip", "x.zip");
  assert.equal(calls.host.length, 1);
});

test("capture list matches uppercase file names; unlisted types go back to the browser", async () => {
  reset();
  await download("https://a.org/BIG.ISO", "BIG.ISO");
  assert.equal(calls.host.length, 1);
  reset();
  const id = await download("https://a.org/page.html", "page.html");
  assert.deepEqual(calls.host, []);
  assert.deepEqual(calls.resume, [id]);
});

test("POST download (also via redirect finalUrl) stays in the browser, untouched", async () => {
  reset();
  chrome.webRequest.onBeforeRequest.fire({ method: "POST", url: "https://a.org/export.zip" });
  chrome.webRequest.onBeforeRequest.fire({ method: "POST", url: "https://a.org/form" });
  await download("https://a.org/export.zip", "export.zip");
  await download("https://a.org/redirected.zip", "redirected.zip", { finalUrl: "https://a.org/form" });
  assert.deepEqual(calls.pause, []);
  assert.deepEqual(calls.resume, []);
  assert.deepEqual(calls.host, []);
});

test("GET navigation is not remembered as POST", async () => {
  reset();
  chrome.webRequest.onBeforeRequest.fire({ method: "GET", url: "https://a.org/get.zip" });
  await download("https://a.org/get.zip", "get.zip");
  assert.equal(calls.host.length, 1);
});

test("service down (native host unreachable): download is resumed in the browser and the badge turns on", async () => {
  reset();
  hostError = "Native host has exited.";
  const id = await download("https://a.org/off.zip", "off.zip");
  assert.deepEqual(calls.resume, [id]);
  assert.deepEqual(calls.cancel, []);
  assert.equal(calls.badge.at(-1), "!");
});

test("service refuses (accepted false): browser carries on", async () => {
  reset();
  hostReply = () => ({ type: "ack", accepted: false });
  const id = await download("https://a.org/no.zip", "no.zip");
  assert.deepEqual(calls.resume, [id]);
  assert.deepEqual(calls.cancel, []);
});

test("service stopped (hello offline) keeps saved lists and reports disconnected", async () => {
  reset();
  hostReply = () => ({ type: "hello", version: "1", captureExts: ["rar"], excludedSites: [], offline: true });
  let s;
  chrome.runtime.onMessage.fire({ type: "status" }, {}, (r) => (s = r));
  await tick();
  assert.equal(s.connected, false);
  assert.deepEqual(chrome.storage.local.d.captureExts, ["zip", "iso"]);
  assert.equal(calls.badge.at(-1), "");
});

test("disabled extension: nothing is paused", async () => {
  reset();
  chrome.storage.local.d.enabled = false;
  await download("https://a.org/dis.zip", "dis.zip");
  assert.deepEqual(calls.pause, []);
  assert.deepEqual(calls.host, []);
  delete chrome.storage.local.d.enabled;
});

test("batch: each link carries its own browser cookies; the page cookie is not spread", async () => {
  reset();
  jar["https://a.org/"] = [{ name: "page", value: "1" }];
  jar["https://a.org/x.zip"] = [{ name: "s", value: "2" }];
  chrome.runtime.onMessage.fire(
    { type: "batch", pageUrl: "https://a.org/", links: [{ url: "https://a.org/x.zip" }, { url: "https://user.a.org/y.zip" }] },
    { url: "chrome-extension://x/links.html#k" }, () => {});
  await tick();
  const [m] = calls.host;
  assert.equal(m.cookies || "", "");
  assert.deepEqual(m.links.map((l) => l.cookies), ["s=2", ""]);
});

test("batch: Referer is the full page only for same-origin links, else the origin, none on https to http", async () => {
  reset();
  chrome.runtime.onMessage.fire(
    { type: "batch", pageUrl: "https://a.org/share/doc?token=1#x",
      links: [{ url: "https://a.org/x.zip" }, { url: "https://cdn.b.net/y.zip" }, { url: "http://a.org/z.zip" }] },
    { url: "chrome-extension://x/links.html#k" }, () => {});
  await tick();
  const [m] = calls.host;
  assert.deepEqual(m.links.map((l) => l.referrer), ["https://a.org/share/doc?token=1", "https://a.org/", ""]);
  assert.equal(m.referrer, "https://a.org/"); // old hosts put this on every link
});

test("download without its own referrer: unrelated active tab is not sent, only the download's origin", async () => {
  reset();
  activeTab = [{ url: "https://mail.test/?token=secret" }];
  await download("https://a.org/dl/r.zip?id=1", "r.zip");
  activeTab = [];
  const [m] = calls.host;
  assert.equal(m.referrer, "https://a.org/");
  assert.ok(!JSON.stringify(m).includes("mail.test"));
});

test("download referrer from another site is cut to its origin; same-origin stays whole", async () => {
  reset();
  await download("https://a.org/c.zip", "c.zip", { referrer: "https://b.net/page?q=1" });
  await download("https://a.org/d.zip", "d.zip", { referrer: "https://a.org/page?q=1" });
  assert.deepEqual(calls.host.map((m) => m.referrer), ["https://b.net/", "https://a.org/page?q=1"]);
});

test("'Octo ile indir' on a cross-site link sends only the page origin as Referer", async () => {
  reset();
  chrome.contextMenus.onClicked.fire({ menuItemId: "link", linkUrl: "https://b.net/y.zip" }, { url: "https://a.org/p?t=1" });
  await tick();
  assert.equal(calls.host[0].referrer, "https://a.org/");
});

test("a link opened from an iframe sends the frame's URL as Referer, not the tab's", async () => {
  reset();
  chrome.contextMenus.onClicked.fire(
    { menuItemId: "link", linkUrl: "https://b.net/y.zip", frameUrl: "https://b.net/embed?x=1" },
    { url: "https://a.org/p?t=1" },
  );
  await tick();
  assert.equal(calls.host[0].referrer, "https://b.net/embed?x=1");
  assert.equal(calls.host[0].tabUrl, undefined); // the host never read it
});

test("a site excluded in the popup (local list) stays with the browser; the service's hello does not erase it", async () => {
  reset();
  const s = chrome.storage.local.d;
  Object.assign(s, { localExcludedSites: ["local.test"], helloAt: 0 });
  hostReply = (m) => m.type === "hello" ? { type: "hello", version: "1", captureExts: ["zip", "iso"], excludedSites: [] } : { type: "ack", accepted: true };
  const id = await download("https://local.test/x.zip", "x.zip");
  assert.deepEqual(calls.host.map((m) => m.type), ["hello"]);
  assert.deepEqual(calls.resume, [id]);
  assert.deepEqual(s.localExcludedSites, ["local.test"]);
  s.helloAt = Date.now() + 1e9;
  delete s.localExcludedSites;
});

test("batch: the start flag reaches the host; anything but true means queue only", async () => {
  for (const [start, want] of [[true, true], [false, false], [undefined, false], ["yes", false]]) {
    reset();
    chrome.runtime.onMessage.fire({ type: "batch", pageUrl: "https://a.org/", links: [{ url: "https://a.org/x.zip" }], start },
      { url: "chrome-extension://x/links.html#k" }, () => {});
    await tick();
    assert.equal(calls.host[0].start, want, String(start));
  }
});

test("a site closed in the popup also keeps files it serves from another host (page = referrer)", async () => {
  reset();
  const s = chrome.storage.local.d;
  s.localExcludedSites = ["github.com"];
  const id = await download("https://objects.githubusercontent.com/r/x.zip", "x.zip", { referrer: "https://github.com/o/r/releases" });
  assert.deepEqual(calls.host, []);
  assert.deepEqual(calls.resume, [id]);
  delete s.localExcludedSites;
});

test("the service's list stays a download-host list: a file linked from an excluded page is still captured", async () => {
  reset();
  await download("https://a.org/y.zip", "y.zip", { referrer: "https://bank.test/statements" });
  assert.equal(calls.host.length, 1);
});

test("Kaynak sayfa: the full page travels as pageUrl (never the unrelated active tab), Referer stays trimmed", async () => {
  reset();
  activeTab = [{ url: "https://mail.test/?token=secret" }];
  await download("https://a.org/c.zip", "c.zip", { referrer: "https://b.net/page?q=1" });
  await download("https://a.org/d.zip", "d.zip");
  activeTab = [];
  chrome.contextMenus.onClicked.fire({ menuItemId: "link", linkUrl: "https://b.net/y.zip" }, { url: "https://a.org/p?t=1" });
  await tick();
  chrome.runtime.onMessage.fire({ type: "batch", pageUrl: "https://a.org/share?k=1", links: [{ url: "https://a.org/x.zip" }] },
    { url: "chrome-extension://x/links.html#k" }, () => {});
  await tick();
  assert.deepEqual(calls.host.map((m) => m.pageUrl), ["https://b.net/page?q=1", undefined, "https://a.org/p?t=1", "https://a.org/share?k=1"]);
  assert.equal(calls.host[0].referrer, "https://b.net/");
  assert.ok(!JSON.stringify(calls.host).includes("mail.test"));
});

test("app switched capture off (hello): the download is not even paused", async () => {
  reset();
  const s = chrome.storage.local.d;
  s.helloAt = 0;
  hostReply = (m) => m.type === "hello" ? { type: "hello", version: "1", captureExts: ["zip"], excludedSites: [], captureOff: true } : { type: "ack", accepted: true };
  chrome.runtime.onMessage.fire({ type: "status" }, {}, () => {});
  await tick();
  reset();
  await download("https://a.org/off.zip", "off.zip");
  assert.deepEqual(calls.pause, []);
  assert.deepEqual(calls.host, []);
  Object.assign(s, { captureOff: false, captureExts: ["zip", "iso"], helloAt: Date.now() + 1e9 });
});

test("below the app's small-file threshold the browser keeps it; above it Octo takes it", async () => {
  reset();
  const s = chrome.storage.local.d;
  s.captureMinKB = 100;
  const small = await download("https://a.org/small.zip", "small.zip", { totalBytes: 99 * 1024 });
  assert.deepEqual(calls.host, []);
  assert.deepEqual(calls.resume, [small]);
  await download("https://a.org/big.zip", "big.zip", { totalBytes: 100 * 1024 });
  assert.deepEqual(calls.host.map((m) => [m.filename, m.size]), [["big.zip", 100 * 1024]]);
  s.captureMinKB = 0;
});
