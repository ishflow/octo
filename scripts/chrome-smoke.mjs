#!/usr/bin/env node
// Real Chrome smoke test for the Octo extension (no installs: Node's built-ins + the installed Google Chrome).
//   - headless=new Chrome, throwaway --user-data-dir; branded Chrome 137+ ignores --load-extension, so the extension is
//     loaded over CDP (--remote-debugging-pipe + --enable-unsafe-extension-debugging, Extensions.loadUnpacked)
//   - the real engine (go build) runs with a temp OCTO_DATA_DIR; its native host manifest lives only in the temp profile
//   - a local HTTP server is the only network peer (--host-resolver-rules blocks every other host)
// Screenshots go to docs/test-results/chrome/ (OCTO_SHOTS=dir elsewhere). Exit: 0 passed, 1 red, 2 environment error.
// Usage: node scripts/chrome-smoke.mjs   (CHROME=/path/to/chrome to override the browser)
import { spawn, execFileSync } from "node:child_process";
import { createHash } from "node:crypto";
import fs from "node:fs";
import http from "node:http";
import net from "node:net";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const ROOT = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const EXT = path.join(ROOT, "extension/chrome");
const FX = path.join(ROOT, "engine/testdata/video");
const VIDEO_PAGE = path.join(ROOT, "scripts/video-smoke/page.html");
const SHOTS = process.env.OCTO_SHOTS || path.join(ROOT, "docs/test-results/chrome");
const CHROME = process.env.CHROME || "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const EXT_ID = "elhjlbdkhcpjihiapkeoeceopennmkab"; // manifest "key" (scripts/install-native-host.sh DEFAULT_ID)

let fail = 0, skipped = 0;
const ok = (m) => console.log(`  ✅ ${m}`);
const bad = (m) => { console.log(`  ❌ ${m}`); fail = 1; };
const skip = (m) => { console.log(`  ⏭️  ${m}`); skipped++; };
const check = (cond, m, why = "") => (cond ? ok(m) : bad(why ? `${m}: ${why}` : m));
const die = (m) => { console.error(`ortam hatası: ${m}`); process.exit(2); };
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(fn, ms = 8000, step = 100) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn().catch(() => undefined);
    if (v) return v;
    if (Date.now() > end) return v;
    await sleep(step);
  }
}

if (!fs.existsSync(CHROME)) die(`Chrome yok: ${CHROME}`);

// ---- temp workspace ------------------------------------------------------------------------
const W = fs.mkdtempSync(path.join(fs.realpathSync(os.tmpdir()), "octo-csmoke."));
const DATA = path.join(W, "data"), DL = path.join(W, "dl"), CDL = path.join(W, "chrome-dl"), UDD = path.join(W, "profile");
const SOCK = path.join(DATA, "engine.sock");
if (SOCK.length >= 100) die(`soket yolu çok uzun (${SOCK.length}): TMPDIR kısaltın`);
for (const d of [DATA, DL, CDL, path.join(UDD, "Default"), path.join(UDD, "NativeMessagingHosts"), SHOTS]) fs.mkdirSync(d, { recursive: true });
// Downloads that stay in the browser (the POST form) land here, never in ~/Downloads.
fs.writeFileSync(path.join(UDD, "Default/Preferences"), JSON.stringify({
  download: { default_directory: CDL, prompt_for_download: false, directory_upgrade: true },
  savefile: { default_directory: CDL },
}));
fs.writeFileSync(path.join(DATA, "settings.json"), JSON.stringify({ downloadDir: DL, speedLimitOn: false }));

let chrome = null, engine = null, server = null;
async function cleanup() {
  if (chrome && chrome.exitCode === null) {
    await cdp?.send("Browser.close").catch(() => {});
    await Promise.race([new Promise((r) => chrome.once("exit", r)), sleep(10000)]);
    if (chrome.exitCode === null && chrome.signalCode === null) console.error(`UYARI: Chrome kapanmadı pid=${chrome.pid}`);
  }
  if (engine && engine.exitCode === null) {
    await engineCall("shutdown").catch(() => {});
    await Promise.race([new Promise((r) => engine.once("exit", r)), sleep(30000)]);
    if (engine.exitCode === null && engine.signalCode === null) console.error(`UYARI: motor kapanmadı pid=${engine.pid}`);
  }
  server?.close();
  server?.closeAllConnections?.();
  if (process.env.OCTO_SMOKE_KEEP) console.log(`geçici klasör: ${W}`);
  else if (path.basename(W).startsWith("octo-csmoke.")) fs.rmSync(W, { recursive: true, force: true });
}

// ---- local test server ---------------------------------------------------------------------
const blob = (n, seed) => { // deterministic bytes
  const b = Buffer.alloc(n);
  let x = seed;
  for (let i = 0; i < n; i++) { x = (x * 1103515245 + 12345) >>> 0; b[i] = x >>> 24; }
  return b;
};
const FILES = { "/files/ornek.zip": blob(4 << 20, 1), "/files/a.zip": blob(300 << 10, 2), "/files/b.pdf": blob(200 << 10, 3) };
const sha = (b) => createHash("sha256").update(b).digest("hex");
const MIME = { ".m3u8": "application/vnd.apple.mpegurl", ".mpd": "application/dash+xml", ".ts": "video/mp2t", ".mp4": "video/mp4",
  ".m4s": "video/iso.segment", ".zip": "application/zip", ".pdf": "application/pdf" };
