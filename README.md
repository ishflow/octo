# Octo

[Türkçe](README.tr.md)

A native download manager for macOS. Octo splits a file into parts and downloads them over several connections at once, resumes interrupted downloads, and takes over downloads from Chrome. It is a Go download engine, a SwiftUI app (Liquid Glass) and a Chrome extension. The app is in Turkish.

![Octo main window](docs/img/main-window.png)

## Features

- **Multiple connections.** 8 connections by default (1, 2, 4, 8, 16, 24 or 32). A download starts with one connection and doubles it every 2 seconds; the engine always splits the largest remaining part in half.
- **Resume.** Pause and continue; interrupted downloads and engine restarts pick up where they stopped (when the server supports `Range`). Expired links can be renewed with **Bağlantıyı yenile…** (right-click menu), and the download continues if the file size is unchanged.
- **Queues and scheduler.** Several queues with start and stop times, batch downloads (**Toplu indirme…**) and a total speed limit. Details and limits of the scheduler: [known limits](docs/BILINEN-SINIRLAR.md).
- **Menu bar icon.** Active downloads, total speed and a stop-all button; downloads keep running when you close the window.
- **Chrome extension.** A click on a `.zip` or `.iso` link starts the download in Octo instead of the browser. Hold **Option** to let Chrome download instead. Right-click menu: "Octo ile indir" and "Tüm linkleri Octo ile indir". Works with Chrome and Chromium-based browsers (Edge, Brave, Arc, Vivaldi…).
- **Video catcher.** A small download button appears on videos in the page. It handles plain video files, HLS (`.m3u8`) and DASH (`.mpd`) streams, and lets you pick the quality.
  **No DRM:** protected videos (Widevine/FairPlay, encrypted HLS) are not downloaded and Octo does not try to bypass the protection. Live streams are not supported.

  ![Video download button in a page](docs/test-results/chrome/03-video-menu-dogrudan.png)
- **Site logins.** Save a username and password per site (stored in the Keychain, sent only over `https://`).
- **Command line.** `octo URL` downloads directly, `octo add URL` adds it to the running app.

![Welcome screen](docs/img/onboarding.png)

![Page links picker ("Tüm linkleri Octo ile indir")](docs/test-results/chrome/01-links-sayfasi.png)

## Requirements

- macOS 26 or later (Liquid Glass design language)
- Chrome or a Chromium-based browser, for the extension

## Install

1. Download `Octo-<version>-macos.zip` from the Releases page and unzip it. Move **Octo.app** to `/Applications`.
2. **First launch (Gatekeeper).** Octo is ad-hoc signed and not notarized, so macOS blocks the first launch. Right-click **Octo.app** and choose **Open**, then confirm. If macOS 26 shows no "Open" button, open **System Settings › Privacy & Security**, scroll down and click **Open Anyway** next to the Octo message. Alternatively, remove the quarantine flag in Terminal:

   ```sh
   xattr -dr com.apple.quarantine /Applications/Octo.app
   ```
3. **Chrome extension.** The welcome screen of the app (and **Settings › Tarayıcı › Eklentiyi kur…**) registers the native-messaging host for your browsers. Then load the extension:
   1. Open `chrome://extensions` and turn on **Developer mode** (top right).
   2. Click **Load unpacked** and select the `ChromeExtension` folder inside the app: `/Applications/Octo.app/Contents/Resources/ChromeExtension`. (The release also ships the same extension as `Octo-Chrome-Extension-<version>.zip`; unzip it and select the folder.)
   3. The extension ID must be `elhjlbdkhcpjihiapkeoeceopennmkab` (fixed by the `key` in the manifest).
4. Optional command line: `ln -s /Applications/Octo.app/Contents/MacOS/octo /opt/homebrew/bin/octo` (or `/usr/local/bin`).

Verify the download with `shasum -a 256 -c SHA256SUMS` in the folder with the release files.

### Build from source

Needs Go 1.26, Xcode and XcodeGen.

```sh
scripts/build-app.sh          # build/Octo.app (universal, ad-hoc signed)
scripts/install.sh            # copies it to /Applications, links the octo CLI, writes the native-messaging manifest
scripts/package-release.sh    # dist/: app zip, extension zip, SHA256SUMS
```

## Uninstall

From a source checkout: `scripts/install.sh --uninstall`. It asks first, moves `Octo.app` to the Trash, removes the `octo` link it created and the `com.uba.octo` native-messaging manifests.

By hand: move `Octo.app` to the Trash, remove the extension in `chrome://extensions`, and delete the `com.uba.octo.json` files under `~/Library/Application Support/<browser>/NativeMessagingHosts/` (for Chrome: `Google/Chrome`). Octo's data stays in `~/Library/Application Support/Octo/` until you delete it; unfinished downloads are `.part` + `.part.json` files in the download folder.

## FAQ

**Does it download YouTube, Netflix and similar?**
No. Octo only uses video addresses the browser shows openly (video files, HLS, DASH). DRM-protected videos are never downloaded, and sites with their own streaming formats, such as YouTube, are not supported.

**Why is a download slow?**
Raise the connection count in Settings › Bağlantı. Some servers limit connections; the engine then reduces its connections by one. Servers without `Range` support cannot be split or resumed (the list shows "Devam: Hayır").

**A link says 403 or has expired.**
Right-click the download, choose **Bağlantıyı yenile…** and enter the new address. If the size is the same, the download continues.

**The extension shows a "!" badge.**
The native-messaging host is missing or points to an old path. Use **Settings › Tarayıcı › Eklentiyi kur…** again and reload the extension in `chrome://extensions`. Do this after moving the app, too.

**Safari or Firefox?**
There is no extension for them. Paste the address into Octo with **URL ekle**.

**Does it send data anywhere?**
Octo has no telemetry and no update check; it contacts only the servers of the files you download. Site passwords stay in your Keychain.

**Where are my files?**
In the download folder you set (default `~/Downloads`); category sub-folders are optional (Settings › Kaydetme).

**Is it a copy of another app?**
No. Octo is written from scratch. It follows the segmented-download approach known from download managers and contains no code from other download managers.

## Known limits

Things that are deliberate or not solved yet (POST downloads, scheduler, video, running next to the old "Hızlı İndir" app…): [docs/BILINEN-SINIRLAR.md](docs/BILINEN-SINIRLAR.md) (Turkish).

## Development

```sh
go test -race ./...                       # engine tests
go test -tags e2e -race ./...             # end to end: socket service + real binary
(cd mac && xcodegen generate && xcodebuild test -scheme Octo -destination 'platform=macOS')
node --test extension/chrome/test/*.test.mjs
scripts/test-install.sh                   # install.sh with a temporary HOME
scripts/test-install-native-host.sh
scripts/test-release.sh                   # release scripts
```

Architecture: one Go binary `octo` has three roles: CLI, `serve` (the engine service) and `native-host` (the bridge Chrome starts). The app is a thin client of the engine over a Unix socket (one JSON message per line); all download state lives in the engine.

```
Chrome extension --native messaging--> octo native-host --+
                                                          +--> unix socket --> octo serve
Octo.app (SwiftUI) -------------------------------------- +     (Go engine: queues, resume,
                                                                  scheduler, speed limit)
```

## License

[MIT](LICENSE)
