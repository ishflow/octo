//go:build e2e

// Gerçek ikili: go build → `octo serve` → soketten add → bitti → SIGTERM → durum diskte.
// Çalıştır: go test -tags e2e -run TestE2EBinary -v .
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestE2EBinaryServeSIGTERM(t *testing.T) {
	bin := buildBin(t)
	dataDir, err := os.MkdirTemp("/tmp", "hi") // sun_path 104 bayt sınırı
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dataDir) })
	dl := t.TempDir()
	data := bytes.Repeat([]byte("hizli-indir "), 200_000) // 2.4 MB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "x.bin", time.Unix(1_700_000_000, 0), bytes.NewReader(data))
	}))
	defer srv.Close()

	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), "OCTO_DATA_DIR="+dataDir)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var waitErr error
	exited := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(exited) }()
	defer func() { // test yarıda kalırsa süreç arkada kalmasın
		select {
		case <-exited:
		default:
			cmd.Process.Signal(syscall.SIGTERM)
			<-exited
		}
	}()

	sock := filepath.Join(dataDir, "engine.sock")
	c := dialWait(t, sock, &stderr)
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	id := 0
	call := func(cmd string, args map[string]any) map[string]any {
		t.Helper()
		id++
		req := map[string]any{"id": id, "cmd": cmd}
		for k, v := range args {
			req[k] = v
		}
		b, _ := json.Marshal(req)
		c.Write(append(b, '\n'))
		if !sc.Scan() {
			t.Fatalf("yanıt yok: %v", sc.Err())
		}
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		if m["ok"] != true {
			t.Fatalf("%s: %v", cmd, m)
		}
		return m
	}

	// İkinci servis reddedilir (aynı soket).
	if out, err := exec.Command("env", "OCTO_DATA_DIR="+dataDir, bin, "serve").CombinedOutput(); err != nil || !strings.Contains(string(out), "zaten çalışıyor") {
		t.Fatalf("ikinci servis: %v %s", err, out)
	}

	st := call("list", nil)["settings"].(map[string]any)
	st["downloadDir"] = dl
	call("settings.set", map[string]any{"settings": st})
	it := call("add", map[string]any{"url": srv.URL + "/x.bin", "start": true})["item"].(map[string]any)
	for end := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		items := call("list", nil)["items"].([]any)
		if items[0].(map[string]any)["state"] == "done" {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("bitmedi: %v", items[0])
		}
	}
	got, err := os.ReadFile(filepath.Join(dl, it["name"].(string)))
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("dosya farklı: %v", err)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case <-exited:
		if waitErr != nil {
			t.Fatalf("SIGTERM sonrası çıkış: %v %s", waitErr, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SIGTERM sonrası kapanmadı")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("soket dosyası kaldı")
	}
	b, err := os.ReadFile(filepath.Join(dataDir, "downloads.json"))
	if err != nil || !strings.Contains(string(b), `"state": "done"`) && !strings.Contains(string(b), `"state":"done"`) {
		t.Fatalf("durum diske yazılmadı: %v %s", err, b)
	}
}

func buildBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "octo")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func dialWait(t *testing.T, sock string, stderr *bytes.Buffer) net.Conn {
	t.Helper()
	for i := 0; ; i++ {
		c, err := net.Dial("unix", sock)
		if err == nil {
			return c
		}
		if i > 300 {
			t.Fatalf("servis açılmadı: %s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Eski HizliIndir klasörü olan bir HOME: serve önce durumu Octo klasörüne kopyalar; `shutdown` komutu süreci 0 ile kapatır.
func TestE2EBinaryLegacyMigrateAndShutdown(t *testing.T) {
	bin := buildBin(t)
	home, err := os.MkdirTemp("/tmp", "hi") // sun_path 104 bayt sınırı
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	old := filepath.Join(home, "Library", "Application Support", "HizliIndir")
	os.MkdirAll(old, 0o700)
	if err := os.WriteFile(filepath.Join(old, "settings.json"), []byte(`{"maxConns":8,"downloadDir":"/eski/indirilenler"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "serve")
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "OCTO_DATA_DIR=") && !strings.HasPrefix(kv, "HIZLI_DATA_DIR=") && !strings.HasPrefix(kv, "HOME=") {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, "HOME="+home)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var waitErr error
	exited := make(chan struct{})
	go func() { waitErr = cmd.Wait(); close(exited) }()
	defer func() {
		select {
		case <-exited:
		default:
			cmd.Process.Signal(syscall.SIGTERM)
			<-exited
		}
	}()

	sock := filepath.Join(home, "Library", "Application Support", "Octo", "engine.sock")
	c := dialWait(t, sock, &stderr)
	defer c.Close()
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	call := func(line string) map[string]any {
		t.Helper()
		c.Write([]byte(line + "\n"))
		if !sc.Scan() {
			t.Fatalf("yanıt yok: %v", sc.Err())
		}
		var m map[string]any
		json.Unmarshal(sc.Bytes(), &m)
		if m["ok"] != true {
			t.Fatalf("%s: %v", line, m)
		}
		return m
	}
	if d := call(`{"id":1,"cmd":"list"}`)["settings"].(map[string]any)["downloadDir"]; d != "/eski/indirilenler" {
		t.Fatal("eski ayarlar taşınmadı:", d)
	}
	call(`{"id":2,"cmd":"shutdown"}`)
	select {
	case <-exited:
		if waitErr != nil {
			t.Fatalf("shutdown sonrası çıkış: %v %s", waitErr, stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("shutdown sonrası kapanmadı")
	}
	if _, err := os.Stat(sock); err == nil {
		t.Fatal("soket dosyası kaldı")
	}
	if _, err := os.Stat(filepath.Join(old, "settings.json")); err != nil {
		t.Fatal("eski klasör korunmadı:", err)
	}
}

// Eski HizliIndir servisi (sahte soket) çalışırken Octo göç etmez, indirme başlatmaz, durum yazmaz ve Türkçe uyarır;
// eski servis kapanınca sonraki açılışta göç normal yapılır.
func TestE2EBinaryLegacyRunningBlocks(t *testing.T) {
	bin := buildBin(t)
	home, err := os.MkdirTemp("/tmp", "hi") // sun_path 104 bayt sınırı
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	old := filepath.Join(home, "Library", "Application Support", "HizliIndir")
	os.MkdirAll(old, 0o700)
	os.WriteFile(filepath.Join(old, "settings.json"), []byte(`{"maxConns":8,"downloadDir":"/eski/indirilenler"}`), 0o600)
	legacy, err := net.Listen("unix", filepath.Join(old, "engine.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacy.Close()
	go func() { // eski servis gibi bağlantı kabul eder
		for {
			c, err := legacy.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	octo := filepath.Join(home, "Library", "Application Support", "Octo")

	serve := func() (call func(string) map[string]any, stop func() string, exited func() string) {
		cmd := exec.Command(bin, "serve")
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "OCTO_DATA_DIR=") && !strings.HasPrefix(kv, "HIZLI_DATA_DIR=") && !strings.HasPrefix(kv, "HOME=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+home)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() }) // test yarıda kalırsa süreç arkada kalmasın (çıkmışsa hata yok sayılır)
		c := dialWait(t, filepath.Join(octo, "engine.sock"), &stderr)
		sc := bufio.NewScanner(c)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		call = func(line string) map[string]any {
			t.Helper()
			c.Write([]byte(line + "\n"))
			if !sc.Scan() {
				t.Fatalf("yanıt yok: %v", sc.Err())
			}
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			return m
		}
		exited = func() string {
			done := make(chan struct{})
			go func() { cmd.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(20 * time.Second):
				cmd.Process.Kill()
				t.Fatal("motor kendiliğinden çıkmadı")
			}
			c.Close()
			return stderr.String()
		}
		return call, func() string {
			call(`{"id":99,"cmd":"shutdown"}`)
			return exited()
		}, exited
	}

	call, _, exited := serve()
	if d := call(`{"id":1,"cmd":"list"}`)["settings"].(map[string]any)["downloadDir"]; d == "/eski/indirilenler" {
		t.Fatal("eski servis çalışırken göç yapıldı")
	}
	if r := call(`{"id":2,"cmd":"add","url":"http://127.0.0.1:1/a.zip","name":"a.zip","start":true}`); r["ok"] != false || !strings.Contains(r["error"].(string), "Eski Hızlı İndir servisi") {
		t.Fatalf("add=%v", r)
	}
	call(`{"id":3,"cmd":"settings.set","settings":{"maxConns":4,"downloadDir":"/yeni","duplicate":"number"}}`)
	legacy.Close() // eski servis kapanınca bloklu motor kendiliğinden çıkar; uygulama yeniden bağlanınca göç yapılır
	if log := exited(); !strings.Contains(log, "Eski Hızlı İndir servisi") {
		t.Fatalf("log uyarısı yok: %q", log)
	}
	for _, f := range []string{"downloads.json", "settings.json", "queues.json"} {
		if _, err := os.Stat(filepath.Join(octo, f)); err == nil {
			t.Fatalf("eski servis çalışırken %s yazıldı", f)
		}
	}

	call, stop, _ := serve()
	if d := call(`{"id":1,"cmd":"list"}`)["settings"].(map[string]any)["downloadDir"]; d != "/eski/indirilenler" {
		t.Fatal("eski servis kapanınca göç yapılmadı:", d)
	}
	stop()
}

// Eski klasörde üç durum dosyası ve ölü (dinleyicisiz) soket dosyası: engel yok, üç dosya olduğu gibi Octo klasörüne kopyalanır,
// eski klasör değişmez; ikinci açılışta göç yinelenmez (Octo verisi eskinin üstüne yazılmaz).
func TestE2EBinaryLegacyMigrateAllFilesStaleSocket(t *testing.T) {
	bin := buildBin(t)
	home, err := os.MkdirTemp("/tmp", "hi") // sun_path 104 bayt sınırı
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	old := filepath.Join(home, "Library", "Application Support", "HizliIndir")
	octo := filepath.Join(home, "Library", "Application Support", "Octo")
	os.MkdirAll(old, 0o700)
	files := map[string]string{
		"downloads.json": `{"nextId":2,"items":[{"id":"d1","url":"http://example.invalid/a.zip","name":"a.zip","dir":"/eski/indirilenler","state":"done","size":10,"got":10,"eta":-1}]}`,
		"settings.json":  `{"maxConns":8,"downloadDir":"/eski/indirilenler"}`,
		"queues.json":    `[{"id":"main","name":"Ana kuyruk","atOnce":4,"items":[]},{"id":"q2","name":"Gece","atOnce":2,"items":["d1"]}]`,
	}
	for f, body := range files {
		if err := os.WriteFile(filepath.Join(old, f), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Çökmüş eski servisin bıraktığı soket dosyası: bağlanılamaz, engellemez.
	ul, err := net.ListenUnix("unix", &net.UnixAddr{Name: filepath.Join(old, "engine.sock"), Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	ul.SetUnlinkOnClose(false)
	ul.Close()

	serve := func() (call func(string) map[string]any, stop func() string) {
		cmd := exec.Command(bin, "serve")
		for _, kv := range os.Environ() {
			if !strings.HasPrefix(kv, "OCTO_DATA_DIR=") && !strings.HasPrefix(kv, "HIZLI_DATA_DIR=") && !strings.HasPrefix(kv, "HOME=") {
				cmd.Env = append(cmd.Env, kv)
			}
		}
		cmd.Env = append(cmd.Env, "HOME="+home)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill() }) // test yarıda kalırsa süreç arkada kalmasın (çıkmışsa hata yok sayılır)
		c := dialWait(t, filepath.Join(octo, "engine.sock"), &stderr)
		sc := bufio.NewScanner(c)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		call = func(line string) map[string]any {
			t.Helper()
			c.Write([]byte(line + "\n"))
			if !sc.Scan() {
				t.Fatalf("yanıt yok: %v", sc.Err())
			}
			var m map[string]any
			json.Unmarshal(sc.Bytes(), &m)
			if m["ok"] != true {
				t.Fatalf("%s: %v", line, m)
			}
			return m
		}
		return call, func() string {
			call(`{"id":99,"cmd":"shutdown"}`)
			c.Close()
			cmd.Wait()
			return stderr.String()
		}
	}

	call, stop := serve()
	r := call(`{"id":1,"cmd":"list"}`)
	items, queues := r["items"].([]any), r["queues"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["id"] != "d1" || items[0].(map[string]any)["state"] != "done" {
		t.Fatalf("indirme listesi göç etmedi: %v", items)
	}
	if len(queues) != 2 || queues[1].(map[string]any)["id"] != "q2" {
		t.Fatalf("kuyruklar göç etmedi: %v", queues)
	}
	for f, want := range files { // list yazma yapmaz: dosyalar kopyalandığı haliyle durur
		if got, err := os.ReadFile(filepath.Join(octo, f)); err != nil || string(got) != want {
			t.Fatalf("%s kopyası eşleşmiyor: %v %q", f, err, got)
		}
	}
	if log := stop(); strings.Contains(log, "Eski Hızlı İndir") {
		t.Fatalf("ölü soket servisi engelledi: %q", log)
	}
	for f, want := range files {
		if got, err := os.ReadFile(filepath.Join(old, f)); err != nil || string(got) != want {
			t.Fatalf("eski klasörde %s değişti: %v %q", f, err, got)
		}
	}

	// İkinci açılış: Octo kendi verisini sürdürür, eski klasörden yeniden kopyalamaz.
	os.WriteFile(filepath.Join(old, "settings.json"), []byte(`{"maxConns":2,"downloadDir":"/baska"}`), 0o600)
	call, stop = serve()
	if d := call(`{"id":1,"cmd":"list"}`)["settings"].(map[string]any)["downloadDir"]; d != "/eski/indirilenler" {
		t.Fatal("ikinci açılışta göç yinelendi:", d)
	}
	stop()
}
