"""Yahoo adapter for TideDesk. Read market data and emit one JSON response."""

import argparse
import contextlib
import json
import math
import os
import sys


INDEX_SYMBOLS = {
    "SPX": "^SPX", "SPXW": "^SPX", "XSP": "^XSP",
    "NDX": "^NDX", "NDXP": "^NDX", "RUT": "^RUT", "DJX": "^DJX",
}

FIELDS = {
    "lastPrice": "last_price",
    "bid": "bid",
    "ask": "ask",
    "volume": "volume",
    "openInterest": "open_interest",
    "impliedVolatility": "implied_volatility",
}


def number(value):
    try:
        value = float(value)
        return value if math.isfinite(value) else None
    except (TypeError, ValueError):
        return None


def snapshot(ticker):
    spot = number(ticker.fast_info["lastPrice"])
    if spot is None or spot <= 0:
        raise ValueError("Yahoo returned no valid underlying price")
    rows = []
    for expiry in ticker.options:
        chain = ticker.option_chain(expiry)
        for side, frame in (("call", chain.calls), ("put", chain.puts)):
            for contract in frame.itertuples(index=False):
                strike = number(contract.strike)
                if strike is None or strike <= 0:
                    continue
                symbol = getattr(contract, "contractSymbol", None)
                row = {
                    "expiry": expiry, "strike": strike, "option_type": side,
                    "contract_symbol": symbol if isinstance(symbol, str) else "",
                    "in_the_money": number(getattr(contract, "inTheMoney", None)) == 1,
                }
                row.update({dest: number(getattr(contract, src, None)) for src, dest in FIELDS.items()})
                rows.append(row)
    return {"spot": spot, "rows": rows}


def realized_volatility(ticker):
    import numpy as np

    history = ticker.history(period="6mo", raise_errors=True)
    if history.empty:
        return {"values": {}}
    # Preserve the existing close-to-close sample standard deviation formula.
    closes = history["Close"].dropna()
    log_returns = np.log(closes / closes.shift(1)).dropna()
    values = {}
    for window in (10, 20, 30):
        if len(log_returns) >= window:
            value = number(log_returns.tail(window).std() * np.sqrt(252))
            if value is not None:
                values[window] = value
    return {"values": values}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--op", choices=("snapshot", "rv"), required=True)
    parser.add_argument("--ticker", required=True)
    args = parser.parse_args()
    try:
        with contextlib.redirect_stdout(sys.stderr):
            import yfinance

            # yfinance 1.7.0 falls back to disabled caches when its cache path
            # is not a directory. Keep this helper free of SQLite writes.
            yfinance.set_tz_cache_location(os.devnull)
            symbol = args.ticker.upper()
            ticker = yfinance.Ticker(INDEX_SYMBOLS.get(symbol, symbol))
            result = snapshot(ticker) if args.op == "snapshot" else realized_volatility(ticker)
    except Exception as exc:
        text = str(exc) or type(exc).__name__
        rate_limited = "ratelimit" in type(exc).__name__.lower() or any(
            marker in text.lower() for marker in ("429", "too many requests", "rate limit", "ratelimit")
        )
        result = {"error": text, "rate_limited": rate_limited}
    json.dump(result, sys.stdout, allow_nan=False, separators=(",", ":"))
    sys.stdout.write("\n")


if __name__ == "__main__":
    main()
