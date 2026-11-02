"""Draw assets/cover.svg, the README cover, with every letter as an outline.

    uv run --with fonttools --with uharfbuzz --with pillow python scripts/make_cover.py FONT_DIR MONO_FONT

FONT_DIR holds YodelGrotesk-Bold.otf and YodelGrotesk-Regular.otf from
https://github.com/osolmaz/yodel-grotesk (assets/fonts/yodel-grotesk), and MONO_FONT is
JetBrains Mono Regular. Each string is shaped with HarfBuzz, so kerning and ligatures survive,
and each glyph is drawn at the position HarfBuzz returns. The SVG has no text elements and needs
no font.

The logo sits in front of the name. It is read from the browser page's logo file, so the cover
and the page always show the same logo.

The picture: a WhatsApp group on the left, where someone asks a question and the owner wakes
the agent, and the agent's Claude Code session on the right, which reads the chat and answers in
it. The arrow between them goes both ways.
"""

from __future__ import annotations

import hashlib
import itertools
import math
import sys
import xml.etree.ElementTree as ET
from dataclasses import dataclass
from pathlib import Path

import uharfbuzz as hb
from fontTools.pens.boundsPen import BoundsPen
from fontTools.pens.recordingPen import RecordingPen
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.svgLib.path import parse_path
from fontTools.ttLib import TTFont
from PIL import Image, ImageDraw

ROOT = Path(__file__).resolve().parent.parent
LOGO = ROOT / "internal" / "web" / "ui" / "logo.svg"

WIDTH, HEIGHT = 960, 320
BACKGROUND = "#F5F0E6"
TEXT = "#111111"
SECONDARY = "#4A4A4A"
CARD = "#FFFFFF"
CARD_BORDER = "#D9D3C7"
LABEL = "#6E6A62"
BAR = "#EBE7DE"
# WhatsApp's own chat colors, kept for fidelity: the wallpaper and the sent-message bubble.
WALLPAPER = "#EFEAE2"
SENT = "#D9FDD3"
TERM_TEXT = "#4A4A4A"
TERM_DIM = "#8A867E"

NAME = "WhatsAgent"
NAME_SIZE = 54  # below the usual 64, so that the logo and the name fit beside the picture
TAGLINE = ["A WhatsApp bridge for coding agents", "such as Claude Code and Codex."]


@dataclass
class Font:
    face: TTFont
    hb_font: hb.Font
    upem: int

    @classmethod
    def load(cls, path: Path) -> Font:
        data = path.read_bytes()
        face = hb.Face(data)
        return cls(TTFont(path), hb.Font(face), face.upem)


def shaped(font: Font, text: str, size: float, tracking: float = 0.0) -> list[tuple[str, float]]:
    """Glyph names and x positions in pixels, with HarfBuzz's kerning, then tracking and collision fixes."""
    buf = hb.Buffer()
    buf.add_str(text)
    buf.guess_segment_properties()
    hb.shape(font.hb_font, buf, {"kern": True, "liga": True})
    names = font.face.getGlyphOrder()
    scale = size / font.upem
    out, x = [], 0.0
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions, strict=True):
        assert names[info.codepoint] != ".notdef", f"the font has no glyph for a character in {text!r}"
        out.append((names[info.codepoint], x + pos.x_offset * scale))
        x += pos.x_advance * scale + tracking
    return separate(font, out, size)


RASTER = 8  # outline samples per pixel when measuring the gap between two glyphs


def bounds(font: Font, glyph: str, size: float) -> tuple[float, float] | None:
    pen = BoundsPen(font.face.getGlyphSet())
    font.face.getGlyphSet()[glyph].draw(pen)
    if pen.bounds is None:
        return None
    scale = size / font.upem
    return pen.bounds[0] * scale, pen.bounds[2] * scale


