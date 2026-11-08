package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type TickerCollectConfig struct {
	Ticker          string `json:"ticker"`
	LowFreqEnabled  bool   `json:"low_freq_enabled"`
	HighFreqEnabled bool   `json:"high_freq_enabled"`
	HighFreqSec     int    `json:"high_freq_sec"`
}

type IntradayBar struct {
	Time           string  `json:"time"`
	WindowLabel    string  `json:"window_label"`
	Spot           float64 `json:"spot"`
	CallVol        float64 `json:"call_vol"`
	PutVol         float64 `json:"put_vol"`
	TotalVol       float64 `json:"total_vol"`
	PCRatio        float64 `json:"pc_ratio"`
	CallPrem       float64 `json:"call_prem"`
	PutPrem        float64 `json:"put_prem"`
	TotalPrem      float64 `json:"total_prem"`
	NetPrem        float64 `json:"net_prem"`
	WeeklyCallPrem float64 `json:"weekly_call_prem"`
	WeeklyPutPrem  float64 `json:"weekly_put_prem"`
	WeeklyNetPrem  float64 `json:"weekly_net_prem"`
	CumWeeklyCall  float64 `json:"cum_weekly_call"`
	CumWeeklyPut   float64 `json:"cum_weekly_put"`
	CumWeeklyNet   float64 `json:"cum_weekly_net"`
	WeeklyNetGEX   float64 `json:"weekly_net_gex"`
	IsBaseline     bool    `json:"is_baseline"`
}

type IntradaySummary struct {
	DateLabel         string        `json:"date_label"`
	TotalVol          float64       `json:"total_vol"`
	TotalVolStr       string        `json:"total_vol_str"`
	TotalPrem         float64       `json:"total_prem"`
	TotalPremStr      string        `json:"total_prem_str"`
	NetPrem           float64       `json:"net_prem"`
	NetPremStr        string        `json:"net_prem_str"`
	PutVol            float64       `json:"put_vol"`
	PutVolStr         string        `json:"put_vol_str"`
	PutPrem           float64       `json:"put_prem"`
	PutPremStr        string        `json:"put_prem_str"`
	CallVol           float64       `json:"call_vol"`
	CallVolStr        string        `json:"call_vol_str"`
	CallPrem          float64       `json:"call_prem"`
	CallPremStr       string        `json:"call_prem_str"`
	WeeklyScope       string        `json:"weekly_scope"`
	WeeklyCallPremStr string        `json:"weekly_call_prem_str"`
	WeeklyPutPremStr  string        `json:"weekly_put_prem_str"`
	WeeklyNetPrem     float64       `json:"weekly_net_prem"`
	WeeklyNetPremStr  string        `json:"weekly_net_prem_str"`
	WeeklyNetGEX      float64       `json:"weekly_net_gex"`
	WeeklyNetGEXStr   string        `json:"weekly_net_gex_str"`
	TimeBars          []IntradayBar `json:"time_bars"`
	StrikeBars        []IntradayBar `json:"strike_bars"`
}

type intradayBaseline struct {
	tradingDay string
	volumes    map[string]float64
}

func normalizeHighFreqSec(sec int) int {
	switch sec {
	case 60, 120, 180, 300, 600:
		return sec
	default:
		if sec <= 0 {
			return 60
		}
		if sec < 90 {
			return 60
		}
		if sec < 150 {
			return 120
		}
		if sec < 240 {
			return 180
		}
		if sec < 450 {
			return 300
		}
		return 600
	}
}

func (s *Store) tickerCollectConfig(ctx context.Context, ticker string) (TickerCollectConfig, error) {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	cfg := TickerCollectConfig{
		Ticker:          tkr,
		LowFreqEnabled:  true,
		HighFreqEnabled: false,
		HighFreqSec:     60,
	}
	lowDis, err := s.setting(ctx, "low_freq_disabled:"+tkr)
	if err != nil {
		return cfg, err
	}
	if lowDis == "1" {
		cfg.LowFreqEnabled = false
	}
	hfEn, err := s.setting(ctx, "high_freq_enabled:"+tkr)
	if err != nil {
		return cfg, err
	}
	if hfEn == "1" {
		cfg.HighFreqEnabled = true
	}
	hfSecRaw, err := s.setting(ctx, "high_freq_sec:"+tkr)
	if err != nil {
		return cfg, err
	}
	if sec, err := strconv.Atoi(hfSecRaw); err == nil && sec > 0 {
		cfg.HighFreqSec = normalizeHighFreqSec(sec)
	}
	return cfg, nil
}

