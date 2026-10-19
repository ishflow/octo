#!/usr/bin/env bash
# Başarı ölçütü 1-2 (spec): yerel sunucuda (bağlantı başına hız sınırlı) 100 MB dosya
#   1) octo ↔ tek bağlantılı curl süresi + sha eşitliği
#   2) duraklat → motoru kapat → aç → devam sonrası sha eşitliği
# Motor wt içinde derlenir, geçici veri klasörü/soketle çalışır; gerçek ~/Library'ye ve internete dokunmaz.
# Ortam: BENCH_MB (100), BENCH_RATE_KBPS (bağlantı başına, 4096), BENCH_PAUSE_PCT (30),
#        BENCH_PAUSE_TRIES (5), BENCH_OUT (rapor yolu), BENCH_KEEP=1 (geçici klasör silinmesin).
# Çıkış: 0 hepsi geçti, 1 bir ölçüt kırmızı, 2 ortam hatası.
set -u
here="$(cd "$(dirname "$0")" && pwd)"; root="$(cd "$here/.." && pwd)"
MB="${BENCH_MB:-100}"; RATE="${BENCH_RATE_KBPS:-4096}"; PCT="${BENCH_PAUSE_PCT:-30}"; TRIES="${BENCH_PAUSE_TRIES:-5}"
OUT="${BENCH_OUT:-$root/docs/test-results/bench/$(date +%Y-%m-%d-%H%M).md}"

T="$(cd "${TMPDIR:-/tmp}" && pwd -P)" || exit 2
W="$(mktemp -d "$T/octo-bench.XXXXXX")" || exit 2
mkdir -p "$W/data" "$W/dl" "$(dirname "$OUT")" || exit 2
SOCK="$W/data/engine.sock"
[ "${#SOCK}" -lt 100 ] || { echo "soket yolu çok uzun (${#SOCK}): TMPDIR kısalt" >&2; exit 2; }
SRV=""; ENG=""; PORT=""; fail=0; rows=""

row() { rows="$rows$1"$'\n'; echo "$1"; }
ok()  { row "- ✅ $1"; }
bad() { row "- ❌ $1"; fail=1; }
die() { echo "ortam hatası: $1" >&2; exit 2; }
alive() { ps -p "$1" >/dev/null 2>&1; }

cleanup() {
  [ -n "$ENG" ] && alive "$ENG" && python3 "$W/oc.py" "$SOCK" shutdown >/dev/null 2>&1
  [ -n "$PORT" ] && curl -s --max-time 3 "http://127.0.0.1:$PORT/quit" >/dev/null 2>&1
  [ -n "$ENG" ] && alive "$ENG" && echo "UYARI: motor kapanmadı pid=$ENG ($W/octo serve)" >&2
  if [ "${BENCH_KEEP:-0}" = 1 ]; then echo "geçici klasör duruyor: $W"
  else case "$W" in "$T"/octo-bench.?*) rm -rf "$W" ;; esac; fi
}
trap cleanup EXIT

# ---- yardımcılar (python3 stdlib) -------------------------------------------
cat > "$W/srv.py" <<'PY'
# Range destekli, bağlantı başına hız sınırlı test sunucusu. /quit kapatır, /stats sayaçları verir.
import sys, os, time, json, threading
from http.server import ThreadingHTTPServer, BaseHTTPRequestHandler
path, rate, portfile = sys.argv[1], int(sys.argv[2]) * 1024, sys.argv[3]
size = os.path.getsize(path); mt = os.stat(path).st_mtime
lock = threading.Lock(); cur = {"now": 0, "max": 0, "gets": 0, "ranged": 0}
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
        if self.path == "/quit":
            self.reply(200, b"bye"); threading.Thread(target=self.server.shutdown).start(); return
        if self.path == "/stats":
            self.reply(200, json.dumps(cur).encode(), [("Content-Type", "application/json")]); return
        a, b, code = 0, size - 1, 200
        r = self.headers.get("Range")
        if r and r.startswith("bytes="):
            s, _, e = r[6:].partition("-")
            a = int(s) if s else size - int(e)
            b = int(e) if e and s else size - 1
            b = min(b, size - 1); code = 206
            if a > b: self.reply(416, b"", [("Content-Range", "bytes */%d" % size)]); return
        h = [("Accept-Ranges", "bytes"), ("ETag", '"bench-%d-%d"' % (size, int(mt))),
             ("Last-Modified", time.strftime("%a, %d %b %Y %H:%M:%S GMT", time.gmtime(mt))),
             ("Content-Type", "application/octet-stream")]
        if code == 206: h.append(("Content-Range", "bytes %d-%d/%d" % (a, b, size)))
        n = b - a + 1
        self.send_response(code)
        for k, v in h: self.send_header(k, v)
        self.send_header("Content-Length", str(n)); self.end_headers()
        if head: return
        with lock:
            cur["gets"] += 1; cur["ranged"] += bool(r); cur["now"] += 1; cur["max"] = max(cur["max"], cur["now"])
        try:
            t0, sent = time.monotonic(), 0
            with open(path, "rb") as f:
                f.seek(a)
                while sent < n:
                    d = f.read(min(65536, n - sent)); self.wfile.write(d); sent += len(d)
                    w = sent / rate - (time.monotonic() - t0)
                    if w > 0: time.sleep(w)
        except (BrokenPipeError, ConnectionResetError): pass
        finally:
            with lock: cur["now"] -= 1
