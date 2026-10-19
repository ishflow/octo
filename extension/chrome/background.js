import {
  shouldCapture, cookieHeader, referrerFor, stuckPausedIds, DEFAULT_EXTS, helloConfig, rememberPost, wasPosted, toggleSite,
} from "./lib/decide.mjs";
import { collectLinks } from "./lib/collect.mjs";
import {
  classifyMedia, addRecord, pickCandidates, variantOptions, streamSpec, videoFilename, isVideoSrc, localBlocked,
} from "./lib/media.mjs";

const HOST = "com.uba.octo";
const ACK_TIMEOUT_MS = 15000; // host may need ~10 s to launch the app
const ALT_TTL_MS = 10000;
const REFRESH_MS = 60000; // settings changed in the app reach the extension within a minute

const pending = new Map(); // downloadId -> safety timer (download is paused by us)
let alt = { held: false, at: 0 };

// ---- native host -----------------------------------------------------------

function sendHost(msg) {
  return new Promise((resolve, reject) => {
    chrome.runtime.sendNativeMessage(HOST, msg, (reply) => {
      const err = chrome.runtime.lastError;
      if (err) reject(new Error(err.message));
      else resolve(reply);
    });
  });
}

const setBadge = (on) => chrome.action.setBadgeText({ text: on ? "!" : "" });

async function hello() {
  try {
    const r = await sendHost({ type: "hello" });
    if (r?.type !== "hello") throw new Error("beklenmeyen yanıt");
    const cfg = helloConfig(r);
    // features/hideVideoButton: an older host sends neither (no stream option, button shown).
    if (cfg) {
      await chrome.storage.local.set({
        ...cfg, hostFeatures: Array.isArray(r.features) ? r.features : [], hideVideoButton: r.hideVideoButton === true, helloAt: Date.now(),
      });
    }
    setBadge(false);
    return { connected: !!cfg }; // offline: the host answered but the service is not running
  } catch (e) {
    setBadge(true);
    return { connected: false, error: e.message };
  }
}

async function getConfig() {
  return chrome.storage.local.get({ enabled: true, captureExts: DEFAULT_EXTS, excludedSites: [], localExcludedSites: [], captureOff: false, captureMinKB: 0 });
}

async function freshConfig() {
  const { helloAt = 0 } = await chrome.storage.local.get("helloAt");
  if (Date.now() - helloAt > REFRESH_MS) await hello();
  return getConfig();
}

// ---- download interception ---------------------------------------------------

const isWeb = (u) => /^https?:/i.test(u || "");
const altHeld = () => alt.held && Date.now() - alt.at < ALT_TTL_MS;

const posts = new Map(); // url -> time of a form (POST) navigation to it
// storage.session: the worker may be stopped before a slow form answers; the record must outlive it.
const postsLoaded = chrome.storage.session.get({ posts: [] })
  .then(({ posts: saved }) => { for (const [u, t] of saved) if (!posts.has(u)) posts.set(u, t); })
  .catch(() => {});
chrome.webRequest.onBeforeRequest.addListener(
  (d) => {
    if (d.method !== "POST") return;
    rememberPost(posts, d.url);
    postsLoaded.then(() => chrome.storage.session.set({ posts: [...posts] })).catch(() => {});
  },
  { urls: ["<all_urls>"], types: ["main_frame", "sub_frame"] }
);
const posted = (item) => wasPosted(posts, item.url) || wasPosted(posts, item.finalUrl || item.url);

// storage.local: must survive worker restarts, browser restarts and extension reloads.
const persist = () => chrome.storage.local.set({ pendingIds: [...pending.keys()] }).catch(() => {});

