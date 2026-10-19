// Pure capture logic: no chrome.* calls, so `node --test` can load it.

// Classic download manager default capture list, lowercase. `*` and `?` are wildcards (r0* = split archives).
export const DEFAULT_EXTS = (
  "3gp 7z aac ace aif apk arj asf avi bin bz2 exe gz gzip img iso lzh m4a m4v mkv mov mp3 mp4 mpa mpe " +
  "mpeg mpg msi msu ogg ogv pdf plj pps ppt qt r0* r1* ra rar rm rmvb sea sit sitx tar tif tiff wav wma " +
  "wmv z zip"
).split(" ");

const escapeRe = (s) => s.replace(/[.+^${}()|[\]\\]/g, "\\$&");

function extRegex(ext) {
  const body = escapeRe(ext.toLowerCase()).replace(/\*/g, ".*").replace(/\?/g, ".");
  return new RegExp(`\\.${body}$`);
}

function parseUrl(url) {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u : null; // blob:, data:, file: etc. are never taken
  } catch {
    return null;
  }
}

function hostExcluded(host, patterns) {
  host = host.toLowerCase();
  return patterns.some((p) => {
    p = String(p).trim().toLowerCase();
    if (!p.startsWith("*.")) return host === p;
    p = p.slice(2); // the domain and its subdomains, like the app's HostPattern
    return host === p || host.endsWith("." + p);
  });
}

function lastSegment(path) {
  return path.slice(path.lastIndexOf("/") + 1);
}

// excluded: download hosts (the service's list, classic-manager style). pageExcluded: sites closed in the popup, matched on the page too.
// off: capture switched off in the app. size below minKB stays in the browser; an unknown size (-1) is taken.
export function shouldCapture({ url, filename, exts, excluded = [], bypass, pageUrl, pageExcluded = [], off, size = -1, minKB = 0 }) {
  if (bypass || off) return false;
  if (size >= 0 && size < minKB * 1024) return false;
  const u = parseUrl(url);
  if (!u) return false;
  if (hostExcluded(u.hostname, [...excluded, ...pageExcluded])) return false;
  const page = parseUrl(pageUrl);
  if (page && hostExcluded(page.hostname, pageExcluded)) return false;
  let name = lastSegment(filename || "");
  if (!name) {
    try { name = lastSegment(decodeURIComponent(u.pathname)); } catch { name = lastSegment(u.pathname); }
  }
  name = name.toLowerCase();
  return (exts || []).some((e) => extRegex(e).test(name));
}

// page + target given and on different sites: SameSite=Strict cookies are left out, as the browser would.
export function cookieHeader(cookies, page, target) {
  const cross = page && target && !sameSite(page, target);
  return (cookies || []).filter((c) => !(cross && c.sameSite === "strict")).map((c) => `${c.name}=${c.value}`).join("; ");
}

// shortcut: last two host labels stand in for the registrable domain (no public-suffix list); a.co.uk and b.co.uk count as one site, which only sends the cookie as before. Unparsable input counts as same site.
function sameSite(a, b) {
  try {
    const site = (u) => new URL(u).hostname.split(".").slice(-2).join(".");
    return site(a) === site(b);
  } catch { return true; }
}

// Referer the browser would send by default (strict-origin-when-cross-origin): full page same-origin, else origin, none on https -> http.
export function referrerFor(page, target) {
  let p, t;
  try { p = new URL(page); t = new URL(target); } catch { return ""; }
  if (!/^https?:$/.test(p.protocol) || (p.protocol === "https:" && t.protocol === "http:")) return "";
  return p.origin === t.origin ? p.href.split("#")[0] : p.origin + "/";
}

// Ids we paused earlier that are still paused mid-download and not handled by this worker instance.
export function stuckPausedIds(storedIds, liveIds, items) {
  const live = new Set(liveIds);
  return (storedIds || []).filter(
    (id) => !live.has(id) && (items || []).some((i) => i.id === id && i.state === "in_progress" && i.paused === true)
  );
}

// What to store from a hello reply. null when the service is down: its lists are defaults, not the user's.
export function helloConfig(r) {
  if (r?.type !== "hello" || r.offline) return null;
  return {
    captureExts: r.captureExts ?? DEFAULT_EXTS, excludedSites: r.excludedSites ?? [], hostVersion: r.version ?? "",
    captureOff: r.captureOff === true, captureMinKB: r.captureMinKB > 0 ? r.captureMinKB : 0, // absent (older app) = capture all
  };
}

// Form (POST) navigations: the engine can only GET, so their downloads stay in the browser.
export const POST_TTL_MS = 10 * 60000; // slow exports (reports, statements) answer after minutes
const bare = (u) => String(u).split("#")[0];

export function rememberPost(posts, url, now = Date.now()) {
  for (const [u, t] of posts) if (now - t > POST_TTL_MS) posts.delete(u);
  posts.set(bare(url), now);
}

export function wasPosted(posts, url, now = Date.now()) {
  const t = posts.get(bare(url));
  return t !== undefined && now - t <= POST_TTL_MS;
}

// "All links" list: links the capture list would take start checked.
export function preselect(links, exts, excluded) {
  return (links || []).map((l) => ({ ...l, checked: shouldCapture({ url: l.url, filename: "", exts, excluded }) }));
}

// Extension of the link's file name ("" when it has none); the "All links" list groups by it.
export function linkExt(url) {
  const u = parseUrl(url);
  if (!u) return "";
  let name;
  try { name = lastSegment(decodeURIComponent(u.pathname)); } catch { name = lastSegment(u.pathname); }
  const dot = name.lastIndexOf(".");
  return dot > 0 ? name.slice(dot + 1).toLowerCase() : "";
}

// Sites the user closed in the popup live beside the service's list: hello rewrites only excludedSites.
export const mergeExcluded = (fromService, local) => [...new Set([...(fromService || []), ...(local || [])])];

export function toggleSite(list, host, on) {
  const rest = (list || []).filter((h) => h !== host);
  return on ? [...rest, host] : rest;
}
