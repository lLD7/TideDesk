"""Design tokens and the global stylesheet of the GammaGrid terminal.

THE AESTHETIC. A trading terminal rather than a web page: near-black ink
surfaces, hairline borders, monospaced tabular figures, and colour reserved
for meaning. Every pixel on the first screen is spent on the chain.

COLOUR CARRIES MEANING AND NOTHING ELSE.
  * POS  (mint)    — positive exposure: dealers long gamma, hedging damps moves.
  * NEG  (rose)    — negative exposure: dealers short gamma, hedging amplifies.
  * SPOT (amber)   — where the underlying is right now.
  * FLIP (azure)   — the gamma flip, the boundary between the two regimes.
  * VOL  (violet)  — volatility series, which carry no sign.
Red/green is deliberately avoided for the gamma sign so it never reads as
"price up / price down" (which also flips meaning between US and CN markets).

Constants rather than a stylesheet file: a Plotly figure and a CSS rule can then
use the very same value.
"""

from __future__ import annotations

# --- palette ---------------------------------------------------------------
BG = "#07090C"
PANEL = "#0D1117"
PANEL_2 = "#131922"
PANEL_3 = "#1A2230"
LINE = "#1F2733"
LINE_SOFT = "#161D27"

TEXT = "#E7EBF1"
MUTED = "#8C96A8"
FAINT = "#566173"

POS = "#2FD6A0"
POS_SOFT = "#7EEBC6"
POS_DEEP = "#0F5C46"
NEG = "#FF5C93"
NEG_SOFT = "#FF9CBF"
NEG_DEEP = "#6B1E3A"
SPOT = "#FFB547"
FLIP = "#6EA8FE"
VOL = "#A78BFA"
WARN = "#F2C14E"
ERROR = "#FF6B6B"

# Back-compatible names (app/weather.py draws its glyphs with these).
PRIMARY = NEG
PRIMARY_SOFT = NEG_SOFT
ACCENT = POS
ACCENT_SOFT = POS_SOFT
BACKGROUND = BG
SURFACE = PANEL
SURFACE_2 = PANEL_2
BORDER = LINE
GRID = LINE_SOFT
WARNING = WARN

# Diverging scale for exposure heatmaps: rose ← ink → mint. The centre is a
# touch lighter than the panel so a zero cell still reads as a cell.
EXPOSURE_SCALE = [
    [0.00, "#FF4F8B"],
    [0.18, "#C2346A"],
    [0.38, "#4A1A30"],
    [0.50, "#121821"],
    [0.62, "#103A2F"],
    [0.82, "#1C9C76"],
    [1.00, "#36E0A9"],
]

# --- type ------------------------------------------------------------------
FONT_UI = (
    "'Inter', -apple-system, BlinkMacSystemFont, 'PingFang SC', 'Hiragino Sans GB', "
    "'Microsoft YaHei', 'Segoe UI', Roboto, sans-serif"
)
FONT_MONO = (
    "'JetBrains Mono', 'SF Mono', ui-monospace, 'Cascadia Code', Menlo, Consolas, monospace"
)

RADIUS = "6px"


