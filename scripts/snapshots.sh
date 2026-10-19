#!/usr/bin/env bash
# Renders every main screen (light + dark, @2x) into docs/snapshots/*.png via AllScreensSnapshotTests.
# Usage: scripts/snapshots.sh [output-dir]
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$root/docs/snapshots}"
mkdir -p "$out"
cd "$root/mac"
xcodegen generate >/dev/null
TEST_RUNNER_SHOTS_DIR="$out" xcodebuild test -project Octo.xcodeproj -scheme Octo -derivedDataPath "$root/build/snapshots" -destination 'platform=macOS' \
  -only-testing:OctoTests/AllScreensSnapshotTests 2>&1 | tail -25 || { echo "Hata: xcodebuild test başarısız (yukarıdaki çıktıya bakın)" >&2; exit 1; }
n="$(find "$out" -maxdepth 1 -name '*.png' | wc -l | tr -d ' ')"
[ "$n" -gt 0 ] || { echo "Hata: $out içinde PNG üretilmedi (SHOTS_DIR testin çıktısına bakın)" >&2; exit 1; }
echo "$n PNG -> $out"
