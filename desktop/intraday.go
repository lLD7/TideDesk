package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/provider"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"
)

type intradayBaseline struct {
	tradingDay string
	source     string
	volumes    map[store.ContractID]float64
}

func optionMidPrice(last, bid, ask *float64) float64 {
	b := valOrZero(bid)
	a := valOrZero(ask)
	l := valOrZero(last)
	if b > 0 && a > 0 {
		return (b + a) * 0.5
	}
	if l > 0 {
		return l
	}
	if b > 0 {
		return b
	}
	if a > 0 {
		return a
	}
	return 0
}

func fmtDollarComma(v float64, signed bool) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "$0"
	}
	neg := v < 0
	absVal := int64(math.Round(math.Abs(v)))
	numStr := formatIntComma(absVal)
	if neg {
		return "-$" + numStr
	}
	if signed && absVal > 0 {
		return "+$" + numStr
	}
	return "$" + numStr
}

func fmtDollarCompact(v float64, signed bool) string {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return "$0"
	}
	neg := v < 0
	a := math.Abs(v)
	var body string
	switch {
	case a >= 1e9:
		body = fmt.Sprintf("$%.2fB", a/1e9)
	case a >= 1e6:
		body = fmt.Sprintf("$%.2fM", a/1e6)
	case a >= 1e3:
		body = fmt.Sprintf("$%.1fK", a/1e3)
	default:
		body = fmt.Sprintf("$%.0f", a)
	}
	if neg {
		return "-" + body
	}
	if signed && a > 0 {
		return "+" + body
	}
	return body
}

