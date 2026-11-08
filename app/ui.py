"""Presentation building blocks of the terminal: formatters, HTML components
and Plotly figure builders. No database access and no Streamlit calls live
here — dashboard.py decides what to show, this decides how it looks.
"""

from __future__ import annotations

import numpy as np
import pandas as pd
import plotly.graph_objects as go
from plotly.subplots import make_subplots

from app import theme
from app import weather as weather_words
from app.presentation import (
    DIMENSION_CN as DIMENSION_CN,
)
from app.presentation import (
    UNIT_CN as UNIT_CN,
)
from app.presentation import (
    WEATHER_CN as WEATHER_CN,
)

# --------------------------------------------------------------------------
# formatting
# --------------------------------------------------------------------------
from app.presentation import (
    WEEKDAYS_CN as WEEKDAYS_CN,
)
from app.presentation import (
    cell_compact as cell_compact,
)
from app.presentation import (
    compact as compact,
)
from app.presentation import (
    dte as dte,
)
from app.presentation import (
    esc as esc,
)
from app.presentation import (
    expiry_short as expiry_short,
)
from app.presentation import (
    pct as pct,
)
from app.presentation import (
    price as price,
)
from app.presentation import (
    strike_label as strike_label,
)
from app.presentation import (
    tone as tone,
)


# --------------------------------------------------------------------------
# HTML components
# --------------------------------------------------------------------------
def brand() -> str:
    return (
        '<div class="gg-brand"><div class="gg-brand-mark">'
        f'<i style="background:{theme.NEG}"></i><i style="background:{theme.PANEL_3}"></i>'
        f'<i style="background:{theme.PANEL_3}"></i><i style="background:{theme.POS}"></i>'
        '</div><div class="gg-brand-name">GAMMAGRID<em>TERMINAL</em></div></div>'
    )


def status(items: list[str], dot: str = "ok", tip: str = "") -> str:
    sep = '<span class="gg-sep">│</span>'
    return (
        f'<div class="gg-status" title="{esc(tip)}"><span class="gg-dot {dot}"></span>'
        + sep.join(items) + "</div>"
    )


def tile(label: str, value: str, sub: str = "", value_class: str = "", tip: str = "") -> str:
    title = f' title="{esc(tip)}"' if tip else ""
    sub_html = f'<div class="gg-tile-s">{sub}</div>' if sub else '<div class="gg-tile-s">&nbsp;</div>'
    return (
        f'<div class="gg-tile"{title}><div class="gg-tile-k">{label}</div>'
        f'<div class="gg-tile-v {value_class}">{value}</div>{sub_html}</div>'
    )


def regime_tile(reading: dict | None) -> str:
    if not reading:
        return tile("Gamma 状态", "—", "数据不足")
    name, sentence = WEATHER_CN.get(reading["state"], (reading["state"], ""))
    positive = reading["net_gex"] >= 0
    if reading.get("gamma_flip") is None:
        sentence = ("正 Gamma · 对冲压制波动" if positive else "负 Gamma · 对冲放大波动") + " · 25% 内无翻转点"
    art = weather_words._ART.get(reading["state"], "")
    scope = ""
    if reading.get("horizon_dte") is not None:
        scope = f"近 {int(reading['horizon_dte'])} 天 · {int(reading['expiry_count'])} 个到期日"
    return (
        f'<div class="gg-tile gg-regime {"pos" if positive else "neg"}" title="{esc(sentence)}">'
        f'<svg width="34" height="34" viewBox="0 0 48 48" aria-hidden="true">{art}</svg>'
        f'<div style="min-width:0"><div class="gg-regime-name">{name}'
        f'<span>{"+γ" if positive else "−γ"}</span></div>'
        f'<div class="gg-regime-sub">{esc(sentence)}</div>'
        f'<div class="gg-tile-k" style="margin-top:2px">{esc(scope)}</div></div></div>'
    )


def ribbon(tiles: list[str]) -> str:
    return f'<div class="gg-ribbon">{"".join(tiles)}</div>'


def header(title: str, meta: str = "", right: str = "") -> str:
    right_html = f'<span class="gg-h-right">{right}</span>' if right else ""
    meta_html = f"<span>{meta}</span>" if meta else ""
    return f'<div class="gg-h"><b>{title}</b>{meta_html}{right_html}</div>'


def empty(title: str, body: str = "", progress: float | None = None) -> str:
    meter = ""
    if progress is not None:
        meter = f'<div class="gg-meter"><i style="width:{max(4, min(100, progress * 100)):.0f}%"></i></div>'
    return f'<div class="gg-empty"><b>{title}</b>{body}{meter}</div>'


