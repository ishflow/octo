#!/usr/bin/env bash
# install.sh'ı geçici HOME ve sahte /Applications ile sınar; gerçek sisteme dokunmaz.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
inst="$here/install.sh"
fail=0
ok()  { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fail=1; }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (beklenen '$2', gelen '$3')"; fi; }
exists() { if [ -e "$2" ] || [ -L "$2" ]; then ok "$1"; else bad "$1"; fi; }
absent() { if [ ! -e "$2" ] && [ ! -L "$2" ]; then ok "$1"; else bad "$1"; fi; }

T="$(cd "${TMPDIR:-/tmp}" && pwd -P)" || exit 1
H="$(mktemp -d)" || exit 1
H="$(cd "$H" && pwd -P)" || exit 1
case "$H" in "$T"/?*) [ -d "$H" ] || exit 1 ;; *) echo "mktemp dizini beklenmedik: '$H'" >&2; exit 1 ;; esac
cleanup() { case "$H" in "$T"/?*) [ -d "$H" ] && rm -rf "$H" ;; esac; }
trap cleanup EXIT
APPS="$H/Apps"; BINS="$H/bin"; SRC="$H/src/Octo.app"; LOG="$H/host.log"
mkdir -p "$BINS" "$SRC/Contents/MacOS" "$SRC/Contents/Resources"
printf '#!/bin/sh\necho fake\n' > "$SRC/Contents/MacOS/octo"; chmod +x "$SRC/Contents/MacOS/octo"
# Sahte native-host kurucusu: çağrı argümanlarını kaydeder
printf '#!/bin/sh\necho "$@" >> "%s"\n' "$LOG" > "$SRC/Contents/Resources/install-native-host.sh"