func fmtVolCompact(v float64) string {
	if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
		return "0"
	}
	switch {
	case v >= 1e6:
		return fmt.Sprintf("%.2fM", v/1e6)
	case v >= 1e3:
		return fmt.Sprintf("%.1fK", v/1e3)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

func weeklyExpirySetFromRows(rows []store.StoredOptionRow, refMoment string) (map[string]bool, string) {
	expDTE := make(map[string]int)
	for i := range rows {
		exp := rows[i].Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		if _, seen := expDTE[exp]; !seen {
			if yearsToExpiry(exp, refMoment) > 0 {
				expDTE[exp] = viewmodel.CalcDTE(exp, refMoment)
			}
		}
	}
	weeklySet := make(map[string]bool)
	minDTE := math.MaxInt32
	nearestExp := ""
	for exp, dte := range expDTE {
		if dte >= 0 && dte <= 7 {
			weeklySet[exp] = true
		}
		if dte >= 0 && dte < minDTE {
			minDTE = dte
			nearestExp = exp
		}
	}
	if len(weeklySet) > 0 {
		return weeklySet, fmt.Sprintf("0–7 DTE · %d 个到期日", len(weeklySet))
	}
	if nearestExp != "" {
		weeklySet[nearestExp] = true
		return weeklySet, fmt.Sprintf("近端 %d DTE · 1 个到期日", minDTE)
	}
	return weeklySet, "0–7 DTE"
}

func storedRowsFromSnapshot(ticker, source string, at time.Time, snap provider.Snapshot) []store.StoredOptionRow {
	dedup := store.DedupOptions(ticker, snap.Rows)
	when := marketclock.Stamp(at)
	out := make([]store.StoredOptionRow, 0, len(dedup))
	for _, r := range dedup {
		out = append(out, store.StoredOptionRow{
			Ticker:          ticker,
			CollectedAt:     when,
			UnderlyingPrice: snap.Spot,
			Expiry:          r.Expiry,
			Strike:          r.Strike,
			OptionType:      r.Type,
			LastPrice:       r.Last,
			Bid:             r.Bid,
			Ask:             r.Ask,
			Volume:          r.Volume,
			OI:              r.OI,
			IV:              r.IV,
			ProviderIV:      r.IV,
			InTheMoney:      r.ITM,
			Delta:           r.Delta,
			Gamma:           r.Gamma,
			Theta:           r.Theta,
			Vega:            r.Vega,
			Source:          source,
			ContractSymbol:  r.Symbol,
			QuoteTimestamp:  r.QuoteTime,
		})
	}
	return out
}

// recordIntradayBar computes contract volume/premium deltas and records a compact bar into intraday_tide_bars.
func (s *DesktopService) recordIntradayBar(ctx context.Context, ticker, source string, at time.Time, snap provider.Snapshot, keepLive bool) error {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	var storedRows []store.StoredOptionRow
	if keepLive {
		storedRows = storedRowsFromSnapshot(tkr, source, at, snap)
		if err := solveSnapshotIV(ctx, storedRows); err != nil {
			return err
		}
	} else {
		snapshot, err := s.snapshot(ctx, store.SnapshotMeta{Ticker: tkr, Source: source, At: marketclock.Stamp(at)})
		if err != nil {
			return err
		}
		storedRows = snapshot.rows
	}
	if len(storedRows) == 0 || snap.Spot <= 0 {
		return nil
	}

	tradingDay := at.In(marketclock.Zone).Format("2006-01-02")
	bucketTime := at.In(marketclock.Zone).Format("15:04")
	s.intradayMu.Lock()
	base := s.intradayBase[tkr]
	s.intradayMu.Unlock()
	if base == nil || base.tradingDay != tradingDay || base.source != source {
		base = s.seedBaselineFromDB(ctx, tkr, source, tradingDay, marketclock.Stamp(at))
	}
	if base == nil {
		base = &intradayBaseline{tradingDay: tradingDay, source: source, volumes: make(map[store.ContractID]float64, len(storedRows))}
	}
	hasPrior := len(base.volumes) > 0
	updates := make(map[store.ContractID]float64)

	whenStr := marketclock.Stamp(at)
	weeklySet, _ := weeklyExpirySetFromRows(storedRows, whenStr)

	var (
		totCallVol, totPutVol, totCallPrem, totPutPrem         float64
		totWCallPrem, totWPutPrem, weeklyNetGEX                float64
		deltaCallVol, deltaPutVol, deltaCallPrem, deltaPutPrem float64
		deltaWCallPrem, deltaWPutPrem                          float64
	)

	for i := range storedRows {
		r := &storedRows[i]
		vol := valOrZero(r.Volume)
		mid := optionMidPrice(r.LastPrice, r.Bid, r.Ask)
		prem := vol * mid * 100.0
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		isWeekly := weeklySet[exp]
		if isWeekly {
			if gex, ok := rowExposure(r, "gex", defaultRiskFreeRate, 0.0); ok && !math.IsNaN(gex) {
				weeklyNetGEX += gex
			}
		}
		if r.OptionType == "call" {
			totCallVol += vol
			totCallPrem += prem
			if isWeekly {
				totWCallPrem += prem
			}
		} else if r.OptionType == "put" {
			totPutVol += vol
			totPutPrem += prem
			if isWeekly {
				totWPutPrem += prem
			}
		}

		idKey := store.ContractID{Expiry: exp, Strike: r.Strike, Side: r.OptionType}
		if hasPrior {
			prev := base.volumes[idKey]
			dv := math.Max(0, vol-prev)
			dp := dv * mid * 100.0
			if r.OptionType == "call" {
				deltaCallVol += dv
				deltaCallPrem += dp
				if isWeekly {
					deltaWCallPrem += dp
				}
			} else if r.OptionType == "put" {
				deltaPutVol += dv
				deltaPutPrem += dp
				if isWeekly {
					deltaWPutPrem += dp
				}
			}
			if vol > prev {
				updates[idKey] = vol
			}
		} else {
			updates[idKey] = vol
		}
	}

	useDelta := hasPrior && (deltaCallVol+deltaPutVol > 0)
	isBaseline := 1
	cVol, pVol := totCallVol, totPutVol
	cPrem, pPrem := totCallPrem, totPutPrem
	wcPrem, wpPrem := totWCallPrem, totWPutPrem
	if useDelta {
		isBaseline = 0
		cVol, pVol = deltaCallVol, deltaPutVol
		cPrem, pPrem = deltaCallPrem, deltaPutPrem
		wcPrem, wpPrem = deltaWCallPrem, deltaWPutPrem
	}
	netPrem := cPrem - pPrem
	wNetPrem := wcPrem - wpPrem

	err := s.store.Transaction(ctx, func(tx *sql.Tx) error {
		if isBaseline == 0 {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO intraday_tide_bars (
					ticker, source, trading_day, bucket_time, spot,
					call_vol, put_vol, call_prem, put_prem, net_prem,
					weekly_call_prem, weekly_put_prem, weekly_net_prem, weekly_net_gex, is_baseline
				) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0)
				ON CONFLICT(ticker, source, trading_day, bucket_time) DO UPDATE SET
					spot = excluded.spot,
					call_vol = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.call_vol ELSE intraday_tide_bars.call_vol + excluded.call_vol END,
					put_vol = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.put_vol ELSE intraday_tide_bars.put_vol + excluded.put_vol END,
					call_prem = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.call_prem ELSE intraday_tide_bars.call_prem + excluded.call_prem END,
					put_prem = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.put_prem ELSE intraday_tide_bars.put_prem + excluded.put_prem END,
					net_prem = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.net_prem ELSE intraday_tide_bars.net_prem + excluded.net_prem END,
					weekly_call_prem = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.weekly_call_prem ELSE intraday_tide_bars.weekly_call_prem + excluded.weekly_call_prem END,
					weekly_put_prem = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.weekly_put_prem ELSE intraday_tide_bars.weekly_put_prem + excluded.weekly_put_prem END,
					weekly_net_prem = CASE WHEN intraday_tide_bars.is_baseline = 1 THEN excluded.weekly_net_prem ELSE intraday_tide_bars.weekly_net_prem + excluded.weekly_net_prem END,
					weekly_net_gex = excluded.weekly_net_gex,
					is_baseline = 0
			`, tkr, source, tradingDay, bucketTime, snap.Spot,
				cVol, pVol, cPrem, pPrem, netPrem,
				wcPrem, wpPrem, wNetPrem, weeklyNetGEX)
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO intraday_tide_bars (
				ticker, source, trading_day, bucket_time, spot,
				call_vol, put_vol, call_prem, put_prem, net_prem,
				weekly_call_prem, weekly_put_prem, weekly_net_prem, weekly_net_gex, is_baseline
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(ticker, source, trading_day, bucket_time) DO UPDATE SET
				spot = excluded.spot,
				weekly_net_gex = excluded.weekly_net_gex
		`, tkr, source, tradingDay, bucketTime, snap.Spot,
			cVol, pVol, cPrem, pPrem, netPrem,
			wcPrem, wpPrem, wNetPrem, weeklyNetGEX)
		return err
	})
	if err != nil {
		return err
	}
	for key, volume := range updates {
		base.volumes[key] = volume
	}
	s.intradayMu.Lock()
	if s.intradayBase == nil {
		s.intradayBase = make(map[string]*intradayBaseline)
	}
	s.intradayBase[tkr] = base
	s.intradayMu.Unlock()
	if keepLive {
		s.publishLive(tkr, storedRows)
	} else {
		s.intradayMu.Lock()
		s.dropLive(tkr)
		s.intradayMu.Unlock()
	}
	s.intradayMu.Lock()
	s.barVersion[tkr]++
	s.intradayMu.Unlock()
	return nil
}

