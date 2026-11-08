package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"
)

// --- /api/contract in pure Go ---

func medianSorted(vals []float64) float64 {
	n := len(vals)
	if n == 0 {
		return 0
	}
	cp := make([]float64, n)
	copy(cp, vals)
	sort.Float64s(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return 0.5 * (cp[n/2-1] + cp[n/2])
}

// markUnreliableIV matches metrics_core._mark_unreliable_iv on a contract's sorted history.
func markUnreliableIV(rows []store.StoredOptionRow) {
	var validIVs []float64
	var validPrices []float64
	for i := range rows {
		if isValidPos(rows[i].IV) {
			validIVs = append(validIVs, *rows[i].IV)
			if isValidFinite(rows[i].LastPrice) {
				validPrices = append(validPrices, *rows[i].LastPrice)
			}
		}
	}
	if len(validIVs) < ivOutlierMinHistoryPoints {
		return
	}
	refIV := medianSorted(validIVs)
	refPrice := medianSorted(validPrices)
	if !(refIV > 0) || !(refPrice > 0) {
		return
	}
	flags := make([]bool, len(rows))
	flaggedCount := 0
	for i := range rows {
		if !isValidPos(rows[i].IV) {
			continue
		}
		ivDev := math.Abs(*rows[i].IV-refIV) / refIV
		isOutlier := ivDev > ivOutlierRelThreshold
		isCorroborated := false
		if isValidFinite(rows[i].LastPrice) {
			priceDev := math.Abs(*rows[i].LastPrice-refPrice) / refPrice
			isCorroborated = priceDev > ivOutlierPriceCorroborationThreshold
		}
		if isOutlier && !isCorroborated {
			flags[i] = true
			flaggedCount++
		}
	}
	if float64(flaggedCount) > ivOutlierMaxUnreliableFraction*float64(len(validIVs)) {
		return
	}
	for i := range rows {
		if flags[i] {
			rows[i].IV = nil
			rows[i].IVUnreliable = true
		}
	}
}

// lastSnapshotPerDay matches metrics_core._last_snapshot_per_day:
// filters to Monday..Friday in America/New_York and keeps rows at each NY day's max collected_at.
func lastSnapshotPerDay(rows []store.StoredOptionRow) []store.StoredOptionRow {
	if len(rows) == 0 {
		return nil
	}
	maxMomentByDay := make(map[string]string)
	for i := range rows {
		t, ok := marketclock.ParseTime(rows[i].CollectedAt)
		if !ok {
			continue
		}
		ny := t.In(marketclock.Zone)
		wd := ny.Weekday()
		if wd == time.Saturday || wd == time.Sunday {
			continue
		}
		day := ny.Format("2006-01-02")
		if cur, exists := maxMomentByDay[day]; !exists || rows[i].CollectedAt > cur {
			maxMomentByDay[day] = rows[i].CollectedAt
		}
	}
	if len(maxMomentByDay) == 0 {
		return nil
	}
	var out []store.StoredOptionRow
	for i := range rows {
		t, ok := marketclock.ParseTime(rows[i].CollectedAt)
		if !ok {
			continue
		}
		ny := t.In(marketclock.Zone)
		day := ny.Format("2006-01-02")
		if maxMomentByDay[day] == rows[i].CollectedAt {
			out = append(out, rows[i])
		}
	}
	return out
}

type greekAttrTotals struct {
	hasData  bool
	start    float64
	end      float64
	delta    float64
	gamma    float64
	vega     float64
	theta    float64
	residual float64
	actual   float64
}

// computeGreekAttribution matches metrics_core.greek_attribution.
func computeGreekAttribution(history []store.StoredOptionRow) greekAttrTotals {
	daily := lastSnapshotPerDay(history)
	if len(daily) < 2 {
		return greekAttrTotals{}
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].CollectedAt < daily[j].CollectedAt })

	var usable []store.StoredOptionRow
	for i := range daily {
		r := &daily[i]
		if isValidFinite(r.LastPrice) && !(math.IsNaN(r.UnderlyingPrice)) &&
			isValidFinite(r.IV) && isValidFinite(r.Delta) && isValidFinite(r.Gamma) &&
			isValidFinite(r.Vega) && isValidFinite(r.Theta) {
			usable = append(usable, *r)
		}
	}
	if len(usable) < 2 {
		return greekAttrTotals{}
	}

	type cand struct {
		cheap  bool
		start  float64
		end    float64
		actual float64
		delta  float64
		gamma  float64
		vega   float64
		theta  float64
	}
	cands := make([]cand, 0, len(usable)-1)
	for i := 0; i < len(usable)-1; i++ {
		prev := &usable[i]
		curr := &usable[i+1]
		p0 := *prev.LastPrice
		p1 := *curr.LastPrice
		cheap := math.Min(p0, p1) < attributionMinPrice
		t0, _ := marketclock.ParseTime(prev.CollectedAt)
		t1, _ := marketclock.ParseTime(curr.CollectedAt)
		elapsedDays := t1.Sub(t0).Seconds() / 86400.0
		dS := curr.UnderlyingPrice - prev.UnderlyingPrice
		dIV := (*curr.IV - *prev.IV) * 100.0
		cands = append(cands, cand{
			cheap:  cheap,
			start:  p0,
			end:    p1,
			actual: p1 - p0,
			delta:  (*prev.Delta) * dS,
			gamma:  0.5 * (*prev.Gamma) * dS * dS,
			vega:   (*prev.Vega) * dIV,
			theta:  (*prev.Theta) * elapsedDays,
		})
	}

	bestStart, bestLen, runStart, runLen := 0, 0, 0, 0
	for i, c := range cands {
		if c.cheap {
			runLen = 0
			continue
		}
		if runLen == 0 {
			runStart = i
		}
		runLen++
		if runLen > bestLen {
			bestStart = runStart
			bestLen = runLen
		}
	}
	if bestLen == 0 {
		return greekAttrTotals{}
	}
	kept := cands[bestStart : bestStart+bestLen]
	out := greekAttrTotals{
		hasData: true,
		start:   kept[0].start,
		end:     kept[len(kept)-1].end,
	}
	for _, k := range kept {
		res := k.actual - (k.delta + k.gamma + k.vega + k.theta)
		out.delta += k.delta
		out.gamma += k.gamma
		out.vega += k.vega
		out.theta += k.theta
		out.residual += res
		out.actual += k.actual
	}
	return out
}

