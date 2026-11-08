"""Python calculation adapter for the native MyGo application.

In native mode Go owns storage and scheduling; this process is read-only and
serves one numerical request at a time. Legacy standalone routes remain for
browser compatibility and parity tests.
"""

from __future__ import annotations

import datetime as dt
import json
import math
import os
import signal
import sys
import threading
import traceback
from http.server import BaseHTTPRequestHandler, HTTPServer, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

import numpy as np
import pandas as pd

from app import (
    config,
    day_summary,
    db,
    freshness,
    market_calendar,
    metrics,
)
from app import (
    presentation as ui,
)
from app.view_selectors import strikes_around_money


def get_runtime():
    from app.scheduler import get_runtime as runtime
    return runtime()

_conn_local = threading.local()


def _conn():
    c = getattr(_conn_local, "conn", None)
    if c is None:
        c = db.get_connection()
        _conn_local.conn = c
    return c


def _clean(obj):
    """Turn numpy/pandas/datetime values into plain JSON-safe primitives."""
    if obj is None:
        return None
    if isinstance(obj, (bool, np.bool_)):
        return bool(obj)
    if isinstance(obj, (int, np.integer)):
        return int(obj)
    if isinstance(obj, (float, np.floating)):
        v = float(obj)
        return v if math.isfinite(v) else None
    if isinstance(obj, (pd.Timestamp, dt.datetime)):
        return pd.Timestamp(obj).strftime("%Y-%m-%d %H:%M:%S")
    if isinstance(obj, dt.date):
        return obj.strftime("%Y-%m-%d")
    if isinstance(obj, dict):
        return {str(k): _clean(v) for k, v in obj.items()}
    if isinstance(obj, (list, tuple)):
        return [_clean(v) for v in obj]
    if pd.isna(obj):
        return None
    return obj


def _iso_date(val) -> str:
    return pd.Timestamp(val).strftime("%Y-%m-%d")


def _time_left(expiry, moment) -> bool:
    return metrics.years_to_expiry(expiry, moment) > 0


def build_state() -> dict:
    conn = _conn()
    watchlist = db.get_watchlist(conn)
    runtime = get_runtime()
    rt_status = runtime.status()
    interval = db.get_collector_interval(conn)
    depth = db.collection_depth(conn)
    rows_by_ticker = db.rows_per_pass_by_ticker(conn) if interval > 0 else {}
    growth_mb = db.estimated_growth_mb_per_month(conn, interval, rows_by_ticker) if interval > 0 else 0.0

    cooldowns = {}
    for src in ("cboe", "yahoo"):
        until = db.provider_cooldown_until(conn, source=src)
        if until is not None:
            cooldowns[src] = f"{until:%H:%M} UTC"

    tickers_summary = []
    for sym in watchlist:
        src = db.active_source(conn, sym)
        snap = db.get_latest_snapshot(conn, sym, source=src)
        item = {
            "symbol": sym,
            "source": src or "",
            "since": _iso_date(depth[sym]) if sym in depth else "",
            "has_data": not snap.empty,
            "spot": None,
            "net_gex": None,
            "net_gex_str": "—",
            "state": "",
            "state_cn": "无数据",
            "collected_at": "",
        }
        if not snap.empty:
            solved = metrics.with_solved_iv(snap, metrics.DEFAULT_PRICING, ticker=sym)
            w = metrics.gamma_weather(solved)
            spot = float(solved["underlying_price"].iloc[0])
            item["spot"] = spot
            item["collected_at"] = pd.Timestamp(solved["collected_at"].max()).strftime("%m-%d %H:%M")
            if w:
                item["net_gex"] = float(w["net_gex"])
                item["net_gex_str"] = ui.compact(w["net_gex"], signed=True)
                item["state"] = w["state"]
                item["state_cn"] = ui.WEATHER_CN.get(w["state"], (w["state"], ""))[0]
        tickers_summary.append(item)

    runs_df = db.get_recent_runs(conn, limit=25)
    runs = []
    if not runs_df.empty:
        for r in runs_df.itertuples():
            runs.append({
                "started_at": pd.Timestamp(r.started_at).strftime("%m-%d %H:%M:%S"),
                "ticker": r.ticker,
                "source": r.source,
                "status": r.status,
                "rows": int(r.rows_fetched) if pd.notna(r.rows_fetched) else 0,
                "oi_zero": f"{r.oi_zero_fraction:.0%}" if pd.notna(r.oi_zero_fraction) else "—",
                "error": r.error_message or "",
            })

    return _clean({
        "version": config.APP_VERSION,
        "watchlist": watchlist,
        "tickers": tickers_summary,
        "collector_state": rt_status.get("state", "idle"),
        "collector_error": rt_status.get("error") or "",
        "interval_minutes": interval,
        "interval_choices": [
            {"label": k, "minutes": v} for k, v in config.COLLECTOR_INTERVAL_CHOICES.items()
        ],
        "growth_gb_per_month": round(growth_mb / 1000.0, 2),
        "cooldowns": cooldowns,
        "runs": runs,
    })