func (s *Store) saveTickerCollectConfig(ctx context.Context, cfg TickerCollectConfig) error {
	tkr, err := normalizeSymbol(cfg.Ticker)
	if err != nil {
		return err
	}
	sec := normalizeHighFreqSec(cfg.HighFreqSec)
	lowDisVal := "0"
	if !cfg.LowFreqEnabled {
		lowDisVal = "1"
	}
	hfEnVal := "0"
	if cfg.HighFreqEnabled {
		hfEnVal = "1"
	}
	return s.transaction(ctx, func(tx *sql.Tx) error {
		if err := setSetting(tx, "low_freq_disabled:"+tkr, lowDisVal); err != nil {
			return err
		}
		if err := setSetting(tx, "high_freq_enabled:"+tkr, hfEnVal); err != nil {
			return err
		}
		return setSetting(tx, "high_freq_sec:"+tkr, strconv.Itoa(sec))
	})
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

func weeklyExpirySetFromRows(rows []StoredOptionRow, refMoment string) (map[string]bool, string) {
	expDTE := make(map[string]int)
	for i := range rows {
		exp := rows[i].Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		if _, seen := expDTE[exp]; !seen {
			if yearsToExpiry(exp, refMoment) > 0 {
				expDTE[exp] = calcDTE(exp, refMoment)
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

func storedRowsFromSnapshot(ticker, source string, at time.Time, snap Snapshot) []StoredOptionRow {
	dedup := dedupOptions(ticker, snap.Rows)
	when := stamp(at)
	out := make([]StoredOptionRow, 0, len(dedup))
	for _, r := range dedup {
		out = append(out, StoredOptionRow{
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
func (s *DesktopService) recordIntradayBar(ctx context.Context, ticker, source string, at time.Time, snap Snapshot) error {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	storedRows := storedRowsFromSnapshot(tkr, source, at, snap)
	if len(storedRows) == 0 || snap.Spot <= 0 {
		return nil
	}
	withSolvedIV(storedRows, defaultRiskFreeRate, 0.0)

	// Keep the latest snapshot in memory so live high-frequency updates are immediately reflected in views.
	s.intradayMu.Lock()
	if s.liveSnapshots == nil {
		s.liveSnapshots = make(map[string][]StoredOptionRow)
	}
	s.liveSnapshots[tkr] = storedRows
	if s.intradayBase == nil {
		s.intradayBase = make(map[string]*intradayBaseline)
	}
	tradingDay := at.In(marketZone).Format("2006-01-02")
	bucketTime := at.In(marketZone).Format("15:04")
	base := s.intradayBase[tkr]
	s.intradayMu.Unlock()

	// If in-memory baseline is missing for today, try seeding from the latest snapshot of today in SQLite.
	if base == nil || base.tradingDay != tradingDay {
		seeded := s.seedBaselineFromDB(ctx, tkr, source, tradingDay, stamp(at))
		s.intradayMu.Lock()
		if s.intradayBase[tkr] == nil || s.intradayBase[tkr].tradingDay != tradingDay {
			s.intradayBase[tkr] = seeded
		}
		base = s.intradayBase[tkr]
		s.intradayMu.Unlock()
	}

	whenStr := stamp(at)
	weeklySet, _ := weeklyExpirySetFromRows(storedRows, whenStr)

	var (
		totCallVol, totPutVol, totCallPrem, totPutPrem             float64
		totWCallPrem, totWPutPrem, weeklyNetGEX                    float64
		deltaCallVol, deltaPutVol, deltaCallPrem, deltaPutPrem     float64
		deltaWCallPrem, deltaWPutPrem                              float64
	)

	s.intradayMu.Lock()
	hasPrior := base != nil && base.tradingDay == tradingDay && len(base.volumes) > 0
	if base == nil || base.tradingDay != tradingDay {
		base = &intradayBaseline{
			tradingDay: tradingDay,
			volumes:    make(map[string]float64, len(storedRows)),
		}
		s.intradayBase[tkr] = base
	}

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

		idKey := fmt.Sprintf("%s/%g/%s", exp, r.Strike, r.OptionType)
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
				base.volumes[idKey] = vol
			}
		} else {
			base.volumes[idKey] = vol
		}
	}
	s.intradayMu.Unlock()

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

	return s.store.transaction(ctx, func(tx *sql.Tx) error {
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
}

func (s *DesktopService) seedBaselineFromDB(ctx context.Context, ticker, source, tradingDay, beforeStamp string) *intradayBaseline {
	moment, rows, err := s.store.latestSnapshot(ctx, ticker, source)
	if err != nil || len(rows) == 0 || moment == "" {
		return nil
	}
	if marketDayString(moment) != tradingDay {
		return nil
	}
	vols := make(map[string]float64, len(rows))
	for i := range rows {
		r := &rows[i]
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		idKey := fmt.Sprintf("%s/%g/%s", exp, r.Strike, r.OptionType)
		vols[idKey] = valOrZero(r.Volume)
	}
	return &intradayBaseline{
		tradingDay: tradingDay,
		volumes:    vols,
	}
}

// collectHighFreqTick fetches the current chain for a single ticker and records a lightweight
// intraday bar in intraday_tide_bars without bloating option_snapshots with duplicate full chains.
func (s *DesktopService) collectHighFreqTick(ctx context.Context, ticker string) error {
	if !s.collectMu.TryLock() {
		return nil
	}
	defer s.collectMu.Unlock()

	tkr, err := normalizeSymbol(ticker)
	if err != nil || tkr == "VIX" || tkr == "VIXW" {
		return err
	}

	candidates := []string{"cboe", "yahoo"}
	switch os.Getenv("GEX_PROVIDER") {
	case "cboe":
		candidates = []string{"cboe"}
	case "yahoo":
		candidates = []string{"yahoo"}
	}

	for _, src := range candidates {
		raw, err := s.store.setting(ctx, "provider_cooldown_until:"+src)
		if err != nil {
			continue
		}
		if until, err := parseStamp(raw); err == nil && until.After(s.now()) {
			continue
		}
		at := s.now()
		var snap Snapshot
		if src == "cboe" {
			snap, err = s.fetch(ctx, tkr)
		} else {
			snap, err = s.fetchYahooOnDemand(ctx, tkr)
		}
		if err != nil || len(snap.Rows) == 0 || snap.Spot <= 0 {
			var pe *ProviderError
			if errors.As(err, &pe) && pe.Status == 429 {
				_ = s.store.setSetting(ctx, "provider_cooldown_until:"+src, stamp(s.now().Add(max(pe.RetryAfter, 30*time.Minute))))
			}
			continue
		}
		// If the database has no snapshot at all for this ticker yet, store one baseline snapshot.
		existingMoment, _ := s.store.latestMoment(ctx, tkr, "")
		if existingMoment == "" {
			zero := 0.0
			for _, row := range snap.Rows {
				if row.OI == nil || *row.OI == 0 {
					zero++
				}
			}
			zero /= float64(len(snap.Rows))
			_ = s.store.saveSnapshot(ctx, tkr, src, at, snap, zero)
		}
		if err := s.recordIntradayBar(ctx, tkr, src, at, snap); err != nil {
			return err
		}
		s.intradayMu.Lock()
		if s.lastHighFreqAt == nil {
			s.lastHighFreqAt = make(map[string]time.Time)
		}
		s.lastHighFreqAt[tkr] = at
		s.intradayMu.Unlock()
		return nil
	}
	return fmt.Errorf("高频抓取未获取到 %s 的有效数据", tkr)
}

func (s *DesktopService) buildIntradaySummary(ctx context.Context, ticker, source, chosenMoment string, snapshotRows []StoredOptionRow) IntradaySummary {
	if len(snapshotRows) == 0 {
		return IntradaySummary{
			TimeBars:   []IntradayBar{},
			StrikeBars: []IntradayBar{},
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

	dateLabel := marketDayString(chosenMoment)
	if t, ok := parseAnyTime(chosenMoment); ok {
		dateLabel = t.In(marketZone).Format("01/02/2006")
	}

	timeBars := s.loadOrSynthesizeTimeBars(ctx, ticker, source, chosenMoment, snapshotRows, weeklySet)
	strikeBars := buildStrikeFlowBars(snapshotRows, spot, weeklySet)

	return IntradaySummary{
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

func (s *DesktopService) loadOrSynthesizeTimeBars(ctx context.Context, ticker, source, chosenMoment string, latestRows []StoredOptionRow, weeklySet map[string]bool) []IntradayBar {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	tradingDay := marketDayString(chosenMoment)
	rows, err := s.store.read.QueryContext(ctx, `
		SELECT bucket_time, spot, call_vol, put_vol, call_prem, put_prem, net_prem,
		       weekly_call_prem, weekly_put_prem, weekly_net_prem, weekly_net_gex, is_baseline
		FROM intraday_tide_bars
		WHERE ticker = ? AND (? = '' OR source = ?) AND trading_day = ?
		ORDER BY bucket_time ASC
	`, tkr, source, source, tradingDay)

	var dbBars []IntradayBar
	hasDeltaBar := false
	if err == nil {
		for rows.Next() {
			var b IntradayBar
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
		filtered := make([]IntradayBar, 0, len(dbBars))
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
	moments, err := s.store.collectionMoments(ctx, tkr, source, 14, false)
	if err == nil && len(moments) > 0 {
		if len(moments) > 16 {
			moments = moments[:16]
		}
		// Reverse to chronological order (oldest -> newest).
		chron := make([]string, len(moments))
		for i := range moments {
			chron[i] = moments[len(moments)-1-i]
		}
		allSnaps, snapErr := s.store.snapshotsAt(ctx, tkr, source, chron)
		if snapErr == nil && len(allSnaps) > 0 {
			byMoment := make(map[string][]StoredOptionRow, len(chron))
			for i := range allSnaps {
				m := allSnaps[i].CollectedAt
				byMoment[m] = append(byMoment[m], allSnaps[i])
			}
			var synth []IntradayBar
			var prevDay string
			var prevVols map[string]float64
			for _, m := range chron {
				mRows := byMoment[m]
				if len(mRows) == 0 {
					continue
				}
				withSolvedIV(mRows, defaultRiskFreeRate, 0.0)
				day := marketDayString(m)
				wSet, _ := weeklyExpirySetFromRows(mRows, m)
				curVols := make(map[string]float64, len(mRows))
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
					idKey := fmt.Sprintf("%s/%g/%s", exp, r.Strike, r.OptionType)
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
				if t, ok := parseAnyTime(m); ok {
					et := t.In(marketZone)
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
				synth = append(synth, IntradayBar{
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
	}

	if len(dbBars) > 0 {
		populateCumulativeWeekly(dbBars)
		return dbBars
	}
	return []IntradayBar{}
}

func populateCumulativeWeekly(bars []IntradayBar) {
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

func buildStrikeFlowBars(rows []StoredOptionRow, spot float64, weeklySet map[string]bool) []IntradayBar {
	type strikeAcc struct {
		strike                         float64
		cVol, pVol, cPrem, pPrem       float64
		wCPrem, wPPrem, wGEX           float64
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
		return []IntradayBar{}
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

	out := make([]IntradayBar, 0, len(banded))
	for _, k := range banded {
		acc := byStrike[k]
		pc := 0.0
		if acc.cVol > 0 {
			pc = acc.pVol / acc.cVol
		}
		sLbl := fmtStrikeVal(k)
		out = append(out, IntradayBar{
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
