"""GammaGrid Terminal — the Streamlit UI.

Display and user input only: calculations live in metrics.py, collection in
collector.py, presentation primitives in ui.py and theme.py.

LAYOUT, top to bottom:
  1. Command bar   — brand · ticker switcher · data status · live toggle · collect
  2. Level ribbon  — one row: regime, spot, net GEX, flip, walls, max pain, 1σ, IV, P/C
  3. Workspace nav — 工作台 / 波动率 / 期权链 / 资金流
  4. The workspace — the exposure workbench opens by default.
Management (watchlist edits, schedules, logs) lives in the sidebar, which is
collapsed once there is something to look at.

ONE VIEW RENDERS PER RUN. Streamlit has no lazy tabs, so the workspaces are a
segmented control and module-level `if active_view == "...":` blocks — each run
pays for one view. tests/render_views.py checks no view reads a name only
another view assigns.
"""

from __future__ import annotations

import datetime as dt

import numpy as np
import pandas as pd
import streamlit as st

from app import (  # noqa: I001 — grouped by what they are, not alphabetised
    catalogue,
    collector,
    config,
    day_summary,
    db,
    freshness,
    market_calendar,
    metrics,
    providers,
    suggestions,
    theme,
    ui,
)
from app.scheduler import get_runtime
from app.viewtime import (
    format_datetime,
    strikes_around_money,
    timezone_label,
    to_viewer,
    with_viewer_index,
)

FAVICON = (
    "data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E"
    "%3Crect width='32' height='32' rx='6' fill='%2307090C'/%3E"
    "%3Crect x='5' y='5' width='10' height='10' rx='2' fill='%23FF5C93'/%3E"
    "%3Crect x='17' y='5' width='10' height='10' rx='2' fill='%231A2230'/%3E"
    "%3Crect x='5' y='17' width='10' height='10' rx='2' fill='%231A2230'/%3E"
    "%3Crect x='17' y='17' width='10' height='10' rx='2' fill='%232FD6A0'/%3E%3C/svg%3E"
)

VIEWS = ["工作台", "波动率", "期权链", "资金流"]
VIEW_ICONS = {"工作台": "▦ 工作台", "波动率": "◠ 波动率", "期权链": "☰ 期权链", "资金流": "⇅ 资金流"}
LIVE_REFRESH_SECONDS = 30


@st.cache_resource(scope="session", on_release=lambda connection: connection.close())
def session_connection():
    return db.get_connection()


runtime = get_runtime()
conn = session_connection()
watchlist = db.get_watchlist(conn)

st.set_page_config(
    page_title="GammaGrid Terminal",
    page_icon=FAVICON,
    layout="wide",
    initial_sidebar_state="collapsed" if watchlist else "expanded",
)
st.markdown(theme.stylesheet(), unsafe_allow_html=True)


# ==========================================================================
# helpers
# ==========================================================================
def _drop_stale_choice(key: str, options) -> None:
    """Forget a stored selection that is no longer offered — Streamlit raises
    when a widget's remembered value is absent from its options."""
    if key in st.session_state and st.session_state[key] not in list(options):
        del st.session_state[key]


@st.cache_data(ttl=300)
def _cached_catalogue_options(_conn, watched: tuple[str, ...]) -> list[tuple[str, str]]:
    return catalogue.options(_conn, list(watched))


@st.cache_data(ttl=1800)
def _cached_has_options(ticker: str) -> bool | None:
    """Does this symbol have a chain to collect. None = the source could not
    answer, and a ticker is never refused on that."""
    check = getattr(providers.get_provider(), "underlying_has_options", None)
    return None if check is None else check(ticker)


@st.cache_data(ttl=1800)
def _cached_collection_depth() -> dict[str, dt.date]:
    depth_conn = db.get_connection()
    try:
        return db.collection_depth(depth_conn)
    finally:
        depth_conn.close()


def html(markup: str) -> None:
    st.markdown(markup, unsafe_allow_html=True)


def plot(fig, key: str) -> None:
    st.plotly_chart(fig, config=ui.PLOT_CONFIG, key=key, width="stretch")


def time_left(expiry, moment) -> bool:
    return metrics.years_to_expiry(expiry, moment) > 0