func (s *DesktopService) buildContractGo(ctx context.Context, m store.SnapshotMeta, q contractQuery) (viewmodel.ContractResponse, error) {
	ticker, source := m.Ticker, m.Source
	expiry, strikeVal, optType := q.Expiry, q.Strike, q.Side
	if optType != "put" {
		optType = "call"
	}

	if expiry == "" || strikeVal <= 0 {
		snapshot, err := s.snapshot(ctx, m)
		if err != nil || len(snapshot.rows) == 0 {
			return viewmodel.ContractResponse{Empty: true}, nil
		}
		latest := snapshot.rows
		spot := latest[0].UnderlyingPrice
		expSet := make(map[string]bool)
		for i := range latest {
			e := latest[i].Expiry
			if len(e) >= 10 {
				e = e[:10]
			}
			expSet[e] = true
		}
		exps := make([]string, 0, len(expSet))
		for e := range expSet {
			exps = append(exps, e)
		}
		sort.Strings(exps)
		expiry = exps[0]
		bestDist := math.Inf(1)
		for i := range latest {
			e := latest[i].Expiry
			if len(e) >= 10 {
				e = e[:10]
			}
			if e == expiry {
				if d := math.Abs(latest[i].Strike - spot); d < bestDist {
					bestDist = d
					strikeVal = latest[i].Strike
				}
			}
		}
	}

	if len(expiry) >= 10 {
		expiry = expiry[:10]
	}
	hist, err := s.store.ContractHistory(ctx, ticker, source, expiry, strikeVal, optType, snapshotHistoryDays())
	if err != nil || len(hist) == 0 {
		return viewmodel.ContractResponse{Empty: true}, nil
	}

	if err := solveSnapshotIV(ctx, hist); err != nil {
		return viewmodel.ContractResponse{}, err
	}
	markUnreliableIV(hist)
	for i := range hist {
		row := &hist[i]
		if row.IVUnreliable || !isValidFinite(row.IV) || !(row.UnderlyingPrice > 0) {
			row.Delta = nil
			row.Gamma = nil
			row.Theta = nil
			row.Vega = nil
			row.Rho = nil
			row.Vanna = nil
			row.Charm = nil
			continue
		}
		t := yearsToExpiry(row.Expiry, row.CollectedAt)
		g := calcBSGreeks(row.UnderlyingPrice, row.Strike, t, defaultRiskFreeRate, *row.IV, row.OptionType, 0.0)
		if g.valid {
			if !isValidFinite(row.Delta) {
				row.Delta = floatPtr(g.delta)
			}
			if !isValidFinite(row.Gamma) {
				row.Gamma = floatPtr(g.gamma)
			}
			if !isValidFinite(row.Theta) {
				row.Theta = floatPtr(g.theta)
			}
			if !isValidFinite(row.Vega) {
				row.Vega = floatPtr(g.vega)
			}
			row.Rho = floatPtr(g.rho)
			row.Vanna = floatPtr(g.vanna)
			row.Charm = floatPtr(g.charm)
		}
	}

	attr := computeGreekAttribution(hist)
	var attrSteps []viewmodel.AttributionStep
	if attr.hasData {
		type item struct {
			key   string
			label string
			val   float64
		}
		for _, it := range []item{
			{"start", "起点", attr.start},
			{"delta", "Delta", attr.delta},
			{"gamma", "Gamma", attr.gamma},
			{"vega", "Vega", attr.vega},
			{"theta", "Theta", attr.theta},
			{"residual", "残差", attr.residual},
			{"end", "终点", attr.end},
		} {
			isTotal := it.key == "start" || it.key == "end"
			cents := it.val * 100.0
			kind := "neg"
			str := ""
			if isTotal {
				kind = "total"
				str = fmt.Sprintf("%.1f¢", cents)
			} else {
				if cents >= 0 {
					kind = "pos"
				}
				str = fmt.Sprintf("%+.1f¢", cents)
			}
			attrSteps = append(attrSteps, viewmodel.AttributionStep{
				Label: it.label,
				Cents: cents,
				Str:   str,
				Kind:  kind,
			})
		}
	} else {
		attrSteps = []viewmodel.AttributionStep{}
	}

	points := make([]viewmodel.ContractPoint, len(hist))
	for i := range hist {
		r := &hist[i]
		points[i] = viewmodel.ContractPoint{
			Time:  fmtTimeMMDDHHMM(r.CollectedAt),
			Price: r.LastPrice,
			IV:    r.IV,
			Delta: r.Delta,
			Gamma: r.Gamma,
			Theta: r.Theta,
			Vega:  r.Vega,
		}
	}

	lastRow := &hist[len(hist)-1]
	expYYMMDD := expiry
	if len(expiry) == 10 {
		expYYMMDD = expiry[2:4] + expiry[5:7] + expiry[8:10]
	}
	name := fmt.Sprintf("%s %s %s%s", ticker, expYYMMDD, viewmodel.FmtStrikeVal(strikeVal), strings.ToUpper(optType[:1]))
	deltaStr, gammaStr, thetaStr, vegaStr := "—", "—", "—", "—"
	if isValidFinite(lastRow.Delta) {
		deltaStr = fmt.Sprintf("%+.3f", *lastRow.Delta)
	}
	if isValidFinite(lastRow.Gamma) {
		gammaStr = fmt.Sprintf("%.4f", *lastRow.Gamma)
	}
	if isValidFinite(lastRow.Theta) {
		thetaStr = fmt.Sprintf("%+.3f", *lastRow.Theta)
	}
	if isValidFinite(lastRow.Vega) {
		vegaStr = fmt.Sprintf("%.3f", *lastRow.Vega)
	}

	return viewmodel.ContractResponse{
		Empty:       false,
		Name:        name,
		ExpiryISO:   expiry,
		Strike:      strikeVal,
		Type:        optType,
		PriceStr:    viewmodel.FmtPrice(lastRow.LastPrice, 2),
		IVStr:       viewmodel.FmtPct(lastRow.IV, 1, false),
		DeltaStr:    deltaStr,
		GammaStr:    gammaStr,
		ThetaStr:    thetaStr,
		VegaStr:     vegaStr,
		Samples:     len(hist),
		Attribution: attrSteps,
		Points:      points,
	}, nil
}

