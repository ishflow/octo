#!/usr/bin/env bash
# release-export.sh, release-scan.sh ve package-release.sh'ı geçici dizinlerle sınar; gerçek depoya, build/ ya da dist/ dizinine dokunmaz.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
export_sh="$here/release-export.sh"
scan_sh="$here/release-scan.sh"
pack_sh="$here/package-release.sh"
fail=0
ok()  { echo "ok   - $1"; }
bad() { echo "FAIL - $1"; fail=1; }
check() { if [ "$2" = "$3" ]; then ok "$1"; else bad "$1 (beklenen '$2', gelen '$3')"; fi; }

T="$(cd "${TMPDIR:-/tmp}" && pwd -P)" || exit 1
W="$(mktemp -d)" || exit 1
W="$(cd "$W" && pwd -P)" || exit 1
case "$W" in "$T"/?*) [ -d "$W" ] || exit 1 ;; *) echo "mktemp dizini beklenmedik: '$W'" >&2; exit 1 ;; esac
cleanup() { case "$W" in "$T"/?*) [ -d "$W" ] && rm -rf "$W" ;; esac; }
trap cleanup EXIT

# --- sahte depo: temiz ilk commit, kirli ikinci commit
# tarayıcının bu dosyayı bulmaması için desenler parçalı yazılır
U="/Users/u""ba"; BR="UBA-""BRAIN"; CO=".co""ord"; SECRET="AKIA""ABCDEFGHIJKLMNOP"
R="$W/repo"
mkdir -p "$R/docs/re" "$R/docs/superpowers/specs" "$R/.superpowers/brainstorm" "$R/docs/test-results/bench" "$R/docs/test-results/chrome" "$R/src"
echo "# Octo" > "$R/README.md"
echo "sınırlar" > "$R/docs/BILINEN-SINIRLAR.md"
echo "dosya $U/Developer/x" > "$R/docs/re/n.md"
echo "$BR notu" > "$R/docs/superpowers/specs/s.md"
echo "x" > "$R/.superpowers/brainstorm/b.html"
echo "$U/Downloads" > "$R/docs/test-results/a.log"
echo "x" > "$R/docs/test-results/bench/r.md"
echo "$U/Downloads" > "$R/docs/test-results/chrome/smoke.log"
printf 'png' > "$R/docs/test-results/chrome/s.png"
echo "package main" > "$R/src/a.go"
G=(env GIT_AUTHOR_NAME=Sahte GIT_AUTHOR_EMAIL=sahte@example.invalid GIT_COMMITTER_NAME=Sahte GIT_COMMITTER_EMAIL=sahte@example.invalid git -C "$R")
"${G[@]}" init -q -b main && "${G[@]}" add -A && "${G[@]}" commit -q -m "temiz" || { echo "fixture kurulamadı" >&2; exit 1; }
CLEAN="$("${G[@]}" rev-parse HEAD)"
{ echo "key = $SECRET"; echo "yol $U/Developer/hizli-indir"; echo "mail ben@gmail""x.com"; echo "ek $CO/hizli-indir"; } > "$R/src/leak.txt"
"${G[@]}" add -A && "${G[@]}" commit -q -m "kirli"
DIRTY="$("${G[@]}" rev-parse HEAD)"
echo "izlenmeyen" > "$R/untracked.txt"

# --- release-export
D="$W/out-clean"
RELEASE_REPO="$R" "$export_sh" "$CLEAN" "$D" >"$W/exp.out" 2>&1; rc=$?
check "temiz rev: çıkış 0" 0 "$rc"
[ -f "$D/README.md" ] && [ -f "$D/src/a.go" ] && [ -f "$D/docs/BILINEN-SINIRLAR.md" ] && ok "dosyalar çıktı" || bad "dosyalar eksik"
[ -f "$D/docs/test-results/chrome/s.png" ] && ok "chrome ekran görüntüsü kaldı" || bad "chrome ekran görüntüsü yok"
n=0; for p in docs/re docs/superpowers .superpowers docs/test-results/a.log docs/test-results/bench docs/test-results/chrome/smoke.log; do [ -e "$D/$p" ] && { n=$((n+1)); echo "  kaldı: $p"; }; done
check "iç notlar hariç" 0 "$n"
[ ! -e "$D/untracked.txt" ] && ok "izlenmeyen dosya alınmadı" || bad "izlenmeyen dosya alındı"
check "tek commit" 1 "$(git -C "$D" rev-list --count HEAD)"
check "uzak depo yok" "" "$(git -C "$D" remote)"
check "yazar" "Octo contributors" "$(git -C "$D" log -1 --format=%an)"
check "yazar e-postası" "octo-contributors@users.noreply.github.com" "$(git -C "$D" log -1 --format=%ae)"
check "çalışma ağacı temiz" "" "$(git -C "$D" status --porcelain)"
[ -f "$D.tarama.txt" ] && ok "tarama raporu yazıldı" || bad "tarama raporu yok"