const html = (body, head = "") => `<!doctype html><html lang="tr"><meta charset="utf-8">${head}<body style="font:14px system-ui;margin:24px">${body}</body></html>`;
const VIDEO_CSS = "<style>video{display:block;width:480px;height:270px;background:#000}</style>";
const PAGES = {
  "/s/index.html": html('<h1>Octo smoke</h1><p><a id="zip" href="/files/ornek.zip">ornek.zip (4 MB)</a></p>'),
  "/s/form.html": html('<form method="post" action="/post/rapor.zip"><input name="q" value="1"><button id="go">Raporu indir</button></form>'),
  "/s/links.html": html(['<a href="/files/a.zip">Arşiv A</a>', '<a href="/files/b.pdf">Belge B</a>', '<a href="/s/hakkinda.html">Hakkında</a>',
    '<a href="/files/a.zip#ikinci">Arşiv A (tekrar)</a>', '<a href="mailto:test@example.invalid">Posta</a>'].join("<br>")),
  "/s/fs.html": html('<div id="player" style="width:640px;height:360px;background:#000"><video id="v" src="/direct.mp4" muted playsinline style="width:100%;height:100%"></video></div>'),
  "/s/eme.html": html(`<video id="v" src="/direct.mp4" muted controls></video><p id="st">bekliyor</p><script>
    (async () => { const st = document.getElementById("st");
      try { const a = await navigator.requestMediaKeySystemAccess("org.w3.clearkey", [{ initDataTypes: ["keyids"],
              videoCapabilities: [{ contentType: 'video/mp4; codecs="avc1.42E01E"' }] }]);
            await document.getElementById("v").setMediaKeys(await a.createMediaKeys()); st.textContent = "mediaKeys hazır";
      } catch (e) { st.textContent = "hata: " + e; } })();</script>`, VIDEO_CSS),
  // No inline style or script may run here: the button's look must come from adoptedStyleSheets alone.
  "/s/csp.html": html('<h1>Sıkı CSP</h1><video id="v" src="/direct.mp4" muted controls width="480" height="270"></video>'),
  "/s/wr.html": html(`<p id="st">bekliyor</p><script>
    Promise.all(["/hls-fmp4/index.m3u8?token=abc", "/s/manifest?format=mpd", "/s/play?file=video.m3u8"].map((u) => fetch(u).then((r) => r.text())))
      .then(() => { document.getElementById("st").textContent = "bitti"; });</script>`),
  "/s/blank.html": html(`<iframe id="f" style="width:520px;height:320px;border:0"></iframe><script>
    setTimeout(() => { const d = document.getElementById("f").contentDocument, v = d.createElement("video");
      Object.assign(v, { src: location.origin + "/direct.mp4", muted: true, controls: true, width: 480, height: 270 }); d.body.append(v); }, 500);</script>`),
};
const CSP = "default-src 'none'; media-src 'self'; style-src 'self'; img-src 'self'; require-trusted-types-for 'script'";
const log = []; // every request: method, path, Range/Cookie/Referer, and whether it came from the browser

function sendBody(req, res, body, type, extra = {}, throttle = false) {
  const size = body.length;
  let a = 0, b = size - 1, code = 200;
  const m = /^bytes=(\d*)-(\d*)$/.exec(req.headers.range || "");
  if (m) {
    if (m[1]) { a = +m[1]; if (m[2]) b = Math.min(+m[2], size - 1); } else { a = Math.max(0, size - +m[2]); }
    if (a > b) { res.writeHead(416, { "Content-Range": `bytes */${size}` }); return res.end(); }
    code = 206;
  }
  res.writeHead(code, { "Content-Type": type, "Content-Length": b - a + 1, "Accept-Ranges": "bytes", "Last-Modified": "Tue, 20 Oct 2026 00:00:00 GMT",
    ...(code === 206 && { "Content-Range": `bytes ${a}-${b}/${size}` }), ...extra });
  if (req.method === "HEAD") return res.end();
  const part = body.subarray(a, b + 1);
  if (!throttle) return res.end(part);
  // ~1.6 MB/s: the browser is still mid-download when the extension pauses it.
  let off = 0;
  const tick = () => {
    if (res.destroyed) return;
    if (off >= part.length) return res.end();
    res.write(part.subarray(off, off + 65536));
    off += 65536;
    setTimeout(tick, 40);
  };
  tick();
}

