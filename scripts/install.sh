#!/usr/bin/env bash
# Octo kurulumu: uygulamayı kopyalar, CLI bağlantısını ve Chrome native-host manifestini kurar.
# Kullanım: scripts/install.sh [-y] | scripts/install.sh --uninstall [-y]
# Ortam (test ve özel kurulum için): INSTALL_APPS_DIR, BIN_DIR, APP_SRC, BUILD_APP_SCRIPT
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
APP_NAME=Octo.app
APP_SRC="${APP_SRC:-$ROOT/build/$APP_NAME}"
BUILD_APP_SCRIPT="${BUILD_APP_SCRIPT:-$ROOT/scripts/build-app.sh}"
yes=0
mode=install

say() { printf '%s\n' "$*"; }
warn() { printf 'Uyarı: %s\n' "$*" >&2; }
die() { printf 'Hata: %s\n' "$*" >&2; exit 1; }

for a in "$@"; do
  case "$a" in
    -y|--yes) yes=1 ;;
    --uninstall) mode=uninstall ;;
    -h|--help) say "Kullanım: $0 [-y] [--uninstall]"; exit 0 ;;
    *) say "Kullanım: $0 [-y] [--uninstall]" >&2; exit 2 ;;
  esac
done

confirm() {
  [ "$yes" = 1 ] && return 0
  [ -t 0 ] || die "onay gerekli ama terminal yok; -y ile çalıştırın"
  local r; read -r -p "$1 [e/H] " r
  case "$r" in e|E|evet) return 0 ;; *) return 1 ;; esac
}

# Çöpe taşı (silme yok): ~/.Trash/<ad>-<zaman>-<pid>
to_trash() {
  mkdir -p "$HOME/.Trash"
  local dest
  dest="$HOME/.Trash/$(basename "$1")-$(date +%Y%m%d-%H%M%S)-$$"
  mv "$1" "$dest"
  say "Çöpe taşındı: $dest"
}

# Sembolik bağ bize mi ait? (Octo.app içindeki motora işaret ediyor mu)
# Eski /opt/homebrew/bin/hizli-indir bağı bizim değil: ne kurulum ne kaldırma ona dokunur.
is_ours() {
  [ -L "$1" ] || return 1
  case "$(readlink "$1")" in */Octo.app/Contents/MacOS/octo) return 0 ;; esac
  return 1
}

# PATH'te olan ve yazılabilen ilk Homebrew bin dizini
pick_bin_dir() {
  if [ -n "${BIN_DIR:-}" ]; then echo "$BIN_DIR"; return 0; fi
  local d
  for d in /opt/homebrew/bin /usr/local/bin; do
    case ":$PATH:" in *":$d:"*) if [ -w "$d" ]; then echo "$d"; return 0; fi ;; esac
  done
  return 0
}

# Uygulamanın içindeki kurucu, yoksa depodaki
find_installer() {
  local p
  for p in "$1/Contents/Resources/install-native-host.sh" "$ROOT/scripts/install-native-host.sh"; do
    if [ -f "$p" ]; then echo "$p"; return 0; fi
  done
  return 0
}

# Eski Hızlı İndir servisi çalışıyorsa durdur: iki servis aynı .part dosyalarına yazmasın. kill yok.
stop_legacy() {
  local sock="$HOME/Library/Application Support/HizliIndir/engine.sock" r
  [ -e "$sock" ] || return 0
  if pgrep -x HizliIndir >/dev/null 2>&1; then
    say "Eski Hızlı İndir uygulaması kapatılıyor."
    osascript -e 'quit app "HizliIndir"' >/dev/null 2>&1 || warn "eski Hızlı İndir uygulaması kapatılamadı; elle kapatın"
  fi
  r="$(printf '{"id":1,"cmd":"shutdown"}\n' | nc -U -w 2 "$sock" 2>/dev/null)" || true
  case "$r" in
    '') # yanıt yok: soket bağlantı kabul ediyorsa servis takılı/meşgul, etmiyorsa çalışmıyor (bayat soket)
      if nc -U -w 1 "$sock" </dev/null >/dev/null 2>&1; then
        warn "eski Hızlı İndir servisi kapatma komutuna yanıt vermedi ama hâlâ çalışıyor. Octo'yu açmadan önce Etkinlik Monitörü'nden 'hizli-indir' sürecini sonlandırın; yoksa iki servis aynı yarım indirmelere yazabilir."
      fi ;;
    *'"ok":true'*) say "Eski Hızlı İndir servisi durduruldu." ;;
    *) warn "eski Hızlı İndir servisi hâlâ çalışıyor ve kapatma komutunu bilmiyor. Octo'yu açmadan önce Etkinlik Monitörü'nden 'hizli-indir' sürecini sonlandırın; yoksa iki servis aynı yarım indirmelere yazabilir." ;;
  esac
}