RELEASE_REPO="$R" "$export_sh" "$CLEAN" "$D" >/dev/null 2>"$W/err"; rc=$?
[ "$rc" -ne 0 ] && ok "dolu hedef reddedildi" || bad "dolu hedef kabul edildi"
RELEASE_REPO="$R" "$export_sh" nope-rev "$W/out-bad" >/dev/null 2>"$W/err"; rc=$?
[ "$rc" -ne 0 ] && [ ! -e "$W/out-bad" ] && ok "geçersiz rev reddedildi" || bad "geçersiz rev kabul edildi"
"$export_sh" >/dev/null 2>&1; rc=$?
[ "$rc" -ne 0 ] && ok "argümansız kullanım hata" || bad "argümansız kullanım hata vermedi"

E="$W/out-dirty"
RELEASE_REPO="$R" "$export_sh" "$DIRTY" "$E" >"$W/exp2.out" 2>&1; rc=$?
check "kirli rev: çıkış 1 (bulgu var)" 1 "$rc"
[ -d "$E/.git" ] && ok "bulguda da depo kuruldu" || bad "bulguda depo kurulmadı"
grep -q 'src/leak.txt' "$E.tarama.txt" && ok "rapor dosya adını veriyor" || bad "rapor src/leak.txt'yi anmıyor"
! grep -q "$SECRET" "$E.tarama.txt" "$W/exp2.out" && ok "sır değeri rapora yazılmadı" || bad "sır değeri rapora sızdı"

# --- release-scan, iki kipte
for mode in grep gitleaks; do
  export RELEASE_SCAN_NO_GITLEAKS=$([ "$mode" = grep ] && echo 1 || echo "")
  [ "$mode" = gitleaks ] && ! command -v gitleaks >/dev/null && { ok "gitleaks yok, atlandı"; continue; }
  "$scan_sh" "$D" >"$W/s1" 2>&1; rc=$?
  check "[$mode] temiz ağaç çıkış 0" 0 "$rc"
  "$scan_sh" "$E" >"$W/s2" 2>&1; rc=$?
  check "[$mode] kirli ağaç çıkış 1" 1 "$rc"
  for t in 'leak.txt:1:' 'leak.txt:2:' 'leak.txt:3:' 'leak.txt:4:'; do grep -q "$t" "$W/s2" && ok "[$mode] $t" || bad "[$mode] $t bulunamadı"; done
  ! grep -q "$SECRET" "$W/s2" && ok "[$mode] değer yazılmadı" || bad "[$mode] değer sızdı"
done
unset RELEASE_SCAN_NO_GITLEAKS

# --- package-release
APP="$W/build/Octo.app"
mkdir -p "$APP/Contents/MacOS"
printf '#!/bin/sh\n' > "$APP/Contents/MacOS/Octo"; chmod +x "$APP/Contents/MacOS/Octo"
/usr/libexec/PlistBuddy -c 'Add :CFBundleShortVersionString string 9.8' "$APP/Contents/Info.plist" >/dev/null 2>&1 \
  || { printf '<?xml version="1.0"?><plist version="1.0"><dict><key>CFBundleShortVersionString</key><string>9.8</string></dict></plist>' > "$APP/Contents/Info.plist"; }
