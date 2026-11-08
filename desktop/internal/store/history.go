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

type ContractID struct {
	Expiry string
	Strike float64
	Side   string
}

type VolumeStat struct {
	AvgVolume     *float64
	StdVolume     *float64
	HistoryPoints int
}

func (s *Store) VolumeStatsMap(ctx context.Context, ticker, source string) (map[ContractID]VolumeStat, error) {
	where := "ticker = ?"
	args := []any{strings.ToUpper(ticker)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	rows, err := s.read.QueryContext(ctx,
		"SELECT substr(expiry,1,10), strike, option_type, avg_volume, std_volume, COALESCE(history_points,0) FROM contract_volume_stats WHERE "+where,
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[ContractID]VolumeStat)
	for rows.Next() {
		var exp, side string
		var strike float64
		var avg, std sql.NullFloat64
		var pts int
		if err := rows.Scan(&exp, &strike, &side, &avg, &std, &pts); err == nil {
			out[ContractID{Expiry: exp, Strike: strike, Side: side}] = VolumeStat{
				AvgVolume:     NullFloat(avg),
				StdVolume:     NullFloat(std),
				HistoryPoints: pts,
			}
		}
	}
	return out, rows.Err()
}

type DaySummaryRecord struct {
	Day            string
	CollectedAt    string
	Spot           float64
	NetGEX         float64
	CallWall       *float64
	PutWall        *float64
	GammaFlip      *float64
	State          string
	ExpiryCount    int
	HorizonDTE     int
	NearestExpiry  string
	NearestMaxPain *float64
	MoveLower      *float64
	MoveUpper      *float64
	Contracts      int
	CallVolume     *int64
	PutVolume      *int64
	CallOI         *int64
	PutOI          *int64
	Expiries       []string
	GEXByStrike    [][2]float64
	CoreSHA        string
}

func (s *Store) SessionDays(ctx context.Context, ticker, source string, limit int) ([]string, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT substr(CAST(day AS TEXT),1,10), CAST(collected_at AS TEXT) FROM ticker_day_summary
		 WHERE ticker = ? AND source = ? ORDER BY day DESC`,
		strings.ToUpper(ticker), source)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var d, colAt string
		if err := rows.Scan(&d, &colAt); err == nil {
			if marketclock.SessionStarted(colAt) && marketclock.MarketDay(colAt) == d {
				days = append(days, d)
				if limit > 0 && len(days) >= limit {
					break
				}
			}
		}
	}
	return days, rows.Err()
}

func (s *Store) DaySummaryOn(ctx context.Context, ticker, source, day string) (*DaySummaryRecord, error) {
	var r DaySummaryRecord
	var cw, pw, gf, mp, ml, mu sql.NullFloat64
	var nearExp, expJSON, gexJSON, sha sql.NullString
	var cv, pv, coi, poi sql.NullInt64
	err := s.read.QueryRowContext(ctx,
		`SELECT substr(CAST(day AS TEXT),1,10), CAST(collected_at AS TEXT), underlying_price, net_gex, call_wall, put_wall, gamma_flip,
		        state, expiry_count, horizon_dte, COALESCE(substr(CAST(nearest_expiry AS TEXT),1,10),''), nearest_max_pain,
		        move_lower, move_upper, contracts, call_volume, put_volume, call_oi, put_oi,
		        COALESCE(expiries,'[]'), COALESCE(gex_by_strike,'[]'), COALESCE(core_sha,'')
		 FROM ticker_day_summary WHERE ticker = ? AND source = ? AND substr(CAST(day AS TEXT),1,10) = ?`,
		strings.ToUpper(ticker), source, day,
	).Scan(
		&r.Day, &r.CollectedAt, &r.Spot, &r.NetGEX, &cw, &pw, &gf,
		&r.State, &r.ExpiryCount, &r.HorizonDTE, &nearExp, &mp,
		&ml, &mu, &r.Contracts, &cv, &pv, &coi, &poi,
		&expJSON, &gexJSON, &sha,
	)
	if err != nil {
		return nil, err
	}
	r.CallWall = NullFloat(cw)
	r.PutWall = NullFloat(pw)
	r.GammaFlip = NullFloat(gf)
	r.NearestExpiry = nearExp.String
	r.NearestMaxPain = NullFloat(mp)
	r.MoveLower = NullFloat(ml)
	r.MoveUpper = NullFloat(mu)
	if cv.Valid {
		v := cv.Int64
		r.CallVolume = &v
	}
	if pv.Valid {
		v := pv.Int64
		r.PutVolume = &v
	}
	if coi.Valid {
		v := coi.Int64
		r.CallOI = &v
	}
	if poi.Valid {
		v := poi.Int64
		r.PutOI = &v
	}
	_ = json.Unmarshal([]byte(expJSON.String), &r.Expiries)
	_ = json.Unmarshal([]byte(gexJSON.String), &r.GEXByStrike)
	r.CoreSHA = sha.String
	return &r, nil
}

func (s *Store) MomentRollupIV(ctx context.Context, ticker, source, collectedAt string) *float64 {
	var iv sql.NullFloat64
	_ = s.read.QueryRowContext(ctx,
		"SELECT iv_weighted_avg FROM snapshot_iv_summary WHERE ticker = ? AND source = ? AND collected_at = ?",
		strings.ToUpper(ticker), source, collectedAt,
	).Scan(&iv)
	return NullFloat(iv)
}

func (s *Store) SnapshotRibbon(ctx context.Context, m SnapshotMeta, model string) ([]byte, error) {
	var payload []byte
	err := s.read.QueryRowContext(ctx,
		"SELECT ribbon_json FROM snapshot_iv_summary WHERE ticker=? AND source=? AND collected_at=? AND ribbon_model=?",
		m.Ticker, m.Source, m.At, model).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return payload, err
}

const pcrSums = `SUM(CASE WHEN option_type='call' THEN volume END),
	SUM(CASE WHEN option_type='put' THEN volume END),
	SUM(CASE WHEN option_type='call' THEN open_interest END),
	SUM(CASE WHEN option_type='put' THEN open_interest END)`

const pendingPCRRows = `
	SELECT o.ticker,o.source,o.collected_at,o.option_type,o.volume,o.open_interest
	FROM missing m CROSS JOIN option_snapshots o
	ON o.ticker=m.ticker AND o.source=m.source AND o.collected_at=m.collected_at
	UNION ALL
	SELECT o.ticker,o.source,o.collected_at,o.option_type,o.volume,o.open_interest
	FROM missing m CROSS JOIN option_snapshots_archive o
	ON o.ticker=m.ticker AND o.source=m.source AND o.collected_at=m.collected_at`

func (s *Store) BackfillPCR(ctx context.Context, ticker, source string) (bool, error) {
	var changed bool
	err := s.Transaction(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `WITH missing AS MATERIALIZED (
			SELECT ticker,source,collected_at FROM snapshot_iv_summary
			WHERE ticker=? AND source=? AND pcr_ready=0 ORDER BY collected_at DESC LIMIT 8
		), totals(ticker,source,collected_at,cv,pv,ci,pi) AS (
			SELECT ticker,source,collected_at,`+pcrSums+` FROM (`+pendingPCRRows+`)
			GROUP BY ticker,source,collected_at
		)
		UPDATE snapshot_iv_summary AS s SET call_volume=t.cv,put_volume=t.pv,call_oi=t.ci,put_oi=t.pi,pcr_ready=1
		FROM totals t WHERE s.ticker=t.ticker AND s.source=t.source AND s.collected_at=t.collected_at`, ticker, source)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		changed = n > 0
		return err
	})
	return changed, err
}

func (s *Store) PutCallRatioSeries(ctx context.Context, ticker, source string, days int) ([]PCRPoint, error) {
	where := "ticker = ?"
	args := []any{strings.ToUpper(ticker)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	if days > 0 {
		where += " AND collected_at >= datetime('now', '-' || ? || ' days')"
		args = append(args, days)
	}
	q := `WITH selected AS MATERIALIZED (
		SELECT ticker,source,collected_at,call_volume,put_volume,call_oi,put_oi,pcr_ready
		FROM snapshot_iv_summary WHERE ` + where + `
	), missing AS MATERIALIZED (
		SELECT ticker,source,collected_at FROM selected WHERE pcr_ready=0
	), totals(collected_at,cv,pv,ci,pi) AS (
		SELECT collected_at,call_volume,put_volume,call_oi,put_oi FROM selected WHERE pcr_ready=1
		UNION ALL
		SELECT collected_at,` + pcrSums + ` FROM (` + pendingPCRRows + `) GROUP BY collected_at
	)
	SELECT CAST(collected_at AS TEXT),SUM(cv),SUM(pv),SUM(ci),SUM(pi)
	FROM totals GROUP BY collected_at ORDER BY collected_at`
	rows, err := s.read.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PCRPoint
	for rows.Next() {
		var p PCRPoint
		var cv, pv, ci, pi sql.NullFloat64
		if err := rows.Scan(&p.Time, &cv, &pv, &ci, &pi); err != nil {
			return nil, err
		}
		if pv.Valid && cv.Valid && cv.Float64 > 0 {
			ratio := pv.Float64 / cv.Float64
			p.PCRVol = &ratio
		}
		if pi.Valid && ci.Valid && ci.Float64 > 0 {
			ratio := pi.Float64 / ci.Float64
			p.PCROI = &ratio
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

type welfordAccum struct {
	n      int
	mean   float64
	m2     float64
	maxDay string
}

func (w *welfordAccum) step(v float64, day string) {
	w.n++
	delta := v - w.mean
	w.mean += delta / float64(w.n)
	w.m2 += delta * (v - w.mean)
	if day > w.maxDay {
		w.maxDay = day
	}
}

func (s *Store) ComputeVolumeStatsWrites(ctx context.Context, ticker, source string, moments []string) ([]DerivedWrite, error) {
	momentsJSON, _ := json.Marshal(moments)
	where := "ticker = ? AND collected_at IN (SELECT value FROM json_each(?))"
	args := []any{ticker, string(momentsJSON)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	q := fmt.Sprintf(`SELECT COALESCE(source,''), substr(CAST(collected_at AS TEXT),1,10), substr(CAST(expiry AS TEXT),1,10), strike, option_type, volume
		FROM option_snapshots WHERE %s
		UNION ALL
		SELECT COALESCE(source,''), substr(CAST(collected_at AS TEXT),1,10), substr(CAST(expiry AS TEXT),1,10), strike, option_type, volume
		FROM option_snapshots_archive WHERE %s`, where, where)
	rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type statKey struct {
		source string
		expiry string
		strike float64
		side   string
	}
	accMap := make(map[statKey]*welfordAccum)
	var order []statKey
	for rows.Next() {
		var src, day, exp, side string
		var strike float64
		var vol sql.NullFloat64
		if err := rows.Scan(&src, &day, &exp, &strike, &side, &vol); err != nil {
			return nil, err
		}
		sk := statKey{source: src, expiry: exp, strike: strike, side: side}
		acc, exists := accMap[sk]
		if !exists {
			acc = &welfordAccum{maxDay: day}
			accMap[sk] = acc
			order = append(order, sk)
		} else if day > acc.maxDay {
			acc.maxDay = day
		}
		if vol.Valid && !math.IsNaN(vol.Float64) && !math.IsInf(vol.Float64, 0) {
			acc.step(vol.Float64, day)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sqlStmt := `INSERT INTO contract_volume_stats
		(ticker,source,expiry,strike,option_type,avg_volume,std_volume,history_points,through_day,computed_at)
		VALUES (?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
		ON CONFLICT (ticker,source,expiry,strike,option_type) DO UPDATE SET
		avg_volume=EXCLUDED.avg_volume, std_volume=EXCLUDED.std_volume,
		history_points=EXCLUDED.history_points, through_day=EXCLUDED.through_day,
		computed_at=EXCLUDED.computed_at`

	writes := make([]DerivedWrite, 0, len(order))
	for _, sk := range order {
		acc := accMap[sk]
		var avgPtr, stdPtr *float64
		if acc.n > 0 {
			m := acc.mean
			avgPtr = &m
		}
		if acc.n > 1 {
			sd := math.Sqrt(math.Max(0, acc.m2) / float64(acc.n-1))
			stdPtr = &sd
		}
		writes = append(writes, DerivedWrite{SQL: sqlStmt, Args: []any{ticker, sk.source, sk.expiry, sk.strike, sk.side, avgPtr, stdPtr, acc.n, acc.maxDay}})
	}
	return writes, nil
}

func (s *Store) IvWeightedAverageSeries(ctx context.Context, ticker, source string, days int) ([]IVPoint, error) {
	where := "ticker = ?"
	args := []any{strings.ToUpper(ticker)}
	if source != "" {
		where += " AND source = ?"
		args = append(args, source)
	}
	if days > 0 {
		where += " AND collected_at >= datetime('now', '-' || ? || ' days')"
		args = append(args, days)
	}
	rows, err := s.read.QueryContext(ctx,
		"SELECT CAST(collected_at AS TEXT), iv_weighted_avg FROM snapshot_iv_summary WHERE "+where+" ORDER BY collected_at",
		args...)
	if err != nil {
		return []IVPoint{}, err
	}
	defer rows.Close()
	out := []IVPoint{}
	for rows.Next() {
		var t string
		var v sql.NullFloat64
		if err := rows.Scan(&t, &v); err == nil && v.Valid && !math.IsNaN(v.Float64) {
			out = append(out, IVPoint{At: t, IV: v.Float64})
		}
	}
	return out, rows.Err()
}

func (s *Store) RealizedVolatilityItems(ctx context.Context, ticker string) ([]RVPoint, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT window_days, value FROM realized_volatility
		 WHERE ticker = ? AND as_of_date = (SELECT max(as_of_date) FROM realized_volatility WHERE ticker = ?)
		 ORDER BY window_days`,
		strings.ToUpper(ticker), strings.ToUpper(ticker))
	if err != nil {
		return []RVPoint{}, err
	}
	defer rows.Close()
	out := []RVPoint{}
	for rows.Next() {
		var w int
		var v float64
		if err := rows.Scan(&w, &v); err == nil && !math.IsNaN(v) {
			out = append(out, RVPoint{WindowDays: w, Value: v})
		}
	}
	return out, rows.Err()
}

type IVPoint struct {
	At string
	IV float64
}
type RVPoint struct {
	WindowDays int
	Value      float64
}
type PCRPoint struct {
	Time          string
	PCRVol, PCROI *float64
}
