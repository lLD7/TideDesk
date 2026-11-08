-- SQLite desktop schema, based on upstream v0.8.0.









CREATE TABLE IF NOT EXISTS watchlist (
    ticker   TEXT PRIMARY KEY,
    added_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS option_snapshots (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    ticker             TEXT NOT NULL,
    collected_at       TIMESTAMP NOT NULL,
    underlying_price   REAL NOT NULL,
    expiry             DATE NOT NULL,
    strike             REAL NOT NULL,
    option_type        TEXT NOT NULL CHECK (option_type IN ('call', 'put')),
    last_price         REAL,
    bid                REAL,
    ask                REAL,
    volume             INTEGER,
    open_interest      INTEGER,
    implied_volatility REAL,
    in_the_money       BOOLEAN,
    
    
    
    
    delta              REAL,
    gamma              REAL,
    theta              REAL,
    vega               REAL,
    
    
    
    
    source             TEXT NOT NULL DEFAULT 'yahoo'
);











CREATE INDEX IF NOT EXISTS idx_snapshots_ticker_source_date
    ON option_snapshots (ticker, source, collected_at);









CREATE INDEX IF NOT EXISTS idx_snapshots_contract_source
    ON option_snapshots (ticker, source, expiry, strike, option_type);

CREATE TABLE IF NOT EXISTS collection_runs (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at       TIMESTAMP NOT NULL,
    finished_at      TIMESTAMP,
    ticker           TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('success', 'failed')),
    error_message    TEXT,
    rows_fetched     INTEGER,
    oi_zero_fraction REAL
);

CREATE INDEX IF NOT EXISTS idx_collection_runs_started
    ON collection_runs (started_at DESC);

CREATE TABLE IF NOT EXISTS tracked_contracts (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    ticker      TEXT NOT NULL,
    expiry      DATE NOT NULL,
    strike      REAL NOT NULL,
    option_type TEXT NOT NULL CHECK (option_type IN ('call', 'put')),
    added_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (ticker, expiry, strike, option_type)
);









CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);











CREATE TABLE IF NOT EXISTS option_snapshots_archive (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    ticker             TEXT NOT NULL,
    collected_at       TIMESTAMP NOT NULL,
    underlying_price   REAL NOT NULL,
    expiry             DATE NOT NULL,
    strike             REAL NOT NULL,
    option_type        TEXT NOT NULL CHECK (option_type IN ('call', 'put')),
    last_price         REAL,
    bid                REAL,
    ask                REAL,
    volume             INTEGER,
    open_interest      INTEGER,
    implied_volatility REAL,
    in_the_money       BOOLEAN,
    
    
    
    
    delta              REAL,
    gamma              REAL,
    theta              REAL,
    vega               REAL,
    
    
    
    
    source             TEXT NOT NULL DEFAULT 'yahoo'
);










CREATE INDEX IF NOT EXISTS idx_snapshots_archive_ticker_source_date
    ON option_snapshots_archive (ticker, source, collected_at);

CREATE INDEX IF NOT EXISTS idx_snapshots_archive_contract_source
    ON option_snapshots_archive (ticker, source, expiry, strike, option_type);























CREATE TABLE IF NOT EXISTS snapshot_iv_summary (
    ticker          TEXT NOT NULL,
    source          TEXT NOT NULL,
    collected_at    TIMESTAMP NOT NULL,
    iv_weighted_avg REAL,
    PRIMARY KEY (ticker, source, collected_at)
);













CREATE TABLE IF NOT EXISTS contract_volume_stats (
    ticker         TEXT NOT NULL,
    source         TEXT NOT NULL,
    expiry         DATE NOT NULL,
    strike         REAL NOT NULL,
    option_type    TEXT NOT NULL,
    avg_volume     REAL,
    std_volume     REAL,
    history_points INTEGER NOT NULL,
    
    
    through_day    DATE NOT NULL,
    computed_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (ticker, source, expiry, strike, option_type)
);













INSERT INTO snapshot_iv_summary (ticker, source, collected_at, iv_weighted_avg)
SELECT
    ticker,
    source,
    collected_at,
    CASE WHEN SUM(CASE WHEN implied_volatility IS NOT NULL AND expiry >= date(collected_at)
                       THEN COALESCE(volume, 0) ELSE 0 END) > 0
         THEN SUM(CASE WHEN implied_volatility IS NOT NULL AND expiry >= date(collected_at)
                       THEN implied_volatility * COALESCE(volume, 0) ELSE 0 END)
              / SUM(CASE WHEN implied_volatility IS NOT NULL AND expiry >= date(collected_at)
                         THEN COALESCE(volume, 0) ELSE 0 END)
    END
