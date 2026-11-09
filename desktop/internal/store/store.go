package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/provider"

	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	read, write *sql.DB
	owner       *os.File
	path        string
	writeMu     sync.Mutex
	revision    atomic.Uint64
}

func Open(root, path string) (s *Store, err error) {
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	owner, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		owner.Close()
		return nil, fmt.Errorf("数据库已被另一个 TideDesk 或旧版 GammaGrid 使用，请先关闭它")
	}
	s = &Store{owner: owner, path: path}
	defer func(opened *Store) {
		if err != nil {
			opened.Close()
		}
	}(s)
	uri := (&url.URL{Scheme: "file", Path: path}).String()
	s.write, err = sql.Open("sqlite3", uri+"?_busy_timeout=10000&_journal_mode=WAL&_synchronous=FULL&_foreign_keys=on&_cache_size=-2000")
	if err != nil {
		return nil, err
	}
	s.write.SetMaxOpenConns(1)
	s.write.SetMaxIdleConns(1)
	if err = s.migrate(root); err != nil {
		return nil, err
	}
	s.read, err = sql.Open("sqlite3", uri+"?mode=ro&_busy_timeout=10000&_query_only=on&_cache_size=-2000")
	if err != nil {
		return nil, err
	}
	s.read.SetMaxOpenConns(2)
	s.read.SetMaxIdleConns(2)
	err = s.read.Ping()
	if err == nil {
		err = os.Chmod(path, 0600)
	}
	return s, err
}
func (s *Store) Close() {
	if s.read != nil {
		s.read.Close()
	}
	if s.write != nil {
		s.write.Close()
	}
	if s.owner != nil {
		s.owner.Close()
	}
}
func (s *Store) migrate(root string) error {
	tx, e := s.write.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY,name TEXT NOT NULL,checksum TEXT NOT NULL,applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)`); e != nil {
		return e
	}
	paths, e := filepath.Glob(filepath.Join(root, "app/sqlite_migrations/*.sql"))
	if e != nil {
		return e
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return fmt.Errorf("SQLite migrations are missing")
	}
	for _, p := range paths {
		name := strings.TrimSuffix(filepath.Base(p), ".sql")
		parts := strings.SplitN(name, "_", 2)
		if len(parts) != 2 {
			return fmt.Errorf("invalid migration %s", name)
		}
		b, e := os.ReadFile(p)
		if e != nil {
			return e
		}
		hash := sha256.Sum256(b)
		digest := hex.EncodeToString(hash[:])
		var old string
		e = tx.QueryRow("SELECT checksum FROM schema_migrations WHERE version=?", parts[0]).Scan(&old)
		if e == nil {
			if old != digest {
				return fmt.Errorf("已应用的迁移被修改: %s", name)
			}
			continue
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if _, e = tx.Exec(string(b)); e != nil {
			return fmt.Errorf("migration %s: %w", name, e)
		}
		if _, e = tx.Exec("INSERT INTO schema_migrations(version,name,checksum) VALUES(?,?,?)", parts[0], parts[1], digest); e != nil {
			return e
		}
	}
	if _, e = tx.Exec(`CREATE TABLE IF NOT EXISTS intraday_tide_bars (
		ticker TEXT NOT NULL,
		source TEXT NOT NULL,
		trading_day TEXT NOT NULL,
		bucket_time TEXT NOT NULL,
		spot REAL NOT NULL,
		call_vol REAL NOT NULL DEFAULT 0,
		put_vol REAL NOT NULL DEFAULT 0,
		call_prem REAL NOT NULL DEFAULT 0,
		put_prem REAL NOT NULL DEFAULT 0,
		net_prem REAL NOT NULL DEFAULT 0,
		weekly_call_prem REAL NOT NULL DEFAULT 0,
		weekly_put_prem REAL NOT NULL DEFAULT 0,
		weekly_net_prem REAL NOT NULL DEFAULT 0,
		weekly_net_gex REAL NOT NULL DEFAULT 0,
		is_baseline INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (ticker, source, trading_day, bucket_time)
	)`); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Transaction(ctx context.Context, fn func(*sql.Tx) error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	tx, e := s.write.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var before, after int64
	if e = tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&before); e != nil {
		return e
	}
	if e = fn(tx); e != nil {
		return e
	}
	if e = tx.QueryRowContext(ctx, "SELECT total_changes()").Scan(&after); e != nil {
		return e
	}
	if e = tx.Commit(); e == nil && after != before {
		s.revision.Add(1)
	}
	return e
}
func (s *Store) Setting(ctx context.Context, key string) (string, error) {
	var v string
	e := s.read.QueryRowContext(ctx, "SELECT value FROM app_settings WHERE key=?", key).Scan(&v)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return v, e
}
func SetSetting(tx *sql.Tx, key, value string) error {
	_, e := tx.Exec("INSERT INTO app_settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=CURRENT_TIMESTAMP", key, value)
	return e
}
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	return s.Transaction(ctx, func(tx *sql.Tx) error { return SetSetting(tx, key, value) })
}
func (s *Store) Watchlist(ctx context.Context) ([]string, error) {
	rows, e := s.read.QueryContext(ctx, "SELECT ticker FROM watchlist ORDER BY ticker")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var t string
		if e = rows.Scan(&t); e != nil {
			return nil, e
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

type SnapshotMeta struct {
	Ticker, Source, At, Since string
	Spot                      float64
}

func (s *Store) Meta(ctx context.Context, ticker string) (SnapshotMeta, error) {
	rows, err := s.snapshotMetas(ctx, "SELECT ?", ticker)
	if err != nil {
		return SnapshotMeta{}, err
	}
	return rows[ticker], nil
}

func (s *Store) WatchlistMetas(ctx context.Context) (map[string]SnapshotMeta, error) {
	return s.snapshotMetas(ctx, "SELECT ticker FROM watchlist")
}

func (s *Store) snapshotMetas(ctx context.Context, symbols string, args ...any) (map[string]SnapshotMeta, error) {
	q := `WITH symbols(ticker) AS (` + symbols + `),
	sources AS MATERIALIZED (
		SELECT ticker, COALESCE(
			(SELECT source FROM collection_runs WHERE ticker=w.ticker AND status='success' AND COALESCE(rows_fetched,0)>0 ORDER BY started_at DESC,id DESC LIMIT 1),
			(SELECT source FROM snapshot_iv_summary WHERE ticker=w.ticker ORDER BY collected_at DESC LIMIT 1), '') AS source
		FROM symbols w
	), moments AS MATERIALIZED (
		SELECT ticker,source,COALESCE((SELECT MAX(at) FROM (
			SELECT CAST(MAX(collected_at) AS TEXT) AS at FROM option_snapshots WHERE ticker=m.ticker AND source=m.source
			UNION ALL
			SELECT CAST(MAX(collected_at) AS TEXT) FROM option_snapshots_archive WHERE ticker=m.ticker AND source=m.source
		)), '') AS at FROM sources m
	)
	SELECT ticker,source,at,COALESCE((
		SELECT underlying_price FROM option_snapshots WHERE ticker=m.ticker AND source=m.source AND collected_at=m.at
		UNION ALL
		SELECT underlying_price FROM option_snapshots_archive WHERE ticker=m.ticker AND source=m.source AND collected_at=m.at LIMIT 1
	), 0), COALESCE((
		SELECT CAST(MIN(collected_at) AS TEXT) FROM snapshot_iv_summary WHERE ticker=m.ticker AND source=m.source
	), '') FROM moments m`
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]SnapshotMeta)
	for rows.Next() {
		var m SnapshotMeta
		if err := rows.Scan(&m.Ticker, &m.Source, &m.At, &m.Spot, &m.Since); err != nil {
			return nil, err
		}
		out[m.Ticker] = m
	}
	return out, rows.Err()
}

func DedupOptions(ticker string, rows []provider.OptionRow) []provider.OptionRow {
	out := make([]provider.OptionRow, 0, len(rows))
	positions := make(map[ContractID]int, len(rows))
	rank := func(o provider.OptionRow) int {
		if len(o.Symbol) <= 15 {
			return 2
		}
		if strings.TrimSpace(o.Symbol[:len(o.Symbol)-15]) == ticker {
			return 0
		}
		return 1
	}
	for _, r := range rows {
		key := ContractID{Expiry: r.Expiry, Strike: r.Strike, Side: r.Type}
		i, ok := positions[key]
		if !ok {
			positions[key] = len(out)
			out = append(out, r)
		} else if rank(r) < rank(out[i]) {
			out[i] = r
		}
	}
	return out
}
func countValue(v *float64) any {
	if v == nil || *v >= float64(math.MaxInt64) || *v < 0 {
		return nil
	}
	return int64(*v)
}
func (s *Store) SaveSnapshot(ctx context.Context, ticker, source string, at time.Time, snap provider.Snapshot, zeroFraction float64) error {
	rows := DedupOptions(ticker, snap.Rows)
	when := marketclock.Stamp(at)
	return s.Transaction(ctx, func(tx *sql.Tx) error {
		stmt, e := tx.Prepare(`INSERT INTO option_snapshots(ticker,collected_at,underlying_price,expiry,strike,option_type,last_price,bid,ask,volume,open_interest,implied_volatility,in_the_money,delta,gamma,theta,vega,source,contract_symbol,quote_timestamp) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if e != nil {
			return e
		}
		defer stmt.Close()
		for _, r := range rows {
			if _, e = stmt.ExecContext(ctx, ticker, when, snap.Spot, r.Expiry, r.Strike, r.Type, r.Last, r.Bid, r.Ask, countValue(r.Volume), countValue(r.OI), r.IV, r.ITM, r.Delta, r.Gamma, r.Theta, r.Vega, source, r.Symbol, r.QuoteTime); e != nil {
				return e
			}
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO contract_registry(ticker,source,expiry,strike,option_type,first_seen_at,last_seen_at,snapshots) SELECT ticker,source,expiry,strike,option_type,?,?,1 FROM option_snapshots WHERE ticker=? AND source=? AND collected_at=? GROUP BY expiry,strike,option_type ON CONFLICT(ticker,source,expiry,strike,option_type) DO UPDATE SET last_seen_at=max(contract_registry.last_seen_at,excluded.last_seen_at),snapshots=contract_registry.snapshots+1`, when, when, ticker, source, when)
		if e != nil {
			return e
		}
		// This is the existing SQL rollup, not a new numerical model.
		_, e = tx.ExecContext(ctx, `INSERT INTO snapshot_iv_summary(ticker,source,collected_at,iv_weighted_avg,call_volume,put_volume,call_oi,put_oi,pcr_ready) SELECT ?,?,?,CASE WHEN SUM(CASE WHEN implied_volatility IS NOT NULL AND expiry>=date(collected_at) THEN COALESCE(volume,0) ELSE 0 END)>0 THEN SUM(CASE WHEN implied_volatility IS NOT NULL AND expiry>=date(collected_at) THEN implied_volatility*COALESCE(volume,0) ELSE 0 END)/SUM(CASE WHEN implied_volatility IS NOT NULL AND expiry>=date(collected_at) THEN COALESCE(volume,0) ELSE 0 END) END, `+pcrSums+`, 1 FROM option_snapshots WHERE ticker=? AND source=? AND collected_at=? ON CONFLICT(ticker,source,collected_at) DO UPDATE SET iv_weighted_avg=excluded.iv_weighted_avg,iv_model=NULL,call_volume=excluded.call_volume,put_volume=excluded.put_volume,call_oi=excluded.call_oi,put_oi=excluded.put_oi,pcr_ready=1`, ticker, source, when, ticker, source, when)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, "INSERT INTO collection_runs(started_at,finished_at,ticker,status,rows_fetched,oi_zero_fraction,source) VALUES(?,?,?,'success',?,?,?)", when, marketclock.Stamp(time.Now()), ticker, len(snap.Rows), zeroFraction, source)
		return e
	})
}
func (s *Store) LogFailure(ctx context.Context, ticker, source string, at time.Time, message string, rows any, zero any) error {
	return s.Transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO collection_runs(started_at,finished_at,ticker,status,error_message,rows_fetched,oi_zero_fraction,source) VALUES(?,?,?,'failed',?,?,?,?)`, marketclock.Stamp(at), marketclock.Stamp(time.Now()), ticker, message, rows, zero, source)
		return e
	})
}

