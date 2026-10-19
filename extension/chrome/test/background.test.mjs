// Wiring checks for background.js against a minimal fake chrome.* (no browser needed).
import test from "node:test";
import assert from "node:assert/strict";
import { collectLinks } from "../lib/collect.mjs";

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
let scriptFunc;
const calls = { pause: [], resume: [], host: [], badge: [], tabs: [] };
let hostReply = () => ({ type: "ack", accepted: true });
const chrome = (globalThis.chrome = {
  runtime: {
    lastError: null, getURL: (p) => `chrome-extension://x/${p}`,
    sendNativeMessage: (_h, msg, cb) => { calls.host.push(msg); cb(hostReply(msg)); },
    onInstalled: ev(), onStartup: ev(), onMessage: ev(),
  },
  storage: { local: store({ captureExts: ["zip"], excludedSites: ["bank.example"] }), session: store() },
  action: { setBadgeText: ({ text }) => calls.badge.push(text) },
  downloads: {
    onCreated: ev(), onDeterminingFilename: ev(), search: async () => [],
    pause: async (id) => calls.pause.push(id), resume: async (id) => calls.resume.push(id),
    cancel: async () => {}, erase: async () => {},
  },
  webRequest: { onBeforeRequest: ev(), onHeadersReceived: ev() },
  contextMenus: { onClicked: ev(), removeAll: (cb) => cb(), create: () => {} },
  scripting: { executeScript: async ({ func }) => { scriptFunc = func; return [{ result: [{ url: "https://a.org/x.zip", text: "x" }] }]; } },
  tabs: { create: async (t) => calls.tabs.push(t), query: async () => [], onRemoved: ev() },
  cookies: { getAll: async () => [] },
});
await import("../background.js");
const tick = () => new Promise((r) => setTimeout(r, 20));

test("download started by a form POST is not paused or captured", async () => {
  chrome.webRequest.onBeforeRequest.fire({ method: "POST", url: "https://bank.test/statement.pdf" });
  chrome.downloads.onCreated.fire({ id: 1, url: "https://bank.test/statement.pdf" });
  await tick();
  assert.deepEqual(calls.pause, []);
});

test("hello from a stopped service keeps the saved lists", async () => {
  hostReply = () => ({ type: "hello", version: "1.0", captureExts: ["zip", "pdf"], excludedSites: [], offline: true });
  let status;
  chrome.runtime.onMessage.fire({ type: "status" }, {}, (s) => (status = s));
  await tick();
  assert.equal(status.connected, false);
  assert.deepEqual(chrome.storage.local.d.excludedSites, ["bank.example"]);
});

test("a download refreshes stale lists from the service before deciding", async () => {
  hostReply = (m) => m.type === "hello"
    ? { type: "hello", version: "1.0", captureExts: ["iso"], excludedSites: [] }
    : { type: "ack", accepted: true };
  calls.host.length = 0;
  chrome.downloads.onCreated.fire({ id: 2, url: "https://a.org/x.iso" });
  chrome.downloads.onDeterminingFilename.fire({ id: 2, url: "https://a.org/x.iso", filename: "x.iso", referrer: "" });
  await tick();
  assert.deepEqual(calls.host.map((m) => m.type), ["hello", "download"]);
});

test("'all links' opens the picker; only the picked links are sent", async () => {
  calls.host.length = 0;
  chrome.contextMenus.onClicked.fire({ menuItemId: "page" }, { id: 7, index: 0, url: "https://a.org/" });
  await tick();
  assert.deepEqual(calls.host, []); // nothing queued yet
  const key = calls.tabs[0].url.split("#")[1];
  assert.equal(chrome.storage.session.d[key].links.length, 1);
  assert.equal(scriptFunc, collectLinks);
  let reply;
  chrome.runtime.onMessage.fire({ type: "batch", links: [{ url: "https://a.org/x.zip" }], pageUrl: "https://a.org/" },
    { url: "chrome-extension://x/links.html#" + key }, (r) => (reply = r));
  chrome.runtime.onMessage.fire({ type: "batch", links: [{ url: "https://evil/" }] }, { url: "https://a.org/" }, () => {});
  await tick();
  assert.deepEqual(reply, { accepted: true });
  assert.deepEqual(calls.host.map((m) => m.links.map((l) => l.url)), [["https://a.org/x.zip"]]);
});

test("'Octo ile indir' shows the error badge when Octo refuses the link", async () => {
  hostReply = () => ({ type: "ack", accepted: false });
  chrome.contextMenus.onClicked.fire({ menuItemId: "link", linkUrl: "https://a.org/y.zip" }, { url: "https://a.org/" });
  await tick();
  assert.equal(calls.badge.at(-1), "!");
});

test("L2: an incognito download stays in the browser; the link menu does nothing in an incognito tab", async () => {
  calls.pause.length = 0;
  calls.host.length = 0;
  hostReply = () => ({ type: "ack", accepted: true });
  chrome.downloads.onCreated.fire({ id: 30, url: "https://a.org/p.zip", incognito: true });
  chrome.downloads.onDeterminingFilename.fire({ id: 30, url: "https://a.org/p.zip", filename: "p.zip", referrer: "", incognito: true });
  chrome.contextMenus.onClicked.fire({ menuItemId: "link", linkUrl: "https://a.org/y.zip" }, { url: "https://a.org/", incognito: true });
  chrome.contextMenus.onClicked.fire({ menuItemId: "page" }, { id: 8, index: 0, url: "https://a.org/", incognito: true });
  await tick();
  assert.deepEqual(calls.pause, []);
  assert.deepEqual(calls.host, []);
});

test("SameSite=Strict cookies are not sent for a link on another site", async () => {
  chrome.cookies.getAll = async () => [{ name: "s", value: "1", sameSite: "strict" }, { name: "l", value: "2", sameSite: "lax" }];
  hostReply = () => ({ type: "ack", accepted: true });
  calls.host.length = 0;
  chrome.contextMenus.onClicked.fire({ menuItemId: "link", linkUrl: "https://files.b.org/y.zip" }, { url: "https://a.org/" });
  chrome.contextMenus.onClicked.fire({ menuItemId: "link", linkUrl: "https://a.org/z.zip" }, { url: "https://a.org/" });
  await tick();
  assert.deepEqual(calls.host.map((m) => m.cookies), ["l=2", "s=1; l=2"]);
  chrome.cookies.getAll = async () => [];
});
