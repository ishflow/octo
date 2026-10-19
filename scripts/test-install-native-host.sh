#!/usr/bin/env bash
# Kurulum betiğini geçici HOME ile sınar; gerçek ~/Library'ye dokunmaz.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
inst="$here/install-native-host.sh"
fail=0
ok()  { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fail=1; }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (beklenen '$2', gelen '$3')"; fi; }

T="$(cd "${TMPDIR:-/tmp}" && pwd -P)" || exit 1
H="$(mktemp -d)" || exit 1
H="$(cd "$H" && pwd -P)" || exit 1
case "$H" in "$T"/?*) [ -d "$H" ] || exit 1 ;; *) echo "mktemp dizini beklenmedik: '$H'" >&2; exit 1 ;; esac
cleanup() { case "$H" in "$T"/?*) [ -d "$H" ] && rm -rf "$H" ;; esac; }
trap cleanup EXIT
AS="$H/Library/Application Support"
BIN="$H/bin dir/octo"   # boşluklu yol
mkdir -p "$H/bin dir" "$AS/Google/Chrome" "$AS/Microsoft Edge" "$AS/Arc/User Data" "$AS/BraveSoftware/Brave-Browser"
printf '#!/bin/sh\n' > "$BIN"; chmod +x "$BIN"
M="NativeMessagingHosts/com.uba.octo.json"
OLD="NativeMessagingHosts/com.uba.hizliindir.json"

field() { python3 -I -c 'import json,sys;d=json.load(open(sys.argv[1]));print(eval(sys.argv[2]))' "$1" "$2"; }

out="$(HOME="$H" "$inst" "$BIN")"; rc=$?
check "çıkış kodu 0" 0 "$rc"
check "4 tarayıcı satırı" 4 "$(printf '%s\n' "$out" | grep -c .)"
for d in "Google/Chrome" "Microsoft Edge" "Arc/User Data" "BraveSoftware/Brave-Browser"; do
  f="$AS/$d/$M"
  [ -f "$f" ] && ok "$d yazıldı" || { bad "$d yazılmadı"; continue; }
  check "$d name" com.uba.octo "$(field "$f" "d['name']")"
  check "$d type" stdio "$(field "$f" "d['type']")"
  check "$d description" "Octo" "$(field "$f" "d['description']")"
  check "$d path" "$BIN" "$(field "$f" "d['path']")"
  check "$d origins" "['chrome-extension://elhjlbdkhcpjihiapkeoeceopennmkab/']" "$(field "$f" "d['allowed_origins']")"
done
[ ! -e "$AS/Chromium" ] && ok "olmayan tarayıcı oluşturulmadı" || bad "Chromium oluşturuldu"

# göreli yol mutlak olur, özel eklenti kimliği
(cd "$H/bin dir" && HOME="$H" "$inst" ./octo abcdefghijklmnopabcdefghijklmnop >/dev/null)
check "göreli yol mutlaklaştı" "$BIN" "$(field "$AS/Google/Chrome/$M" "d['path']")"
check "özel kimlik" "['chrome-extension://abcdefghijklmnopabcdefghijklmnop/']" "$(field "$AS/Google/Chrome/$M" "d['allowed_origins']")"

# hatalar
HOME="$H" "$inst" "$H/yok" >/dev/null 2>"$H/err"; rc=$?
[ "$rc" -ne 0 ] && grep -q "bulunamadı" "$H/err" && ok "olmayan ikili reddedildi" || bad "olmayan ikili reddedilmedi"
printf 'x' > "$H/nox"; HOME="$H" "$inst" "$H/nox" >/dev/null 2>&1; rc=$?
[ "$rc" -ne 0 ] && ok "çalıştırılamaz ikili reddedildi" || bad "çalıştırılamaz ikili kabul edildi"

# kaldırma
out="$(HOME="$H" "$inst" --uninstall)"; rc=$?
check "uninstall çıkış 0" 0 "$rc"
n=0; for d in "Google/Chrome" "Microsoft Edge" "Arc/User Data" "BraveSoftware/Brave-Browser"; do [ -e "$AS/$d/$M" ] && n=$((n+1)); done
check "uninstall dosyaları sildi" 0 "$n"
[ -d "$AS/Google/Chrome" ] && ok "tarayıcı klasörü korundu" || bad "tarayıcı klasörü silindi"

# eski (Hızlı İndir) manifestleri kurulumda ve kaldırmada Çöp'e taşınır, silinmez
old_count() { find "$H/.Trash" -maxdepth 1 -name '*com.uba.hizliindir.json*' 2>/dev/null | wc -l | tr -d ' '; }
for d in "Google/Chrome" "Microsoft Edge"; do mkdir -p "$AS/$d/NativeMessagingHosts"; echo '{}' > "$AS/$d/$OLD"; done
HOME="$H" "$inst" "$BIN" >/dev/null; rc=$?
check "eski manifestli kurulum çıkış 0" 0 "$rc"
n=0; for d in "Google/Chrome" "Microsoft Edge"; do [ -e "$AS/$d/$OLD" ] && n=$((n+1)); done
check "kurulumda eski manifest kalmadı" 0 "$n"
check "kurulumda eski manifestler Çöp'te" 2 "$(old_count)"
[ -f "$AS/Google/Chrome/$M" ] && ok "eski manifestin yanında yenisi yazıldı" || bad "yeni manifest yazılmadı"
echo '{}' > "$AS/Arc/User Data/NativeMessagingHosts/com.uba.hizliindir.json"
HOME="$H" "$inst" --uninstall >/dev/null; rc=$?
check "eski manifestli kaldırma çıkış 0" 0 "$rc"
absent_old="$( [ -e "$AS/Arc/User Data/$OLD" ] && echo var || echo yok )"
check "kaldırmada eski manifest kalmadı" yok "$absent_old"
check "kaldırmada eski manifest Çöp'te" 3 "$(old_count)"

# hiç tarayıcı yok
E="$(mktemp -d)"; out="$(HOME="$E" "$inst" "$BIN")"; rc=$?
check "tarayıcısız çıkış 0" 0 "$rc"
check "tarayıcısız mesaj" "Desteklenen tarayıcı bulunamadı" "$out"
rm -rf "$E"

[ "$fail" = 0 ] && echo "TÜMÜ GEÇTİ" || { echo "BAŞARISIZ"; exit 1; }