install_app() {
  if [ ! -d "$APP_SRC" ]; then
    [ -f "$BUILD_APP_SCRIPT" ] || die "$APP_NAME yok ve build-app.sh bulunamadı ($BUILD_APP_SCRIPT). Önce uygulamayı derleyin."
    say "Uygulama derleniyor: $BUILD_APP_SCRIPT"
    bash "$BUILD_APP_SCRIPT"
    [ -d "$APP_SRC" ] || die "derleme bitti ama $APP_SRC oluşmadı"
  fi

  local apps
  if [ -n "${INSTALL_APPS_DIR:-}" ]; then apps="$INSTALL_APPS_DIR"
  elif [ -w /Applications ]; then apps=/Applications
  else apps="$HOME/Applications"; fi
  local dest="$apps/$APP_NAME"

  [ -x "$APP_SRC/Contents/MacOS/octo" ] || die "kaynak pakette çalıştırılabilir motor yok: $APP_SRC/Contents/MacOS/octo (eski kurulum korundu)"

  confirm "$APP_NAME şuraya kurulsun: $apps ?" || die "iptal edildi"
  stop_legacy
  mkdir -p "$apps"
  # Önce yanına kopyala, doğrula; ancak sonra eskiyi Çöp'e taşıyıp yerine koy
  local tmp="$dest.new"
  if [ -e "$tmp" ] || [ -L "$tmp" ]; then to_trash "$tmp"; fi
  if command -v ditto >/dev/null 2>&1; then ditto "$APP_SRC" "$tmp" || { to_trash "$tmp"; die "kopyalama başarısız (eski kurulum korundu)"; }
  else cp -R "$APP_SRC" "$tmp" || { to_trash "$tmp"; die "kopyalama başarısız (eski kurulum korundu)"; }; fi
  if [ ! -x "$tmp/Contents/MacOS/octo" ]; then to_trash "$tmp"; die "kopya doğrulanamadı (eski kurulum korundu)"; fi
  if [ -e "$dest" ] || [ -L "$dest" ]; then to_trash "$dest"; fi
  mv "$tmp" "$dest"
  local bin="$dest/Contents/MacOS/octo"
  say "Uygulama kuruldu: $dest"

  local bdir link
  bdir="$(pick_bin_dir)"
  if [ -z "$bdir" ]; then
    warn "PATH'te yazılabilir /opt/homebrew/bin veya /usr/local/bin yok; CLI bağlantısı atlandı. Elle: ln -s '$bin' <PATH'teki dizin>/octo"
  else
    mkdir -p "$bdir"
    link="$bdir/octo"
    if [ ! -e "$link" ] && [ ! -L "$link" ]; then ln -s "$bin" "$link"; say "CLI bağlantısı: $link"
    elif is_ours "$link"; then ln -sfn "$bin" "$link"; say "CLI bağlantısı yenilendi: $link"
    else warn "$link zaten var ve Octo'ya ait değil; dokunulmadı"; fi
  fi

  local inst
  inst="$(find_installer "$dest")"
  if [ -n "$inst" ]; then bash "$inst" "$bin"
  else warn "install-native-host.sh bulunamadı; Chrome bağlantısı kurulmadı"; fi

  cat <<EOM

Kurulum bitti. Sonraki adımlar:
  1. Uygulamayı açın: open -a Octo
  2. Chrome eklentisi: chrome://extensions -> Geliştirici modu -> Paketlenmemiş öğe yükle
     -> $dest/Contents/Resources/ChromeExtension
  3. Komut satırı: octo URL
EOM
}

uninstall_app() {
  local cands=() c
  if [ -n "${INSTALL_APPS_DIR:-}" ]; then cands=("$INSTALL_APPS_DIR")
  else cands=(/Applications "$HOME/Applications"); fi

  local found=() app_for_installer=""
  for c in "${cands[@]}"; do
    if [ -d "$c/$APP_NAME" ]; then found+=("$c/$APP_NAME"); app_for_installer="$c/$APP_NAME"; fi
  done

  confirm "Octo kaldırılsın (uygulama Çöp'e taşınır)?" || die "iptal edildi"

  # Manifestleri uygulamayı taşımadan önce kaldır (kurucu uygulamanın içinde olabilir)
  local inst
  inst="$(find_installer "$app_for_installer")"
  if [ -n "$inst" ]; then bash "$inst" --uninstall; else warn "install-native-host.sh bulunamadı; Chrome manifestleri kalmış olabilir"; fi

  local d link
  if [ -n "${BIN_DIR:-}" ]; then d=("$BIN_DIR"); else d=(/opt/homebrew/bin /usr/local/bin); fi
  for c in "${d[@]}"; do
    link="$c/octo"
    if is_ours "$link"; then rm -f "$link"; say "CLI bağlantısı kaldırıldı: $link"
    elif [ -e "$link" ] || [ -L "$link" ]; then warn "$link Octo'ya ait görünmüyor; dokunulmadı"; fi
  done

  for c in ${found[@]+"${found[@]}"}; do to_trash "$c"; done
  [ ${#found[@]} -gt 0 ] || say "Kurulu uygulama bulunamadı."
  say "Veri klasörü silinmedi: ~/Library/Application Support/Octo (isterseniz elle silin)."
}

if [ "$mode" = uninstall ]; then uninstall_app; else install_app; fi
