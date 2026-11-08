package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"
)

func openSecondsBetween(start, end time.Time) float64 {
	if !end.After(start) {
		return 0
	}
	sLocal := start.In(marketclock.Zone)
	eLocal := end.In(marketclock.Zone)
	day := marketclock.Day(sLocal)
	endDay := marketclock.Day(eLocal)
	total := 0.0
	for !day.After(endDay) {
		if hrs, ok := marketclock.Session(day); ok {
			openAt, closeAt, okBounds := marketclock.SessionBounds(day, hrs)
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
	lastTime, ok := marketclock.ParseTime(lastSuccess)
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
		if marketclock.MarketState(ref.Format(time.RFC3339)) == "closed" && gap > time.Hour {
			return "ok", fmt.Sprintf("Market closed since the last collection (%s ago). The chain does not change until the next session.", agePhrase(gap))
		}
		return "ok", "数据正常"
	}
	return "warn", fmt.Sprintf("This data is %s old. The market has been open for %s since the last successful collection, so this is not the market being closed.", agePhrase(gap), agePhrase(time.Duration(traded)*time.Second))
}

// buildRibbonPresolved builds RibbonData from a single snapshot whose rows have already run withSolvedIV.
func buildRibbonPresolved(ctx context.Context, rows []store.StoredOptionRow, r, q float64) viewmodel.RibbonData {
	if len(rows) == 0 {
		return viewmodel.RibbonData{}
	}
	w := computeGammaWeatherPresolved(ctx, rows, r, q)
	if w == nil {
		return viewmodel.RibbonData{}
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
	var weatherRows []store.StoredOptionRow
	if len(chosenWSet) > 0 {
		weatherRows = make([]store.StoredOptionRow, 0, len(rows))
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

	expMap := make(map[string][]store.StoredOptionRow)
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
		moveStr = fmt.Sprintf("±%s", viewmodel.FmtPriceVal(move.Move, 2))
		movePctStr = fmt.Sprintf("±%.2f%% · %s", move.MovePct, viewmodel.FmtExpiryShort(nearestExp))
	}
	maxPainExp := "—"
	if nearestExp != "" {
		maxPainExp = viewmodel.FmtExpiryShort(nearestExp)
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
	return viewmodel.RibbonData{
		Spot:         spot,
		SpotStr:      viewmodel.FmtPriceVal(spot, 2),
		State:        state,
		StateCN:      cnPair[0],
		StateSub:     cnPair[1],
		Positive:     w.NetGEX >= 0,
		Scope:        fmt.Sprintf("近 %d 天 · %d 到期日", w.HorizonDTE, w.ExpiryCount),
		NetGEX:       &netGEX,
		NetGEXStr:    viewmodel.FmtCompactVal(w.NetGEX, true, 2),
		GammaFlip:    w.GammaFlip,
		GammaFlipStr: viewmodel.FmtPrice(w.GammaFlip, 2),
		FlipDistStr:  flipDistStr,
		CallGEXWall:  viewmodel.FmtStrikeLabel(callGEXWall),
		PutGEXWall:   viewmodel.FmtStrikeLabel(putGEXWall),
		CallOIWall:   viewmodel.FmtStrikeLabel(w.CallWall),
		PutOIWall:    viewmodel.FmtStrikeLabel(w.PutWall),
		MaxPain:      viewmodel.FmtStrikeLabel(mp),
		MaxPainExp:   maxPainExp,
		MoveStr:      moveStr,
		MovePctStr:   movePctStr,
		ATMIVStr:     viewmodel.FmtPct(atmIV, 1, false),
		PCROIStr:     pcrOIStr,
		PCRVolStr:    pcrVolStr,
	}
}

func fmtTimeMMDDHHMM(momentStr string) string {
	if t, ok := marketclock.ParseTime(momentStr); ok {
		return t.UTC().Format("01-02 15:04")
	}
	if len(momentStr) >= 16 {
		return momentStr[5:16]
	}
	return momentStr
}

func fmtMomentFull(momentStr string) string {
	if t, ok := marketclock.ParseTime(momentStr); ok {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return momentStr
}

func fmtMomentYYYYMMDDHHMM(momentStr string) string {
	if t, ok := marketclock.ParseTime(momentStr); ok {
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

func (s *DesktopService) buildWorkbenchGo(ctx context.Context, m store.SnapshotMeta, live []store.StoredOptionRow, barVersion uint64, q workbenchQuery) (viewmodel.WorkbenchResponse, error) {
	ticker, source, newestMoment := m.Ticker, m.Source, m.At
	dimension, side, asOf := q.Dimension, q.Side, q.AsOf
	if dimension != "gex" && dimension != "vanna" && dimension != "charm" {
		dimension = "gex"
	}
	if side != "call" && side != "put" {
		side = "net"
	}
	nExpiries, strikesEach, strikeRangePct := q.Expiries, q.Strikes, q.RangePct
	if nExpiries <= 0 {
		nExpiries = 12
	}
	if strikesEach <= 0 {
		strikesEach = 20
	}

	latestRows := live
	if len(live) > 0 {
		newestMoment, source = live[0].CollectedAt, live[0].Source
	} else {
		snapshot, err := s.snapshot(ctx, m)
		if err != nil {
			return viewmodel.WorkbenchResponse{}, err
		}
		latestRows = snapshot.rows
	}
	srcLabel := strings.ToUpper(source)
	if srcLabel == "" {
		srcLabel = "—"
	}
	if len(latestRows) == 0 {
		return viewmodel.WorkbenchResponse{
			Ticker:    ticker,
			Source:    srcLabel,
			Empty:     true,
			Moments:   []viewmodel.MomentOption{},
			Strikes:   []float64{},
			Expiries:  []string{},
			Totals:    []float64{},
			TotalsStr: []string{},
			CellsText: [][]string{},
			CellsNorm: [][]float64{},
			CellsTip:  [][]string{},
			Profile:   []float64{},
			Board:     []viewmodel.ExpiryBoardRow{},
		}, nil
	}

	ribbonMeta := m
	ribbonMeta.At, ribbonMeta.Source = newestMoment, source
	ribbon, err := s.snapshotRibbon(ctx, ribbonMeta, latestRows)
	if err != nil {
		return viewmodel.WorkbenchResponse{}, err
	}

	moments, err := s.store.CollectionMoments(ctx, ticker, source, snapshotHistoryDays(), false)
	if err != nil {
		return viewmodel.WorkbenchResponse{}, err
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
			historical := m
			historical.At = chosenMoment
			snap, err := s.snapshot(ctx, historical)
			if err == nil && len(snap.rows) > 0 {
				snapshotRows = snap.rows
			} else {
				chosenMoment = newestMoment
			}
		}
	}

	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
	}
	dot, statusNote := "rest", "本地快照 · 手动刷新"

	spot := snapshotRows[0].UnderlyingPrice

	// Group snapshot rows by expiry and filter to tradeable expiries (years_to_expiry > 0).
	expRowsMap := make(map[string][]store.StoredOptionRow, 32)
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

	callRows := make([]store.StoredOptionRow, 0, len(snapshotRows)/2+1)
	putRows := make([]store.StoredOptionRow, 0, len(snapshotRows)/2+1)
	shownSnapshotRows := make([]store.StoredOptionRow, 0, len(snapshotRows))
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

	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
	}
	fullNetMat := buildExposureMatrixPresolved(ctx, snapshotRows, dimension, shownSet, defaultRiskFreeRate, 0.0)
	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
	}
	callMat := buildExposureMatrixPresolved(ctx, callRows, dimension, shownSet, defaultRiskFreeRate, 0.0)
	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
	}
	putMat := buildExposureMatrixPresolved(ctx, putRows, dimension, shownSet, defaultRiskFreeRate, 0.0)

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
		gexMat = buildExposureMatrixPresolved(ctx, snapshotRows, "gex", shownSet, defaultRiskFreeRate, 0.0)
	}
	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
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
			return viewmodel.WorkbenchResponse{}, fmt.Errorf("当前快照缺少有效现价，无法计算百分比范围")
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

	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
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
		colTotalsStr[idx] = viewmodel.FmtCompactVal(v, true, 2)
	}

	// Compute vmax as 92nd percentile of abs(non-zero finite values in bandedCells).
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
		profileStr[si] = viewmodel.FmtCompactVal(rowSum, true, 2)
		callProfile[si] = cSum
		callProfileStr[si] = viewmodel.FmtCompactVal(cSum, true, 2)
		putProfile[si] = pSum
		putProfileStr[si] = viewmodel.FmtCompactVal(pSum, true, 2)
		strikeCallOI[si] = fmtVolCompact(cOISum)
		strikePutOI[si] = fmtVolCompact(pOISum)
		if a := math.Abs(rowSum); a > profileMax {
			profileMax = a
		}
	}

	expLabels := make([]string, nCols)
	expDTEs := make([]string, nCols)
	for idx, exp := range activeExpiries {
		expLabels[idx] = viewmodel.FmtExpiryShort(exp)
		expDTEs[idx] = fmt.Sprintf("%dd", viewmodel.CalcDTE(exp, chosenMoment))
	}

	strikeLabels := make([]string, len(bandedStrikes))
	strikeTags := make([]string, len(bandedStrikes))
	cellsText := make([][]string, len(bandedStrikes))
	cellsNorm := make([][]float64, len(bandedStrikes))
	cellsTip := make([][]string, len(bandedStrikes))

	for si, k := range bandedStrikes {
		strikeLabels[si] = viewmodel.FmtStrikeVal(k)
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

	}
	if strikeRangePct == 0 {
		var nonZeroAbs []float64
		for _, row := range bandedCells {
			for _, v := range row {
				if !math.IsNaN(v) && !math.IsInf(v, 0) && v != 0 {
					nonZeroAbs = append(nonZeroAbs, math.Abs(v))
				}
			}
		}
		vmax := 1.0
		if len(nonZeroAbs) > 0 {
			vmax = percentileLinear(nonZeroAbs, 92.0)
			if vmax <= 0 {
				vmax = 1
			}
		}
		for si, k := range bandedStrikes {
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
					tRow[idx] = viewmodel.FmtCellCompact(val)
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
					viewmodel.FmtStrikeVal(k), expLabels[idx],
					dimensionCN[dimension], viewmodel.FmtCompactVal(valForTip, true, 2),
					viewmodel.FmtCompactVal(cv, true, 2), viewmodel.FmtCompactVal(pv, true, 2),
					formatIntComma(int64(ov.callOI)), formatIntComma(int64(ov.putOI)), formatIntComma(int64(ov.vol)),
				)
			}
			cellsText[si] = tRow
			cellsNorm[si] = nRow
			cellsTip[si] = tipRow
		}
	}
	if err := ctx.Err(); err != nil {
		return viewmodel.WorkbenchResponse{}, err
	}
	var board []viewmodel.ExpiryBoardRow
	if strikeRangePct == 0 {
		// Expiry structure board iterates over shownExpiries.
		board = make([]viewmodel.ExpiryBoardRow, 0, len(shownExpiries))
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
				moveStr = fmt.Sprintf("±%s", viewmodel.FmtPriceVal(mv.Move, 2))
				movePctStr = fmt.Sprintf("±%.2f%%", mv.MovePct)
			}
			pcrStr := "—"
			if cOI > 0 {
				pcrStr = fmt.Sprintf("%.2f", pOI/cOI)
			}
			board = append(board, viewmodel.ExpiryBoardRow{
				Expiry:     viewmodel.FmtExpiryShort(exp),
				ExpiryISO:  exp,
				DTE:        viewmodel.CalcDTE(exp, chosenMoment),
				NetGEX:     netG,
				NetGEXStr:  viewmodel.FmtCompactVal(netG, true, 2),
				CallGEXStr: viewmodel.FmtCompactVal(cGEX, true, 2),
				PutGEXStr:  viewmodel.FmtCompactVal(pGEX, true, 2),
				MaxPain:    viewmodel.FmtStrikeLabel(mp),
				ATMIV:      viewmodel.FmtPct(atmIV, 1, false),
				Move:       moveStr,
				MovePct:    movePctStr,
				CallOI:     formatIntComma(int64(cOI)),
				PutOI:      formatIntComma(int64(pOI)),
				PCR:        pcrStr,
				Contracts:  contractsBackingExpiry(eRows),
			})
		}

	}
	momentOpts := make([]viewmodel.MomentOption, 0, len(moments))
	for _, m := range moments {
		suffix := ""
		if m == newest {
			suffix = " (最新)"
		}
		momentOpts = append(momentOpts, viewmodel.MomentOption{
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

	var intraday viewmodel.IntradaySummary
	if strikeRangePct == 0 {
		key := fmt.Sprintf("intraday/%s/%s/%s/%s/%d", ticker, source, chosenMoment, s.marketEpoch(), barVersion)
		intraday, err = cached(s.cache, ctx, key, func(task context.Context) (viewmodel.IntradaySummary, error) {
			result := s.buildIntradaySummary(task, ticker, source, chosenMoment, snapshotRows)
			return result, task.Err()
		})
		if err != nil {
			return viewmodel.WorkbenchResponse{}, err
		}
	}

	return viewmodel.WorkbenchResponse{
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
		Ribbon:         *ribbon,
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
		GrandTotalStr:  viewmodel.FmtCompactVal(grandTotal, true, 2),
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

func (s *DesktopService) buildVolatilityGo(ctx context.Context, m store.SnapshotMeta, widthPct float64) (viewmodel.VolatilityResponse, error) {
	ticker, source, chosenMoment := m.Ticker, m.Source, m.At
	snapshot, err := s.snapshot(ctx, m)
	if err != nil {
		return viewmodel.VolatilityResponse{}, err
	}
	latestRows := snapshot.rows
	if len(latestRows) == 0 {
		return viewmodel.VolatilityResponse{
			Ticker:    ticker,
			Empty:     true,
			Skew:      []viewmodel.SkewCurve{},
			Term:      []viewmodel.TermPoint{},
			IVHistory: []viewmodel.IVHistoryPoint{},
			RV:        []viewmodel.RVItem{},
		}, nil
	}

	spot := latestRows[0].UnderlyingPrice
	lo := spot * (1.0 - widthPct/100.0)
	hi := spot * (1.0 + widthPct/100.0)

	expRowsMap := make(map[string][]store.StoredOptionRow)
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

	skew := make([]viewmodel.SkewCurve, 0, 5)
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
			dte := viewmodel.CalcDTE(exp, chosenMoment)
			skew = append(skew, viewmodel.SkewCurve{
				Label:   fmt.Sprintf("%s (%dd)", viewmodel.FmtExpiryShort(exp), dte),
				Strikes: strikes,
				IVs:     ivs,
			})
		}
	}

	term := make([]viewmodel.TermPoint, 0, 24)
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
			term = append(term, viewmodel.TermPoint{
				Label:   lbl,
				DTE:     viewmodel.CalcDTE(exp, chosenMoment),
				ATMIV:   mv.ATMIV,
				MovePct: mv.MovePct,
				Move:    mv.Move,
			})
		}
	}

	ivPoints, _ := s.store.IvWeightedAverageSeries(ctx, ticker, source, snapshotHistoryDays())
	ivHist := make([]viewmodel.IVHistoryPoint, 0, len(ivPoints))
	for _, p := range ivPoints {
		ivHist = append(ivHist, viewmodel.IVHistoryPoint{Time: fmtTimeMMDDHHMM(p.At), IV: p.IV})
	}
	rvPoints, _ := s.store.RealizedVolatilityItems(ctx, ticker)
	rvList := make([]viewmodel.RVItem, 0, len(rvPoints))
	for _, p := range rvPoints {
		rvList = append(rvList, viewmodel.RVItem{Window: fmt.Sprintf("RV%dd", p.WindowDays), Value: viewmodel.FmtPctVal(p.Value, 1, false), Raw: p.Value})
	}

	return viewmodel.VolatilityResponse{
		Ticker:    ticker,
		Empty:     false,
		Spot:      spot,
		Skew:      skew,
		Term:      term,
		IVHistory: ivHist,
		RV:        rvList,
	}, nil
}
