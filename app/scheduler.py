"""One daemon scheduler inside Streamlit, with a process-wide SQLite owner."""
from __future__ import annotations

import atexit
import datetime as dt
import fcntl
import logging
import os
import threading
import time
from pathlib import Path

import streamlit as st

from app import config, db, market_calendar, worker
from app.resources import get_write_lock

log = logging.getLogger(__name__)
_runtime = None
_runtime_lock = threading.Lock()


class DesktopRuntime:
    def __init__(self, *, start_scheduler=True):
        path = Path(config.DATABASE_PATH).resolve()
        path.parent.mkdir(parents=True, exist_ok=True)
        self._owner = open(str(path) + ".lock", "a+")
        try:
            fcntl.flock(self._owner, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            self._owner.close()
            raise RuntimeError("This database is already open in another GammaGrid app. Close that app first.") from None
        self.stop_event = threading.Event()
        self._status_lock = threading.Lock()
        self._status = {"state": "idle", "error": None}
        self.thread = threading.Thread(target=self._run, name="gex-collector", daemon=True)
        if start_scheduler:
            self.thread.start()
        atexit.register(self.stop)

    def status(self):
        with self._status_lock:
            return dict(self._status)

    def _set_status(self, **values):
        with self._status_lock:
            self._status.update(values)

    def _run(self):
        conn = None
        due = 0.0
        previous_interval = 0
        try:
            conn = db.get_connection()
            while not self.stop_event.is_set():
                try:
                    interval = db.get_collector_interval(conn)
                    if interval != previous_interval:
                        due = 0.0
                        previous_interval = interval
                    if interval <= 0:
                        self._set_status(state="off")
                    elif time.monotonic() >= due:
                        self._set_status(state="collecting", error=None)
                        state = market_calendar.state_from_clock()
                        today = market_calendar.market_day()
                        db.set_setting(conn, worker.MARKET_STATE_KEY, state)
                        db.set_setting(conn, worker.MARKET_STATE_AT_KEY, dt.datetime.now(dt.timezone.utc).isoformat())
                        closed = state == market_calendar.CLOSED
                        if not closed or not worker.closed_market_slot_taken(conn, today):
                            worker.archive_if_due(conn)
                            collected = worker.collect_once(conn)
                            if closed and collected:
                                worker.take_closed_market_slot(conn, today)
                            worker.rebuild_stale_volume_stats(conn)
                        worker.catch_up_own_iv(conn)
                        worker.fill_missing_day_summaries(conn)
                        worker.refresh_catalogue_if_due(conn)
                        due = time.monotonic() + worker.sleep_seconds(interval)
                        self._set_status(state="waiting", last_finished=dt.datetime.now(dt.timezone.utc).isoformat())
                except Exception as exc:
                    log.exception("Background collection cycle failed")
                    self._set_status(state="retrying", error=str(exc))
                    due = time.monotonic() + 60
                self.stop_event.wait(2)
        except Exception as exc:
            log.exception("Scheduler initialization failed")
            self._set_status(state="failed", error=str(exc))
        finally:
            if conn is not None:
                conn.close()

    def stop(self):
        self.stop_event.set()
        if self.thread.is_alive() and threading.current_thread() is not self.thread:
            self.thread.join(timeout=2)
        # Keep ownership if a bounded network request is still finishing.
        if not self.thread.is_alive() and not self._owner.closed:
            self._owner.close()
        self._set_status(state="stopping" if self.thread.is_alive() else "stopped")


@st.cache_resource(show_spinner=False)
def get_runtime():
    """Cache clears reattach to the same process resource, never spawn a duplicate."""
    global _runtime
    get_write_lock()
    with _runtime_lock:
        if _runtime is None:
            _runtime = DesktopRuntime(start_scheduler=os.environ.get("GEX_DISABLE_SCHEDULER") != "1")
        return _runtime