def build_workbench(
    ticker: str,
    dimension: str = "gex",
    side: str = "net",
    as_of_str: str = "",
    n_expiries: int = 12,
    strikes_each_side: int = 20,
) -> dict:
    conn = _conn()
    source = db.active_source(conn, ticker)
    moments_raw = db.get_collection_moments(conn, ticker, days=config.SNAPSHOT_HISTORY_DAYS, source=source)
    moments_asc = sorted({pd.Timestamp(m) for m in moments_raw})

    latest_raw = db.get_latest_snapshot(conn, ticker, source=source)
    if latest_raw.empty:
        return _clean({"ticker": ticker, "source": source or "", "empty": True})

    latest_df = metrics.with_solved_iv(latest_raw, metrics.DEFAULT_PRICING, ticker=ticker)
    latest_date = pd.Timestamp(latest_df["collected_at"].max())
    if not moments_asc:
        moments_asc = [latest_date]
    newest = moments_asc[-1]

    as_of = newest
    if as_of_str:
        try:
            target = pd.Timestamp(as_of_str)
            for m in moments_asc:
                if abs((m - target).total_seconds()) < 1:
                    as_of = m
                    break
        except Exception:
            as_of = newest

    if as_of == latest_date:
        snapshot_df = latest_df
    else:
        snap_raw = db.get_snapshots_at(conn, ticker, [as_of], source=source)
        snapshot_df = (
            metrics.with_solved_iv(snap_raw, metrics.DEFAULT_PRICING, ticker=ticker)
            if not snap_raw.empty else pd.DataFrame()
        )

    # Freshness & status
    interval = max(db.get_collector_interval(conn), 15)
    last_success, failures = db.collection_state(conn, ticker, source=source)
    fresh = freshness.assess(last_success, failures, interval)
    market_note = market_calendar.status_note(
        db.get_setting(conn, "market_state"), db.get_setting(conn, "market_state_at"), interval
    )
    dot = "ok"
    status_note = market_note or "正常"
    if fresh:
        dot = {freshness.RESTING: "rest", freshness.STALE: "warn", freshness.FAILING: "bad"}.get(fresh[0], "ok")
        status_note = fresh[1]

    from app.desktop_compute import ribbon_for_frame
    ribbon = ribbon_for_frame(latest_df)

    if snapshot_df.empty:
        return _clean({
            "ticker": ticker,
            "source": source or "",
            "empty": True,
            "ribbon": ribbon,
        })

    tradeable = [e for e in sorted(snapshot_df["expiry"].unique()) if _time_left(e, as_of)]
    spot = float(snapshot_df["underlying_price"].iloc[0])
    n_exp = max(1, min(int(n_expiries), len(tradeable))) if tradeable else 1
    shown = tradeable[:n_exp]

    calls = snapshot_df[snapshot_df["option_type"] == "call"]
    puts = snapshot_df[snapshot_df["option_type"] == "put"]
    gex_full = metrics.gex_matrix(snapshot_df, as_of=as_of, expiries=shown)
    gex_call = metrics.exposure_matrix(calls, as_of=as_of, expiries=shown, dimension="gex")
    gex_put = metrics.exposure_matrix(puts, as_of=as_of, expiries=shown, dimension="gex")

    if dimension == "gex":
        dim_full, dim_call, dim_put = gex_full, gex_call, gex_put
    else:
        dim_full = metrics.exposure_matrix(snapshot_df, as_of=as_of, expiries=shown, dimension=dimension)
        dim_call = metrics.exposure_matrix(calls, as_of=as_of, expiries=shown, dimension=dimension)
        dim_put = metrics.exposure_matrix(puts, as_of=as_of, expiries=shown, dimension=dimension)

    display_full = {"net": dim_full, "call": dim_call, "put": dim_put}.get(side, dim_full)

    band = strikes_around_money(display_full, spot, int(strikes_each_side)) if not display_full.empty else pd.DataFrame()
    if not band.empty:
        band = band.loc[:, ((band != 0) | band.isna()).any(axis=0)]

    # Order strikes descending (high strike at top, low strike at bottom, like an orderbook/ladder)
    strikes = np.sort(band.index.to_numpy(dtype=float))[::-1] if not band.empty else np.array([])
    exp_cols = list(band.columns) if not band.empty else []

    z = band.reindex(index=strikes).to_numpy(dtype=float) if len(strikes) else np.zeros((0, 0))
    finite = np.abs(z[np.isfinite(z) & (z != 0)])
    vmax = float(np.percentile(finite, 92)) if finite.size else 1.0
    vmax = vmax or 1.0

    def aligned_arr(frame: pd.DataFrame) -> np.ndarray:
        if frame is None or frame.empty or not len(strikes):
            return np.full((len(strikes), len(exp_cols)), np.nan)
        return frame.reindex(index=strikes, columns=exp_cols).to_numpy(dtype=float)

    c_arr = aligned_arr(dim_call)
    p_arr = aligned_arr(dim_put)
    oi_c_piv = (
        calls.pivot_table(index="strike", columns="expiry", values="open_interest", aggfunc="sum")
        if not calls.empty else pd.DataFrame()
    )
    oi_p_piv = (
        puts.pivot_table(index="strike", columns="expiry", values="open_interest", aggfunc="sum")
        if not puts.empty else pd.DataFrame()
    )
    vol_piv = snapshot_df.pivot_table(index="strike", columns="expiry", values="volume", aggfunc="sum")
    oi_c_arr = aligned_arr(oi_c_piv)
    oi_p_arr = aligned_arr(oi_p_piv)
    vol_arr = aligned_arr(vol_piv)

    totals_series = display_full.sum(min_count=1) if not display_full.empty else pd.Series(dtype=float)
    totals = [float(totals_series.get(e, 0.0)) if pd.notna(totals_series.get(e, np.nan)) else 0.0 for e in exp_cols]
    totals_str = [ui.compact(v, signed=True) for v in totals]
    grand_total = float(np.nansum(totals))

    profile = np.nansum(np.where(np.isfinite(z), z, 0.0), axis=1).tolist() if len(strikes) else []
    profile_str = [ui.compact(v, signed=True) for v in profile]
    profile_max = max((abs(v) for v in profile), default=1.0) or 1.0

    flip = metrics.gamma_flip_from_matrix(gex_full, spot) if not gex_full.empty else None
    walls = metrics.dealer_walls(snapshot_df, as_of=as_of, expiries=shown)
    gwalls = metrics.gex_walls(snapshot_df, as_of=as_of, expiries=shown)
    near_pain = metrics.max_pain(snapshot_df, shown[0]) if shown else None
    move = metrics.expected_move(snapshot_df, shown[0], as_of) if shown else None
    atm_strike = float(strikes[np.abs(strikes - spot).argmin()]) if len(strikes) else None

    strike_tags = {}
    for key, tag in (
        ("call_gex_wall", "◆C"),
        ("put_gex_wall", "◆P"),
        ("call_wall", "▲OI"),
        ("put_wall", "▼OI"),
    ):
        val = gwalls.get(key) if "gex" in key else walls.get(key)
        if val is not None:
            strike_tags.setdefault(float(val), []).append(tag)
    if near_pain is not None:
        strike_tags.setdefault(float(near_pain), []).append("✕Pain")

    cells_text = []
    cells_norm = []
    cells_tip = []
    dim_name = ui.DIMENSION_CN.get(dimension, "GEX")
    exp_labels = [f"{ui.expiry_short(e)} ({ui.dte(e, as_of)}D)" for e in exp_cols]

    for r_idx, s_val in enumerate(strikes):
        row_t, row_n, row_tip = [], [], []
        for c_idx in range(len(exp_cols)):
            val = z[r_idx, c_idx]
            if not np.isfinite(val):
                row_t.append("")
                row_n.append(0.0)
            elif val == 0:
                row_t.append("·")
                row_n.append(0.0)
            else:
                row_t.append(ui.cell_compact(val))
                row_n.append(float(np.clip(val / vmax, -1.0, 1.0)))
            cv = c_arr[r_idx, c_idx]
            pv = p_arr[r_idx, c_idx]
            coi = oi_c_arr[r_idx, c_idx]
            poi = oi_p_arr[r_idx, c_idx]
            vv = vol_arr[r_idx, c_idx]
            coi_s = f"{coi:,.0f}" if np.isfinite(coi) else "0"
            poi_s = f"{poi:,.0f}" if np.isfinite(poi) else "0"
            vv_s = f"{vv:,.0f}" if np.isfinite(vv) else "0"
            tip = (
                f"K {s_val:g} · {exp_labels[c_idx]}\n"
                f"{dim_name} 净: {ui.compact(val, signed=True)} "
                f"(Call {ui.compact(cv, signed=True)} / Put {ui.compact(pv, signed=True)})\n"
                f"OI: C {coi_s} / P {poi_s} · 成交量: {vv_s}"
            )
            row_tip.append(tip)
        cells_text.append(row_t)
        cells_norm.append(row_n)
        cells_tip.append(row_tip)

    board = []
    for e in shown:
        em = metrics.expected_move(snapshot_df, e, as_of)
        ch = snapshot_df[snapshot_df["expiry"] == e]
        c_oi = float(ch.loc[ch["option_type"] == "call", "open_interest"].sum())
        p_oi = float(ch.loc[ch["option_type"] == "put", "open_interest"].sum())
        net_e = float(gex_full[e].sum()) if e in gex_full else 0.0
        cg_e = float(gex_call[e].sum()) if e in gex_call else 0.0
        pg_e = float(gex_put[e].sum()) if e in gex_put else 0.0
        mp_e = metrics.max_pain(snapshot_df, e)
        board.append({
            "expiry": ui.expiry_short(e),
            "expiry_iso": _iso_date(e),
            "dte": ui.dte(e, as_of),
            "net_gex": net_e,
            "net_gex_str": ui.compact(net_e, signed=True),
            "call_gex_str": ui.compact(cg_e, signed=True),
            "put_gex_str": ui.compact(pg_e, signed=True),
            "max_pain": ui.strike_label(mp_e),
            "atm_iv": ui.pct(em["implied_volatility"]) if em else "—",
            "move": f"±{em['move']:,.2f}" if em else "—",
            "move_pct": f"±{em['move_pct']:.2f}%" if em else "—",
            "call_oi": f"{c_oi:,.0f}",
            "put_oi": f"{p_oi:,.0f}",
            "pcr": f"{p_oi / c_oi:.2f}" if c_oi > 0 else "—",
            "contracts": int(metrics.contracts_backing_expiry(snapshot_df, e, as_of)),
        })

    return _clean({
        "ticker": ticker,
        "source": (source or "").upper(),
        "empty": False,
        "collected_at": latest_date.strftime("%Y-%m-%d %H:%M"),
        "as_of": as_of.strftime("%Y-%m-%d %H:%M:%S"),
        "as_of_display": as_of.strftime("%m-%d %H:%M") + (" · 最新" if as_of == newest else " · 回放"),
        "moments": [
            {"value": m.strftime("%Y-%m-%d %H:%M:%S"), "label": m.strftime("%m-%d %H:%M") + (" (最新)" if m == newest else "")}
            for m in reversed(moments_asc)
        ],
        "contracts_count": int(latest_df[["expiry", "strike", "option_type"]].drop_duplicates().shape[0]),
        "dot": dot,
        "status_note": status_note,
        "ribbon": ribbon,
        "dimension": dimension,
        "dimension_cn": dim_name,
        "unit_cn": ui.UNIT_CN.get(dimension, ""),
        "side": side,
        "max_expiries": len(tradeable),
        "shown_expiries": len(exp_cols),
        "spot": spot,
        "atm_strike": atm_strike,
        "flip": flip,
        "move_lower": move["lower"] if move else None,
        "move_upper": move["upper"] if move else None,
        "strikes": [float(s) for s in strikes],
        "strike_labels": [f"{s:g}" for s in strikes],
        "strike_tags": [" ".join(strike_tags.get(float(s), [])) for s in strikes],
        "expiries": [ui.expiry_short(e) for e in exp_cols],
        "expiry_dtes": [f"{ui.dte(e, as_of)}D" for e in exp_cols],
        "totals": totals,
        "totals_str": totals_str,
        "grand_total": grand_total,
        "grand_total_str": ui.compact(grand_total, signed=True),
        "cells_text": cells_text,
        "cells_norm": cells_norm,
        "cells_tip": cells_tip,
        "profile": profile,
        "profile_str": profile_str,
        "profile_max": profile_max,
        "board": board,
    })


