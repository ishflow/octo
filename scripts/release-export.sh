#!/usr/bin/env bash
# Yayın için temiz kopya: release-export.sh <rev> <hedef>
# <rev> ağacını git archive ile <hedef> klasörüne çıkarır (iç notlar hariç), sır/yerel iz taraması yapar
# (rapor: <hedef>.tarama.txt) ve tek commit'li yeni bir git deposu kurar. Push YAPMAZ, uzak depo eklemez.
# Çıkış 0: temiz, 1: taramada bulgu var (depo yine kurulur; yayından önce bulguları giderin), 2: kullanım/hazırlık hatası.
# RELEASE_REPO: kaynak depo (varsayılan bu betiğin deposu).
set -euo pipefail
here="$(cd "$(dirname "$0")" && pwd)"
repo="${RELEASE_REPO:-$(cd "$here/.." && pwd)}"
rev="${1:-}"; dest="${2:-}"
[ -n "$rev" ] && [ -n "$dest" ] || { echo "Kullanım: $0 <rev> <hedef klasör>" >&2; exit 2; }
dest="${dest%/}"
git -C "$repo" rev-parse --verify -q "$rev^{commit}" >/dev/null || { echo "Hata: '$rev' bu depoda bir commit değil" >&2; exit 2; }
if [ -e "$dest" ] && [ -n "$(ls -A "$dest" 2>/dev/null)" ]; then
  echo "Hata: hedef dolu ya da klasör değil: $dest" >&2; exit 2
fi
mkdir -p "$dest"
dest="$(cd "$dest" && pwd -P)"

# Yayına girmeyenler: tersine mühendislik/iç notlar, DESIGN.md (özel tuval bağlantısı), plan ve spec'ler, test günlükleri (ekran görüntüleri docs/test-results/chrome/*.png kalır).
git -C "$repo" archive --format=tar "$rev" -- . \
  ':(exclude)docs/re' ':(exclude)docs/DESIGN.md' ':(exclude)docs/superpowers' ':(exclude).superpowers' \
  ':(exclude)docs/test-results/bench' ':(exclude,glob)docs/test-results/*' ':(exclude,glob)docs/test-results/chrome/*.log' \
  | tar -x -C "$dest"

rc=0
"$here/release-scan.sh" "$dest" > "$dest.tarama.txt" 2>&1 || rc=$?
if [ "$rc" -eq 2 ]; then echo "Hata: tarama çalışmadı, bkz. $dest.tarama.txt" >&2; exit 2; fi

# Kimlik sabittir: yerel git kimliği yayın geçmişine sızmasın.
export GIT_AUTHOR_NAME="Octo contributors" GIT_COMMITTER_NAME="Octo contributors"
export GIT_AUTHOR_EMAIL="octo-contributors@users.noreply.github.com" GIT_COMMITTER_EMAIL="octo-contributors@users.noreply.github.com"
git -C "$dest" init -q -b main
git -C "$dest" add -A
git -C "$dest" -c commit.gpgsign=false -c core.hooksPath=/dev/null commit -q -m "Initial commit"

echo "Hazır: $dest ($(git -C "$dest" rev-list --count HEAD) commit, $(git -C "$dest" ls-files | wc -l | tr -d ' ') dosya)"
if [ "$rc" -eq 1 ]; then
  echo "Taramada bulgu var, rapor: $dest.tarama.txt" >&2
  cat "$dest.tarama.txt" >&2
  exit 1
fi
echo "Tarama temiz, rapor: $dest.tarama.txt"
