# Octo'nun bilinen sınırları

Bunlar hata değil; bilerek verilmiş ya da şimdilik çözülmemiş sınırlardır. Octo'dan ne beklemeniz gerektiğini önceden bilin diye burada.

## Tarayıcıdan gelen indirmeler

- **Form ile başlayan (POST) indirmeler tarayıcıda kalır.** Chrome, bir formu gönderince açılan indirmenin gövdesini eklentiye vermiyor. Octo aynı adresi sıradan bir istekle isteseydi sunucu başka bir şey dönerdi. Bu yüzden eklenti bu indirmeleri Octo'ya yollamaz, Chrome kendisi indirir. Sizin yapacağınız bir şey yok.
- **Yalnız Chrome ve Chromium tabanlı tarayıcılar var** (Chrome, Edge, Brave, Arc, Vivaldi…). Safari ve Firefox için eklenti yok; o tarayıcılardan indirmeleri Octo'ya adresi yapıştırarak ekleyebilirsiniz.
- **Gizli penceredeki indirmeler tarayıcıda kalır.** Eklentiye "Gizli modda izin ver" verilse de Octo gizli pencerede devreye girmez: indirmeyi yakalamaz, video düğmesini göstermez, "Octo ile indir" menüsü bir şey yapmaz. Octo'ya geçen indirme normal profilin çerezleriyle gider ve Octo'nun listesinde kalıcı olarak kalırdı.
- **Chrome'un Güvenli Tarama (Safe Browsing) denetimi Octo'ya geçen indirmelerde çalışmaz.** Chrome'un "tehlikeli dosya" uyarısı devreye girmeden önce indirme Octo'ya geçer ve Octo dosyayı yeniden indirir. Geriye macOS'in koruması kalır: Octo her dosyaya indirme karantinası işaretler, dosyayı açtığınızda Gatekeeper ve XProtect denetler. Şüphelendiğiniz bir dosyayı Alt tuşuna basılı tutarak indirin; o zaman Chrome indirir ve kendi denetimini yapar.
- Eklenti yalnız adres, çerez ve sayfa adresi (Referer) gönderir. Tarayıcıda oturum açtığınız bir sitede dosya yine de gelmezse adresi Octo'ya elle ekleyip **Girişler** bölümünden kullanıcı adı ve parolayı verin.

## Site girişleri

- **Girişler'e kaydettiğiniz parola yalnız `https://` adreslere gönderilir.** Düz `http://` bir adres giriş isterse indirme "giriş gerekli (güvensiz bağlantı)" hatasıyla durur; parola ağda açık metin olarak gideceği için bilerek gönderilmez (yerel ağdaki NAS gibi düz HTTP sunucular da buna dahil).

## Silme

- **Bitmemiş bir indirmeyi dosyasıyla birlikte silerseniz yarım dosya Çöp'e gitmez, kalıcı silinir.** Yarım bir parça Çöp'ten geri alınsa da işe yaramaz. Bitmiş dosya her zaman Çöp'e gider ve oradan geri alınabilir.

## Zamanlayıcı

- Başlama ve durma yalnız o dakikada, Octo çalışıyorsa tetiklenir. Mac o dakikada uyuyorsa ya da Octo kapalıysa o gün atlanır; sonradan telafi edilmez.
- **Durma saati gelince** kuyruk durur, yeni dosya başlamaz. Çalışan indirmeler durur ve yeniden "sırada" olur; yarım dosyalar kalır, kuyruk bir sonraki başlama saatinde kaldığı yerden sürer. Sunucu kaldığı yerden devamı desteklemiyorsa o indirme kesilmez (kesilse baştan inerdi), bitene dek sürer.
- Bu otomatik durmada **uyarı çıkmaz** ve kuyruğa bağlı "bitince uyut / kapat / aç" işlemi **çalışmaz** (bu işlem yalnız kuyruğun tüm dosyaları bitince çalışır). Kuyruğu siz durdurduğunuzda da çalışmaz.
- Zamanlayıcı Octo'nun içindeki indirme servisinde işler; uygulama penceresi o sırada ara giremez.