def banner(text: str, kind: str = "warn") -> str:
    return f'<div class="gg-banner {kind}">{text}</div>'


def chips(items: list[tuple[str, str]], keys: dict[str, str] | None = None) -> str:
    keys = keys or {}
    parts = []
    for label, value in items:
        key = keys.get(label)
        swatch = f'<span class="gg-key" style="background:{key}"></span>' if key else ""
        parts.append(f"<span>{swatch}{label} <b>{value}</b></span>")
    return f'<div class="gg-chips">{"".join(parts)}</div>'


# --------------------------------------------------------------------------
# Plotly
# --------------------------------------------------------------------------
PLOT_CONFIG = {"displayModeBar": False, "displaylogo": False, "scrollZoom": False}


def _axis(**extra) -> dict:
    base = dict(
        gridcolor=theme.LINE_SOFT, zerolinecolor=theme.LINE, linecolor=theme.LINE,
        tickfont=dict(family=theme.FONT_MONO, size=10, color=theme.MUTED),
        title=dict(font=dict(size=11, color=theme.FAINT)),
    )
    base.update(extra)
    return base


def styled(fig: go.Figure, height: int, legend: bool = False, margin: dict | None = None) -> go.Figure:
    fig.update_layout(
        height=height,
        paper_bgcolor="rgba(0,0,0,0)",
        plot_bgcolor="rgba(0,0,0,0)",
        font=dict(family=theme.FONT_UI, size=11, color=theme.MUTED),
        margin=margin or dict(l=8, r=8, t=10, b=8),
        showlegend=legend,
        legend=dict(orientation="h", y=1.02, yanchor="bottom", x=0, bgcolor="rgba(0,0,0,0)",
                    font=dict(size=10.5, color=theme.MUTED)),
        hoverlabel=dict(bgcolor=theme.PANEL_2, bordercolor=theme.LINE,
                        font=dict(family=theme.FONT_MONO, size=11, color=theme.TEXT)),
        hovermode="x unified" if legend else "closest",
    )
    fig.update_xaxes(**_axis())
    fig.update_yaxes(**_axis())
    return fig


def _hover_matrix(*parts) -> np.ndarray:
    return np.dstack(parts)


