package main

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"
)

type chainQuery struct {
	Expiries, Side string
	Band           float64
	MinOI, Page    int
}

type chainExpiries struct {
	options []viewmodel.ExpiryOption
	total   int
}

func (s *DesktopService) Chain(ctx context.Context, ticker string, q chainQuery) (*viewmodel.ChainResponse, error) {
	m, e := s.meta(ctx, ticker)
	if e != nil {
		return nil, e
	}
	ticker = m.Ticker
	out := &viewmodel.ChainResponse{Ticker: ticker, Spot: m.Spot, Empty: m.At == "", Expiries: []viewmodel.ExpiryOption{}, Pinned: []viewmodel.PinnedItem{}, Rows: []viewmodel.ChainRow{}, PageSize: 200}
	if out.Empty {
		return out, nil
	}
	// Preserve the numerical module's expiry boundary: 16:00 America/New_York.
	now := s.now().In(marketclock.Zone)
	day := now.Format("2006-01-02")
	includeToday := now.Hour() < 16
	base := `ticker=? AND source=? AND collected_at=? AND (expiry>? OR (expiry=? AND ?))`
	args := []any{ticker, m.Source, m.At, day, day, includeToday}
	union := "SELECT " + store.SnapshotColumns + " FROM option_snapshots WHERE " + base + " UNION ALL SELECT " + store.SnapshotColumns + " FROM option_snapshots_archive WHERE " + base
	bothArgs := append(append([]any{}, args...), args...)
	key := fmt.Sprintf("chain/%s/%s/%s/%s/%t", ticker, m.Source, m.At, day, includeToday)
	expiries, e := cached(s.cache, ctx, key+"/expiries", func(task context.Context) (chainExpiries, error) {
		rows, err := s.store.Read().QueryContext(task, "SELECT substr(expiry,1,10),count(*) FROM ("+union+") GROUP BY substr(expiry,1,10) ORDER BY substr(expiry,1,10)", bothArgs...)
		if err != nil {
			return chainExpiries{}, err
		}
		defer rows.Close()
		result := chainExpiries{}
		for rows.Next() {
			var exp string
			var count int
			if err := rows.Scan(&exp, &count); err != nil {
				return chainExpiries{}, err
			}
			t, err := time.Parse("2006-01-02", exp)
			if err != nil {
				return chainExpiries{}, err
			}
			result.options = append(result.options, viewmodel.ExpiryOption{ISO: exp, Label: t.Format("01-02") + " " + viewmodel.WeekdaysCN[t.Weekday()]})
			result.total += count
		}
		return result, rows.Err()
	})
	if e != nil {
		return nil, e
	}
	out.Expiries, out.TotalChain = expiries.options, expiries.total
	selected := []string{}
	raw := q.Expiries
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
			for _, option := range expiries.options {
				if exp == option.ISO {
					selected = append(selected, exp)
					break
				}
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
	side := q.Side
	if side == "call" || side == "put" {
		where += " AND option_type=?"
		filterArgs = append(filterArgs, side)
	}
	band := max(0, min(100, q.Band))
	if band > 0 {
		where += " AND strike BETWEEN ? AND ?"
		filterArgs = append(filterArgs, m.Spot*(1-band/100), m.Spot*(1+band/100))
	}
	minOI := max(0, min(q.MinOI, 1000000000))
	if minOI > 0 {
		where += " AND COALESCE(open_interest,0)>=?"
		filterArgs = append(filterArgs, minOI)
	}
	allArgs := append(append([]any{}, bothArgs...), filterArgs...)
	filterKey := fmt.Sprintf("%s/filter/%s/%s/%g/%d", key, strings.Join(selected, ","), side, band, minOI)
	out.TotalMatching, e = cached(s.cache, ctx, filterKey, func(task context.Context) (int, error) {
		var count int
		err := s.store.Read().QueryRowContext(task, "SELECT count(*) FROM ("+union+") WHERE "+where, allArgs...).Scan(&count)
		return count, err
	})
	if e != nil {
		return nil, e
	}
	out.Page = max(0, min(q.Page, (out.TotalMatching-1)/out.PageSize))
	query := "SELECT * FROM (" + union + ") WHERE " + where + " ORDER BY substr(expiry,1,10),strike,option_type LIMIT ? OFFSET ?"
	allArgs = append(allArgs, out.PageSize, out.Page*out.PageSize)
	pageKey := fmt.Sprintf("%s/page/%d", filterKey, out.Page)
	page, e := buildView(s, ctx, pageKey, func(task context.Context) ([]viewmodel.ChainRow, error) {
		sqlRows, err := s.store.Read().QueryContext(task, query, allArgs...)
		if err != nil {
			return nil, err
		}
		defer sqlRows.Close()
		storedRows, err := store.ScanSnapshots(sqlRows)
		if err != nil || len(storedRows) == 0 {
			return nil, err
		}
		return buildChainRowsGo(storedRows, m.Spot), nil
	})
	if e != nil {
		return nil, e
	}
	out.Rows = *page
	pins, e := s.store.Read().QueryContext(ctx, "SELECT id,CAST(expiry AS TEXT),strike,option_type FROM tracked_contracts WHERE ticker=? ORDER BY expiry,strike", ticker)
	if e != nil {
		return nil, e
	}
	defer pins.Close()
	for pins.Next() {
		var p viewmodel.PinnedItem
		if e = pins.Scan(&p.ID, &p.ExpiryISO, &p.Strike, &p.Type); e != nil {
			return nil, e
		}
		p.Label = fmt.Sprintf("%s %g%s", p.ExpiryISO[5:], p.Strike, strings.ToUpper(p.Type[:1]))
		out.Pinned = append(out.Pinned, p)
	}
	return out, pins.Err()
}

func calcScreenerDTE(expiryISO, momentStr string) int {
	tExp, ok1 := marketclock.ParseTime(expiryISO)
	tRef, ok2 := marketclock.ParseTime(momentStr)
	if !ok1 || !ok2 {
		return 0
	}
	tExpDay := time.Date(tExp.Year(), tExp.Month(), tExp.Day(), 0, 0, 0, 0, time.UTC)
	return int(math.Floor(tExpDay.Sub(tRef.UTC()).Seconds() / 86400.0))
}

func buildChainRowsGo(rows []store.StoredOptionRow, spot float64) []viewmodel.ChainRow {
	enrichGreeks(rows, defaultRiskFreeRate, 0.0)
	moment := rows[0].CollectedAt
	out := make([]viewmodel.ChainRow, len(rows))
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
		out[i] = viewmodel.ChainRow{
			ExpiryISO: expISO,
			Expiry:    viewmodel.FmtExpiryShort(expISO),
			DTE:       calcScreenerDTE(expISO, moment),
			Strike:    k,
			StrikeStr: viewmodel.FmtStrikeVal(k),
			Type:      otype,
			TypeShort: typeShort,
			Moneyness: moneyness,
			Bid:       viewmodel.FmtPrice(r.Bid, 2),
			Ask:       viewmodel.FmtPrice(r.Ask, 2),
			Last:      viewmodel.FmtPrice(r.LastPrice, 2),
			Volume:    volStr,
			OI:        oiStr,
			IV:        viewmodel.FmtPct(r.IV, 1, false),
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