## Uyku

- **"İndirme sürerken Mac uyumasın" (Ayarlar > Gelişmiş) yalnız Octo uygulaması açıkken işler.** Uygulama kapalıyken indirme servisi (zamanlayıcı ya da tarayıcıdan gelen indirme) çalışıyor olsa da Mac boşta kalınca uyuyabilir. Kuyruğun "bitince uyut" işlemi ve kapağı kapatmak bu anahtardan etkilenmez, Mac'i yine uyutur.

## Tek Octo, eski Hızlı İndir

- **Aynı anda tek Octo çalıştırın.** Octo'nun kendi servisi (motor) tek olmalı.
- Eski **Hızlı İndir** uygulaması hâlâ çalışıyorsa Octo'yu açmadan önce onu kapatın. Veriler Octo'ya kopyalanır, eski klasör geri dönüş için yerinde kalır. İki servis aynı yarım dosyaya yazarsa dosya bozulabilir ya da çift dosya çıkar. Kurulum betiği eski servisi durdurmayı dener ama her durumda başarılı olacağı garanti değildir.
- Eski `~/Library/Application Support/HizliIndir` klasöründeki `downloads.json` eski çerezleri içerebilir. İşiniz bittiğinde o klasörü kendiniz Çöp'e atabilirsiniz; Octo silmez.
- Eski `hizli-indir` komut bağı (`/opt/homebrew/bin/hizli-indir`) bilerek yerinde bırakıldı.

## Görünüm

- Menü çubuğu simgesi ve vurgu rengi (logo moru) tasarım tuvalinden bilerek farklıdır; tuvalde mavi vardı.
- Arama alanı ve satır görünümü macOS'un kendi bileşenlerini kullanır; tuvaldeki ölçülerle birebir değildir.
- Ayarlar'daki bazı seçenekler (indirme başına hız sınırı, deneme sayısı, vekil sunucu ayarı, UA düzenleme) motorda karşılığı olmadığı için yoktur. Vekil sunucu ortam değişkeninden okunur.

## Video indirme

