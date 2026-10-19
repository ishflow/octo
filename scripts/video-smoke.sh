#!/usr/bin/env bash
# Video duman testi: fikstür sunucusu + <video> sayfası + (motor stream destekliyorsa) gerçek motorla akış indirme.
#   A) sunucu/sayfa/fikstür sağlığı (her zaman)
#   B) motor: stream.probe + add{stream} → ffprobe ile çıktı denetimi; motor henüz "stream" bilmiyorsa ATLANIR (VG2 öncesi)
# Motor wt içinde derlenir, geçici veri klasörü/soketle çalışır; gerçek ~/Library'ye ve internete dokunmaz.
# Elle bakmak için: VIDEO_SMOKE_SERVE=1 → yalnız sunucuyu açar, adresi yazar, Enter'a basılınca kapanır.
# Çıkış: 0 hepsi geçti (atlananlar dahil), 1 kırmızı, 2 ortam hatası.
set -u
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/.." && pwd)"
FX="$root/engine/testdata/video"
T="$(cd "${TMPDIR:-/tmp}" && pwd -P)" || exit 2
W="$(mktemp -d "$T/octo-vsmoke.XXXXXX")" || exit 2
mkdir -p "$W/data" "$W/dl" || exit 2
SOCK="$W/data/engine.sock"
[ "${#SOCK}" -lt 100 ] || { echo "soket yolu çok uzun (${#SOCK}): TMPDIR kısalt" >&2; exit 2; }
SRV=""; ENG=""; PORT=""; fail=0; skipped=0
ok()   { echo "  ✅ $1"; }
bad()  { echo "  ❌ $1"; fail=1; }
skip() { echo "  ⏭️  $1"; skipped=$((skipped+1)); }
die()  { echo "ortam hatası: $1" >&2; exit 2; }
alive() { ps -p "$1" >/dev/null 2>&1; }

cleanup() {
  [ -n "$ENG" ] && alive "$ENG" && python3 "$W/oc.py" "$SOCK" shutdown >/dev/null 2>&1
  [ -n "$PORT" ] && curl -s --max-time 3 "http://127.0.0.1:$PORT/quit" >/dev/null 2>&1
  [ -n "$ENG" ] && alive "$ENG" && echo "UYARI: motor kapanmadı pid=$ENG ($W/octo serve)" >&2
  case "$W" in "$T"/octo-vsmoke.?*) rm -rf "$W" ;; esac
}
trap cleanup EXIT

command -v python3 >/dev/null || die "python3 yok"
[ -f "$FX/direct.mp4" ] || die "fikstürler yok: scripts/make-video-fixtures.sh çalıştırın"

# ---- sunucu: fikstür kökü + / (sayfa) + /stats (yola göre GET sayısı) + /quit --------------
cat > "$W/srv.py" <<'PY'
import sys, os, json, mimetypes, threading
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
fx, page, portfile = sys.argv[1], sys.argv[2], sys.argv[3]
MIME = {".m3u8": "application/vnd.apple.mpegurl", ".mpd": "application/dash+xml", ".ts": "video/mp2t",
        ".mp4": "video/mp4", ".m4s": "video/iso.segment", ".html": "text/html; charset=utf-8"}
lock = threading.Lock(); hits = {}
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def log_message(self, *a): pass
    def do_HEAD(self): self.serve(True)
    def do_GET(self): self.serve(False)
    def reply(self, code, body=b"", extra=()):
        self.send_response(code)
        for k, v in extra: self.send_header(k, v)
        self.send_header("Content-Length", str(len(body))); self.end_headers(); self.wfile.write(body)
    def serve(self, head):
        p = self.path.split("?")[0]
        if p == "/quit":
            self.reply(200, b"bye"); threading.Thread(target=self.server.shutdown).start(); return
        if p == "/stats":
            self.reply(200, json.dumps(hits).encode(), [("Content-Type", "application/json")]); return
        f = page if p in ("/", "/index.html") else os.path.normpath(os.path.join(fx, p.lstrip("/")))
        if not (f == page or f.startswith(fx + os.sep)) or not os.path.isfile(f):
            self.reply(404, b"yok"); return
        data = open(f, "rb").read(); size = len(data); a, b, code = 0, size - 1, 200
        r = self.headers.get("Range")
        if r and r.startswith("bytes="):
            s, _, e = r[6:].partition("-"); a = int(s) if s else size - int(e); b = min(int(e) if e and s else size - 1, size - 1); code = 206
            if a > b: self.reply(416, b"", [("Content-Range", "bytes */%d" % size)]); return
        h = [("Content-Type", MIME.get(os.path.splitext(f)[1], "application/octet-stream")), ("Accept-Ranges", "bytes")]
        if code == 206: h.append(("Content-Range", "bytes %d-%d/%d" % (a, b, size)))
        if not head:
            with lock: hits[p] = hits.get(p, 0) + 1
        self.send_response(code)
        for k, v in h: self.send_header(k, v)
        self.send_header("Content-Length", str(b - a + 1)); self.end_headers()
        if not head: self.wfile.write(data[a:b + 1])