// --- /api/flow in pure Go ---

func computeUnusualActivity(latest []store.StoredOptionRow, stats map[store.ContractID]store.VolumeStat) []viewmodel.UnusualRow {
	type flaggedItem struct {
		row    *store.StoredOptionRow
		avgVol *float64
		zscore *float64
	}
	var flagged []flaggedItem
	for i := range latest {
		r := &latest[i]
		if !isValidFinite(r.Volume) || *r.Volume < unusualMinVolume {
			continue
		}
		vol := *r.Volume
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		st := stats[store.ContractID{Expiry: exp, Strike: r.Strike, Side: r.OptionType}]
		var z *float64
		if st.AvgVolume != nil && st.StdVolume != nil && *st.StdVolume != 0 {
			zv := (vol - *st.AvgVolume) / (*st.StdVolume)
			if !math.IsNaN(zv) && !math.IsInf(zv, 0) {
				z = &zv
			}
		}
		hasEnough := st.HistoryPoints >= unusualMinHistoryPoints
		zFlag := hasEnough && z != nil && *z > unusualZThreshold
		oiClip := math.Max(valOrZero(r.OI), 1.0)
		fallbackFlag := !hasEnough && (vol > 2.0*oiClip)
		if zFlag || fallbackFlag {
			flagged = append(flagged, flaggedItem{row: r, avgVol: st.AvgVolume, zscore: z})
		}
	}
	sort.SliceStable(flagged, func(i, j int) bool {
		zi, zj := flagged[i].zscore, flagged[j].zscore
		if zi == nil && zj == nil {
			return false
		}
		if zi == nil {
			return false
		}
		if zj == nil {
			return true
		}
		return *zi > *zj
	})
	if len(flagged) > 150 {
		flagged = flagged[:150]
	}
	out := make([]viewmodel.UnusualRow, len(flagged))
	for i, f := range flagged {
		r := f.row
		vol := valOrZero(r.Volume)
		oi := valOrZero(r.OI)
		zStr := "—"
		if f.zscore != nil {
			zStr = fmt.Sprintf("%.1f", *f.zscore)
		}
		avgStr := "—"
		if f.avgVol != nil && !math.IsNaN(*f.avgVol) {
			avgStr = formatIntComma(int64(math.Round(*f.avgVol)))
		}
		volOI := "—"
		if oi > 0 {
			volOI = fmt.Sprintf("%.2fx", vol/oi)
		}
		out[i] = viewmodel.UnusualRow{
			Expiry: viewmodel.FmtExpiryShort(r.Expiry),
			Strike: viewmodel.FmtStrikeVal(r.Strike),
			Type:   strings.ToUpper(r.OptionType[:1]),
			Volume: formatIntComma(int64(vol)),
			OI:     formatIntComma(int64(oi)),
			AvgVol: avgStr,
			ZScore: zStr,
			VolOI:  volOI,
		}
	}
	return out
}

func computeOIDelta(twoDaysRows []store.StoredOptionRow) []viewmodel.OIDeltaRow {
	daily := lastSnapshotPerDay(twoDaysRows)
	if len(daily) == 0 {
		return []viewmodel.OIDeltaRow{}
	}
	momentSet := make(map[string]bool)
	for i := range daily {
		momentSet[daily[i].CollectedAt] = true
	}
	moments := make([]string, 0, len(momentSet))
	for m := range momentSet {
		moments = append(moments, m)
	}
	sort.Strings(moments)
	if len(moments) < 2 {
		return []viewmodel.OIDeltaRow{}
	}
	latestMoment := moments[len(moments)-1]
	prevMoment := moments[len(moments)-2]

	latestOI := make(map[store.ContractID]float64)
	prevOI := make(map[store.ContractID]float64)
	var keysOrder []store.ContractID
	seenKey := make(map[store.ContractID]bool)
	for i := range daily {
		r := &daily[i]
		if !isValidFinite(r.OI) {
			continue
		}
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		cid := store.ContractID{Expiry: exp, Strike: r.Strike, Side: r.OptionType}
		if r.CollectedAt == latestMoment {
			latestOI[cid] = *r.OI
			if !seenKey[cid] {
				seenKey[cid] = true
				keysOrder = append(keysOrder, cid)
			}
		} else if r.CollectedAt == prevMoment {
			prevOI[cid] = *r.OI
		}
	}

	type deltaItem struct {
		cid    store.ContractID
		oi     float64
		delta  float64
		pct    *float64
		absDel float64
	}
	var items []deltaItem
	for _, cid := range keysOrder {
		curr, ok1 := latestOI[cid]
		prev, ok2 := prevOI[cid]
		if !ok1 || !ok2 {
			continue
		}
		d := curr - prev
		var pct *float64
		if prev != 0 {
			p := (d / prev) * 100.0
			pct = &p
		}
		items = append(items, deltaItem{
			cid:    cid,
			oi:     curr,
			delta:  d,
			pct:    pct,
			absDel: math.Abs(d),
		})
	}
	sort.SliceStable(items, func(i, j int) bool {
		return items[i].absDel > items[j].absDel
	})
	if len(items) > 150 {
		items = items[:150]
	}
	out := make([]viewmodel.OIDeltaRow, len(items))
	for i, it := range items {
		dInt := int(it.delta)
		pctStr := "—"
		if it.pct != nil && !math.IsNaN(*it.pct) && !math.IsInf(*it.pct, 0) {
			pctStr = fmt.Sprintf("%+.1f%%", *it.pct)
		}
		deltaStr := formatIntComma(int64(dInt))
		if dInt >= 0 {
			deltaStr = "+" + deltaStr
		}
		out[i] = viewmodel.OIDeltaRow{
			Expiry:     viewmodel.FmtExpiryShort(it.cid.Expiry),
			Strike:     viewmodel.FmtStrikeVal(it.cid.Strike),
			Type:       strings.ToUpper(it.cid.Side[:1]),
			OI:         formatIntComma(int64(it.oi)),
			OIDelta:    dInt,
			OIDeltaStr: deltaStr,
			OIPctStr:   pctStr,
		}
	}
	return out
}

