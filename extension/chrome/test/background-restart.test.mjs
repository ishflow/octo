// background.js across a service worker restart: two module instances share only chrome.storage.session.
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
const tick = () => new Promise((r) => setTimeout(r, 20));
const session = store(); // survives the worker; storage.local and memory do not matter here
const paused = [];

// A fresh worker: new chrome.* events, new module instance of background.js.
async function startWorker(n) {
  globalThis.chrome = {
    runtime: { lastError: null, getURL: (p) => `chrome-extension://x/${p}`, sendNativeMessage: (_h, _m, cb) => cb({ type: "ack", accepted: true }),
      onInstalled: ev(), onStartup: ev(), onMessage: ev() },
    storage: { local: store({ captureExts: ["pdf"], excludedSites: [], helloAt: Date.now() + 1e9 }), session },
    action: { setBadgeText: () => {} },
    downloads: { onCreated: ev(), onDeterminingFilename: ev(), search: async () => [], pause: async (id) => paused.push(id),
      resume: async () => {}, cancel: async () => {}, erase: async () => {} },
    webRequest: { onBeforeRequest: ev(), onHeadersReceived: ev() },
    contextMenus: { onClicked: ev(), removeAll: (cb) => cb(), create: () => {} },
    scripting: { executeScript: async () => [] },
    tabs: { create: async () => {}, query: async () => [], onRemoved: ev() },
    cookies: { getAll: async () => [] },
  };
  await import(`../background.js?worker=${n}`);
  return globalThis.chrome;
}

test("a POST made before the worker restarted still keeps its download in the browser", async () => {
  const first = await startWorker(1);
  first.webRequest.onBeforeRequest.fire({ method: "POST", url: "https://bank.test/export.pdf" });
  await tick();
  const second = await startWorker(2); // the slow response arrives after the old worker was stopped
  second.downloads.onCreated.fire({ id: 1, url: "https://bank.test/export.pdf" });
  second.downloads.onDeterminingFilename.fire({ id: 1, url: "https://bank.test/export.pdf", filename: "export.pdf", referrer: "" });
  await tick();
  assert.deepEqual(paused, []);
});
