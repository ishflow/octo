// octo: parçalı indirme motorunun komut satırı arayüzü (CLI).
// Motor engine paketindedir; burada yalnız bayrak okuma ve ilerleme yazdırma var.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"hizli-indir/engine"
)

func human(b float64) string {
	u := []string{"B", "KB", "MB", "GB", "TB"}
	i := 0
	for b >= 1024 && i < len(u)-1 {
		b /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", b, u[i])
}

func run(rawURL, out string, maxConn int) (string, error) {
	d, err := engine.Resume(context.Background(), engine.Request{URL: rawURL, Path: out, MaxConns: maxConn, Overwrite: out != ""}, nil) // -o: yarım .part sürer, hedef ezilir
	if err != nil {
		return "", err
	}
	if !d.Snapshot().Resumable {
		fmt.Fprintln(os.Stderr, "Sunucu parçalı indirmeyi desteklemiyor, tek bağlantı.")
	}
	done := make(chan error, 1)
	go func() { done <- d.Wait() }()
	start := time.Now()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case err := <-done:
			s := d.Snapshot()
			fmt.Fprintf(os.Stderr, "\r%s indi, ortalama %s/s                         \n", human(float64(s.Got)), human(float64(s.Got)/time.Since(start).Seconds()))
			return d.Path(), err
		case <-tick.C:
			s := d.Snapshot()
			conns := 0
			for _, c := range s.Conns {
				if c.Pos <= c.End {
					conns++
				}
			}
			fmt.Fprintf(os.Stderr, "\r%5.1f%%  %s / %s  %s/s  %d bağlantı   ", 100*float64(s.Got)/float64(max(s.Size, 1)), human(float64(s.Got)), human(float64(s.Size)), human(float64(s.Got)/time.Since(start).Seconds()), max(conns, 1))
		}
	}
}

// newBackend servis Yöneticisini gerçek bağımlılıklarla kurar.
var newBackend = func(dataDir string) (*engine.Manager, error) {
	return engine.NewManager(dataDir, engine.DefaultDeps())
}

func serve() int {
	// Eski servis aynı .part dosyalarına yazıyor olabilir: göç yok, indirme başlatılmaz, uyarı her denemede görünür.
	legacy := engine.LegacyEngineRunning()
	if legacy {
		log.Print("octo: ", engine.LegacyRunningMsg)
	} else if err := engine.MigrateLegacyDataDir(); err != nil {
		// Göç yarıda kalırsa açılmayız: sonraki açılış göçü yeniden denesin (eski veri kayıp görünmesin).
		fmt.Fprintln(os.Stderr, "hata: eski veri klasörü kopyalanamadı:", err)
		return 1
	}
	ln, err := engine.Listen(engine.SockPath())
	if err == engine.ErrAlreadyRunning {
		fmt.Fprintln(os.Stderr, "Octo servisi zaten çalışıyor")
		return 0
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "hata:", err)
		return 1
	}
	b, err := newBackend(engine.DataDir())
	if err != nil {
		ln.Close()
		fmt.Fprintln(os.Stderr, "hata:", err)
		return 1
	}
	defer b.Close() // SIGINT/SIGTERM: indirmeler duraklar, durum diske yazılır
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if legacy {
		b.Block(engine.LegacyRunningMsg)
		go exitWhenLegacyStops(ctx, stop)
	}
	if err := engine.ServeListener(ctx, b, ln); err != nil {
		fmt.Fprintln(os.Stderr, "hata:", err)
		return 1
	}
	return 0
}

// exitWhenLegacyStops: eski servis kapanınca bloklu motoru durdurur; uygulama yeniden bağlanırken
// başlattığı yeni `serve` göçü yapar.
func exitWhenLegacyStops(ctx context.Context, stop func()) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if !engine.LegacyEngineRunning() {
				log.Print("octo: eski servis kapandı, motor yeniden başlatılmak üzere çıkıyor")
				stop()
				return
			}
		}
	}
}

// add servise `add {start:true}` yollar; servis yoksa doğrudan indirir.
func add(url string) int {
	if d, err := connectDaemon(engine.SockPath(), nil, 0); err == nil {
		defer d.Close()
		r, err := d.call("add", map[string]any{"url": url, "start": true})
		if err != nil {
			fmt.Fprintln(os.Stderr, "hata:", err)
			return 1
		}
		fmt.Fprintln(os.Stderr, "Servise eklendi")
		if it, ok := r["item"].(map[string]any); ok {
			fmt.Println(it["name"])
		}
		return 0
	}
	name, err := run(url, "", 8)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hata:", err)
		return 1
	}
	fmt.Println(name)
	return 0
}

func main() {
	if len(os.Args) > 1 {
		switch a := os.Args[1]; {
		case a == "native-host" || strings.HasPrefix(a, "chrome-extension://"):
			if err := newNativeHost(engine.SockPath()).serve(os.Stdin, os.Stdout); err != nil {
				fmt.Fprintln(os.Stderr, "hata:", err)
				os.Exit(1)
			}
			return
		case a == "serve":
			os.Exit(serve())
		case a == "add" && len(os.Args) == 3:
			os.Exit(add(os.Args[2]))
		}
	}
	conns := flag.Int("n", 8, "en fazla bağlantı (varsayılan 8; 1,2,4,8,16,24,32)")
	out := flag.String("o", "", "çıktı dosyası (boşsa sunucudaki ad)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "kullanım: octo [-n 8] [-o dosya] URL | add URL | serve | native-host")
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() != 1 || *conns < 1 || *conns > 32 {
		flag.Usage()
		os.Exit(2)
	}
	name, err := run(flag.Arg(0), *out, *conns)
	if err != nil {
		fmt.Fprintln(os.Stderr, "hata:", err)
		os.Exit(1)
	}
	fmt.Println(name)
}
