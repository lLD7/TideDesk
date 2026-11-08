"""Yahoo Finance via yfinance: fallback when the Cboe full-chain feed fails."""

from __future__ import annotations

import pandas as pd
import yfinance as yf

from app.providers.base import (
    CHAIN_COLUMNS,
    CHAIN_OPTIONAL_COLUMNS,
    ProviderStatus,
    with_retry,
)

CHAIN_RENAMES = {
    "lastPrice": "last_price",
    "openInterest": "open_interest",
    "impliedVolatility": "implied_volatility",
    "inTheMoney": "in_the_money",
    # Yahoo serves the contract's own OCC symbol, and it is the only thing that
    # distinguishes an adjusted series from the standard one at the same strike
    # and expiry. It used to be dropped on the way out of this function, which
    # is what made a split-adjusted ticker fail to collect at all.
    "contractSymbol": "contract_symbol",
}


# What these instruments are called at Yahoo: the index under a caret. The
# chain, the spot and the price history all come from that one symbol.
#
# THE CARET FORM IS THE CBOE INDEX, NOT THE PUBLISHER'S QUOTE. `^GSPC` and
# `^DJI` are the S&P 500 and the Dow as their publishers quote them, and Yahoo
# serves no option chain under either — 0 expiries, measured on yfinance 1.7.0
# — which is how this product once concluded that Yahoo has no index options
# at all. Under `^SPX`, `^XSP`, `^NDX`, `^RUT` and `^DJX` it serves 53, 42, 40,
# 28 and 12 expiries, dailies included. And `^DJI` is not even DJX's price:
# DJX is written on one hundredth of the Dow, so the old `DJX → ^DJI` mapping
# would have put a spot a hundred times too high under every greek.
#
# SPOT AND CHAIN FROM THE SAME SYMBOL, deliberately. Cboe's index quotes at
# Yahoo are delayed about fifteen minutes, like the chain; a publisher's quote
# can be fresher, and pricing a delayed chain against a fresher spot moves
# every strike's moneyness by however far the index travelled in between.
#
# ONE TRANSLATION, IN ONE PLACE. The sibling product learned this the expensive
# way: the map was wired into the spot lookup alone, and the price-history
# lookup then failed with "possibly delisted; no price data found" for every
# index ticker. Nothing crashed, because that caller swallows failures and
# returns nothing — so realized volatility was simply absent, with no symptom.
# One translation used in one of two places is the same bug twice.
INDEX_SYMBOLS = {
    "SPX": "^SPX",
    "XSP": "^XSP",
    "NDX": "^NDX",
    "RUT": "^RUT",
    "DJX": "^DJX",
    # Price history only: VIX's options are refused below.
    "VIX": "^VIX",
}


def _yahoo_symbol(ticker: str) -> str:
    """What Yahoo calls this instrument. The single place the translation
    happens, so that adding a lookup elsewhere cannot forget it."""
    return INDEX_SYMBOLS.get(ticker.upper(), ticker)