func strikeStepOf(r *store.DaySummaryRecord) *float64 {
	if r == nil || len(r.GEXByStrike) < 2 {
		return nil
	}
	strikes := make([]float64, len(r.GEXByStrike))
	for i, pair := range r.GEXByStrike {
		strikes[i] = pair[0]
	}
	sort.Float64s(strikes)
	best := math.Inf(1)
	for i := 0; i < len(strikes)-1; i++ {
		if gap := strikes[i+1] - strikes[i]; gap > 1e-9 && gap < best {
			best = gap
		}
	}
	if math.IsInf(best, 1) {
		return nil
	}
	return &best
}

var weatherWordsEN = map[string]string{
	weatherClear:     "Clear",
	weatherFair:      "Fair",
	weatherUnsettled: "Unsettled",
	weatherRain:      "Showers",
	weatherStorm:     "Storm",
}

var stateOrderIndex = map[string]int{
	weatherClear:     0,
	weatherFair:      1,
	weatherUnsettled: 2,
	weatherRain:      3,
	weatherStorm:     4,
}

func formatGEXWeather(v float64, signed bool) string {
	mag := math.Abs(v)
	type unit struct {
		limit  float64
		suffix string
	}
	for _, u := range []unit{{1e9, "B"}, {1e6, "M"}, {1e3, "K"}} {
		if mag >= u.limit {
			if signed {
				return fmt.Sprintf("%+.2f%s", v/u.limit, u.suffix)
			}
			return fmt.Sprintf("%.2f%s", v/u.limit, u.suffix)
		}
	}
	if signed {
		return fmt.Sprintf("%+.0f", v)
	}
	return fmt.Sprintf("%.0f", v)
}

func formatIntComma(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		var b strings.Builder
		rem := len(s) % 3
		if rem > 0 {
			b.WriteString(s[:rem])
			b.WriteByte(',')
		}
		for i := rem; i < len(s); i += 3 {
			if i > rem {
				b.WriteByte(',')
			}
			b.WriteString(s[i : i+3])
		}
		s = b.String()
	}
	if neg {
		return "-" + s
	}
	return s
}

func isZeroPrinted(printed string) bool {
	first := strings.Split(printed, " ")[0]
	if first == "" {
		return false
	}
	for _, ch := range first {
		if !strings.ContainsRune("+-0.,%", ch) {
			return false
		}
	}
	return true
}