func (s *DesktopService) seedBaselineFromDB(ctx context.Context, ticker, source, tradingDay, beforeStamp string) *intradayBaseline {
	var moment string
	err := s.store.Read().QueryRowContext(ctx, `SELECT COALESCE(MAX(at),'') FROM (
		SELECT CAST(MAX(collected_at) AS TEXT) AS at FROM option_snapshots WHERE ticker=? AND source=? AND collected_at<?
		UNION ALL
		SELECT CAST(MAX(collected_at) AS TEXT) FROM option_snapshots_archive WHERE ticker=? AND source=? AND collected_at<?
	)`, ticker, source, beforeStamp, ticker, source, beforeStamp).Scan(&moment)
	if err != nil || moment == "" || marketclock.MarketDay(moment) != tradingDay {
		return nil
	}
	rows, err := s.store.SnapshotsAt(ctx, ticker, source, []string{moment})
	if err != nil || len(rows) == 0 {
		return nil
	}
	vols := make(map[store.ContractID]float64, len(rows))
	for i := range rows {
		r := &rows[i]
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		idKey := store.ContractID{Expiry: exp, Strike: r.Strike, Side: r.OptionType}
		vols[idKey] = valOrZero(r.Volume)
	}
	return &intradayBaseline{
		tradingDay: tradingDay,
		source:     source,
		volumes:    vols,
	}
}

