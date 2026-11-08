"""Maintenance helpers called by the in-process desktop scheduler. No worker process."""

from __future__ import annotations

import datetime as dt
import logging

from app import (
    catalogue,
    collector,
    config,
    day_summary,
    db,
    iv_backfill,
    market_calendar,
    providers,
)

log = logging.getLogger("worker")

# How long to wait before asking again when collection is switched off. Short
# enough that turning it on in the dashboard feels immediate, long enough that
# an idle worker is not a busy loop.
IDLE_POLL_SECONDS = 60

_ARCHIVE_MARKER = "last_archive_pass_date"
_DAY_SUMMARY_MARKER = "last_day_summary_backfill_date"


# Which trading day the single closed-market collection has been spent on, and
# what the collector last believed about the market. Both live in app_settings
# rather than in memory: the worker restarts on every `docker compose up`, and a
# marker that forgets on restart would collect again on each one.
CLOSED_DAY_KEY = "last_closed_market_collection"
MARKET_STATE_KEY = "market_state"
MARKET_STATE_AT_KEY = "market_state_at"

# How long to wait when our clock says the session is running and the provider
# still says it is not. Short, because this is the one window where the two
# genuinely disagree and the answer changes within a minute. Confirmed on the
# sibling product: the first collection of the day landed at 09:31, exactly one
# poll after the bell.
DISAGREEMENT_POLL_SECONDS = 60


def closed_market_slot_taken(conn, day) -> bool:
    return db.get_setting(conn, CLOSED_DAY_KEY) == day.isoformat()


def take_closed_market_slot(conn, day) -> None:
    db.set_setting(conn, CLOSED_DAY_KEY, day.isoformat())


def sleep_seconds(interval_minutes: int, now: dt.datetime | None = None,
                  provider_state: str | None = None) -> float:
    """How long to wait before the next cycle.

    Normally one interval; never longer than the time left until the market
    opens. Written as a `min` on purpose: working out when the market opens is
    a calculation that can be wrong, and capped by the interval a wrong answer
    can only make the worker wake EARLIER than it does today. There is no input
    for which this waits longer than before.

    The disagreement case is the one measured in anger. Waking exactly at the
    bell means asking the provider at the worst possible instant — it has not
    flipped yet, says "closed", and a full interval of sleep follows. So when
    our clock says the session is running and the provider still says it is
    not, the wait drops to a minute. The provider stays authoritative: on a
    holiday it keeps saying closed and nothing is collected, at the price of one
    status call a minute for one day.
    """
    full = interval_minutes * 60
    if provider_state == market_calendar.CLOSED and \
            market_calendar.state_from_clock(now) == market_calendar.OPEN:
        return min(full, DISAGREEMENT_POLL_SECONDS)
    return min(full, max(30.0, market_calendar.seconds_until_open(now) + 5))


def collect_once(conn) -> int:
    """One pass over the watchlist. Returns how many tickers were collected."""
    tickers = db.get_watchlist(conn)
    if not tickers:
        log.info("Watchlist is empty — nothing to collect.")
        return 0
    provider = providers.get_provider()
    results = collector.collect_watchlist(conn, tickers, provider=provider)
    return sum(status == "success" for status in results.values())


def archive_if_due(conn) -> int:
    """Move long-expired contracts out of the hot table, at most once a day.

    Guarded by a date written to app_settings rather than by a timer, so that a
    machine which is switched off overnight — which is most machines this runs
    on — still archives on its next start instead of only when it happens to be
    awake at the right moment.
    """
    today = dt.date.today().isoformat()
    if db.get_setting(conn, _ARCHIVE_MARKER) == today:
        return 0
    moved = db.archive_expired_contracts(conn)
    db.set_setting(conn, _ARCHIVE_MARKER, today)
    if moved:
        log.info(
            "Archived %s row(s) for contracts expired more than %s day(s) ago.",
            moved, config.CONTRACT_ARCHIVE_GRACE_DAYS,
        )
    return moved


def rebuild_stale_volume_stats(conn) -> None:
    """Recompute the Unusual Activity baseline for any ticker whose stored one
    no longer covers the closed days.

    Here rather than on a page view because it is an aggregate over one
    snapshot per day per contract — work that belongs off the path where
    somebody is waiting. Asked per ticker and skipped when already current, so
    a machine that was off when the day rolled over catches up on its next pass
    instead of waiting for a scheduler nobody watches.
    """
    for ticker in db.get_watchlist(conn):
        if db.volume_stats_are_current(conn, ticker):
            continue
        written = db.rebuild_volume_stats(conn, ticker)
        log.info("Rebuilt volume statistics for %s: %s contract(s).", ticker, written)


def fill_missing_day_summaries(conn) -> int:
    """Build the day rows that are absent, at most once a day.

    THE HOLE THIS FILLS IS INVISIBLE, which is why it is filled without being
    asked for. A machine switched off for a week, an upgrade that introduces
    these rows for the first time, a formula that changed — all leave the same
    gap, and a gap in a view about what changed cannot be told apart from
    nothing having changed. Bounded by a day marker and by how far back it
    looks, so a machine that has been running costs one query per ticker.
    """
    today = dt.date.today().isoformat()
    if db.get_setting(conn, _DAY_SUMMARY_MARKER) == today:
        return 0
    written = 0
    for ticker in db.get_watchlist(conn):
        source = db.active_source(conn, ticker)
        if source is None:
            continue
        written += day_summary.backfill(
            conn, source, [ticker], config.DAY_SUMMARY_BACKFILL_DAYS
        )
    db.set_setting(conn, _DAY_SUMMARY_MARKER, today)
    if written:
        log.info("Filled in %s missing day summary row(s).", written)
    return written


def refresh_catalogue_if_due(conn) -> int:
    """Keep the Add-ticker search box's directory current. Returns symbols loaded.

    Here rather than in the app for the same reason collection is: Streamlit
    re-executes its whole script on every interaction, so a download started
    from a page render is a download started again on every click. The worker
    asks once a cycle and answers "not yet" from one setting almost every time.

    It is also the only outbound call in this product that is not to the data
    source, which is why it is bounded and visible rather than convenient: a
    weekly cadence, a marker written even when the attempt fails, and a
    complete degradation path — no catalogue means the box is a plain text
    field, which is what it was before it existed.
    """
    return catalogue.refresh_if_due(conn)


def catch_up_own_iv(conn) -> int:
    """Move a batch of stored volatility averages onto our own model.

    THE UPGRADE PATH FOR ONE NUMBER, and the reason it is a background task
    rather than a documented command. Every screen already solves volatility
    from the contract's price as it draws; the volume-weighted average per
    collection is stored, and every row written before this release holds the
    data source's number instead. A step in the middle of that chart is a
    question every reader has to be told the answer to, and a command in
    UPGRADING.md is a command almost nobody runs.

    So it happens here, bounded, until there is nothing left — after which this
    costs one index-only read per pass against an index that is empty. A
    machine switched off for a month catches up on its next start, which is the
    same property archiving and the volume baseline already rely on.

    Returns how many averages were rewritten, for the log.
    """
    rewritten = iv_backfill.backfill_watchlist(conn)
    if rewritten:
        log.info(
            "Recomputed %s stored volatility average(s) with our own model.", rewritten
        )
    return rewritten



if __name__ == "__main__":
    raise SystemExit("Standalone worker removed. Start the app with ./scripts/start-local.sh")