// buildChangesLadder matches day_summary.changes + format_value + format_delta.
func buildChangesLadder(prev, curr *store.DaySummaryRecord, prevIV, currIV *float64) []viewmodel.LadderRow {
	if curr == nil {
		return []viewmodel.LadderRow{}
	}
	step := strikeStepOf(curr)
	if step == nil {
		step = strikeStepOf(prev)
	}

	type rawLine struct {
		label  string
		unit   string
		before *float64
		after  *float64
		bState string
		aState string
		bBand  *[2]float64
		aBand  *[2]float64
		kind   string
		delta  *float64
	}

	var lines []rawLine

	// 1. Gamma weather
	bState := ""
	if prev != nil {
		bState = prev.State
	}
	aState := curr.State
	wKind := "unknown"
	if bState != "" && aState != "" {
		bi, ok1 := stateOrderIndex[bState]
		ai, ok2 := stateOrderIndex[aState]
		if ok1 && ok2 {
			if ai == bi {
				wKind = "same"
			} else if ai > bi {
				wKind = "amplifying"
			} else {
				wKind = "damping"
			}
		}
	}
	lines = append(lines, rawLine{label: "Gamma weather", unit: "state", bState: bState, aState: aState, kind: wKind})

	addNum := func(label, unit string, b, a *float64, flipDamping bool) {
		k := "unknown"
		var d *float64
		if b == nil && a == nil {
			k = "unknown"
		} else if b == nil {
			k = "new"
		} else if a == nil {
			k = "gone"
		} else {
			dv := *a - *b
			d = &dv
			if math.Abs(dv) < 1e-9 {
				k = "same"
			} else {
				k = "moved"
				if flipDamping && ((*b < 0) != (*a < 0)) {
					if *a >= 0 {
						k = "damping"
					} else {
						k = "amplifying"
					}
				}
			}
		}
		lines = append(lines, rawLine{label: label, unit: unit, before: b, after: a, kind: k, delta: d})
	}

	var prevSpot, prevNetGEX, prevCW, prevPW, prevGF *float64
	if prev != nil {
		prevSpot = &prev.Spot
		prevNetGEX = &prev.NetGEX
		prevCW = prev.CallWall
		prevPW = prev.PutWall
		prevGF = prev.GammaFlip
	}
	addNum("Price", "price", prevSpot, &curr.Spot, false)
	addNum("Net GEX", "gex", prevNetGEX, &curr.NetGEX, true)
	addNum("Call wall", "strikes", prevCW, curr.CallWall, false)
	addNum("Put wall", "strikes", prevPW, curr.PutWall, false)
	addNum("Gamma flip", "price", prevGF, curr.GammaFlip, false)

	// Max pain & Expected move
	pExp := ""
	if prev != nil {
		pExp = prev.NearestExpiry
	}
	cExp := curr.NearestExpiry
	sameExp := pExp != "" && cExp != "" && pExp == cExp
	var painBefore *float64
	if sameExp || pExp == "" {
		if prev != nil {
			painBefore = prev.NearestMaxPain
		}
	}
	mpLabel := "Max pain"
	if cExp != "" {
		mpLabel = "Max pain · " + cExp
	}
	addNum(mpLabel, "strikes", painBefore, curr.NearestMaxPain, false)
	if prev != nil && pExp != "" && !sameExp && painBefore == nil {
		if cExp != "" {
			lines[len(lines)-1].kind = "new"
		} else {
			lines[len(lines)-1].kind = "gone"
		}
	}

	var bBand, aBand *[2]float64
	if sameExp && prev != nil && prev.MoveLower != nil && prev.MoveUpper != nil {
		bBand = &[2]float64{*prev.MoveLower, *prev.MoveUpper}
	}
	if curr.MoveLower != nil && curr.MoveUpper != nil {
		aBand = &[2]float64{*curr.MoveLower, *curr.MoveUpper}
	}
	emLabel := "Expected move"
	if cExp != "" {
		emLabel = "Expected move · " + cExp
	}
	emKind := "unknown"
	var emDelta *float64
	if bBand != nil && aBand != nil {
		dv := (aBand[1] - aBand[0]) - (bBand[1] - bBand[0])
		emDelta = &dv
		if math.Abs(dv) < 1e-9 {
			emKind = "same"
		} else {
			emKind = "moved"
		}
	}
	lines = append(lines, rawLine{label: emLabel, unit: "band", bBand: bBand, aBand: aBand, kind: emKind, delta: emDelta})

	ratioPtr := func(num, den *int64) *float64 {
		if num == nil || den == nil || *den == 0 {
			return nil
		}
		v := float64(*num) / float64(*den)
		return &v
	}
	intToFloatPtr := func(p *int64) *float64 {
		if p == nil {
			return nil
		}
		v := float64(*p)
		return &v
	}
	var pPCRVol, pPCROI, pCallOI, pPutOI, pExps, pContracts *float64
	if prev != nil {
		pPCRVol = ratioPtr(prev.PutVolume, prev.CallVolume)
		pPCROI = ratioPtr(prev.PutOI, prev.CallOI)
		pCallOI = intToFloatPtr(prev.CallOI)
		pPutOI = intToFloatPtr(prev.PutOI)
		eCnt := float64(len(prev.Expiries))
		pExps = &eCnt
		cCnt := float64(prev.Contracts)
		pContracts = &cCnt
	}
	cPCRVol := ratioPtr(curr.PutVolume, curr.CallVolume)
	cPCROI := ratioPtr(curr.PutOI, curr.CallOI)
	cCallOI := intToFloatPtr(curr.CallOI)
	cPutOI := intToFloatPtr(curr.PutOI)
	cExps := float64(len(curr.Expiries))
	cContracts := float64(curr.Contracts)

	addNum("Put/Call · volume", "ratio", pPCRVol, cPCRVol, false)
	addNum("Put/Call · open interest", "ratio", pPCROI, cPCROI, false)
	addNum("IV · volume-weighted", "pts", prevIV, currIV, false)
	addNum("Open interest · calls", "pct", pCallOI, cCallOI, false)
	addNum("Open interest · puts", "pct", pPutOI, cPutOI, false)
	addNum("Expiries listed", "count", pExps, &cExps, false)
	addNum("Contracts in the chain", "count", pContracts, &cContracts, false)

	if prev == nil {
		for i := range lines {
			lines[i].kind = "unknown"
		}
	}

	fmtSide := func(l rawLine, isBefore bool) string {
		switch l.unit {
		case "state":
			st := l.aState
			if isBefore {
				st = l.bState
			}
			if st == "" {
				return "—"
			}
			if pair, ok := weatherCN[st]; ok {
				return pair[0]
			}
			return st
		case "band":
			bd := l.aBand
			if isBefore {
				bd = l.bBand
			}
			if bd == nil {
				return "—"
			}
			return fmt.Sprintf("%s – %s", viewmodel.FmtPriceVal(bd[0], 2), viewmodel.FmtPriceVal(bd[1], 2))
		default:
			v := l.after
			if isBefore {
				v = l.before
			}
			if v == nil {
				return "—"
			}
			switch l.unit {
			case "gex":
				return formatGEXWeather(*v, false)
			case "pts":
				return fmt.Sprintf("%.1f%%", *v*100.0)
			case "count", "pct":
				return formatIntComma(int64(math.Round(*v)))
			case "ratio":
				return fmt.Sprintf("%.2f", *v)
			default:
				return viewmodel.FmtPriceVal(*v, 2)
			}
		}
	}

	fmtDelta := func(l rawLine) string {
		switch l.kind {
		case "unknown":
			return "—"
		case "gone":
			return "消失"
		case "new":
			return "新增"
		case "same":
			return "不变"
		}
		if l.unit == "state" {
			if l.kind == "amplifying" {
				return "→ 放大"
			}
			return "→ 压制"
		}
		if l.delta == nil {
			return ""
		}
		d := *l.delta
		var printed string
		switch l.unit {
		case "gex":
			printed = formatGEXWeather(d, true)
		case "pts":
			printed = fmt.Sprintf("%+.1f pts", d*100.0)
		case "strikes":
			if step != nil && *step > 0 {
				st := math.Round((d/(*step))*100) / 100
				plural := "s"
				if math.Abs(st) == 1 {
					plural = ""
				}
				sign := "+"
				if st < 0 {
					sign = "-"
				}
				printed = fmt.Sprintf("%s%s strike%s · %s", sign, viewmodel.FmtStrikeVal(math.Abs(st)), plural, fmtSignedPrice(d, 2))
			} else {
				printed = fmtSignedPrice(d, 2)
			}
		case "band":
			printed = fmt.Sprintf("%s wide", fmtSignedPrice(d, 2))
		case "count":
			n := int64(math.Round(d))
			if n >= 0 {
				printed = "+" + formatIntComma(n)
			} else {
				printed = formatIntComma(n)
			}
		case "pct":
			if l.before != nil && *l.before != 0 {
				pct := d / math.Abs(*l.before) * 100.0
				printed = fmt.Sprintf("%+.1f%%", pct)
			} else {
				printed = fmtSignedPrice(d, 2)
			}
		case "price":
			if l.before != nil && *l.before != 0 {
				pct := d / math.Abs(*l.before) * 100.0
				printed = fmt.Sprintf("%s (%+.1f%%)", fmtSignedPrice(d, 2), pct)
			} else {
				printed = fmtSignedPrice(d, 2)
			}
		default:
			printed = fmtSignedPrice(d, 2)
		}
		if isZeroPrinted(printed) {
			return "不变"
		}
		return printed
	}

	labelsCN := map[string]string{
		"Gamma weather":            "Gamma 状态",
		"Price":                    "现价",
		"Net GEX":                  "净 GEX",
		"Call wall":                "Call OI 墙",
		"Put wall":                 "Put OI 墙",
		"Gamma flip":               "Gamma 翻转",
		"Put/Call · volume":        "P/C 成交量",
		"Put/Call · open interest": "P/C 持仓",
		"IV · volume-weighted":     "IV 加权",
		"Open interest · calls":    "Call 持仓",
		"Open interest · puts":     "Put 持仓",
		"Expiries listed":          "挂牌到期日",
		"Contracts in the chain":   "合约数",
	}

	out := make([]viewmodel.LadderRow, len(lines))
	for i, l := range lines {
		lbl := l.label
		if strings.HasPrefix(lbl, "Max pain") {
			lbl = "Max Pain"
		} else if strings.HasPrefix(lbl, "Expected move") {
			lbl = "±1σ 区间"
		} else if cn, ok := labelsCN[lbl]; ok {
			lbl = cn
		}
		tone := ""
		if l.kind == "damping" {
			tone = "pos"
		} else if l.kind == "amplifying" {
			tone = "neg"
		}
		out[i] = viewmodel.LadderRow{
			Label:  lbl,
			Before: fmtSide(l, true),
			After:  fmtSide(l, false),
			Delta:  fmtDelta(l),
			Tone:   tone,
		}
	}
	return out
}