func (s *Store) Backup(ctx context.Context, path string) error {
	path, e := filepath.Abs(path)
	if e != nil {
		return e
	}
	if path == s.path {
		return fmt.Errorf("备份路径不能是当前数据库")
	}
	if _, e = os.Stat(path); e == nil {
		return fmt.Errorf("备份已存在: %s", path)
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, e = s.write.ExecContext(ctx, "VACUUM INTO ?", path)
	if e == nil {
		e = os.Chmod(path, 0600)
	}
	return e
}
func (s *Store) Archive(ctx context.Context) error {
	return s.Transaction(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `INSERT INTO option_snapshots_archive SELECT o.* FROM contract_registry r JOIN option_snapshots o ON r.ticker=o.ticker AND r.source=o.source AND r.expiry=o.expiry AND r.strike=o.strike AND r.option_type=o.option_type WHERE r.expiry<date('now','-30 days')`)
		if e != nil {
			return e
		}
		_, e = tx.ExecContext(ctx, `DELETE FROM option_snapshots WHERE id IN (SELECT id FROM option_snapshots_archive)`)
		return e
	})
}

// DerivedWrite is constructed by the Go analytics layer.
type DerivedWrite struct {
	SQL  string
	Args []any
}

func (s *Store) ApplyDerived(ctx context.Context, writes []DerivedWrite) error {
	if len(writes) == 0 {
		return nil
	}
	return s.Transaction(ctx, func(tx *sql.Tx) error {
		for _, w := range writes {
			if _, err := tx.ExecContext(ctx, w.SQL, w.Args...); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) Read() *sql.DB           { return s.read }
func (s *Store) Path() string            { return s.path }
func (s *Store) Revision() uint64        { return s.revision.Load() }
func (s *Store) WriteStats() sql.DBStats { return s.write.Stats() }
