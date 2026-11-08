"""Read-only numerical/Yahoo adapter for the native Go application.

Existing formulas stay in Python. Derived database writes are returned as a
batch to the Go owner; this process opens SQLite with mode=ro/query_only.
"""
import datetime as dt
from contextlib import contextmanager

import numpy as np
import pandas as pd

from app import config, day_summary, db, market_calendar, metrics
from app import presentation as ui
from app.sqlite_store import parameters


def _iso_date(value):
    return pd.Timestamp(value).strftime("%Y-%m-%d")


class DerivedBatch:
    """Reuse the existing derived-write SQL without granting Python a writer."""
    def __init__(self, reader):
        self.reader = reader
        self.writes = []
        self.rowcount = 0

    def execute(self, sql, params=()):
        normalized = " ".join(sql.lower().split())
        if normalized.startswith("insert into contract_volume_stats"):
            # stddev is the existing Python SQLite aggregate. Evaluate the
            # SELECT here and return only its result, never reimplement it in Go.
            start = sql.upper().index("SELECT")
            end = sql.upper().index("ON CONFLICT")
            result = self.reader.execute(sql[start:end], params).fetchall()
            prefix = sql[:start]
            suffix = sql[end:]
            for row in result:
                self.writes.append({"sql": prefix + "VALUES (?,?,?,?,?,?,?,?,?,?) " + suffix,
                                    "params": parameters(row)})
            self.rowcount = len(result)
            return self
        if normalized.startswith((
            "insert into ticker_day_summary", "update snapshot_iv_summary", "insert into realized_volatility"
        )):
            self.writes.append({"sql": sql, "params": parameters(params)})
            self.rowcount = 1
            return self
        if normalized.startswith(("select", "with")):
            return self.reader.execute(sql, params)
        raise ValueError("Unexpected statement in numerical adapter")

    def cursor(self):
        return self

    def __enter__(self):
        return self

    def __exit__(self, *args):
        pass

    def close(self):
        pass

    def commit(self):
        pass

    @contextmanager
    def transaction(self):
        start = len(self.writes)
        try:
            yield self
        except BaseException:
            del self.writes[start:]
            raise

def ticker_summary(conn, ticker):
    source = db.active_source(conn, ticker)
    raw = db.get_latest_snapshot(conn, ticker, source)
    out = {"symbol": ticker, "source": source or "", "has_data": not raw.empty,
           "net_gex": None, "net_gex_str": "—", "state": "", "state_cn": "无数据"}
    if not raw.empty:
        solved = metrics.with_solved_iv(raw, metrics.DEFAULT_PRICING, ticker=ticker)
        weather = metrics.gamma_weather(solved)
        out["ribbon"] = ribbon_for_frame(solved, weather)
        if weather:
            out.update(net_gex=weather["net_gex"], net_gex_str=ui.compact(weather["net_gex"], signed=True),
                       state=weather["state"], state_cn=ui.WEATHER_CN.get(weather["state"], (weather["state"], ""))[0])
    return out


def derived(conn, ticker, source):
    batch = DerivedBatch(conn)
    # Read and calculate with the real connection, record only the existing
    # persistence helpers. Numerical functions themselves are unchanged.
    moments = db.iv_summary_pending_moments(conn, ticker, limit=5, source=source)
    if moments:
        frame = db.get_snapshots_at(conn, ticker, moments, source=source)
        values = dict.fromkeys(moments)
        if not frame.empty:
            solved = metrics.with_solved_iv(frame, metrics.DEFAULT_PRICING, ticker=ticker)
            for r in metrics.iv_weighted_average(solved).itertuples():
                values[r.collected_at] = None if pd.isna(r.iv_weighted_avg) else float(r.iv_weighted_avg)
        db.store_own_iv_weighted_average(batch, ticker, source, values)
    latest = db.get_collection_moments(conn, ticker, days=1, source=source)
    missing = db.missing_day_summaries(conn, ticker, source, 45, day_summary.CODE_SHA)
    for moment in list(dict.fromkeys(latest[:1] + list(missing)[-3:])):
        row = day_summary.build_row(conn, ticker, source, moment, metrics.DEFAULT_PRICING)
        if row is not None:
            db.upsert_day_summary(batch, ticker, source, row, day_summary.CODE_SHA)
    # Reads inside this helper use execute()/fetchall(), not a Pandas cursor.
    if not db.volume_stats_are_current(conn, ticker, source):
        db.rebuild_volume_stats(batch, ticker, source=source)
    if config.FETCH_REALIZED_VOLATILITY:
        from app.providers.yahoo import YahooProvider
        day = market_calendar.last_completed_trading_day()
        if db.tickers_missing_realized_volatility(conn, [ticker], day):
            history = YahooProvider().fetch_price_history(ticker)
            if not history.empty:
                db.store_realized_volatility(batch, ticker, day, metrics.realized_volatility(history), "yahoo")
    return {"writes": batch.writes}