def build_volatility(ticker: str, width_pct: float = 12.0) -> dict:
    conn = _conn()
    source = db.active_source(conn, ticker)
    raw = db.get_latest_snapshot(conn, ticker, source=source)
    if raw.empty:
        return _clean({"ticker": ticker, "empty": True})
    latest_df = metrics.with_solved_iv(raw, metrics.DEFAULT_PRICING, ticker=ticker)
    latest_date = pd.Timestamp(latest_df["collected_at"].max())
    spot = float(latest_df["underlying_price"].iloc[0])
    live_exp = [e for e in sorted(latest_df["expiry"].unique()) if _time_left(e, latest_date)]

    skew_series = []
    for e in live_exp[:5]:
        ch = latest_df[latest_df["expiry"] == e]
        otm = ch[
            ((ch["option_type"] == "put") & (ch["strike"] < spot))
            | ((ch["option_type"] == "call") & (ch["strike"] >= spot))
        ]
        otm = otm[(otm["strike"] - spot).abs() <= spot * width_pct / 100.0]
        otm = otm[otm["implied_volatility"] > 0][["strike", "implied_volatility"]].sort_values("strike")
        if len(otm) > 1:
            skew_series.append({
                "label": f"{ui.expiry_short(e)} ({ui.dte(e, latest_date)}D)",
                "strikes": [float(x) for x in otm["strike"]],
                "ivs": [float(y) for y in otm["implied_volatility"]],
            })

    term = []
    for e in live_exp[:24]:
        em = metrics.expected_move(latest_df, e)
        if em:
            term.append({
                "label": pd.Timestamp(e).strftime("%m-%d"),
                "dte": ui.dte(e, latest_date),
                "atm_iv": float(em["implied_volatility"]),
                "move_pct": float(em["move_pct"]),
                "move": float(em["move"]),
            })

    iv_avg = db.get_iv_weighted_average(conn, ticker, days=config.SNAPSHOT_HISTORY_DAYS, source=source)
    iv_hist = []
    if not iv_avg.empty:
        for r in iv_avg.itertuples():
            if pd.notna(r.iv_weighted_avg):
                iv_hist.append({
                    "time": pd.Timestamp(r.collected_at).strftime("%m-%d %H:%M"),
                    "iv": float(r.iv_weighted_avg),
                })

    stored_rv = db.get_realized_volatility(conn, ticker)
    rv_list = []
    if stored_rv:
        as_of_rv, rv_map, rv_src = stored_rv
        for w, v in sorted(rv_map.items()):
            rv_list.append({"window": f"RV{w}d", "value": ui.pct(v), "raw": float(v)})

    return _clean({
        "ticker": ticker,
        "empty": False,
        "spot": spot,
        "skew": skew_series,
        "term": term,
        "iv_history": iv_hist,
        "rv": rv_list,
    })


