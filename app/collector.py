"""Collection orchestration. Everything here is provider-agnostic.

This module used to be "the only module that talks to the external data
source". That is now app/providers/ — but the rule it protected still holds:
nothing outside app/providers/ makes network requests, and nothing inside
app/providers/ knows about the database.

What stays here is the part that is the same whichever source is active: the
quality gate, run logging, and the guarantee that one failing ticker cannot
take down the rest of the batch.
"""

from __future__ import annotations

import datetime as dt
import logging
from datetime import datetime, timezone

import pandas as pd

from app import config, db, iv_backfill, market_calendar, metrics, providers

log = logging.getLogger(__name__)


def fetch_ticker_snapshot(
    ticker: str, provider: providers.DataProvider | None = None
) -> tuple[float, pd.DataFrame]:
    """Kept as a module-level function for call sites that predate providers."""
    return (provider or providers.get_provider()).fetch_ticker_snapshot(ticker)


def fetch_price_history(
    ticker: str, period: str = "6mo", provider: providers.DataProvider | None = None
) -> pd.DataFrame:
    return (provider or providers.get_provider()).fetch_price_history(ticker, period)


def _now_utc() -> datetime:
    """Naive datetime, UTC by convention. No tzinfo — avoids tz-aware/tz-naive
    conflicts in date arithmetic with expiry dates in metrics.py, which come
    from the DB as naive (dates without time)."""
    return datetime.now(timezone.utc).replace(tzinfo=None)


class ZeroOpenInterestChain(ValueError):
    """The quality gate refused a chain whose open interest came back zeroed.

    Its own type because the same refusal means two different things. During a
    session it is a corrupted snapshot, a failure like any other. With the
    market shut it is what Yahoo serves every night — the single snapshot the
    worker takes on a closed day meets it routinely — and it is reported as
    `closed:`, logged with the guard's own words so that the run log's readers
    can tell the two apart (`db.is_closed_market_refusal`), and neither counted
    as a failure nor towards suspending the symbol.
    """

    def __init__(self, fraction: float, market_closed: bool):
        found = (
            f"{fraction:.0%} {db.ZERO_OI_REFUSAL} "
            f"(threshold {config.MAX_ZERO_OI_FRACTION:.0%})"
        )
        if market_closed:
            reason = (
                "the market is closed, and outside regular hours the data source "
                "reports open interest as zero across most of the chain; not saving, and "
                "not counted as a failure. The next session's collection will store it."
            )
        else:
            reason = (
                "during the session, so the data source returned a corrupted or incomplete "
                "snapshot; not saving."
            )
        super().__init__(f"{found} — {reason}")
        self.market_closed = market_closed


def _oi_zero_fraction(chain_df: pd.DataFrame) -> float:
    """Fraction of contracts with open_interest=0 in a freshly collected
    chain. The real-world signal for detecting corrupted data-source
    responses — see `config.MAX_ZERO_OI_FRACTION`."""
    if chain_df.empty:
        return 1.0
    return float((chain_df["open_interest"].fillna(0) == 0).mean())


def collect_watchlist(conn, tickers: list[str], provider=None) -> dict[str, str]:
    """Serialize manual/automatic passes; fallback by whole snapshot and actual source."""
    from app.resources import get_collection_lock
    lock = get_collection_lock()
    if not lock.acquire(blocking=False):
        return {ticker: "skipped: another collection is already running" for ticker in tickers}
    try:
        return _collect_watchlist(conn, tickers, provider or providers.get_provider())
    finally:
        lock.release()


def _collect_watchlist(conn, tickers, active):
    results = {}
    successes = {}
    candidates = getattr(active, "candidates", (active,))
    for ticker in tickers:
        errors = []
        for candidate in candidates:
            source = candidate.name
            if ticker.upper() in (getattr(candidate, "unsupported_symbols", {}) or {}):
                errors.append(f"{source}: unsupported instrument")
                continue
            until = db.provider_cooldown_until(conn, source=source)
            if until is not None:
                errors.append(f"{source}: cooldown until {until} UTC")
                continue
            started = _now_utc()
            rows = None
            zero_fraction = None
            try:
                spot, chain = candidate.fetch_ticker_snapshot(ticker)
                if chain.empty:
                    raise ValueError("Empty option chain")
                rows = len(chain)
                zero_fraction = _oi_zero_fraction(chain)
                if zero_fraction > getattr(candidate, "max_zero_oi_fraction", config.MAX_ZERO_OI_FRACTION):
                    raise ZeroOpenInterestChain(
                        zero_fraction, market_closed=market_calendar.state_from_clock(started) == market_calendar.CLOSED)
            except Exception as exc:
                message = _scrub(str(exc), candidate)
                status_code = getattr(getattr(exc, "response", None), "status_code", None)
                if status_code == 429 or providers.is_rate_limited(exc):
                    db.start_provider_cooldown(conn, source=source)
                db.log_run(conn, started, _now_utc(), ticker, "failed", message,
                           rows_fetched=rows, oi_zero_fraction=zero_fraction, source=source)
                errors.append(f"{source}: {message}")
                continue
            # Storage failures are not provider failures and must not switch sources.
            try:
                with conn.transaction():
                    db.insert_snapshot(conn, ticker, started, spot, chain, source=source)
                    db.log_run(conn, started, _now_utc(), ticker, "success", rows_fetched=rows,
                               oi_zero_fraction=zero_fraction, source=source)
            except Exception as exc:
                results[ticker] = f"failed: SQLite write: {exc}"
                break
            results[ticker] = "success"
            successes[ticker] = source
            iv_backfill.backfill_ticker(conn, ticker, limit=1, source=source)
            _write_day_summaries(conn, [ticker], source)
            break
        else:
            results[ticker] = "failed: " + "; ".join(errors)
    # Optional stock history is isolated from the successful option-chain pass.
    if successes and getattr(config, "FETCH_REALIZED_VOLATILITY", False):
        refresh_realized_volatility(conn, list(successes))
    return results


