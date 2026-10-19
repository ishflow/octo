# docs/img

Screenshots used by `README.md` and `README.tr.md`.

| File | Shows | Status |
|---|---|---|
| `main-window.png` | Main window: finished, running and paused downloads | **real** (live window capture, 2252×1412, 2026-10-20) |
| `onboarding.png` | Welcome sheet (download folder path masked) | **real** |

Still missing (need a click in the running app; see `docs/test-results/2026-10-20-L2.md` §5):
`new-download-video.png` (Yeni indirme with the video quality picker), `progress.png` (progress window, "Parça x/y"),
`settings-genel.png`, `settings-baglanti.png`, `settings-tarayici-video.png`, `settings-gelismis-uyku.png`.

How the real ones were taken: `screencapture -x -l <window id>` on the Octo window only (window id from `CGWindowListCopyWindowInfo`), kept at the Retina size (≤ 2400 px wide). The Mac is in dark appearance, so the captures are dark.