def row_extents(font: Font, glyph: str, size: float) -> dict[int, tuple[float, float]]:
    """For each sampled row of the glyph's filled outline, its leftmost and rightmost x in pixels."""
    glyph_set = font.face.getGlyphSet()
    scale = size / font.upem * RASTER
    pen = RecordingPen()
    glyph_set[glyph].draw(TransformPen(pen, (scale, 0, 0, -scale, 0, 0)))
    contours = polygons(pen.value)
    if not contours:
        return {}
    xs = [x for c in contours for x, _ in c]
    ys = [y for c in contours for _, y in c]
    left, top = math.floor(min(xs)), math.floor(min(ys))
    img = Image.new("1", (math.ceil(max(xs)) - left + 2, math.ceil(max(ys)) - top + 2), 0)
    draw = ImageDraw.Draw(img)
    for contour in contours:
        draw.polygon([(x - left, y - top) for x, y in contour], fill=1)
    out: dict[int, tuple[float, float]] = {}
    for row in range(img.height):
        filled = [x for x in range(img.width) if img.getpixel((x, row))]
        if filled:
            out[row + top] = ((filled[0] + left) / RASTER, (filled[-1] + 1 + left) / RASTER)
    return out


def polygons(segments: list[tuple[str, tuple[tuple[float, float], ...]]]) -> list[list[tuple[float, float]]]:
    """A recorded outline as polygons, with each curve sampled at 8 points."""
    contours: list[list[tuple[float, float]]] = []
    cur: list[tuple[float, float]] = []
    for op, pts in segments:
        if op == "moveTo":
            cur = [pts[0]]
        elif op == "lineTo":
            cur.append(pts[0])
        elif op in ("qCurveTo", "curveTo"):
            cur.extend(bezier([cur[-1], *pts], step / 8) for step in range(1, 9))
        elif op in ("closePath", "endPath"):
            contours.append(cur)
            cur = []
    return [c for c in contours if len(c) > 2]


def bezier(points: list[tuple[float, float]], t: float) -> tuple[float, float]:
    while len(points) > 1:
        points = [((1 - t) * a[0] + t * b[0], (1 - t) * a[1] + t * b[1]) for a, b in itertools.pairwise(points)]
    return points[0]


def separate(font: Font, glyphs: list[tuple[str, float]], size: float) -> list[tuple[str, float]]:
    """Push a glyph and everything after it right until its outline clears its neighbour's by 1% of the size."""
    out = list(glyphs)
    for i in range(1, len(out)):
        prev, cur = row_extents(font, out[i - 1][0], size), row_extents(font, out[i][0], size)
        shared = prev.keys() & cur.keys()
        if not shared:
            continue
        gap = min((out[i][1] + cur[r][0]) - (out[i - 1][1] + prev[r][1]) for r in shared)
        if gap < 0.01 * size:
            shift = 0.01 * size - gap
            out[i:] = [(g, x + shift) for g, x in out[i:]]
    return out


def width(font: Font, glyphs: list[tuple[str, float]], size: float) -> float:
    last, x = glyphs[-1]
    end = bounds(font, last, size)
    return x + (end[1] if end else 0)


def path(font: Font, text: str, x: float, y: float, size: float, fill: str, tracking: float = 0.0) -> str:
    """One <path> for a string with its baseline at (x, y)."""
    glyph_set = font.face.getGlyphSet()
    scale = size / font.upem
    pen = SVGPathPen(glyph_set, ntos=lambda v: f"{v:.1f}".rstrip("0").rstrip("."))
    for glyph, gx in shaped(font, text, size, tracking):
        glyph_set[glyph].draw(TransformPen(pen, (scale, 0, 0, -scale, x + gx, y)))
    return f'<path fill="{fill}" d="{pen.getCommands()}"/>'


def text_width(font: Font, text: str, size: float) -> float:
    return width(font, shaped(font, text, size), size)


def cap_height(font: Font, size: float) -> float:
    """The height of a flat-topped capital, measured on the "H" outline."""
    pen = BoundsPen(font.face.getGlyphSet())
    font.face.getGlyphSet()[font.face.getBestCmap()[ord("H")]].draw(pen)
    return pen.bounds[3] * size / font.upem