// Resume every download we paused in an earlier worker/browser lifetime. Never leave one paused.
async function recover() {
  const { pendingIds = [] } = await chrome.storage.local.get("pendingIds");
  const live = [...pending.keys()];
  const found = (await Promise.all(
    pendingIds.map((id) => chrome.downloads.search({ id }).catch(() => []))
  )).flat();
  await Promise.all(stuckPausedIds(pendingIds, live, found).map((id) => chrome.downloads.resume(id).catch(() => {})));
  await persist(); // keeps only ids this worker instance is handling
}
recover().catch(() => {});

async function release(id) {
  clearTimeout(pending.get(id)?.timer);
  pending.delete(id);
  await persist();
}

async function resume(id) {
  await release(id);
  try { await chrome.downloads.resume(id); } catch { /* already finished or gone */ }
}

async function takeOver(id) {
  await release(id);
  try {
    await chrome.downloads.cancel(id);
    await chrome.downloads.erase({ id });
  } catch { /* gone already */ }
}

async function pauseIfCapturable(id) {
  const cfg = await getConfig();
  if (!cfg.enabled || cfg.captureOff || altHeld()) return false;
  try { await chrome.downloads.pause(id); return true; } catch { return false; }
}

chrome.downloads.onCreated.addListener((item) => {
  if (!isWeb(item.url) || !isWeb(item.finalUrl || item.url)) return; // blob:, data:, file: stay with the browser
  if (item.incognito) return; // Octo would send the normal profile's cookies and keep it in its history
  // Registered synchronously: onDeterminingFilename can fire before the pause finishes.
  // A POST is never paused: it cannot be resumed. A restarted worker reads the records first.
  const ready = postsLoaded.then(() => !posted(item) && pauseIfCapturable(item.id));
  // Never lose a download: if no decision arrives, let the browser carry on.
  pending.set(item.id, { ready, timer: setTimeout(() => resume(item.id).catch(() => {}), ACK_TIMEOUT_MS + 5000) });
  ready.then((ok) => (ok ? persist() : release(item.id)));
});

// Not returning true: we never call suggest(), the browser keeps its own filename.
chrome.downloads.onDeterminingFilename.addListener((item) => {
  const entry = pending.get(item.id);
  if (!entry) return;
  entry.ready
    .then((ok) => ok && decide(item))
    .catch(() => resume(item.id));
});

async function decide(item) {
  const cfg = await freshConfig();
  const url = item.finalUrl || item.url;
  const size = item.fileSize > 0 ? item.fileSize : item.totalBytes > 0 ? item.totalBytes : -1;
  const capture = shouldCapture({
    url,
    filename: item.filename,
    exts: cfg.captureExts,
    excluded: cfg.excludedSites,
    pageUrl: item.referrer, // the page the download came from: "github.com için kapat" covers its CDN files too
    pageExcluded: cfg.localExcludedSites,
    bypass: altHeld(),
    off: cfg.captureOff,
    size,
    minKB: cfg.captureMinKB,
  });
  if (!capture) return resume(item.id);

  const msg = {
    type: "download",
    url,
    // Never the active tab: it may be unrelated to this download.
    referrer: referrerFor(item.referrer, url) || new URL(url).origin + "/",
    pageUrl: item.referrer || undefined, // "Kaynak sayfa" in Octo's info panel; stays local, never sent as a header
    cookies: await cookiesFor(url, item.referrer),
    userAgent: navigator.userAgent,
    filename: item.filename.split("/").pop(),
    size,
    mime: item.mime || "",
  };
  // A late "accepted" still wins: cancel even if the safety timer already resumed it.
  const timer = setTimeout(() => resume(item.id).catch(() => {}), ACK_TIMEOUT_MS);
  try {
    const reply = await sendHost(msg);
    setBadge(false);
    if (reply?.type === "ack" && reply.accepted === true) await takeOver(item.id);
    else await resume(item.id);
  } catch {
    setBadge(true);
    await resume(item.id);
  } finally {
    clearTimeout(timer);
  }
}

async function cookiesFor(url, page) {
  try { return cookieHeader(await chrome.cookies.getAll({ url }), page, url); } catch { return ""; }
}

