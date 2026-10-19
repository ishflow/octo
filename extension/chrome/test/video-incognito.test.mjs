// video.js in an incognito window (L2): Octo stays out, no listeners, no button.
import test from "node:test";
import assert from "node:assert/strict";

const seen = [];
globalThis.document = { addEventListener: (t) => seen.push(t) };
globalThis.addEventListener = (t) => seen.push(t);
globalThis.chrome = { extension: { inIncognitoContext: true } };
await import("../video.js");

test("video.js stays idle in an incognito window", () => assert.deepEqual(seen, []));
