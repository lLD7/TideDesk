package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *DesktopService) chain(ctx context.Context, q url.Values) (*ChainResponse, error) {
	ticker, e := normalizeSymbol(q.Get("ticker"))
	if e != nil {
		return nil, e
	}
	m, e := s.store.meta(ctx, ticker)
	if e != nil {
		return nil, e
	}
	out := &ChainResponse{Ticker: ticker, Spot: m.Spot, Empty: m.At == "", Expiries: []ExpiryOption{}, Pinned: []PinnedItem{}, Rows: []ChainRow{}, PageSize: 200}
	if out.Empty {
		return out, nil
	}
	// Preserve the numerical module's expiry boundary: 16:00 America/New_York.
	now := s.now().In(marketZone)
	day := now.Format("2006-01-02")
	includeToday := now.Hour() < 16
	base := `ticker=? AND source=? AND collected_at=? AND (expiry>? OR (expiry=? AND ?))`
	args := []any{ticker, m.Source, m.At, day, day, includeToday}
	union := "SELECT " + snapshotSelectCols + " FROM option_snapshots WHERE " + base + " UNION ALL SELECT " + snapshotSelectCols + " FROM option_snapshots_archive WHERE " + base
	bothArgs := append(append([]any{}, args...), args...)
	expRows, e := s.store.read.QueryContext(ctx, "SELECT substr(expiry,1,10),count(*) FROM ("+union+") GROUP BY substr(expiry,1,10) ORDER BY substr(expiry,1,10)", bothArgs...)
	if e != nil {
		return nil, e
	}
	valid := map[string]bool{}
	for expRows.Next() {
		var exp string
		var n int
		if e = expRows.Scan(&exp, &n); e != nil {
			expRows.Close()
			return nil, e
		}
		valid[exp] = true
		t, e := time.Parse("2006-01-02", exp)
		if e != nil {
			expRows.Close()
			return nil, e
		}
		out.Expiries = append(out.Expiries, ExpiryOption{ISO: exp, Label: t.Format("01-02") + " " + weekdaysCNByGoWeekday[t.Weekday()]})
		out.TotalChain += n
	}
	e = expRows.Err()
	expRows.Close()
	if e != nil {
		return nil, e
	}
	selected := []string{}
	raw := q.Get("expiries")
	if raw == "" {
		for _, exp := range out.Expiries[:min(2, len(out.Expiries))] {
			selected = append(selected, exp.ISO)
		}
	} else {
		for _, exp := range strings.Split(raw, ",") {
			exp = strings.TrimSpace(exp)
			if _, e = time.Parse("2006-01-02", exp); e != nil {
				return nil, fmt.Errorf("无效到期日")
			}
			if valid[exp] {
				selected = append(selected, exp)
			}
		}
	}
	where := "0"
	filterArgs := []any{}
	if len(selected) > 0 {
		where = "substr(expiry,1,10) IN (" + strings.TrimRight(strings.Repeat("?,", len(selected)), ",") + ")"
		for _, exp := range selected {
			filterArgs = append(filterArgs, exp)
		}
	}
	side := q.Get("side")
	if side == "call" || side == "put" {
		where += " AND option_type=?"
		filterArgs = append(filterArgs, side)
	}
	band := 10.0
	if v, e := strconv.ParseFloat(q.Get("band"), 64); e == nil {
		band = max(0, min(100, v))
	}
	if band > 0 {
		where += " AND strike BETWEEN ? AND ?"
		filterArgs = append(filterArgs, m.Spot*(1-band/100), m.Spot*(1+band/100))
	}
	minOI := intQuery(q, "min_oi", 0, 0, 1000000000)
	if minOI > 0 {
		where += " AND COALESCE(open_interest,0)>=?"
		filterArgs = append(filterArgs, minOI)
	}
	allArgs := append(append([]any{}, bothArgs...), filterArgs...)
	e = s.store.read.QueryRowContext(ctx, "SELECT count(*) FROM ("+union+") WHERE "+where, allArgs...).Scan(&out.TotalMatching)
	if e != nil {
		return nil, e
	}
	out.PageSize = intQuery(q, "page_size", 200, 1, 400)
	out.Page = intQuery(q, "page", 0, 0, 100000)
	out.Page = min(out.Page, max(0, (out.TotalMatching-1)/out.PageSize))
	query := "SELECT * FROM (" + union + ") WHERE " + where + " ORDER BY substr(expiry,1,10),strike,option_type LIMIT ? OFFSET ?"
	allArgs = append(allArgs, out.PageSize, out.Page*out.PageSize)
	key := fmt.Sprintf("chain/%d/%s/%s/%s/%s/%t/%s", s.store.revision.Load(), ticker, m.Source, m.At, day, includeToday, q.Encode())
	b, e := s.cache.Do(ctx, key, func(task context.Context) ([]byte, error) {
		sqlRows, err := s.store.read.QueryContext(task, query, allArgs...)
		if err != nil {
			return nil, err
		}
		defer sqlRows.Close()
		storedRows, err := scanStoredOptionRows(sqlRows)
		if err != nil {
			return nil, err
		}
		if len(storedRows) == 0 {
			return []byte(`{"rows":[]}`), nil
		}
		chainRows := buildChainRowsGo(storedRows, m.Spot)
		return json.Marshal(map[string]any{"rows": chainRows})
	})
	if e != nil {
		return nil, e
	}
	var result struct {
		Rows []ChainRow `json:"rows"`
	}
	if e = json.Unmarshal(b, &result); e != nil {
		return nil, e
	}
	out.Rows = result.Rows
	pins, e := s.store.read.QueryContext(ctx, "SELECT id,CAST(expiry AS TEXT),strike,option_type FROM tracked_contracts WHERE ticker=? ORDER BY expiry,strike", ticker)
	if e != nil {
		return nil, e
	}
	defer pins.Close()
	for pins.Next() {
		var p PinnedItem
		if e = pins.Scan(&p.ID, &p.ExpiryISO, &p.Strike, &p.Type); e != nil {
			return nil, e
		}
		p.Label = fmt.Sprintf("%s %g%s", p.ExpiryISO[5:], p.Strike, strings.ToUpper(p.Type[:1]))
		out.Pinned = append(out.Pinned, p)
	}
	return out, pins.Err()
}