# ==========================================================================
# sidebar — management only
# ==========================================================================
def render_sidebar() -> int | None:
    """Watchlist edits, collection controls and logs. Returns the history
    window in days (None = everything)."""
    with st.sidebar:
        html(ui.brand())
        html('<div class="gg-side-h">添加标的</div>')
        options = _cached_catalogue_options(conn, tuple(watchlist))
        label_to_symbol = {label: symbol for symbol, label in options}
        catalogue_symbols = set(label_to_symbol.values())
        # One box that filters as you type, over the whole catalogue in the
        # browser; `accept_new_options` lets an unlisted symbol through.
        choice = st.selectbox(
            "添加标的", options=list(label_to_symbol), index=None, placeholder="代码或公司名，如 SPY / Apple",
            accept_new_options=True, filter_mode="contains", key="add_ticker_choice",
            label_visibility="collapsed",
            help="仅美股期权。欧洲/亚洲上市的股票请使用其美国 ADR（如 ASML、SAP、NVO）。",
        )
        new_ticker = (label_to_symbol.get(choice) or (choice or "")).strip().upper()
        if not catalogue_symbols:
            st.caption("代码目录尚未下载，暂不能按公司名搜索 — 请直接输入代码。")

        depth = _cached_collection_depth()
        provider = providers.get_provider()
        unsupported = getattr(provider, "unsupported_symbols", {}) or {}
        proposal = suggestions.propose(
            new_ticker, depth.keys(), in_catalogue=catalogue_symbols.__contains__,
            unsupported=unsupported, provider=provider.name,
        )
        if proposal:
            st.warning(proposal.message)
        if new_ticker and not (proposal and proposal.blocking):
            since = depth.get(new_ticker)
            st.caption(f"自 {since:%Y-%m-%d} 起已有数据" if since else "新标的的历史从首次采集开始积累")

        if st.button("＋ 加入自选", disabled=not new_ticker or bool(proposal and proposal.blocking),
                     width="stretch", type="primary"):
            # The live check still runs: the directory says a symbol has options
            # somewhere, not that the source will serve its chain.
            if _cached_has_options(new_ticker) is False:
                st.error(
                    suggestions.source_refusal(new_ticker, unsupported.get(new_ticker), provider.name)
                    if new_ticker in catalogue_symbols
                    else suggestions.refusal(new_ticker, watchlist, catalogue_symbols.__contains__)
                )
            else:
                db.add_ticker(conn, new_ticker)
                _cached_collection_depth.clear()
                _cached_catalogue_options.clear()
                st.session_state["selected_ticker"] = new_ticker
                st.rerun()

        if watchlist:
            html('<div class="gg-side-h">自选列表</div>')
            for ticker in watchlist:
                name_col, remove_col = st.columns([5, 1], vertical_alignment="center")
                name_col.markdown(
                    f'<span class="gg-mono" style="font-size:13px;color:{theme.TEXT}">{ticker}</span>'
                    + (f' <span class="gg-tag">自 {depth[ticker]:%m-%d}</span>' if ticker in depth else ""),
                    unsafe_allow_html=True,
                )
                if remove_col.button("✕", key=f"remove_{ticker}", help=f"从自选移除 {ticker}（数据保留）"):
                    db.remove_ticker(conn, ticker)
                    if st.session_state.get("selected_ticker") == ticker:
                        st.session_state.pop("selected_ticker", None)
                        st.session_state.pop("_last_ticker", None)
                    st.rerun()

        html('<div class="gg-side-h">采集</div>')
        if st.button("⟳ 立即采集全部", disabled=not watchlist, width="stretch",
                     help="最好在美股期权交易时段内运行；休市时源数据的 OI 常为 0，这类快照不会保存。"):
            with st.spinner("采集中…"):
                results = collector.collect_watchlist(conn, watchlist)
            for ticker, outcome in results.items():
                if outcome == "success":
                    st.success(f"{ticker}: 成功")
                elif outcome.startswith(("skipped", "closed")):
                    st.info(f"{ticker}: {outcome}")
                else:
                    st.error(f"{ticker}: {outcome}")

        current_interval = db.get_collector_interval(conn)
        labels = list(config.COLLECTOR_INTERVAL_CHOICES)
        current_label = next(
            (name for name, minutes in config.COLLECTOR_INTERVAL_CHOICES.items() if minutes == current_interval),
            labels[0],
        )
        chosen = st.selectbox(
            "自动采集频率", labels, index=labels.index(current_label),
            help="在本地应用运行期间由后台线程执行；退出应用或 Mac 休眠时停止。休市期间每天只保留一次快照。",
        )
        chosen_minutes = config.COLLECTOR_INTERVAL_CHOICES[chosen]
        if chosen_minutes != current_interval:
            db.set_collector_interval(conn, chosen_minutes)
            st.rerun()
        if chosen_minutes > 0:
            rows_by_ticker = db.rows_per_pass_by_ticker(conn)
            growth = db.estimated_growth_mb_per_month(conn, chosen_minutes, rows_by_ticker)
            st.caption(f"预计磁盘增长 ≈ {growth / 1000:.1f} GB / 月" if growth > 0 else "采集一次后可估算磁盘占用")

        state = runtime.status()
        cooldowns = []
        for source_name in ("cboe", "yahoo"):
            until = db.provider_cooldown_until(conn, source=source_name)
            if until is not None:
                cooldowns.append(f"{source_name.title()} 冷却至 {until:%H:%M} UTC")
        st.caption(
            f"后台采集器：**{state['state']}** · 数据源 Cboe 全链优先，Yahoo 兜底（延迟行情）"
            + ("".join(f" · ⚠️ {c}" for c in cooldowns))
        )
        if state.get("error"):
            st.warning(state["error"])

        html('<div class="gg-side-h">设置</div>')
        full = st.checkbox(f"加载完整历史（默认近 {config.SNAPSHOT_HISTORY_DAYS} 天）", key="load_full_history")

        with st.expander("采集日志"):
            runs = db.get_recent_runs(conn, limit=30)
            if runs.empty:
                st.caption("暂无采集记录")
            else:
                shown = runs.copy()
                shown["started_at"] = to_viewer(shown["started_at"]).dt.strftime("%m-%d %H:%M:%S")
                shown["oi_zero_fraction"] = shown["oi_zero_fraction"].map(
                    lambda v: f"{v:.0%}" if pd.notna(v) else "—")
                st.dataframe(
                    shown[["started_at", "ticker", "source", "status", "rows_fetched", "oi_zero_fraction",
                           "error_message"]].rename(columns={
                        "started_at": "开始", "ticker": "标的", "source": "源", "status": "状态",
                        "rows_fetched": "行数", "oi_zero_fraction": "OI=0", "error_message": "信息"}),
                    hide_index=True, width="stretch", height=260,
                )
                st.caption(f"时间为本地时区（{timezone_label()}）。OI=0 占比超过 "
                           f"{config.MAX_ZERO_OI_FRACTION:.0%} 的快照视为可疑，不保存。")
        st.caption(f"GammaGrid {config.APP_VERSION}")
    return None if full else config.SNAPSHOT_HISTORY_DAYS


history_days = render_sidebar()

if not watchlist:
    html(ui.brand())
    html(ui.empty(
        "从添加第一个标的开始",
        "在左侧边栏搜索代码或公司名（如 SPY、QQQ、NVDA），加入自选后点击「立即采集全部」，"
        "或开启自动采集。首个快照到达后，这里就是你的 GEX 工作台。",
    ))
    st.stop()


# ==========================================================================
# command bar
# ==========================================================================
if st.session_state.get("selected_ticker") not in watchlist:
    st.session_state["selected_ticker"] = st.session_state.get("_last_ticker") \
        if st.session_state.get("_last_ticker") in watchlist else watchlist[0]


def _keep_ticker() -> None:
    """A segmented control deselects when its active pill is clicked again;
    that gesture never means "show nothing"."""
    if st.session_state.get("selected_ticker") is None:
        st.session_state["selected_ticker"] = st.session_state.get("_last_ticker", watchlist[0])


bar_brand, bar_tickers, bar_status, bar_live, bar_collect = st.columns(
    [1.3, 4.4, 3.6, 0.75, 0.55], vertical_alignment="center", gap="small")
bar_brand.markdown(ui.brand(), unsafe_allow_html=True)
with bar_tickers:
    if len(watchlist) <= 10:
        st.segmented_control("标的", watchlist, key="selected_ticker", on_change=_keep_ticker,
                             label_visibility="collapsed")
    else:
        st.selectbox("标的", watchlist, key="selected_ticker", label_visibility="collapsed")
selected_ticker = st.session_state.get("selected_ticker") or watchlist[0]
st.session_state["_last_ticker"] = selected_ticker
live = bar_live.toggle("实时", key="live_refresh",
                       help=f"每 {LIVE_REFRESH_SECONDS} 秒检查一次新快照，有新数据时自动刷新界面（需开启自动采集）。")
