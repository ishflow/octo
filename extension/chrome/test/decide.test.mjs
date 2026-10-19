import test from "node:test";
import assert from "node:assert/strict";
import {
  shouldCapture, DEFAULT_EXTS, cookieHeader, stuckPausedIds, helloConfig, rememberPost, wasPosted, POST_TTL_MS, preselect, mergeExcluded, toggleSite,
} from "../lib/decide.mjs";

const base = {
  url: "https://cdn.example.org/files/setup.zip",
  filename: "setup.zip",
  exts: ["zip", "iso", "tar.gz"],
  excluded: [],
  bypass: false,
};
const sc = (o) => shouldCapture({ ...base, ...o });

test("listed extension is captured", () => assert.equal(sc({}), true));

test("extension match is case-insensitive on both sides", () => {
  assert.equal(sc({ filename: "SETUP.ZIP" }), true);
  assert.equal(sc({ exts: ["ZIP"] }), true);
});

test("multi-part extension like .tar.gz", () => {
  assert.equal(sc({ filename: "src.tar.gz", url: "https://a.org/src.tar.gz" }), true);
  assert.equal(sc({ filename: "src.gz", exts: ["tar.gz"] }), false);
});

test("extension must be the whole suffix, not a substring", () => {
  assert.equal(sc({ filename: "notzip", exts: ["zip"] }), false);
  assert.equal(sc({ filename: "a.zipx", exts: ["zip"] }), false);
});

test("wildcard extension (R0* matches split archives)", () => {
  assert.equal(sc({ filename: "a.r07", exts: ["r0*"] }), true);
  assert.equal(sc({ filename: "a.rar", exts: ["r0*"] }), false);
});

test("filename may be a full path", () => {
  assert.equal(sc({ filename: "/Users/u/Downloads/x.iso" }), true);
});

test("falls back to URL path when filename is empty", () => {
  assert.equal(sc({ filename: "", url: "https://a.org/dl/x.ISO?token=1#frag" }), true);
  assert.equal(sc({ filename: "", url: "https://a.org/dl/page.html" }), false);
});

test("unknown extension is not captured", () => {
  assert.equal(sc({ filename: "doc.xyz", url: "https://a.org/doc.xyz" }), false);
  assert.equal(sc({ filename: "noext", url: "https://a.org/noext" }), false);
});

test("empty ext list captures nothing", () => assert.equal(sc({ exts: [] }), false));

test("excluded host: exact and wildcard", () => {
  assert.equal(sc({ excluded: ["cdn.example.org"] }), false);
  assert.equal(sc({ excluded: ["*.example.org"] }), false);
  assert.equal(sc({ excluded: ["CDN.Example.ORG"] }), false);
  assert.equal(sc({ excluded: ["*.example.com"] }), true);
  assert.equal(sc({ excluded: ["other.org", "*.example.com"] }), true);
});

test("wildcard host also matches the bare domain (same as the app's HostPattern)", () => {
  assert.equal(sc({ url: "https://example.org/a.zip", excluded: ["*.example.org"] }), false);
});

test("wildcard host does not match look-alike suffix", () => {
  assert.equal(sc({ url: "https://evilexample.org/a.zip", excluded: ["*.example.org"] }), true);
});

test("blob:, data:, file: and garbage URLs are skipped", () => {
  assert.equal(sc({ url: "blob:https://a.org/uuid" }), false);
  assert.equal(sc({ url: "data:application/zip;base64,AAAA" }), false);
  assert.equal(sc({ url: "file:///tmp/a.zip" }), false);
  assert.equal(sc({ url: "not a url" }), false);
});

test("http is captured too", () => assert.equal(sc({ url: "http://a.org/x.zip" }), true));

test("bypass true always wins", () => {
  assert.equal(sc({ bypass: true }), false);
});

test("default list is lowercase classic list and includes iso/zip/pdf", () => {
  for (const e of ["zip", "iso", "pdf", "mp4", "r0*"]) assert.ok(DEFAULT_EXTS.includes(e), e);
  assert.ok(DEFAULT_EXTS.every((e) => e === e.toLowerCase()));
});

test("cookieHeader joins name=value pairs", () => {
  assert.equal(cookieHeader([{ name: "a", value: "b" }, { name: "c", value: "d" }]), "a=b; c=d");
  assert.equal(cookieHeader([]), "");
  assert.equal(cookieHeader(undefined), "");
})

test("cookieHeader drops SameSite=Strict cookies on a cross-site download only", () => {
  const jar = [{ name: "s", value: "1", sameSite: "strict" }, { name: "l", value: "2", sameSite: "lax" }, { name: "n", value: "3", sameSite: "no_restriction" }];
  assert.equal(cookieHeader(jar, "https://other.org/p", "https://cdn.a.org/f.zip"), "l=2; n=3");
  assert.equal(cookieHeader(jar, "https://www.a.org/p", "https://cdn.a.org/f.zip"), "s=1; l=2; n=3"); // same site
  assert.equal(cookieHeader(jar, "", "https://a.org/f.zip"), "s=1; l=2; n=3"); // no page: unknown, as before
  assert.equal(cookieHeader(jar, "not a url", "https://a.org/f.zip"), "s=1; l=2; n=3");
});;

