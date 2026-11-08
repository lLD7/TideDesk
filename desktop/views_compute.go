package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- Store read/write queries for option analytics ---

func (s *Store) activeSource(ctx context.Context, ticker string) (string, error) {
	m, err := s.meta(ctx, strings.ToUpper(ticker))
	return m.Source, err
}

func (s *Store) latestMoment(ctx context.Context, ticker, source string) (string, error) {
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

func (s *Store) collectionMoments(ctx context.Context, ticker, source string, days int, perDay bool) ([]string, error) {
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
		day := marketDayString(m)
		if !seen[day] && sessionStartedAt(m) {
			seen[day] = true
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

const snapshotSelectCols = `ticker, CAST(collected_at AS TEXT) AS collected_at, underlying_price, substr(CAST(expiry AS TEXT),1,10) AS expiry, strike, option_type,
	last_price, bid, ask, volume, open_interest, implied_volatility, COALESCE(in_the_money, 0) AS in_the_money,
	delta, gamma, theta, vega, COALESCE(source, '') AS source, COALESCE(contract_symbol, '') AS contract_symbol, COALESCE(CAST(quote_timestamp AS TEXT), '') AS quote_timestamp`

func scanStoredOptionRows(rows *sql.Rows) ([]StoredOptionRow, error) {
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
		r.LastPrice = nullFloatPtr(last)
		r.Bid = nullFloatPtr(bid)
		r.Ask = nullFloatPtr(ask)
		r.Volume = nullFloatPtr(vol)
		r.OI = nullFloatPtr(oi)
		r.IV = nullFloatPtr(iv)
		r.ProviderIV = r.IV
		r.InTheMoney = itm != 0
		r.Delta = nullFloatPtr(delta)
		r.Gamma = nullFloatPtr(gamma)
		r.Theta = nullFloatPtr(theta)
		r.Vega = nullFloatPtr(vega)
		out = append(out, r)
	}
	return out, rows.Err()
}

func nullFloatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid || math.IsNaN(n.Float64) || math.IsInf(n.Float64, 0) {
		return nil
	}
	v := n.Float64
	return &v
}

func (s *Store) snapshotsAt(ctx context.Context, ticker, source string, moments []string) ([]StoredOptionRow, error) {
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
			ORDER BY collected_at`, snapshotSelectCols, where, snapshotSelectCols, where)
		rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		return scanStoredOptionRows(rows)
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
		ORDER BY collected_at`, snapshotSelectCols, where, snapshotSelectCols, where)
	rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStoredOptionRows(rows)
}

func (s *Store) latestSnapshot(ctx context.Context, ticker, source string) (string, []StoredOptionRow, error) {
	moment, err := s.latestMoment(ctx, ticker, source)
	if err != nil || moment == "" {
		return "", nil, err
	}
	rows, err := s.snapshotsAt(ctx, ticker, source, []string{moment})
	return moment, rows, err
}

func (s *Store) collectionDepth(ctx context.Context) (map[string]string, error) {
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

func (s *Store) collectionState(ctx context.Context, ticker, source string) (string, int, error) {
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
		if status == "failed" && !isClosedMarketRefusal(started, errMsg) {
			failures++
		}
	}
	return "", failures, rows.Err()
}

func isClosedMarketRefusal(startedAt, errorMessage string) bool {
	return errorMessage != "" &&
		strings.Contains(errorMessage, "of contracts have open_interest=0") &&
		marketStateFromClock(startedAt) == "closed"
}

func openSecondsBetween(start, end time.Time) float64 {
	if !end.After(start) {
		return 0
	}
	sLocal := start.In(marketZone)
	eLocal := end.In(marketZone)
	day := nyDay(sLocal)
	endDay := nyDay(eLocal)
	total := 0.0
	for !day.After(endDay) {
		if hrs, ok := nyseSessionHours(day); ok {
			openAt, closeAt, okBounds := parseSessionBounds(day, hrs)
			if okBounds {
				ovStart := openAt
				if sLocal.After(ovStart) {
					ovStart = sLocal
				}
				ovEnd := closeAt
				if eLocal.Before(ovEnd) {
					ovEnd = eLocal
				}
				if ovEnd.After(ovStart) {
					total += ovEnd.Sub(ovStart).Seconds()
				}
			}
		}
		day = day.AddDate(0, 0, 1)
	}
	return total
}