if bar_collect.button("⟳", key="collect_one", help=f"立即采集 {selected_ticker}"):
    with st.spinner(f"采集 {selected_ticker}…"):
        outcome = collector.collect_watchlist(conn, [selected_ticker]).get(selected_ticker, "")
    st.session_state["_flash"] = (selected_ticker, outcome)
    st.rerun()
if "_flash" in st.session_state:
    flashed, outcome = st.session_state.pop("_flash")
    st.toast(f"{flashed}: {'采集成功' if outcome == 'success' else outcome}",
             icon="✅" if outcome == "success" else "⚠️")

# ONE SOURCE PER PAGE: two providers calculate IV differently, and mixing them
# on one chart would draw moves that never happened (see db.active_source).
page_source = db.active_source(conn, selected_ticker)


def with_our_iv(frame):
    """Every chain this page draws goes through here: IV solved from the price,
    the source's own number kept where the price determines none."""
    return metrics.with_solved_iv(frame, metrics.DEFAULT_PRICING, ticker=selected_ticker)


latest_df = with_our_iv(db.get_latest_snapshot(conn, selected_ticker, source=page_source))

# How old this screen is — read from what the collector wrote, never asked of
# the provider during a render.
_interval = max(db.get_collector_interval(conn), 15)
_last_success, _failures = db.collection_state(conn, selected_ticker, source=page_source)
_fresh = freshness.assess(_last_success, _failures, _interval)
_market_note = market_calendar.status_note(
    db.get_setting(conn, "market_state"), db.get_setting(conn, "market_state_at"), _interval)
_dot = "ok"
_tip = _market_note or ""
if _fresh:
    _dot = {freshness.RESTING: "rest", freshness.STALE: "warn", freshness.FAILING: "bad"}.get(_fresh[0], "ok")
    _tip = _fresh[1]

latest_date = latest_df["collected_at"].max() if not latest_df.empty else None
_status_items = [f'<span class="gg-tag">{(page_source or "—").upper()}</span>']
if latest_date is not None:
    _status_items.append(f"快照 <b>{format_datetime(latest_date)}</b>")
    _status_items.append(f"<b>{latest_df[['expiry', 'strike', 'option_type']].drop_duplicates().shape[0]:,}</b> 合约")
_status_items.append("延迟行情")
bar_status.markdown(ui.status(_status_items, _dot, _tip), unsafe_allow_html=True)

_refused = getattr(providers.get_provider(), "unsupported_symbols", {}) or {}
if selected_ticker in _refused:
    st.error(suggestions.source_refusal(selected_ticker, _refused[selected_ticker],
                                        providers.get_provider().name), icon="⛔")

if _fresh and _fresh[0] in (freshness.STALE, freshness.FAILING):
    html(ui.banner(f"⚠ {ui.esc(_fresh[1])}", "bad" if _fresh[0] == freshness.FAILING else "warn"))

if latest_df.empty:
    if not (_fresh and _fresh[0] == freshness.FAILING):
        html(ui.empty(f"{selected_ticker} 还没有数据",
                      "点击右上角 ⟳ 立即采集，或在侧边栏开启自动采集。"))
    st.stop()

expiries = sorted(latest_df["expiry"].unique())
live_expiries = [e for e in expiries if time_left(e, latest_date)]


# ==========================================================================
# level ribbon — the near term (≤30 days), one row, every view
# ==========================================================================
def ribbon_tiles() -> list[str]:
    reading = metrics.gamma_weather(latest_df)
    tiles = [ui.regime_tile(reading)]
    spot = float(latest_df["underlying_price"].iloc[0])
    tiles.append(ui.tile("现价", ui.price(spot), selected_ticker, "gg-spot"))
    if reading:
        chosen = [pd.Timestamp(e) for e in reading["expiries"]]
        gwalls = metrics.gex_walls(latest_df, expiries=chosen)
        net = reading["net_gex"]
        tiles.append(ui.tile("净 GEX", ui.compact(net, signed=True), "USD / 1%", ui.tone(net),
                             tip="近月到期日合计的做市商 Gamma 敞口（每 1% 标的变动的美元对冲量）"))
        flip = reading.get("gamma_flip")
        tiles.append(ui.tile(
            "Gamma 翻转", ui.price(flip) if flip else "—",
            f"距现价 {reading['flip_distance_pct']:.1f}%" if flip else "25% 内无", "gg-flip"))
        tiles.append(ui.tile("Call 墙", ui.strike_label(gwalls["call_gex_wall"]),
                             f"OI 墙 {ui.strike_label(reading['call_wall'])}", "gg-pos",
                             tip="最大正 GEX 行权价；副行为最大 Call 持仓行权价"))
        tiles.append(ui.tile("Put 墙", ui.strike_label(gwalls["put_gex_wall"]),
                             f"OI 墙 {ui.strike_label(reading['put_wall'])}", "gg-neg",
                             tip="最大负 GEX 行权价；副行为最大 Put 持仓行权价"))
    if live_expiries:
        near = live_expiries[0]
        pain = metrics.max_pain(latest_df, near)
        tiles.append(ui.tile("Max Pain", ui.strike_label(pain), ui.expiry_short(near), "gg-vol"))
        move = metrics.expected_move(latest_df, near)
        if move:
            tiles.append(ui.tile("±1σ 预期", f"±{move['move']:,.2f}",
                                 f"{move['move_pct']:.2f}% · {ui.expiry_short(near)}",
                                 tip=f"区间 {move['lower']:,.2f} – {move['upper']:,.2f}（约 68% 概率）"))
            tiles.append(ui.tile("ATM IV", ui.pct(move["implied_volatility"]), ui.expiry_short(near), "gg-vol"))
    by_type = latest_df.groupby("option_type")[["open_interest", "volume"]].sum()
    if {"call", "put"} <= set(by_type.index) and by_type.loc["call", "open_interest"] > 0:
        pcr_oi = by_type.loc["put", "open_interest"] / by_type.loc["call", "open_interest"]
        pcr_vol = (by_type.loc["put", "volume"] / by_type.loc["call", "volume"]
                   if by_type.loc["call", "volume"] > 0 else np.nan)
        tiles.append(ui.tile("P/C 持仓", f"{pcr_oi:.2f}", f"成交量 {pcr_vol:.2f}" if pd.notna(pcr_vol) else ""))
    return tiles


html(ui.ribbon(ribbon_tiles()))

# ==========================================================================
# workspace navigation
# ==========================================================================
with st.container(key="gg_nav"):
    active_view = st.segmented_control(
        "视图", VIEWS, default=VIEWS[0], key="active_view", format_func=VIEW_ICONS.get,
        label_visibility="collapsed",
    ) or VIEWS[0]

