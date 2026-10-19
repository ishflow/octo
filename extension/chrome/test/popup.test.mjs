// popup.js against a fake DOM and chrome.* (no browser needed).
import test from "node:test";
import assert from "node:assert/strict";
import { collectLinks } from "../lib/collect.mjs";

const els = {};
const el = (id) => (els[id] ??= { id, hidden: false, disabled: false, checked: false, className: "", textContent: "", fns: {},
  addEventListener(t, f) { this.fns[t] = f; } });
globalThis.document = { getElementById: el };
let closed = false;
globalThis.window = { close: () => { closed = true; } };
const session = {};
const created = [];
const local = {};
const native = [];
globalThis.chrome = {
  storage: { local: { get: async (q) => ({ ...q, ...local }), set: async (o) => Object.assign(local, o) }, session: { set: async (o) => Object.assign(session, o) } },
  runtime: { sendMessage: async () => ({ connected: true }), sendNativeMessage: (host, msg, cb) => { native.push([host, msg]); cb({ type: "ack", accepted: true }); } },
  tabs: {
    query: async () => [{ id: 7, index: 2, url: "https://example.com/page" }],
    create: async (o) => created.push(o),
  },
  scripting: { executeScript: async ({ target, func }) => [{ result: target.tabId === 7 && typeof func === "function" ? [{ url: "https://example.com/a.zip", text: "a" }] : [] }] },
};

await import("../popup.js");
await new Promise((r) => setTimeout(r, 0));

test("shows the connection state", () => {
  assert.equal(els.status.className, "ok");
  assert.equal(els.status.textContent, "● Uygulamaya bağlı");
  assert.equal(els.note.hidden, true);
});

test("list button opens the picker with the page's links", async () => {
  await els.links.fns.click();
  const key = Object.keys(session)[0];
  assert.deepEqual(session[key], { links: [{ url: "https://example.com/a.zip", text: "a" }], pageUrl: "https://example.com/page" });
  assert.deepEqual(created, [{ url: `links.html#${key}`, index: 3, openerTabId: 7 }]);
  assert.equal(closed, true);
});

test("site row names the tab's host and starts off", () => {
  assert.equal(els.siteName.textContent, "example.com için kapat");
  assert.equal(els.siteRow.hidden, false);
  assert.equal(els.site.checked, false);
});

test("site switch adds and removes the host in the local exclusion list", async () => {
  local.localExcludedSites = ["other.test"];
  await els.site.fns.change({ target: { checked: true } });
  assert.deepEqual(local.localExcludedSites, ["other.test", "example.com"]);
  await els.site.fns.change({ target: { checked: false } });
  assert.deepEqual(local.localExcludedSites, ["other.test"]);
});

test("collectLinks injected into the page is the one from lib/", async () => {
  let func;
  chrome.scripting.executeScript = async (o) => { func = o.func; return [{ result: [] }]; };
  await els.links.fns.click();
  assert.equal(func, collectLinks);
});

test("open button asks the host to open the app, then closes the popup", async () => {
  closed = false;
  await els.open.fns.click();
  assert.deepEqual(native, [["com.uba.octo", { type: "open" }]]);
  assert.equal(closed, true);
});

test("video switch: on unless the tab's host is in videoOffSites; toggling edits only that list", async () => {
  assert.equal(els.videoRow.hidden, false);
  assert.equal(els.video.checked, true);
  local.videoOffSites = ["other.test"];
  await els.video.fns.change({ target: { checked: false } });
  assert.deepEqual(local.videoOffSites, ["other.test", "example.com"]);
  await els.video.fns.change({ target: { checked: true } });
  assert.deepEqual(local.videoOffSites, ["other.test"]);
  assert.deepEqual(local.localExcludedSites, ["other.test"]); // the capture list is a separate intent
});
