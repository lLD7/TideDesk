"""Provider registry.

get_provider() is the only place that turns a name into a working object.
Everything else — collector, dashboard, tests — takes a provider it was handed
and never asks which one it got.

## Adding your own data source

The whole point of this package is that a second source is a new file here and
nothing else. Write a class with the attributes and methods in
`base.DataProvider` (it is a runtime-checkable Protocol, so
`isinstance(mine, DataProvider)` tells you whether you have them all), return
at least `base.CHAIN_COLUMNS` from `fetch_ticker_snapshot`, and register it in
`known_providers()` and `get_provider()` below. Import it lazily, the way this
file already does for anything optional, so that people who do not use your
provider do not have to install its dependencies.

## What is optional, and why it is optional rather than required

Two things improve the product when a source can supply them and change
nothing when it cannot. Neither is in the Protocol, so a provider written
before they existed is still a valid provider.

- **`base.CHAIN_OPTIONAL_COLUMNS`** — today just `contract_symbol`, the
  contract's own identifier. Return it as an extra column if you have it. It
  is what distinguishes an adjusted series from the standard one when both
  trade at the same strike and expiry, which happens after a split or a
  special dividend. Without it the two are indistinguishable and one of them
  is dropped arbitrarily; with it the standard series is kept.
- **`underlying_has_options(ticker) -> bool | None`** — asked once, when
  somebody types a ticker into the watchlist, so that a symbol with no chain
  is refused with an explanation instead of failing forever. **`None` means
  "could not find out" and lets the ticker through**, and a provider that does
  not define the method at all reads as `None`. Keep that three-valued: a
  valid symbol refused because your source had a bad minute is a worse failure
  than the typo the check exists to catch.

One thing to get right, because it is invisible until it is not: every row is
stamped with `name` in `option_snapshots.source`, and the reads act on that
stamp. Two providers derive implied volatility with different models, so the
same contract on the same day legitimately differs between them; the app
therefore shows one source at a time — the one that collected most recently
for that ticker — rather than blending them (see `db.active_source`). Pick a
name and never change it. Changing it does not merely rename rows: the old
name's history stops being the freshest under any name, so it disappears from
every screen while sitting in the database in full.

The hosted version of GammaGrid ships an adapter for a paid, licensed feed. It
is not in this repository; the interface it implements is exactly the one
above, so nothing stops you writing your own against whatever you subscribe to.
"""

from __future__ import annotations

from app import config
from app.providers.base import (
    CHAIN_COLUMNS,
    CHAIN_OPTIONAL_COLUMNS,
    DataProvider,
    ProviderStatus,
    is_rate_limited,
    with_retry,
)
from app.providers.cboe import CboeProvider


def __getattr__(name):
    if name == "YahooProvider":
        from app.providers.yahoo import YahooProvider
        return YahooProvider
    raise AttributeError(name)


class _LazyYahoo:
    name = "yahoo"

    def __getattr__(self, name):
        from app.providers.yahoo import YahooProvider
        provider = self.__dict__.get("_provider")
        if provider is None:
            provider = self._provider = YahooProvider()
        return getattr(provider, name)

DEFAULT_PROVIDER = "auto"

__all__ = [
    "CHAIN_COLUMNS",
    "CHAIN_OPTIONAL_COLUMNS",
    "DEFAULT_PROVIDER",
    "DataProvider",
    "ProviderStatus",
    "YahooProvider",
    "get_provider",
    "is_rate_limited",
    "known_providers",
    "with_retry",
]


def known_providers() -> tuple[str, ...]:
    return ("auto", "cboe", "yahoo")


def get_provider(name: str | None = None, token: str | None = None) -> DataProvider:
    """Builds a provider by name.

    An unknown name raises rather than quietly falling back to the default:
    silently substituting a different data source than the one asked for is the
    kind of surprise that gets diagnosed as "the numbers look wrong" weeks
    later.
    """
    resolved = (name or config.DATA_PROVIDER or DEFAULT_PROVIDER).strip().lower()

    if resolved == "auto":
        return AutoProvider()
    if resolved == "cboe":
        return CboeProvider()
    if resolved == "yahoo":
        from app.providers.yahoo import YahooProvider
        return YahooProvider()

    raise ValueError(
        f"Unknown data provider {resolved!r}. Known providers: {', '.join(known_providers())}. "
        "Adding one is a new file in app/providers/ — see the module docstring."
    )


class AutoProvider:
    """Selection policy only; the collector persists each actual provider name."""
    name = "auto"
    price_history_source = "yahoo"
    requires_token = False
    token = None
    unsupported_symbols = {"VIX": None, "VIXW": None}

    def __init__(self):
        self.candidates = (CboeProvider(), _LazyYahoo())

    def underlying_has_options(self, ticker):
        return self.candidates[0].underlying_has_options(ticker)

    def fetch_ticker_snapshot(self, ticker):
        errors = []
        for provider in self.candidates:
            try:
                spot, frame = provider.fetch_ticker_snapshot(ticker)
                frame.attrs["source"] = provider.name
                return spot, frame
            except Exception as exc:
                errors.append(f"{provider.name}: {exc}")
        raise RuntimeError("; ".join(errors))

    def fetch_price_history(self, ticker, period="6mo"):
        return self.candidates[1].fetch_price_history(ticker, period)

    def check_access(self):
        return self.candidates[0].check_access()