def logo(x: float, y: float, height: float) -> tuple[str, float]:
    """The logo, scaled so that its drawn outline, stroke included, is height tall with its top-left
    corner at (x, y). Returns it and its drawn width."""
    root = ET.parse(LOGO).getroot()
    paths = [el.get("d") for el in root.iter("{http://www.w3.org/2000/svg}path")]
    pen = BoundsPen(None)
    for d in paths:
        parse_path(d, pen)
    half = float(root.get("stroke-width")) / 2
    left, top, right, bottom = pen.bounds[0] - half, pen.bounds[1] - half, pen.bounds[2] + half, pen.bounds[3] + half
    scale = height / (bottom - top)
    style = " ".join(f'{a}="{root.get(a)}"' for a in ("fill", "stroke", "stroke-width", "stroke-linecap", "stroke-linejoin"))
    body = "".join(f'<path d="{d}"/>' for d in paths)
    group = f'<g transform="translate({x - left * scale:.2f} {y - top * scale:.2f}) scale({scale:.4f})" {style}>{body}</g>'
    return group, (right - left) * scale


BUTTONS_END = 12 + 2 * 13 + 3.2  # right edge of the third window button, measured from the window's left


def window(regular: Font, x: float, y: float, w: float, h: float, title: str, inner: str, body: str, clip: str) -> str:
    """A light macOS window: gray title bar, buttons and a centered title, inner clipped to the body."""
    bar, r = 24.0, 8.0
    tw = text_width(regular, title, 10)
    assert x + w / 2 - tw / 2 >= x + BUTTONS_END + 8, f"window title runs into the buttons: {title}"
    shape = f"M{x} {y + bar}H{x + w}V{y + h - r}a{r} {r} 0 0 1 -{r} {r}H{x + r}a{r} {r} 0 0 1 -{r} -{r}Z"
    parts = [
        f'<clipPath id="{clip}"><path d="{shape}"/></clipPath>',
        f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="{body}"/>',
        f'<rect x="{x}" y="{y}" width="{w}" height="{bar}" rx="{r}" fill="{BAR}"/>',
        f'<rect x="{x}" y="{y + bar - r}" width="{w}" height="{r}" fill="{BAR}"/>',
    ]
    for i, color in enumerate(("#FF5F57", "#FEBC2E", "#28C840")):
        parts.append(f'<circle cx="{x + 12 + i * 13:.1f}" cy="{y + bar / 2:.1f}" r="3.2" fill="{color}"/>')
    parts.append(path(regular, title, x + w / 2 - tw / 2, y + bar / 2 + 3.5, 10, LABEL))
    parts.append(f'<g clip-path="url(#{clip})">{inner}</g>')
    parts.append(f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{r}" fill="none" stroke="{CARD_BORDER}"/>')
    return "".join(parts)


PAD = 12  # padding inside a window and inside a bubble's window column
LINE = 12.5  # line height of chat text


def bubble(bold: Font, regular: Font, x: float, y: float, w: float, lines: list[tuple[str, str]], sent: bool) -> tuple[str, float]:
    """A chat bubble with (font, text) lines, right-aligned in the column when sent. Returns it and its height."""
    inner = 0.0
    for kind, text in lines:
        font = bold if kind == "bold" else regular
        inner = max(inner, text_width(font, text, 9.5))
    bw = inner + 2 * 8
    assert bw <= w, f"bubble too wide: {lines}"
    h = 8 + LINE * len(lines) + 2
    bx = x + w - bw if sent else x
    parts = [f'<rect x="{bx:.1f}" y="{y:.1f}" width="{bw:.1f}" height="{h:.1f}" rx="6" fill="{SENT if sent else CARD}"/>']
    for i, (kind, text) in enumerate(lines):
        font = bold if kind == "bold" else regular
        color = LABEL if kind == "bold" and not sent else TEXT
        parts.append(path(font, text, bx + 8, y + 8 + 9 + i * LINE, 9.5, color))
    return "".join(parts), h


def agent_line(bold: Font, regular: Font, x: float, y: float, label: str, text: str) -> str:
    """A sent message from the agent: its name in bold, then the text, on one bubble line."""
    return path(bold, label, x, y, 9.5, TEXT) + path(regular, text, x + text_width(bold, label, 9.5) + 3, y, 9.5, TEXT)


def build(font_dir: Path, mono_path: Path) -> str:
    bold = Font.load(font_dir / "YodelGrotesk-Bold.otf")
    regular = Font.load(font_dir / "YodelGrotesk-Regular.otf")
    mono = Font.load(mono_path)

    top = 56  # the windows are the tallest element; the same margin sits above and below them
    win_w, win_h, gap = 200, HEIGHT - 2 * top, 56
    term_x = WIDTH - 64 - win_w - 0.5  # the windows' border is centered on their edge; keep all of it inside
    chat_x = term_x - gap - win_w

    # The text block: the logo and the name on one line, then the tagline, all from the left margin,
    # centered from the top of the capitals to the last tagline baseline. A round mark looks smaller
    # than flat-topped capitals of the same height, so the logo is a little taller than the capitals
    # and centered on them.
    tracking = -5 * NAME_SIZE / 64
    cap = cap_height(bold, NAME_SIZE)
    tag_gap, tag_line = 50, 26  # name baseline to the first tagline baseline, and between tagline baselines
    baseline = HEIGHT / 2 + (cap - tag_gap - tag_line) / 2
    mark_h = 1.1 * cap
    mark, mark_w = logo(64, baseline - cap / 2 - mark_h / 2, mark_h)
    name_x = 64 + mark_w + cap / 4  # the "W" leans away from the logo, so the gap looks wider than this
    for line in TAGLINE:
        assert 64 + text_width(regular, line, 19) <= chat_x - 24, f"tagline too wide: {line}"
    assert name_x + width(bold, shaped(bold, NAME, NAME_SIZE, tracking), NAME_SIZE) <= chat_x - 24, "name too wide"

    # The group chat: a question, the owner waking the agent, and the agent's two messages.
    col_x, col_w = chat_x + PAD, win_w - 2 * PAD
    y = top + 24 + 10
    chat: list[str] = []
    question, h = bubble(bold, regular, col_x, y, col_w - 24, [("bold", "Sam"), ("text", "Do the totals in the"), ("text", "sheet add up?")], False)
    chat.append(question)
    y += h + 6
    wake, h = bubble(bold, regular, col_x, y, col_w, [("text", "Bob, wake up and check")], True)
    chat.append(wake)
    y += h + 6
    for words in (["Now monitoring this chat", "until 11:30."], ["The totals match the", "sheet: 48,200."]):
        # The agent prefix: the name in bold and a colon, then the message.
        h = 8 + LINE * 2 + 2
        bw = max(text_width(bold, "Bob:", 9.5) + 3 + text_width(regular, words[0], 9.5), text_width(regular, words[1], 9.5)) + 16
        assert bw <= col_w, f"agent bubble too wide: {words}"
        bx = col_x + col_w - bw
        chat.append(f'<rect x="{bx:.1f}" y="{y:.1f}" width="{bw:.1f}" height="{h:.1f}" rx="6" fill="{SENT}"/>')
        chat.append(agent_line(bold, regular, bx + 8, y + 17, "Bob:", words[0]))
        chat.append(path(regular, words[1], bx + 8, y + 17 + LINE, 9.5, TEXT))
        y += h + 6
    assert y <= top + win_h - 6, "the chat overflows its window"

    # The agent's Claude Code session: the channel event, then the tools it used.
    mono_size, mono_line = 8.6, 13.5
    term_lines = [
        ("● ", "whatsagent · Team", TEXT),
        ("  ", "Sam: Do the totals in", TERM_TEXT),
        ("  ", "the sheet add up?", TERM_TEXT),
        ("● ", "read_chat Team", TEXT),
        ("  └ ", "14 messages", TERM_DIM),
        ("● ", "send Team", TEXT),
        ("  └ ", "The totals match", TERM_DIM),
        ("    ", "the sheet: 48,200.", TERM_DIM),
    ]
    term: list[str] = []
    ty = top + 24 + PAD + 9
    for lead, text, color in term_lines:
        line = lead + text
        assert PAD + text_width(mono, line, mono_size) <= win_w - PAD, f"terminal line too wide: {line}"
        term.append(path(mono, line, term_x + PAD, ty, mono_size, color))
        ty += mono_line
    # Claude Code's input box and its footer, at the bottom of the window.
    footer = top + win_h - PAD
    box_top = footer - 30
    assert ty - mono_line + 6 <= box_top, "the terminal lines run into the input box"
    term.append(f'<path d="M{term_x + PAD} {box_top:.1f}H{term_x + win_w - PAD}M{term_x + PAD} {box_top + 18:.1f}H{term_x + win_w - PAD}" stroke="{CARD_BORDER}"/>')
    term.append(path(mono, ">", term_x + PAD, box_top + 12.5, mono_size, TEXT))
    term.append(path(mono, "›› auto mode on", term_x + PAD, footer, 7.6, TERM_DIM))

    # A two-way arrow in the middle of the gap.
    ax0, ax1, ay = chat_x + win_w + 14, term_x - 14, top + win_h / 2
    arrow = (
        f'<path d="M{ax0:.1f} {ay:.1f}H{ax1:.1f}" stroke="{SECONDARY}" stroke-width="1.6"/>'
        f'<path d="M{ax0 + 6:.1f} {ay - 5:.1f}L{ax0:.1f} {ay:.1f}L{ax0 + 6:.1f} {ay + 5:.1f}" fill="none" stroke="{SECONDARY}" stroke-width="1.6"/>'
        f'<path d="M{ax1 - 6:.1f} {ay - 5:.1f}L{ax1:.1f} {ay:.1f}L{ax1 - 6:.1f} {ay + 5:.1f}" fill="none" stroke="{SECONDARY}" stroke-width="1.6"/>'
    )

    label = " ".join(TAGLINE)
    parts = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{WIDTH}" height="{HEIGHT}" viewBox="0 0 {WIDTH} {HEIGHT}" '
        f'role="img" aria-label="WhatsAgent: {label}">',
        f"<title>WhatsAgent: {label}</title>",
        f'<rect width="{WIDTH}" height="{HEIGHT}" rx="12" fill="{BACKGROUND}"/>',
        mark,
        path(bold, NAME, name_x, baseline, NAME_SIZE, TEXT, tracking=tracking),
        path(regular, TAGLINE[0], 64, baseline + tag_gap, 19, SECONDARY),
        path(regular, TAGLINE[1], 64, baseline + tag_gap + tag_line, 19, SECONDARY),
        window(regular, chat_x, top, win_w, win_h, "Team", "".join(chat), WALLPAPER, "chat"),
        arrow,
        window(regular, term_x, top, win_w, win_h, "Claude Code", "".join(term), CARD, "term"),
        "</svg>",
    ]
    return "".join(parts) + "\n"


def main() -> int:
    font_dir = Path(sys.argv[1])
    mono = Path(sys.argv[2])
    out = ROOT / "assets" / "cover.svg"
    out.parent.mkdir(exist_ok=True)
    svg = build(font_dir, mono)
    assert "<text" not in svg and "font-family" not in svg, "delivered cover must ship no font references"
    out.write_text(svg, encoding="utf-8")
    print(out)
    for face in (font_dir / "YodelGrotesk-Bold.otf", font_dir / "YodelGrotesk-Regular.otf", mono):
        print(f"{face.name} sha256={hashlib.sha256(face.read_bytes()).hexdigest()}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