// ---- context menus -----------------------------------------------------------

chrome.runtime.onInstalled.addListener(() => {
  chrome.contextMenus.removeAll(() => {
    chrome.contextMenus.create({ id: "link", title: "Octo ile indir", contexts: ["link"] });
    chrome.contextMenus.create({ id: "page", title: "Tüm bağlantıları Octo ile indir", contexts: ["page"] });
  });
  recover().catch(() => {});
  hello();
});
chrome.runtime.onStartup.addListener(() => {
  recover().catch(() => {});
  hello();
});

chrome.contextMenus.onClicked.addListener(async (info, tab) => {
  if (tab?.incognito) return; // as for captured downloads: an incognito window stays with the browser
  const pageUrl = tab?.url || info.pageUrl || "";
  if (info.menuItemId === "page" && tab?.id != null) {
    // The user picks links on links.html; nothing is queued until then.
    let links = [];
    try {
      const [r] = await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: collectLinks });
      links = r?.result ?? [];
    } catch { /* restricted page: the list opens empty */ }
    const key = crypto.randomUUID();
    await chrome.storage.session.set({ [key]: { links, pageUrl } });
    await chrome.tabs.create({ url: `links.html#${key}`, index: tab.index + 1, openerTabId: tab.id });
    return;
  }
  if (info.menuItemId !== "link" || !isWeb(info.linkUrl)) return;
  const fromUrl = info.frameUrl || pageUrl; // a link inside an iframe is requested with that frame as the referrer
  try {
    const reply = await sendHost({
      type: "download", url: info.linkUrl, referrer: referrerFor(fromUrl, info.linkUrl), userAgent: navigator.userAgent,
      cookies: await cookiesFor(info.linkUrl, fromUrl), filename: "", size: -1, mime: "", pageUrl: pageUrl || undefined,
    });
    setBadge(!(reply?.type === "ack" && reply.accepted === true));
  } catch {
    setBadge(true);
  }
});

// Cookies and Referer per link, as the browser would send them; the page's own cookies are never spread to other links.
async function sendBatch(links, pageUrl, start) {
  try {
    const own = await Promise.all((links || []).map(async (l) => ({
      ...l, cookies: await cookiesFor(l.url, pageUrl), referrer: referrerFor(pageUrl, l.url),
    })));
    // Top-level referrer: the page origin only (its cross-origin form); older hosts put it on every link.
    const reply = await sendHost({
      type: "batch", links: own, referrer: referrerFor(pageUrl, "about:blank"), pageUrl: pageUrl || undefined,
      userAgent: navigator.userAgent, start: start === true,
    });
    const ok = reply?.type === "ack" && reply.accepted === true;
    setBadge(!ok);
    return { accepted: ok };
  } catch {
    setBadge(true);
    return { accepted: false };
  }
}

// ---- video button ------------------------------------------------------------------

const TOKEN_TTL_MS = 5 * 60000;
const ERR_PROTECTED = "Bu video korumalı (DRM), indirilemez"; // the engine's own texts
const ERR_LIVE = "Canlı yayın indirilemez";

// One storage.session read-modify-write at a time (records arrive in bursts).
let queue = Promise.resolve();
const serial = (fn) => { const p = queue.then(fn); queue = p.catch(() => {}); return p; };
const mediaKey = (tabId) => `media:${tabId}`;