test("stuckPausedIds: resumes only our still-paused, in-progress, non-live downloads", () => {
  const items = [
    { id: 1, state: "in_progress", paused: true },
    { id: 2, state: "in_progress", paused: false }, // already running
    { id: 3, state: "complete", paused: false },
    { id: 4, state: "interrupted", paused: true },
    { id: 5, state: "in_progress", paused: true }, // handled by live worker
    { id: 9, state: "in_progress", paused: true }, // paused by the user, not stored by us
  ];
  assert.deepEqual(stuckPausedIds([1, 2, 3, 4, 5, 6], [5], items), [1]); // 6: gone from history
  assert.deepEqual(stuckPausedIds([], [], items), []);
  assert.deepEqual(stuckPausedIds(undefined, undefined, undefined), []);
});

test("helloConfig: stores the service's lists, keeps saved ones when the service is down", () => {
  assert.deepEqual(helloConfig({ type: "hello", version: "1.0", captureExts: ["zip"], excludedSites: ["a.org"] }),
    { captureExts: ["zip"], excludedSites: ["a.org"], hostVersion: "1.0", captureOff: false, captureMinKB: 0 });
  assert.deepEqual(helloConfig({ type: "hello", version: "1.0", captureExts: null, excludedSites: null }),
    { captureExts: DEFAULT_EXTS, excludedSites: [], hostVersion: "1.0", captureOff: false, captureMinKB: 0 });
  assert.equal(helloConfig({ type: "hello", captureExts: DEFAULT_EXTS, excludedSites: [], offline: true }), null);
  assert.equal(helloConfig({ type: "error", error: "x" }), null);
  assert.equal(helloConfig(undefined), null);
});

test("POST navigations stay in the browser for the TTL, fragment ignored, old entries pruned", () => {
  const posts = new Map();
  rememberPost(posts, "https://bank.example/statement#x", 1000);
  assert.equal(wasPosted(posts, "https://bank.example/statement", 1000 + POST_TTL_MS), true);
  assert.equal(wasPosted(posts, "https://bank.example/statement", 1001 + POST_TTL_MS), false);
  assert.equal(wasPosted(posts, "https://bank.example/other", 1000), false);
  rememberPost(posts, "https://b.example/", 2000 + POST_TTL_MS);
  assert.deepEqual([...posts.keys()], ["https://b.example/"]);
});

test("a form whose response takes minutes is still a POST", () => {
  const posts = new Map();
  rememberPost(posts, "https://bank.example/export", 0);
  assert.equal(wasPosted(posts, "https://bank.example/export", 5 * 60000), true);
});

test("preselect: only links the capture list would take start checked", () => {
  const links = [
    { url: "https://a.org/x.zip", text: "x" },
    { url: "https://a.org/about", text: "Hakkında" },
    { url: "https://skip.org/y.zip", text: "y" },
  ];
  assert.deepEqual(preselect(links, ["zip"], ["skip.org"]).map((l) => l.checked), [true, false, false]);
  assert.deepEqual(preselect(undefined, ["zip"], []), []);
});

test("mergeExcluded: the service's list and the local one combine without duplicates; missing lists count as empty", () => {
  assert.deepEqual(mergeExcluded(["a.org", "b.org"], ["b.org", "c.org"]), ["a.org", "b.org", "c.org"]);
  assert.deepEqual(mergeExcluded(undefined, ["c.org"]), ["c.org"]);
  assert.deepEqual(mergeExcluded(["a.org"], undefined), ["a.org"]);
});

test("toggleSite adds a host once and removes it again", () => {
  assert.deepEqual(toggleSite(["a.org"], "b.org", true), ["a.org", "b.org"]);
  assert.deepEqual(toggleSite(["a.org", "b.org"], "b.org", true), ["a.org", "b.org"]);
  assert.deepEqual(toggleSite(["a.org", "b.org"], "a.org", false), ["b.org"]);
});

test("capture switched off in the app: nothing is taken", () => {
  assert.equal(sc({ off: true }), false);
  assert.equal(sc({ off: false }), true);
});

test("small-file threshold: known size below it stays in the browser, unknown size is taken", () => {
  assert.equal(sc({ size: 100 * 1024 - 1, minKB: 100 }), false);
  assert.equal(sc({ size: 100 * 1024, minKB: 100 }), true);
  assert.equal(sc({ size: -1, minKB: 100 }), true);
  assert.equal(sc({ size: 10, minKB: 0 }), true);
});

test("helloConfig carries the app's capture switch and threshold; junk means old behaviour", () => {
  const c = helloConfig({ type: "hello", captureExts: [], excludedSites: [], captureOff: true, captureMinKB: 512 });
  assert.equal(c.captureOff, true);
  assert.equal(c.captureMinKB, 512);
  const j = helloConfig({ type: "hello", captureOff: "yes", captureMinKB: -5 });
  assert.equal(j.captureOff, false);
  assert.equal(j.captureMinKB, 0);
});