func fmtSignedPrice(val float64, digits int) string {
	s := viewmodel.FmtPriceVal(math.Abs(val), digits)
	if val < 0 {
		return "-" + s
	}
	return "+" + s
}

func fmtDayMMDD(iso string) string {
	if len(iso) >= 10 {
		return iso[5:10]
	}
	return iso
}

func (s *DesktopService) buildFlowGo(ctx context.Context, m store.SnapshotMeta) (viewmodel.FlowResponse, error) {
	ticker, source := m.Ticker, m.Source
	snapshot, err := s.snapshot(ctx, m)
	if err != nil {
		return viewmodel.FlowResponse{}, err
	}
	latest := snapshot.rows
	if len(latest) == 0 {
		return viewmodel.FlowResponse{
			Ticker:     ticker,
			Empty:      true,
			Unusual:    []viewmodel.UnusualRow{},
			OIDelta:    []viewmodel.OIDeltaRow{},
			Ladder:     []viewmodel.LadderRow{},
			PCRHistory: []viewmodel.PCRPoint{},
		}, nil
	}
	spot := latest[0].UnderlyingPrice

	statsMap, _ := s.store.VolumeStatsMap(ctx, ticker, source)
	unusualRows := computeUnusualActivity(latest, statsMap)

	dailyMoments, _ := s.store.CollectionMoments(ctx, ticker, source, snapshotHistoryDays(), true)
	var oiRows []viewmodel.OIDeltaRow
	if len(dailyMoments) >= 2 {
		twoDays, err := s.store.SnapshotsAt(ctx, ticker, source, dailyMoments[:2])
		if err == nil {
			oiRows = computeOIDelta(twoDays)
		}
	}
	if oiRows == nil {
		oiRows = []viewmodel.OIDeltaRow{}
	}

	days, _ := s.store.SessionDays(ctx, ticker, source, 400)
	ladder := []viewmodel.LadderRow{}
	daysLabel := ""
	if len(days) >= 1 {
		dayAfter := days[0]
		dayBefore := days[0]
		if len(days) >= 2 {
			dayBefore = days[1]
		}
		daysLabel = fmt.Sprintf("%s → %s", fmtDayMMDD(dayBefore), fmtDayMMDD(dayAfter))
		after, err1 := s.store.DaySummaryOn(ctx, ticker, source, dayAfter)
		var before *store.DaySummaryRecord
		if dayBefore != dayAfter {
			before, _ = s.store.DaySummaryOn(ctx, ticker, source, dayBefore)
		}
		if err1 == nil && after != nil {
			ivAfter := s.store.MomentRollupIV(ctx, ticker, source, after.CollectedAt)
			var ivBefore *float64
			if before != nil {
				ivBefore = s.store.MomentRollupIV(ctx, ticker, source, before.CollectedAt)
			}
			ladder = buildChangesLadder(before, after, ivBefore, ivAfter)
		}
	}

	pcrPoints, _ := s.store.PutCallRatioSeries(ctx, ticker, source, snapshotHistoryDays())
	pcrHist := make([]viewmodel.PCRPoint, 0, len(pcrPoints))
	for _, p := range pcrPoints {
		pcrHist = append(pcrHist, viewmodel.PCRPoint{Time: fmtTimeMMDDHHMM(p.Time), PCRVol: p.PCRVol, PCROI: p.PCROI})
	}
	return viewmodel.FlowResponse{
		Ticker:     ticker,
		Empty:      false,
		Spot:       spot,
		Unusual:    unusualRows,
		OIDelta:    oiRows,
		DaysCount:  len(days),
		DaysLabel:  daysLabel,
		Ladder:     ladder,
		PCRHistory: pcrHist,
	}, nil
}

// --- Pure Go derive (replaces /compute/derive) ---