_tracked_cache: dict[str, pd.DataFrame] = {}


def get_tracked() -> pd.DataFrame:
    if "df" not in _tracked_cache:
        _tracked_cache["df"] = db.get_tracked_contracts(conn, selected_ticker)
    return _tracked_cache["df"]


def render_contract(expiry, strike: float, option_type: str, history_window: int | None, key: str) -> None:
    """Price / IV / greeks / attribution for one contract."""
    is_expired = pd.Timestamp(expiry) < pd.Timestamp(dt.date.today())
    contract_df = with_our_iv(db.get_contract_history(
        conn, selected_ticker, expiry, strike, option_type,
        days=None if is_expired else history_window, source=page_source,
    ))
    history = metrics.contract_greeks_history(contract_df, strike, expiry, option_type)
    tracked = get_tracked()
    pinned = tracked[(tracked["expiry"] == pd.Timestamp(expiry)) & (tracked["strike"] == strike)
                     & (tracked["option_type"] == option_type)] if not tracked.empty else tracked

    name = f"{selected_ticker} {pd.Timestamp(expiry):%y%m%d} {strike:g}{option_type[0].upper()}"
    facts = []
    if not history.empty:
        last = history.iloc[-1]
        facts = [("价格", ui.price(last["last_price"])), ("IV", ui.pct(last["implied_volatility"])),
                 ("Delta", f"{last['delta']:.3f}" if pd.notna(last["delta"]) else "—"),
                 ("Gamma", f"{last['gamma']:.4f}" if pd.notna(last["gamma"]) else "—"),
                 ("Theta", f"{last['theta']:.3f}" if pd.notna(last["theta"]) else "—"),
                 ("Vega", f"{last['vega']:.3f}" if pd.notna(last["vega"]) else "—"),
                 ("DTE", str(ui.dte(expiry, latest_date)) if not is_expired else "已到期"),
                 ("采集点", str(len(history)))]
    head, pin = st.columns([10, 1.4], vertical_alignment="center")
    head.markdown(
        f'<div class="gg-contract"><span class="gg-cname">{name}</span><dl>'
        + "".join(f"<div><dt>{k}</dt><dd>{v}</dd></div>" for k, v in facts) + "</dl></div>",
        unsafe_allow_html=True,
    )
    if pinned.empty:
        if pin.button("📌 固定", key=f"{key}_pin", width="stretch"):
            db.add_tracked_contract(conn, selected_ticker, pd.Timestamp(expiry), strike, option_type)
            st.rerun()
    elif pin.button("取消固定", key=f"{key}_unpin", width="stretch"):
        db.remove_tracked_contract(conn, int(pinned.iloc[0]["id"]))
        st.rerun()

    if history.empty:
        html(ui.empty("该合约暂无历史", "它可能刚进入期权链，下一次采集后出现。"))
        return
    viewer = with_viewer_index(history)
    left, right = st.columns(2)
    with left:
        html(ui.header("价格与 IV", f"{len(history)} 个采集点"))
        plot(ui.price_iv_figure(viewer), f"{key}_price")
    with right:
        attribution = metrics.greek_attribution(history)
        html(ui.header("价格归因", "Greeks 拆解 · 美分/股"))
        if attribution.by_day.empty:
            reason = (f"该合约有 {attribution.dropped_cheap} 天价格低于 "
                      f"{config.ATTRIBUTION_MIN_PRICE * 100:.0f}¢，拆解会是噪声。"
                      if attribution.dropped_cheap else
                      "需要至少两个间隔一天以上、且 IV 通过质量检查的交易日。")
            html(ui.empty("归因数据积累中", reason))
        else:
            totals = attribution.totals
            steps = [totals[n] * 100 for n in (*metrics.ATTRIBUTION_TERMS, "residual")]
            plot(ui.waterfall_figure(attribution.start_price * 100, steps), f"{key}_waterfall")
            share = abs(totals["residual"]) / max(abs(totals["actual"]), 1e-9)
            if share > 0.5:
                st.caption(f"⚠ 残差占总变动 {share:.0%}：价差、陈旧成交或利率变化的影响大于 Greeks。")
    html(ui.header("Greeks 走势"))
    plot(ui.greeks_grid(viewer), f"{key}_greeks")
    stored_rv = db.get_realized_volatility(conn, selected_ticker)
    if stored_rv:
        as_of, rv, rv_source = stored_rv
        st.caption(
            f"合约 IV {ui.pct(history.iloc[-1]['implied_volatility'])} · 标的已实现波动率（{rv_source}，"
            f"{as_of:%Y-%m-%d}）：" + " · ".join(f"RV{w}d {v:.1%}" for w, v in sorted(rv.items()))
        )