def exposure_workbench(
    band: pd.DataFrame,
    totals: pd.Series,
    moment,
    spot: float,
    dimension: str,
    call_band: pd.DataFrame | None = None,
    put_band: pd.DataFrame | None = None,
    oi_call: pd.DataFrame | None = None,
    oi_put: pd.DataFrame | None = None,
    volume: pd.DataFrame | None = None,
    flip: float | None = None,
    levels: dict | None = None,
    expected: dict | None = None,
) -> go.Figure:
    """Heatmap (strike × expiry) with its Total row on top and the strike
    profile on the right, sharing one strike axis.

    The strike axis is an index (0..n-1) rather than the strikes themselves so
    that every row is the same height whatever the strike spacing, and the
    spot and flip lines can sit between two rows at their true position.
    """
    levels = levels or {}
    strikes = np.sort(band.index.to_numpy(dtype=float))
    expiries = list(band.columns)
    n, m = len(strikes), len(expiries)
    z = band.reindex(index=strikes).to_numpy(dtype=float)
    ys = np.arange(n)
    xs = [f"{expiry_short(e)}<br><span style='color:{theme.FAINT}'>{dte(e, moment)}D</span>" for e in expiries]

    finite = np.abs(z[np.isfinite(z) & (z != 0)])
    vmax = float(np.percentile(finite, 92)) if finite.size else 1.0
    vmax = vmax or 1.0

    def aligned(frame):
        if frame is None:
            return np.full((n, m), np.nan)
        return frame.reindex(index=strikes, columns=expiries).to_numpy(dtype=float)

    c_exp, p_exp = aligned(call_band), aligned(put_band)
    c_oi, p_oi, vol = aligned(oi_call), aligned(oi_put), aligned(volume)

    fmt = np.vectorize(lambda v: compact(v, signed=True) if np.isfinite(v) else "—", otypes=[object])
    fmt_int = np.vectorize(lambda v: f"{v:,.0f}" if np.isfinite(v) else "—", otypes=[object])
    strike_grid = np.tile(np.array([f"{s:g}" for s in strikes], dtype=object).reshape(-1, 1), (1, m))
    custom = _hover_matrix(strike_grid, fmt(z), fmt(c_exp), fmt(p_exp), fmt_int(c_oi), fmt_int(p_oi), fmt_int(vol))
    name = DIMENSION_CN[dimension]

    cell_text = np.vectorize(cell_compact, otypes=[object])(z) if m <= 18 and n <= 90 else None

    row_px = 20 if n <= 45 else 15
    total_px = 30
    top_px = 44
    main_px = n * row_px
    height = top_px + total_px + 6 + main_px + 26

    fig = make_subplots(
        rows=2, cols=2,
        column_widths=[0.80, 0.20],
        row_heights=[total_px / (total_px + main_px), main_px / (total_px + main_px)],
        horizontal_spacing=0.008, vertical_spacing=6 / height,
        shared_xaxes="columns", shared_yaxes="rows",
    )

    # Total row — every strike of the shown expiries, not just the band.
    tot = np.array([totals.get(e, np.nan) for e in expiries], dtype=float).reshape(1, -1)
    tmax = float(np.nanmax(np.abs(tot))) if np.isfinite(tot).any() else 1.0
    fig.add_trace(go.Heatmap(
        z=tot, x=xs, y=["合计"], colorscale=theme.EXPOSURE_SCALE, zmid=0,
        zmin=-(tmax or 1), zmax=(tmax or 1), showscale=False, xgap=1, ygap=1,
        text=np.vectorize(lambda v: compact(v, signed=True), otypes=[object])(tot),
        texttemplate="<b>%{text}</b>", textfont=dict(family=theme.FONT_MONO, size=10.5, color=theme.TEXT),
        hovertemplate=f"%{{x}}<br>{name} 合计 %{{text}}<extra></extra>",
    ), row=1, col=1)

    fig.add_trace(go.Heatmap(
        z=z, x=xs, y=ys, colorscale=theme.EXPOSURE_SCALE, zmid=0, zmin=-vmax, zmax=vmax,
        showscale=False, xgap=1, ygap=1, customdata=custom,
        text=cell_text, texttemplate="%{text}" if cell_text is not None else None,
        textfont=dict(family=theme.FONT_MONO, size=9.5, color="rgba(231,235,241,0.92)"),
        hovertemplate=(
            "<b>K %{customdata[0]}</b>  ·  %{x}<br>"
            f"{name} 净 %{{customdata[1]}}<br>"
            "Call %{customdata[2]}   Put %{customdata[3]}<br>"
            "OI  C %{customdata[4]} / P %{customdata[5]}<br>"
            "成交量 %{customdata[6]}<extra></extra>"
        ),
    ), row=2, col=1)

    profile = np.nansum(np.where(np.isfinite(z), z, 0), axis=1)
    fig.add_trace(go.Bar(
        x=profile, y=ys, orientation="h",
        marker=dict(color=np.where(profile >= 0, theme.POS, theme.NEG), line=dict(width=0)),
        opacity=0.9,
        customdata=np.array([f"{s:g}" for s in strikes]),
        text=np.vectorize(lambda v: compact(v, signed=True), otypes=[object])(profile),
        hovertemplate=f"K %{{customdata}}<br>{name} 合计 %{{text}}<extra></extra>",
        textposition="none",
    ), row=2, col=2)

    grand = float(np.nansum(tot))
    fig.add_annotation(
        x=0.5, y=0.5, xref="x2 domain", yref="y2 domain", showarrow=False,
        text=f"<span style='color:{theme.FAINT}'>Σ</span> <b>{compact(grand, signed=True)}</b>",
        font=dict(family=theme.FONT_MONO, size=12, color=theme.POS if grand >= 0 else theme.NEG),
    )

    # Spot, flip and the 1σ band, at their true position between rows.
    def position(value):
        if value is None or not np.isfinite(value) or n < 2:
            return None
        if value < strikes[0] or value > strikes[-1]:
            return None
        return float(np.interp(value, strikes, ys))

    spot_y = position(spot)
    if expected:
        lo, hi = position(expected.get("lower")), position(expected.get("upper"))
        lo = -0.5 if lo is None and expected.get("lower", 0) < strikes[0] else lo
        hi = n - 0.5 if hi is None and expected.get("upper", 0) > strikes[-1] else hi
        if lo is not None and hi is not None:
            fig.add_shape(type="rect", xref="x4 domain", yref="y4", x0=0, x1=1, y0=lo, y1=hi,
                          fillcolor="rgba(255,181,71,0.06)", line=dict(width=0), layer="below")
    for value, color, dash, label in (
        (spot_y, theme.SPOT, "solid", f"现价 {spot:,.2f}"),
        (position(flip), theme.FLIP, "dash", f"翻转 {flip:,.2f}" if flip else ""),
    ):
        if value is None:
            continue
        for axis in ("x3", "x4"):
            fig.add_shape(type="line", xref=f"{axis} domain", yref=axis.replace("x", "y"),
                          x0=0, x1=1, y0=value, y1=value, line=dict(color=color, width=1.4, dash=dash))
        fig.add_annotation(x=1, y=value, xref="x4 domain", yref="y4", text=label, showarrow=False,
                           xanchor="right", yanchor="bottom", font=dict(size=10, color=color, family=theme.FONT_MONO),
                           bgcolor="rgba(7,9,12,0.75)")

    # Strike labels carry the levels: ATM bold amber, walls tagged.
    atm = strikes[np.abs(strikes - spot).argmin()] if n else None
    marks = {}
    for key, glyph, color in (
        ("call_gex_wall", "◆C", theme.POS), ("put_gex_wall", "◆P", theme.NEG),
        ("call_wall", "▲", theme.POS_SOFT), ("put_wall", "▼", theme.NEG_SOFT),
        ("max_pain", "✕", theme.VOL),
    ):
        value = levels.get(key)
        if value is not None:
            marks.setdefault(float(value), []).append(f"<span style='color:{color}'>{glyph}</span>")
    ticktext = []
    for s in strikes:
        tags = " ".join(marks.get(float(s), []))
        label = f"<b style='color:{theme.SPOT}'>{s:g}</b>" if s == atm else f"{s:g}"
        ticktext.append(f"{tags} {label}" if tags else label)

    fig = styled(fig, height, margin=dict(l=4, r=4, t=top_px, b=26))
    fig.update_xaxes(side="top", showgrid=False, tickfont=dict(size=10, color=theme.MUTED), row=1, col=1)
    fig.update_xaxes(showticklabels=False, showgrid=False, row=2, col=1)
    fig.update_xaxes(visible=False, row=1, col=2)
    fig.update_xaxes(showgrid=True, gridcolor=theme.LINE_SOFT, zeroline=True, zerolinecolor=theme.FAINT,
                     tickformat="~s", nticks=4, row=2, col=2)
    fig.update_yaxes(showgrid=False, tickfont=dict(size=10.5, color=theme.MUTED), row=1, col=1)
    fig.update_yaxes(visible=False, row=1, col=2)
    fig.update_yaxes(tickmode="array", tickvals=ys, ticktext=ticktext, showgrid=False,
                     range=[-0.5, n - 0.5], row=2, col=1)
    fig.update_yaxes(showgrid=False, showticklabels=False, range=[-0.5, n - 0.5], row=2, col=2)
    fig.update_layout(bargap=0.18, hovermode="closest")
    return fig


