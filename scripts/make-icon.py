#!/usr/bin/env python3
"""Pre-Octo app icon generator (Liquid Glass style), superseded by octo-logos/final: running it overwrites the Octo icon.
Run: python3 scripts/make-icon.py (needs Pillow + numpy)
Writes mac/HizliIndir/Assets.xcassets/AppIcon.appiconset (1024 master + all macOS sizes + Contents.json)."""
import sys
sys.exit("make-icon.py devre dışı: Octo ikonu octo-logos/final kaynağından gelir; bu betik onun üzerine yazardı.")
import json, pathlib
import numpy as np
from PIL import Image, ImageDraw, ImageFilter, ImageChops

ROOT = pathlib.Path(__file__).resolve().parent.parent
SET = ROOT / "mac/HizliIndir/Assets.xcassets/AppIcon.appiconset"
S = 2048            # supersampled canvas; downscaled to 1024
TILE = 1648         # 824 * 2: Apple's macOS icon grid
OFF = (S - TILE) // 2


def squircle(size, n=5.0):
    y, x = np.mgrid[0:size, 0:size].astype(np.float32)
    c = (size - 1) / 2
    v = (np.abs(x - c) / c) ** n + (np.abs(y - c) / c) ** n
    # soft 1.5px edge from the implicit function
    a = np.clip((1 - v ** (1 / n)) * c / 1.5, 0, 1)
    return Image.fromarray((a * 255).astype(np.uint8))


def vgrad(size, top, bottom):
    t = np.linspace(0, 1, size, dtype=np.float32)[:, None, None]
    top, bottom = np.array(top, np.float32), np.array(bottom, np.float32)
    arr = np.repeat(top + (bottom - top) * t, size, axis=1)
    return Image.fromarray(arr.astype(np.uint8))


def layer(): return Image.new("RGBA", (S, S), (0, 0, 0, 0))


def with_alpha(img, mult):
    r, g, b, a = img.split()
    return Image.merge("RGBA", (r, g, b, a.point(lambda v: int(v * mult))))


mask_t = squircle(TILE)
mask = Image.new("L", (S, S), 0); mask.paste(mask_t, (OFF, OFF))

# --- drop shadow
sh = Image.new("RGBA", (S, S), (0, 0, 0, 0))
sh.putalpha(mask.point(lambda v: int(v * 0.45)))
sh = sh.filter(ImageFilter.GaussianBlur(28)); sh = ImageChops.offset(sh, 0, 28)
canvas = sh

# --- glass body: cyan (top) -> system blue (bottom)
body = vgrad(S, (96, 220, 255), (0, 92, 245)).convert("RGBA")
# lower glow so the base reads as thick glass
glow = Image.new("RGBA", (S, S), (0, 0, 0, 0))
ImageDraw.Draw(glow).ellipse((OFF + 200, OFF + TILE * 0.55, OFF + TILE - 200, OFF + TILE * 1.25), fill=(120, 230, 255, 120))
body.alpha_composite(glow.filter(ImageFilter.GaussianBlur(120)))

# inner shadow: darkening toward edges (bottom heavier)
inner = Image.new("L", (S, S), 0)
inner.paste(ImageChops.invert(mask.filter(ImageFilter.MinFilter(3))), (0, 0))
inner = ImageChops.offset(inner, 0, -34).filter(ImageFilter.GaussianBlur(46))
ish = Image.new("RGBA", (S, S), (0, 40, 150, 0)); ish.putalpha(inner.point(lambda v: int(v * 0.55)))
body.alpha_composite(ish)

# top specular sheen (glass reflection)
sheen = Image.new("L", (S, S), 0)
ImageDraw.Draw(sheen).ellipse((OFF - 200, OFF - TILE * 0.62, OFF + TILE + 200, OFF + TILE * 0.50), fill=255)
sheen = sheen.filter(ImageFilter.GaussianBlur(6))
grad = Image.fromarray((np.clip(1 - (np.arange(S)[:, None] - OFF) / (TILE * 0.5), 0, 1) * 150 * np.ones((1, S))).astype(np.uint8))
sh_a = ImageChops.multiply(sheen, grad)
white = Image.new("RGBA", (S, S), (255, 255, 255, 0)); white.putalpha(sh_a)
body.alpha_composite(white)

