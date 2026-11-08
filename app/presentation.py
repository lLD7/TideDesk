"""Shared text formatting without the browser plotting stack."""
from __future__ import annotations

import html

import numpy as np
import pandas as pd

from app import metrics

WEEKDAYS_CN = "一二三四五六日"

# Regime words, in Chinese. Same five states as app/weather.py.
WEATHER_CN = {
    metrics.WEATHER_CLEAR: ("晴朗", "正 Gamma，翻转点远 — 做市商对冲在压制波动"),
    metrics.WEATHER_FAIR: ("多云", "正 Gamma，但翻转点已在射程内 — 压制可能失效"),
    metrics.WEATHER_UNSETTLED: ("临界", "价格贴近 Gamma 翻转点 — 状态随时可能切换"),
    metrics.WEATHER_RAIN: ("阵雨", "负 Gamma，翻转点较近 — 波动暂时被放大"),
    metrics.WEATHER_STORM: ("风暴", "负 Gamma，附近无边界 — 做市商对冲放大波动"),
}

DIMENSION_CN = {"gex": "GEX", "vanna": "Vanna", "charm": "Charm"}
UNIT_CN = {
    "gex": "USD / 1% 标的变动",
    "vanna": "USD / 1 vol 点",
    "charm": "USD / 日",
}


def compact(value, signed: bool = False, digits: int = 2) -> str:
    """1_234_567 → 1.23M. Units follow the number's own size."""
    if value is None or (isinstance(value, float) and not np.isfinite(value)) or pd.isna(value):
        return "—"
    value = float(value)
    sign = "+" if signed and value > 0 else ("-" if value < 0 else "")
    magnitude = abs(value)
    for limit, suffix in ((1e12, "T"), (1e9, "B"), (1e6, "M"), (1e3, "K")):
        if magnitude >= limit:
            return f"{sign}{magnitude / limit:.{digits}f}{suffix}"
    return f"{sign}{magnitude:.0f}"


def cell_compact(value) -> str:
    """Shorter variant for heatmap cells: one decimal, no plus sign."""
    if value is None or pd.isna(value):
        return ""
    if value == 0:
        return "·"
    return compact(value, digits=1)


def price(value, digits: int = 2) -> str:
    if value is None or pd.isna(value):
        return "—"
    return f"{float(value):,.{digits}f}"


def strike_label(value) -> str:
    return "—" if value is None or pd.isna(value) else f"{float(value):g}"


def pct(value, digits: int = 1, signed: bool = False) -> str:
    if value is None or pd.isna(value):
        return "—"
    return f"{float(value) * 100:{'+' if signed else ''}.{digits}f}%"


def expiry_short(expiry) -> str:
    stamp = pd.Timestamp(expiry)
    return f"{stamp:%m-%d} {WEEKDAYS_CN[stamp.weekday()]}"


def dte(expiry, moment) -> int:
    return int((pd.Timestamp(expiry).normalize() - pd.Timestamp(moment).normalize()).days)


def tone(value) -> str:
    if value is None or pd.isna(value):
        return ""
    return "gg-pos" if value >= 0 else "gg-neg"


def esc(text) -> str:
    return html.escape(str(text), quote=True)