def legend_strip(dimension: str) -> str:
    return chips(
        [("现价", ""), ("Gamma 翻转", ""), ("±1σ 预期区间", ""),
         ("◆C/◆P GEX 墙", ""), ("▲/▼ OI 墙", ""), ("✕ Max Pain", "")],
        keys={"现价": theme.SPOT, "Gamma 翻转": theme.FLIP, "±1σ 预期区间": "rgba(255,181,71,0.35)"},
    )


def line_history(frame: pd.DataFrame, series: dict[str, tuple[str, str]], height: int = 220,
                 percent: bool = False) -> go.Figure:
    """Time series with markers (history is often only a few points)."""
    fig = go.Figure()
    for column, (label, color) in series.items():
        if column not in frame:
            continue
        fig.add_trace(go.Scatter(
            x=frame.index, y=frame[column], name=label, mode="lines+markers",
            line=dict(color=color, width=1.8), marker=dict(size=5),
        ))
    fig = styled(fig, height, legend=len(series) > 1, margin=dict(l=8, r=8, t=28 if len(series) > 1 else 8, b=8))
    if percent:
        fig.update_yaxes(tickformat=".0%")
    return fig


def skew_figure(curves: list[tuple[str, pd.DataFrame]], spot: float, height: int = 330) -> go.Figure:
    palette = [theme.VOL, theme.FLIP, theme.POS, theme.SPOT, theme.NEG_SOFT, "#C3CAD6"]
    fig = go.Figure()
    for i, (label, frame) in enumerate(curves):
        fig.add_trace(go.Scatter(
            x=frame["strike"], y=frame["iv"], name=label, mode="lines",
            line=dict(color=palette[i % len(palette)], width=1.8, shape="spline", smoothing=0.6),
            hovertemplate="K %{x:g}  IV %{y:.1%}<extra>" + label + "</extra>",
        ))
    fig.add_vline(x=spot, line=dict(color=theme.SPOT, width=1, dash="dot"))
    fig = styled(fig, height, legend=True, margin=dict(l=8, r=8, t=30, b=8))
    fig.update_layout(hovermode="closest")
    fig.update_yaxes(tickformat=".0%")
    fig.update_xaxes(title_text="行权价")
    return fig


