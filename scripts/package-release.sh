#!/usr/bin/env bash
# Yayın paketleri: build-app.sh sonrası dist/ altına Octo.app zip'i, Chrome eklentisi zip'i ve SHA256SUMS yazar,
# sonra dist/'i release-scan.sh ile tarar (bulgu varsa çıkış 1).
#   package-release.sh [--no-build]
# Ortam (test/özel kullanım): BUILD_CMD (varsayılan scripts/build-app.sh), APP_SRC (varsayılan build/Octo.app),
# EXT_SRC (varsayılan extension/chrome), DIST_DIR (varsayılan dist).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
BUILD_CMD="${BUILD_CMD:-\"$ROOT/scripts/build-app.sh\"}"
APP="${APP_SRC:-$ROOT/build/Octo.app}"
EXT="${EXT_SRC:-$ROOT/extension/chrome}"
DIST="${DIST_DIR:-$ROOT/dist}"

[ "${1:-}" = "--no-build" ] || bash -c "$BUILD_CMD"
[ -d "$APP" ] || { echo "Hata: $APP yok (önce scripts/build-app.sh)" >&2; exit 1; }
[ -f "$EXT/manifest.json" ] || { echo "Hata: $EXT/manifest.json yok" >&2; exit 1; }

app_ver="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$APP/Contents/Info.plist")"
ext_ver="$(python3 -I -c 'import json,sys;print(json.load(open(sys.argv[1]))["version"])' "$EXT/manifest.json")"
[ -n "$app_ver" ] && [ -n "$ext_ver" ] || { echo "Hata: sürüm okunamadı" >&2; exit 1; }

mkdir -p "$DIST"
DIST="$(cd "$DIST" && pwd -P)"
app_zip="Octo-$app_ver-macos.zip"
ext_zip="Octo-Chrome-Extension-$ext_ver.zip"
# Aynı adlı eski çıktı varsa önce silinir; zip var olan arşive eklemesin.
rm -f "$DIST/$app_zip" "$DIST/$ext_zip"
ditto -c -k --keepParent --norsrc "$APP" "$DIST/$app_zip"

# Eklenti: Chrome "Paketlenmemiş öğe yükle" klasör ister; zip açılınca tek klasör çıksın. Test ve .DS_Store paket dışı.
stage="$(mktemp -d)"
trap 'rm -rf "$stage"' EXIT
mkdir "$stage/Octo-Chrome-Extension"
rsync -a --exclude test --exclude .DS_Store "$EXT/" "$stage/Octo-Chrome-Extension/"
(cd "$stage" && zip -qr -X "$DIST/$ext_zip" Octo-Chrome-Extension)

(cd "$DIST" && shasum -a 256 "$app_zip" "$ext_zip" > SHA256SUMS)
# Yayın öncesi son kapı: zip'ler açılıp sır ve yerel iz (ikilideki yollar dahil) taranır.
"$ROOT/scripts/release-scan.sh" "$DIST" || { echo "Hata: $DIST taramasında bulgu var (yukarıda); yayınlama" >&2; exit 1; }
echo "Tamam: $DIST"
cat "$DIST/SHA256SUMS"