# ==========================================================================
# ▦ 工作台 — exposure heatmap + strike profile + expiry board
# ==========================================================================
if active_view == "工作台":
    moments = [pd.Timestamp(m) for m in db.get_collection_moments(
        conn, selected_ticker, days=history_days, source=page_source)] or [pd.Timestamp(latest_date)]
    moments_asc = sorted(set(moments))
    newest = moments_asc[-1]
    # Following "latest" across a new collection: a reader sitting on the
    # newest snapshot stays on the newest one when another arrives.
    if st.session_state.get("_replay_newest") != newest:
        if st.session_state.get("heatmap_as_of") in (None, st.session_state.get("_replay_newest")):
            st.session_state["heatmap_as_of"] = newest
        st.session_state["_replay_newest"] = newest
    _drop_stale_choice("heatmap_as_of", moments_asc)

    c_dim, c_side, c_exp, c_band, c_time = st.columns([1.35, 1.2, 1.5, 1.5, 3.0], vertical_alignment="bottom")
    dimension = c_dim.segmented_control(
        "敞口维度", ["gex", "vanna", "charm"], default="gex", key="exposure_dimension",
        format_func=ui.DIMENSION_CN.get) or "gex"
    side = c_side.segmented_control("口径", ["净值", "Call", "Put"], default="净值", key="exposure_side") or "净值"

    if len(moments_asc) > 1:
        as_of = c_time.select_slider(
            "快照回放", moments_asc, key="heatmap_as_of",
            format_func=lambda m: f"{to_viewer(m):%m-%d %H:%M}" + (" · 最新" if m == newest else ""))
    else:
        as_of = newest
        c_time.markdown(ui.chips([("快照", f"{to_viewer(newest):%m-%d %H:%M}"), ("回放", "需 ≥2 次采集")]),
                        unsafe_allow_html=True)

    snapshot_df = with_our_iv(db.get_snapshots_at(conn, selected_ticker, [as_of], source=page_source))
    if snapshot_df.empty:
        html(ui.empty("该快照没有数据", "采集日志中有记录但未存储行 — 请选择其他时间点。"))
        st.stop()
    tradeable = [e for e in sorted(snapshot_df["expiry"].unique()) if time_left(e, as_of)]
    spot = float(snapshot_df["underlying_price"].iloc[0])
    if not tradeable:
        html(ui.empty("该快照的所有到期日都已停止交易", "请选择更近的快照。"))
        st.stop()

    if len(tradeable) > 1:
        n_expiries = c_exp.slider("到期日数", 1, len(tradeable), min(12, len(tradeable)),
                                  key=f"heatmap_n_expiries_{selected_ticker}")
    else:
        n_expiries = 1
        c_exp.caption("仅 1 个可交易到期日")
    each_side = c_band.slider("行权价 ±档", 5, 60, 20, key="heatmap_strikes_each_side",
                              help="以最接近现价的行权价为中心，上下各显示的档数")
    shown = tradeable[:n_expiries]

    def exposure(frame: pd.DataFrame, dim: str) -> pd.DataFrame:
        return metrics.exposure_matrix(frame, as_of=as_of, expiries=shown, dimension=dim)

    calls = snapshot_df[snapshot_df["option_type"] == "call"]
    puts = snapshot_df[snapshot_df["option_type"] == "put"]
    gex_full = metrics.gex_matrix(snapshot_df, as_of=as_of, expiries=shown)
    gex_call, gex_put = exposure(calls, "gex"), exposure(puts, "gex")
    if dimension == "gex":
        dim_full, dim_call, dim_put = gex_full, gex_call, gex_put
    else:
        dim_full, dim_call, dim_put = exposure(snapshot_df, dimension), exposure(calls, dimension), \
            exposure(puts, dimension)
    display_full = {"净值": dim_full, "Call": dim_call, "Put": dim_put}[side]

    if display_full.empty:
        html(ui.empty("没有可绘制的数据", "所选快照与到期日范围内没有可计算的合约。"))
    else:
        band = strikes_around_money(display_full, spot, each_side)
        band = band.loc[:, ((band != 0) | band.isna()).any(axis=0)]
        flip = metrics.gamma_flip_from_matrix(gex_full, spot)
        walls = metrics.dealer_walls(snapshot_df, as_of=as_of, expiries=shown)
        gwalls = metrics.gex_walls(snapshot_df, as_of=as_of, expiries=shown)
        near_pain = metrics.max_pain(snapshot_df, shown[0])
        move = metrics.expected_move(snapshot_df, shown[0], as_of)

        def pivot(frame: pd.DataFrame, value: str) -> pd.DataFrame:
            return frame.pivot_table(index="strike", columns="expiry", values=value, aggfunc="sum")

        def align(frame: pd.DataFrame) -> pd.DataFrame:
            return frame.reindex(index=band.index, columns=band.columns) if not frame.empty else frame

        figure = ui.exposure_workbench(
            band, display_full.sum(min_count=1), as_of, spot, dimension,
            call_band=align(dim_call), put_band=align(dim_put),
            oi_call=pivot(calls, "open_interest"), oi_put=pivot(puts, "open_interest"),
            volume=pivot(snapshot_df, "volume"), flip=flip,
            levels={**walls, **gwalls, "max_pain": near_pain}, expected=move,
        )
        title = f"{ui.DIMENSION_CN[dimension]} 热力图" + ("" if side == "净值" else f" · 仅 {side}")
        meta = (f"{ui.UNIT_CN[dimension]} · {len(band.columns)}/{len(tradeable)} 个到期日 · "
                f"{len(band)} 个行权价 · {'最新快照' if as_of == newest else '回放 ' + format_datetime(as_of)}")
        html(ui.header(title, meta, right=ui.legend_strip(dimension)))
        plot(figure, "workbench_heatmap")
        missing = dim_full.attrs.get("missing_contracts", 0)
        st.caption(
            "假设：做市商 Call 多头 (+) / Put 空头 (−)，合约乘数 100；Vanna/Charm 为本地模型估算。"
            "右侧柱状为所示行权价的跨到期日合计；Total 行与 Σ 覆盖所选到期日的全部行权价。"
            + (f" {missing:,} 个合约缺少有效输入，未计入。" if missing else "")
        )

        # ---- expiry board -------------------------------------------------
        html(ui.header("到期日面板", "每个到期日的关键数字 · 基于当前快照"))
        board = []
        for expiry in shown:
            em = metrics.expected_move(snapshot_df, expiry, as_of)
            chain = snapshot_df[snapshot_df["expiry"] == expiry]
            c_oi = chain.loc[chain["option_type"] == "call", "open_interest"].sum()
            p_oi = chain.loc[chain["option_type"] == "put", "open_interest"].sum()
            board.append({
                "到期日": ui.expiry_short(expiry),
                "DTE": ui.dte(expiry, as_of),
                "净 GEX": gex_full[expiry].sum() if expiry in gex_full else np.nan,
                "Call GEX": gex_call[expiry].sum() if expiry in gex_call else np.nan,
                "Put GEX": gex_put[expiry].sum() if expiry in gex_put else np.nan,
                "Max Pain": metrics.max_pain(snapshot_df, expiry),
                "ATM IV": em["implied_volatility"] if em else np.nan,
                "±1σ": em["move"] if em else np.nan,
                "±1σ %": em["move_pct"] / 100 if em else np.nan,
                "Call OI": c_oi, "Put OI": p_oi,
                "P/C": p_oi / c_oi if c_oi else np.nan,
                "有效合约": metrics.contracts_backing_expiry(snapshot_df, expiry, as_of),
            })
        board_df = pd.DataFrame(board)
        gex_cols = ["净 GEX", "Call GEX", "Put GEX"]
        styled_board = (
            board_df.style
            .format({c: lambda v: ui.compact(v, signed=True) for c in gex_cols})
            .format({"Max Pain": ui.strike_label, "ATM IV": ui.pct, "±1σ": "±{:,.2f}",
                     "±1σ %": lambda v: ui.pct(v, 2), "Call OI": "{:,.0f}", "Put OI": "{:,.0f}",
                     "P/C": "{:.2f}"}, na_rep="—")
            .map(lambda v: f"color: {theme.POS if v >= 0 else theme.NEG}" if pd.notna(v) else "", subset=gex_cols)
        )
        st.dataframe(styled_board, hide_index=True, width="stretch",
                     height=min(36 + 35 * len(board_df), 460))