def stylesheet() -> str:
    """The one <style> block the page injects."""
    return f"""
<style>
  :root {{
    --gg-bg: {BG}; --gg-panel: {PANEL}; --gg-panel2: {PANEL_2}; --gg-line: {LINE};
    --gg-text: {TEXT}; --gg-muted: {MUTED}; --gg-faint: {FAINT};
    --gg-pos: {POS}; --gg-neg: {NEG}; --gg-spot: {SPOT}; --gg-flip: {FLIP};
    --gg-mono: {FONT_MONO};
  }}

  /* ---------- page chrome ---------- */
  .stApp {{
    background:
      radial-gradient(1200px 500px at 85% -10%, rgba(110,168,254,0.06), transparent 60%),
      radial-gradient(900px 400px at -10% 0%, rgba(47,214,160,0.05), transparent 55%),
      {BG};
  }}
  [data-testid="stHeader"] {{ background: transparent; height: 2.25rem; }}
  [data-testid="stMainBlockContainer"], .block-container {{
    padding-top: 0.9rem !important;
    padding-bottom: 2rem !important;
    padding-left: 1.6rem !important;
    padding-right: 1.6rem !important;
    max-width: 100% !important;
  }}
  [data-testid="stVerticalBlock"] {{ gap: 0.55rem; }}
  [data-testid="stSidebar"] {{ border-right: 1px solid {LINE}; }}
  [data-testid="stSidebar"] [data-testid="stVerticalBlock"] {{ gap: 0.45rem; }}
  [data-testid="stSidebarContent"] {{ padding-top: 0.25rem; }}
  h1, h2, h3, h4 {{ font-family: {FONT_UI} !important; letter-spacing: 0.01em; }}
  hr {{ margin: 0.4rem 0 !important; border-color: {LINE} !important; }}
  [data-testid="stCaptionContainer"] {{ color: {FAINT}; }}
  [data-testid="stDataFrame"], .gg-mono {{
    font-family: {FONT_MONO};
    font-variant-numeric: tabular-nums;
  }}

  /* ---------- segmented controls read as terminal tabs ---------- */
  [data-testid="stButtonGroup"] button {{
    font-family: {FONT_UI};
    font-size: 12.5px !important;
    letter-spacing: 0.02em;
    min-height: 30px !important;
    padding: 2px 14px !important;
  }}
  [data-testid="stButtonGroup"] button[kind="segmented_controlActive"],
  [data-testid="stButtonGroup"] button[kind="pillsActive"] {{
    background: {PANEL_3} !important;
    color: {TEXT} !important;
    border-color: #2C3A4F !important;
    box-shadow: inset 0 -2px 0 {FLIP};
  }}
  .gg-nav [data-testid="stButtonGroup"] button {{ min-height: 34px !important; padding: 4px 18px !important; }}

  /* ---------- brand ---------- */
  .gg-brand {{ display: flex; align-items: center; gap: 10px; height: 34px; }}
  .gg-brand-mark {{ display: grid; grid-template-columns: 9px 9px; grid-template-rows: 9px 9px; gap: 2px; }}
  .gg-brand-mark i {{ display: block; border-radius: 1.5px; }}
  .gg-brand-name {{ font-weight: 750; font-size: 15px; letter-spacing: 0.04em; color: {TEXT}; }}
  .gg-brand-name em {{ font-style: normal; color: {FAINT}; font-weight: 500; margin-left: 6px;
    font-size: 11px; letter-spacing: 0.16em; }}

  /* ---------- status pill ---------- */
  .gg-status {{
    display: flex; align-items: center; justify-content: flex-end; gap: 10px;
    font-size: 11.5px; color: {MUTED}; height: 34px; white-space: nowrap; overflow: hidden;
  }}
  .gg-status b {{ color: {TEXT}; font-weight: 600; font-family: {FONT_MONO}; }}
  .gg-status .gg-sep {{ color: {LINE}; }}
  .gg-dot {{ width: 7px; height: 7px; border-radius: 50%; display: inline-block; flex: none; }}
  .gg-dot.ok {{ background: {POS}; box-shadow: 0 0 0 3px rgba(47,214,160,0.15); }}
  .gg-dot.rest {{ background: {FAINT}; }}
  .gg-dot.warn {{ background: {WARN}; box-shadow: 0 0 0 3px rgba(242,193,78,0.15); }}
  .gg-dot.bad {{ background: {ERROR}; box-shadow: 0 0 0 3px rgba(255,107,107,0.18); }}
  .gg-tag {{
    font-family: {FONT_MONO}; font-size: 10.5px; padding: 1px 6px; border-radius: 4px;
    border: 1px solid {LINE}; color: {MUTED}; background: {PANEL};
  }}

  /* ---------- key-level ribbon ---------- */
  .gg-ribbon {{
    display: grid;
    grid-template-columns: minmax(210px, 1.6fr) repeat(auto-fit, minmax(96px, 1fr));
    border: 1px solid {LINE};
    border-radius: {RADIUS};
    background: linear-gradient(180deg, {PANEL} 0%, #0B0F14 100%);
    overflow: hidden;
  }}
  .gg-tile {{ padding: 8px 12px 8px; border-left: 1px solid {LINE_SOFT}; min-width: 0; }}
  .gg-tile:first-child {{ border-left: none; }}
  .gg-tile-k {{
    font-size: 10px; letter-spacing: 0.14em; text-transform: uppercase; color: {FAINT};
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  }}
  .gg-tile-v {{
    font-family: {FONT_MONO}; font-variant-numeric: tabular-nums;
    font-size: 17px; font-weight: 650; color: {TEXT}; line-height: 1.35; white-space: nowrap;
  }}
  .gg-tile-s {{ font-family: {FONT_MONO}; font-size: 10.5px; color: {MUTED}; white-space: nowrap;
    overflow: hidden; text-overflow: ellipsis; }}
  .gg-pos {{ color: {POS} !important; }}
  .gg-neg {{ color: {NEG} !important; }}
  .gg-spot {{ color: {SPOT} !important; }}
  .gg-flip {{ color: {FLIP} !important; }}
  .gg-vol {{ color: {VOL} !important; }}
  .gg-muted {{ color: {MUTED} !important; }}

  .gg-regime {{ display: flex; align-items: center; gap: 10px; position: relative; }}
  .gg-regime::before {{
    content: ""; position: absolute; left: 0; top: 0; bottom: 0; width: 3px;
  }}
  .gg-regime.pos::before {{ background: {POS}; }}
  .gg-regime.neg::before {{ background: {NEG}; }}
  .gg-regime svg {{ flex: none; }}
  .gg-regime-name {{ font-size: 15px; font-weight: 700; color: {TEXT}; line-height: 1.2; }}
  .gg-regime-name span {{ font-family: {FONT_MONO}; font-size: 10.5px; font-weight: 600;
    padding: 1px 6px; border-radius: 3px; margin-left: 6px; vertical-align: 2px; }}
  .gg-regime.pos .gg-regime-name span {{ background: rgba(47,214,160,0.14); color: {POS}; }}
  .gg-regime.neg .gg-regime-name span {{ background: rgba(255,92,147,0.14); color: {NEG}; }}
  .gg-regime-sub {{ font-size: 11px; color: {MUTED}; line-height: 1.35; }}

  /* ---------- section headers / panels ---------- */
  .gg-h {{
    display: flex; align-items: baseline; gap: 10px; margin: 0.2rem 0 -0.15rem;
    border-bottom: 1px solid {LINE_SOFT}; padding-bottom: 5px;
  }}
  .gg-h b {{ font-size: 12.5px; letter-spacing: 0.08em; color: {TEXT}; font-weight: 650; }}
  .gg-h span {{ font-size: 11px; color: {FAINT}; font-family: {FONT_MONO}; }}
  .gg-h .gg-h-right {{ margin-left: auto; }}

  .gg-empty {{
    border: 1px dashed #263041; border-radius: {RADIUS}; padding: 18px 18px;
    background: rgba(13,17,23,0.6); color: {MUTED}; font-size: 12.5px;
  }}
  .gg-empty b {{ display: block; color: {TEXT}; font-size: 13px; margin-bottom: 4px; font-weight: 600; }}
  .gg-empty .gg-meter {{ margin-top: 10px; height: 4px; background: {PANEL_3}; border-radius: 2px; overflow: hidden; }}
  .gg-empty .gg-meter i {{ display: block; height: 100%; background: {FLIP}; }}

  .gg-banner {{
    border: 1px solid; border-radius: {RADIUS}; padding: 6px 12px; font-size: 12px;
    display: flex; gap: 8px; align-items: center;
  }}
  .gg-banner.warn {{ border-color: rgba(242,193,78,0.35); background: rgba(242,193,78,0.06); color: #F5D78A; }}
  .gg-banner.bad {{ border-color: rgba(255,107,107,0.35); background: rgba(255,107,107,0.06); color: #FFB0B0; }}
  .gg-banner.info {{ border-color: {LINE}; background: {PANEL}; color: {MUTED}; }}

  /* chip strip under a chart */
  .gg-chips {{ display: flex; flex-wrap: wrap; gap: 6px 14px; font-size: 11px; color: {MUTED}; }}
  .gg-chips b {{ font-family: {FONT_MONO}; color: {TEXT}; font-weight: 600; }}
  .gg-key {{ display: inline-block; width: 10px; height: 2px; vertical-align: middle; margin-right: 5px; }}

  /* contract header */
  .gg-contract {{ display: flex; flex-wrap: wrap; align-items: baseline; gap: 6px 22px;
    padding: 10px 14px; border: 1px solid {LINE}; border-radius: {RADIUS}; background: {PANEL}; }}
  .gg-contract .gg-cname {{ font-family: {FONT_MONO}; font-size: 16px; font-weight: 700; color: {TEXT}; }}
  .gg-contract dl {{ display: flex; gap: 18px; margin: 0; flex-wrap: wrap; }}
  .gg-contract dt {{ font-size: 10px; letter-spacing: 0.12em; color: {FAINT}; text-transform: uppercase; }}
  .gg-contract dd {{ margin: 0; font-family: {FONT_MONO}; font-size: 13.5px; color: {TEXT}; }}

  /* the day-over-day ladder */
  table.gg-ladder {{ width: 100%; border-collapse: collapse; font-size: 12.5px; }}
  table.gg-ladder th, table.gg-ladder td {{ border: none; }}
  table.gg-ladder th {{
    text-align: left; font-size: 10px; letter-spacing: 0.14em; text-transform: uppercase;
    color: {FAINT}; font-weight: 600; padding: 0 10px 6px 0; border-bottom: 1px solid {LINE};
  }}
  table.gg-ladder td {{ padding: 6px 10px 6px 0; border-bottom: 1px solid {LINE_SOFT}; }}
  table.gg-ladder td.n {{ font-family: {FONT_MONO}; font-variant-numeric: tabular-nums; }}
  table.gg-ladder td.b {{ color: {MUTED}; }}
  table.gg-ladder .note {{ display: block; font-size: 10.5px; color: {FAINT}; }}

  /* compact widgets */
  [data-testid="stWidgetLabel"] p {{ font-size: 11px !important; color: {MUTED}; letter-spacing: 0.04em; }}
  [data-testid="stSlider"] {{ padding-top: 0; }}
  [data-testid="stExpander"] details {{ border-color: {LINE} !important; background: {PANEL}; }}
  [data-testid="stPopoverButton"] {{ min-height: 32px; }}
  [data-testid="stSidebar"] button[kind="secondary"] {{ justify-content: flex-start; }}
  .gg-side-h {{ font-size: 10px; letter-spacing: 0.18em; text-transform: uppercase; color: {FAINT};
    margin: 0.5rem 0 -0.2rem; }}
</style>
"""