def build_chain(
    ticker: str,
    expiry_filter: str = "",
    side: str = "all",
    band_pct: float = 10.0,
    min_oi: int = 0,
) -> dict:
    conn = _conn()
    source = db.active_source(conn, ticker)
    raw = db.get_latest_snapshot(conn, ticker, source=source)
    if raw.empty:
        return _clean({"ticker": ticker, "empty": True})
    latest_df = metrics.with_solved_iv(raw, metrics.DEFAULT_PRICING, ticker=ticker)
    spot = float(latest_df["underlying_price"].iloc[0])
    table = metrics.screener_table(latest_df)
    extra = latest_df[["expiry", "strike", "option_type", "bid", "ask", "volume"]].drop_duplicates(
        ["expiry", "strike", "option_type"]
    )
    table = table.merge(extra, on=["expiry", "strike", "option_type"], how="left")
    now_utc = pd.Timestamp.now("UTC")
    if not table.empty:
        table = table[table["expiry"].map(lambda e: _time_left(e, now_utc))]

    exp_options = sorted(table["expiry"].unique()) if not table.empty else []
    exp_list = [{"iso": _iso_date(e), "label": ui.expiry_short(e)} for e in exp_options]

    view = table
    if expiry_filter:
        wanted = {s.strip() for s in expiry_filter.split(",") if s.strip()}
        if wanted:
            view = view[view["expiry"].map(_iso_date).isin(wanted)]
    elif exp_options:
        view = view[view["expiry"].isin(exp_options[:2])]

    if side in ("call", "put"):
        view = view[view["option_type"] == side]
    if band_pct > 0:
        view = view[(view["strike"] - spot).abs() <= spot * band_pct / 100.0]
    if min_oi > 0:
        view = view[view["open_interest"].fillna(0) >= min_oi]

    view = view.sort_values(["expiry", "strike", "option_type"]).reset_index(drop=True)
    total_matching = len(view)
    view = view.head(400)

    rows = []
    for r in view.itertuples():
        moneyness = (float(r.strike) / spot - 1.0) * 100.0
        rows.append({
            "expiry_iso": _iso_date(r.expiry),
            "expiry": ui.expiry_short(r.expiry),
            "dte": int(r.dte) if pd.notna(r.dte) else 0,
            "strike": float(r.strike),
            "strike_str": f"{r.strike:g}",
            "type": r.option_type,
            "type_short": r.option_type[0].upper(),
            "moneyness": f"{moneyness:+.1f}%",
            "bid": ui.price(r.bid),
            "ask": ui.price(r.ask),
            "last": ui.price(r.last_price),
            "volume": f"{int(r.volume):,}" if pd.notna(r.volume) else "0",
            "oi": f"{int(r.open_interest):,}" if pd.notna(r.open_interest) else "0",
            "iv": ui.pct(r.implied_volatility),
            "delta": f"{r.delta:+.3f}" if pd.notna(r.delta) else "—",
            "gamma": f"{r.gamma:.4f}" if pd.notna(r.gamma) else "—",
            "theta": f"{r.theta:+.3f}" if pd.notna(r.theta) else "—",
            "vega": f"{r.vega:.3f}" if pd.notna(r.vega) else "—",
            "vanna": f"{r.vanna:+.4f}" if pd.notna(r.vanna) else "—",
            "charm": f"{r.charm:+.4f}" if pd.notna(r.charm) else "—",
        })

    tracked_df = db.get_tracked_contracts(conn, ticker)
    pinned = []
    if not tracked_df.empty:
        for t in tracked_df.itertuples():
            pinned.append({
                "id": int(t.id),
                "expiry_iso": _iso_date(t.expiry),
                "strike": float(t.strike),
                "type": t.option_type,
                "label": f"{pd.Timestamp(t.expiry):%m-%d} {t.strike:g}{t.option_type[0].upper()}",
            })

    return _clean({
        "ticker": ticker,
        "empty": False,
        "spot": spot,
        "total_chain": len(table),
        "total_matching": total_matching,
        "expiries": exp_list,
        "pinned": pinned,
        "rows": rows,
    })