# ==========================================================================
# ◠ 波动率 — skew, term structure, surface, history
# ==========================================================================
if active_view == "波动率":
    spot_now = float(latest_df["underlying_price"].iloc[0])
    left, right = st.columns(2, gap="medium")
    with left:
        _drop_stale_choice(f"skew_expiries_{selected_ticker}", [])
        picks = st.multiselect(
            "波动率微笑 · 到期日", live_expiries, default=live_expiries[: min(4, len(live_expiries))],
            format_func=ui.expiry_short, key=f"skew_pick_{selected_ticker}", max_selections=6,
        )
        width_pct = st.slider("行权价范围 ±%", 3, 40, 12, key="skew_width")
        curves = []
        for expiry in picks:
            chain = latest_df[latest_df["expiry"] == expiry]
            otm = chain[((chain["option_type"] == "put") & (chain["strike"] < spot_now))
                        | ((chain["option_type"] == "call") & (chain["strike"] >= spot_now))]
            otm = otm[(otm["strike"] - spot_now).abs() <= spot_now * width_pct / 100]
            otm = otm[otm["implied_volatility"] > 0][["strike", "implied_volatility"]]
            otm = otm.rename(columns={"implied_volatility": "iv"}).sort_values("strike")
            if len(otm) > 1:
                curves.append((f"{ui.expiry_short(expiry)} · {ui.dte(expiry, latest_date)}D", otm))
        html(ui.header("波动率微笑", "OTM 合约 IV · Put 取现价下方，Call 取现价上方"))
        if curves:
            plot(ui.skew_figure(curves, spot_now), "vol_skew")
        else:
            html(ui.empty("所选到期日没有可用 IV"))
    with right:
        term_rows = []
        for expiry in live_expiries[:30]:
            em = metrics.expected_move(latest_df, expiry)
            if em:
                term_rows.append({"label": f"{pd.Timestamp(expiry):%m-%d}", "atm_iv": em["implied_volatility"],
                                  "move_pct": em["move_pct"]})
        html(ui.header("期限结构", "ATM IV 与 ±1σ 预期波动（%）"))
        if len(term_rows) > 1:
            plot(ui.term_figure(pd.DataFrame(term_rows), height=420), "vol_term")
        else:
            html(ui.empty("到期日不足，无法绘制期限结构"))

    s_col, h_col = st.columns([1.35, 1], gap="medium")
    with s_col:
        html(ui.header("IV 曲面", "最新快照 · OTM IV · 插值仅用于显示"))
        points = metrics.iv_surface(latest_df)
        grid = metrics.iv_surface_grid(points)
        if grid is None:
            html(ui.empty("数据不足", "需要至少 2 个行权价与 2 个到期日的有效 IV。"))
        else:
            plot(ui.surface_figure(points, grid), "vol_surface")
    with h_col:
        iv_avg = db.get_iv_weighted_average(conn, selected_ticker, days=history_days, source=page_source)
        html(ui.header("IV 历史", "成交量加权平均"))
        if len(iv_avg) > 1:
            plot(ui.line_history(with_viewer_index(iv_avg), {"iv_weighted_avg": ("IV 加权", theme.VOL)},
                                 height=300, percent=True), "vol_history")
        else:
            html(ui.empty("历史积累中", f"已有 {len(iv_avg)} 个采集点；至少 2 个点后显示趋势。",
                          progress=len(iv_avg) / 2))
        stored_rv = db.get_realized_volatility(conn, selected_ticker)
        if stored_rv:
            as_of_rv, rv, rv_source = stored_rv
            html(ui.chips([(f"RV{w}d", f"{v:.1%}") for w, v in sorted(rv.items())]
                          + [("来源", f"{rv_source} · {as_of_rv:%m-%d}")]))
        elif page_source == "cboe":
            st.caption("Cboe 原生 IV 保留；Vanna/Charm 由本地模型基于该 IV 估算。")