def _write_day_summaries(conn, tickers: list[str], source: str) -> int:
    """Store the day row for each ticker just collected. Returns rows written.

    NOTHING HERE MAY BREAK A COLLECTION PASS, for the same reason the
    volatility refresh above may not: the chains are already stored, and a row
    that cannot be built costs one view a day rather than the snapshot
    somebody actually asked for. Late import because day_summary reads
    snapshots through db, and db is imported by everything.
    """
    from app import day_summary

    written = 0
    for ticker in tickers:
        try:
            moment = db.get_collection_moments(conn, ticker, days=1, source=source)
            if not moment:
                continue
            row = day_summary.build_row(conn, ticker, source, moment[0], metrics.DEFAULT_PRICING)
            if row is None:
                continue
            db.upsert_day_summary(conn, ticker, source, row, day_summary.CODE_SHA)
            written += 1
        except Exception:  # noqa: BLE001 — a summary is not worth a failed pass
            log.error("Day summary not written for %s", ticker, exc_info=True)
    return written


def refresh_realized_volatility(
    conn, tickers: list[str], *, today: dt.date | None = None, fetch=None
) -> int:
    """Store today's realized volatility for tickers that have none. Returns rows written.

    WHY THIS IS NOT DONE WHERE IT IS SHOWN. The Contract view prints RV(10d),
    RV(20d) and RV(30d) next to the contract's implied volatility, and it used
    to get them by fetching six months of daily closes over the network in the
    middle of drawing the page — with a thirty-minute cache as the only thing
    making that tolerable. A cache is not a fix: it moves the cost to whoever
    happens to arrive after it expires, and it hides a network call inside a
    render, where a slow source is indistinguishable from a broken product.

    ONE REQUEST PER TICKER PER DAY, AND ONLY AFTER A SUCCESSFUL COLLECTION.
    Daily closes cannot change during a session, so a second fetch in the same
    day would buy nothing; asking only for tickers with no row for today is
    what keeps a fifteen-minute interval from turning into ninety-six identical
    requests. Running it from the collection pass rather than from the worker's
    own schedule is deliberate too: collection by hand with the worker switched
    off is a supported way to run this, and a figure that only appeared for
    people using the timer would be a quietly different product.

    NOTHING HERE MAY BREAK A COLLECTION PASS. The chains are already stored by
    the time this runs, and a price-history request that fails costs a caption
    on one tab — so every failure is logged and swallowed, per ticker.
    """
    if not tickers:
        return 0
    day = today or market_calendar.last_completed_trading_day()
    wanted = db.tickers_missing_realized_volatility(conn, tickers, day)
    if not wanted:
        return 0
    fetch = fetch or fetch_price_history
    source = providers.get_provider().price_history_source
    written = 0
    for ticker in wanted:
        try:
            history = fetch(ticker)
            values = metrics.realized_volatility(history) if not history.empty else {}
            written += db.store_realized_volatility(conn, ticker, day, values, source)
        except Exception as exc:  # noqa: BLE001 — a caption is not worth a failed pass
            log.warning(
                "Could not refresh realized volatility for %s (%s). "
                "The Contract tab will show the last stored figures.", ticker, exc
            )
    return written


def _scrub(message: str, provider: providers.DataProvider) -> str:
    """Strips the provider's credential out of an error message before it is
    stored.

    Yahoo has no token, so today this does nothing — it is here because the
    moment someone adds an authenticated provider it stops doing nothing.
    Collection failures are written to collection_runs and rendered verbatim in
    the interface, and HTTP client errors routinely quote the request URL. A
    key that leaks into that log is readable by anyone who can open the page.
    """
    secret = getattr(provider, "token", None)
    if not secret:
        return message
    return message.replace(secret, "***")