FROM (
    SELECT ticker, source, collected_at, expiry, implied_volatility, volume
    FROM option_snapshots
    UNION ALL
    SELECT ticker, source, collected_at, expiry, implied_volatility, volume
    FROM option_snapshots_archive
) all_collected_rows
GROUP BY ticker, source, collected_at
ON CONFLICT (ticker, source, collected_at) DO NOTHING;

























ALTER TABLE collection_runs ADD COLUMN source TEXT NOT NULL DEFAULT 'yahoo';




CREATE INDEX IF NOT EXISTS idx_collection_runs_ticker_source_started
    ON collection_runs (ticker, source, started_at DESC);






CREATE INDEX IF NOT EXISTS idx_iv_summary_ticker_collected
    ON snapshot_iv_summary (ticker, collected_at DESC);
























CREATE TABLE IF NOT EXISTS contract_registry (
    ticker        TEXT NOT NULL,
    source        TEXT NOT NULL,
    expiry        DATE NOT NULL,
    strike        REAL NOT NULL,
    option_type   TEXT NOT NULL,
    first_seen_at TIMESTAMP NOT NULL,
    last_seen_at  TIMESTAMP NOT NULL,
    snapshots     BIGINT NOT NULL DEFAULT 0,
    PRIMARY KEY (ticker, source, expiry, strike, option_type)
);




CREATE INDEX IF NOT EXISTS idx_registry_expiry ON contract_registry(expiry);








INSERT INTO contract_registry
    (ticker, source, expiry, strike, option_type, first_seen_at, last_seen_at, snapshots)
SELECT ticker, source, expiry, strike, option_type,
       min(collected_at), max(collected_at), count(*)
FROM (
    SELECT ticker, source, expiry, strike, option_type, collected_at FROM option_snapshots
    UNION ALL
    SELECT ticker, source, expiry, strike, option_type, collected_at FROM option_snapshots_archive
) every_row
GROUP BY ticker, source, expiry, strike, option_type
ON CONFLICT (ticker, source, expiry, strike, option_type) DO UPDATE
SET first_seen_at = min(contract_registry.first_seen_at, EXCLUDED.first_seen_at),
    last_seen_at  = max(contract_registry.last_seen_at, EXCLUDED.last_seen_at),
    snapshots     = EXCLUDED.snapshots;





























ALTER TABLE snapshot_iv_summary ADD COLUMN iv_model TEXT;





CREATE INDEX IF NOT EXISTS idx_iv_summary_pending
    ON snapshot_iv_summary (ticker, source, collected_at DESC)
    WHERE iv_model IS NULL;




















CREATE TABLE IF NOT EXISTS option_symbols (
    symbol       TEXT PRIMARY KEY,
    company      TEXT,
    
    
    
    last_seen_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);



































CREATE TABLE IF NOT EXISTS realized_volatility (
    ticker      TEXT    NOT NULL,
    as_of_date  DATE    NOT NULL,
    window_days INTEGER NOT NULL,
    value       REAL NOT NULL,   
    source      TEXT    NOT NULL,
    fetched_at  TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (ticker, as_of_date, window_days)
);



































CREATE TABLE IF NOT EXISTS ticker_day_summary (
    ticker            TEXT             NOT NULL,
    source            TEXT             NOT NULL,
    day               DATE             NOT NULL,
    
    collected_at      TIMESTAMP        NOT NULL,
    underlying_price  REAL NOT NULL,
    
    
    net_gex           REAL NOT NULL,
    call_wall         REAL,
    put_wall          REAL,
    gamma_flip        REAL,
    state             TEXT             NOT NULL,
    expiry_count      INTEGER          NOT NULL,
    horizon_dte       INTEGER          NOT NULL,
    
    
    nearest_expiry    DATE,
    nearest_max_pain  REAL,
    move_lower        REAL,
    move_upper        REAL,
    
    
    contracts         INTEGER          NOT NULL,
    
    
    call_volume       BIGINT,
    put_volume        BIGINT,
    call_oi           BIGINT,
    put_oi            BIGINT,
    expiries          JSON            NOT NULL,
    
    
    gex_by_strike     JSON            NOT NULL,
    computed_at       TIMESTAMP        NOT NULL DEFAULT CURRENT_TIMESTAMP,
    
    
    
    core_sha          TEXT             NOT NULL,
    PRIMARY KEY (ticker, source, day)
);
ALTER TABLE option_snapshots ADD COLUMN contract_symbol TEXT;
ALTER TABLE option_snapshots_archive ADD COLUMN contract_symbol TEXT;
ALTER TABLE option_snapshots ADD COLUMN quote_timestamp TEXT;
ALTER TABLE option_snapshots_archive ADD COLUMN quote_timestamp TEXT;
