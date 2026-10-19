// Pure video helpers for the worker: no chrome.* calls, so `node --test` can load it.
// Manifests are only noticed and named here; the engine parses them (one parser, one header rule).

export const MAX_RECORDS = 30; // per tab
const MAX_PER_FRAME = 10, MAX_URL = 4096; // an ad frame cannot push out the player's records or fill storage.session
const MAX_OPTIONS = 8;
const HLS_TYPES = ["application/vnd.apple.mpegurl", "application/x-mpegurl", "audio/mpegurl", "audio/x-mpegurl"];
const VAGUE_TYPES = ["", "application/octet-stream", "binary/octet-stream", "text/plain", "application/xml", "text/xml"];

// "hls" | "dash" | null for a response the browser received.
export function classifyMedia(url, contentType, status = 200) {
  if (status < 200 || status > 299) return null;
  const type = String(contentType || "").split(";")[0].trim().toLowerCase();
  if (HLS_TYPES.includes(type)) return "hls";
  if (type === "application/dash+xml") return "dash";
  if (!VAGUE_TYPES.includes(type)) return null; // text/html (an error page), scripts, JSON…
  let u;
  try { u = new URL(url); } catch { return null; }
  const path = u.pathname.toLowerCase();
  if (/\.m3u8?$/.test(path) || /format=m3u8/i.test(u.search)) return "hls";
  if (path.endsWith(".mpd") || /format=mpd/i.test(u.search)) return "dash";
  return null;
}

// The first record of a URL stays (the master playlist is usually requested first). Over a cap the oldest record
// of the frame with most records goes, so an SPA's next video still gets in.
export function addRecord(list, rec) {
  list = list || [];
  if (rec.url.length > MAX_URL || (rec.documentUrl || "").length > MAX_URL || list.some((r) => r.url === rec.url)) return list;
  const next = [...list, rec];
  const n = (f) => next.filter((r) => r.frameId === f).length;
  while (next.length > MAX_RECORDS || n(rec.frameId) > MAX_PER_FRAME) {
    const f = n(rec.frameId) > MAX_PER_FRAME ? rec.frameId : next.reduce((a, r) => (n(r.frameId) > n(a) ? r.frameId : a), rec.frameId);
    next.splice(next.findIndex((r) => r.frameId === f), 1);
  }
  return next;
}

const originOf = (u) => { try { return new URL(u).origin; } catch { return "null"; } };
const noHash = (u) => String(u || "").split("#")[0];

// Manifests to try for a video in frameId: that frame's own, else those of same-origin frames (a third-party iframe
// must not offer the page's stream). The ones the current document fetched lead (an SPA's next video).
// site: the frame's origin or URL.
export function pickCandidates(records, frameId, site = "", documentUrl = "") {
  const all = records || [];
  const own = all.filter((r) => r.frameId === frameId);
  const origin = originOf(site);
  const found = own.length ? own : all.filter((r) => origin !== "null" && originOf(r.documentUrl) === origin);
  const cur = (r) => noHash(r.documentUrl) === noHash(documentUrl);
  const pool = [...found.filter(cur), ...found.filter((r) => !cur(r))];
  const firsts = pool.filter((r, i) => pool.findIndex((o) => o.kind === r.kind) === i);
  return [...firsts, ...pool.filter((r) => !firsts.includes(r))].slice(0, 3);
}

const quality = (v) => v.label || (v.height ? `${v.height}p` : "");
const mbps = (b) => String(Math.round(b / 1e5) / 10).replace(".", ",");

const sorted = (info) => [...(info.variants || [])]
  .sort((a, b) => (b.height || 0) - (a.height || 0) || (b.bandwidth || 0) - (a.bandwidth || 0))
  .slice(0, MAX_OPTIONS);

// Menu entries for a probed stream; the audio track is picked by the engine's default (choice lives in the app).
export function variantOptions(info) {
  const opts = sorted(info).map((v) => ({
    id: v.id,
    label: [quality(v) || "Video", v.bandwidth > 0 ? `${mbps(v.bandwidth)} Mb/sn` : ""].filter(Boolean).join(" · "),
  }));
  return opts.length ? opts : [{ id: "", label: "Videoyu indir" }];
}

// The `stream` field of a download for option id; null when the id was not offered.
export function streamSpec(info, id) {
  const v = sorted(info).find((x) => x.id === id);
  if (!v && (id !== "" || sorted(info).length)) return null;
  const audios = info.audios || [];
  const audio = v?.audio || (audios.find((a) => a.default) || audios[0])?.id || "";
  return { kind: info.kind, video: id, audio, label: v ? quality(v) : "" };
}

// "<tab title> (1080p).mp4"; the engine sanitizes again.
export function videoFilename(title, label, container) {
  const base = String(title || "").replace(/[/:\\\0]/g, " ").replace(/\s+/g, " ").trim().slice(0, 80).trim() || "video";
  return `${base}${label ? ` (${label})` : ""}.${container === "ts" ? "ts" : "mp4"}`;
}

export const isVideoSrc = (src) => typeof src === "string" && /^(https?:|blob:)/i.test(src);

// Same ranges as the engine's localIP: loopback, private, link-local, CGNAT (100.64/10), 0/8, multicast.
const local4 = ([a, b]) => a === 127 || a === 10 || a === 0 || (a === 172 && b >= 16 && b <= 31) || (a === 192 && b === 168)
  || (a === 169 && b === 254) || (a === 100 && b >= 64 && b <= 127) || (a >= 224 && a <= 239);

// The 8 hextets of an IPv6 address as URL serializes it (lowercase hex, "::" at most once, no dotted tail).
function hextets(h) {
  const [a, b] = h.split("::");
  const head = a ? a.split(":") : [], tail = b ? b.split(":") : [];
  return [...head, ...Array(b === undefined ? 0 : 8 - head.length - tail.length).fill("0"), ...tail].map((x) => parseInt(x, 16));
}

// A URL hostname on the local network: an IP literal in a local range (IPv4-mapped and NAT64 included), localhost,
// a single-label name or a local-only suffix. Names are not resolved: the engine checks the connected address.
export function isLocalHost(hostname) {
  const h = String(hostname || "").toLowerCase().replace(/\.$/, "");
  if (!h) return false;
  if (/^\d+\.\d+\.\d+\.\d+$/.test(h)) return local4(h.split(".").map(Number));
  if (!h.startsWith("[")) return !h.includes(".") || /\.(localhost|local|lan|home\.arpa|internal)$/.test(h);
  const x = hextets(h.slice(1, -1));
  const zero = (n) => x.slice(0, n).every((v) => v === 0);
  if ((zero(5) && x[5] === 0xffff) || (x[0] === 0x64 && x[1] === 0xff9b && x.slice(2, 6).every((v) => v === 0))) {
    return local4([x[6] >> 8, x[6] & 255]);
  }
  return (zero(7) && x[7] <= 1) || (x[0] & 0xfe00) === 0xfc00 || (x[0] & 0xffc0) === 0xfe80 || x[0] >> 8 === 0xff;
}

const hostOf = (u) => { try { return new URL(u).hostname; } catch { return ""; } };

// V3s M1: a local-network address a page picked is used only when the pages it came from (frame and tab) are local
// too; the browser itself would not let an outside page reach the local network with the user's cookies.
export function localBlocked(url, pages) {
  if (!isLocalHost(hostOf(url))) return false;
  const known = pages.filter(Boolean);
  return !known.length || !known.every((p) => isLocalHost(hostOf(p)));
}
