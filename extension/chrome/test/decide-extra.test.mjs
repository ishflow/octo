// Extra capture-rule cases for lib/decide.mjs: bypass, non-web schemes, excluded sites, ext case, offline hello, picker preselect.
import test from "node:test";
import assert from "node:assert/strict";
import { shouldCapture, helloConfig, preselect, referrerFor, linkExt, DEFAULT_EXTS } from "../lib/decide.mjs";

const sc = (o) => shouldCapture({ url: "https://a.org/x.zip", filename: "x.zip", exts: ["zip"], excluded: [], bypass: false, ...o });

test("Option (bypass) skips even a listed, non-excluded download", () => {
  assert.equal(sc({}), true);
  assert.equal(sc({ bypass: true }), false);
});

test("bypass undefined behaves as not held", () => assert.equal(sc({ bypass: undefined }), true));

for (const url of ["blob:https://a.org/1234-5678", "data:application/zip;base64,AAAA", "file:///tmp/x.zip", "ftp://a.org/x.zip", "javascript:void(0)"]) {
  test(`non-web URL is never captured: ${url.split(":")[0]}:`, () => assert.equal(sc({ url }), false));
}

test("excluded: *.example.com covers the root and subdomains, not look-alikes", () => {
  const excluded = ["*.example.com"];
  assert.equal(sc({ url: "https://example.com/x.zip", excluded }), false);
  assert.equal(sc({ url: "https://dl.example.com/x.zip", excluded }), false);
  assert.equal(sc({ url: "https://a.b.example.com/x.zip", excluded }), false);
  assert.equal(sc({ url: "https://notexample.com/x.zip", excluded }), true);
  assert.equal(sc({ url: "https://example.com.evil.org/x.zip", excluded }), true);
});

test("excluded: exact entry does not cover subdomains; case and spaces ignored", () => {
  assert.equal(sc({ url: "https://bank.example/x.zip", excluded: ["bank.example"] }), false);
  assert.equal(sc({ url: "https://www.bank.example/x.zip", excluded: ["bank.example"] }), true);
  assert.equal(sc({ url: "https://BANK.Example/x.zip", excluded: ["  Bank.EXAMPLE "] }), false);
});

test("excluded host wins over a listed extension; other hosts unaffected", () => {
  assert.equal(sc({ url: "https://ok.org/x.zip", excluded: ["bank.example"] }), true);
});

test("excluded ignores port and path", () => {
  assert.equal(sc({ url: "https://example.com:8443/a/b/x.zip", excluded: ["*.example.com"] }), false);
});

test("capture ext list is case-insensitive for names, URL fallback and entries", () => {
  assert.equal(sc({ filename: "Archive.ZiP" }), true);
  assert.equal(sc({ filename: "", url: "https://a.org/DL/FILE.ZIP?token=1" }), true);
  assert.equal(sc({ exts: ["Zip", "ISO"], filename: "x.iso" }), true);
});

test("URL fallback decodes percent-encoding and ignores the query", () => {
  assert.equal(sc({ filename: "", url: "https://a.org/my%20file.zip?x=.txt" }), true);
  assert.equal(sc({ filename: "", url: "https://a.org/page?file=x.zip" }), false);
});

test("regex metacharacters in an ext entry are literal", () => {
  assert.equal(sc({ exts: ["t.ar"], filename: "a.tXar" }), false);
  assert.equal(sc({ exts: ["c++"], filename: "a.c++" }), true);
});

test("default list matches uppercase real-world names", () => {
  assert.equal(sc({ exts: DEFAULT_EXTS, filename: "SETUP.EXE" }), true);
  assert.equal(sc({ exts: DEFAULT_EXTS, filename: "movie.MKV" }), true);
  assert.equal(sc({ exts: DEFAULT_EXTS, filename: "page.html" }), false);
});

test("helloConfig: offline reply and non-hello replies store nothing; missing lists fall back to defaults", () => {
  assert.equal(helloConfig({ type: "hello", offline: true, captureExts: ["x"] }), null);
  assert.equal(helloConfig({ type: "ack" }), null);
  assert.equal(helloConfig(undefined), null);
  assert.deepEqual(helloConfig({ type: "hello" }), { captureExts: DEFAULT_EXTS, excludedSites: [], hostVersion: "", captureOff: false, captureMinKB: 0 });
});

test("preselect: excluded and non-web links start unchecked, order and extra fields kept", () => {
  const out = preselect(
    [{ url: "https://a.org/x.zip", text: "x" }, { url: "https://example.com/y.zip", text: "y" }, { url: "blob:https://a.org/z", text: "z" }],
    ["zip"], ["*.example.com"]);
  assert.deepEqual(out.map((l) => [l.text, l.checked]), [["x", true], ["y", false], ["z", false]]);
  assert.deepEqual(preselect(undefined, ["zip"], []), []);
});

test("referrerFor follows strict-origin-when-cross-origin", () => {
  assert.equal(referrerFor("https://a.org/p?q=1#h", "https://a.org/x.zip"), "https://a.org/p?q=1");
  assert.equal(referrerFor("https://a.org/p?q=1", "https://dl.a.org/x.zip"), "https://a.org/");
  assert.equal(referrerFor("http://a.org/p", "https://b.net/x.zip"), "http://a.org/");
  assert.equal(referrerFor("https://a.org/p", "http://a.org/x.zip"), "");
  assert.equal(referrerFor("", "https://a.org/x.zip"), "");
  assert.equal(referrerFor("chrome://newtab/", "https://a.org/x.zip"), "");
});

test("linkExt: lowercase extension of the last path segment, query ignored, empty when none", () => {
  assert.equal(linkExt("https://a.org/dl/File.ZIP?x=.txt"), "zip");
  assert.equal(linkExt("https://a.org/my%20file.tar.gz"), "gz");
  assert.equal(linkExt("https://a.org/page"), "");
  assert.equal(linkExt("https://a.org/dir.v2/"), "");
  assert.equal(linkExt("not a url"), "");
});

test("pageExcluded is checked against the page's host; the excluded list only against the download's", () => {
  const url = "https://cdn.b.net/x.zip";
  assert.equal(sc({ url, pageUrl: "https://www.a.org/p", pageExcluded: ["*.a.org"] }), false);
  assert.equal(sc({ url, pageUrl: "https://www.a.org/p", excluded: ["*.a.org"] }), true);
  assert.equal(sc({ url, pageUrl: "", pageExcluded: ["*.a.org"] }), true);
  assert.equal(sc({ url, pageUrl: "https://c.org/", pageExcluded: ["*.a.org"] }), true);
});