- **Korumalı videolar indirilmez.** Netflix, Disney+, Prime Video gibi DRM'li (Widevine/FairPlay) ya da şifreli HLS (AES-128 dahil) videoları Octo indirmez ve bu korumayı aşmaya çalışmaz. Düğme "Bu video korumalı (DRM), indirilemez" der; Octo korumalı akışın video parçalarını hiç istemez.
- **Canlı yayınlar indirilmez ve kaydedilmez.** Yalnız sonu belli (isteğe bağlı) videolar iner.
- **Octo yalnız tarayıcının açıkça gördüğü video adreslerini kullanır** (video dosyası, HLS `.m3u8`, DASH `.mpd`). YouTube gibi kendi akış biçimini kullanan sitelerde düğme indirme seçeneği sunmaz; adresinde `m3u8`/`mpd` geçmeyen akışlar da görülmeyebilir.
- Eklenti kurulmadan ya da yeniden yüklenmeden önce açılmış sekmede düğme akışı bulamaz: **sayfayı yenileyip videoyu oynatın.**
- **HLS (TS) videolar `.ts` dosyası olarak kaydedilir.** QuickTime Player `.ts` açmaz; VLC ya da IINA ile oynatın. HLS fMP4 ve DASH videoları `.mp4` olur. TS parçalı bir listede `EXT-X-MAP` varsa (nadir) Octo onu mp4 sayar ve birleştirme hata verir.
- Ses ve görüntüsü ayrı gelen TS akışları, WebM biçimli DASH videoları ve reklam araları olan (çok bölümlü) akışlar desteklenmez. Altyazılar indirilmez.
- Ses ve görüntü birleştirilemezse ikisi yan yana ayrı dosya olarak kalır (`… .video.mp4`, `… .audio.m4a`). Bu durumda ayrı kalan **`.audio.m4a` dosyasına indirme karantinası işaretlenmez** (yalnız ana video dosyasına işlenir); iki dosyaya da güvenmeden önce siz bakın.
- Sunucu manifesti gzip ile sıkıştırıp yollarsa (Octo sıkıştırma istemese de) manifest çözülmez ve akış eklenemez.
- Ses grubunda hem gömülü (adresi olmayan) varsayılan hem adresli başka ses seçenekleri varsa Octo varsayılan olarak adresli ilkini seçer; ses seçimi yalnız uygulamadaki seçim sayfasında yapılır.
- İndirme ve birleştirme sırasında video boyutunun yaklaşık iki katı boş disk gerekir.
- İmzalı akış adreslerinin süresi dolabilir. Duraklatılmış bir video sürmezse sayfayı yenileyip videoyu yeniden ekleyin ya da "Adresi yenile"yi kullanın.
- Kapalı (closed) Shadow DOM içindeki ya da servis çalışanıyla (service worker) akış çeken oynatıcılarda düğme çıkmayabilir. Düğme klavye Tab sırasının sonundadır. Düğme Ayarlar'dan ya da "Bu sitede gösterme" ile kapatılabilir.
- Video ilerlemesi bayt değil parça sayısıyla gösterilir; toplam boyut çoğu zaman bitene dek bilinmez.
- **Video düğmesi yerel ağdaki bir adrese ancak sayfanın kendisi de yerel ağdaysa gider.** Dışarıdaki bir sayfa yerel bir adres gösterse (`<video src="http://192.168.1.1/…">`) düğme onu sunmaz. Octo da dış sayfadan gelen bir akışta yerel ağa bağlanmaz: akışın alan adı yerel bir adrese çözülse de bağlantı anında çözülen adrese bakar. Bu yüzden dış bir sitenin web arayüzünden açtığınız yerel sunucu videosu (ör. medya sunucunuzun genel sitesi) indirilmez; videoyu sunucunun yerel adresinden açın.
- Bu denetimin sınırları: (1) Octo sayfayı önce sayfa adresinden (Referer) tanır. Referer yoksa (`https://` bir sayfadan `http://` bir akışa gitmez) eklentinin gönderdiği sayfa adresine bakar. İkisi de yoksa (uygulamaya elle eklenen akış, eski eklenti) eski kural geçer: ilk bağlantı yerel ağdaysa iş yerel sayılır. Sayfanın adı IP harfi ya da `localhost` değilse Octo adı çözer; sayfa ancak çözülen adreslerin hepsi yerel ağdaysa yerel sayılır. Yerel ağdaki bir ad genel bir IPv6 adresine de çözülüyorsa (birçok ev ağında olur) dış sayılır; o durumda sayfayı IP adresiyle (ör. `http://192.168.1.10/`) açın. Sayfa açıldıktan sonra adını yalnız yerel bir adrese çeviren bir site (DNS rebinding) bu denetimi aşabilir; o durumda giden, yerel cihaza çerezsiz tek bir istektir. Eklenti alan adını çözemez, yalnız IP harfine ve `localhost`, `.local` gibi yerel adlara bakar. (2) Doğrudan video dosyasında (`.mp4` gibi) yalnız eklentinin denetimi vardır; Octo onu tarayıcıdan yakalanan sıradan indirmeden ayıramaz. (3) Sistemde ya da ortamda vekil sunucu (proxy) açıksa bağlantı vekile gider, hedefin IP'si görülmez; o zaman yalnız adresteki IP harfi ve `localhost` denetlenir.

## Doğrulanmamış

Gerçek Chrome'da elle denenmemiş şeyler: form (POST) indirmesinin tarayıcıda kalması, güncellemede eklenti izin uyarısı çıkmaması, `Referer` alanının tam adres mi olduğu. Mantıkları testlerle sınandı. Video için gerçek CDN'ler ve imzalı adresler, vekil sunucu altında parça indirme ve QuickTime'da birleşik dosyanın elle oynatılması denenmedi; yerel fikstürlerle, ffprobe ve AVFoundation denetimiyle sınandı.
