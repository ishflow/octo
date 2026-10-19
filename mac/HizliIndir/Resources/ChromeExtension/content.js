// Reports Option/Alt state to the worker (bypass).
let last = false;
function report(held) {
  if (held === last && !held) return;
  last = held;
  try { chrome.runtime.sendMessage({ type: "alt", held }).catch(() => {}); } catch { /* extension reloaded */ }
}
addEventListener("keydown", (e) => { if (e.key === "Alt") report(true); }, true);
addEventListener("keyup", (e) => { if (e.key === "Alt") report(false); }, true);
addEventListener("mousedown", (e) => report(e.altKey), true); // refreshes the worker's TTL on Option-click
addEventListener("blur", (e) => { if (e.target === window) report(false); }, true); // element blurs bubble in capture phase too
