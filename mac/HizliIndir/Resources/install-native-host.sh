#!/usr/bin/env bash
# Kullanım: install-native-host.sh <octo-yolu> [eklenti-kimliği]
#           install-native-host.sh --uninstall
# Chromium tabanlı tarayıcılara native messaging manifestini yazar (yönetici hakkı gerekmez).
# Eski ad (com.uba.hizliindir) manifestleri kurulumda ve kaldırmada Çöp'e taşınır.
set -eu

HOST=com.uba.octo
OLD_HOST=com.uba.hizliindir
DEFAULT_ID=elhjlbdkhcpjihiapkeoeceopennmkab # extension/chrome/manifest.json "key" ile sabit
AS="$HOME/Library/Application Support"
BROWSERS=("Google/Chrome" "Google/Chrome Beta" "Google/Chrome Canary" "Chromium" "Microsoft Edge" "BraveSoftware/Brave-Browser" "Arc/User Data" "Vivaldi")

uninstall=0
if [ "${1:-}" = "--uninstall" ]; then uninstall=1
elif [ $# -lt 1 ] || [ $# -gt 2 ]; then
  echo "Kullanım: $0 <octo-yolu> [eklenti-kimliği] | --uninstall" >&2; exit 2
fi

if [ "$uninstall" = 0 ]; then
  bin="$1"; id="${2:-$DEFAULT_ID}"
  [ -f "$bin" ] || { echo "Hata: ikili dosya bulunamadı: $bin" >&2; exit 1; }
  [ -x "$bin" ] || { echo "Hata: ikili dosya çalıştırılabilir değil: $bin" >&2; exit 1; }
  bin="$(cd "$(dirname "$bin")" && pwd -P)/$(basename "$bin")"
  # JSON string kaçışı: \ ve "
  esc_bin="$(printf '%s' "$bin" | sed 's/\\/\\\\/g; s/"/\\"/g')"
  esc_id="$(printf '%s' "$id" | sed 's/\\/\\\\/g; s/"/\\"/g')"
fi

# Çöpe taşı (silme yok): ~/.Trash/<tarayıcı>-<ad>-<zaman>-<pid>
to_trash() {
  mkdir -p "$HOME/.Trash"
  local dest
  dest="$HOME/.Trash/$2-$(basename "$1")-$(date +%Y%m%d-%H%M%S)-$$"
  mv "$1" "$dest"
  echo "Çöpe taşındı: $dest"
}

found=0
for b in "${BROWSERS[@]}"; do
  [ -d "$AS/$b" ] || continue
  found=1
  dir="$AS/$b/NativeMessagingHosts"; file="$dir/$HOST.json"
  if [ -f "$dir/$OLD_HOST.json" ]; then to_trash "$dir/$OLD_HOST.json" "${b//\//-}"; fi
  if [ "$uninstall" = 1 ]; then
    if [ -f "$file" ]; then rm -f "$file"; echo "Kaldırıldı: $b"; fi
  else
    mkdir -p "$dir"
    printf '{\n  "name": "%s",\n  "description": "Octo",\n  "path": "%s",\n  "type": "stdio",\n  "allowed_origins": ["chrome-extension://%s/"]\n}\n' \
      "$HOST" "$esc_bin" "$esc_id" > "$file"
    echo "Kuruldu: $b"
  fi
done
[ "$found" = 1 ] || echo "Desteklenen tarayıcı bulunamadı"
