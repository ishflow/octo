// video.js (content script) against a minimal fake DOM and chrome.* (no browser needed).
import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const src = readFileSync(new URL("../video.js", import.meta.url), "utf8");

class El {
  constructor(tag) {
    this.tag = tag; this.attrs = {}; this.children = []; this.fns = {}; this.hidden = false; this.open = false; this._text = "";
    this.style = { props: {}, setProperty: (k, v, p) => { this.style.props[k] = [v, p]; } };
  }
  set textContent(t) { this._text = String(t); this.children = []; }
  get textContent() { return this._text + this.children.map((c) => c.textContent).join(""); }
  setAttribute(k, v) { this.attrs[k] = String(v); }
  getAttribute(k) { return this.attrs[k] ?? null; }
  append(...c) { for (const x of c) { this.children.push(x); x.parent = this; } }
  replaceChildren(...c) { this.children = []; this.append(...c); }
  addEventListener(t, f) { (this.fns[t] ??= []).push(f); }
  removeEventListener(t, f) { this.fns[t] = (this.fns[t] || []).filter((g) => g !== f); }
  fire(t, e = {}) { for (const f of this.fns[t] || []) f({ isTrusted: true, type: t, target: this, preventDefault() {}, stopPropagation() {}, ...e }); }
  attachShadow(o) { this.shadowMode = o.mode; this.shadow = new El("#shadow"); return this.shadow; }
  showPopover() { this.open = true; }
  hidePopover() { this.open = false; }
  matches(s) { return s === ":popover-open" ? this.open : false; }
  getBoundingClientRect() { return { left: 0, top: 0, right: 10, bottom: 10, width: 10, height: 10 }; }
  focus() { shadowOf()?.constructor && (shadowOf().activeElement = this); }
  get isConnected() { return this.tag === "video" || !!this.parent; }
  all() { return this.children.flatMap((c) => [c, ...c.all()]); }
  querySelectorAll() { return this.all().filter((c) => c.attrs.role === "menuitem"); }
}
class Video extends El {
  constructor(rect) { super("video"); this.rect = rect; this.currentSrc = "blob:https://site.test/1"; this.mediaKeys = null; }
  getBoundingClientRect() { const { x, y, w, h } = this.rect; return { left: x, top: y, right: x + w, bottom: y + h, width: w, height: h }; }
}
const doc = new El("#document");
doc.documentElement = new El("html");
doc.createElement = (t) => new El(t);
doc.createElementNS = (_ns, t) => new El(t);
doc.fullscreenElement = null;
let cover = null; // the page's element over the button
doc.elementFromPoint = () => cover || host();
let clock = 1e6;
performance.now = () => clock;
const observers = [];
class FakeIO { // IntersectionObserver v2: a control is reported visible (long ago) as soon as it is observed
  constructor(cb, opts) { this.cb = cb; this.opts = opts; observers.push(this); }
  observe(target) { this.cb([{ target, isVisible: true, time: clock - 10000 }]); }
}
const see = (target, isVisible) => observers[0].cb([{ target, isVisible, time: clock }]);
const win = new El("#window");
const raf = [];
const flush = () => raf.splice(0).forEach((f) => f(0));
const sent = [];
let replies = {};
const local = { enabled: true, hideVideoButton: false, videoOffSites: [] };
const changed = [];
Object.assign(globalThis, {
  document: doc, HTMLVideoElement: Video, innerWidth: 1280, innerHeight: 800,
  addEventListener: win.addEventListener.bind(win), removeEventListener: win.removeEventListener.bind(win),
  requestAnimationFrame: (f) => raf.push(f), IntersectionObserver: FakeIO,
  getComputedStyle: (e) => ({ display: "block", visibility: "visible", opacity: "1", getPropertyValue: (k) => e.style.props[k]?.[0] ?? "" }),
  CSSStyleSheet: class { replaceSync(css) { this.css = css; } },
  location: { ancestorOrigins: [] },
  chrome: {
    runtime: { id: "x", sendMessage: async (m) => { sent.push(m); return replies[m.type]?.(m); } },
    storage: {
      local: { get: async (q) => Object.fromEntries(Object.entries(q).map(([k, v]) => [k, local[k] ?? v])) },
      onChanged: { addListener: (f) => changed.push(f) },
    },
  },
});
Object.defineProperty(globalThis, "origin", { value: "https://site.test", configurable: true });
globalThis.self = globalThis;
const tick = () => new Promise((r) => setTimeout(r, 0));
const host = () => doc.documentElement.children[0];
const shadowOf = () => host()?.shadow;
const btn = () => shadowOf().all().find((e) => e.tag === "button" && e.attrs["aria-label"]);
const items = () => shadowOf().all().filter((e) => e.attrs.role === "menuitem");
const status = () => shadowOf().all().find((e) => e.attrs.role === "status");
const move = (x, y) => { win.fire("mousemove", { clientX: x, clientY: y }); flush(); };
const click = (e, o) => { clock += 1000; e.fire("click", o); }; // a person takes a moment to aim

