"""Unauthenticated Cboe delayed option chains, one request per underlying."""
from __future__ import annotations

import math
import re

import pandas as pd
import requests

from app.providers.base import CHAIN_COLUMNS, ProviderStatus

_CONTRACT = re.compile(r'^(?P<root>.+)(?P<date>\d{6})(?P<side>[CP])(?P<strike>\d{8})$')
_INDEXES = {'SPX', 'XSP', 'NDX', 'RUT', 'DJX', 'VIX', 'OEX', 'XEO'}


class CboeProvider:
    name = 'cboe'
    price_history_source = 'yahoo'
    requires_token = False
    token = None
    max_zero_oi_fraction = 0.99
    unsupported_symbols = {'VIX': None, 'VIXW': None}

    def __init__(self, session=None):
        self.session = session or requests.Session()
        self.session.headers.update({'User-Agent': 'GammaGridDesktop/0.1 (personal research)'})

    @staticmethod
    def url_for(ticker):
        symbol = ticker.strip().upper().lstrip('^_')
        symbol = {'SPXW': 'SPX', 'NDXP': 'NDX'}.get(symbol, symbol)
        if not re.fullmatch(r'[A-Z0-9][A-Z0-9.\-]{0,15}', symbol):
            raise ValueError('Invalid Cboe symbol')
        prefix = '_' if symbol in _INDEXES else ''
        return f'https://cdn.cboe.com/api/global/delayed_quotes/options/{prefix}{symbol}.json'

    @staticmethod
    def parse(payload):
        data = payload.get('data', {})
        spot = float(data.get('current_price') or 0)
        if not math.isfinite(spot) or spot <= 0:
            raise ValueError('Cboe returned no valid underlying price')
        raw = data.get('options')
        if not isinstance(raw, list) or not raw:
            raise ValueError('Cboe returned an empty option chain')
        rows = []
        expiries = {}
        for item in raw:
            symbol = str(item.get('option', ''))
            match = _CONTRACT.fullmatch(symbol)
            if not match:
                raise ValueError(f'Cboe returned an unrecognized contract: {symbol[:40]}')
            date_key = match['date']
            if date_key not in expiries:
                expiries[date_key] = pd.to_datetime(date_key, format='%y%m%d').date()
            expiry = expiries[date_key]
            strike = int(match['strike']) / 1000
            side = 'call' if match['side'] == 'C' else 'put'
            row = dict(expiry=expiry, strike=strike, option_type=side,
                       contract_symbol=symbol, quote_timestamp=payload.get('timestamp'),
                       in_the_money=spot > strike if side == 'call' else spot < strike)
            for target, source in {'last_price':'last_trade_price', 'bid':'bid', 'ask':'ask',
                                   'volume':'volume', 'open_interest':'open_interest',
                                   'implied_volatility':'iv', 'gamma':'gamma', 'delta':'delta',
                                   'theta':'theta', 'vega':'vega'}.items():
                value = item.get(source)
                try:
                    number = float(value) if value is not None else None
                except (TypeError, ValueError):
                    number = None
                if number is not None and not math.isfinite(number):
                    number = None
                if (target in ('bid','ask','last_price','volume','open_interest','gamma','vega')
                        and number is not None and number < 0):
                    number = None
                if target == 'implied_volatility' and number is not None and number <= 0:
                    number = None
                if target == 'delta' and number is not None and abs(number) > 1:
                    number = None
                row[target] = number
            rows.append(row)
        frame = pd.DataFrame(rows, columns=CHAIN_COLUMNS + ['contract_symbol','quote_timestamp'])
        if frame['contract_symbol'].duplicated().any():
            raise ValueError('Cboe returned duplicate contract identifiers')
        frame.attrs['source'] = 'cboe'
        frame.attrs['source_timestamp'] = payload.get('timestamp')
        return spot, frame

    def fetch_ticker_snapshot(self, ticker):
        response = self.session.get(self.url_for(ticker), timeout=(10, 35))
        response.raise_for_status()
        return self.parse(response.json())

    def fetch_price_history(self, ticker, period='6mo'):
        from app.providers.yahoo import YahooProvider
        return YahooProvider().fetch_price_history(ticker, period)

    def underlying_has_options(self, ticker):
        # Do not download the entire chain just to validate an Add-ticker box.
        self.url_for(ticker)
        return None

    def check_access(self):
        try:
            _, chain = self.fetch_ticker_snapshot('SPY')
            return ProviderStatus(True, f'Cboe: {len(chain):,} delayed SPY contracts')
        except Exception as exc:
            return ProviderStatus(False, f'Cboe unavailable: {exc}')
