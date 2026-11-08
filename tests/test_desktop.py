"""Local desktop regressions. Each test owns a temporary SQLite database."""
import datetime as dt
import subprocess
import sys
import threading
from types import SimpleNamespace

import numpy as np
import pandas as pd
import pytest
import requests

from app import collector, config, db, metrics, migrate
from app.providers.cboe import CboeProvider
from app.sqlite_store import connect


@pytest.fixture
def connection(tmp_path, monkeypatch):
    monkeypatch.setattr(config, "DATABASE_PATH", tmp_path / "test.db")
    conn = db.get_connection()
    yield conn
    conn.close()


@pytest.fixture
def chain():
    at = dt.datetime.now(dt.timezone.utc).replace(tzinfo=None)
    expiry = (at + dt.timedelta(days=30)).date()
    return pd.DataFrame([
        dict(expiry=expiry, strike=strike, option_type=side, last_price=3., bid=2.9, ask=3.1,
             volume=100, open_interest=oi, implied_volatility=.25, gamma=gamma,
             delta=.5 if side == "call" else -.5, in_the_money=False,
             contract_symbol=f"TEST{expiry:%y%m%d}{side[0].upper()}{int(strike*1000):08d}")
        for side in ("call", "put") for strike, oi, gamma in ((95., 1000, .001), (100., 100, .1), (105., 50, .02))
    ])


def snapshot(chain, spot=200.):
    return chain.assign(underlying_price=spot, source="cboe", collected_at=pd.Timestamp.now(tz="UTC").tz_localize(None))


def test_native_gex_formula_walls_and_missing_iv(chain):
    frame = snapshot(chain)
    values = metrics.contract_exposure(frame)
    assert values.iloc[0] == pytest.approx(.001 * 1000 * 100 * 200**2 * .01)
    assert values.iloc[3] == -values.iloc[0]
    assert metrics.dealer_walls(frame)["call_wall"] == 95
    assert metrics.gex_walls(frame) == {"call_gex_wall": 100., "put_gex_wall": 100.}
    frame["implied_volatility"] = np.nan
    pd.testing.assert_series_equal(metrics.contract_exposure(frame), values)
    assert metrics.contract_exposure(frame, dimension="vanna").isna().all()
    frame["gamma"] = np.nan
    assert metrics.contract_exposure(frame).isna().all()
    frame["open_interest"] = 0
    assert (metrics.contract_exposure(frame) == 0).all()


@pytest.mark.parametrize("side", ["call", "put"])
@pytest.mark.parametrize("q", [0., .03])
def test_vanna_charm_against_finite_differences(side, q):
    f = metrics._black_scholes_greeks
    s, k, t, v, r, h = 103., 100., .4, .27, .04, 1e-6
    value = f(s, k, t, v, r, side, q)
    vanna = (f(s, k, t, v+h, r, side, q)["delta"] - f(s, k, t, v-h, r, side, q)["delta"]) / (2*h)
    charm = (f(s, k, t-h, v, r, side, q)["delta"] - f(s, k, t+h, v, r, side, q)["delta"]) / (2*h)
    assert value["vanna"] == pytest.approx(vanna, rel=1e-7)
    assert value["charm"] == pytest.approx(charm, rel=1e-7)
    batch = metrics._black_scholes_greeks_batch(*[pd.Series([x]) for x in (s, k, t, v, r, side)], dividend_yield=q)
    for name in ("vanna", "charm", "gamma"):
        assert batch[name].iloc[0] == pytest.approx(value[name])


@pytest.mark.parametrize("dimension", ["gex", "vanna", "charm"])
def test_matrix_scale_sum_and_expiry(chain, dimension):
    frame = snapshot(chain)
    frame.loc[frame.option_type == "put", "open_interest"] *= 2
    values = metrics.contract_exposure(frame, dimension=dimension)
    matrix = metrics.exposure_matrix(frame, dimension=dimension)
    assert matrix.sum().sum() == pytest.approx(values.sum())
    assert matrix.attrs["unit"] == metrics.EXPOSURE_UNITS[dimension]
    years = metrics.years_to_expiry(frame.expiry.iloc[0], frame.collected_at.iloc[0])
    greek = metrics._black_scholes_greeks(200., 95., years, .25, metrics.DEFAULT_PRICING.rate_series(pd.Series([years]))[0],
                                       "call", metrics.DEFAULT_PRICING.dividend_yield)
    if dimension != "gex":
        scale = .01 if dimension == "vanna" else 1/365
        assert values.iloc[0] == pytest.approx(greek[dimension] * 1000 * 100 * 200 * scale)
    frame["expiry"] = dt.date(2000, 1, 1)
    assert (metrics.contract_exposure(frame, dimension=dimension) == 0).all()