# rim highlight: bright at top, fading down, faint at bottom
rim_ring = ImageChops.subtract(mask, mask.filter(ImageFilter.MinFilter(13)))  # ~6px ring
rim_ring = rim_ring.filter(ImageFilter.GaussianBlur(1.5))
fade = Image.fromarray((np.clip(1 - (np.arange(S)[:, None] - OFF) / (TILE * 0.75), 0.18, 1) * 255 * np.ones((1, S))).astype(np.uint8))
rim = Image.new("RGBA", (S, S), (255, 255, 255, 0)); rim.putalpha(ImageChops.multiply(rim_ring, fade))
body.alpha_composite(rim)
# thin cyan-ish inner bottom rim (refraction edge)
b_ring = ImageChops.multiply(rim_ring, Image.fromarray((np.clip((np.arange(S)[:, None] - (OFF + TILE * 0.6)) / (TILE * 0.4), 0, 1) * 110 * np.ones((1, S))).astype(np.uint8)))
brim = Image.new("RGBA", (S, S), (170, 245, 255, 0)); brim.putalpha(b_ring)
body.alpha_composite(brim)

body.putalpha(ImageChops.multiply(body.getchannel("A"), mask))
canvas.alpha_composite(body)

# --- foreground: speed lines + arrow (glass white)
cx = S // 2
fg = Image.new("L", (S, S), 0)
d = ImageDraw.Draw(fg)
sw = 190                         # stem width
top, neck, tip = OFF + 250, OFF + 840, OFF + 1420
hw = 400                         # head half-width
d.rounded_rectangle((cx - sw // 2, top + 260, cx + sw // 2, neck + 40), 40, fill=255)
head = [(cx - hw, neck - 10), (cx + hw, neck - 10), (cx, tip)]
d.polygon(head, fill=255)
# round the head corners
fg = fg.filter(ImageFilter.GaussianBlur(26)).point(lambda v: 255 if v > 128 else int(max(0, (v - 100) * 255 / 28)))
# speed lines (trailing above): 2 each side, shorter/fainter outward
lines = Image.new("L", (S, S), 0); ld = ImageDraw.Draw(lines)
for dx, y0, y1, a in [(-330, top + 120, neck - 70, 215), (330, top + 120, neck - 70, 215),
                      (-500, top + 230, neck - 70, 140), (500, top + 230, neck - 70, 140)]:
    ld.rounded_rectangle((cx + dx - 42, y0, cx + dx + 42, y1), 42, fill=a)
fg = ImageChops.lighter(fg, lines)

# arrow shadow
shadow = Image.new("RGBA", (S, S), (0, 40, 150, 0))
shadow.putalpha(ImageChops.offset(fg, 0, 26).filter(ImageFilter.GaussianBlur(30)).point(lambda v: int(v * 0.5)))
canvas.alpha_composite(shadow)
# glass fill: white -> pale cyan, with top-lit edge
fill = vgrad(S, (255, 255, 255), (190, 238, 255)).convert("RGBA")
fill.putalpha(fg)
canvas.alpha_composite(fill)
# edge light on the arrow top (specular)
hi = ImageChops.subtract(fg, ImageChops.offset(fg, 0, -10))
hi = Image.new("RGBA", (S, S), (255, 255, 255, 0)); hi.putalpha(ImageChops.subtract(ImageChops.offset(fg, 0, 0), ImageChops.offset(fg, 0, 14).filter(ImageFilter.GaussianBlur(2))).point(lambda v: min(255, v * 2)))
canvas.alpha_composite(hi)

master = canvas.resize((1024, 1024), Image.LANCZOS)

# --- export
SET.mkdir(parents=True, exist_ok=True)
images = []
for pt in (16, 32, 128, 256, 512):
    for sc in (1, 2):
        px = pt * sc
        name = f"icon_{pt}x{pt}{'@2x' if sc == 2 else ''}.png"
        (master if px == 1024 else master.resize((px, px), Image.LANCZOS)).save(SET / name)
        images.append({"filename": name, "idiom": "mac", "scale": f"{sc}x", "size": f"{pt}x{pt}"})
(SET / "Contents.json").write_text(json.dumps({"images": images, "info": {"author": "xcode", "version": 1}}, indent=2) + "\n")
(ROOT / "docs").mkdir(exist_ok=True)
master.resize((512, 512), Image.LANCZOS).save(ROOT / "docs/icon-preview.png")
print("ok")