func agePhrase(gap time.Duration) string {
	secs := int(gap.Seconds())
	if secs < 0 {
		secs = 0
	}
	days := secs / 86400
	secs %= 86400
	hours := secs / 3600
	secs %= 3600
	mins := secs / 60
	if days > 0 {
		if hours > 0 {
			return fmt.Sprintf("%dd %dh", days, hours)
		}
		return fmt.Sprintf("%dd", days)
	}
	if hours > 0 {
		if mins > 0 {
			return fmt.Sprintf("%dh %dm", hours, mins)
		}
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dm", mins)
}

func assessFreshness(lastSuccess string, failures, intervalMinutes int, now time.Time) (string, string) {
	if intervalMinutes <= 0 {
		intervalMinutes = 60
	}
	ref := now.UTC()
	if lastSuccess == "" {
		if failures >= 3 {
			return "err", fmt.Sprintf("Nothing has been collected for this symbol yet. The last %d attempts failed. It may not be listed with the current data source.", failures)
		}
		return "ok", "数据正常"
	}
	lastTime, ok := parseAnyTime(lastSuccess)
	if !ok {
		return "ok", "数据正常"
	}
	traded := openSecondsBetween(lastTime, ref)
	budget := float64(2 * intervalMinutes * 60)
	gap := ref.Sub(lastTime)
	if failures >= 3 {
		return "err", fmt.Sprintf("Collection for this symbol is failing. The last %d attempts were refused; the newest data here is from %s ago. Other symbols are unaffected.", failures, agePhrase(gap))
	}
	if traded <= budget {
		if marketStateFromClock(ref.Format(time.RFC3339)) == "closed" && gap > time.Hour {
			return "ok", fmt.Sprintf("Market closed since the last collection (%s ago). The chain does not change until the next session.", agePhrase(gap))
		}
		return "ok", "数据正常"
	}
	return "warn", fmt.Sprintf("This data is %s old. The market has been open for %s since the last successful collection, so this is not the market being closed.", agePhrase(gap), agePhrase(time.Duration(traded)*time.Second))
}

// buildRibbonPresolved builds RibbonData from a single snapshot whose rows have already run withSolvedIV.
func buildRibbonPresolved(rows []StoredOptionRow, fullGEXMatrix *ExposureMatrix, r, q float64) RibbonData {
	if len(rows) == 0 {
		return RibbonData{}
	}
	w := computeGammaWeatherPresolved(rows, r, q)
	if w == nil {
		return RibbonData{}
	}
	spot := w.Spot
	state := w.State
	if state == "" {
		state = weatherClear
	}
	cnPair, ok := weatherCN[state]
	if !ok {
		cnPair = [2]string{state, ""}
	}

	// Weather expiries for GEX walls (chosen_w in desktop_compute.py).
	chosenWSet := make(map[string]bool, len(w.Expiries))
	for _, e := range w.Expiries {
		chosenWSet[e] = true
	}
	var weatherRows []StoredOptionRow
	if len(chosenWSet) > 0 {
		weatherRows = make([]StoredOptionRow, 0, len(rows))
		for i := range rows {
			exp := rows[i].Expiry
			if len(exp) >= 10 {
				exp = exp[:10]
			}
			if chosenWSet[exp] {
				weatherRows = append(weatherRows, rows[i])
			}
		}
	} else {
		weatherRows = rows
	}
	callGEXWall, putGEXWall := gexWallsPresolved(weatherRows, r, q)

	latestMoment := rows[0].CollectedAt
	for i := 1; i < len(rows); i++ {
		if rows[i].CollectedAt > latestMoment {
			latestMoment = rows[i].CollectedAt
		}
	}

	expMap := make(map[string][]StoredOptionRow)
	for i := range rows {
		e := rows[i].Expiry
		if len(e) >= 10 {
			e = e[:10]
		}
		expMap[e] = append(expMap[e], rows[i])
	}
	var liveExps []string
	for e := range expMap {
		if yearsToExpiry(e, latestMoment) > 0 {
			liveExps = append(liveExps, e)
		}
	}
	sort.Strings(liveExps)

	nearestExp := ""
	if len(liveExps) > 0 {
		nearestExp = liveExps[0]
	}

	var mp *float64
	var move *ExpectedMoveResult
	var atmIV *float64
	if nearestExp != "" {
		expRows := expMap[nearestExp]
		mp = maxPain(expRows)
		move = expectedMovePresolved(expRows, spot)
		if move != nil {
			v := move.ATMIV
			atmIV = &v
		}
	}

	var callOI, putOI, callVol, putVol float64
	for i := range rows {
		row := &rows[i]
		if row.OptionType == "call" {
			callOI += valOrZero(row.OI)
			callVol += valOrZero(row.Volume)
		} else if row.OptionType == "put" {
			putOI += valOrZero(row.OI)
			putVol += valOrZero(row.Volume)
		}
	}
	var pcrOI, pcrVol *float64
	if callOI > 0 {
		v := putOI / callOI
		pcrOI = &v
	}
	if callVol > 0 {
		v := putVol / callVol
		pcrVol = &v
	}

	flipDistStr := "25% 内无"
	if w.GammaFlip != nil && w.FlipDistancePct != nil && !math.IsNaN(*w.FlipDistancePct) {
		flipDistStr = fmt.Sprintf("距现价 %.1f%%", *w.FlipDistancePct)
	}
	moveStr := "—"
	movePctStr := ""
	if move != nil {
		moveStr = fmt.Sprintf("±%s", fmtPriceVal(move.Move, 2))
		movePctStr = fmt.Sprintf("±%.2f%% · %s", move.MovePct, fmtExpiryShort(nearestExp))
	}
	maxPainExp := "—"
	if nearestExp != "" {
		maxPainExp = fmtExpiryShort(nearestExp)
	}
	pcrOIStr := "—"
	if pcrOI != nil {
		pcrOIStr = fmt.Sprintf("%.2f", *pcrOI)
	}
	pcrVolStr := ""
	if pcrVol != nil {
		pcrVolStr = fmt.Sprintf("成交量 %.2f", *pcrVol)
	}
	netGEX := w.NetGEX
	return RibbonData{
		Spot:         spot,
		SpotStr:      fmtPriceVal(spot, 2),
		State:        state,
		StateCN:      cnPair[0],
		StateSub:     cnPair[1],
		Positive:     w.NetGEX >= 0,
		Scope:        fmt.Sprintf("近 %d 天 · %d 到期日", w.HorizonDTE, w.ExpiryCount),
		NetGEX:       &netGEX,
		NetGEXStr:    fmtCompactVal(w.NetGEX, true, 2),
		GammaFlip:    w.GammaFlip,
		GammaFlipStr: fmtPrice(w.GammaFlip, 2),
		FlipDistStr:  flipDistStr,
		CallGEXWall:  fmtStrikeLabel(callGEXWall),
		PutGEXWall:   fmtStrikeLabel(putGEXWall),
		CallOIWall:   fmtStrikeLabel(w.CallWall),
		PutOIWall:    fmtStrikeLabel(w.PutWall),
		MaxPain:      fmtStrikeLabel(mp),
		MaxPainExp:   maxPainExp,
		MoveStr:      moveStr,
		MovePctStr:   movePctStr,
		ATMIVStr:     fmtPct(atmIV, 1, false),
		PCROIStr:     pcrOIStr,
		PCRVolStr:    pcrVolStr,
	}
}

func (s *DesktopService) buildSummaryJSON(ctx context.Context, ticker, source string) ([]byte, error) {
	_, latest, err := s.store.latestSnapshot(ctx, ticker, source)
	if err != nil {
		return nil, err
	}
	if len(latest) == 0 {
		return json.Marshal(TickerSummary{
			Symbol:    ticker,
			Source:    source,
			HasData:   false,
			NetGEXStr: "—",
			StateCN:   "无数据",
		})
	}
	withSolvedIV(latest, defaultRiskFreeRate, 0.0)
	ribbon := buildRibbonPresolved(latest, nil, defaultRiskFreeRate, 0.0)
	spot := ribbon.Spot
	return json.Marshal(TickerSummary{
		Symbol:    ticker,
		Source:    source,
		HasData:   true,
		Spot:      &spot,
		NetGEX:    ribbon.NetGEX,
		NetGEXStr: ribbon.NetGEXStr,
		State:     ribbon.State,
		StateCN:   ribbon.StateCN,
		Ribbon:    &ribbon,
	})
}

func (s *DesktopService) buildViewJSON(ctx context.Context, path string, q url.Values) ([]byte, error) {
	switch path {
	case "/api/workbench":
		res, err := s.buildWorkbenchGo(ctx, q)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "/api/volatility":
		res, err := s.buildVolatilityGo(ctx, q)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "/api/contract":
		res, err := s.buildContractGo(ctx, q)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	case "/api/flow":
		res, err := s.buildFlowGo(ctx, q)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	default:
		return nil, fmt.Errorf("未知视图: %s", path)
	}
}

func fmtTimeMMDDHHMM(momentStr string) string {
	if t, ok := parseAnyTime(momentStr); ok {
		return t.UTC().Format("01-02 15:04")
	}
	if len(momentStr) >= 16 {
		return momentStr[5:16]
	}
	return momentStr
}

func fmtMomentFull(momentStr string) string {
	if t, ok := parseAnyTime(momentStr); ok {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return momentStr
}

func fmtMomentYYYYMMDDHHMM(momentStr string) string {
	if t, ok := parseAnyTime(momentStr); ok {
		return t.UTC().Format("2006-01-02 15:04")
	}
	if len(momentStr) >= 16 {
		return momentStr[:16]
	}
	return momentStr
}

func percentileLinear(vals []float64, p float64) float64 {
	if len(vals) == 0 {
		return 1.0
	}
	cp := make([]float64, len(vals))
	copy(cp, vals)
	sort.Float64s(cp)
	if len(cp) == 1 {
		return cp[0]
	}
	pos := (p / 100.0) * float64(len(cp)-1)
	lo := int(math.Floor(pos))
	hi := int(math.Ceil(pos))
	frac := pos - float64(lo)
	return cp[lo] + frac*(cp[hi]-cp[lo])
}

// buildWorkbenchGo implements /api/workbench in pure Go.
func (s *DesktopService) buildWorkbenchGo(ctx context.Context, q url.Values) (WorkbenchResponse, error) {
	ticker := strings.ToUpper(strings.TrimSpace(q.Get("ticker")))
	if ticker == "" {
		ticker = "SPY"
	}
	dimension := strings.ToLower(strings.TrimSpace(q.Get("dimension")))
	if dimension != "gex" && dimension != "vanna" && dimension != "charm" {
		dimension = "gex"
	}
	side := strings.ToLower(strings.TrimSpace(q.Get("side")))
	if side != "net" && side != "call" && side != "put" {
		side = "net"
	}
	asOf := strings.TrimSpace(q.Get("as_of"))
	nExpiries, err := strconv.Atoi(q.Get("n_expiries"))
	if err != nil || nExpiries <= 0 {
		nExpiries = 12
	}
	strikesEach, err := strconv.Atoi(q.Get("strikes"))
	if err != nil || strikesEach <= 0 {
		strikesEach = 20
	}
	strikeRangePct := 0.0
	if q.Has("range_pct") {
		strikeRangePct, err = strconv.ParseFloat(q.Get("range_pct"), 64)
		if err != nil || math.IsNaN(strikeRangePct) || math.IsInf(strikeRangePct, 0) || strikeRangePct < 1 || strikeRangePct > 50 {
			return WorkbenchResponse{}, fmt.Errorf("行权价范围须为现价的 ±1%% 至 ±50%%")
		}
	}

	source, err := s.store.activeSource(ctx, ticker)
	if err != nil {
		return WorkbenchResponse{}, err
	}
	newestMoment, latestRows, err := s.store.latestSnapshot(ctx, ticker, source)
	if err != nil {
		return WorkbenchResponse{}, err
	}
	if asOf == "" {
		s.intradayMu.Lock()
		live := s.liveSnapshots[ticker]
		s.intradayMu.Unlock()
		if len(live) > 0 && live[0].CollectedAt > newestMoment {
			cp := make([]StoredOptionRow, len(live))
			copy(cp, live)
			latestRows = cp
			newestMoment = cp[0].CollectedAt
		}
	}
	srcLabel := strings.ToUpper(source)
	if srcLabel == "" {
		srcLabel = "—"
	}
	if len(latestRows) == 0 {
		return WorkbenchResponse{
			Ticker:    ticker,
			Source:    srcLabel,
			Empty:     true,
			Moments:   []MomentOption{},
			Strikes:   []float64{},
			Expiries:  []string{},
			Totals:    []float64{},
			TotalsStr: []string{},
			CellsText: [][]string{},
			CellsNorm: [][]float64{},
			CellsTip:  [][]string{},
			Profile:   []float64{},
			Board:     []ExpiryBoardRow{},
		}, nil
	}

	withSolvedIV(latestRows, defaultRiskFreeRate, 0.0)
	ribbon := buildRibbonPresolved(latestRows, nil, defaultRiskFreeRate, 0.0)

	moments, err := s.store.collectionMoments(ctx, ticker, source, snapshotHistoryDays(), false)
	if err != nil {
		return WorkbenchResponse{}, err
	}
	newest := newestMoment
	if len(moments) > 0 {
		newest = moments[0]
	}

	chosenMoment := newestMoment
	snapshotRows := latestRows
	if asOf != "" {
		found := false
		for _, m := range moments {
			if m == asOf || fmtMomentFull(m) == asOf {
				chosenMoment = m
				found = true
				break
			}
		}
		if found && chosenMoment != newestMoment {
			snap, err := s.store.snapshotsAt(ctx, ticker, source, []string{chosenMoment})
			if err == nil && len(snap) > 0 {
				withSolvedIV(snap, defaultRiskFreeRate, 0.0)
				snapshotRows = snap
			} else {
				chosenMoment = newestMoment
			}
		}
	}

	lastSuccess, failures, _ := s.store.collectionState(ctx, ticker, source)
	intervalRaw, _ := s.store.setting(ctx, "collector_interval_minutes")
	intervalMin, _ := strconv.Atoi(intervalRaw)
	dot, statusNote := assessFreshness(lastSuccess, failures, intervalMin, s.now())

	spot := snapshotRows[0].UnderlyingPrice

	// Group snapshot rows by expiry and filter to tradeable expiries (years_to_expiry > 0).
	expRowsMap := make(map[string][]StoredOptionRow, 32)
	for i := range snapshotRows {
		exp := snapshotRows[i].Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		expRowsMap[exp] = append(expRowsMap[exp], snapshotRows[i])
	}
	allExpiries := make([]string, 0, len(expRowsMap))
	for e := range expRowsMap {
		if yearsToExpiry(e, chosenMoment) > 0 {
			allExpiries = append(allExpiries, e)
		}
	}
	sort.Strings(allExpiries)

	maxExp := len(allExpiries)
	if maxExp < 1 {
		maxExp = 1
	}
	shownCount := nExpiries
	if shownCount < 1 {
		shownCount = 1
	}
	if len(allExpiries) > 0 && shownCount > len(allExpiries) {
		shownCount = len(allExpiries)
	}
	shownExpiries := allExpiries
	if len(shownExpiries) > shownCount {
		shownExpiries = shownExpiries[:shownCount]
	}
	shownSet := make(map[string]bool, len(shownExpiries))
	for _, e := range shownExpiries {
		shownSet[e] = true
	}

	callRows := make([]StoredOptionRow, 0, len(snapshotRows)/2+1)
	putRows := make([]StoredOptionRow, 0, len(snapshotRows)/2+1)
	shownSnapshotRows := make([]StoredOptionRow, 0, len(snapshotRows))
	for i := range snapshotRows {
		exp := snapshotRows[i].Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		if shownSet[exp] {
			shownSnapshotRows = append(shownSnapshotRows, snapshotRows[i])
		}
		if snapshotRows[i].OptionType == "call" {
			callRows = append(callRows, snapshotRows[i])
		} else if snapshotRows[i].OptionType == "put" {
			putRows = append(putRows, snapshotRows[i])
		}
	}

	fullNetMat := buildExposureMatrixPresolved(snapshotRows, dimension, shownSet, defaultRiskFreeRate, 0.0)
	callMat := buildExposureMatrixPresolved(callRows, dimension, shownSet, defaultRiskFreeRate, 0.0)
	putMat := buildExposureMatrixPresolved(putRows, dimension, shownSet, defaultRiskFreeRate, 0.0)

	displayFull := fullNetMat
	if side == "call" {
		displayFull = callMat
	} else if side == "put" {
		displayFull = putMat
	}

	var gexMat ExposureMatrix
	if dimension == "gex" {
		gexMat = fullNetMat
	} else {
		gexMat = buildExposureMatrixPresolved(snapshotRows, "gex", shownSet, defaultRiskFreeRate, 0.0)
	}
	flip := gammaFlipFromMatrix(gexMat, spot)
	callWall, putWall := dealerWalls(shownSnapshotRows, shownSet)
	callGEXWall, putGEXWall := gexWallsPresolved(shownSnapshotRows, defaultRiskFreeRate, 0.0)

	var nearPain, moveLower, moveUpper *float64
	if len(shownExpiries) > 0 {
		nearRows := expRowsMap[shownExpiries[0]]
		nearPain = maxPain(nearRows)
		if mv := expectedMovePresolved(nearRows, spot); mv != nil {
			moveLower = floatPtr(mv.Lower)
			moveUpper = floatPtr(mv.Upper)
		}
	}

	// Pressure uses a price-relative interval; the workbench retains its ATM
	// strike-count window. Both slice strikes and cells together, in descending order.
	var atmStrike *float64
	var strikeLower, strikeUpper *float64
	if strikeRangePct > 0 {
		if spot <= 0 || math.IsNaN(spot) || math.IsInf(spot, 0) {
			return WorkbenchResponse{}, fmt.Errorf("当前快照缺少有效现价，无法计算百分比范围")
		}
		halfWidth := spot * strikeRangePct / 100
		strikeLower, strikeUpper = floatPtr(spot-halfWidth), floatPtr(spot+halfWidth)
	}
	bandedStrikes := displayFull.Strikes
	rawBandedCells := displayFull.Cells
	if len(displayFull.Strikes) > 0 {
		atmIdx := 0
		bestDist := math.Abs(displayFull.Strikes[0] - spot)
		for i, k := range displayFull.Strikes[1:] {
			if d := math.Abs(k - spot); d < bestDist {
				bestDist = d
				atmIdx = i + 1
			}
		}
		atmVal := displayFull.Strikes[atmIdx]
		atmStrike = &atmVal
		lo := atmIdx - strikesEach
		if lo < 0 {
			lo = 0
		}
		hi := atmIdx + strikesEach + 1
		if hi > len(displayFull.Strikes) {
			hi = len(displayFull.Strikes)
		}
		if strikeRangePct > 0 {
			// Inclusive bounds with only floating-point roundoff tolerance. Do not
			// limit the number of strikes or expand an empty band to the nearest ATM.
			epsilon := math.Max(1, spot) * 1e-12
			lo = sort.Search(len(displayFull.Strikes), func(i int) bool {
				return displayFull.Strikes[i] <= *strikeUpper+epsilon
			})
			hi = sort.Search(len(displayFull.Strikes), func(i int) bool {
				return displayFull.Strikes[i] < *strikeLower-epsilon
			})
			if atmVal < *strikeLower-epsilon || atmVal > *strikeUpper+epsilon {
				atmStrike = nil
			}
		}
		bandedStrikes = displayFull.Strikes[lo:hi]
		rawBandedCells = displayFull.Cells[lo:hi]
	}

	// Keep columns where at least one row in bandedCells is != 0 or NaN.
	var activeColIndices []int
	var activeExpiries []string
	for ci, exp := range displayFull.Expiries {
		// A zero net exposure may still have offsetting Call/Put pressure and OI.
		keep := strikeRangePct > 0 && len(rawBandedCells) > 0
		for si := range rawBandedCells {
			v := rawBandedCells[si][ci]
			if math.IsNaN(v) || v != 0 {
				keep = true
				break
			}
		}
		if keep {
			activeColIndices = append(activeColIndices, ci)
			activeExpiries = append(activeExpiries, exp)
		}
	}

	nCols := len(activeColIndices)
	if nCols == 0 {
		bandedStrikes = []float64{}
		atmStrike = nil
	}
	bandedCells := make([][]float64, len(bandedStrikes))
	for si := range bandedStrikes {
		row := make([]float64, nCols)
		for idx, ci := range activeColIndices {
			row[idx] = rawBandedCells[si][ci]
		}
		bandedCells[si] = row
	}

	// Lookup maps for callMat and putMat by (strike, expiry) plus OI and volume.
	type seKey struct {
		strike float64
		expiry string
	}
	callValMap := make(map[seKey]float64, len(callMat.Strikes)*len(callMat.Expiries))
	for si, k := range callMat.Strikes {
		for ci, exp := range callMat.Expiries {
			callValMap[seKey{k, exp}] = callMat.Cells[si][ci]
		}
	}
	putValMap := make(map[seKey]float64, len(putMat.Strikes)*len(putMat.Expiries))
	for si, k := range putMat.Strikes {
		for ci, exp := range putMat.Expiries {
			putValMap[seKey{k, exp}] = putMat.Cells[si][ci]
		}
	}

	type cellOIVol struct {
		callOI float64
		putOI  float64
		vol    float64
	}
	oiVolMap := make(map[seKey]cellOIVol, len(shownSnapshotRows))
	for i := range shownSnapshotRows {
		r := &shownSnapshotRows[i]
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		sk := seKey{r.Strike, exp}
		cur := oiVolMap[sk]
		if r.OptionType == "call" {
			cur.callOI += valOrZero(r.OI)
		} else if r.OptionType == "put" {
			cur.putOI += valOrZero(r.OI)
		}
		cur.vol += valOrZero(r.Volume)
		oiVolMap[sk] = cur
	}

	// The pressure total must match the selected interval and displayed profiles.
	// The workbench keeps its existing full-chain column totals.
	totalCells := displayFull.Cells
	if strikeRangePct > 0 {
		totalCells = rawBandedCells
	}
	colTotals := make([]float64, nCols)
	for idx, ci := range activeColIndices {
		sum := 0.0
		for si := range totalCells {
			v := totalCells[si][ci]
			if !math.IsNaN(v) {
				sum += v
			}
		}
		colTotals[idx] = sum
	}
	grandTotal := 0.0
	colTotalsStr := make([]string, nCols)
	for idx, v := range colTotals {
		grandTotal += v
		colTotalsStr[idx] = fmtCompactVal(v, true, 2)
	}

	// Compute vmax as 92nd percentile of abs(non-zero finite values in bandedCells).
	var nonZeroAbs []float64
	for si := range bandedCells {
		for _, v := range bandedCells[si] {
			if !math.IsNaN(v) && !math.IsInf(v, 0) && v != 0 {
				nonZeroAbs = append(nonZeroAbs, math.Abs(v))
			}
		}
	}
	vmax := 1.0
	if len(nonZeroAbs) > 0 {
		vmax = percentileLinear(nonZeroAbs, 92.0)
		if vmax <= 0 {
			vmax = 1.0
		}
	}

	profile := make([]float64, len(bandedStrikes))
	profileStr := make([]string, len(bandedStrikes))
	callProfile := make([]float64, len(bandedStrikes))
	callProfileStr := make([]string, len(bandedStrikes))
	putProfile := make([]float64, len(bandedStrikes))
	putProfileStr := make([]string, len(bandedStrikes))
	strikeCallOI := make([]string, len(bandedStrikes))
	strikePutOI := make([]string, len(bandedStrikes))
	profileMax := 1.0
	for si, k := range bandedStrikes {
		rowSum := 0.0
		for _, v := range bandedCells[si] {
			if !math.IsNaN(v) {
				rowSum += v
			}
		}
		var cSum, pSum, cOISum, pOISum float64
		for _, exp := range activeExpiries {
			sk := seKey{k, exp}
			if cv := callValMap[sk]; !math.IsNaN(cv) {
				cSum += cv
			}
			if pv := putValMap[sk]; !math.IsNaN(pv) {
				pSum += pv
			}
			ov := oiVolMap[sk]
			cOISum += ov.callOI
			pOISum += ov.putOI
		}
		profile[si] = rowSum
		profileStr[si] = fmtCompactVal(rowSum, true, 2)
		callProfile[si] = cSum
		callProfileStr[si] = fmtCompactVal(cSum, true, 2)
		putProfile[si] = pSum
		putProfileStr[si] = fmtCompactVal(pSum, true, 2)
		strikeCallOI[si] = fmtVolCompact(cOISum)
		strikePutOI[si] = fmtVolCompact(pOISum)
		if a := math.Abs(rowSum); a > profileMax {
			profileMax = a
		}
	}

	expLabels := make([]string, nCols)
	expDTEs := make([]string, nCols)
	for idx, exp := range activeExpiries {
		expLabels[idx] = fmtExpiryShort(exp)
		expDTEs[idx] = fmt.Sprintf("%dd", calcDTE(exp, chosenMoment))
	}

	strikeLabels := make([]string, len(bandedStrikes))
	strikeTags := make([]string, len(bandedStrikes))
	cellsText := make([][]string, len(bandedStrikes))
	cellsNorm := make([][]float64, len(bandedStrikes))
	cellsTip := make([][]string, len(bandedStrikes))

	for si, k := range bandedStrikes {
		strikeLabels[si] = fmtStrikeVal(k)
		var tags []string
		if callGEXWall != nil && math.Abs(k-*callGEXWall) < 1e-6 {
			tags = append(tags, "◆C")
		}
		if putGEXWall != nil && math.Abs(k-*putGEXWall) < 1e-6 {
			tags = append(tags, "◆P")
		}
		if callWall != nil && math.Abs(k-*callWall) < 1e-6 {
			tags = append(tags, "▲OI")
		}
		if putWall != nil && math.Abs(k-*putWall) < 1e-6 {
			tags = append(tags, "▼OI")
		}
		if nearPain != nil && math.Abs(k-*nearPain) < 1e-6 {
			tags = append(tags, "✕Pain")
		}
		strikeTags[si] = strings.Join(tags, " ")

		tRow := make([]string, nCols)
		nRow := make([]float64, nCols)
		tipRow := make([]string, nCols)
		for idx, exp := range activeExpiries {
			val := bandedCells[si][idx]
			if math.IsNaN(val) {
				tRow[idx] = ""
				nRow[idx] = 0.0
			} else if val == 0 {
				tRow[idx] = "·"
				nRow[idx] = 0.0
			} else {
				tRow[idx] = fmtCellCompact(val)
				norm := val / vmax
				if norm < -1.0 {
					norm = -1.0
				} else if norm > 1.0 {
					norm = 1.0
				}
				nRow[idx] = norm
			}
			sk := seKey{k, exp}
			cv := callValMap[sk]
			pv := putValMap[sk]
			ov := oiVolMap[sk]
			valForTip := val
			if math.IsNaN(valForTip) {
				valForTip = 0
			}
			tipRow[idx] = fmt.Sprintf(
				"K %s · %s\n%s 净: %s (Call %s / Put %s)\nOI: C %s / P %s · 成交量: %s",
				fmtStrikeVal(k), expLabels[idx],
				dimensionCN[dimension], fmtCompactVal(valForTip, true, 2),
				fmtCompactVal(cv, true, 2), fmtCompactVal(pv, true, 2),
				formatIntComma(int64(ov.callOI)), formatIntComma(int64(ov.putOI)), formatIntComma(int64(ov.vol)),
			)
		}
		cellsText[si] = tRow
		cellsNorm[si] = nRow
		cellsTip[si] = tipRow
	}

	// Expiry structure board iterates over shownExpiries.
	board := make([]ExpiryBoardRow, 0, len(shownExpiries))
	for _, exp := range shownExpiries {
		eRows := expRowsMap[exp]
		var cGEX, pGEX, cOI, pOI float64
		for i := range eRows {
			r := &eRows[i]
			v, ok := rowExposure(r, "gex", defaultRiskFreeRate, 0.0)
			if ok && !math.IsNaN(v) {
				if r.OptionType == "call" {
					cGEX += v
				} else if r.OptionType == "put" {
					pGEX += v
				}
			}
			if r.OptionType == "call" {
				cOI += valOrZero(r.OI)
			} else if r.OptionType == "put" {
				pOI += valOrZero(r.OI)
			}
		}
		netG := cGEX + pGEX
		mp := maxPain(eRows)
		mv := expectedMovePresolved(eRows, spot)
		var atmIV *float64
		moveStr := "—"
		movePctStr := "—"
		if mv != nil {
			v := mv.ATMIV
			atmIV = &v
			moveStr = fmt.Sprintf("±%s", fmtPriceVal(mv.Move, 2))
			movePctStr = fmt.Sprintf("±%.2f%%", mv.MovePct)
		}
		pcrStr := "—"
		if cOI > 0 {
			pcrStr = fmt.Sprintf("%.2f", pOI/cOI)
		}
		board = append(board, ExpiryBoardRow{
			Expiry:     fmtExpiryShort(exp),
			ExpiryISO:  exp,
			DTE:        calcDTE(exp, chosenMoment),
			NetGEX:     netG,
			NetGEXStr:  fmtCompactVal(netG, true, 2),
			CallGEXStr: fmtCompactVal(cGEX, true, 2),
			PutGEXStr:  fmtCompactVal(pGEX, true, 2),
			MaxPain:    fmtStrikeLabel(mp),
			ATMIV:      fmtPct(atmIV, 1, false),
			Move:       moveStr,
			MovePct:    movePctStr,
			CallOI:     formatIntComma(int64(cOI)),
			PutOI:      formatIntComma(int64(pOI)),
			PCR:        pcrStr,
			Contracts:  contractsBackingExpiry(eRows),
		})
	}

	momentOpts := make([]MomentOption, 0, len(moments))
	for _, m := range moments {
		suffix := ""
		if m == newest {
			suffix = " (最新)"
		}
		momentOpts = append(momentOpts, MomentOption{
			Value: fmtMomentFull(m),
			Label: fmtTimeMMDDHHMM(m) + suffix,
		})
	}

	asOfSuffix := " · 回放"
	if chosenMoment == newest {
		asOfSuffix = " · 最新"
	}

	// Count unique (expiry, strike, option_type) in latestRows.
	type uniqKey struct {
		exp  string
		k    float64
		side string
	}
	uniqSet := make(map[uniqKey]bool, len(latestRows))
	for i := range latestRows {
		r := &latestRows[i]
		uniqSet[uniqKey{r.Expiry, r.Strike, r.OptionType}] = true
	}

	intraday := s.buildIntradaySummary(ctx, ticker, source, chosenMoment, snapshotRows)

	return WorkbenchResponse{
		Ticker:         ticker,
		Source:         srcLabel,
		Empty:          false,
		CollectedAt:    fmtMomentYYYYMMDDHHMM(newestMoment),
		AsOf:           fmtMomentFull(chosenMoment),
		AsOfDisplay:    fmtTimeMMDDHHMM(chosenMoment) + asOfSuffix,
		Moments:        momentOpts,
		ContractsCount: len(uniqSet),
		Dot:            dot,
		StatusNote:     statusNote,
		Ribbon:         ribbon,
		Dimension:      dimension,
		DimensionCN:    dimensionCN[dimension],
		UnitCN:         unitCN[dimension],
		Side:           side,
		MaxExpiries:    maxExp,
		ShownExpiries:  len(shownExpiries),
		Spot:           spot,
		StrikeRangePct: strikeRangePct,
		StrikeLower:    strikeLower,
		StrikeUpper:    strikeUpper,
		ATMStrike:      atmStrike,
		Flip:           flip,
		MoveLower:      moveLower,
		MoveUpper:      moveUpper,
		Strikes:        bandedStrikes,
		StrikeLabels:   strikeLabels,
		StrikeTags:     strikeTags,
		Expiries:       expLabels,
		ExpiryDTEs:     expDTEs,
		Totals:         colTotals,
		TotalsStr:      colTotalsStr,
		GrandTotal:     grandTotal,
		GrandTotalStr:  fmtCompactVal(grandTotal, true, 2),
		CellsText:      cellsText,
		CellsNorm:      cellsNorm,
		CellsTip:       cellsTip,
		Profile:        profile,
		ProfileStr:     profileStr,
		ProfileMax:     profileMax,
		CallProfile:    callProfile,
		CallProfileStr: callProfileStr,
		PutProfile:     putProfile,
		PutProfileStr:  putProfileStr,
		StrikeCallOI:   strikeCallOI,
		StrikePutOI:    strikePutOI,
		Intraday:       intraday,
		Board:          board,
	}, nil
}

// buildVolatilityGo implements /api/volatility in pure Go.
func (s *DesktopService) buildVolatilityGo(ctx context.Context, q url.Values) (VolatilityResponse, error) {
	ticker := strings.ToUpper(strings.TrimSpace(q.Get("ticker")))
	if ticker == "" {
		ticker = "SPY"
	}
	widthPct, err := strconv.ParseFloat(q.Get("width"), 64)
	if err != nil || widthPct <= 0 {
		widthPct = 12.0
	}
	source, err := s.store.activeSource(ctx, ticker)
	if err != nil {
		return VolatilityResponse{}, err
	}
	chosenMoment, latestRows, err := s.store.latestSnapshot(ctx, ticker, source)
	if err != nil {
		return VolatilityResponse{}, err
	}
	if len(latestRows) == 0 {
		return VolatilityResponse{
			Ticker:    ticker,
			Empty:     true,
			Skew:      []SkewCurve{},
			Term:      []TermPoint{},
			IVHistory: []IVHistoryPoint{},
			RV:        []RVItem{},
		}, nil
	}

	withSolvedIV(latestRows, defaultRiskFreeRate, 0.0)
	spot := latestRows[0].UnderlyingPrice
	lo := spot * (1.0 - widthPct/100.0)
	hi := spot * (1.0 + widthPct/100.0)

	expRowsMap := make(map[string][]StoredOptionRow)
	for i := range latestRows {
		exp := latestRows[i].Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		expRowsMap[exp] = append(expRowsMap[exp], latestRows[i])
	}
	var expiries []string
	for e := range expRowsMap {
		if yearsToExpiry(e, chosenMoment) > 0 {
			expiries = append(expiries, e)
		}
	}
	sort.Strings(expiries)

	skew := make([]SkewCurve, 0, 5)
	skewExps := expiries
	if len(skewExps) > 5 {
		skewExps = skewExps[:5]
	}
	for _, exp := range skewExps {
		eRows := expRowsMap[exp]
		strikeIV := make(map[float64]struct {
			sum float64
			cnt int
		})
		for i := range eRows {
			r := &eRows[i]
			if r.Strike < lo || r.Strike > hi || !isValidPos(r.IV) {
				continue
			}
			isOTM := (r.Strike < spot && r.OptionType == "put") || (r.Strike >= spot && r.OptionType == "call")
			if !isOTM {
				continue
			}
			cur := strikeIV[r.Strike]
			cur.sum += *r.IV
			cur.cnt++
			strikeIV[r.Strike] = cur
		}
		if len(strikeIV) > 0 {
			strikes := make([]float64, 0, len(strikeIV))
			for k := range strikeIV {
				strikes = append(strikes, k)
			}
			sort.Float64s(strikes)
			ivs := make([]float64, len(strikes))
			for i, k := range strikes {
				ivs[i] = strikeIV[k].sum / float64(strikeIV[k].cnt)
			}
			dte := calcDTE(exp, chosenMoment)
			skew = append(skew, SkewCurve{
				Label:   fmt.Sprintf("%s (%dd)", fmtExpiryShort(exp), dte),
				Strikes: strikes,
				IVs:     ivs,
			})
		}
	}

	term := make([]TermPoint, 0, 24)
	termExps := expiries
	if len(termExps) > 24 {
		termExps = termExps[:24]
	}
	for _, exp := range termExps {
		eRows := expRowsMap[exp]
		mv := expectedMovePresolved(eRows, spot)
		if mv != nil {
			lbl := exp
			if len(lbl) >= 10 {
				lbl = lbl[5:10]
			}
			term = append(term, TermPoint{
				Label:   lbl,
				DTE:     calcDTE(exp, chosenMoment),
				ATMIV:   mv.ATMIV,
				MovePct: mv.MovePct,
				Move:    mv.Move,
			})
		}
	}

	ivHist, _ := s.store.ivWeightedAverageSeries(ctx, ticker, source, snapshotHistoryDays())
	rvList, _ := s.store.realizedVolatilityItems(ctx, ticker)

	return VolatilityResponse{
		Ticker:    ticker,
		Empty:     false,
		Spot:      spot,
		Skew:      skew,
		Term:      term,
		IVHistory: ivHist,
		RV:        rvList,
	}, nil
}

func (s *Store) ivWeightedAverageSeries(ctx context.Context, ticker, source string, days int) ([]IVHistoryPoint, error) {
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
		return []IVHistoryPoint{}, err
	}
	defer rows.Close()
	out := []IVHistoryPoint{}
	for rows.Next() {
		var t string
		var v sql.NullFloat64
		if err := rows.Scan(&t, &v); err == nil && v.Valid && !math.IsNaN(v.Float64) {
			out = append(out, IVHistoryPoint{Time: fmtTimeMMDDHHMM(t), IV: v.Float64})
		}
	}
	return out, rows.Err()
}

func (s *Store) realizedVolatilityItems(ctx context.Context, ticker string) ([]RVItem, error) {
	rows, err := s.read.QueryContext(ctx,
		`SELECT window_days, value FROM realized_volatility
		 WHERE ticker = ? AND as_of_date = (SELECT max(as_of_date) FROM realized_volatility WHERE ticker = ?)
		 ORDER BY window_days`,
		strings.ToUpper(ticker), strings.ToUpper(ticker))
	if err != nil {
		return []RVItem{}, err
	}
	defer rows.Close()
	out := []RVItem{}
	for rows.Next() {
		var w int
		var v float64
		if err := rows.Scan(&w, &v); err == nil && !math.IsNaN(v) {
			out = append(out, RVItem{
				Window: fmt.Sprintf("RV%dd", w),
				Value:  fmtPctVal(v, 1, false),
				Raw:    v,
			})
		}
	}
	return out, rows.Err()
}
