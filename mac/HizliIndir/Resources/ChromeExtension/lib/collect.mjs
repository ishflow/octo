// Runs in the page (injected with chrome.scripting, so it must not touch anything outside its own body).
// Works in tabs opened before the extension was installed, unlike a content script.
export function collectLinks() {
  const seen = new Set();
  const links = [];
  for (const a of document.links) {
    const url = a.href.split("#")[0];
    if (!/^https?:/i.test(url) || seen.has(url)) continue;
    seen.add(url);
    links.push({ url, text: (a.textContent || a.getAttribute("download") || "").trim().slice(0, 200) });
  }
  return links;
}