// MSE players give the <video> only a blob: address: remember the manifests the tab fetched.
// Observed, never blocked; the URL filter keeps the worker asleep for ordinary XHRs.
chrome.webRequest.onHeadersReceived.addListener(
  (d) => {
    if (d.tabId < 0) return; // a service worker's request belongs to no tab
    const type = d.responseHeaders?.find((h) => h.name.toLowerCase() === "content-type")?.value;
    const kind = classifyMedia(d.url, type, d.statusCode);
    if (!kind) return;
    const rec = { url: d.url, kind, frameId: d.frameId, documentUrl: d.documentUrl || "", at: Date.now() };
    serial(async () => {
      const k = mediaKey(d.tabId);
      const { [k]: list = [] } = await chrome.storage.session.get(k);
      const next = addRecord(list, rec);
      if (next !== list) await chrome.storage.session.set({ [k]: next });
    }).catch(() => {});
  },
  { urls: ["*://*/*m3u8*", "*://*/*.mpd*", "*://*/*format=mpd*"], types: ["xmlhttprequest", "media", "other"] },
  ["responseHeaders"]
);
const forgetTab = (tabId) => serial(() => chrome.storage.session.remove(mediaKey(tabId))).catch(() => {});
chrome.webRequest.onBeforeRequest.addListener(
  (d) => { if (d.type === "main_frame" && d.tabId >= 0) forgetTab(d.tabId); },
  { urls: ["<all_urls>"], types: ["main_frame"] }
);
chrome.tabs.onRemoved.addListener((tabId) => forgetTab(tabId));

// Addresses never go back to the page: it gets an opaque token for the options it may pick.
function saveToken(rec) {
  return serial(async () => {
    const token = crypto.randomUUID();
    const now = Date.now();
    const { vtokens = {} } = await chrome.storage.session.get("vtokens");
    const live = Object.fromEntries(Object.entries(vtokens).filter(([, t]) => now - t.at < TOKEN_TTL_MS));
    await chrome.storage.session.set({ vtokens: { ...live, [token]: { ...rec, at: now } } });
    return token;
  });
}

// The token's record if it was given to this tab and frame and offers id; used once.
function takeToken(token, id, sender) {
  return serial(async () => {
    const { vtokens = {} } = await chrome.storage.session.get("vtokens");
    if (typeof token !== "string" || !Object.hasOwn(vtokens, token)) return null;
    const t = vtokens[token];
    if (Date.now() - t.at >= TOKEN_TTL_MS || t.tabId !== sender.tab.id || t.frameId !== (sender.frameId ?? 0)) return null;
    if (t.kind === "direct" ? id !== "direct" : !streamSpec(t.info, id)) return null;
    delete vtokens[token];
    await chrome.storage.session.set({ vtokens });
    return t;
  });
}

// "ok" when the host can probe streams; the app may have been updated since the last hello.
async function streamHost() {
  await freshConfig();
  const has = async () => (await chrome.storage.local.get({ hostFeatures: [] })).hostFeatures.includes("stream");
  if (await has()) return "ok";
  if (!(await hello()).connected) return "offline";
  return (await has()) ? "ok" : "old";
}

