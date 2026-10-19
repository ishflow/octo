// collectLinks (lib/collect.mjs) against a fake document.
import test from "node:test";
import assert from "node:assert/strict";
import { collectLinks } from "../lib/collect.mjs";

const a = (href, text = "", download) => ({ href, textContent: text, getAttribute: (n) => (n === "download" ? download ?? null : null) });

test("keeps http(s) links once, drops fragments, non-web schemes and duplicates; text falls back to download name", () => {
  globalThis.document = { links: [
    a("https://a.org/x.zip#top", "  X  "),
    a("https://a.org/x.zip", "again"),
    a("mailto:me@a.org", "mail"),
    a("javascript:void(0)", "js"),
    a("https://a.org/y.iso", "", "y-name.iso"),
    a("http://b.net/z", "z".repeat(300)),
  ] };
  const links = collectLinks();
  assert.deepEqual(links.map((l) => l.url), ["https://a.org/x.zip", "https://a.org/y.iso", "http://b.net/z"]);
  assert.deepEqual(links.map((l) => l.text.length), [1, 10, 200]);
  assert.equal(links[0].text, "X");
  assert.equal(links[1].text, "y-name.iso");
});
