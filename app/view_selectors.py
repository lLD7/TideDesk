"""Pure table selection shared by native and browser views."""
import numpy as np
import pandas as pd


def strikes_around_money(matrix: pd.DataFrame, spot: float, each_side: int) -> pd.DataFrame:
    """The strike closest to the money, plus `each_side` rows above and below.

    A FUNCTION AND NOT THREE LINES IN THE PAGE, because those three lines were a
    defect that shipped and was reported from production. The control
    then asked for a percentage band and the page trimmed the result to 45 rows
    around the money afterwards, so on any symbol with more than 45 strikes
    inside the narrowest band it did nothing whatsoever: SPY has 77 strikes
    within ±5% of 769.35, SPX has 154 within ±5% of 7711.76. Here the count is
    testable, and the test asserts what the page could not — that asking for
    more rows returns more rows.

    COUNTED IN ROWS RATHER THAN IN PERCENT, decided 30.08 after the percentage
    version was tried. Percent means something different on every chain: ±5% is
    8 strikes on MO, 77 on SPY and 154 on SPX, so one number could not be a
    sensible default for all three, and the money drifted out of view on the
    dense ones. A count is the same request everywhere, and it keeps the money
    in the middle of the table by construction.

    CLAMPED AT THE ENDS, NOT BACKFILLED. When the money sits near the top of the
    chain there are fewer rows above it, and the table is then shorter — showing
    extra rows from the other side to reach 2n+1 would answer a question nobody
    asked, and quietly at that.
    """
    if matrix.empty:
        return matrix
    strikes = pd.to_numeric(pd.Series(matrix.index), errors="coerce").to_numpy()
    atm_position = int(np.abs(strikes - spot).argmin())
    start = max(0, atm_position - each_side)
    end = min(len(matrix), atm_position + each_side + 1)
    return matrix.iloc[start:end]
