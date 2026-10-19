// "Octo ile indir" button over videos (closed Shadow DOM popover). Runs in every frame of every page, so:
// idle until a <video> plays, page data is never an instruction, synthetic (untrusted) events are ignored,
// no HTML parsing (CSP / Trusted Types safe). Addresses stay in the worker; the page only sees option labels.
(() => {
  if (chrome.extension?.inIncognitoContext) return; // incognito stays with the browser (L2)
  const MIN_W = 200, MIN_H = 120, SIZE = 32, IDLE_MS = 2000, STATUS_MS = 2000;
  const SEEN_MS = 500, ITEMS_MS = 400; // clickjacking: a control counts only once it was plainly visible this long
  const TEXT = {
    protected: "Bu video korumalı (DRM), indirilemez",
    live: "Canlı yayın indirilemez",
    none: "Akış adresi bulunamadı. Sayfayı yenileyip videoyu oynatın.",
    old: "Octo'yu güncelleyin",
    offline: "Octo açılamadı",
  };
  const CSS = `
    .w { all: initial; display: block; position: relative; color: #1d1d1f;
         font: 13px/16px -apple-system, BlinkMacSystemFont, "Segoe UI", system-ui, sans-serif; -webkit-font-smoothing: antialiased; }
    .b { all: unset; box-sizing: border-box; display: grid; place-items: center; width: ${SIZE}px; height: ${SIZE}px;
         border-radius: 50%; background: #5B3FD6; color: #fff; cursor: pointer;
         box-shadow: 0 1px 4px rgba(0,0,0,.4), 0 0 0 1px rgba(255,255,255,.4); }
    .b:hover { background: #4A30C2; }
    .b:focus-visible { outline: 2px solid #fff; outline-offset: 1px; box-shadow: 0 0 0 5px #5B3FD6; }
    .b svg { width: 18px; height: 18px; fill: none; stroke: currentColor; stroke-width: 2; stroke-linecap: round; stroke-linejoin: round; }
    .p { position: absolute; top: ${SIZE + 6}px; right: 0; box-sizing: border-box; min-width: 230px; max-width: 320px; padding: 5px;
         border-radius: 10px; background: #fff; box-shadow: 0 8px 28px rgba(0,0,0,.28), 0 0 0 .5px rgba(0,0,0,.18); }
    .p[hidden] { display: none; }
    .i { all: unset; box-sizing: border-box; display: block; width: 100%; padding: 7px 10px; border-radius: 6px; cursor: pointer; }
    .i:hover, .i:focus-visible { background: #5B3FD6; color: #fff; }
    .i[aria-disabled="true"] { cursor: default; color: #6e6e73; background: none; }
    .i[aria-disabled="true"]:focus-visible { outline: 2px solid #5B3FD6; outline-offset: -2px; }
    .sep { height: 1px; margin: 4px 0; background: rgba(0,0,0,.1); }
    .s { padding: 7px 10px; color: #6e6e73; }
    .s:empty { display: none; }
    @media (prefers-color-scheme: dark) {
      .w { color: #f5f5f7; }
      .p { background: #2a2a2e; box-shadow: 0 8px 28px rgba(0,0,0,.5), 0 0 0 .5px rgba(255,255,255,.15); }
      .i:hover, .i:focus-visible { background: #6E58EE; }
      .i[aria-disabled="true"], .s { color: #a1a1a6; }
      .sep { background: rgba(255,255,255,.12); }
    }`;
  // Inline !important beats the page's own rules for the host; display is left to the popover state.
  const HOST_STYLE = {
    position: "fixed", inset: "auto", margin: "0", border: "0", padding: "0", width: `${SIZE}px`, height: `${SIZE}px`,
    overflow: "visible", background: "transparent", opacity: "1", visibility: "visible", transform: "none", filter: "none",
    "clip-path": "none", "pointer-events": "auto", "max-width": "none", "max-height": "none", "z-index": "2147483647",
    "mask-image": "none", "mix-blend-mode": "normal", scale: "none", rotate: "none", translate: "none", zoom: "1",
  };
  // Re-checked on every click: a page script can overwrite the inline !important declarations.
  const VISUAL = ["opacity", "visibility", "transform", "filter", "clip-path", "pointer-events", "mask-image", "mix-blend-mode",
    "scale", "rotate", "translate", "zoom"];

  let videos = []; // WeakRef<HTMLVideoElement>, in the order they first played
  const drm = new WeakSet();
  const seen = new Map(); // control → since when IntersectionObserver v2 sees it unoccluded and undistorted (0: not now)
  let ui = null, current = null, menuOpen = false, menuGen = 0, idle = 0, frame = 0, started = false, off = true;
  let px = -1, py = -1, moveQueued = false, itemsAt = 0;

  const tracked = () => (videos = videos.filter((r) => r.deref())).map((r) => r.deref()).filter((v) => v.isConnected);
  const inside = (r, x, y) => x >= r.left && x <= r.right && y >= r.top && y <= r.bottom;

  function onMedia(e) {
    const v = e.target;
    if (!e.isTrusted || !(v instanceof HTMLVideoElement)) return;
    if (e.type === "encrypted") drm.add(v);
    if (!videos.some((r) => r.deref() === v)) videos.push(new WeakRef(v));
    if (!started) start();
  }
  // Media events do not bubble, but the capture phase passes through document.
  for (const t of ["loadedmetadata", "play", "encrypted", "emptied"]) document.addEventListener(t, onMedia, true);

  // This frame's site and the sites embedding it: "Bu sitede gösterme" on a page covers its embedded players.
  function sites() {
    const out = [];
    for (const o of [self.origin, ...(location.ancestorOrigins || [])]) {
      try { out.push(new URL(o).hostname); } catch { /* opaque origin */ }
    }
    return out;
  }

  function start() {
    started = true;
    const keys = { enabled: true, hideVideoButton: false, videoOffSites: [] };
    const load = () => chrome.storage.local.get(keys).then((s) => {
      off = !s.enabled || s.hideVideoButton || sites().some((h) => s.videoOffSites.includes(h));
      if (off) hide(true);
    }).catch(() => {});
    load();
    chrome.storage.onChanged.addListener((ch, area) => { if (area === "local" && Object.keys(keys).some((k) => k in ch)) load(); });
    addEventListener("mousemove", onMove, { capture: true, passive: true });
    document.addEventListener("fullscreenchange", onFullscreen, true);
    document.addEventListener("focusin", onFocus, true);
    // The pointer left the window (it may have rested on the button, so the idle timer kept it): no more mousemove.
    document.addEventListener("mouseout", (e) => { if (e.isTrusted && !e.relatedTarget) hide(); }, true);
  }

  function eligible(v, r = v.getBoundingClientRect()) {
    if (r.width < MIN_W || r.height < MIN_H || document.fullscreenElement === v) return false;
    const cs = getComputedStyle(v);
    return cs.display !== "none" && cs.visibility !== "hidden" && Number(cs.opacity) !== 0;
  }

  // The video's rectangle, not the event target: player overlays cover the <video>.
  function onMove(e) {
    if (!e.isTrusted || off) return;
    px = e.clientX; py = e.clientY;
    if (moveQueued) return;
    moveQueued = true;
    requestAnimationFrame(() => {
      moveQueued = false;
      if (menuOpen) return;
      const v = tracked().find((x) => { const r = x.getBoundingClientRect(); return inside(r, px, py) && eligible(x, r); });
      if (!v) return hide();
      show(v);
      clearTimeout(idle);
      idle = setTimeout(() => { if (!ui?.host.matches(":hover")) hide(); }, IDLE_MS);
    });
  }

  // Keyboard users: focusing the player shows the button (it sits at the end of the Tab order).
  function onFocus(e) {
    if (!e.isTrusted || off || (ui && e.target === ui.host)) return;
    const v = tracked().find((x) => x === e.target || x.parentElement?.contains(e.target));
    if (v && eligible(v)) {
      clearTimeout(idle);
      show(v);
    } else if (current && !inside(current.getBoundingClientRect(), px, py)) {
      hide();
    }
  }

  // Entering fullscreen hides every popover; showing it again puts the button above the fullscreen element.
  function onFullscreen(e) {
    if (!e.isTrusted || !current) return;
    const v = current;
    if (!eligible(v)) return hide(true);
    try { ui.host.hidePopover(); } catch { /* already hidden */ }
    show(v);
  }

  function show(v) {
    if (off) return;
    ui ??= build();
    current = v;
    if (!ui.host.isConnected) document.documentElement.append(ui.host);
    place();
    if (!ui.host.matches(":popover-open")) {
      try { ui.host.showPopover(); } catch { /* the page removed or moved it */ }
    }
    if (!frame) frame = requestAnimationFrame(follow);
  }

  function follow() {
    frame = 0;
    if (!current || !ui.host.matches(":popover-open")) return;
    if (!current.isConnected || !eligible(current)) return hide(true);
    place();
    frame = requestAnimationFrame(follow);
  }

  function place() {
    const r = current.getBoundingClientRect();
    ui.host.style.setProperty("top", `${Math.max(r.top, 0) + 8}px`, "important");
    ui.host.style.setProperty("left", `${Math.min(r.right, innerWidth) - 8 - SIZE}px`, "important");
  }

  function hide(force) {
    if (!ui || (menuOpen && !force)) return;
    closeMenu(false);
    current = null;
    try { ui.host.hidePopover(); } catch { /* not shown */ }
  }

  const el = (tag, attrs = {}) => {
    const e = document.createElement(tag);
    for (const [k, v] of Object.entries(attrs)) e.setAttribute(k, v);
    return e;
  };

  function icon() {
    const NS = "http://www.w3.org/2000/svg";
    const svg = document.createElementNS(NS, "svg");
    svg.setAttribute("viewBox", "0 0 24 24");
    svg.setAttribute("aria-hidden", "true");
    const path = document.createElementNS(NS, "path");
    path.setAttribute("d", "M12 4v11M7 10l5 5 5-5M5 20h14");
    svg.append(path);
    return svg;
  }

  function build() {
    const host = el("div", { popover: "manual" });
    for (const [k, v] of Object.entries(HOST_STYLE)) host.style.setProperty(k, v, "important");
    const root = host.attachShadow({ mode: "closed" });
    const sheet = new CSSStyleSheet();
    sheet.replaceSync(CSS);
    root.adoptedStyleSheets = [sheet];
    const wrap = el("div", { class: "w" });
    const btn = el("button", { class: "b", type: "button", "aria-label": "Octo ile indir", "aria-haspopup": "menu", "aria-expanded": "false" });
    btn.append(icon());
    const panel = el("div", { class: "p" });
    panel.hidden = true;
    const menu = el("div", { role: "menu", "aria-label": "Octo" });
    const status = el("div", { class: "s", role: "status" });
    panel.append(menu, status);
    wrap.append(btn, panel);
    root.append(wrap);
    btn.addEventListener("click", (e) => {
      if (!e.isTrusted) return;
      if (menuOpen) closeMenu(true);
      else if (visible(btn, btn)) openMenu();
    });
    root.addEventListener("keydown", onKey);
    const io = new IntersectionObserver((es) => {
      for (const e of es) seen.set(e.target, e.isVisible ? seen.get(e.target) || e.time : 0);
    }, { trackVisibility: true, delay: 100 });
    io.observe(btn);
    io.observe(panel);
    return { host, root, btn, panel, menu, status };
  }

  // Clickjacking: a real click counts only on a control the person could plainly see for a moment, not one the page
  // made transparent or distorted, covered, or showed just before the click. A pointer-events: none overlay escapes
  // elementFromPoint, not IntersectionObserver v2.
  function visible(control, target) {
    const since = seen.get(control);
    if (!since || performance.now() - since < SEEN_MS || !ui.host.matches(":popover-open")) return false;
    const cs = getComputedStyle(ui.host);
    if (VISUAL.some((k) => cs.getPropertyValue(k) !== HOST_STYLE[k])) return false;
    const r = target.getBoundingClientRect();
    return document.elementFromPoint((r.left + r.right) / 2, (r.top + r.bottom) / 2) === ui.host;
  }

  async function openMenu() {
    const v = current;
    if (!v) return;
    const gen = ++menuGen;
    menuOpen = true;
    ui.btn.setAttribute("aria-expanded", "true");
    ui.panel.hidden = false;
    ui.menu.replaceChildren();
    ui.status.textContent = "Yükleniyor…";
    document.addEventListener("pointerdown", outside, true);
    let isDrm = drm.has(v);
    try { isDrm ||= v.mediaKeys != null; } catch { /* not readable here */ }
    const r = await send({ type: "video.probe", src: v.currentSrc || v.src || "", protected: isDrm });
    if (gen !== menuGen) return;
    const items = r?.state === "ok" && Array.isArray(r.options)
      ? r.options.map((o) => ({ label: String(o.label), run: () => pick(r.token, o.id) }))
      : [{ label: r?.state === "error" ? String(r.error) : TEXT[r?.state] || TEXT.offline }];
    items.push(null, { label: "Bu sitede gösterme", run: siteOff });
    ui.menu.replaceChildren(...items.map(item));
    itemsAt = performance.now();
    ui.status.textContent = "";
    ui.menu.querySelector?.('[role="menuitem"]')?.focus();
  }

  function item(it) {
    if (!it) return el("div", { class: "sep", role: "separator" });
    const e = el(it.run ? "button" : "div", { class: "i", role: "menuitem", tabindex: "-1" });
    if (!it.run) e.setAttribute("aria-disabled", "true");
    e.textContent = it.label;
    if (it.run) {
      e.addEventListener("click", (ev) => {
        if (ev.isTrusted && performance.now() - itemsAt >= ITEMS_MS && visible(ui.panel, e)) it.run();
      });
    }
    return e;
  }

  async function pick(token, id) {
    const gen = menuGen;
    ui.btn.focus();
    ui.menu.replaceChildren();
    ui.status.textContent = "Ekleniyor…";
    const r = await send({ type: "video.get", token, id });
    if (gen !== menuGen) return;
    ui.status.textContent = r?.accepted ? "Octo'ya eklendi" : String(r?.error || TEXT.offline);
    setTimeout(() => { if (gen === menuGen) closeMenu(false); }, STATUS_MS);
  }

  function siteOff() {
    send({ type: "video.off" });
    off = true; // storage.onChanged confirms it
    hide(true);
  }

  function closeMenu(focus) {
    if (!menuOpen) return;
    menuOpen = false;
    menuGen++;
    ui.btn.setAttribute("aria-expanded", "false");
    ui.panel.hidden = true;
    document.removeEventListener("pointerdown", outside, true);
    if (focus) ui.btn.focus();
  }

  // Clicks inside the closed shadow root reach document retargeted to the host.
  function outside(e) {
    if (e.isTrusted && e.target !== ui.host) closeMenu(false);
  }

  function onKey(e) {
    if (!e.isTrusted) return;
    e.stopPropagation(); // keep the player's own shortcuts (Space, arrows) out of our menu
    if (!menuOpen) return;
    const items = [...ui.menu.querySelectorAll('[role="menuitem"]')];
    const i = items.indexOf(ui.root.activeElement);
    const next = {
      ArrowDown: items[(i + 1) % items.length], ArrowUp: items[(i - 1 + items.length) % items.length],
      Home: items[0], End: items.at(-1),
    }[e.key];
    if (e.key === "Escape") closeMenu(true);
    else if (next) next.focus();
    else return;
    e.preventDefault();
  }

  async function send(msg) {
    try { return await chrome.runtime.sendMessage(msg); } catch { return null; } // extension reloaded
  }
})();