function handle(req, res) {
  const u = new URL(req.url, "http://x");
  const p = u.pathname;
  log.push({ method: req.method, path: p, url: req.url, range: req.headers.range || "", cookie: req.headers.cookie || "",
    referer: req.headers.referer || "", browser: !!req.headers["sec-fetch-mode"] });
  if (PAGES[p]) {
    res.writeHead(200, { "Content-Type": "text/html; charset=utf-8", "Set-Cookie": "sid=octo-smoke; Path=/; SameSite=Lax",
      ...(p === "/s/csp.html" && { "Content-Security-Policy": CSP }) });
    return res.end(PAGES[p]);
  }
  if (p === "/s/video.html") return sendBody(req, res, fs.readFileSync(VIDEO_PAGE), "text/html; charset=utf-8");
  if (p === "/s/manifest") return sendBody(req, res, fs.readFileSync(path.join(FX, "dash/manifest.mpd")), MIME[".mpd"]);
  if (p === "/s/play") return sendBody(req, res, fs.readFileSync(path.join(FX, "hls-fmp4/index.m3u8")), MIME[".m3u8"]);
  if (p === "/post/rapor.zip" && req.method === "POST") {
    req.resume();
    return req.on("end", () => sendBody(req, res, FILES["/files/a.zip"], MIME[".zip"], { "Content-Disposition": 'attachment; filename="rapor.zip"' }));
  }
  if (FILES[p]) return sendBody(req, res, FILES[p], MIME[path.extname(p)], {}, p === "/files/ornek.zip" && !req.headers.range);
  const f = path.normalize(path.join(FX, p));
  if (f.startsWith(FX + path.sep) && fs.existsSync(f) && fs.statSync(f).isFile()) {
    return sendBody(req, res, fs.readFileSync(f), MIME[path.extname(f)] || "application/octet-stream");
  }
  res.writeHead(404, { "Content-Type": "text/plain" });
  res.end("yok");
}

// ---- engine --------------------------------------------------------------------------------
function engineCall(cmd, extra = {}) {
  return new Promise((resolve, reject) => {
    const s = net.createConnection(SOCK);
    let buf = "";
    s.setTimeout(15000, () => { s.destroy(); reject(new Error("motor zaman aşımı")); });
    s.on("connect", () => s.write(JSON.stringify({ id: 1, cmd, ...extra }) + "\n"));
    s.on("data", (d) => {
      buf += d;
      const i = buf.indexOf("\n");
      if (i >= 0) { s.end(); resolve(JSON.parse(buf.slice(0, i))); }
    });
    s.on("error", reject);
  });
}
const items = async () => (await engineCall("list")).items || [];
const itemDone = (suffix, ms = 20000) => until(async () => (await items()).find((i) => i.url.endsWith(suffix) && i.state === "done"), ms, 200);

// ---- CDP over the debugging pipe (NUL-separated JSON) ---------------------------------------
let cdp = null;
class CDP {
  constructor(w, r) {
    this.w = w; this.id = 0; this.calls = new Map(); this.subs = new Set();
    let buf = Buffer.alloc(0);
    r.on("data", (d) => {
      buf = Buffer.concat([buf, d]);
      for (let i; (i = buf.indexOf(0)) >= 0; buf = buf.subarray(i + 1)) this.onMsg(JSON.parse(buf.subarray(0, i).toString()));
    });
  }
  onMsg(m) {
    if (m.id == null) { for (const f of this.subs) f(m); return; }
    const c = this.calls.get(m.id);
    this.calls.delete(m.id);
    if (m.error) c.reject(new Error(`${c.method}: ${m.error.message}`)); else c.resolve(m.result);
  }
  send(method, params = {}, sessionId) {
    const id = ++this.id;
    this.w.write(JSON.stringify({ id, method, params, ...(sessionId && { sessionId }) }) + "\0");
    return new Promise((resolve, reject) => this.calls.set(id, { resolve, reject, method }));
  }
  on(f) { this.subs.add(f); return () => this.subs.delete(f); }
}

async function session(targetId) {
  const { sessionId } = await cdp.send("Target.attachToTarget", { targetId, flatten: true });
  const send = (m, p) => cdp.send(m, p, sessionId);
  const evaluate = async (expression, opts = {}) => {
    const r = await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true, ...opts });
    if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text);
    return r.result.value;
  };
  return { targetId, sessionId, send, evaluate };
}

async function openPage(url) {
  const { targetId } = await cdp.send("Target.createTarget", { url: "about:blank" });
  const s = await session(targetId);
  await s.send("Page.enable");
  await s.send("DOM.enable");
  await s.send("Page.bringToFront");
  s.goto = async (u) => {
    let off;
    const loaded = new Promise((r) => { off = cdp.on((m) => m.sessionId === s.sessionId && m.method === "Page.loadEventFired" && r()); });
    await s.send("Page.navigate", { url: u });
    await Promise.race([loaded, sleep(10000)]);
    off();
  };
  if (url) await s.goto(url);
  return s;
}