def build_contract(ticker: str, expiry_str: str, strike: float, option_type: str) -> dict:
    conn = _conn()
    source = db.active_source(conn, ticker)
    expiry = pd.Timestamp(expiry_str)
    is_expired = expiry < pd.Timestamp(dt.date.today())
    raw = db.get_contract_history(
        conn, ticker, expiry, float(strike), option_type,
        days=None if is_expired else config.SNAPSHOT_HISTORY_DAYS, source=source,
    )
    if raw.empty:
        return _clean({"empty": True})
    contract_df = metrics.with_solved_iv(raw, metrics.DEFAULT_PRICING, ticker=ticker)
    history = metrics.contract_greeks_history(contract_df, float(strike), expiry, option_type)
    if history.empty:
        return _clean({"empty": True})

    last = history.iloc[-1]
    attribution = metrics.greek_attribution(history)
    attr_steps = []
    if not attribution.by_day.empty:
        totals = attribution.totals
        start_c = float(attribution.start_price) * 100.0
        attr_steps.append({"label": "起点", "cents": start_c, "str": f"{start_c:.1f}¢", "kind": "total"})
        for k, name in (("delta", "Delta"), ("gamma", "Gamma"), ("vega", "Vega"), ("theta", "Theta"), ("residual", "残差")):
            v = float(totals[k]) * 100.0
            attr_steps.append({"label": name, "cents": v, "str": f"{v:+.1f}¢", "kind": "pos" if v >= 0 else "neg"})
        end_c = start_c + sum(float(totals[k]) * 100.0 for k in (*metrics.ATTRIBUTION_TERMS, "residual"))
        attr_steps.append({"label": "终点", "cents": end_c, "str": f"{end_c:.1f}¢", "kind": "total"})

    points = []
    for r in history.itertuples():
        points.append({
            "time": pd.Timestamp(r.collected_at).strftime("%m-%d %H:%M"),
            "price": float(r.last_price) if pd.notna(r.last_price) else None,
            "iv": float(r.implied_volatility) if pd.notna(r.implied_volatility) else None,
            "delta": float(r.delta) if pd.notna(r.delta) else None,
            "gamma": float(r.gamma) if pd.notna(r.gamma) else None,
            "theta": float(r.theta) if pd.notna(r.theta) else None,
            "vega": float(r.vega) if pd.notna(r.vega) else None,
        })

    return _clean({
        "empty": False,
        "name": f"{ticker} {expiry:%y%m%d} {strike:g}{option_type[0].upper()}",
        "expiry_iso": _iso_date(expiry),
        "strike": float(strike),
        "type": option_type,
        "price_str": ui.price(last["last_price"]),
        "iv_str": ui.pct(last["implied_volatility"]),
        "delta_str": f"{last['delta']:+.3f}" if pd.notna(last["delta"]) else "—",
        "gamma_str": f"{last['gamma']:.4f}" if pd.notna(last["gamma"]) else "—",
        "theta_str": f"{last['theta']:+.3f}" if pd.notna(last["theta"]) else "—",
        "vega_str": f"{last['vega']:.3f}" if pd.notna(last["vega"]) else "—",
        "samples": len(history),
        "attribution": attr_steps,
        "points": points,
    })


