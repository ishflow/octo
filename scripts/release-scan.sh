#!/usr/bin/env bash
# Yayın ağacında sır ve yerel iz taraması: release-scan.sh <klasör> (kaynak ağacı, build/Octo.app ya da dist/)
# Çıktı yalnız "dosya:satır: tür" satırlarıdır (ikilide "dosya:ikili: tür", zip içinde "arşiv.zip!dosya:…"),
# bulunan değer asla yazılmaz.
# Çıkış 0: bulgu yok, 1: bulgu var, 2: kullanım hatası.
# gitleaks varsa onu da koşar (RELEASE_SCAN_NO_GITLEAKS=1 ile kapatılır); grep desenleri her durumda koşar.
# Desenlerde [x] köşeli parantezi bu dosyanın kendi kendini bulmaması içindir.
set -u
dir="${1:-}"
[ -d "$dir" ] || { echo "Kullanım: $0 <klasör>" >&2; exit 2; }
self="$(cd "$(dirname "$0")" && pwd)/$(basename "$0")"
cd "$dir" || exit 2

found="$(mktemp)" || exit 2
trap 'rm -f "$found"' EXIT

scan() { # tür, desen, [izin verilen eşleşme deseni]
  local out
  out="$(grep -rInoE --exclude-dir=.git --exclude=.git -e "$2" . 2>/dev/null)" || return 0
  [ -n "${3:-}" ] && { out="$(printf '%s\n' "$out" | grep -vE "$3")" || return 0; }
  printf '%s\n' "$out" | awk -F: -v t="$1" '{ sub(/^\.\//, "", $1); print $1 ":" $2 ": " t }' >> "$found"
}

# sır desenleri (e-posta izin listesi: @2x.png dosya adları, eklenti testindeki me@a.org, noreply/example)
scan "AWS erişim anahtarı"      'AKI[A][0-9A-Z]{16}'
scan "GitHub belirteci"         'gh[pousr]_[A-Za-z0-9]{30,}|github_pa[t]_[A-Za-z0-9_]{20,}'
scan "API anahtarı (sk-/AIza)"  'sk-[A-Za-z0-9_-]{24,}|AIz[a][0-9A-Za-z_-]{35}'
scan "Slack belirteci"          'xox[baprs]-[A-Za-z0-9-]{10,}'
scan "özel anahtar bloğu"       '-----BEGIN [A-Z ]*PRIVATE KE[Y]-----'
scan "sabit parola/anahtar ataması" "(api[_-]?key|secret|token|passw(or)?d)[\"']?[[:space:]]*[:=][[:space:]]*[\"'][^\"']{12,}[\"']"
# yerel iz desenleri
# /Users/uba/x: MainWindowTests.swift'in sahte ev klasörü örneği
userpath='/Users/ub[a]/[A-Za-z0-9._-]*|/Users/ub[a]$'
inner='UBA-BRAI[N]|\.coor[d]([^A-Za-z0-9]|$)|Nextclou[d]|tailb76[0]|ishflo[w]'
scan "yerel kullanıcı yolu"     "$userpath" ':/Users/ub[a]/x$'
scan "iç klasör adı"            "$inner"
scan "ticari marka adı"        '\bID[M]\b|IDMa[n]|Internet Download Manage[r]'
scan "e-posta adresi"           '[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}' '@([0-9]x\.|a\.org$|[^:]*(noreply|example|\.invalid))'

# İkililer (grep -I onları atlar): strings çıktısında yerel iz (ör. -trimpath'siz Go ikilisindeki kaynak yolları).
while IFS= read -r -d '' f; do
  grep -qI . "$f" 2>/dev/null && continue # metin dosyası: yukarıda tarandı
  t="$(strings -a "$f" 2>/dev/null)"
  grep -qE "$userpath" <<<"$t" && echo "${f#./}:ikili: yerel kullanıcı yolu" >> "$found"
  grep -qE "$inner" <<<"$t" && echo "${f#./}:ikili: iç klasör adı" >> "$found"
done < <(find . -type f ! -path './.git/*' ! -name '*.zip' -print0)

# Yayın zip'leri: açılıp aynı taramadan geçer.
while IFS= read -r -d '' z; do
  x="$(mktemp -d)" || exit 2
  if unzip -qq "$z" -d "$x" 2>/dev/null; then
    "$self" "$x" 2>/dev/null | grep -v '^Bulgu yok\.$' | sed "s|^|${z#./}!|" >> "$found"
  else
    echo "${z#./}:0: zip açılamadı" >> "$found"
  fi
  rm -rf "$x"
done < <(find . -type f -name '*.zip' ! -path './.git/*' -print0)

if [ -z "${RELEASE_SCAN_NO_GITLEAKS:-}" ] && command -v gitleaks >/dev/null; then
  rep="$(mktemp)"
  # shortcut: gitleaks 8.19+ "dir" alt komutu varsayılır, eski sürümde atlanır; ihtiyaç olursa "detect --no-git" ekle
  if gitleaks dir . --no-banner --redact=100 --exit-code 0 -f json -r "$rep" >/dev/null 2>&1 && [ -s "$rep" ]; then
    python3 -I -c '
import json, os, sys
for f in json.load(open(sys.argv[1])) or []:
    p = os.path.relpath(f["File"])
    # manifest "key" alanı herkese açık eklenti anahtarıdır (eklenti kimliğini sabitler), sır değil
    if os.path.basename(p) in ("manifest.json", "manifest-pre-scripting.json") and f["RuleID"] == "generic-api-key":
        continue
    print("%s:%s: gitleaks (%s)" % (p, f["StartLine"], f["RuleID"]))
' "$rep" >> "$found"
  fi
  rm -f "$rep"
fi

if [ -s "$found" ]; then
  sort -u "$found"
  echo "Toplam bulgu: $(sort -u "$found" | wc -l | tr -d ' ')" >&2
  exit 1
fi
echo "Bulgu yok."