func buildDaySummaryRecord(ctx context.Context, rows []store.StoredOptionRow, r, q float64) *store.DaySummaryRecord {
	if len(rows) == 0 {
		return nil
	}
	w := computeGammaWeatherPresolved(ctx, rows, r, q)
	if w == nil {
		return nil
	}
	if !marketclock.SessionStarted(w.CollectedAt) {
		return nil
	}
	expMap := make(map[string][]store.StoredOptionRow)
	for i := range rows {
		e := rows[i].Expiry
		if len(e) >= 10 {
			e = e[:10]
		}
		expMap[e] = append(expMap[e], rows[i])
	}
	expiries := make([]string, 0, len(expMap))
	for e := range expMap {
		expiries = append(expiries, e)
	}
	sort.Strings(expiries)

	nearest := ""
	for _, e := range expiries {
		if yearsToExpiry(e, w.CollectedAt) <= 0 {
			continue
		}
		if contractsBackingExpiry(expMap[e]) < minContractsForExpiryMetrics {
			continue
		}
		nearest = e
		break
	}
	var mp, moveLower, moveUpper *float64
	if nearest != "" {
		eRows := expMap[nearest]
		mp = maxPain(eRows)
		if mv := expectedMovePresolved(eRows, w.Spot); mv != nil {
			moveLower = floatPtr(mv.Lower)
			moveUpper = floatPtr(mv.Upper)
		}
	}
	var cVol, pVol, cOI, pOI float64
	for i := range rows {
		row := &rows[i]
		if row.OptionType == "call" {
			cVol += valOrZero(row.Volume)
			cOI += valOrZero(row.OI)
		} else if row.OptionType == "put" {
			pVol += valOrZero(row.Volume)
			pOI += valOrZero(row.OI)
		}
	}
	cvInt := int64(cVol)
	pvInt := int64(pVol)
	coiInt := int64(cOI)
	poiInt := int64(pOI)
	return &store.DaySummaryRecord{
		Day:            marketclock.MarketDay(w.CollectedAt),
		CollectedAt:    w.CollectedAt,
		Spot:           w.Spot,
		NetGEX:         w.NetGEX,
		CallWall:       w.CallWall,
		PutWall:        w.PutWall,
		GammaFlip:      w.GammaFlip,
		State:          w.State,
		ExpiryCount:    w.ExpiryCount,
		HorizonDTE:     w.HorizonDTE,
		NearestExpiry:  nearest,
		NearestMaxPain: mp,
		MoveLower:      moveLower,
		MoveUpper:      moveUpper,
		Contracts:      len(rows),
		CallVolume:     &cvInt,
		PutVolume:      &pvInt,
		CallOI:         &coiInt,
		PutOI:          &poiInt,
		Expiries:       expiries,
		GEXByStrike:    w.GEXByStrike,
	}
}

func newDerivedWrite(sqlStmt string, args ...any) store.DerivedWrite {
	return store.DerivedWrite{SQL: sqlStmt, Args: args}
}

