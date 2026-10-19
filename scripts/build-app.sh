#!/bin/bash
# Builds build/Octo.app: Go engine (universal) + SwiftUI app, ad-hoc signed.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="$ROOT/build"
mkdir -p "$OUT/go"

cd "$ROOT"
# cgo: the engine reads Keychain via Security.framework so the item ACL sees octo, not /usr/bin/security.
# -trimpath: no local source paths (user name, folders) in the released binary.
for arch in arm64 amd64; do
  CGO_ENABLED=1 GOOS=darwin GOARCH=$arch go build -trimpath -o "$OUT/go/octo-$arch" .
done
lipo -create -output "$OUT/go/octo" "$OUT/go/octo-arm64" "$OUT/go/octo-amd64"

# Bundled copies of the extension and host installer always come from their sources.
cp "$ROOT/scripts/install-native-host.sh" "$ROOT/mac/HizliIndir/Resources/"
# --delete: files removed from the source must not linger in the bundled copy.
rsync -a --delete --exclude test --exclude .DS_Store "$ROOT/extension/chrome/" "$ROOT/mac/HizliIndir/Resources/ChromeExtension/"

cd "$ROOT/mac"
xcodegen generate
xcodebuild -project Octo.xcodeproj -scheme Octo -configuration Release \
  -destination 'generic/platform=macOS' -derivedDataPath "$OUT/DerivedData" \
  ARCHS="arm64 x86_64" ONLY_ACTIVE_ARCH=NO CODE_SIGNING_ALLOWED=NO build

APP="$OUT/Octo.app"
rm -rf "$APP"
cp -R "$OUT/DerivedData/Build/Products/Release/Octo.app" "$APP"
# Case-insensitive APFS: the engine must not replace the app executable.
[ ! -e "$APP/Contents/MacOS/octo" ] || { echo "Hata: Contents/MacOS/octo zaten var (uygulamanın çalıştırılabilir dosyasıyla çakışıyor)" >&2; exit 1; }
# Turkish system menus/panels need tr as development region, a declared tr localization and the tr.lproj resource.
[ -f "$APP/Contents/Resources/tr.lproj/InfoPlist.strings" ] || { echo "Hata: pakette tr.lproj/InfoPlist.strings yok" >&2; exit 1; }
[ "$(/usr/libexec/PlistBuddy -c 'Print :CFBundleDevelopmentRegion' "$APP/Contents/Info.plist")" = "tr" ] || { echo "Hata: CFBundleDevelopmentRegion tr değil" >&2; exit 1; }
/usr/libexec/PlistBuddy -c 'Print :CFBundleLocalizations' "$APP/Contents/Info.plist" | grep -qx '    tr' || { echo "Hata: CFBundleLocalizations içinde tr yok" >&2; exit 1; }
cp "$OUT/go/octo" "$APP/Contents/MacOS/octo"
codesign --force --deep -s - "$APP"
codesign --verify --deep --strict "$APP"
echo "Tamam: $APP"
