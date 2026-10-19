// links.js against a fake DOM: only the links left checked are sent.
import test from "node:test";
import assert from "node:assert/strict";

const els = {};
const el = (id) => (els[id] ??= { id, disabled: false, textContent: "", children: [], append(...c) { this.children.push(...c); }, addEventListener() {} });
const boxes = [];
globalThis.document = {
  getElementById: el,
  createElement: (tag) => {
    const e = { tag, children: [], append(...c) { this.children.push(...c); } };
    if (tag === "input") boxes.push(e);
    return e;
  },
};
globalThis.window = { close() {} };
globalThis.location = { hash: "#k1" };
const sent = [];
globalThis.chrome = {
  storage: {
    session: { get: async () => ({ k1: { pageUrl: "https://a.org/", links: [
      { url: "https://a.org/x.zip", text: "x" },
      { url: "https://a.org/page.html", text: "page" },
      { url: "https://example.com/y.zip", text: "y" },
      { url: "https://a.org/z.iso", text: "z" },
    ] } }), remove: async () => {} },
    local: { get: async () => ({ captureExts: ["zip", "iso"], excludedSites: ["*.example.com"] }) },
  },
  runtime: { sendMessage: async (m) => { sent.push(m); return { accepted: true }; } },
};
await import("../links.js");

test("capture list preselects; excluded and unlisted links start unchecked", () => {
  assert.deepEqual(boxes.map((b) => b.checked), [true, false, false, true]);
  assert.equal(els.add.disabled, false);
});

test("unchecking and checking changes what is sent: only checked links, url and text only", async () => {
  boxes[0].checked = false; // user unticks x.zip
  boxes[1].checked = true; // and ticks page.html
  await els.add.onclick();
  assert.equal(sent.length, 1);
  assert.equal(sent[0].type, "batch");
  assert.equal(sent[0].pageUrl, "https://a.org/");
  assert.deepEqual(sent[0].links, [{ url: "https://a.org/page.html", text: "page" }, { url: "https://a.org/z.iso", text: "z" }]);
  assert.match(els.status.textContent, /2 bağlantı/);
});

test("after a successful add the controls are locked so it cannot be queued twice", () => {
  assert.equal(els.add.disabled, true);
  assert.equal(els.all.disabled, true);
  assert.equal(boxes.every((b) => b.disabled), true);
});