def chain_page(data):
    raw = pd.DataFrame(data["rows"])
    if raw.empty:
        return {"rows": []}
    raw["expiry"] = pd.to_datetime(raw["expiry"]).dt.date
    raw["collected_at"] = pd.to_datetime(raw["collected_at"])
    for column in ("last_price", "bid", "ask", "volume", "open_interest", "implied_volatility",
                   "delta", "gamma", "theta", "vega"):
        raw[column] = pd.to_numeric(raw[column], errors="coerce")
    solved = metrics.with_solved_iv(raw, metrics.DEFAULT_PRICING, ticker=data["ticker"])
    view = metrics.screener_table(solved).merge(
        solved[["expiry", "strike", "option_type", "bid", "ask", "volume"]],
        on=["expiry", "strike", "option_type"], how="left")
    spot = float(data["spot"])
    return {"rows": format_chain_rows(view, spot)}


def dispatch(path, data, conn):
    if path == "/compute/summary":
        return ticker_summary(conn, data["ticker"])
    if path == "/compute/derive":
        return derived(conn, data["ticker"], data["source"])
    if path == "/compute/chain":
        return chain_page(data)
    if path == "/compute/calendar":
        start = dt.date.fromisoformat(data["start"])
        sessions = {}
        for offset in range(35):
            day = start + dt.timedelta(days=offset)
            opens, closes = market_calendar.session_times(day)
            if opens is not None:
                sessions[day.isoformat()] = {"open": opens.strftime("%H:%M"), "close": closes.strftime("%H:%M")}
        return {"sessions": sessions}
    if path == "/compute/yahoo":
        from app.providers.yahoo import YahooProvider
        try:
            spot, frame = YahooProvider().fetch_ticker_snapshot(data["ticker"])
            return {"spot": spot, "rows": frame.to_dict("records")}
        except Exception as exc:
            from app.providers import is_rate_limited
            return {"error": str(exc), "rate_limited": is_rate_limited(exc)}
    raise ValueError("Unknown compute operation")


def format_chain_rows(view, spot):
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

    return rows