# ==========================================================================
# ☰ 期权链 — filterable chain, contract detail
# ==========================================================================
if active_view == "期权链":
    chain_key = f"chain_contract_{selected_ticker}"
    tracked = get_tracked()
    if not tracked.empty:
        labels = {f"{pd.Timestamp(r.expiry):%m-%d} {r.strike:g}{r.option_type[0].upper()}":
                  (pd.Timestamp(r.expiry), float(r.strike), r.option_type) for r in tracked.itertuples()}
        picked_pin = st.pills("📌 已固定", list(labels), key=f"pinned_pick_{selected_ticker}")
        if picked_pin and labels[picked_pin] != st.session_state.get(chain_key):
            st.session_state[chain_key] = labels[picked_pin]

    table = metrics.screener_table(latest_df)
    extra = latest_df[["expiry", "strike", "option_type", "bid", "ask", "volume"]].drop_duplicates(
        ["expiry", "strike", "option_type"])
    table = table.merge(extra, on=["expiry", "strike", "option_type"], how="left")
    spot_now = float(latest_df["underlying_price"].iloc[0])
    now_utc = pd.Timestamp.now("UTC")
    table = table[table["expiry"].map(lambda e: time_left(e, now_utc))] \
        if not table.empty else table

    f_exp, f_type, f_band, f_oi, f_more = st.columns([3.2, 1.3, 1.6, 1.1, 0.9], vertical_alignment="bottom")
    exp_options = sorted(table["expiry"].unique()) if not table.empty else []
    _drop_stale_choice(f"chain_expiries_{selected_ticker}", [])
    chosen_exp = f_exp.multiselect("到期日", exp_options, default=exp_options[:2], format_func=ui.expiry_short,
                                   key=f"chain_exp_{selected_ticker}")
    side = f_type.segmented_control("类型", ["全部", "Call", "Put"], default="全部", key="chain_side") or "全部"
    band_pct = f_band.slider("行权价 ±%", 1, 50, 8, key="chain_band")
    min_oi = f_oi.number_input("最低 OI", min_value=0, value=0, step=100, key="chain_min_oi")
    with f_more.popover("Greeks", width="stretch"):
        delta_range = st.slider("|Delta|", 0.0, 1.0, (0.0, 1.0), 0.01, key="chain_delta")
        iv_range = st.slider("IV", 0.0, 3.0, (0.0, 3.0), 0.01, key="chain_iv")
        show_expired = st.toggle("浏览已过期合约", key=f"chain_show_expired_{selected_ticker}")

    view = table
    if chosen_exp:
        view = view[view["expiry"].isin(chosen_exp)]
    if side != "全部":
        view = view[view["option_type"] == side.lower()]
    view = view[(view["strike"] - spot_now).abs() <= spot_now * band_pct / 100]
    view = view[view["open_interest"].fillna(0) >= min_oi]
    view = view[view["delta"].abs().between(*delta_range) | view["delta"].isna()]
    view = view[view["implied_volatility"].between(*iv_range) | view["implied_volatility"].isna()]
    view = view.sort_values(["expiry", "strike", "option_type"]).reset_index(drop=True)

    html(ui.header("期权链", f"{len(view):,} / {len(table):,} 个合约 · 现价 {spot_now:,.2f} · 点击行查看合约详情"))
    if view.empty:
        html(ui.empty("没有符合筛选条件的合约"))
    else:
        shown_table = pd.DataFrame({
            "到期": view["expiry"].map(ui.expiry_short), "DTE": view["dte"],
            "行权价": view["strike"], "类型": view["option_type"].str[0].str.upper(),
            "价内外": view["strike"] / spot_now - 1,
            "Bid": view["bid"], "Ask": view["ask"], "成交价": view["last_price"],
            "成交量": view["volume"], "OI": view["open_interest"], "IV": view["implied_volatility"],
            "Delta": view["delta"], "Gamma": view["gamma"], "Theta": view["theta"], "Vega": view["vega"],
            "Vanna": view["vanna"], "Charm": view["charm"],
        })
        event = st.dataframe(
            shown_table, hide_index=True, width="stretch", height=420,
            on_select="rerun", selection_mode="single-row", key=f"chain_table_{selected_ticker}",
            column_config={
                "DTE": st.column_config.NumberColumn(format="%d", width="small"),
                "行权价": st.column_config.NumberColumn(format="%g"),
                "类型": st.column_config.TextColumn(width="small"),
                "价内外": st.column_config.NumberColumn(format="percent"),
                "Bid": st.column_config.NumberColumn(format="%.2f"),
                "Ask": st.column_config.NumberColumn(format="%.2f"),
                "成交价": st.column_config.NumberColumn(format="%.2f"),
                "成交量": st.column_config.NumberColumn(format="localized"),
                "OI": st.column_config.NumberColumn(format="localized"),
                "IV": st.column_config.NumberColumn(format="percent"),
                "Delta": st.column_config.NumberColumn(format="%.3f"),
                "Gamma": st.column_config.NumberColumn(format="%.4f"),
                "Theta": st.column_config.NumberColumn(format="%.3f"),
                "Vega": st.column_config.NumberColumn(format="%.3f"),
                "Vanna": st.column_config.NumberColumn(format="%.4f"),
                "Charm": st.column_config.NumberColumn(format="%.4f"),
            },
        )
        rows = event.selection.rows if event and event.selection else []
        if rows:
            row = view.iloc[rows[0]]
            st.session_state[chain_key] = (pd.Timestamp(row["expiry"]), float(row["strike"]), row["option_type"])

    if show_expired:
        expired = db.get_expired_contracts(conn, selected_ticker, source=page_source)
        if expired.empty:
            st.caption("没有已过期且有历史的合约。")
        else:
            expired_labels = {
                f"{pd.Timestamp(r.expiry):%Y-%m-%d} {r.strike:g}{r.option_type[0].upper()} · {r.snapshots} 点":
                (pd.Timestamp(r.expiry), float(r.strike), r.option_type) for r in expired.itertuples()}
            pick = st.selectbox("已过期合约", list(expired_labels), index=None, placeholder="选择一个已过期合约",
                                key=f"expired_pick_{selected_ticker}")
            if pick:
                st.session_state[chain_key] = expired_labels[pick]

    contract = st.session_state.get(chain_key)
    if contract:
        render_contract(contract[0], contract[1], contract[2], history_days, key="chain")