srv = ThreadingHTTPServer(("127.0.0.1", 0), H); srv.daemon_threads = True
open(portfile, "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY
python3 "$W/srv.py" "$FX" "$here/video-smoke/page.html" "$W/port" >"$W/srv.log" 2>&1 & SRV=$!
i=0; while [ ! -s "$W/port" ] && [ $i -lt 50 ]; do sleep 0.2; i=$((i+1)); done
PORT="$(cat "$W/port" 2>/dev/null)"; [ -n "$PORT" ] || die "test sunucusu açılmadı ($W/srv.log)"
B="http://127.0.0.1:$PORT"

if [ "${VIDEO_SMOKE_SERVE:-0}" = 1 ]; then
  echo "sayfa: $B/   (kapatmak için Enter)"; read -r _; exit 0
fi

# ---- A) sunucu / sayfa / fikstür sağlığı ---------------------------------------------------
echo "# A) sunucu ve fikstürler ($B)"
ct() { curl -s --path-as-is --max-time 5 -o /dev/null -w "%{http_code} %{content_type}" "$B$1"; }
[ "$(ct /)" = "200 text/html; charset=utf-8" ] && ok "sayfa açılıyor" || bad "sayfa: $(ct /)"
grep -q '<video id="direct" src="/direct.mp4"' "$here/video-smoke/page.html" && ok "sayfada <video> var" || bad "sayfada <video> yok"
[ "$(ct /hls-ts/master.m3u8)" = "200 application/vnd.apple.mpegurl" ] && ok "m3u8 içerik türü" || bad "m3u8: $(ct /hls-ts/master.m3u8)"
[ "$(ct /dash/manifest.mpd)" = "200 application/dash+xml" ] && ok "mpd içerik türü" || bad "mpd: $(ct /dash/manifest.mpd)"
[ "$(curl -s --max-time 5 -o /dev/null -w '%{http_code}' -H 'Range: bytes=0-9' "$B/direct.mp4")" = 206 ] && ok "Range → 206" || bad "Range 206 değil"
[ "$(ct /../../go.mod | cut -c1-3)" = 404 ] && ok "kök dışı yol 404" || bad "kök dışı yol 404 değil"
if command -v ffprobe >/dev/null; then
  d="$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$FX/direct.mp4")"
  awk -v d="$d" 'BEGIN{exit !(d>=3.5 && d<=4.5)}' && ok "direct.mp4 süresi $d sn" || bad "direct.mp4 süresi $d"
else skip "ffprobe yok: süre denetimi atlandı"; fi

# ---- B) gerçek motor ---------------------------------------------------------------------------
echo "# B) motor stream indirme"
cat > "$W/oc.py" <<'PY'
# Octo motoru soket istemcisi. Her çağrı tek bağlantı, tek istek. Çıkış: 0 ok, 3 "Bilinmeyen komut", 1 diğer hata.
import sys, json, socket, time
sock, cmd, a = sys.argv[1], sys.argv[2], sys.argv[3:]
def call(**req):
    s = socket.socket(socket.AF_UNIX); s.settimeout(15); s.connect(sock)
    s.sendall((json.dumps(dict(id=1, **req)) + "\n").encode()); buf = b""
    while not buf.endswith(b"\n"): buf += s.recv(65536)
    s.close(); return json.loads(buf)
def need(r):
    if not r.get("ok"):
        print(r.get("error")); sys.exit(3 if "Bilinmeyen komut" in (r.get("error") or "") else 1)
    return r
def item(i): return next((x for x in need(call(cmd="list"))["items"] if x["id"] == i), None)
if cmd == "up":
    t0 = time.time()
    while True:
        try: need(call(cmd="list")); break
        except SystemExit: raise
        except Exception:
            if time.time() - t0 > 15: sys.exit("motor açılmadı")
            time.sleep(0.2)
elif cmd == "probe":    # URL → JSON info (stream.probe)
    print(json.dumps(need(call(cmd="stream.probe", url=a[0], headers={}))["info"]))
elif cmd == "get":      # URL KIND DIR NAME [VIDEO [AUDIO]] → bitince "state|error|name"
    spec = dict(kind=a[1], video=a[4] if len(a) > 4 else "", audio=a[5] if len(a) > 5 else "")
    i = need(call(cmd="add", url=a[0], name=a[3], dir=a[2], start=True, stream=spec))["item"]["id"]
    t0 = time.time()
    while time.time() - t0 < 60:
        it = item(i)
        if it["state"] in ("done", "error"): print("%s|%s|%s" % (it["state"], it.get("error") or "", it.get("name") or "")); sys.exit(0)
        time.sleep(0.2)
    sys.exit("zaman aşımı: %s" % item(i)["state"])