// collectHighFreqTick fetches the current chain for a single ticker and records a lightweight
// intraday bar in intraday_tide_bars without bloating option_snapshots with duplicate full chains.
func (s *DesktopService) collectHighFreqTick(ctx context.Context, ticker string) error {
	defer s.wakeScheduler()
	if !s.collectMu.TryLock() {
		return fmt.Errorf("已有采集任务在进行，请等待完成")
	}
	defer s.collectMu.Unlock()
	return s.collectHighFreqLocked(ctx, ticker)
}

func (s *DesktopService) collectHighFreqLocked(ctx context.Context, ticker string) error {

	tkr, err := provider.NormalizeSymbol(ticker)
	if err != nil {
		return err
	}
	if tkr == "VIX" || tkr == "VIXW" {
		return fmt.Errorf("暂不支持 VIX 期权")
	}

	candidates := []string{"cboe", "yahoo"}
	switch os.Getenv("GEX_PROVIDER") {
	case "cboe":
		candidates = []string{"cboe"}
	case "yahoo":
		candidates = []string{"yahoo"}
	}

	for _, src := range candidates {
		raw, err := s.store.Setting(ctx, "provider_cooldown_until:"+src)
		if err != nil {
			continue
		}
		if until, err := marketclock.ParseStamp(raw); err == nil && until.After(s.now()) {
			continue
		}
		at := s.now()
		var snap provider.Snapshot
		if src == "cboe" {
			snap, err = s.fetch(ctx, tkr)
		} else {
			snap, err = s.fetchYahoo(ctx, tkr)
		}
		if err != nil || len(snap.Rows) == 0 || snap.Spot <= 0 {
			var pe *provider.ProviderError
			if errors.As(err, &pe) && pe.Status == 429 {
				_ = s.store.SetSetting(ctx, "provider_cooldown_until:"+src, marketclock.Stamp(s.now().Add(max(pe.RetryAfter, 30*time.Minute))))
			}
			continue
		}
		// If the database has no snapshot at all for this ticker yet, store one baseline snapshot.
		existingMoment, err := s.store.LatestMoment(ctx, tkr, "")
		if err != nil {
			return err
		}
		if existingMoment == "" {
			zero := 0.0
			for _, row := range snap.Rows {
				if row.OI == nil || *row.OI == 0 {
					zero++
				}
			}
			zero /= float64(len(snap.Rows))
			if err := s.store.SaveSnapshot(ctx, tkr, src, at, snap, zero); err != nil {
				return err
			}
		}
		if err := s.recordIntradayBar(ctx, tkr, src, at, snap, existingMoment != ""); err != nil {
			return err
		}
		s.markSampled(tkr, s.now())
		return nil
	}
	return fmt.Errorf("高频抓取未获取到 %s 的有效数据", tkr)
}