# ==========================================================================
# ⇅ 资金流 — unusual activity, OI change, day-over-day, history
# ==========================================================================
if active_view == "资金流":
    spot_now = float(latest_df["underlying_price"].iloc[0])
    u_col, o_col = st.columns([1, 1.15], gap="medium")
    with u_col:
        flagged = metrics.unusual_activity(
            latest_df, stats=db.get_volume_stats(conn, selected_ticker, source=page_source))
        html(ui.header("异动合约", f"{len(flagged)} 个 · 成交量 z-score > {config.UNUSUAL_Z_THRESHOLD} "
                                   "或 成交量 > 2×OI"))
        if flagged.empty:
            html(ui.empty("今日没有异动合约"))
        else:
            st.dataframe(
                pd.DataFrame({
                    "到期": flagged["expiry"].map(ui.expiry_short), "行权价": flagged["strike"],
                    "类型": flagged["option_type"].str[0].str.upper(), "成交量": flagged["volume"],
                    "OI": flagged["open_interest"], "均量": flagged["avg_volume"],
                    "z": flagged["volume_zscore"],
                    "量/OI": flagged["volume"] / flagged["open_interest"].replace(0, np.nan),
                }).head(200),
                hide_index=True, width="stretch", height=380,
                column_config={
                    "行权价": st.column_config.NumberColumn(format="%g"),
                    "成交量": st.column_config.NumberColumn(format="localized"),
                    "OI": st.column_config.NumberColumn(format="localized"),
                    "均量": st.column_config.NumberColumn(format="%.0f"),
                    "z": st.column_config.NumberColumn(format="%.1f"),
                    "量/OI": st.column_config.NumberColumn(format="%.2f"),
                },
            )
    with o_col:
        day_moments = db.get_collection_moments(
            conn, selected_ticker, days=history_days, per_day=True, source=page_source)[:2]
        delta = metrics.oi_delta(db.get_snapshots_at(conn, selected_ticker, day_moments, source=page_source))
        html(ui.header("持仓变化 ΔOI", "最近交易日 vs 前一交易日"))
        if delta.empty:
            html(ui.empty("需要两个不同交易日的数据",
                          f"已有 {len(day_moments)} 个交易日。持仓量每日只结算一次，第二个交易日采集后出现。",
                          progress=len(day_moments) / 2))
        else:
            near = delta[(delta["strike"] - spot_now).abs() <= spot_now * 0.15]
            by_strike = near.groupby(["strike", "option_type"], as_index=False)["oi_delta"].sum()
            plot(ui.oi_delta_figure(by_strike, spot_now, height=250), "flow_oi_delta")
            top = delta.reindex(delta["oi_delta"].abs().sort_values(ascending=False).index).head(100)
            st.dataframe(
                pd.DataFrame({
                    "到期": top["expiry"].map(ui.expiry_short), "行权价": top["strike"],
                    "类型": top["option_type"].str[0].str.upper(), "OI": top["open_interest"],
                    "ΔOI": top["oi_delta"], "Δ%": top["oi_delta_pct"] / 100,
                }),
                hide_index=True, width="stretch", height=200,
                column_config={
                    "行权价": st.column_config.NumberColumn(format="%g"),
                    "OI": st.column_config.NumberColumn(format="localized"),
                    "ΔOI": st.column_config.NumberColumn(format="%+d"),
                    "Δ%": st.column_config.NumberColumn(format="percent"),
                },
            )

    # ---- day over day -------------------------------------------------------
    days = db.list_day_summaries(conn, selected_ticker, page_source) if page_source else []
    html(ui.header("跨日对比", "两个交易日的定位变化 · 纽约交易日"))
    if len(days) < 2:
        html(ui.empty("跨日对比需要至少两个交易日",
                      f"已记录 {len(days)} 个交易日。每次采集后会写入当日摘要，下一个交易日即可对比。",
                      progress=len(days) / 2))
    else:
        pick_l, pick_r, _spacer = st.columns([1, 1, 3])
        day_before = pick_l.selectbox("从", days, index=1, key="changes_from", format_func=lambda d: f"{d:%Y-%m-%d}")
        day_after = pick_r.selectbox("到", days, index=0, key="changes_to", format_func=lambda d: f"{d:%Y-%m-%d}")
        row_after = db.get_day_summary(conn, selected_ticker, page_source, day_after)
        row_before = db.get_day_summary(conn, selected_ticker, page_source, day_before) \
            if day_before != day_after else None

        def rollup(row):
            if row is None:
                return None
            stored = db.get_moment_rollup(conn, selected_ticker, page_source, row["collected_at"]) or {}
            return {"iv_weighted_avg": stored.get("iv_weighted_avg"),
                    "call_volume": row.get("call_volume"), "put_volume": row.get("put_volume"),
                    "call_oi": row.get("call_oi"), "put_oi": row.get("put_oi")}

        if row_after is not None:
            same_expiry_pain = None
            if row_before and row_after.get("nearest_expiry") and \
                    str(row_before.get("nearest_expiry")) != str(row_after.get("nearest_expiry")):
                earlier = db.get_snapshots_at(conn, selected_ticker, [row_before["collected_at"]], source=page_source)
                if not earlier.empty:
                    wanted = pd.Timestamp(row_after["nearest_expiry"])
                    same_day = earlier[pd.to_datetime(earlier["expiry"], errors="coerce") == wanted]
                    if not same_day.empty:
                        pain = metrics.max_pain(same_day, wanted)
                        same_expiry_pain = None if pain is None or pd.isna(pain) else float(pain)
            ladder = day_summary.changes(row_before, row_after, rollup(row_before), rollup(row_after),
                                         same_expiry_pain)
            labels_cn = {
                "state": "Gamma 状态", "price": "现价", "net_gex": "净 GEX", "call_wall": "Call OI 墙",
                "put_wall": "Put OI 墙", "gamma_flip": "Gamma 翻转", "max_pain": "Max Pain",
                "expected_move": "±1σ 区间", "pcr_volume": "P/C 成交量", "pcr_oi": "P/C 持仓",
                "iv": "IV 加权", "call_oi": "Call 持仓", "put_oi": "Put 持仓", "expiries": "挂牌到期日",
                "contracts": "合约数",
            }
            words_cn = {"unchanged": "不变", "gone": "消失", "new": "新增",
                        "amplifying": "→ 放大", "damping": "→ 压制"}

            def cell(line, which):
                if line["unit"] == "state":
                    value = line.get(which)
                    return ui.WEATHER_CN.get(value, ("—",))[0] if value else "—"
                return day_summary.format_value(line, which)

            ladder_rows = []
            for line in ladder:
                label = labels_cn.get(line["key"], line["label"])
                if " · " in line["label"]:
                    label += f' <span class="note">{ui.esc(line["label"].split(" · ", 1)[1])}</span>'
                delta_text = day_summary.format_delta(line)
                delta_text = words_cn.get(delta_text, delta_text)
                color = {day_summary.DAMPING: "gg-pos", day_summary.AMPLIFYING: "gg-neg"}.get(line["kind"], "")
                ladder_rows.append(
                    f"<tr><td>{label}</td><td class='n b'>{cell(line, 'before')}</td>"
                    f"<td class='n'>{cell(line, 'after')}</td><td class='n {color}'>{delta_text}</td></tr>")
            ladder_col, profile_col = st.columns([1, 1.25], gap="medium")
            ladder_col.markdown(
                f"<table class='gg-ladder'><tr><th></th><th>{day_before:%m-%d}</th><th>{day_after:%m-%d}</th>"
                "<th>变化</th></tr>" + "".join(ladder_rows) + "</table>", unsafe_allow_html=True)
            profile_after = row_after.get("gex_by_strike") or []
            with profile_col:
                if profile_after:
                    frame = pd.DataFrame({f"{day_after:%m-%d}": dict(profile_after)})
                    if row_before and row_before.get("gex_by_strike"):
                        frame[f"{day_before:%m-%d}"] = pd.Series(dict(row_before["gex_by_strike"]))
                    frame = strikes_around_money(frame.sort_index(), row_after["underlying_price"], 15).sort_index()
                    plot(ui.profile_compare_figure(frame, row_after["underlying_price"], height=420), "flow_compare")

    # ---- history ------------------------------------------------------------
    pcr = db.get_put_call_ratio(conn, selected_ticker, days=history_days, source=page_source)
    html(ui.header("Put/Call 比率历史", f"{len(pcr)} 个采集点"))
    if len(pcr) > 1:
        plot(ui.line_history(with_viewer_index(pcr), {"pcr_volume": ("成交量 P/C", theme.SPOT),
                                                      "pcr_oi": ("持仓 P/C", theme.FLIP)}, height=240),
             "flow_pcr")
    else:
        html(ui.empty("历史积累中", "至少 2 个采集点后显示趋势。", progress=len(pcr) / 2))


# ==========================================================================
# live refresh — a tiny watcher that reruns the page when a new snapshot lands
# ==========================================================================
if live:
    @st.fragment(run_every=LIVE_REFRESH_SECONDS)
    def _live_watch():
        newest_moments = db.get_collection_moments(conn, selected_ticker, days=2, source=page_source)
        if newest_moments and pd.Timestamp(newest_moments[0]) > pd.Timestamp(latest_date):
            st.rerun()

    _live_watch()