elif cmd == "shutdown":
    try: call(cmd="shutdown")
    except Exception: pass
PY
( cd "$root" && go build -o "$W/octo" . ) || die "go build başarısız"
OCTO_DATA_DIR="$W/data" "$W/octo" serve >"$W/engine.log" 2>&1 & ENG=$!
python3 "$W/oc.py" "$SOCK" up >/dev/null || die "motor soketi açılmadı ($W/engine.log)"
oc() { python3 "$W/oc.py" "$SOCK" "$@"; }

probe_out="$(oc probe "$B/hls-ts/master.m3u8")"; rc=$?
if [ $rc = 3 ]; then
  skip "motor stream.probe bilmiyor (VG2 henüz yok): B bölümü atlandı"
elif [ $rc != 0 ]; then bad "stream.probe hata: $probe_out"
else
  jf() { python3 -c 'import sys,json; d=json.loads(sys.argv[1]); print(eval(sys.argv[2]))' "$1" "$2"; }
  [ "$(jf "$probe_out" 'len(d["variants"])')" = 2 ] && ok "TS master: 2 varyant" || bad "TS master varyant sayısı: $probe_out"
  # kabul: ad  URL  tür  video  audio  beklenen(mp4|ts)  akışSayısı
  expect() {
    local name="$1" url="$2" kind="$3" video="$4" audio="$5" ext="$6" streams="$7" r st err out n d
    r="$(oc get "$B$url" "$kind" "$W/dl" "$name" $video $audio)" || { bad "$name: $r"; return; }
    st="${r%%|*}"; r="${r#*|}"; err="${r%%|*}"; out="${r#*|}"
    [ "$st" = done ] || { bad "$name: durum=$st $err"; return; }
    [ -f "$W/dl/$out" ] || { bad "$name: dosya yok ($out)"; return; }
    case "$out" in *."$ext") ;; *) bad "$name: uzantı .$ext değil ($out)"; return ;; esac
    if command -v ffprobe >/dev/null; then
      n="$(ffprobe -v error -show_entries stream=codec_type -of json "$W/dl/$out" | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["streams"]))')"  # csv .ts'te program bölümünü de yazar
      d="$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$W/dl/$out")"
      [ "$n" = "$streams" ] && awk -v d="$d" 'BEGIN{exit !(d>=3.5 && d<=4.5)}' && ok "$name: $out ($n akış, $d sn)" || bad "$name: akış=$n süre=$d (beklenen $streams akış, 4±0,5 sn)"
    else ok "$name: $out indi (ffprobe yok, içerik denetlenmedi)"; fi
  }
  reject() { # ad URL tür beklenen-metin-parçası
    local name="$1" url="$2" kind="$3" want="$4" r
    r="$(oc get "$B$url" "$kind" "$W/dl" "$name")"
    case "$r" in *"$want"*) ok "$name: reddedildi ($want)" ;; *) bad "$name: beklenen '$want', gelen '$r'" ;; esac
  }
  # HLS kimlikleri özellik özetidir (VGR M5), sıra değil: uygulama ve eklenti gibi probe'dan alınır.
  ats="$(oc probe "$B/hls-fmp4-audio/master.m3u8")" || bad "stream.probe hata: $ats"
  expect hlsts  /hls-ts/master.m3u8         hls  "$(jf "$probe_out" 'd["variants"][0]["id"]')" "" ts 2
  expect hlsfm  /hls-fmp4/index.m3u8        hls  "" "" mp4 2
  expect hlsfma /hls-fmp4-audio/master.m3u8 hls  "$(jf "$ats" 'd["variants"][0]["id"]')" "$(jf "$ats" 'd["audios"][0]["id"]')" mp4 2
  expect dash   /dash/manifest.mpd          dash 0  1  mp4 2
  reject hlsaes  /hls-aes/index.m3u8        hls  "korumalı"
  reject hlslive /hls-live/index.m3u8       hls  "Canlı yayın"
  reject dashdrm /dash-drm/manifest.mpd     dash "korumalı"
  [ "$(curl -s --max-time 3 "$B/stats" | python3 -c 'import sys,json; print(sum(v for k,v in json.load(sys.stdin).items() if k.startswith("/hls-aes/seg")))')" = 0 ] \
    && ok "AES: parçaya hiç istek gitmedi" || bad "AES: korumalı akışın parçası istendi"
fi

python3 "$W/oc.py" "$SOCK" shutdown
i=0; while alive "$ENG" && [ $i -lt 60 ]; do sleep 0.5; i=$((i+1)); done
if alive "$ENG"; then bad "motor 30 sn'de kapanmadı (pid=$ENG)"; else wait "$ENG" 2>/dev/null; ENG=""; ok "motor kapandı"; fi
echo
if [ $fail = 0 ]; then echo "SONUÇ: GEÇTİ (atlanan: $skipped)"; else echo "SONUÇ: KIRMIZI"; fi
exit $fail