def ribbon_for_frame(latest_df, weather=None):
    if weather is None:
        weather = metrics.gamma_weather(latest_df)
    latest_date = pd.Timestamp(latest_df["collected_at"].max())
    spot_latest = float(latest_df["underlying_price"].iloc[0])
    live_exp_latest = [e for e in sorted(latest_df["expiry"].unique()) if metrics.years_to_expiry(e, latest_date) > 0]

    ribbon = {
        "spot": spot_latest,
        "spot_str": ui.price(spot_latest),
        "state": weather["state"] if weather else "",
        "state_cn": ui.WEATHER_CN.get(weather["state"], ("—", ""))[0] if weather else "—",
        "state_sub": ui.WEATHER_CN.get(weather["state"], ("", ""))[1] if weather else "",
        "positive": bool(weather and weather["net_gex"] >= 0),
        "scope": (
            f"近 {int(weather['horizon_dte'])} 天 · {int(weather['expiry_count'])} 到期日"
            if weather and weather.get("horizon_dte") is not None else ""
        ),
        "net_gex": weather["net_gex"] if weather else None,
        "net_gex_str": ui.compact(weather["net_gex"], signed=True) if weather else "—",
        "gamma_flip": weather.get("gamma_flip") if weather else None,
        "gamma_flip_str": ui.price(weather["gamma_flip"]) if weather and weather.get("gamma_flip") else "—",
        "flip_dist_str": f"距现价 {weather['flip_distance_pct']:.1f}%" if weather and weather.get("gamma_flip") else "25% 内无",
        "call_gex_wall": "—",
        "put_gex_wall": "—",
        "call_oi_wall": ui.strike_label(weather.get("call_wall")) if weather else "—",
        "put_oi_wall": ui.strike_label(weather.get("put_wall")) if weather else "—",
        "max_pain": "—",
        "max_pain_exp": "",
        "move_str": "—",
        "move_pct_str": "",
        "atm_iv_str": "—",
        "pcr_oi_str": "—",
        "pcr_vol_str": "",
    }
    if weather:
        chosen_w = [pd.Timestamp(e) for e in weather["expiries"]]
        gwalls = metrics.gex_walls(latest_df, expiries=chosen_w)
        ribbon["call_gex_wall"] = ui.strike_label(gwalls["call_gex_wall"])
        ribbon["put_gex_wall"] = ui.strike_label(gwalls["put_gex_wall"])
    if live_exp_latest:
        near = live_exp_latest[0]
        ribbon["max_pain"] = ui.strike_label(metrics.max_pain(latest_df, near))
        ribbon["max_pain_exp"] = ui.expiry_short(near)
        em = metrics.expected_move(latest_df, near)
        if em:
            ribbon["move_str"] = f"±{em['move']:,.2f}"
            ribbon["move_pct_str"] = f"±{em['move_pct']:.2f}% · {ui.expiry_short(near)}"
            ribbon["atm_iv_str"] = ui.pct(em["implied_volatility"])
    by_type = latest_df.groupby("option_type")[["open_interest", "volume"]].sum()
    if {"call", "put"} <= set(by_type.index) and by_type.loc["call", "open_interest"] > 0:
        pcr_oi = by_type.loc["put", "open_interest"] / by_type.loc["call", "open_interest"]
        pcr_vol = (
            by_type.loc["put", "volume"] / by_type.loc["call", "volume"]
            if by_type.loc["call", "volume"] > 0 else np.nan
        )
        ribbon["pcr_oi_str"] = f"{pcr_oi:.2f}"
        ribbon["pcr_vol_str"] = f"成交量 {pcr_vol:.2f}" if pd.notna(pcr_vol) else ""

    return ribbon


def _main_cli() -> None:
    import json
    import sys

    op = ""
    ticker = ""
    as_of = ""
    for arg in sys.argv[1:]:
        if arg.startswith("--op="):
            op = arg.split("=", 1)[1].strip()
        elif arg.startswith("--ticker="):
            ticker = arg.split("=", 1)[1].strip().upper()
        elif arg.startswith("--as-of="):
            as_of = arg.split("=", 1)[1].strip()

    if op == "yahoo":
        out = dispatch("/compute/yahoo", {"ticker": ticker}, None)
        sys.stdout.write(json.dumps(out, ensure_ascii=False))
        return
    if op == "rv":
        writes: list[dict] = []
        day = dt.date.fromisoformat(as_of[:10]) if as_of else market_calendar.last_completed_trading_day()
        try:
            rv = metrics.compute_realized_volatility(ticker)
        except Exception:
            rv = {}
        for w, v in (rv or {}).items():
            if v is not None and np.isfinite(v):
                writes.append({
                    "sql": (
                        "INSERT INTO realized_volatility (ticker,window_days,as_of_date,value) "
                        "VALUES (?,?,?,?) ON CONFLICT (ticker,window_days,as_of_date) "
                        "DO UPDATE SET value=EXCLUDED.value"
                    ),
                    "params": [ticker, int(w), day.isoformat(), float(v)],
                })
        sys.stdout.write(json.dumps({"writes": writes}, ensure_ascii=False))
        return
    raise SystemExit(f"Unsupported CLI op: {op}")


if __name__ == "__main__":
    _main_cli()