async function videoProbe(msg, sender) {
  if (!isVideoSrc(msg.src)) return { state: "none" };
  if (msg.protected === true) return { state: "protected" }; // no request at all for a DRM video
  const base = { tabId: sender.tab.id, frameId: sender.frameId ?? 0, pageUrl: sender.tab.url || "", title: sender.tab.title || "" };
  const blob = msg.src.startsWith("blob:");
  const kind = blob ? null : classifyMedia(msg.src, "");
  if (!blob && !kind) {
    if (localBlocked(msg.src, [sender.origin || sender.url, base.pageUrl])) return { state: "none" };
    const token = await saveToken({ ...base, kind: "direct", url: msg.src, documentUrl: sender.url || "" });
    return { state: "ok", token, options: [{ id: "direct", label: "Videoyu indir" }] };
  }
  const k = mediaKey(base.tabId);
  const cands = (blob
    ? pickCandidates((await chrome.storage.session.get(k))[k], base.frameId, sender.origin || sender.url, sender.url)
    : [{ url: msg.src, kind, documentUrl: sender.url || "" }] // a native HLS player
  ).filter((c) => !localBlocked(c.url, [c.documentUrl, base.pageUrl]));
  if (!cands.length) return { state: "none" };
  const ready = await streamHost();
  if (ready !== "ok") return { state: ready };
  let last = "";
  for (const c of cands) {
    let r;
    try {
      r = await sendHost({
        type: "stream.probe", url: c.url, referrer: referrerFor(c.documentUrl, c.url), cookies: await cookiesFor(c.url, c.documentUrl),
        userAgent: navigator.userAgent, pageUrl: base.pageUrl || undefined,
      });
    } catch {
      setBadge(true);
      return { state: "offline" };
    }
    if (r?.type === "stream" && r.info) {
      if (r.info.protected) return { state: "protected" };
      if (r.info.live) return { state: "live" };
      const token = await saveToken({ ...base, kind: "stream", url: c.url, documentUrl: c.documentUrl, info: r.info });
      return { state: "ok", token, options: variantOptions(r.info) };
    }
    if (r?.type === "ack") return { state: "offline" }; // the host could not reach the service
    const e = String(r?.error || "");
    if (e.includes(ERR_PROTECTED)) return { state: "protected" };
    if (e.includes(ERR_LIVE)) return { state: "live" };
    if (e.startsWith("bilinmeyen")) return { state: "old" };
    last = e || last; // an expired or unsupported candidate: try the next one
  }
  return last ? { state: "error", error: last } : { state: "none" };
}

async function videoGet(msg, sender) {
  const t = await takeToken(msg.token, msg.id, sender);
  if (!t) return { accepted: false, error: "Seçenek geçersiz ya da süresi doldu. Menüyü yeniden açın." };
  const stream = t.kind === "stream" ? streamSpec(t.info, msg.id) : null;
  try {
    const reply = await sendHost({
      type: "download", url: t.url, referrer: referrerFor(t.documentUrl, t.url), cookies: await cookiesFor(t.url, t.documentUrl),
      userAgent: navigator.userAgent, filename: stream ? videoFilename(t.title, stream.label, t.info.container) : "",
      size: -1, mime: "", pageUrl: t.pageUrl || undefined, ...(stream && { stream }),
    });
    const ok = reply?.type === "ack" && reply.accepted === true;
    setBadge(!ok);
    return ok ? { accepted: true } : { accepted: false, error: reply?.error || "Octo videoyu eklemedi" };
  } catch {
    setBadge(true);
    return { accepted: false, error: "Octo açılamadı" };
  }
}

// The sender's own site (origin covers about:blank players); a host named in the message is never trusted.
async function videoOff(sender) {
  let host = "";
  try { const u = new URL(sender.origin || sender.url); if (/^https?:$/.test(u.protocol)) host = u.hostname; } catch { /* no site */ }
  if (!host) return { ok: false };
  const { videoOffSites } = await chrome.storage.local.get({ videoOffSites: [] });
  await chrome.storage.local.set({ videoOffSites: toggleSite(videoOffSites, host, true) });
  return { ok: true };
}

const videoHandlers = { "video.probe": videoProbe, "video.get": videoGet, "video.off": (_m, s) => videoOff(s) };

// ---- messages from content script / popup --------------------------------------

chrome.runtime.onMessage.addListener((msg, sender, respond) => {
  if (msg?.type === "batch" && sender.url?.startsWith(chrome.runtime.getURL("links.html"))) {
    sendBatch(msg.links, msg.pageUrl, msg.start).then(respond);
    return true;
  } else if (msg?.type === "alt") {
    alt = { held: !!msg.held, at: Date.now() };
  } else if (msg?.type === "status") {
    Promise.all([hello(), getConfig()]).then(([h, c]) => respond({ ...h, enabled: c.enabled }));
    return true;
  } else if (Object.hasOwn(videoHandlers, msg?.type) && sender.tab?.id != null && !sender.tab.incognito) {
    videoHandlers[msg.type](msg, sender).then(respond, () => respond({ state: "offline", accepted: false }));
    return true;
  }
});