class YahooProvider:
    name = "yahoo"
    price_history_source = "yahoo"
    requires_token = False

    # WHAT NOT TO ASK YAHOO FOR, AND WHAT TO OFFER INSTEAD.
    #
    # SPXW and NDXP are not underlyings: they are the weekly and PM-settled
    # series of SPX and NDX options, and Yahoo serves them inside the index's
    # own chain. Somebody who types the series root wants the index, so that is
    # what is offered.
    #
    # VIX IS REFUSED, AND MAPS TO NOTHING, ON PURPOSE. Yahoo does list a VIX
    # chain, but VIX options are priced off VIX futures rather than off the
    # index, so a model that takes the index as spot gets every greek of it
    # wrong. And an ETF on VIX futures is a different instrument with its own
    # term structure, not a proxy for the index — offering one would be worse
    # than offering nothing.
    unsupported_symbols = {
        "SPXW": "SPX", "NDXP": "NDX",
        "VIX": None, "VIXW": None,
    }
    token: str | None = None  # nothing to authenticate with, nothing to scrub

    def _fetch_underlying_price(self, ticker_obj: yf.Ticker) -> float:
        """The spot price, or a refusal a person can act on.

        `float(fast_info["lastPrice"])` used to be the whole body, and what a
        user saw when it went wrong was one of these, verbatim, in the
        collection log:

            float() argument must be a string or a real number, not 'NoneType'
            'currentTradingPeriod'

        Neither says what happened or what to do, and neither is stable: the
        same missing symbol raised a KeyError one week and a TypeError the
        next, because yfinance's shape for "nothing here" changes with its
        version. So the check is on the VALUE — there is no price — rather
        than on the type of the explosion, and the message names both causes
        the user can actually be in.
        """
        try:
            price = ticker_obj.fast_info["lastPrice"]
        except Exception:  # noqa: BLE001 — every shape of "not there" means the same thing
            price = None
        if price is None:
            raise ValueError(
                f"no price for {ticker_obj.ticker} — either the symbol does not exist, "
                "or Yahoo Finance is rate-limiting this installation. Check the spelling "
                "first; if it is right, wait a few minutes and collect again."
            )
        return float(price)

    def fetch_underlying_price(self, ticker: str) -> float:
        """Spot price on its own, without pulling a whole option chain.

        Public rather than private because an options-only provider needs it:
        spot is not optional — it is the input every greek, max pain and GEX
        figure is built on — and a vendor that sells only option chains has no
        way to supply it.
        """
        return with_retry(self._fetch_underlying_price, yf.Ticker(_yahoo_symbol(ticker)))

    def _fetch_chain_for_expiry(self, ticker_obj: yf.Ticker, expiry: str) -> pd.DataFrame:
        chain = ticker_obj.option_chain(expiry)

        calls = chain.calls.copy()
        calls["option_type"] = "call"
        puts = chain.puts.copy()
        puts["option_type"] = "put"

        combined = pd.concat([calls, puts], ignore_index=True)
        combined["expiry"] = expiry
        combined = combined.rename(columns=CHAIN_RENAMES)

        # Yahoo serves no greeks. The columns exist so that every provider hands
        # back the same shape; they stay empty here and the reader computes
        # greeks from implied volatility instead. Explicitly None rather than 0
        # — a zero delta is a real value a deep-OTM contract can have, and
        # storing 0 would be indistinguishable from "the provider said so".
        for greek in ("delta", "gamma", "theta", "vega"):
            combined[greek] = None

        extras = [name for name in CHAIN_OPTIONAL_COLUMNS if name in combined.columns]
        return combined[CHAIN_COLUMNS + extras]

    def fetch_ticker_snapshot(self, ticker: str) -> tuple[float, pd.DataFrame]:
        ticker_obj = yf.Ticker(_yahoo_symbol(ticker))
        underlying_price = with_retry(self._fetch_underlying_price, ticker_obj)
        expiries = with_retry(lambda: ticker_obj.options)

        frames = [
            with_retry(self._fetch_chain_for_expiry, ticker_obj, expiry) for expiry in expiries
        ]
        chain_df = pd.concat(frames, ignore_index=True) if frames else pd.DataFrame()
        return underlying_price, chain_df

    def fetch_price_history(self, ticker: str, period: str = "6mo") -> pd.DataFrame:
        """Daily close history of the underlying, for realized volatility. A
        separate lightweight read-only request, unrelated to collect_watchlist:
        it doesn't write to the database, doesn't take part in snapshot quality
        checks, and doesn't depend on how many days of option chains have
        already been collected."""
        history = with_retry(lambda: yf.Ticker(_yahoo_symbol(ticker)).history(period=period))
        if history.empty:
            return pd.DataFrame(columns=["close"])
        return history.rename(columns={"Close": "close"})[["close"]]

    def underlying_has_options(self, ticker: str) -> bool | None:
        """Does this symbol have options here at all — True, False, or "no idea".

        Yahoo's answer is its expiry list: no expiries, no chain to collect.

        THREE-VALUED ON PURPOSE. None means the question could not be answered
        — Yahoo was unreachable, rate-limiting, or slow — and the caller must
        let the ticker through on None. Refusing a valid symbol because a free
        data source had a bad minute is a worse failure than the typo this
        catches: the typo is visible in the collection log within the hour, and
        a wrongly rejected ticker looks like the product being broken.

        Not part of the DataProvider Protocol, and called through getattr, so
        that a provider written before this existed is not suddenly an invalid
        provider. A source that cannot answer simply does not offer the method.
        """
        try:
            return bool(yf.Ticker(_yahoo_symbol(ticker)).options)
        except Exception:  # noqa: BLE001 — every failure means "could not find out"
            return None

    def check_access(self) -> ProviderStatus:
        """yfinance needs no credentials, so there is nothing to verify beyond
        "is Yahoo answering at all". Kept for interface symmetry — a caller
        checks access without caring which provider is active."""
        try:
            with_retry(lambda: yf.Ticker("SPY").fast_info["lastPrice"])
        except Exception as exc:
            return ProviderStatus(False, f"Yahoo Finance is not responding: {exc}")
        return ProviderStatus(True, "Yahoo Finance is reachable. No token required.")
