// links.js: filter box, pick-by-extension, start-now option and the local exclusion list (fake DOM, like links.test.mjs).
import test from "node:test";
import assert from "node:assert/strict";

const els = {};
const el = (id) => (els[id] ??= { id, disabled: false, checked: false, value: "", textContent: "", children: [], append(...c) { this.children.push(...c); }, addEventListener() {} });
const boxes = [], rows = [];
globalThis.document = {
  getElementById: el,
  createElement: (tag) => {
    const e = { tag, children: [], hidden: false, append(...c) { this.children.push(...c); } };
    if (tag === "input") boxes.push(e);
    if (tag === "li") rows.push(e);
    return e;
  },
};
globalThis.window = { close() {} };
globalThis.location = { hash: "#k1" };
const sent = [];
globalThis.chrome = {
  storage: {
    session: { get: async () => ({ k1: { pageUrl: "https://a.org/", links: [
      { url: "https://a.org/one.zip", text: "Alpha" },
      { url: "https://a.org/two.zip", text: "Beta" },
      { url: "https://a.org/three.iso", text: "Gamma" },
      { url: "https://a.org/page.html", text: "Delta" },
      { url: "https://local.test/four.zip", text: "Epsilon" },
    ] } }), remove: async () => {} },
    local: { get: async () => ({ captureExts: ["zip", "iso"], excludedSites: [], localExcludedSites: ["local.test"] }) },
  },
  runtime: { sendMessage: async (m) => { sent.push(m); return { accepted: true }; } },
};
await import("../links.js");

const checked = () => boxes.map((b) => b.checked);
const hidden = () => rows.map((r) => r.hidden);
const filter = (v) => { els.filter.value = v; els.filter.oninput(); };
const pickExt = (v) => { els.ext.value = v; els.ext.onchange(); };

test("a site closed in the popup (local list) starts unchecked", () => {
  assert.deepEqual(checked(), [true, true, true, false, false]);
});

test("filter box hides rows whose text and address do not match, case-insensitively", () => {
  filter("ALPHA");
  assert.deepEqual(hidden(), [false, true, true, true, true]);
  filter("three");
  assert.deepEqual(hidden(), [true, true, false, true, true]);
  filter("");
  assert.deepEqual(hidden(), [false, false, false, false, false]);
});

test("Tümünü seç / Hiçbirini seçme only touch the visible rows; hidden picks are kept", () => {
  filter(".zip");
  els.none.onclick();
  assert.deepEqual(checked(), [false, false, true, false, false]); // iso stays: it was hidden
  els.all.onclick();
  assert.deepEqual(checked(), [true, true, true, false, true]); // page.html is hidden, so it stays unchecked
  filter("");
  els.none.onclick();
  assert.deepEqual(checked(), [false, false, false, false, false]);
});

test("extension picker lists each extension with its count and picks exactly that one", () => {
  assert.deepEqual(els.ext.children.map((o) => o.value), ["", "zip", "iso", "html"]);
  assert.match(els.ext.children[1].textContent, /zip \(3\)/);
  pickExt("zip");
  assert.deepEqual(checked(), [true, true, false, false, true]);
  pickExt("iso");
  assert.deepEqual(checked(), [false, false, true, false, false]);
  assert.equal(els.ext.value, ""); // back to the prompt, so the same extension can be picked again
});

test("extension picker respects the filter", () => {
  filter("four");
  pickExt("zip");
  assert.deepEqual(checked(), [false, false, true, false, true]); // hidden rows untouched
  filter("");
});

test("count line mentions how many rows the filter shows", () => {
  filter("zip");
  assert.match(els.count.textContent, /3 gösteriliyor/);
  filter("");
  assert.doesNotMatch(els.count.textContent, /gösteriliyor/);
});

test("start option: queue only by default, start flag true when ticked", async () => {
  pickExt("iso");
  await els.add.onclick();
  assert.equal(sent.at(-1).start, false);
  assert.match(els.status.textContent, /ana kuyruğuna eklendi/);
  els.start.checked = true;
  await els.add.onclick();
  assert.equal(sent.at(-1).start, true);
  assert.match(els.status.textContent, /başlat/);
  assert.deepEqual(sent.at(-1).links, [{ url: "https://a.org/three.iso", text: "Gamma" }]);
});

test("controls lock after a successful add, including filter and extension picker", () => {
  assert.equal(els.filter.disabled, true);
  assert.equal(els.ext.disabled, true);
  assert.equal(els.start.disabled, true);
});