def test_cboe_single_request_parsing_and_native_values():
    payload = {"timestamp": "2026-11-06 16:24:09", "data": {"current_price": 100, "options": [
        {"option": "B1SPY261120C00100000", "gamma": .012, "delta": .55, "iv": .23,
         "open_interest": 42, "volume": 10, "bid": 2, "ask": 3, "last_trade_price": 2.5}
    ]}}
    calls = []
    response = SimpleNamespace(raise_for_status=lambda: None, json=lambda: payload)
    session = SimpleNamespace(headers={}, get=lambda url, **kw: calls.append((url, kw)) or response)
    spot, result = CboeProvider(session).fetch_ticker_snapshot("SPY")
    assert spot == 100 and len(calls) == 1
    assert calls[0][0].endswith("/SPY.json")
    assert CboeProvider.url_for("^SPX").endswith("/_SPX.json")
    assert result.iloc[0].gamma == .012 and result.iloc[0].implied_volatility == .23
    assert result.iloc[0].contract_symbol == "B1SPY261120C00100000"
    assert result.iloc[0].quote_timestamp == payload["timestamp"]
    payload["data"]["options"].append(dict(payload["data"]["options"][0]))
    with pytest.raises(ValueError, match="duplicate"):
        CboeProvider.parse(payload)


def test_fallback_and_independent_cooldowns(connection, chain):
    def unavailable(_):
        raise requests.HTTPError("rate limited", response=SimpleNamespace(status_code=429))
    cboe = SimpleNamespace(name="cboe", fetch_ticker_snapshot=unavailable)
    yahoo = SimpleNamespace(name="yahoo", fetch_ticker_snapshot=lambda _: (100., chain))
    result = collector.collect_watchlist(connection, ["TEST"], SimpleNamespace(candidates=(cboe, yahoo)))
    assert result == {"TEST": "success"}
    assert db.active_source(connection, "TEST") == "yahoo"
    assert db.provider_cooldown_until(connection, source="cboe") is not None
    assert db.provider_cooldown_until(connection, source="yahoo") is None
    runs = db.get_recent_runs(connection)
    assert set(zip(runs.source, runs.status)) == {("cboe", "failed"), ("yahoo", "success")}
    assert len(db.get_latest_snapshot(connection, "TEST")) == len(chain)


def test_atomic_snapshot_and_run_rollback(connection, chain, monkeypatch):
    original = db.log_run
    def fail(*args, **kwargs):
        raise RuntimeError("injected run log failure")
    monkeypatch.setattr(db, "log_run", fail)
    provider = SimpleNamespace(name="cboe", fetch_ticker_snapshot=lambda _: (100., chain))
    result = collector.collect_watchlist(connection, ["TEST"], provider)
    assert "SQLite write" in result["TEST"]
    for table in ("option_snapshots", "contract_registry", "snapshot_iv_summary", "collection_runs"):
        assert connection.execute(f"SELECT count(*) FROM {table}").fetchone()[0] == 0
    monkeypatch.setattr(db, "log_run", original)


def test_manual_and_scheduled_collection_are_mutually_exclusive(connection):
    from app.resources import get_collection_lock
    with get_collection_lock():
        assert collector.collect_watchlist(connection, ["TEST"])["TEST"].startswith("skipped:")


def test_native_iv_is_preserved_and_missing_cells_are_not_zero(chain):
    frame = snapshot(chain)
    solved = metrics.with_solved_iv(frame)
    pd.testing.assert_series_equal(frame.implied_volatility, solved.implied_volatility)
    frame["implied_volatility"] = np.nan
    frame["gamma"] = np.nan
    for dimension in ("gex", "vanna", "charm"):
        matrix = metrics.exposure_matrix(frame, dimension=dimension)
        assert matrix.isna().all().all()
        assert metrics.net_gex_from_matrix(matrix).net_gex.isna().all()


