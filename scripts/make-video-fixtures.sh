#!/usr/bin/env bash
# Video test fikstürlerini ffmpeg ile BİR KEZ üretir (160x90, 4 sn, 1 sn parça, testsrc + sine).
# Üretilen dosyalar engine/testdata/video/ altında commit'lenir; testler ffmpeg gerektirmez.
# Kullanım: scripts/make-video-fixtures.sh [çıkış klasörü]   (varsayılan engine/testdata/video; mevcut içerik yenilenir)
# Çıkış: 0 tamam, 1 hata, 2 ortam (ffmpeg yok / klasör güvensiz).
set -eu
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/.." && pwd)"
OUT="${1:-$root/engine/testdata/video}"
command -v ffmpeg >/dev/null || { echo "ffmpeg yok" >&2; exit 2; }
case "$OUT" in */testdata/video|"${TMPDIR:-/tmp}"/*) ;; *) echo "güvensiz çıkış klasörü: $OUT" >&2; exit 2 ;; esac

# Eski üretimi Çöp'e değil, yalnız kendi adlarımızdaki alt klasörlerden temizle (rm yok: üzerine yaz).
mkdir -p "$OUT"
for d in hls-ts hls-fmp4 hls-fmp4-audio hls-aes hls-live dash dash-drm; do mkdir -p "$OUT/$d"; done
FF=(ffmpeg -hide_banner -loglevel error -y -bitexact)
SRC=(-f lavfi -i "testsrc=size=160x90:rate=10:duration=4" -f lavfi -i "sine=frequency=440:sample_rate=44100:duration=4")
V264=(-c:v libx264 -preset veryfast -profile:v baseline -pix_fmt yuv420p -b:v 60k -maxrate 60k -bufsize 120k -g 10 -keyint_min 10 -sc_threshold 0)
AAC=(-c:a aac -b:a 24k -ar 44100 -ac 1)

# (1) tek parça MP4 (faststart: Range'siz sunucuda da akabilir)
"${FF[@]}" "${SRC[@]}" "${V264[@]}" "${AAC[@]}" -movflags +faststart -t 4 "$OUT/direct.mp4"

# (2) HLS TS master, 2 varyant (160x90 ve 128x72), gömülü ses
( cd "$OUT/hls-ts" && "${FF[@]}" "${SRC[@]}" \
  -filter_complex "[0:v]split=2[a][b];[a]scale=160:90[v0];[b]scale=128:72[v1]" \
  -map "[v0]" -map 1:a -map "[v1]" -map 1:a "${V264[@]}" "${AAC[@]}" -b:v:1 40k -maxrate:v:1 40k \
  -f hls -hls_time 1 -hls_playlist_type vod -hls_segment_filename "v%v/seg%03d.ts" \
  -master_pl_name master.m3u8 -var_stream_map "v:0,a:0 v:1,a:1" "v%v/index.m3u8" )

# (3) HLS fMP4 (EXT-X-MAP), gömülü ses ... ve (3b) ayrı ses rendition'ı (iki iz birleştirme)
( cd "$OUT/hls-fmp4" && "${FF[@]}" "${SRC[@]}" "${V264[@]}" "${AAC[@]}" \
  -f hls -hls_time 1 -hls_playlist_type vod -hls_segment_type fmp4 -hls_fmp4_init_filename init.mp4 \
  -hls_segment_filename "seg%03d.m4s" index.m3u8 )
( cd "$OUT/hls-fmp4-audio" && "${FF[@]}" "${SRC[@]}" "${V264[@]}" "${AAC[@]}" \
  -f hls -hls_time 1 -hls_playlist_type vod -hls_segment_type fmp4 -hls_fmp4_init_filename init_%v.mp4 \
  -hls_segment_filename "%v/seg%03d.m4s" -master_pl_name master.m3u8 \
  -var_stream_map "v:0,agroup:aud,name:video a:0,agroup:aud,name:audio,default:yes" "%v/index.m3u8" )

# (4) HLS AES-128: sabit TEST anahtarı (gizli değil), anahtar adresi göreli key.bin
printf '\x00\x01\x02\x03\x04\x05\x06\x07\x08\x09\x0a\x0b\x0c\x0d\x0e\x0f' > "$OUT/hls-aes/key.bin"
KI="$(mktemp -d "${TMPDIR:-/tmp}/octo-fx.XXXXXX")"   # key.info mutlak yol içerir: çıkış klasörüne girmesin
printf 'key.bin\n%s\n000102030405060708090a0b0c0d0e0f\n' "$OUT/hls-aes/key.bin" > "$KI/key.info"
( cd "$OUT/hls-aes" && "${FF[@]}" "${SRC[@]}" "${V264[@]}" "${AAC[@]}" \
  -f hls -hls_time 1 -hls_playlist_type vod -hls_key_info_file "$KI/key.info" \
  -hls_segment_filename "seg%03d.ts" index.m3u8 )

# (5) HLS canlı: (2)'nin tek varyantı, ENDLIST'siz ve PLAYLIST-TYPE'sız
( cd "$OUT/hls-live" && "${FF[@]}" "${SRC[@]}" "${V264[@]}" "${AAC[@]}" \
  -f hls -hls_time 1 -hls_list_size 0 -hls_segment_filename "seg%03d.ts" index.m3u8 )
grep -v '^#EXT-X-ENDLIST' "$OUT/hls-live/index.m3u8" | grep -v '^#EXT-X-PLAYLIST-TYPE' > "$OUT/hls-live/index.m3u8.tmp"
mv "$OUT/hls-live/index.m3u8.tmp" "$OUT/hls-live/index.m3u8"

# (6) DASH: SegmentTemplate + SegmentTimeline, ayrı görüntü ve ses
( cd "$OUT/dash" && "${FF[@]}" "${SRC[@]}" "${V264[@]}" "${AAC[@]}" -map 0:v -map 1:a \
  -f dash -seg_duration 1 -use_template 1 -use_timeline 1 -adaptation_sets "id=0,streams=v id=1,streams=a" \
  -init_seg_name 'init-$RepresentationID$.m4s' -media_seg_name 'chunk-$RepresentationID$-$Number%05d$.m4s' manifest.mpd )

# (7) DASH + elle ContentProtection (parçalar ../dash'ten; BaseURL ile)
python3 - "$OUT/dash/manifest.mpd" "$OUT/dash-drm/manifest.mpd" <<'PY'
import sys, re
s = open(sys.argv[1], encoding="utf-8").read()
cp = '<ContentProtection schemeIdUri="urn:uuid:edef8ba9-79d6-4ace-a3c8-27dcd51d21ed" value="Widevine"/>'
s = re.sub(r'(<AdaptationSet\b[^>]*>)', r'\1' + cp, s, count=1)
s = re.sub(r'(<MPD\b[^>]*>)', r'\1<BaseURL>../dash/</BaseURL>', s, count=1)
open(sys.argv[2], "w", encoding="utf-8").write(s)
PY
echo "fikstürler: $OUT"; ls -R "$OUT" | wc -l