func calcScreenerDTE(expiryISO, momentStr string) int {
	tExp, ok1 := parseAnyTime(expiryISO)
	tRef, ok2 := parseAnyTime(momentStr)
	if !ok1 || !ok2 {
		return 0
	}
	tExpDay := time.Date(tExp.Year(), tExp.Month(), tExp.Day(), 0, 0, 0, 0, time.UTC)
	return int(math.Floor(tExpDay.Sub(tRef.UTC()).Seconds() / 86400.0))
}

func buildChainRowsGo(rows []StoredOptionRow, spot float64) []ChainRow {
	enrichGreeks(rows, defaultRiskFreeRate, 0.0)
	moment := rows[0].CollectedAt
	out := make([]ChainRow, len(rows))
	for i := range rows {
		r := &rows[i]
		expISO := r.Expiry
		if len(expISO) >= 10 {
			expISO = expISO[:10]
		}
		k := r.Strike
		otype := r.OptionType
		moneynessPct := (k/math.Max(spot, 1e-9) - 1.0) * 100.0
		moneyness := fmt.Sprintf("%+.1f%%", moneynessPct)
		typeShort := "P"
		if otype == "call" {
			typeShort = "C"
		}
		volStr := "0"
		if isValidFinite(r.Volume) {
			volStr = formatIntComma(int64(*r.Volume))
		}
		oiStr := "0"
		if isValidFinite(r.OI) {
			oiStr = formatIntComma(int64(*r.OI))
		}
		deltaStr, gammaStr, thetaStr, vegaStr, vannaStr, charmStr := "—", "—", "—", "—", "—", "—"
		if isValidFinite(r.Delta) {
			deltaStr = fmt.Sprintf("%+.3f", *r.Delta)
		}
		if isValidFinite(r.Gamma) {
			gammaStr = fmt.Sprintf("%.4f", *r.Gamma)
		}
		if isValidFinite(r.Theta) {
			thetaStr = fmt.Sprintf("%+.3f", *r.Theta)
		}
		if isValidFinite(r.Vega) {
			vegaStr = fmt.Sprintf("%.3f", *r.Vega)
		}
		if isValidFinite(r.Vanna) {
			vannaStr = fmt.Sprintf("%+.4f", *r.Vanna)
		}
		if isValidFinite(r.Charm) {
			charmStr = fmt.Sprintf("%+.4f", *r.Charm)
		}
		out[i] = ChainRow{
			ExpiryISO: expISO,
			Expiry:    fmtExpiryShort(expISO),
			DTE:       calcScreenerDTE(expISO, moment),
			Strike:    k,
			StrikeStr: fmtStrikeVal(k),
			Type:      otype,
			TypeShort: typeShort,
			Moneyness: moneyness,
			Bid:       fmtPrice(r.Bid, 2),
			Ask:       fmtPrice(r.Ask, 2),
			Last:      fmtPrice(r.LastPrice, 2),
			Volume:    volStr,
			OI:        oiStr,
			IV:        fmtPct(r.IV, 1, false),
			Delta:     deltaStr,
			Gamma:     gammaStr,
			Theta:     thetaStr,
			Vega:      vegaStr,
			Vanna:     vannaStr,
			Charm:     charmStr,
		}
	}
	return out
}