const shot = async (s, name) => {
  const { data } = await s.send("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(path.join(SHOTS, name), Buffer.from(data, "base64"));
};
const mouse = (s, type, x, y, extra = {}) => s.send("Input.dispatchMouseEvent", { type, x, y, button: "none", ...extra });
async function click(s, x, y) {
  await mouse(s, "mouseMoved", x, y);
  await mouse(s, "mousePressed", x, y, { button: "left", clickCount: 1 });
  await mouse(s, "mouseReleased", x, y, { button: "left", clickCount: 1 });
}
const center = (r, dx = 0, dy = 0) => [dx + r.x + r.width / 2, dy + r.y + r.height / 2];
async function clickEl(s, sel) {
  const r = await s.evaluate(`document.querySelector(${JSON.stringify(sel)})?.getBoundingClientRect().toJSON()`);
  if (!r) throw new Error(`öğe yok: ${sel}`);
  await click(s, ...center(r));
}

// The button lives in a closed shadow root: CDP's DOM domain still sees it (pierce).
const HOST = "[...document.documentElement.children].find((e) => e.hasAttribute('popover'))";
const UI_STATE = `function () {
  const b = this.querySelector(".b"), p = this.querySelector(".p");
  return { btn: b.getBoundingClientRect().toJSON(), bg: getComputedStyle(b).backgroundColor, w: b.offsetWidth, open: !p.hidden,
    status: this.querySelector('[role="status"]').textContent,
    items: [...this.querySelectorAll('[role="menuitem"]')].map((e) => ({ t: e.textContent, r: e.getBoundingClientRect().toJSON(),
      off: e.getAttribute("aria-disabled") === "true" })) };
}`;
async function ui(s, doc = "document") {
  const host = HOST.replace("document", doc);
  const { result } = await s.send("Runtime.evaluate", { expression: host });
  if (!result.objectId) return null;
  const { node } = await s.send("DOM.describeNode", { objectId: result.objectId, depth: 1, pierce: true });
  const sr = node.shadowRoots?.[0];
  if (!sr) return null;
  const { object } = await s.send("DOM.resolveNode", { backendNodeId: sr.backendNodeId });
  const r = await s.send("Runtime.callFunctionOn", { objectId: object.objectId, functionDeclaration: UI_STATE, returnByValue: true });
  const open = await s.evaluate(`${host}.matches(":popover-open")`);
  return { ...r.result.value, popover: open, shadow: sr.shadowRootType };
}

// Hover the video until the button shows, open its menu; returns the menu state (items) or null.
async function openVideoMenu(s, sel, doc = "document", [ox, oy] = [0, 0]) {
  const vr = await s.evaluate(`${doc}.querySelector(${JSON.stringify(sel)}).getBoundingClientRect().toJSON()`);
  const [vx, vy] = center(vr, ox, oy);
  const shown = await until(async () => {
    await mouse(s, "mouseMoved", vx + Math.random() * 4, vy);
    const u = await ui(s, doc);
    return u?.popover && u;
  }, 6000, 250);
  if (!shown) return null;
  const [bx, by] = center(shown.btn, ox, oy);
  await mouse(s, "mouseMoved", bx, by);
  await sleep(700); // a click on a button shown a moment ago is ignored (clickjacking guard)
  await click(s, bx, by);
  return until(async () => { const u = await ui(s, doc); return u?.open && u.items.length && u; }, 8000, 150);
}
async function pickItem(s, menu, label, doc = "document", [ox, oy] = [0, 0]) {
  const it = menu.items.find((i) => i.t === label);
  if (!it) return null;
  await sleep(500); // items clicked right after the menu fills are ignored (double-click trap)
  await click(s, ...center(it.r, ox, oy));
  return until(async () => { const u = await ui(s, doc); return u?.status && !u.status.startsWith("Ekleniyor") && u.status; }, 10000, 150);
}

// =============================================================================================
async function main() {
  console.log(`Chrome: ${execFileSync("/usr/bin/defaults", ["read", path.resolve(CHROME, "../../Info.plist"), "CFBundleShortVersionString"]).toString().trim()}`);
  execFileSync("go", ["build", "-o", path.join(W, "octo"), "."], { cwd: ROOT, stdio: "inherit" });
  const elog = fs.openSync(path.join(W, "engine.log"), "a");
  engine = spawn(path.join(W, "octo"), ["serve"], { env: { ...process.env, OCTO_DATA_DIR: DATA }, stdio: ["ignore", elog, elog] });
  if (!(await until(() => engineCall("list").then((r) => r.ok), 15000, 200))) throw new Error(`motor açılmadı (${W}/engine.log)`);

  // The host must never fall back to launching the installed app: no socket, no host.
  const hostSh = path.join(W, "octo-host.sh");
  fs.writeFileSync(hostSh, `#!/bin/sh\nexport OCTO_DATA_DIR='${DATA}'\n[ -S '${SOCK}' ] || exit 1\nexec '${path.join(W, "octo")}' "$@"\n`, { mode: 0o755 });
  fs.writeFileSync(path.join(UDD, "NativeMessagingHosts/com.uba.octo.json"), JSON.stringify({
    name: "com.uba.octo", description: "Octo (smoke)", path: hostSh, type: "stdio", allowed_origins: [`chrome-extension://${EXT_ID}/`],
  }));

  server = http.createServer(handle);
  await new Promise((r) => server.listen(0, "127.0.0.1", r));
  const B = `http://127.0.0.1:${server.address().port}`;

  chrome = spawn(CHROME, [
    "--headless=new", `--user-data-dir=${UDD}`, "--remote-debugging-pipe", "--enable-unsafe-extension-debugging",
    "--no-first-run", "--no-default-browser-check", "--use-mock-keychain", "--password-store=basic", "--disable-sync",
    "--disable-background-networking", "--disable-component-update", "--disable-domain-reliability", "--no-pings",
    "--disable-client-side-phishing-detection", "--disable-default-apps", "--metrics-recording-only", "--mute-audio",
    "--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE 127.0.0.1", "--window-size=1280,900", "about:blank",
  ], { stdio: ["ignore", "ignore", fs.openSync(path.join(W, "chrome.log"), "a"), "pipe", "pipe"] });
  cdp = new CDP(chrome.stdio[3], chrome.stdio[4]);
  const dl = new Map(); // guid -> { url, name, state }
  cdp.on((m) => {
    if (m.method === "Browser.downloadWillBegin") dl.set(m.params.guid, { url: m.params.url, name: m.params.suggestedFilename, state: "begin" });
    if (m.method === "Browser.downloadProgress" && dl.has(m.params.guid)) dl.get(m.params.guid).state = m.params.state;
  });
  await cdp.send("Browser.setDownloadBehavior", { behavior: "default", eventsEnabled: true });

  // ---- 1) load + permissions ----
  console.log("# 1) eklenti yükleme ve izinler");
  const { id } = await cdp.send("Extensions.loadUnpacked", { path: EXT });
  check(id === EXT_ID, `eklenti yüklendi (${id})`, `beklenen ${EXT_ID}`);
  const swTarget = await until(async () => (await cdp.send("Target.getTargets")).targetInfos
    .find((t) => t.type === "service_worker" && t.url.startsWith(`chrome-extension://${id}/`)), 10000);
  if (!swTarget) throw new Error("servis çalışanı bulunamadı");
  let sw = await session(swTarget.targetId);
  // The worker's extension bindings appear a moment after it attaches.
  await sw.send("Runtime.runIfWaitingForDebugger").catch(() => {});
  if (!(await until(() => sw.evaluate("typeof chrome !== 'undefined' && !!chrome.permissions"), 10000, 200))) throw new Error("servis çalışanında chrome API yok");
  const swEval = async (expr) => {
    try { return await sw.evaluate(expr); } catch (e) {
      if (!/closed|No target|not found/i.test(e.message)) throw e;
      const t = (await cdp.send("Target.getTargets")).targetInfos.find((x) => x.type === "service_worker" && x.url.startsWith(`chrome-extension://${id}/`));
      sw = await session(t.targetId);
      return sw.evaluate(expr);
    }
  };
  const perms = await swEval("chrome.permissions.getAll()");
  check(["scripting", "webRequest"].every((p) => perms.permissions.includes(p)), `izinler verildi: ${perms.permissions.join(", ")}`);
  const manifest = fs.readFileSync(path.join(EXT, "manifest.json"), "utf8");
  const old = fs.readFileSync(path.join(ROOT, "scripts/testdata/manifest-pre-scripting.json"), "utf8"); // manifest before scripting/webRequest
  const warn = await swEval(`(async () => typeof chrome.management?.getPermissionWarningsByManifest !== "function" ? null : {
    now: await chrome.management.getPermissionWarningsByManifest(${JSON.stringify(manifest)}),
    old: await chrome.management.getPermissionWarningsByManifest(${JSON.stringify(old)}) })()`);
  if (!warn) skip("management.getPermissionWarningsByManifest yok: uyarı karşılaştırması yapılamadı");
  else {
    console.log(`     uyarılar (yeni): ${JSON.stringify(warn.now)}`);
    check(JSON.stringify([...warn.now].sort()) === JSON.stringify([...warn.old].sort()),
      "scripting + webRequest yeni izin uyarısı getirmiyor (güncellemede eklenti devre dışı kalmaz)", `eski ${JSON.stringify(warn.old)}`);
  }
  const helloAt = await until(() => swEval("chrome.storage.local.get('helloAt').then((s) => s.helloAt)"), 10000, 250);
  check(helloAt > 0, "native host hello yanıtladı (gerçek motor, geçici profil)");
  const features = await swEval("chrome.storage.local.get({ hostFeatures: [] }).then((s) => s.hostFeatures)");
  console.log(`     hostFeatures: ${JSON.stringify(features)}`);

  // ---- 2) .zip click: starts in the app, cancelled in the browser ----
  console.log("# 2) .zip bağlantısı tıklaması");
  const page = await openPage(`${B}/s/index.html`);
  await clickEl(page, "#zip");
  const zip = await itemDone("/files/ornek.zip", 30000);
  check(zip, "motor ornek.zip'i indirdi");
  if (zip) {
    const f = path.join(zip.dir || DL, zip.name);
    check(fs.existsSync(f) && sha(fs.readFileSync(f)) === sha(FILES["/files/ornek.zip"]), `dosya bayt bayt aynı (${zip.name}, ${zip.size} B)`);
    check(zip.pageUrl === `${B}/s/index.html`, "Kaynak sayfa geldi", zip.pageUrl);
  }
  const eng = log.filter((r) => r.path === "/files/ornek.zip" && !r.browser);
  check(eng.length && eng.every((r) => r.cookie.includes("sid=octo-smoke")), `motor isteklerinde tarayıcı çerezi (${eng.length} istek)`);
  check(eng.length && eng.every((r) => r.referer === `${B}/s/index.html`), "motor isteklerinde Referer = sayfa", eng[0]?.referer);
  const zipDl = await until(async () => [...dl.values()].find((d) => d.url.endsWith("/files/ornek.zip") && d.state !== "begin" && d.state !== "inProgress"), 5000);
  check(zipDl?.state === "canceled", "tarayıcıda iptal edildi (Browser.downloadProgress)", zipDl?.state);
  check(!fs.readdirSync(CDL).some((n) => n.startsWith("ornek")), "tarayıcı klasöründe ornek.zip yok");
  check((await swEval("chrome.downloads.search({ query: ['ornek'] })")).length === 0, "chrome.downloads geçmişinden silindi");

  // ---- 3) POST form stays in the browser ----
  console.log("# 3) POST formu");
  await page.goto(`${B}/s/form.html`);
  await clickEl(page, "#go");
  const post = await until(async () => [...dl.values()].find((d) => d.url.endsWith("/post/rapor.zip") && d.state === "completed"), 15000);
  check(post, "POST indirmesi tarayıcıda tamamlandı", JSON.stringify([...dl.values()]));
  check(fs.existsSync(path.join(CDL, "rapor.zip")), "dosya tarayıcı klasöründe");
  check(!(await items()).some((i) => i.url.includes("/post/")), "motora gitmedi");
  check(log.filter((r) => r.path === "/post/rapor.zip").length === 1, "POST bir kez gönderildi (yeniden istek yok)");
  const dpage = await openPage("chrome://downloads/");
  await sleep(500);
  await shot(dpage, "02-chrome-indirmeler.png");
  await cdp.send("Target.closeTarget", { targetId: dpage.targetId });

  // ---- 4) links.html ----
  console.log("# 4) Tüm bağlantılar (links.html)");
  await page.goto(`${B}/s/links.html`);
  // Headless Chrome shows no context menu: run the "page" menu handler's body (background.js) with the real collectLinks.
  const collect = fs.readFileSync(path.join(EXT, "lib/collect.mjs"), "utf8").replace(/^export /m, "");
  const n = await swEval(`(async () => { ${collect}
    const tab = (await chrome.tabs.query({})).find((t) => t.url?.endsWith("/s/links.html"));
    const [r] = await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: collectLinks });
    const key = crypto.randomUUID();
    await chrome.storage.session.set({ [key]: { links: r.result, pageUrl: tab.url } });
    await chrome.tabs.create({ url: "links.html#" + key, index: tab.index + 1, openerTabId: tab.id });
    return r.result.length; })()`);
  check(n === 3, `scripting.executeScript ile ${n} bağlantı toplandı (http, tekrarsız)`);
  const lt = await until(async () => (await cdp.send("Target.getTargets")).targetInfos.find((t) => t.url.includes(`${id}/links.html#`)), 5000);
  const lp = await session(lt.targetId);
  await lp.send("Page.enable");
  const rows = await until(() => lp.evaluate(`document.getElementById("count")?.textContent && [...document.querySelectorAll("#list li")]
    .map((li) => ({ url: li.querySelector(".url").textContent, on: li.querySelector("input").checked }))`), 5000);
  const want = { [`${B}/files/a.zip`]: true, [`${B}/files/b.pdf`]: true, [`${B}/s/hakkinda.html`]: false };
  check(rows?.length === 3 && rows.every((r) => want[r.url] === r.on), "liste: zip ve pdf işaretli, html işaretsiz", JSON.stringify(rows));
  await clickEl(lp, "#start");
  await clickEl(lp, "#add");
  const st = await until(async () => { const t = await lp.evaluate('document.getElementById("status").textContent'); return /eklendi|eklenemedi/.test(t) && t; }, 10000);
  check(st?.includes("eklendi ve indirme başlatıldı"), `durum: ${st}`);
  await shot(lp, "01-links-sayfasi.png");
  check((await itemDone("/files/a.zip")) && (await itemDone("/files/b.pdf")), "seçilen 2 bağlantı motorda indi");
  check(!(await items()).some((i) => i.url.endsWith("/hakkinda.html")), "işaretsiz bağlantı gönderilmedi");
  await cdp.send("Target.closeTarget", { targetId: lt.targetId });

  // ---- 5) video button ----
  console.log("# 5) video düğmesi");
  if (!fs.existsSync(path.join(FX, "direct.mp4"))) { skip("video fikstürleri yok (scripts/make-video-fixtures.sh)"); return; }
  await page.goto(`${B}/s/video.html`);
  await until(() => page.evaluate('document.getElementById("direct").readyState >= 1 && /hazır:/.test(document.getElementById("mse-state").textContent)'), 10000);
  let m = await openVideoMenu(page, "#direct");
  check(m?.shadow === "closed" && m.w === 32, "düğme kapalı shadow root içinde, 32 px", JSON.stringify(m && { shadow: m.shadow, w: m.w }));
  check(m?.items.map((i) => i.t).join("|") === "Videoyu indir|Bu sitede gösterme", "doğrudan video menüsü", JSON.stringify(m?.items.map((i) => i.t)));
  await shot(page, "03-video-menu-dogrudan.png");
  const got = m && await pickItem(page, m, "Videoyu indir");
  check(got === "Octo'ya eklendi", `seçim durumu: ${got}`);
  check(await itemDone("/direct.mp4"), "motor direct.mp4'ü indirdi");
  await mouse(page, "mouseMoved", 5, 5);
  await sleep(2500); // the status closes the menu after 2 s

  const tabId = await swEval(`chrome.tabs.query({}).then((t) => t.find((x) => x.url?.endsWith("/s/video.html")).id)`);
  const rec = await swEval(`chrome.storage.session.get("media:${tabId}").then((s) => s["media:${tabId}"] || [])`);
  check(rec.some((r) => r.url.endsWith("/hls-fmp4/index.m3u8") && r.kind === "hls"), "webRequest MSE manifestini kaydetti", JSON.stringify(rec.map((r) => r.url)));
  m = await openVideoMenu(page, "#mse");
  const first = m?.items[0];
  if (features.includes("stream")) {
    check(first && !first.off, `MSE menüsünde akış seçeneği: ${first?.t}`);
    const r = first && await pickItem(page, m, first.t);
    check(r === "Octo'ya eklendi", `akış seçimi: ${r}`);
    check(await until(async () => (await items()).find((i) => i.url.endsWith("/hls-fmp4/index.m3u8") && i.state === "done"), 30000, 300), "motor akışı indirdi");
  } else {
    check(first?.t === "Octo'yu güncelleyin" && first.off, "akış bilmeyen motor: 'Octo'yu güncelleyin'", first?.t);
  }
  await shot(page, "04-video-mse.png");
  await mouse(page, "mouseMoved", 5, 5);
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });

  // fullscreen: the button is re-shown above the fullscreen player
  await page.goto(`${B}/s/fs.html`);
  await until(() => page.evaluate('document.getElementById("v").readyState >= 1'), 8000);
  const vr = await page.evaluate('document.getElementById("v").getBoundingClientRect().toJSON()');
  const shown = await until(async () => { await mouse(page, "mouseMoved", ...center(vr)); return (await ui(page))?.popover; }, 5000, 250);
  check(shown, "tam ekran öncesi düğme görünüyor");
  await page.evaluate('document.getElementById("player").requestFullscreen().then(() => true)', { userGesture: true });
  let last = null;
  const fs1 = await until(async () => {
    const u = await ui(page);
    if (!u?.popover) return null;
    const top = await page.evaluate(`(() => { const r = ${JSON.stringify(u.btn)}; const e = document.elementFromPoint(r.x + r.width / 2, r.y + r.height / 2); return e === ${HOST} || [e?.tagName, e?.id, JSON.stringify(r), innerWidth, innerHeight].join(" "); })()`);
    last = { full: await page.evaluate("document.fullscreenElement?.id"), top };
    return top === true && last;
  }, 3000, 100) || last;
  check(fs1?.full === "player" && fs1.top, "tam ekranda düğme oynatıcının üstünde (elementFromPoint = düğme)", JSON.stringify(fs1));
  await shot(page, "05-tam-ekran.png");
  await page.evaluate("document.exitFullscreen().then(() => true)");
  await page.evaluate('document.getElementById("v").requestFullscreen().then(() => true)', { userGesture: true });
  await sleep(400);
  await mouse(page, "mouseMoved", 640, 450);
  await sleep(400);
  check((await ui(page))?.popover === false, "videonun kendisi tam ekranken düğme gizli (tasarım gereği)");
  await page.evaluate("document.exitFullscreen().then(() => true)");

  // EME: mediaKeys set only by the page's main world must be visible to the isolated world
  await page.goto(`${B}/s/eme.html`);
  const eme = await until(async () => { const t = await page.evaluate('document.getElementById("st").textContent'); return t !== "bekliyor" && t; }, 8000);
  if (eme !== "mediaKeys hazır") skip(`Clear Key kurulamadı (${eme}): mediaKeys denetimi atlandı`);
  else {
    m = await openVideoMenu(page, "#v");
    check(m?.items[0]?.t === "Bu video korumalı (DRM), indirilemez", "yalıtılmış dünya video.mediaKeys'i görüyor → korumalı", m?.items[0]?.t);
    await shot(page, "06-eme-korumali.png");
  }

  // strict CSP: no inline style/script; the closed shadow root's adopted sheet still applies
  await page.goto(`${B}/s/csp.html`);
  await until(() => page.evaluate('document.getElementById("v").readyState >= 1'), 8000);
  m = await openVideoMenu(page, "#v");
  // #5B3FD6, or its :hover tone while the pointer is still on the button after the click
  check(["rgb(91, 63, 214)", "rgb(74, 48, 194)"].includes(m?.bg) && m.w === 32 && m.items[0]?.t === "Videoyu indir", "sıkı CSP'li sayfada düğme biçimli ve çalışıyor", JSON.stringify(m && { bg: m.bg, w: m.w }));
  await shot(page, "07-siki-csp.png");

  // clickjacking (VGR M3): the page script hides or covers the button; a real click must not open the menu
  await page.goto(`${B}/s/fs.html`);
  await until(() => page.evaluate('document.getElementById("v").readyState >= 1'), 8000);
  const cvr = await page.evaluate('document.getElementById("v").getBoundingClientRect().toJSON()');
  const cj = await until(async () => { await mouse(page, "mouseMoved", ...center(cvr)); const u = await ui(page); return u?.popover && u; }, 5000, 250);
  const [cx, cy] = cj ? center(cj.btn) : [0, 0];
  await mouse(page, "mouseMoved", cx, cy); // resting on the button keeps it shown
  const OVERLAY = `(() => { const d = document.createElement("div"); d.id = "cj"; d.popover = "manual";
    d.style.cssText = "position:fixed;inset:0;width:100vw;height:100vh;max-width:none;max-height:none;margin:0;border:0;background:#c00;pointer-events:none";
    d.textContent = "Oynat"; document.body.append(d); d.showPopover(); return true; })()`;
  for (const [name, on, undo] of [
    ["sayfa satır içi opacity:0 yazdı", `${HOST}.style.setProperty("opacity", "0", "important")`, `${HOST}.style.setProperty("opacity", "1", "important")`],
    ["sayfanın popover'ı pointer-events:none ile üstte", OVERLAY, 'document.getElementById("cj").remove()'],
  ]) {
    await page.evaluate(on);
    await sleep(700); // past the IntersectionObserver v2 delay and the visibility wait
    if (name.includes("popover")) await shot(page, "09-clickjacking-ortu.png");
    await click(page, cx, cy);
    await sleep(800);
    const u = await ui(page);
    check(u && !u.open, `clickjacking: ${name} → tıklama yok sayıldı`, JSON.stringify(u && { open: u.open, popover: u.popover }));
    if (u?.open) await click(page, cx, cy);
    await page.evaluate(undo);
  }
  await sleep(700);
  await click(page, cx, cy);
  const cjOk = await until(async () => { const u = await ui(page); return u?.open && u.items.length && u; }, 8000, 150); // "Yükleniyor…" has no items yet
  check(cjOk?.items[0]?.t === "Videoyu indir", "clickjacking: örtü kalkınca düğme yine çalışıyor", JSON.stringify(cjOk?.items.map((i) => i.t)));
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27 });

  // webRequest URL patterns vs. the query part
  await page.goto(`${B}/s/wr.html`);
  await until(() => page.evaluate('document.getElementById("st").textContent === "bitti"'), 8000);
  await sleep(300);
  const wrTab = await swEval(`chrome.tabs.query({}).then((t) => t.find((x) => x.url?.endsWith("/s/wr.html")).id)`);
  const wr = (await swEval(`chrome.storage.session.get("media:${wrTab}").then((s) => s["media:${wrTab}"] || [])`)).map((r) => `${r.url.slice(B.length)}=${r.kind}`);
  for (const [u, k] of [["/hls-fmp4/index.m3u8?token=abc", "hls"], ["/s/manifest?format=mpd", "dash"], ["/s/play?file=video.m3u8", "hls"]]) {
    check(wr.includes(`${u}=${k}`), `webRequest deseni yakaladı: ${u}`, JSON.stringify(wr));
  }

  // about:blank iframe: content script injected, "Bu sitede gösterme" takes the embedding site's host
  await page.goto(`${B}/s/blank.html`);
  const DOC = 'document.getElementById("f").contentDocument';
  const ready = await until(() => page.evaluate(`${DOC}.querySelector("video")?.readyState >= 1`), 8000);
  const fr = await page.evaluate('document.getElementById("f").getBoundingClientRect().toJSON()');
  m = ready && await openVideoMenu(page, "video", DOC, [fr.x, fr.y]);
  check(m?.items[0]?.t === "Videoyu indir", "about:blank çerçevede düğme ve menü", JSON.stringify(m?.items.map((i) => i.t)));
  await shot(page, "08-about-blank.png");
  if (m) {
    const it = m.items.find((i) => i.t === "Bu sitede gösterme");
    await sleep(500);
    await click(page, ...center(it.r, fr.x, fr.y));
    const sites = await until(async () => { const v = await swEval('chrome.storage.local.get({ videoOffSites: [] }).then((s) => s.videoOffSites)'); return v.length && v; }, 5000);
    check(JSON.stringify(sites) === '["127.0.0.1"]', "about:blank çerçeveden 'Bu sitede gösterme' → 127.0.0.1", JSON.stringify(sites));
    await swEval("chrome.storage.local.set({ videoOffSites: [] })");
  }
}

try {
  await main();
} catch (e) {
  bad(`beklenmeyen hata: ${e.stack || e}`);
} finally {
  await cleanup();
}
console.log(`\nSONUÇ: ${fail ? "KIRMIZI" : "GEÇTİ"} (atlanan: ${skipped})`);
process.exit(fail);