func (s *DesktopService) buildIntradaySummary(ctx context.Context, ticker, source, chosenMoment string, snapshotRows []store.StoredOptionRow) viewmodel.IntradaySummary {
	if len(snapshotRows) == 0 {
		return viewmodel.IntradaySummary{
			TimeBars:   []viewmodel.IntradayBar{},
			StrikeBars: []viewmodel.IntradayBar{},
		}
	}
	spot := snapshotRows[0].UnderlyingPrice
	weeklySet, weeklyScope := weeklyExpirySetFromRows(snapshotRows, chosenMoment)

	var (
		callVol, putVol, callPrem, putPrem float64
		wCallPrem, wPutPrem, wNetGEX       float64
	)
	for i := range snapshotRows {
		r := &snapshotRows[i]
		v := valOrZero(r.Volume)
		mid := optionMidPrice(r.LastPrice, r.Bid, r.Ask)
		prem := v * mid * 100.0
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		isW := weeklySet[exp]
		if isW {
			if gex, ok := rowExposure(r, "gex", defaultRiskFreeRate, 0.0); ok && !math.IsNaN(gex) {
				wNetGEX += gex
			}
		}
		if r.OptionType == "call" {
			callVol += v
			callPrem += prem
			if isW {
				wCallPrem += prem
			}
		} else if r.OptionType == "put" {
			putVol += v
			putPrem += prem
			if isW {
				wPutPrem += prem
			}
		}
	}
	totVol := callVol + putVol
	totPrem := callPrem + putPrem
	netPrem := callPrem - putPrem
	wNetPrem := wCallPrem - wPutPrem

	dateLabel := marketclock.MarketDay(chosenMoment)
	if t, ok := marketclock.ParseTime(chosenMoment); ok {
		dateLabel = t.In(marketclock.Zone).Format("01/02/2006")
	}

	timeBars := s.loadOrSynthesizeTimeBars(ctx, ticker, source, chosenMoment, snapshotRows, weeklySet)
	strikeBars := buildStrikeFlowBars(snapshotRows, spot, weeklySet)

	return viewmodel.IntradaySummary{
		DateLabel:         dateLabel,
		TotalVol:          totVol,
		TotalVolStr:       fmtVolCompact(totVol),
		TotalPrem:         totPrem,
		TotalPremStr:      fmtDollarComma(totPrem, false),
		NetPrem:           netPrem,
		NetPremStr:        fmtDollarComma(netPrem, true),
		PutVol:            putVol,
		PutVolStr:         fmtVolCompact(putVol),
		PutPrem:           putPrem,
		PutPremStr:        fmtDollarCompact(putPrem, false),
		CallVol:           callVol,
		CallVolStr:        fmtVolCompact(callVol),
		CallPrem:          callPrem,
		CallPremStr:       fmtDollarCompact(callPrem, false),
		WeeklyScope:       weeklyScope,
		WeeklyCallPremStr: fmtDollarCompact(wCallPrem, false),
		WeeklyPutPremStr:  fmtDollarCompact(wPutPrem, false),
		WeeklyNetPrem:     wNetPrem,
		WeeklyNetPremStr:  fmtDollarCompact(wNetPrem, true),
		WeeklyNetGEX:      wNetGEX,
		WeeklyNetGEXStr:   fmtDollarCompact(wNetGEX, true),
		TimeBars:          timeBars,
		StrikeBars:        strikeBars,
	}
}