func (s *DesktopService) deriveGo(ctx context.Context, ticker, source string, latestOnly bool) error {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	if source == "" {
		var err error
		source, err = s.store.ActiveSource(ctx, tkr)
		if err != nil {
			return err
		}
	}
	var writes []store.DerivedWrite

	// 1. Backfill pending snapshot_iv_summary rows (up to 50 newest moments).
	wherePending := "ticker = ? AND iv_model IS NULL"
	argsPending := []any{tkr}
	if source != "" {
		wherePending += " AND source = ?"
		argsPending = append(argsPending, source)
	}
	pendingLimit := ivBackfillMomentsPerPass
	if latestOnly {
		pendingLimit = 1
	}
	pRows, err := s.store.Read().QueryContext(ctx,
		"SELECT CAST(collected_at AS TEXT) FROM snapshot_iv_summary WHERE "+wherePending+" ORDER BY collected_at DESC LIMIT ?",
		append(argsPending, pendingLimit)...)
	if err == nil {
		var pendingMoments []string
		for pRows.Next() {
			var m string
			if err := pRows.Scan(&m); err == nil {
				pendingMoments = append(pendingMoments, m)
			}
		}
		pRows.Close()
		modelName := "own"
		if source == "cboe" {
			modelName = "cboe_native"
		}
		for i, m := range pendingMoments {
			if err := ctx.Err(); err != nil {
				return err
			}
			snap, err := s.snapshot(ctx, store.SnapshotMeta{Ticker: tkr, Source: source, At: m})
			if err != nil {
				return err
			}
			if i == 0 && source != "" && len(snap.rows) > 0 {
				meta := store.SnapshotMeta{Ticker: tkr, Source: source, At: m}
				ribbon, err := s.snapshotRibbon(ctx, meta, snap.rows)
				if err != nil {
					return err
				}
				payload, err := json.Marshal(ribbon)
				if err != nil {
					return err
				}
				writes = append(writes, newDerivedWrite(
					"UPDATE snapshot_iv_summary SET ribbon_json=?,ribbon_model=? WHERE ticker=? AND source=? AND collected_at=?",
					string(payload), ModelVersion, tkr, source, m,
				))
			}
			avg := ivWeightedAveragePresolved(snap.rows)
			if source != "" {
				writes = append(writes, newDerivedWrite(
					"UPDATE snapshot_iv_summary SET iv_weighted_avg=COALESCE(?, iv_weighted_avg), iv_model=? WHERE ticker=? AND source=? AND collected_at=?",
					avg, modelName, tkr, source, m,
				))
			} else {
				writes = append(writes, newDerivedWrite(
					"UPDATE snapshot_iv_summary SET iv_weighted_avg=COALESCE(?, iv_weighted_avg), iv_model=? WHERE ticker=? AND collected_at=?",
					avg, modelName, tkr, m,
				))
			}
		}
	}

	// 2. Backfill missing ticker_day_summary rows.
	if source != "" {
		modelVersion := ModelVersion
		moments, err := s.store.CollectionMoments(ctx, tkr, source, daySummaryBackfillDays, true)
		if err == nil {
			if latestOnly && len(moments) > 1 {
				moments = moments[:1]
			}
			for _, m := range moments {
				if err := ctx.Err(); err != nil {
					return err
				}
				day := marketclock.MarketDay(m)
				var existingAt, existingSHA string
				err := s.store.Read().QueryRowContext(ctx,
					"SELECT COALESCE(CAST(collected_at AS TEXT),''), COALESCE(core_sha,'') FROM ticker_day_summary WHERE ticker=? AND source=? AND substr(CAST(day AS TEXT),1,10)=?",
					tkr, source, day,
				).Scan(&existingAt, &existingSHA)
				if err != nil || existingSHA != modelVersion || existingAt < m {
					snap, err := s.snapshot(ctx, store.SnapshotMeta{Ticker: tkr, Source: source, At: m})
					if err == nil && len(snap.rows) > 0 {
						if rec := buildDaySummaryRecord(ctx, snap.rows, defaultRiskFreeRate, 0.0); rec != nil {
							expJSON, _ := json.Marshal(rec.Expiries)
							gexJSON, _ := json.Marshal(rec.GEXByStrike)
							var nearExp any
							if rec.NearestExpiry != "" {
								nearExp = rec.NearestExpiry
							}
							writes = append(writes, newDerivedWrite(
								`INSERT INTO ticker_day_summary
								(ticker,source,day,collected_at,underlying_price,net_gex,call_wall,put_wall,gamma_flip,state,
								 expiry_count,horizon_dte,nearest_expiry,nearest_max_pain,move_lower,move_upper,contracts,
								 call_volume,put_volume,call_oi,put_oi,expiries,gex_by_strike,computed_at,core_sha)
								VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,?)
								ON CONFLICT (ticker,source,day) DO UPDATE SET
								collected_at=EXCLUDED.collected_at, underlying_price=EXCLUDED.underlying_price, net_gex=EXCLUDED.net_gex,
								call_wall=EXCLUDED.call_wall, put_wall=EXCLUDED.put_wall, gamma_flip=EXCLUDED.gamma_flip, state=EXCLUDED.state,
								expiry_count=EXCLUDED.expiry_count, horizon_dte=EXCLUDED.horizon_dte, nearest_expiry=EXCLUDED.nearest_expiry,
								nearest_max_pain=EXCLUDED.nearest_max_pain, move_lower=EXCLUDED.move_lower, move_upper=EXCLUDED.move_upper,
								contracts=EXCLUDED.contracts, call_volume=EXCLUDED.call_volume, put_volume=EXCLUDED.put_volume,
								call_oi=EXCLUDED.call_oi, put_oi=EXCLUDED.put_oi, expiries=EXCLUDED.expiries,
								gex_by_strike=EXCLUDED.gex_by_strike, computed_at=EXCLUDED.computed_at, core_sha=EXCLUDED.core_sha
								WHERE EXCLUDED.collected_at >= ticker_day_summary.collected_at`,
								tkr, source, rec.Day, rec.CollectedAt, rec.Spot, rec.NetGEX,
								rec.CallWall, rec.PutWall, rec.GammaFlip, rec.State,
								rec.ExpiryCount, rec.HorizonDTE, nearExp, rec.NearestMaxPain,
								rec.MoveLower, rec.MoveUpper, rec.Contracts,
								rec.CallVolume, rec.PutVolume, rec.CallOI, rec.PutOI,
								string(expJSON), string(gexJSON), modelVersion,
							))
						}
					}
				}
			}
		}
	}

	if latestOnly {
		return s.applyDerived(ctx, tkr, writes)
	}

	// 3. Rebuild contract_volume_stats when stale (through_day < lastCompletedTradingDay).
	lastCompleted := marketclock.LastCompletedTradingDay(s.now())
	var maxThrough sql.NullString
	if source != "" {
		_ = s.store.Read().QueryRowContext(ctx,
			"SELECT max(substr(through_day,1,10)) FROM contract_volume_stats WHERE ticker=? AND source=?",
			tkr, source).Scan(&maxThrough)
	} else {
		_ = s.store.Read().QueryRowContext(ctx,
			"SELECT max(substr(through_day,1,10)) FROM contract_volume_stats WHERE ticker=?",
			tkr).Scan(&maxThrough)
	}
	if !maxThrough.Valid || maxThrough.String < lastCompleted {
		todayNY := s.now().In(marketclock.Zone).Format("2006-01-02")
		allDaily, err := s.store.CollectionMoments(ctx, tkr, source, unusualHistoryDays, true)
		if err == nil {
			var priorMoments []string
			for _, m := range allDaily {
				if marketclock.MarketDay(m) < todayNY {
					priorMoments = append(priorMoments, m)
				}
			}
			if len(priorMoments) > 0 {
				volWrites, err := s.store.ComputeVolumeStatsWrites(ctx, tkr, source, priorMoments)
				if err == nil {
					writes = append(writes, volWrites...)
				}
			}
		}
	}

	// 4. Optional realized volatility (when GEX_FETCH_RV=1).
	if os.Getenv("GEX_FETCH_RV") == "1" {
		var hasRV int
		_ = s.store.Read().QueryRowContext(ctx,
			"SELECT count(*) FROM realized_volatility WHERE ticker=? AND substr(as_of_date,1,10)=?",
			tkr, lastCompleted).Scan(&hasRV)
		if hasRV == 0 {
			if values, err := s.yahoo.RealizedVolatility(ctx, tkr); err == nil {
				for _, window := range []int{10, 20, 30} {
					if value, ok := values[window]; ok {
						writes = append(writes, newDerivedWrite(`INSERT INTO realized_volatility
                            (ticker,as_of_date,window_days,value,source) VALUES (?,?,?,?,?)
                            ON CONFLICT(ticker,as_of_date,window_days) DO UPDATE SET
                            value=excluded.value,source=excluded.source,fetched_at=CURRENT_TIMESTAMP`,
							tkr, lastCompleted, window, value, "yahoo"))
					}
				}
			}
		}
	}

	return s.applyDerived(ctx, tkr, writes)
}

func (s *DesktopService) applyDerived(ctx context.Context, ticker string, writes []store.DerivedWrite) error {
	if err := s.store.ApplyDerived(ctx, writes); err != nil {
		return err
	}
	if len(writes) > 0 {
		s.historyChanged(ticker)
	}
	return nil
}