X="$W/ext"
mkdir -p "$X/test" "$X/icons"
echo '{"manifest_version":3,"name":"Octo","version":"0.7.1"}' > "$X/manifest.json"
echo "x" > "$X/background.js"; echo "t" > "$X/test/a.test.mjs"; echo "d" > "$X/.DS_Store"
DIST="$W/dist"
export APP_SRC="$APP" EXT_SRC="$X" DIST_DIR="$DIST"
BUILD_CMD="touch $W/build-ran" "$pack_sh" >"$W/p.out" 2>&1; rc=$?
check "paketleme çıkış 0" 0 "$rc"
[ -f "$W/build-ran" ] && ok "önce derleme çalıştı" || bad "derleme çalışmadı"
[ -f "$DIST/Octo-9.8-macos.zip" ] && ok "uygulama zip'i" || bad "uygulama zip'i yok"
[ -f "$DIST/Octo-Chrome-Extension-0.7.1.zip" ] && ok "eklenti zip'i" || bad "eklenti zip'i yok"
(cd "$DIST" && shasum -a 256 -c SHA256SUMS >/dev/null 2>&1) && ok "SHA256SUMS doğrulanıyor" || bad "SHA256SUMS tutmuyor"
check "SHA256SUMS yalnız 2 satır" 2 "$(wc -l < "$DIST/SHA256SUMS" | tr -d ' ')"
unzip -l "$DIST/Octo-9.8-macos.zip" | grep -q 'Octo.app/Contents/Info.plist' && ok "zip içinde Octo.app" || bad "zip içinde Octo.app yok"
ez="$(unzip -Z1 "$DIST/Octo-Chrome-Extension-0.7.1.zip")"
printf '%s\n' "$ez" | grep -qx 'Octo-Chrome-Extension/manifest.json' && ok "eklenti manifest'i klasör içinde" || bad "eklenti manifest'i yok"
! printf '%s\n' "$ez" | grep -Eq '/test/|DS_Store' && ok "eklentide test ve DS_Store yok" || bad "eklentide test/DS_Store var"
(cd "$W" && "$pack_sh" --no-build >/dev/null 2>&1); rc=$?
check "--no-build çıkış 0" 0 "$rc"
APP_SRC="$W/yok.app" BUILD_CMD=true "$pack_sh" >/dev/null 2>"$W/err"; rc=$?
[ "$rc" -ne 0 ] && ok "uygulama yoksa hata" || bad "uygulama yokken hata vermedi"
BUILD_CMD=false "$pack_sh" >/dev/null 2>&1; rc=$?
[ "$rc" -ne 0 ] && ok "derleme başarısızsa hata" || bad "derleme başarısızken hata vermedi"

# --- ikili ve zip taraması: yerel yol gömülü sahte ikili
B="$W/bin"
mkdir -p "$B/Octo.app/Contents/MacOS" "$W/zd"
printf '\0\1\2%s/Developer/hizli-indir/engine\0\3' "$U" > "$B/Octo.app/Contents/MacOS/octo"
"$scan_sh" "$B" >"$W/s3" 2>&1; rc=$?
check "ikili: çıkış 1" 1 "$rc"
grep -qx 'Octo.app/Contents/MacOS/octo:ikili: yerel kullanıcı yolu' "$W/s3" && ok "ikilide yerel yol bulundu" || bad "ikilideki yerel yol bulunamadı"
(cd "$B" && zip -qr -X "$W/zd/a.zip" Octo.app)
"$scan_sh" "$W/zd" >"$W/s4" 2>&1; rc=$?
check "zip: çıkış 1" 1 "$rc"
grep -qx 'a.zip!Octo.app/Contents/MacOS/octo:ikili: yerel kullanıcı yolu' "$W/s4" && ok "zip içindeki ikili bulundu" || bad "zip içindeki ikili bulunamadı"
! grep -q "$U" "$W/s3" "$W/s4" && ok "ikili: değer yazılmadı" || bad "ikili: değer sızdı"
cp "$B/Octo.app/Contents/MacOS/octo" "$APP/Contents/MacOS/octo-engine"
DIST_DIR="$W/dist2" BUILD_CMD=true "$pack_sh" >"$W/p2.out" 2>&1; rc=$?
check "kirli ikiliyle paketleme çıkış 1" 1 "$rc"
grep -q 'Octo-9.8-macos.zip!Octo.app/Contents/MacOS/octo-engine:ikili: yerel kullanıcı yolu' "$W/p2.out" && ok "paketleme bulguyu yazdı" || bad "paketleme bulguyu yazmadı"
! grep -q '^Tamam' "$W/p2.out" && ok "bulguda Tamam yazılmadı" || bad "bulguda Tamam yazıldı"

[ "$fail" = 0 ] && echo "TÜMÜ GEÇTİ" || { echo "BAŞARISIZ"; exit 1; }