def term_figure(term: pd.DataFrame, height: int = 330) -> go.Figure:
    fig = make_subplots(specs=[[{"secondary_y": True}]])
    fig.add_trace(go.Bar(
        x=term["label"], y=term["move_pct"], name="±1σ 预期波动",
        marker=dict(color="rgba(255,181,71,0.28)", line=dict(color=theme.SPOT, width=1)),
        hovertemplate="%{x}<br>±1σ %{y:.2f}%<extra></extra>",
    ), secondary_y=True)
    fig.add_trace(go.Scatter(
        x=term["label"], y=term["atm_iv"], name="ATM IV", mode="lines+markers",
        line=dict(color=theme.VOL, width=2), marker=dict(size=5),
        hovertemplate="%{x}<br>ATM IV %{y:.1%}<extra></extra>",
    ), secondary_y=False)
    fig = styled(fig, height, legend=True, margin=dict(l=8, r=8, t=30, b=8))
    fig.update_layout(hovermode="x unified")
    fig.update_yaxes(tickformat=".0%", secondary_y=False)
    fig.update_yaxes(ticksuffix="%", showgrid=False, secondary_y=True)
    fig.update_xaxes(type="category", tickangle=0, nticks=12)
    return fig


def surface_figure(points: pd.DataFrame, grid, height: int = 460) -> go.Figure:
    grid_strikes, grid_years, grid_iv = grid
    real = np.sort(points["strike"].unique())
    nearest = np.array([f"{real[np.abs(real - gx).argmin()]:g}" for gx in grid_strikes])
    dte_rows = np.round(grid_years * 365).astype(int).astype(str)
    custom = np.dstack([np.tile(nearest, (len(grid_years), 1)),
                        np.tile(dte_rows.reshape(-1, 1), (1, len(grid_strikes)))])
    fig = go.Figure(go.Surface(
        x=grid_strikes, y=grid_years, z=grid_iv, customdata=custom,
        colorscale=[[0, "#1B2A4A"], [0.35, "#3B4FA8"], [0.65, theme.VOL], [1, "#FFD08A"]],
        showscale=False, opacity=0.96,
        contours=dict(z=dict(show=True, usecolormap=True, project_z=False, width=1)),
        hovertemplate="K≈%{customdata[0]}<br>DTE %{customdata[1]}d<br>IV %{z:.1%}<extra></extra>",
    ))
    ticks = points[["years_to_expiry", "expiry"]].drop_duplicates().sort_values("years_to_expiry")
    if len(ticks) > 8:
        ticks = ticks.iloc[np.linspace(0, len(ticks) - 1, 8).round().astype(int)]
    axis = dict(backgroundcolor="rgba(0,0,0,0)", gridcolor=theme.LINE, zerolinecolor=theme.LINE,
                tickfont=dict(family=theme.FONT_MONO, size=9.5, color=theme.MUTED),
                title=dict(font=dict(size=10.5, color=theme.FAINT)))
    fig.update_layout(
        scene=dict(
            xaxis=dict(axis, title="行权价"),
            yaxis=dict(axis, title="到期", tickmode="array",
                       tickvals=ticks["years_to_expiry"].tolist(),
                       ticktext=[pd.Timestamp(d).strftime("%m-%d") for d in ticks["expiry"]]),
            zaxis=dict(axis, title="IV", tickformat=".0%"),
            camera=dict(eye=dict(x=1.55, y=-1.55, z=0.85)),
            aspectmode="manual", aspectratio=dict(x=1.3, y=1.1, z=0.65),
        ),
    )
    return styled(fig, height, margin=dict(l=0, r=0, t=0, b=0))