srv = ThreadingHTTPServer(("127.0.0.1", 0), H); srv.daemon_threads = True
open(portfile, "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY

cat > "$W/oc.py" <<'PY'
# Octo motoru soket istemcisi: oc.py SOKET KOMUT [arg…]; her çağrı tek bağlantı, tek istek.
import sys, json, socket, time
sock, cmd, a = sys.argv[1], sys.argv[2], sys.argv[3:]
def call(**req):
    s = socket.socket(socket.AF_UNIX); s.settimeout(10); s.connect(sock)
    s.sendall((json.dumps(dict(id=1, **req)) + "\n").encode()); buf = b""
    while not buf.endswith(b"\n"): buf += s.recv(65536)
    s.close(); r = json.loads(buf)
    if not r.get("ok"): sys.exit("motor hatası: %s" % r.get("error"))
    return r
def item(i):
    return next((x for x in call(cmd="list")["items"] if x["id"] == i), None)
def until(pred, timeout):
    t0 = time.time()
    while time.time() - t0 < timeout:
        it = pred()
        if it: return it, time.time() - t0
        time.sleep(0.2)
    sys.exit("zaman aşımı (%ss): %s" % (timeout, item(a[0]) and {k: item(a[0])[k] for k in ("state", "got", "size", "error")}))
def done(i):
    it = item(i)
    if it and it["state"] == "error": sys.exit("indirme hatası: %s" % it["error"])
    return it if it and it["state"] == "done" else None
if cmd == "up":
    t0 = time.time()
    while True:
        try: call(cmd="list"); break
        except Exception:
            if time.time() - t0 > 15: sys.exit("motor açılmadı")
            time.sleep(0.2)
elif cmd == "timed":  # URL NAME DIR TIMEOUT → "<saniye> <id>"
    t0 = time.time(); i = call(cmd="add", url=a[0], name=a[1], dir=a[2], start=True)["item"]["id"]
    a[0] = i; until(lambda: done(i), float(a[3]))
    print("%.2f %s" % (time.time() - t0, i))
elif cmd == "add": print(call(cmd="add", url=a[0], name=a[1], dir=a[2], start=True)["item"]["id"])
elif cmd == "waitgot":  # ID BAYT TIMEOUT → got
    print(until(lambda: (lambda it: it if it and it["got"] >= int(a[1]) else None)(item(a[0])), float(a[2]))[0]["got"])
elif cmd == "pause":  # ID → durdu: "ok got" ; durmadı: çıkış 1
    call(cmd="pause", ids=[a[0]]); t0 = time.time(); prev = None
    while time.time() - t0 < 4:
        it = item(a[0]); st = (it["state"], it["got"])
        if it["state"] == "paused" and st == prev: print("ok %d" % it["got"]); sys.exit(0)
        prev = st; time.sleep(0.6)
    it = item(a[0]); print("FAIL state=%s got=%d" % (it["state"], it["got"])); sys.exit(1)
elif cmd == "show": it = item(a[0]); print(it["state"], it["got"], it["size"], it["error"] or "-")
elif cmd == "resume": call(cmd="resume", ids=[a[0]])
elif cmd == "waitdone":  # ID TIMEOUT → saniye
    print("%.2f" % until(lambda: done(a[0]), float(a[1]))[1])
elif cmd == "shutdown":
    try: call(cmd="shutdown")
    except Exception: pass
PY

sha() { shasum -a 256 "$1" | cut -d' ' -f1; }
fmt() { awk -v s="$1" -v m="$MB" 'BEGIN{printf "%.1f MB/s", m/s}'; }
start_engine() {
  OCTO_DATA_DIR="$W/data" "$W/octo" serve >>"$W/engine.log" 2>&1 & ENG=$!
  python3 "$W/oc.py" "$SOCK" up || die "motor soketi açılmadı ($W/engine.log)"
}
stop_engine() {
  python3 "$W/oc.py" "$SOCK" shutdown
  local i=0; while alive "$ENG" && [ $i -lt 60 ]; do sleep 0.5; i=$((i+1)); done
  alive "$ENG" && return 1
  wait "$ENG" 2>/dev/null; ENG=""; return 0
}

# ---- hazırlık -----------------------------------------------------------------
( cd "$root" && go build -o "$W/octo" . ) || die "go build başarısız"
head -c $((MB * 1048576)) /dev/urandom > "$W/src.bin" || die "kaynak dosya üretilemedi"
SRC_SHA="$(sha "$W/src.bin")"; SIZE=$((MB * 1048576))
python3 "$W/srv.py" "$W/src.bin" "$RATE" "$W/port" >"$W/srv.log" 2>&1 & SRV=$!
i=0; while [ ! -s "$W/port" ] && [ $i -lt 50 ]; do sleep 0.2; i=$((i+1)); done
PORT="$(cat "$W/port" 2>/dev/null)"; [ -n "$PORT" ] || die "test sunucusu açılmadı ($W/srv.log)"
URL="http://127.0.0.1:$PORT/bench.bin"
row "# Octo bench — $(date '+%Y-%m-%d %H:%M')"
row ""
row "Dosya $MB MB (rastgele), sunucu bağlantı başına ${RATE} KB/s, commit $(git -C "$root" rev-parse --short HEAD 2>/dev/null)"
row ""

# ---- Ölçüt 1: octo ↔ curl -------------------------------------------------------
CURL_S="$(curl --http1.1 -sS -o "$W/dl/curl.bin" -w '%{time_total}' "$URL")" || die "curl indiremedi"
CURL_S="$(printf '%.2f' "$CURL_S")"
[ "$(sha "$W/dl/curl.bin")" = "$SRC_SHA" ] && ok "curl sha = kaynak (sunucu doğru)" || bad "curl sha kaynaktan farklı (sunucu hatalı olabilir)"
start_engine
OCTO_S=""; r="$(python3 "$W/oc.py" "$SOCK" timed "$URL" octo1.bin "$W/dl" 300)" && OCTO_S="${r% *}" || bad "octo indirmesi bitmedi: $r"
if [ -n "$OCTO_S" ]; then
  [ "$(sha "$W/dl/octo1.bin")" = "$SRC_SHA" ] && ok "octo sha = kaynak" || bad "octo sha kaynaktan farklı"
  if awk -v a="$OCTO_S" -v b="$CURL_S" 'BEGIN{exit !(a<b)}'; then
    ok "octo curl'den hızlı: octo ${OCTO_S} sn ($(fmt "$OCTO_S")) < curl ${CURL_S} sn ($(fmt "$CURL_S"))"
  else bad "octo curl'den hızlı DEĞİL: octo ${OCTO_S} sn ($(fmt "$OCTO_S")) ≥ curl ${CURL_S} sn ($(fmt "$CURL_S"))"; fi
fi

# ---- Ölçüt 2: duraklat → kapat → aç → devam ---------------------------------------
ID2="$(python3 "$W/oc.py" "$SOCK" add "$URL" octo2.bin "$W/dl")" || die "ikinci indirme eklenemedi"
python3 "$W/oc.py" "$SOCK" waitgot "$ID2" $((SIZE * PCT / 100)) 120 >/dev/null || die "indirme %$PCT'e ulaşmadı"
n=0; GOT_P=""
while [ $n -lt "$TRIES" ]; do
  n=$((n+1)); r="$(python3 "$W/oc.py" "$SOCK" pause "$ID2")" && { GOT_P="${r#ok }"; break; }
  row "  - pause denemesi $n/$TRIES: $r"
done
if [ -n "$GOT_P" ]; then ok "pause $n. denemede tuttu (got=$GOT_P)"
else bad "pause $TRIES denemede tutmadı (B30 pause yarışı)"; GOT_P="$(python3 "$W/oc.py" "$SOCK" show "$ID2" | cut -d' ' -f2)"; fi
stop_engine && ok "motor kapandı" || { bad "motor 30 sn'de kapanmadı (pid=$ENG)"; die "motor kapanmadı, devam edilemez"; }
[ ! -e "$SOCK" ] && ok "soket temizlendi" || row "- ℹ️ soket dosyası kaldı"
start_engine; ok "motor yeniden açıldı"
read -r ST2 GOT_R _ <<<"$(python3 "$W/oc.py" "$SOCK" show "$ID2")"
row "- ℹ️ yeniden açılışta durum=$ST2 got=$GOT_R (kapatmadan önce $GOT_P)"
[ "$ST2" = paused ] && [ "$GOT_R" -ge "$GOT_P" ] && ok "durum ve bayt korundu" || bad "durum/bayt korunmadı (durum=$ST2 got=$GOT_R, önce $GOT_P)"
python3 "$W/oc.py" "$SOCK" resume "$ID2" && DONE_S="$(python3 "$W/oc.py" "$SOCK" waitdone "$ID2" 300)" || { bad "devam sonrası indirme bitmedi"; DONE_S=""; }
if [ -n "$DONE_S" ]; then
  FS="$(wc -c < "$W/dl/octo2.bin" | tr -d ' ')"
  [ "$FS" = "$SIZE" ] && ok "devam sonrası boyut = $SIZE (kalan ${DONE_S} sn)" || bad "devam sonrası boyut $FS ≠ $SIZE"
  [ "$(sha "$W/dl/octo2.bin")" = "$SRC_SHA" ] && ok "devam sonrası sha = kaynak" || bad "devam sonrası sha kaynaktan FARKLI"
fi
stop_engine || bad "motor kapanmadı (pid=$ENG)"
row ""; row "Sunucu sayaçları: $(curl -s --max-time 3 "http://127.0.0.1:$PORT/stats")"
row ""; [ $fail = 0 ] && row "**SONUÇ: GEÇTİ**" || row "**SONUÇ: KIRMIZI**"
printf '%s' "$rows" > "$OUT"; echo "rapor: $OUT"
exit $fail