def build_flow(ticker: str) -> dict:
    conn = _conn()
    source = db.active_source(conn, ticker)
    raw = db.get_latest_snapshot(conn, ticker, source=source)
    if raw.empty:
        return _clean({"ticker": ticker, "empty": True})
    latest_df = metrics.with_solved_iv(raw, metrics.DEFAULT_PRICING, ticker=ticker)
    spot = float(latest_df["underlying_price"].iloc[0])

    flagged = metrics.unusual_activity(latest_df, stats=db.get_volume_stats(conn, ticker, source=source))
    unusual_rows = []
    if not flagged.empty:
        for r in flagged.head(150).itertuples():
            vol_oi = float(r.volume) / float(r.open_interest) if r.open_interest and r.open_interest > 0 else None
            unusual_rows.append({
                "expiry": ui.expiry_short(r.expiry),
                "strike": f"{r.strike:g}",
                "type": r.option_type[0].upper(),
                "volume": f"{int(r.volume):,}",
                "oi": f"{int(r.open_interest):,}",
                "avg_vol": f"{r.avg_volume:,.0f}" if pd.notna(r.avg_volume) else "—",
                "zscore": f"{r.volume_zscore:.1f}" if pd.notna(r.volume_zscore) else "—",
                "vol_oi": f"{vol_oi:.2f}x" if vol_oi is not None else "—",
            })

    day_moments = db.get_collection_moments(
        conn, ticker, days=config.SNAPSHOT_HISTORY_DAYS, per_day=True, source=source
    )[:2]
    delta_df = (
        metrics.oi_delta(db.get_snapshots_at(conn, ticker, day_moments, source=source))
        if len(day_moments) >= 2 else pd.DataFrame()
    )
    oi_rows = []
    if not delta_df.empty:
        top = delta_df.reindex(delta_df["oi_delta"].abs().sort_values(ascending=False).index).head(150)
        for r in top.itertuples():
            oi_rows.append({
                "expiry": ui.expiry_short(r.expiry),
                "strike": f"{r.strike:g}",
                "type": r.option_type[0].upper(),
                "oi": f"{int(r.open_interest):,}",
                "oi_delta": int(r.oi_delta),
                "oi_delta_str": f"{int(r.oi_delta):+,}",
                "oi_pct_str": f"{r.oi_delta_pct:+.1f}%" if pd.notna(r.oi_delta_pct) else "—",
            })

    days = db.list_day_summaries(conn, ticker, source) if source else []
    ladder_rows = []
    days_label = ""
    if len(days) >= 1:
        day_after = days[0]
        day_before = days[1] if len(days) > 1 else days[0]
        days_label = f"{day_before:%m-%d} → {day_after:%m-%d}"
        row_after = db.get_day_summary(conn, ticker, source, day_after)
        row_before = db.get_day_summary(conn, ticker, source, day_before) if day_before != day_after else None

        def _rollup(row):
            if row is None:
                return None
            stored = db.get_moment_rollup(conn, ticker, source, row["collected_at"]) or {}
            return {
                "iv_weighted_avg": stored.get("iv_weighted_avg"),
                "call_volume": row.get("call_volume"),
                "put_volume": row.get("put_volume"),
                "call_oi": row.get("call_oi"),
                "put_oi": row.get("put_oi"),
            }

        if row_after is not None:
            ladder = day_summary.changes(row_before, row_after, _rollup(row_before), _rollup(row_after), None)
            labels_cn = {
                "state": "Gamma 状态", "price": "现价", "net_gex": "净 GEX", "call_wall": "Call OI 墙",
                "put_wall": "Put OI 墙", "gamma_flip": "Gamma 翻转", "max_pain": "Max Pain",
                "expected_move": "±1σ 区间", "pcr_volume": "P/C 成交量", "pcr_oi": "P/C 持仓",
                "iv": "IV 加权", "call_oi": "Call 持仓", "put_oi": "Put 持仓", "expiries": "挂牌到期日",
                "contracts": "合约数",
            }
            words_cn = {"unchanged": "不变", "gone": "消失", "new": "新增", "amplifying": "→ 放大", "damping": "→ 压制"}
            for line in ladder:
                def _cell(which):
                    if line["unit"] == "state":
                        v = line.get(which)
                        return ui.WEATHER_CN.get(v, ("—",))[0] if v else "—"
                    return day_summary.format_value(line, which)

                d_txt = day_summary.format_delta(line)
                tone_val = (
                    "pos" if line["kind"] == day_summary.DAMPING
                    else ("neg" if line["kind"] == day_summary.AMPLIFYING else "")
                )
                ladder_rows.append({
                    "label": labels_cn.get(line["key"], line["label"]),
                    "before": _cell("before"),
                    "after": _cell("after"),
                    "delta": words_cn.get(d_txt, d_txt),
                    "tone": tone_val,
                })

    pcr_df = db.get_put_call_ratio(conn, ticker, days=config.SNAPSHOT_HISTORY_DAYS, source=source)
    pcr_points = []
    if not pcr_df.empty:
        for r in pcr_df.itertuples():
            pcr_points.append({
                "time": pd.Timestamp(r.collected_at).strftime("%m-%d %H:%M"),
                "pcr_vol": float(r.pcr_volume) if pd.notna(r.pcr_volume) else None,
                "pcr_oi": float(r.pcr_oi) if pd.notna(r.pcr_oi) else None,
            })

    return _clean({
        "ticker": ticker,
        "empty": False,
        "spot": spot,
        "unusual": unusual_rows,
        "oi_delta": oi_rows,
        "days_count": len(days),
        "days_label": days_label,
        "ladder": ladder_rows,
        "pcr_history": pcr_points,
    })