func (s *DesktopService) loadOrSynthesizeTimeBars(ctx context.Context, ticker, source, chosenMoment string, latestRows []store.StoredOptionRow, weeklySet map[string]bool) []viewmodel.IntradayBar {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	tradingDay := marketclock.MarketDay(chosenMoment)
	rows, err := s.store.Read().QueryContext(ctx, `
		SELECT bucket_time, spot, call_vol, put_vol, call_prem, put_prem, net_prem,
		       weekly_call_prem, weekly_put_prem, weekly_net_prem, weekly_net_gex, is_baseline
		FROM intraday_tide_bars
		WHERE ticker = ? AND (? = '' OR source = ?) AND trading_day = ?
		ORDER BY bucket_time ASC
	`, tkr, source, source, tradingDay)

	var dbBars []viewmodel.IntradayBar
	hasDeltaBar := false
	if err == nil {
		for rows.Next() {
			var b viewmodel.IntradayBar
			var isBase int
			if scanErr := rows.Scan(
				&b.Time, &b.Spot, &b.CallVol, &b.PutVol, &b.CallPrem, &b.PutPrem, &b.NetPrem,
				&b.WeeklyCallPrem, &b.WeeklyPutPrem, &b.WeeklyNetPrem, &b.WeeklyNetGEX, &isBase,
			); scanErr == nil {
				b.IsBaseline = isBase != 0
				if !b.IsBaseline {
					hasDeltaBar = true
				}
				b.TotalVol = b.CallVol + b.PutVol
				if b.CallVol > 0 {
					b.PCRatio = b.PutVol / b.CallVol
				}
				b.TotalPrem = b.CallPrem + b.PutPrem
				b.WindowLabel = fmt.Sprintf("%s %s ET", tradingDay, b.Time)
				dbBars = append(dbBars, b)
			}
		}
		rows.Close()
	}

	// If we have real delta bars from high-frequency polling on this trading day, filter out the initial
	// cumulative baseline bar so the candle volume Y-axis stays on a uniform per-interval scale.
	if hasDeltaBar {
		filtered := make([]viewmodel.IntradayBar, 0, len(dbBars))
		for _, b := range dbBars {
			if !b.IsBaseline {
				filtered = append(filtered, b)
			}
		}
		if len(filtered) >= 2 {
			populateCumulativeWeekly(filtered)
			return filtered
		}
	}
	if len(dbBars) >= 2 {
		populateCumulativeWeekly(dbBars)
		return dbBars
	}

	// Fallback: derive time bars from historical snapshots in option_snapshots (up to last 16 snapshots).
	moments, err := s.store.CollectionMoments(ctx, tkr, source, 14, false)
	if err == nil && len(moments) > 0 {
		if len(moments) > 16 {
			moments = moments[:16]
		}
		// Reverse to chronological order (oldest -> newest).
		chron := make([]string, len(moments))
		for i := range moments {
			chron[i] = moments[len(moments)-1-i]
		}
		var synth []viewmodel.IntradayBar
		var prevDay string
		var prevVols map[store.ContractID]float64
		for _, m := range chron {
			if ctx.Err() != nil {
				return nil
			}
			mRows, err := s.store.SnapshotsAt(ctx, tkr, source, []string{m})
			if err != nil || len(mRows) == 0 {
				continue
			}
			if solveSnapshotIV(ctx, mRows) != nil {
				return nil
			}
			day := marketclock.MarketDay(m)
			wSet, _ := weeklyExpirySetFromRows(mRows, m)
			curVols := make(map[store.ContractID]float64, len(mRows))
			var (
				cVol, pVol, cPrem, pPrem, wcPrem, wpPrem, wGEX float64
				dCVol, dPVol, dCPrem, dPPrem, dWCPrem, dWPPrem float64
			)
			sameDayPrior := prevDay == day && len(prevVols) > 0
			for i := range mRows {
				r := &mRows[i]
				v := valOrZero(r.Volume)
				mid := optionMidPrice(r.LastPrice, r.Bid, r.Ask)
				prem := v * mid * 100.0
				exp := r.Expiry
				if len(exp) >= 10 {
					exp = exp[:10]
				}
				isW := wSet[exp]
				if isW {
					if gex, ok := rowExposure(r, "gex", defaultRiskFreeRate, 0.0); ok && !math.IsNaN(gex) {
						wGEX += gex
					}
				}
				if r.OptionType == "call" {
					cVol += v
					cPrem += prem
					if isW {
						wcPrem += prem
					}
				} else if r.OptionType == "put" {
					pVol += v
					pPrem += prem
					if isW {
						wpPrem += prem
					}
				}
				idKey := store.ContractID{Expiry: exp, Strike: r.Strike, Side: r.OptionType}
				curVols[idKey] = v
				if sameDayPrior {
					dv := math.Max(0, v-prevVols[idKey])
					dp := dv * mid * 100.0
					if r.OptionType == "call" {
						dCVol += dv
						dCPrem += dp
						if isW {
							dWCPrem += dp
						}
					} else if r.OptionType == "put" {
						dPVol += dv
						dPPrem += dp
						if isW {
							dWPPrem += dp
						}
					}
				}
			}
			useDiff := sameDayPrior && (dCVol+dPVol > 0)
			if useDiff {
				cVol, pVol = dCVol, dPVol
				cPrem, pPrem = dCPrem, dPPrem
				wcPrem, wpPrem = dWCPrem, dWPPrem
			}
			prevDay = day
			prevVols = curVols

			lbl := fmtTimeMMDDHHMM(m)
			winLbl := m
			if t, ok := marketclock.ParseTime(m); ok {
				et := t.In(marketclock.Zone)
				if len(chron) <= 6 {
					lbl = et.Format("01-02 15:04")
				} else {
					lbl = et.Format("15:04")
				}
				winLbl = et.Format("01/02/2006 15:04 MST")
			}
			pc := 0.0
			if cVol > 0 {
				pc = pVol / cVol
			}
			synth = append(synth, viewmodel.IntradayBar{
				Time:           lbl,
				WindowLabel:    winLbl,
				Spot:           mRows[0].UnderlyingPrice,
				CallVol:        cVol,
				PutVol:         pVol,
				TotalVol:       cVol + pVol,
				PCRatio:        pc,
				CallPrem:       cPrem,
				PutPrem:        pPrem,
				TotalPrem:      cPrem + pPrem,
				NetPrem:        cPrem - pPrem,
				WeeklyCallPrem: wcPrem,
				WeeklyPutPrem:  wpPrem,
				WeeklyNetPrem:  wcPrem - wpPrem,
				WeeklyNetGEX:   wGEX,
				IsBaseline:     !useDiff,
			})
		}
		if len(synth) > 0 {
			populateCumulativeWeekly(synth)
			return synth
		}
	}

	if len(dbBars) > 0 {
		populateCumulativeWeekly(dbBars)
		return dbBars
	}
	return []viewmodel.IntradayBar{}
}

