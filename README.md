<p align="center">
  <img src="docs/media/banner.png" alt="Octo — a download manager for macOS" width="100%">
</p>

<p align="center">
  <a href="https://github.com/ishflow/octo/releases/latest"><img src="https://img.shields.io/github/v/release/ishflow/octo?color=7c5cff&label=release" alt="Latest release"></a>
  <img src="https://img.shields.io/badge/macOS-26%2B-111?logo=apple" alt="macOS 26+">
  <img src="https://img.shields.io/badge/engine-Go-00ADD8?logo=go&logoColor=white" alt="Go">
  <img src="https://img.shields.io/badge/app-SwiftUI-F05138?logo=swift&logoColor=white" alt="SwiftUI">
  <img src="https://img.shields.io/badge/extension-Chrome-4285F4?logo=googlechrome&logoColor=white" alt="Chrome extension">
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-7c5cff" alt="MIT"></a>
</p>

<p align="center">
  <a href="https://github.com/ishflow/octo/releases/latest"><b>Download</b></a> ·
  <a href="https://ishflow.com/ai/projects/octo">Website</a> ·
  <a href="#install">Install</a> ·
  <a href="#faq">FAQ</a> ·
  <a href="README.tr.md">Türkçe</a>
</p>

A native download manager for macOS. Octo splits a file into parts and downloads them over several connections at once, resumes interrupted downloads, and takes over downloads from Chrome. It is a Go download engine, a SwiftUI app (Liquid Glass) and a Chrome extension. The app is in Turkish.

<p align="center">
  <a href="https://ishflow.com/ai/projects/octo"><img src="docs/media/film.png" alt="Watch the Octo film (37 s)" width="820"></a>
</p>

## Features

<table>
  <tr>
    <td width="50%" valign="top">
      <img src="docs/media/speed.gif" alt="Many connections" width="100%">
      <h3>One file. Many connections.</h3>
      8 connections by default (1, 2, 4, 8, 16, 24 or 32). A download starts with one connection and doubles it every 2 seconds; the engine always splits the largest remaining part in half.
    </td>
    <td width="50%" valign="top">
      <img src="docs/media/resume.gif" alt="Pause and resume" width="100%">
      <h3>Pause. Quit. Resume.</h3>
      Interrupted downloads and engine restarts pick up where they stopped (when the server supports <code>Range</code>). Expired link? Renew it with <b>Bağlantıyı yenile…</b> and the download carries on if the size is unchanged.
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <img src="docs/media/chrome.gif" alt="Chrome extension" width="100%">
      <h3>One click in the browser.</h3>
      A click on a <code>.zip</code> or <code>.iso</code> link starts the download in Octo. Hold <b>Option</b> to let Chrome download instead, or pick every link on a page. Chrome, Edge, Brave, Arc, Vivaldi.
    </td>
    <td width="50%" valign="top">
      <img src="docs/media/video.gif" alt="Video download button" width="100%">
      <h3>A download button on the video.</h3>
      Plain video files, HLS (<code>.m3u8</code>) and DASH (<code>.mpd</code>) streams, with a choice of quality. Protected (DRM) videos and live streams are not downloaded.
    </td>
  </tr>
  <tr>
    <td width="50%" valign="top">
      <img src="docs/media/menu.gif" alt="Menu bar" width="100%">
      <h3>Always one glance away.</h3>
      The menu bar shows active downloads, total speed and a stop-all button. Downloads keep running when you close the window.
    </td>
    <td width="50%" valign="top">
      <h3>And more</h3>
      <ul>
        <li><b>Queues and scheduler</b> with start and stop times, batch downloads and a total speed limit.</li>
        <li><b>Site logins</b> stored in the Keychain, sent only over <code>https://</code>.</li>
        <li><b>Command line:</b> <code>octo URL</code> downloads directly, <code>octo add URL</code> hands it to the app.</li>
        <li><b>Private:</b> no telemetry, no update check.</li>
      </ul>
    </td>
  </tr>
</table>

## Screenshots

<p align="center">
  <img src="docs/img/main-window.png" alt="Octo main window" width="820">
</p>
<p align="center">
  <img src="docs/img/onboarding.png" alt="Welcome screen" width="620">
</p>

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