def waterfall_figure(start: float, steps: list[float], height: int = 280) -> go.Figure:
    labels = ["起点", "Delta", "Gamma", "Vega", "Theta", "残差", "终点"]
    text = [f"{start:.1f}¢", *(f"{v:+.1f}¢" for v in steps), f"{start + sum(steps):.1f}¢"]
    fig = go.Figure(go.Waterfall(
        orientation="v", measure=["absolute", *["relative"] * 5, "total"], x=labels,
        y=[start, *steps, 0], text=text, textposition="outside",
        textfont=dict(family=theme.FONT_MONO, size=10, color=theme.TEXT),
        decreasing={"marker": {"color": theme.NEG}}, increasing={"marker": {"color": theme.POS}},
        totals={"marker": {"color": "#56627A"}}, connector={"line": {"color": theme.LINE}},
    ))
    fig = styled(fig, height, margin=dict(l=8, r=8, t=18, b=8))
    fig.update_yaxes(title_text="¢ / 股")
    return fig


def price_iv_figure(history: pd.DataFrame, height: int = 280) -> go.Figure:
    fig = make_subplots(specs=[[{"secondary_y": True}]])
    fig.add_trace(go.Scatter(x=history.index, y=history["last_price"], name="期权价格",
                             mode="lines+markers", line=dict(color=theme.TEXT, width=1.8), marker=dict(size=4)),
                  secondary_y=False)
    fig.add_trace(go.Scatter(x=history.index, y=history["implied_volatility"], name="IV",
                             mode="lines+markers", line=dict(color=theme.VOL, width=1.6, dash="dot"),
                             marker=dict(size=4)), secondary_y=True)
    fig = styled(fig, height, legend=True, margin=dict(l=8, r=8, t=28, b=8))
    fig.update_yaxes(tickformat=".0%", showgrid=False, secondary_y=True)
    return fig


def greeks_grid(history: pd.DataFrame, height: int = 300) -> go.Figure:
    names = ["delta", "gamma", "theta", "vega", "rho", "vanna", "charm"]
    names = [g for g in names if g in history]
    cols = 4
    rows = int(np.ceil(len(names) / cols))
    fig = make_subplots(rows=rows, cols=cols, subplot_titles=[g.capitalize() for g in names],
                        vertical_spacing=0.2, horizontal_spacing=0.05)
    palette = [theme.POS, theme.SPOT, theme.NEG_SOFT, theme.VOL, theme.FLIP, theme.POS_SOFT, "#C3CAD6"]
    for i, greek in enumerate(names):
        fig.add_trace(go.Scatter(x=history.index, y=history[greek], mode="lines+markers",
                                 line=dict(color=palette[i], width=1.5), marker=dict(size=3.5), name=greek),
                      row=i // cols + 1, col=i % cols + 1)
    fig = styled(fig, height, margin=dict(l=8, r=8, t=24, b=8))
    fig.update_annotations(font=dict(size=10.5, color=theme.MUTED))
    fig.update_xaxes(showticklabels=False)
    fig.update_yaxes(tickformat=".3~g", nticks=4)
    return fig


def oi_delta_figure(by_strike: pd.DataFrame, spot: float, height: int = 330) -> go.Figure:
    fig = go.Figure()
    for side, color in (("call", theme.POS), ("put", theme.NEG)):
        part = by_strike[by_strike["option_type"] == side]
        fig.add_trace(go.Bar(x=part["strike"], y=part["oi_delta"], name=side.title(),
                             marker=dict(color=color), opacity=0.85,
                             hovertemplate="K %{x:g}<br>ΔOI %{y:+,.0f}<extra>" + side + "</extra>"))
    fig.add_vline(x=spot, line=dict(color=theme.SPOT, width=1, dash="dot"))
    fig = styled(fig, height, legend=True, margin=dict(l=8, r=8, t=28, b=8))
    fig.update_layout(barmode="relative", bargap=0.15, hovermode="closest")
    return fig


def profile_compare_figure(frame: pd.DataFrame, spot: float, height: int = 300) -> go.Figure:
    fig = go.Figure()
    colors = [theme.FLIP, "#56627A"]
    for i, column in enumerate(frame.columns):
        fig.add_trace(go.Bar(x=[f"{s:g}" for s in frame.index], y=frame[column], name=str(column),
                             marker=dict(color=colors[i % 2]), opacity=1 if i == 0 else 0.7))
    fig = styled(fig, height, legend=True, margin=dict(l=8, r=8, t=28, b=8))
    fig.update_layout(barmode="group", bargap=0.2, hovermode="x unified")
    fig.update_yaxes(tickformat="~s")
    fig.update_xaxes(type="category")
    return fig