func populateCumulativeWeekly(bars []viewmodel.IntradayBar) {
	var cumC, cumP, cumNet float64
	for i := range bars {
		cumC += bars[i].WeeklyCallPrem
		cumP += bars[i].WeeklyPutPrem
		cumNet += bars[i].WeeklyNetPrem
		bars[i].CumWeeklyCall = cumC
		bars[i].CumWeeklyPut = cumP
		bars[i].CumWeeklyNet = cumNet
	}
}

func buildStrikeFlowBars(rows []store.StoredOptionRow, spot float64, weeklySet map[string]bool) []viewmodel.IntradayBar {
	type strikeAcc struct {
		strike                   float64
		cVol, pVol, cPrem, pPrem float64
		wCPrem, wPPrem, wGEX     float64
	}
	byStrike := make(map[float64]*strikeAcc)
	for i := range rows {
		r := &rows[i]
		v := valOrZero(r.Volume)
		mid := optionMidPrice(r.LastPrice, r.Bid, r.Ask)
		prem := v * mid * 100.0
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		isW := weeklySet[exp]
		acc := byStrike[r.Strike]
		if acc == nil {
			acc = &strikeAcc{strike: r.Strike}
			byStrike[r.Strike] = acc
		}
		if isW {
			if gex, ok := rowExposure(r, "gex", defaultRiskFreeRate, 0.0); ok && !math.IsNaN(gex) {
				acc.wGEX += gex
			}
		}
		if r.OptionType == "call" {
			acc.cVol += v
			acc.cPrem += prem
			if isW {
				acc.wCPrem += prem
			}
		} else if r.OptionType == "put" {
			acc.pVol += v
			acc.pPrem += prem
			if isW {
				acc.wPPrem += prem
			}
		}
	}
	strikes := make([]float64, 0, len(byStrike))
	for k, acc := range byStrike {
		if acc.cVol+acc.pVol > 0 || math.Abs(acc.wGEX) > 0 {
			strikes = append(strikes, k)
		}
	}
	sort.Float64s(strikes)
	if len(strikes) == 0 {
		return []viewmodel.IntradayBar{}
	}

	// Band around ATM up to 32 strikes so the bar chart has high density and clear readability.
	atmIdx := 0
	bestDist := math.Abs(strikes[0] - spot)
	for i, k := range strikes[1:] {
		if d := math.Abs(k - spot); d < bestDist {
			bestDist = d
			atmIdx = i + 1
		}
	}
	half := 16
	lo := max(0, atmIdx-half)
	hi := min(len(strikes), atmIdx+half+1)
	banded := strikes[lo:hi]

	out := make([]viewmodel.IntradayBar, 0, len(banded))
	for _, k := range banded {
		acc := byStrike[k]
		pc := 0.0
		if acc.cVol > 0 {
			pc = acc.pVol / acc.cVol
		}
		sLbl := viewmodel.FmtStrikeVal(k)
		out = append(out, viewmodel.IntradayBar{
			Time:           sLbl,
			WindowLabel:    fmt.Sprintf("行权价 K = $%s (现价 $%.2f)", sLbl, spot),
			Spot:           spot,
			CallVol:        acc.cVol,
			PutVol:         acc.pVol,
			TotalVol:       acc.cVol + acc.pVol,
			PCRatio:        pc,
			CallPrem:       acc.cPrem,
			PutPrem:        acc.pPrem,
			TotalPrem:      acc.cPrem + acc.pPrem,
			NetPrem:        acc.cPrem - acc.pPrem,
			WeeklyCallPrem: acc.wCPrem,
			WeeklyPutPrem:  acc.wPPrem,
			WeeklyNetPrem:  acc.wCPrem - acc.wPPrem,
			WeeklyNetGEX:   acc.wGEX,
		})
	}
	populateCumulativeWeekly(out)
	return out
}

