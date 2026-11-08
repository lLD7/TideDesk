package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"gammagrid.local/desktop/internal/marketclock"
)

// --- Store read/write queries for option analytics ---

func (s *Store) ActiveSource(ctx context.Context, ticker string) (string, error) {
	m, err := s.Meta(ctx, strings.ToUpper(ticker))
	return m.Source, err
}

func (s *Store) LatestMoment(ctx context.Context, ticker, source string) (string, error) {
	where := "ticker = ?"
	args := []any{strings.ToUpper(ticker)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	q := fmt.Sprintf(`SELECT COALESCE(max(collected_at), '') FROM (
		SELECT CAST(max(collected_at) AS TEXT) AS collected_at FROM option_snapshots WHERE %s
		UNION ALL
		SELECT CAST(max(collected_at) AS TEXT) FROM option_snapshots_archive WHERE %s
	)`, where, where)
	var at string
	err := s.read.QueryRowContext(ctx, q, append(args, args...)...).Scan(&at)
	return at, err
}

func (s *Store) CollectionMoments(ctx context.Context, ticker, source string, days int, perDay bool) ([]string, error) {
	where := "ticker = ? AND status = 'success' AND COALESCE(rows_fetched, 0) > 0"
	args := []any{strings.ToUpper(ticker)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	if days > 0 {
		where += " AND started_at >= datetime('now', '-' || ? || ' days')"
		args = append(args, days)
	}
	q := "SELECT CAST(started_at AS TEXT) FROM collection_runs WHERE " + where + " ORDER BY started_at DESC"
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var moments []string
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		moments = append(moments, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if !perDay {
		return moments, nil
	}
	seen := make(map[string]bool)
	var filtered []string
	for _, m := range moments {
		day := marketclock.MarketDay(m)
		if !seen[day] && marketclock.SessionStarted(m) {
			seen[day] = true
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

const SnapshotColumns = `ticker, CAST(collected_at AS TEXT) AS collected_at, underlying_price, substr(CAST(expiry AS TEXT),1,10) AS expiry, strike, option_type,
	last_price, bid, ask, volume, open_interest, implied_volatility, COALESCE(in_the_money, 0) AS in_the_money,
	delta, gamma, theta, vega, COALESCE(source, '') AS source, COALESCE(contract_symbol, '') AS contract_symbol, COALESCE(CAST(quote_timestamp AS TEXT), '') AS quote_timestamp`

func ScanSnapshots(rows *sql.Rows) ([]StoredOptionRow, error) {
	var out []StoredOptionRow
	for rows.Next() {
		var r StoredOptionRow
		var last, bid, ask, vol, oi, iv, delta, gamma, theta, vega sql.NullFloat64
		var itm int
		if err := rows.Scan(
			&r.Ticker, &r.CollectedAt, &r.UnderlyingPrice, &r.Expiry, &r.Strike, &r.OptionType,
			&last, &bid, &ask, &vol, &oi, &iv, &itm,
			&delta, &gamma, &theta, &vega, &r.Source, &r.ContractSymbol, &r.QuoteTimestamp,
		); err != nil {
			return nil, err
		}
		r.LastPrice = NullFloat(last)
		r.Bid = NullFloat(bid)
		r.Ask = NullFloat(ask)
		r.Volume = NullFloat(vol)
		r.OI = NullFloat(oi)
		r.IV = NullFloat(iv)
		r.ProviderIV = r.IV
		r.InTheMoney = itm != 0
		r.Delta = NullFloat(delta)
		r.Gamma = NullFloat(gamma)
		r.Theta = NullFloat(theta)
		r.Vega = NullFloat(vega)
		out = append(out, r)
	}
	return out, rows.Err()
}

func NullFloat(n sql.NullFloat64) *float64 {
	if !n.Valid || math.IsNaN(n.Float64) || math.IsInf(n.Float64, 0) {
		return nil
	}
	v := n.Float64
	return &v
}

func (s *Store) SnapshotsAt(ctx context.Context, ticker, source string, moments []string) ([]StoredOptionRow, error) {
	if len(moments) == 0 {
		return nil, nil
	}
	tkr := strings.ToUpper(ticker)
	if len(moments) == 1 {
		where := "ticker = ? AND collected_at = ?"
		args := []any{tkr, moments[0]}
		if source != "" {
			where += " AND source = ?"
			args = append(args, source)
		}
		q := fmt.Sprintf(`SELECT %s FROM option_snapshots WHERE %s
			UNION ALL
			SELECT %s FROM option_snapshots_archive WHERE %s
			ORDER BY collected_at`, SnapshotColumns, where, SnapshotColumns, where)
		rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return ScanSnapshots(rows)
	}
	momentsJSON, _ := json.Marshal(moments)
	where := "ticker = ? AND collected_at IN (SELECT value FROM json_each(?))"
	args := []any{tkr, string(momentsJSON)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	q := fmt.Sprintf(`SELECT %s FROM option_snapshots WHERE %s
		UNION ALL
		SELECT %s FROM option_snapshots_archive WHERE %s
		ORDER BY collected_at`, SnapshotColumns, where, SnapshotColumns, where)
	rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return ScanSnapshots(rows)
}

func (s *Store) LatestSnapshot(ctx context.Context, ticker, source string) (string, []StoredOptionRow, error) {
	moment, err := s.LatestMoment(ctx, ticker, source)
	if err != nil || moment == "" {
		return "", nil, err
	}
	rows, err := s.SnapshotsAt(ctx, ticker, source, []string{moment})
	return moment, rows, err
}

func (s *Store) CollectionDepth(ctx context.Context) (map[string]string, error) {
	rows, err := s.read.QueryContext(ctx, "SELECT ticker, date(min(collected_at)) FROM snapshot_iv_summary GROUP BY ticker")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[string]string)
	for rows.Next() {
		var tkr, d sql.NullString
		if err := rows.Scan(&tkr, &d); err == nil && tkr.Valid && d.Valid {
			out[strings.ToUpper(tkr.String)] = d.String
		}
	}
	return out, rows.Err()
}

func (s *Store) CollectionState(ctx context.Context, ticker, source string) (string, int, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT CAST(started_at AS TEXT), status, COALESCE(error_message, ''), COALESCE(rows_fetched, 0)
		 FROM collection_runs
		 WHERE ticker = ? AND (? = '' OR source = ?)
		 ORDER BY started_at DESC`,
		strings.ToUpper(ticker), source, source)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	failures := 0
	for rows.Next() {
		var started, status, errMsg string
		var count int
		if err := rows.Scan(&started, &status, &errMsg, &count); err != nil {
			return "", failures, err
		}
		if status == "success" && count > 0 {
			return started, failures, nil
		}
		if status == "failed" && !marketclock.IsClosedMarketRefusal(started, errMsg) {
			failures++
		}
	}
	return "", failures, rows.Err()
}

// StoredOptionRow represents a row from option_snapshots / option_snapshots_archive
// enriched with solved IV and Black-Scholes Greeks when needed.
type StoredOptionRow struct {
	Ticker          string
	CollectedAt     string
	UnderlyingPrice float64
	Expiry          string // YYYY-MM-DD
	Strike          float64
	OptionType      string // "call" or "put"
	LastPrice       *float64
	Bid             *float64
	Ask             *float64
	Volume          *float64
	OI              *float64
	IV              *float64
	ProviderIV      *float64
	InTheMoney      bool
	Delta           *float64
	Gamma           *float64
	Theta           *float64
	Vega            *float64
	Rho             *float64
	Vanna           *float64
	Charm           *float64
	Source          string
	ContractSymbol  string
	QuoteTimestamp  string
	IVIsOurs        bool
	IVUnreliable    bool
}

func (s *Store) ContractHistory(ctx context.Context, ticker, source, expiry string, strike float64, optType string, days int) ([]StoredOptionRow, error) {
	where := "ticker = ? AND substr(expiry,1,10) = ? AND strike = ? AND option_type = ?"
	args := []any{strings.ToUpper(ticker), expiry, strike, optType}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	if days > 0 {
		where += " AND collected_at >= datetime('now', '-' || ? || ' days')"
		args = append(args, days)
	}
	q := fmt.Sprintf(`SELECT %s FROM option_snapshots WHERE %s
		UNION ALL
		SELECT %s FROM option_snapshots_archive WHERE %s
		ORDER BY collected_at`, SnapshotColumns, where, SnapshotColumns, where)
	rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return ScanSnapshots(rows)
}