class Handler(BaseHTTPRequestHandler):
    def finish(self):
        try:
            super().finish()
        finally:
            conn = getattr(_conn_local, "conn", None)
            if conn is not None:
                conn.close()
                del _conn_local.conn

    def log_message(self, format, *args):
        return

    def _send(self, code: int, payload: dict):
        body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json; charset=utf-8")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        try:
            parsed = urlparse(self.path)
            path = parsed.path
            qs = parse_qs(parsed.query)

            if path.startswith("/compute/"):
                from app.desktop_compute import dispatch
                self._send(200, _clean(dispatch(path, {k: v[0] for k, v in qs.items()}, _conn())))
                return

            def arg(k: str, default: str = "") -> str:
                return qs.get(k, [default])[0]

            if path in ("/health", "/_stcore/health"):
                self._send(200, {"ok": True})
                return
            if path == "/api/state":
                self._send(200, build_state())
                return
            if path == "/api/workbench":
                ticker = arg("ticker", "SPY").upper()
                self._send(
                    200,
                    build_workbench(
                        ticker=ticker,
                        dimension=arg("dimension", "gex"),
                        side=arg("side", "net"),
                        as_of_str=arg("as_of", ""),
                        n_expiries=int(arg("n_expiries", "12") or 12),
                        strikes_each_side=int(arg("strikes", "20") or 20),
                    ),
                )
                return
            if path == "/api/volatility":
                self._send(200, build_volatility(arg("ticker", "SPY").upper(), float(arg("width", "12") or 12)))
                return
            if path == "/api/chain":
                self._send(
                    200,
                    build_chain(
                        ticker=arg("ticker", "SPY").upper(),
                        expiry_filter=arg("expiries", ""),
                        side=arg("side", "all"),
                        band_pct=float(arg("band", "10") or 10),
                        min_oi=int(arg("min_oi", "0") or 0),
                    ),
                )
                return
            if path == "/api/contract":
                self._send(
                    200,
                    build_contract(
                        ticker=arg("ticker", "SPY").upper(),
                        expiry_str=arg("expiry", ""),
                        strike=float(arg("strike", "0") or 0),
                        option_type=arg("type", "call"),
                    ),
                )
                return
            if path == "/api/flow":
                self._send(200, build_flow(arg("ticker", "SPY").upper()))
                return
            if path == "/api/search":
                from app import catalogue
                conn = _conn()
                watched = db.get_watchlist(conn)
                q = arg("q", "").strip().upper()
                opts = catalogue.options(conn, watched)
                matches = []
                for sym, label in opts:
                    if not q or q in sym.upper() or q in label.upper():
                        matches.append({"symbol": sym, "label": label})
                        if len(matches) >= 25:
                            break
                self._send(200, {"results": matches})
                return
            self._send(404, {"error": f"unknown endpoint {path}"})
        except Exception as exc:
            traceback.print_exc()
            self._send(500, {"error": str(exc)})

    def do_POST(self):
        try:
            length = int(self.headers.get("Content-Length", "0") or 0)
            raw = self.rfile.read(length) if length > 0 else b"{}"
            data = json.loads(raw.decode("utf-8") or "{}")
            path = urlparse(self.path).path
            if path.startswith("/compute/"):
                from app.desktop_compute import dispatch
                self._send(200, _clean(dispatch(path, data, _conn())))
                return
            if os.environ.get("GEX_DB_READONLY") == "1":
                self._send(403, {"error": "Native mode: only the Go application may write the database"})
                return
            conn = _conn()

            if path == "/api/collect":
                from app import collector
                ticker = (data.get("ticker") or "").strip().upper()
                targets = [ticker] if ticker else db.get_watchlist(conn)
                res = collector.collect_watchlist(conn, targets)
                self._send(200, {"results": res})
                return
            if path == "/api/watchlist/add":
                from app import catalogue, providers, suggestions
                sym = (data.get("ticker") or "").strip().upper()
                if not sym:
                    self._send(400, {"error": "请输入标的代码"})
                    return
                provider = providers.get_provider()
                unsupported = getattr(provider, "unsupported_symbols", {}) or {}
                opts = catalogue.options(conn, db.get_watchlist(conn))
                cat_syms = {s for s, _ in opts}
                proposal = suggestions.propose(
                    sym, db.collection_depth(conn).keys(),
                    in_catalogue=cat_syms.__contains__,
                    unsupported=unsupported,
                    provider=provider.name,
                )
                if proposal and proposal.blocking:
                    self._send(400, {"error": proposal.message})
                    return
                check = getattr(provider, "underlying_has_options", None)
                if check is not None and check(sym) is False:
                    msg = (
                        suggestions.source_refusal(sym, unsupported.get(sym), provider.name)
                        if sym in cat_syms
                        else suggestions.refusal(sym, db.get_watchlist(conn), cat_syms.__contains__)
                    )
                    self._send(400, {"error": msg})
                    return
                db.add_ticker(conn, sym)
                self._send(200, {"ok": True, "ticker": sym, "note": proposal.message if proposal else ""})
                return
            if path == "/api/watchlist/remove":
                sym = (data.get("ticker") or "").strip().upper()
                if sym:
                    db.remove_ticker(conn, sym)
                self._send(200, {"ok": True})
                return
            if path == "/api/interval":
                minutes = int(data.get("minutes", 0))
                db.set_collector_interval(conn, minutes)
                self._send(200, {"ok": True, "minutes": minutes})
                return
            if path == "/api/pin":
                ticker = data["ticker"]
                expiry = pd.Timestamp(data["expiry"])
                strike = float(data["strike"])
                opt_type = data["type"]
                db.add_tracked_contract(conn, ticker, expiry, strike, opt_type)
                self._send(200, {"ok": True})
                return
            if path == "/api/unpin":
                db.remove_tracked_contract(conn, int(data["id"]))
                self._send(200, {"ok": True})
                return
            self._send(404, {"error": f"unknown endpoint {path}"})
        except Exception as exc:
            traceback.print_exc()
            self._send(500, {"error": str(exc)})


def _watch_parent(parent: int) -> None:
    while threading.main_thread().is_alive():
        if os.getppid() != parent:
            os.kill(os.getpid(), signal.SIGTERM)
            return
        threading.Event().wait(2)


def main() -> None:
    port = 8765
    for arg in sys.argv[1:]:
        if arg.startswith("--port="):
            port = int(arg.split("=", 1)[1])
        elif arg.startswith("--server.port="):
            port = int(arg.split("=", 1)[1])

    native = os.environ.get("GEX_NATIVE_BACKEND") == "1"
    runtime = None if native else get_runtime()
    parent = os.environ.get("GEX_DESKTOP_PARENT")
    if parent:
        threading.Thread(target=_watch_parent, args=(int(parent),), daemon=True, name="desktop-parent").start()

    # The Go coordinator deduplicates and bounds numerical work. A single
    # handler also prevents cancelled HTTP clients multiplying Python jobs.
    server_type = HTTPServer if native else ThreadingHTTPServer
    server = server_type(("127.0.0.1", port), Handler)
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        pass
    finally:
        server.server_close()
        if runtime is not None:
            runtime.stop()


if __name__ == "__main__":
    main()