func (s *DesktopService) markSampled(ticker string, now time.Time) {
	s.intradayMu.Lock()
	defer s.intradayMu.Unlock()
	s.lastHighFreqAt[ticker] = now
	delete(s.highFreqRetry, ticker)
}

func (s *DesktopService) publishLive(ticker string, rows []store.StoredOptionRow) {
	size := liveSnapshotBytes(rows)
	s.intradayMu.Lock()
	defer s.intradayMu.Unlock()
	s.dropLive(ticker)
	// Keep live market data within a separate budget from cached page results.
	for size <= cacheBudget && s.liveBytes+size > cacheBudget {
		var oldest string
		for symbol, candidate := range s.liveSnapshots {
			if oldest == "" || candidate[0].CollectedAt < s.liveSnapshots[oldest][0].CollectedAt {
				oldest = symbol
			}
		}
		s.dropLive(oldest)
	}
	if size <= cacheBudget {
		s.liveSnapshots[ticker] = rows
		s.liveSizes[ticker] = size
		s.liveBytes += size
	}
}

func liveSnapshotBytes(rows []store.StoredOptionRow) int {
	return int(reflect.TypeOf(rows).Size()) + retainedBytes(reflect.ValueOf(rows))
}

// dropLive is called with intradayMu held.
func (s *DesktopService) dropLive(ticker string) {
	s.liveBytes -= s.liveSizes[ticker]
	delete(s.liveSizes, ticker)
	delete(s.liveSnapshots, ticker)
}

func (s *DesktopService) cleanupIntraday(day string, tickers map[string]store.TickerCollectConfig) {
	s.intradayMu.Lock()
	defer s.intradayMu.Unlock()
	for ticker, base := range s.intradayBase {
		if _, watched := tickers[ticker]; !watched || base == nil || base.tradingDay != day {
			delete(s.intradayBase, ticker)
		}
	}
	for ticker := range s.liveSnapshots {
		if _, watched := tickers[ticker]; !watched {
			s.dropLive(ticker)
		}
	}
	for ticker := range s.lastHighFreqAt {
		if _, watched := tickers[ticker]; !watched {
			delete(s.lastHighFreqAt, ticker)
		}
	}
	for ticker := range s.highFreqRetry {
		if _, watched := tickers[ticker]; !watched {
			delete(s.highFreqRetry, ticker)
		}
	}
}