await import("../video.js");

test("source: no HTML parsing sinks, closed shadow root, untrusted events checked", () => {
  for (const sink of ["innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "eval(", "new Function"]) {
    assert.equal(src.includes(sink), false, sink);
  }
  assert.match(src, /attachShadow\(\{ mode: "closed" \}\)/);
  assert.ok((src.match(/isTrusted/g) || []).length >= 4);
});

test("idle on pages without video: only capture listeners on document, nothing on window", () => {
  assert.deepEqual(Object.keys(doc.fns).sort(), ["emptied", "encrypted", "loadedmetadata", "play"]);
  assert.deepEqual(Object.keys(win.fns), []);
});

const big = new Video({ x: 100, y: 100, w: 640, h: 360 });
const small = new Video({ x: 0, y: 600, w: 160, h: 90 });

test("a synthetic play event is ignored; a real one starts tracking", async () => {
  doc.fire("play", { target: big, isTrusted: false });
  assert.equal(win.fns.mousemove, undefined);
  doc.fire("play", { target: big });
  doc.fire("play", { target: small });
  await tick();
  assert.equal(win.fns.mousemove.length, 1);
});

test("the button appears over a large video under the pointer, never over a small one", () => {
  move(50, 650);
  assert.equal(host(), undefined);
  move(300, 200);
  assert.equal(host().shadowMode, "closed");
  assert.equal(host().attrs.popover, "manual");
  assert.equal(host().open, true);
  assert.deepEqual(host().style.props.position, ["fixed", "important"]);
  assert.deepEqual(host().style.props.top, ["108px", "important"]);
  assert.equal(btn().attrs["aria-haspopup"], "menu");
  move(50, 650);
  assert.equal(host().open, false);
});

test("an untrusted click does nothing; a real click probes and lists the options", async () => {
  move(300, 200);
  click(btn(), { isTrusted: false });
  await tick();
  assert.deepEqual(sent, []);
  replies = {
    "video.probe": () => ({ state: "ok", token: "t1", options: [{ id: "v1", label: "<img src=x onerror=alert(1)>" }, { id: "v0", label: "360p" }] }),
    "video.get": () => ({ accepted: true }),
  };
  click(btn());
  await tick();
  assert.deepEqual(sent, [{ type: "video.probe", src: "blob:https://site.test/1", protected: false }]);
  assert.equal(btn().attrs["aria-expanded"], "true");
  assert.deepEqual(items().map((i) => i.textContent), ["<img src=x onerror=alert(1)>", "360p", "Bu sitede gösterme"]);
  assert.equal(items()[0].children.length, 0); // the label is text, not markup
  click(items()[1], { isTrusted: false });
  await tick();
  assert.equal(sent.length, 1);
  click(items()[1]);
  await tick();
  assert.deepEqual(sent[1], { type: "video.get", token: "t1", id: "v0" });
  assert.equal(status().textContent, "Octo'ya eklendi");
});

test("Escape closes the menu and returns focus to the button", async () => {
  sent.length = 0;
  replies["video.probe"] = () => ({ state: "none" });
  if (btn().attrs["aria-expanded"] === "true") shadowOf().fire("keydown", { key: "Escape" });
  click(btn());
  await tick();
  assert.deepEqual(items().map((i) => [i.textContent, i.getAttribute("aria-disabled")]),
    [["Akış adresi bulunamadı. Sayfayı yenileyip videoyu oynatın.", "true"], ["Bu sitede gösterme", null]]);
  shadowOf().fire("keydown", { key: "Escape" });
  assert.equal(btn().attrs["aria-expanded"], "false");
  assert.equal(shadowOf().activeElement, btn());
});

test("a video that fired 'encrypted' is reported protected", async () => {
  sent.length = 0;
  replies["video.probe"] = () => ({ state: "protected" });
  doc.fire("encrypted", { target: big });
  click(btn());
  await tick();
  assert.equal(sent[0].protected, true);
  assert.equal(items()[0].textContent, "Bu video korumalı (DRM), indirilemez");
  shadowOf().fire("keydown", { key: "Escape" });
});

test("clickjacking: a hidden, distorted, covered or just-shown button ignores real clicks", async () => {
  sent.length = 0;
  replies["video.probe"] = () => ({ state: "live" });
  assert.equal(observers[0].opts.trackVisibility, true);
  assert.ok(observers[0].opts.delay >= 100);
  const probes = () => sent.filter((m) => m.type === "video.probe").length;
  host().style.setProperty("opacity", "0", "important"); // the page rewrites the host's inline style
  click(btn());
  await tick();
  assert.equal(probes(), 0);
  host().style.setProperty("opacity", "1", "important");
  cover = new El("div"); // the page's own element on top
  click(btn());
  await tick();
  assert.equal(probes(), 0);
  cover = null;
  see(btn(), false); // a pointer-events: none overlay: only IntersectionObserver v2 sees it
  click(btn());
  await tick();
  assert.equal(probes(), 0);
  see(btn(), true); // uncovered just before the click
  btn().fire("click");
  await tick();
  assert.equal(probes(), 0);
  click(btn());
  await tick();
  assert.equal(probes(), 1);
  shadowOf().fire("keydown", { key: "Escape" });
});

test("clickjacking: a menu item clicked right after the menu fills (a double-click trap) is ignored", async () => {
  sent.length = 0;
  replies["video.probe"] = () => ({ state: "ok", token: "t2", options: [{ id: "v0", label: "360p" }] });
  click(btn());
  await tick();
  items()[0].fire("click"); // same instant the items appeared
  await tick();
  assert.equal(sent.length, 1);
  cover = new El("div");
  click(items()[0]);
  await tick();
  assert.equal(sent.length, 1);
  cover = null;
  click(items()[0]);
  await tick();
  assert.deepEqual(sent[1], { type: "video.get", token: "t2", id: "v0" });
  shadowOf().fire("keydown", { key: "Escape" });
});

test("the pointer leaving the window hides the button (no endless follow loop)", () => {
  move(300, 200);
  assert.equal(host().open, true);
  doc.fire("mouseout", { relatedTarget: null, isTrusted: false });
  assert.equal(host().open, true);
  doc.fire("mouseout", { relatedTarget: new El("div") });
  assert.equal(host().open, true);
  doc.fire("mouseout", { relatedTarget: null });
  assert.equal(host().open, false);
});

test("'Bu sitede gösterme' asks the worker and hides the button; settings changes hide it too", async () => {
  move(300, 200);
  sent.length = 0;
  replies["video.probe"] = () => ({ state: "live" });
  click(btn());
  await tick();
  click(items().at(-1));
  await tick();
  assert.deepEqual(sent.at(-1), { type: "video.off" });
  assert.equal(host().open, false);
  move(300, 200);
  assert.equal(host().open, false);

  local.videoOffSites = [];
  for (const f of changed) f({ videoOffSites: { newValue: [] } }, "local");
  await tick();
  move(300, 200);
  assert.equal(host().open, true);
  local.videoOffSites = ["site.test"];
  for (const f of changed) f({ videoOffSites: { newValue: ["site.test"] } }, "local");
  await tick();
  assert.equal(host().open, false);
  local.videoOffSites = [];
  local.hideVideoButton = true; // the app's setting
  for (const f of changed) f({ hideVideoButton: { newValue: true } }, "local");
  await tick();
  move(300, 200);
  assert.equal(host().open, false);
});