def test_sqlite_reads_archive_backup_and_sources(connection, chain, tmp_path):
    now = dt.datetime.now(dt.timezone.utc).replace(tzinfo=None)
    db.add_ticker(connection, "TEST")
    for n, days in enumerate((7, 6, 5, 0)):
        at = (now - dt.timedelta(days=days)).replace(hour=18)
        db.insert_snapshot(connection, "TEST", at, 100., chain.assign(volume=100+n*10), source="cboe")
        db.log_run(connection, at, at, "TEST", "success", rows_fetched=len(chain), source="cboe")
    db.insert_snapshot(connection, "TEST", now + dt.timedelta(days=1), 102., chain, source="yahoo")
    db.log_run(connection, now + dt.timedelta(days=1), now + dt.timedelta(days=1), "TEST", "success",
               rows_fetched=len(chain), source="yahoo")
    assert db.active_source(connection, "TEST") == "yahoo"
    assert len(db.get_latest_snapshot(connection, "TEST", source="cboe")) == len(chain)
    assert set(db.get_snapshots(connection, "TEST", source="cboe").source) == {"cboe"}
    assert not db.get_put_call_ratio(connection, "TEST", source="cboe").empty
    assert len(db.get_collection_moments(connection, "TEST", source="cboe")) == 4
    assert not db.get_iv_weighted_average(connection, "TEST", source="cboe").empty
    db.rebuild_volume_stats(connection, "TEST", source="cboe")
    assert not db.get_volume_stats(connection, "TEST", source="cboe").empty
    old = chain.assign(expiry=dt.date(2000, 1, 1))
    db.insert_snapshot(connection, "OLD", now, 100., old, source="cboe")
    assert db.archive_expired_contracts(connection) == len(chain)
    assert len(db.get_latest_snapshot(connection, "OLD")) == len(chain)
    target = tmp_path / "backup.db"
    db.backup_database(connection, target)
    restored = connect(target)
    assert restored.execute("PRAGMA integrity_check").fetchone()[0] == "ok"
    assert restored.execute("SELECT count(*) FROM option_snapshots").fetchone()[0] == len(chain)*5
    restored.close()


def test_concurrent_writers_and_single_instance(connection):
    from app.scheduler import DesktopRuntime
    errors = []
    def write(label):
        conn = db.get_connection()
        try:
            for i in range(20):
                with conn.transaction():
                    db.set_setting(conn, f"{label}-{i}", "ok")
        except Exception as exc:
            errors.append(exc)
        finally:
            conn.close()
    threads = [threading.Thread(target=write, args=(str(i),)) for i in range(3)]
    for thread in threads:
        thread.start()
    for thread in threads:
        thread.join(10)
    assert not errors and not any(t.is_alive() for t in threads)
    assert connection.execute("SELECT count(*) FROM app_settings").fetchone()[0] == 60
    runtime = DesktopRuntime(start_scheduler=False)
    try:
        script = "import fcntl,sys; f=open(sys.argv[1], 'a+'); fcntl.flock(f, fcntl.LOCK_EX|fcntl.LOCK_NB)"
        result = subprocess.run([sys.executable, "-c", script, str(config.DATABASE_PATH)+".lock"], capture_output=True)
        assert result.returncode != 0 and b"BlockingIOError" in result.stderr
    finally:
        runtime.stop()


def test_migration_reopen_and_checksum(connection):
    migrate.ensure_current(connection)
    connection.execute("UPDATE schema_migrations SET checksum='wrong'")
    with pytest.raises(RuntimeError):
        migrate.ensure_current(connection)


def test_dashboard_empty_all_views_and_heatmap_dimensions(connection, chain, monkeypatch):
    from streamlit.testing.v1 import AppTest

    from app import scheduler
    monkeypatch.setattr(scheduler, "get_runtime", lambda: SimpleNamespace(status=lambda: {"state": "test", "error": None}))
    app = AppTest.from_file(str(config.PROJECT_ROOT / "app/dashboard.py"), default_timeout=30).run()
    assert not app.exception
    db.add_ticker(connection, "TEST")
    now = dt.datetime.now(dt.timezone.utc).replace(tzinfo=None)
    for days in (3, 2, 1, 0):
        at = now - dt.timedelta(days=days)
        db.insert_snapshot(connection, "TEST", at, 100., chain.assign(volume=100+days*25), source="cboe")
        db.log_run(connection, at, at, "TEST", "success", rows_fetched=len(chain), source="cboe")
    for view in ("工作台", "波动率", "期权链", "资金流"):
        app.session_state["active_view"] = view
        app.run()
        assert not app.exception, (view, [e.value for e in app.exception])
    app.session_state["active_view"] = "工作台"
    for dimension, label in (("gex", "GEX"), ("vanna", "Vanna"), ("charm", "Charm")):
        app.session_state["exposure_dimension"] = dimension
        app.run()
        assert not app.exception, (dimension, [e.value for e in app.exception])
        assert any(label in m.value for m in app.markdown)
        assert any("Call 墙" in m.value and "Put 墙" in m.value for m in app.markdown)


def test_cached_scheduler_survives_reruns_and_cache_clear(connection, monkeypatch):
    from app import scheduler
    scheduler.get_runtime.clear()
    monkeypatch.setattr(scheduler, "_runtime", None)
    runtime = scheduler.get_runtime()
    try:
        assert runtime.thread.daemon and runtime.thread.is_alive()
        assert scheduler.get_runtime() is runtime
        scheduler.get_runtime.clear()
        assert scheduler.get_runtime() is runtime
        assert sum(t.name == "gex-collector" for t in threading.enumerate()) == 1
    finally:
        runtime.stop()
        scheduler.get_runtime.clear()
    assert not runtime.thread.is_alive()