# Sahte pgrep/osascript/nc: gerçek uygulamaya ve soketlere dokunulmasın; çağrılar kaydedilir
FAKE="$H/fakebin"; CALLS="$H/calls.log"; mkdir -p "$FAKE"
printf '#!/bin/sh\necho "pgrep $*" >> "%s"\n[ -e "%s" ]\n' "$CALLS" "$H/app.running" > "$FAKE/pgrep"
printf '#!/bin/sh\necho "osascript $*" >> "%s"\n' "$CALLS" > "$FAKE/osascript"
# nc -U -w 1 <soket>: yalnız bağlantı denemesi; nc.alive varsa soket dinliyor sayılır
printf '#!/bin/sh\necho "nc $*" >> "%s"\nif [ "$3" = 1 ]; then [ -e "%s" ]; exit; fi\ncat >> "%s"\ncat "%s" 2>/dev/null\n' "$CALLS" "$H/nc.alive" "$CALLS" "$H/nc.reply" > "$FAKE/nc"
chmod +x "$FAKE"/*

run() { PATH="$FAKE:$PATH" HOME="$H" INSTALL_APPS_DIR="$APPS" BIN_DIR="$BINS" APP_SRC="$SRC" BUILD_APP_SCRIPT="$H/yok.sh" "$inst" "$@"; }
LINK="$BINS/octo"; BIN="$APPS/Octo.app/Contents/MacOS/octo"
# Eski CLI bağı (Hızlı İndir): kurulum da kaldırma da ona dokunmaz
OLDLINK="$BINS/hizli-indir"; ln -s "$H/Developer/hizli-indir/hizli-indir" "$OLDLINK"

# 1. onay yok (stdin terminal değil) -> iptal, kurulum yok
run </dev/null >/dev/null 2>&1; rc=$?
check "-y yoksa ve terminal yoksa çıkış 1" 1 "$rc"
absent "iptalde uygulama kurulmadı" "$APPS/Octo.app"

# 2. kurulum
out="$(run -y 2>&1)"; rc=$?
check "kurulum çıkış 0" 0 "$rc"
exists "uygulama kopyalandı" "$BIN"
check "CLI bağı kuruldu" "$BIN" "$(readlink "$LINK")"
check "kurucu kurulu ikiliyle çağrıldı" "$BIN" "$(head -n1 "$LOG")"
case "$out" in *"Sonraki adımlar"*ChromeExtension*) ok "sonraki adımlar yazıldı" ;; *) bad "sonraki adımlar yazılmadı" ;; esac

# 3. yeniden kurulum: eski sürüm Çöp'e gider, bağ yenilenir
run -y >/dev/null 2>&1; rc=$?
check "yeniden kurulum çıkış 0" 0 "$rc"
check "eski uygulama Çöp'te" 1 "$(find "$H/.Trash" -maxdepth 1 -name 'Octo.app-*' | wc -l | tr -d ' ')"
exists "yeni uygulama yerinde" "$BIN"

# 4. yabancı bağ/dosya korunur
rm "$LINK"; ln -s /usr/bin/true "$LINK"
run -y >/dev/null 2>&1
check "yabancı bağ ezilmedi" /usr/bin/true "$(readlink "$LINK")"
rm "$LINK"; echo x > "$LINK"
run -y >/dev/null 2>&1
check "yabancı dosya ezilmedi" x "$(cat "$LINK")"
run --uninstall -y >/dev/null 2>&1
exists "kaldırmada yabancı dosya korundu" "$LINK"
rm "$LINK"

# 5. kaldırma
run -y >/dev/null 2>&1
run --uninstall -y >/dev/null 2>&1; rc=$?
check "kaldırma çıkış 0" 0 "$rc"
absent "uygulama kaldırıldı" "$APPS/Octo.app"
absent "CLI bağı kaldırıldı" "$LINK"
check "kurucu --uninstall ile çağrıldı" "--uninstall" "$(tail -n1 "$LOG")"
check "uygulamalar Çöp'e taşındı (silinmedi)" 5 "$(find "$H/.Trash" -maxdepth 1 -name 'Octo.app-*' | wc -l | tr -d ' ')"

# 5b. bozuk kaynak: eski kurulum yerinde kalmalı, Çöp'e gitmemeli
run -y >/dev/null 2>&1
before="$(find "$H/.Trash" -maxdepth 1 | wc -l | tr -d ' ')"
BAD="$H/bad/Octo.app"; mkdir -p "$BAD/Contents/MacOS"   # motor yok
HOME="$H" INSTALL_APPS_DIR="$APPS" BIN_DIR="$BINS" APP_SRC="$BAD" BUILD_APP_SCRIPT="$H/yok.sh" "$inst" -y >/dev/null 2>&1; rc=$?
check "bozuk kaynakta çıkış 1" 1 "$rc"
exists "bozuk kaynakta eski uygulama yerinde" "$BIN"
check "bozuk kaynakta Çöp değişmedi" "$before" "$(find "$H/.Trash" -maxdepth 1 | wc -l | tr -d ' ')"
absent "bozuk kaynakta .new artığı yok" "$APPS/Octo.app.new"
run --uninstall -y >/dev/null 2>&1

check "eski hizli-indir bağına dokunulmadı" "$H/Developer/hizli-indir/hizli-indir" "$(readlink "$OLDLINK")"

# 6a. eski Hızlı İndir servisi: soket yoksa hiçbir şeye dokunulmaz
absent "eski soket yokken eski servise dokunulmadı" "$CALLS"
OLDSOCK="$H/Library/Application Support/HizliIndir/engine.sock"
mkdir -p "$(dirname "$OLDSOCK")"; : > "$OLDSOCK"; : > "$H/app.running"
printf '{"id":1,"ok":true}\n' > "$H/nc.reply"
out="$(run -y 2>&1)"; rc=$?
check "eski servis varken kurulum çıkış 0" 0 "$rc"
case "$(cat "$CALLS" 2>/dev/null)" in *'osascript -e quit app "HizliIndir"'*) ok "eski uygulama kapatıldı" ;; *) bad "eski uygulama kapatılmadı" ;; esac
case "$(cat "$CALLS" 2>/dev/null)" in *"nc -U -w 2 $OLDSOCK"*'"cmd":"shutdown"'*) ok "eski motora shutdown gitti" ;; *) bad "eski motora shutdown gitmedi" ;; esac
case "$out" in *"Eski Hızlı İndir servisi durduruldu"*) ok "durduruldu yazıldı" ;; *) bad "durduruldu yazılmadı: $out" ;; esac
# shutdown bilmeyen eski motor: kill yok, açık uyarı; uygulama çalışmıyorsa osascript çağrılmaz
rm -f "$H/app.running" "$CALLS"
printf '{"id":1,"ok":false,"error":"Bilinmeyen komut: shutdown"}\n' > "$H/nc.reply"
out="$(run -y 2>&1)"; rc=$?
check "eski motor shutdown bilmiyorsa yine çıkış 0" 0 "$rc"
case "$out" in *"Uyarı:"*"kapatma komutunu bilmiyor"*hizli-indir*) ok "eski motor için açık uyarı" ;; *) bad "uyarı yok: $out" ;; esac
case "$(cat "$CALLS" 2>/dev/null)" in *osascript*) bad "çalışmayan uygulamaya osascript gitti" ;; *) ok "çalışmayan uygulamaya osascript gitmedi" ;; esac
# yanıt vermeyen eski motor: soket bağlantı kabul ediyorsa açık uyarı, etmiyorsa (bayat soket) sessiz
rm -f "$H/nc.reply"; : > "$H/nc.alive"
out="$(run -y 2>&1)"; rc=$?
check "yanıtsız ama canlı eski motorda çıkış 0" 0 "$rc"
case "$out" in *"Uyarı:"*"yanıt vermedi"*hizli-indir*) ok "yanıtsız canlı eski motor için uyarı" ;; *) bad "uyarı yok: $out" ;; esac
rm -f "$H/nc.alive"
out="$(run -y 2>&1)"
case "$out" in *"Uyarı:"*) bad "bayat sokette gereksiz uyarı: $out" ;; *) ok "bayat sokette uyarı yok" ;; esac
rm "$OLDSOCK"
run --uninstall -y >/dev/null 2>&1

# 6. uygulama yok: build-app.sh yoksa Türkçe hata, varsa çağrılır
NOAPP="$H/yok/Octo.app"
out="$(HOME="$H" INSTALL_APPS_DIR="$APPS" BIN_DIR="$BINS" APP_SRC="$NOAPP" BUILD_APP_SCRIPT="$H/yok.sh" "$inst" -y 2>&1)"; rc=$?
check "build betiği yoksa çıkış 1" 1 "$rc"
case "$out" in *"build-app.sh bulunamadı"*) ok "hata mesajı açık" ;; *) bad "hata mesajı: $out" ;; esac
printf '#!/bin/sh\ncp -R "%s" "%s"\n' "$SRC" "$NOAPP" > "$H/build.sh"
mkdir -p "$H/yok"
HOME="$H" INSTALL_APPS_DIR="$APPS" BIN_DIR="$BINS" APP_SRC="$NOAPP" BUILD_APP_SCRIPT="$H/build.sh" "$inst" -y >/dev/null 2>&1; rc=$?
check "uygulama yoksa build çağrılıp kuruldu" 0 "$rc"
exists "build sonrası kuruldu" "$BIN"

# 7. uçtan uca (gerçek native-host kurucusu): kurulum -> kaldırma
E="$H/e2e"; EH="$E/home"; EAPPS="$E/Apps"; EBINS="$E/bin"; ESRC="$E/src/Octo.app"
mkdir -p "$EH" "$EBINS" "$ESRC/Contents/MacOS"
printf '#!/bin/sh\necho fake\n' > "$ESRC/Contents/MacOS/octo"; chmod +x "$ESRC/Contents/MacOS/octo"   # Resources yok: depodaki kurucu kullanılır
EAS="$EH/Library/Application Support"
CHR="$EAS/Google/Chrome/NativeMessagingHosts"; BRV="$EAS/BraveSoftware/Brave-Browser/NativeMessagingHosts"
mkdir -p "$CHR" "$BRV" "$EAS/Octo" "$EAS/HizliIndir"
echo '{"old":1}' > "$CHR/com.uba.hizliindir.json"
echo '{"x":1}' > "$CHR/baska.host.json"
echo '[]' > "$EAS/Octo/downloads.json"; echo '[]' > "$EAS/HizliIndir/downloads.json"
ln -s "$E/eski/hizli-indir" "$EBINS/hizli-indir"
erun() { PATH="$FAKE:$PATH" HOME="$EH" INSTALL_APPS_DIR="$EAPPS" BIN_DIR="$EBINS" APP_SRC="$ESRC" BUILD_APP_SCRIPT="$H/yok.sh" "$inst" "$@"; }
erun -y >/dev/null 2>&1; rc=$?
check "uçtan uca kurulum çıkış 0" 0 "$rc"
exists "uçtan uca: Chrome manifesti yazıldı" "$CHR/com.uba.octo.json"
exists "uçtan uca: Brave manifesti yazıldı" "$BRV/com.uba.octo.json"
absent "uçtan uca: eski ad manifesti kurulumda taşındı" "$CHR/com.uba.hizliindir.json"
echo '{"old":1}' > "$CHR/com.uba.hizliindir.json"   # kaldırmada da taşınmalı
erun --uninstall -y >/dev/null 2>&1; rc=$?
check "uçtan uca kaldırma çıkış 0" 0 "$rc"
absent "uçtan uca: Octo.app yerinde yok" "$EAPPS/Octo.app"
check "uçtan uca: Octo.app Çöp'te" 1 "$(find "$EH/.Trash" -maxdepth 1 -name 'Octo.app-*' | wc -l | tr -d ' ')"
absent "uçtan uca: octo CLI bağı kalktı" "$EBINS/octo"
absent "uçtan uca: Chrome com.uba.octo manifesti kalktı" "$CHR/com.uba.octo.json"
absent "uçtan uca: Brave com.uba.octo manifesti kalktı" "$BRV/com.uba.octo.json"
absent "uçtan uca: eski ad manifesti kalktı" "$CHR/com.uba.hizliindir.json"
check "uçtan uca: eski ad manifesti Çöp'te (kurulum + kaldırma)" 2 "$(find "$EH/.Trash" -maxdepth 1 -name '*com.uba.hizliindir.json*' | wc -l | tr -d ' ')"
exists "uçtan uca: başka host manifesti korundu" "$CHR/baska.host.json"
check "uçtan uca: hizli-indir bağına dokunulmadı" "$E/eski/hizli-indir" "$(readlink "$EBINS/hizli-indir")"
exists "uçtan uca: Octo veri klasörü korundu" "$EAS/Octo/downloads.json"
exists "uçtan uca: eski HizliIndir veri klasörü korundu" "$EAS/HizliIndir/downloads.json"
out="$(erun --uninstall -y 2>&1)"; rc=$?
check "uçtan uca: kurulu değilken kaldırma çıkış 0" 0 "$rc"
case "$out" in *"Kurulu uygulama bulunamadı"*"Veri klasörü silinmedi"*) ok "uçtan uca: kurulu değilken mesajlar" ;; *) bad "kurulu değilken mesajlar: $out" ;; esac

[ "$fail" = 0 ] && echo "TÜMÜ GEÇTİ" || echo "BAŞARISIZ"
exit "$fail"
