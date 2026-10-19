import { toggleSite } from "./lib/decide.mjs";
import { collectLinks } from "./lib/collect.mjs";

const $ = (id) => document.getElementById(id);

chrome.storage.local.get({ enabled: true }).then(({ enabled }) => { $("enabled").checked = enabled; });
$("enabled").addEventListener("change", (e) => chrome.storage.local.set({ enabled: e.target.checked }));

function showStatus(connected) {
  $("status").className = connected ? "ok" : "bad";
  $("status").textContent = connected ? "● Uygulamaya bağlı" : "● Uygulamaya bağlanılamadı";
  $("note").hidden = connected;
}
chrome.runtime.sendMessage({ type: "status" }).then((s) => showStatus(!!s?.connected)).catch(() => showStatus(false));

// "<site> için kapat": a local exclusion for the active tab's host (the service's own list is never touched).
chrome.tabs.query({ active: true, currentWindow: true }).then(async ([tab]) => {
  let host = "";
  try { const u = new URL(tab?.url); if (/^https?:$/.test(u.protocol)) host = u.hostname; } catch { /* no page */ }
  if (!host) return;
  const { localExcludedSites } = await chrome.storage.local.get({ localExcludedSites: [] });
  $("siteName").textContent = `${host} için kapat`;
  $("site").checked = localExcludedSites.includes(host);
  $("siteRow").hidden = false;
  $("site").addEventListener("change", async (e) => {
    const { localExcludedSites: cur } = await chrome.storage.local.get({ localExcludedSites: [] });
    await chrome.storage.local.set({ localExcludedSites: toggleSite(cur, host, e.target.checked) });
  });
  // "Bu sitede video düğmesi": on = shown; separate from the capture list above.
  const { videoOffSites } = await chrome.storage.local.get({ videoOffSites: [] });
  $("video").checked = !videoOffSites.includes(host);
  $("videoRow").hidden = false;
  $("video").addEventListener("change", async (e) => {
    const { videoOffSites: cur } = await chrome.storage.local.get({ videoOffSites: [] });
    await chrome.storage.local.set({ videoOffSites: toggleSite(cur, host, !e.target.checked) });
  });
});

// Opens the same picker as the page context menu ("Tüm bağlantıları Octo ile indir").
$("links").addEventListener("click", async () => {
  $("links").disabled = true;
  const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
  let links = [];
  try {
    const [r] = await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: collectLinks });
    links = r?.result ?? [];
  } catch { /* restricted page: the list opens empty */ }
  const key = crypto.randomUUID();
  await chrome.storage.session.set({ [key]: { links, pageUrl: tab.url || "" } });
  await chrome.tabs.create({ url: `links.html#${key}`, index: tab.index + 1, openerTabId: tab.id });
  window.close();
});

// The host opens the app (`open -b`); hello never does, so this is a separate message.
$("open").addEventListener("click", () => {
  chrome.runtime.sendNativeMessage("com.uba.octo", { type: "open" }, () => {
    void chrome.runtime.lastError; // no host installed: nothing to open, the popup just closes
    window.close();
  });
});
