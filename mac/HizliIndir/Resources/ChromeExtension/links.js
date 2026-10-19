// "All links" picker: only the links the user leaves checked go to Octo (main queue; started only if "Hemen başlat" is ticked).
import { preselect, linkExt, mergeExcluded, DEFAULT_EXTS } from "./lib/decide.mjs";

const $ = (id) => document.getElementById(id);
const key = location.hash.slice(1);
const { [key]: data } = key ? await chrome.storage.session.get(key) : {};
const cfg = await chrome.storage.local.get({ captureExts: DEFAULT_EXTS, excludedSites: [], localExcludedSites: [] });
const links = preselect(data?.links, cfg.captureExts, mergeExcluded(cfg.excludedSites, cfg.localExcludedSites));

$("page").textContent = data?.pageUrl || "";
const rows = [];
const boxes = links.map((l) => {
  const box = Object.assign(document.createElement("input"), { type: "checkbox", checked: l.checked });
  const text = Object.assign(document.createElement("span"), { className: "text", textContent: l.text || l.url });
  const url = Object.assign(document.createElement("span"), { className: "url", textContent: l.url });
  const label = document.createElement("label");
  const li = document.createElement("li");
  label.append(box, text, url);
  li.append(label);
  $("list").append(li);
  rows.push(li);
  return box;
});

const exts = links.map((l) => linkExt(l.url));
const visible = () => boxes.filter((_, i) => !rows[i].hidden);
const option = (value, textContent) => Object.assign(document.createElement("option"), { value, textContent });
$("ext").append(option("", "Uzantıya göre seç…"));
for (const e of new Set(exts.filter(Boolean))) $("ext").append(option(e, `${e} (${exts.filter((x) => x === e).length})`));

function update() {
  const n = boxes.filter((b) => b.checked).length;
  const shown = rows.filter((r) => !r.hidden).length;
  $("count").textContent = `${links.length} bağlantıdan ${n} tanesi seçili` + (shown < links.length ? `, ${shown} gösteriliyor` : "");
  $("add").disabled = n === 0;
  $("add").textContent = n ? `${n} dosyayı Octo'ya ekle` : "Seçilenleri Octo'ya ekle";
}
$("list").addEventListener("change", update);
$("all").onclick = () => { visible().forEach((b) => (b.checked = true)); update(); };
$("close").onclick = () => window.close();
$("none").onclick = () => { visible().forEach((b) => (b.checked = false)); update(); };
$("filter").oninput = () => {
  const q = $("filter").value.trim().toLowerCase();
  links.forEach((l, i) => (rows[i].hidden = !!q && !`${l.text || ""}\n${l.url}`.toLowerCase().includes(q)));
  update();
};
$("ext").onchange = () => {
  const e = $("ext").value;
  if (e) boxes.forEach((b, i) => { if (!rows[i].hidden) b.checked = exts[i] === e; });
  $("ext").value = ""; // back to the prompt, so the same extension can be picked again
  update();
};
update();
if (!data) $("status").textContent = "Liste bulunamadı. Sayfada sağ tıklayıp \"Tüm bağlantıları Octo ile indir\" seçeneğini yeniden kullanın.";
else if (!links.length) $("status").textContent = "Bu sayfada indirilebilir bağlantı bulunamadı.";

$("add").onclick = async () => {
  const start = $("start").checked;
  const picked = links.filter((_, i) => boxes[i].checked).map(({ url, text }) => ({ url, text }));
  $("add").disabled = true;
  $("status").textContent = "Octo'ya gönderiliyor…";
  const r = await chrome.runtime.sendMessage({ type: "batch", links: picked, pageUrl: data.pageUrl, start }).catch(() => null);
  if (r?.accepted) {
    $("status").textContent = start
      ? `${picked.length} bağlantı Octo'nun ana kuyruğuna eklendi ve indirme başlatıldı.`
      : `${picked.length} bağlantı Octo'nun ana kuyruğuna eklendi. Başlatmak için Octo'yu açın.`;
    for (const b of [...boxes, $("all"), $("none"), $("filter"), $("ext"), $("start")]) b.disabled = true; // a second click would add them twice
    chrome.storage.session.remove(key).catch(() => {});
  } else {
    $("status").textContent = "Octo'ya eklenemedi. Octo'nun kurulu olduğundan emin olup tekrar deneyin.";
    $("add").disabled = false;
  }
};
