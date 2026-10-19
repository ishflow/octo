import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";

const manifest = JSON.parse(readFileSync(new URL("../manifest.json", import.meta.url), "utf8"));

test("needs Chrome 112+: storage.session holds 10 MB there (1 MB before), enough for a large 'Index of /' link list", () => {
  assert.ok(Number(manifest.minimum_chrome_version) >= 112, manifest.minimum_chrome_version);
});

test("video button: popover needs Chrome 114; video.js runs beside content.js in every frame, about:blank players too", () => {
  assert.ok(Number(manifest.minimum_chrome_version) >= 114, manifest.minimum_chrome_version);
  const [cs] = manifest.content_scripts;
  assert.deepEqual(cs.js, ["content.js", "video.js"]);
  assert.equal(cs.all_frames, true);
  assert.equal(cs.match_origin_as_fallback, true);
});

test("the video button asks for no new permission (no install-time warning on update)", () => {
  assert.deepEqual(manifest.permissions, ["downloads", "contextMenus", "cookies", "nativeMessaging", "storage", "scripting", "webRequest"]);
  assert.deepEqual(manifest.host_permissions, ["<all_urls>"]);
});
