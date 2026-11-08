package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// --- /api/contract in pure Go ---

func (s *Store) contractHistory(ctx context.Context, ticker, source, expiry string, strike float64, optType string, days int) ([]StoredOptionRow, error) {
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
		ORDER BY collected_at`, snapshotSelectCols, where, snapshotSelectCols, where)
	rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanStoredOptionRows(rows)
}

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
func markUnreliableIV(rows []StoredOptionRow) {
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
func lastSnapshotPerDay(rows []StoredOptionRow) []StoredOptionRow {
	if len(rows) == 0 {
		return nil
	}
	maxMomentByDay := make(map[string]string)
	for i := range rows {
		t, ok := parseAnyTime(rows[i].CollectedAt)
		if !ok {
			continue
		}
		ny := t.In(marketZone)
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
	var out []StoredOptionRow
	for i := range rows {
		t, ok := parseAnyTime(rows[i].CollectedAt)
		if !ok {
			continue
		}
		ny := t.In(marketZone)
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
func computeGreekAttribution(history []StoredOptionRow) greekAttrTotals {
	daily := lastSnapshotPerDay(history)
	if len(daily) < 2 {
		return greekAttrTotals{}
	}
	sort.Slice(daily, func(i, j int) bool { return daily[i].CollectedAt < daily[j].CollectedAt })

	var usable []StoredOptionRow
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
		t0, _ := parseAnyTime(prev.CollectedAt)
		t1, _ := parseAnyTime(curr.CollectedAt)
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

func (s *DesktopService) buildContractGo(ctx context.Context, q url.Values) (ContractResponse, error) {
	ticker := strings.ToUpper(strings.TrimSpace(q.Get("ticker")))
	if ticker == "" {
		ticker = "SPY"
	}
	expiry := strings.TrimSpace(q.Get("expiry"))
	strikeVal, _ := strconv.ParseFloat(q.Get("strike"), 64)
	optType := strings.ToLower(strings.TrimSpace(q.Get("type")))
	if optType != "call" && optType != "put" {
		optType = "call"
	}
	source, err := s.store.activeSource(ctx, ticker)
	if err != nil {
		return ContractResponse{}, err
	}

	if expiry == "" || strikeVal <= 0 {
		_, latest, err := s.store.latestSnapshot(ctx, ticker, source)
		if err != nil || len(latest) == 0 {
			return ContractResponse{Empty: true}, nil
		}
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
	hist, err := s.store.contractHistory(ctx, ticker, source, expiry, strikeVal, optType, snapshotHistoryDays())
	if err != nil || len(hist) == 0 {
		return ContractResponse{Empty: true}, nil
	}

	withSolvedIV(hist, defaultRiskFreeRate, 0.0)
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
	var attrSteps []AttributionStep
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
			attrSteps = append(attrSteps, AttributionStep{
				Label: it.label,
				Cents: cents,
				Str:   str,
				Kind:  kind,
			})
		}
	} else {
		attrSteps = []AttributionStep{}
	}

	points := make([]ContractPoint, len(hist))
	for i := range hist {
		r := &hist[i]
		points[i] = ContractPoint{
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
	name := fmt.Sprintf("%s %s %s%s", ticker, expYYMMDD, fmtStrikeVal(strikeVal), strings.ToUpper(optType[:1]))
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

	return ContractResponse{
		Empty:       false,
		Name:        name,
		ExpiryISO:   expiry,
		Strike:      strikeVal,
		Type:        optType,
		PriceStr:    fmtPrice(lastRow.LastPrice, 2),
		IVStr:       fmtPct(lastRow.IV, 1, false),
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

type contractID struct {
	expiry string
	strike float64
	side   string
}

type volStatRow struct {
	avgVolume     *float64
	stdVolume     *float64
	historyPoints int
}

func (s *Store) volumeStatsMap(ctx context.Context, ticker, source string) (map[contractID]volStatRow, error) {
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
	out := make(map[contractID]volStatRow)
	for rows.Next() {
		var exp, side string
		var strike float64
		var avg, std sql.NullFloat64
		var pts int
		if err := rows.Scan(&exp, &strike, &side, &avg, &std, &pts); err == nil {
			out[contractID{expiry: exp, strike: strike, side: side}] = volStatRow{
				avgVolume:     nullFloatPtr(avg),
				stdVolume:     nullFloatPtr(std),
				historyPoints: pts,
			}
		}
	}
	return out, rows.Err()
}

func computeUnusualActivity(latest []StoredOptionRow, stats map[contractID]volStatRow) []UnusualRow {
	type flaggedItem struct {
		row    *StoredOptionRow
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
		st := stats[contractID{expiry: exp, strike: r.Strike, side: r.OptionType}]
		var z *float64
		if st.avgVolume != nil && st.stdVolume != nil && *st.stdVolume != 0 {
			zv := (vol - *st.avgVolume) / (*st.stdVolume)
			if !math.IsNaN(zv) && !math.IsInf(zv, 0) {
				z = &zv
			}
		}
		hasEnough := st.historyPoints >= unusualMinHistoryPoints
		zFlag := hasEnough && z != nil && *z > unusualZThreshold
		oiClip := math.Max(valOrZero(r.OI), 1.0)
		fallbackFlag := !hasEnough && (vol > 2.0*oiClip)
		if zFlag || fallbackFlag {
			flagged = append(flagged, flaggedItem{row: r, avgVol: st.avgVolume, zscore: z})
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
	out := make([]UnusualRow, len(flagged))
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
		out[i] = UnusualRow{
			Expiry: fmtExpiryShort(r.Expiry),
			Strike: fmtStrikeVal(r.Strike),
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

func computeOIDelta(twoDaysRows []StoredOptionRow) []OIDeltaRow {
	daily := lastSnapshotPerDay(twoDaysRows)
	if len(daily) == 0 {
		return []OIDeltaRow{}
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
		return []OIDeltaRow{}
	}
	latestMoment := moments[len(moments)-1]
	prevMoment := moments[len(moments)-2]

	latestOI := make(map[contractID]float64)
	prevOI := make(map[contractID]float64)
	var keysOrder []contractID
	seenKey := make(map[contractID]bool)
	for i := range daily {
		r := &daily[i]
		if !isValidFinite(r.OI) {
			continue
		}
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		cid := contractID{expiry: exp, strike: r.Strike, side: r.OptionType}
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
		cid    contractID
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
	out := make([]OIDeltaRow, len(items))
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
		out[i] = OIDeltaRow{
			Expiry:     fmtExpiryShort(it.cid.expiry),
			Strike:     fmtStrikeVal(it.cid.strike),
			Type:       strings.ToUpper(it.cid.side[:1]),
			OI:         formatIntComma(int64(it.oi)),
			OIDelta:    dInt,
			OIDeltaStr: deltaStr,
			OIPctStr:   pctStr,
		}
	}
	return out
}

type daySummaryRecord struct {
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

func (s *Store) sessionDays(ctx context.Context, ticker, source string, limit int) ([]string, error) {
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
			if sessionStartedAt(colAt) && marketDayString(colAt) == d {
				days = append(days, d)
				if limit > 0 && len(days) >= limit {
					break
				}
			}
		}
	}
	return days, rows.Err()
}

func (s *Store) daySummaryOn(ctx context.Context, ticker, source, day string) (*daySummaryRecord, error) {
	var r daySummaryRecord
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
	r.CallWall = nullFloatPtr(cw)
	r.PutWall = nullFloatPtr(pw)
	r.GammaFlip = nullFloatPtr(gf)
	r.NearestExpiry = nearExp.String
	r.NearestMaxPain = nullFloatPtr(mp)
	r.MoveLower = nullFloatPtr(ml)
	r.MoveUpper = nullFloatPtr(mu)
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

func (s *Store) momentRollupIV(ctx context.Context, ticker, source, collectedAt string) *float64 {
	var iv sql.NullFloat64
	_ = s.read.QueryRowContext(ctx,
		"SELECT iv_weighted_avg FROM snapshot_iv_summary WHERE ticker = ? AND source = ? AND collected_at = ?",
		strings.ToUpper(ticker), source, collectedAt,
	).Scan(&iv)
	return nullFloatPtr(iv)
}

func strikeStepOf(r *daySummaryRecord) *float64 {
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
func buildChangesLadder(prev, curr *daySummaryRecord, prevIV, currIV *float64) []LadderRow {
	if curr == nil {
		return []LadderRow{}
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
			return fmt.Sprintf("%s – %s", fmtPriceVal(bd[0], 2), fmtPriceVal(bd[1], 2))
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
				return fmtPriceVal(*v, 2)
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
				printed = fmt.Sprintf("%s%s strike%s · %s", sign, fmtStrikeVal(math.Abs(st)), plural, fmtSignedPrice(d, 2))
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

	out := make([]LadderRow, len(lines))
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
		out[i] = LadderRow{
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
	s := fmtPriceVal(math.Abs(val), digits)
	if val < 0 {
		return "-" + s
	}
	return "+" + s
}

func (s *Store) putCallRatioSeries(ctx context.Context, ticker, source string, days int) ([]PCRPoint, error) {
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
	q := fmt.Sprintf(`SELECT CAST(collected_at AS TEXT), option_type, SUM(volume), SUM(open_interest)
		FROM (
			SELECT collected_at, option_type, volume, open_interest FROM option_snapshots WHERE %s
			UNION ALL
			SELECT collected_at, option_type, volume, open_interest FROM option_snapshots_archive WHERE %s
		)
		GROUP BY collected_at, option_type
		ORDER BY collected_at`, where, where)
	rows, err := s.read.QueryContext(ctx, q, append(args, args...)...)
	if err != nil {
		return []PCRPoint{}, err
	}
	defer rows.Close()

	type sideSums struct {
		cVol, pVol sql.NullFloat64
		cOI, pOI   sql.NullFloat64
	}
	byMoment := make(map[string]*sideSums)
	var order []string
	for rows.Next() {
		var m, side string
		var vol, oi sql.NullFloat64
		if err := rows.Scan(&m, &side, &vol, &oi); err == nil {
			ss, exists := byMoment[m]
			if !exists {
				ss = &sideSums{}
				byMoment[m] = ss
				order = append(order, m)
			}
			if side == "call" {
				ss.cVol = vol
				ss.cOI = oi
			} else if side == "put" {
				ss.pVol = vol
				ss.pOI = oi
			}
		}
	}
	out := make([]PCRPoint, len(order))
	for i, m := range order {
		ss := byMoment[m]
		var pVol, pOI *float64
		if ss.pVol.Valid && ss.cVol.Valid && ss.cVol.Float64 > 0 {
			v := ss.pVol.Float64 / ss.cVol.Float64
			pVol = &v
		}
		if ss.pOI.Valid && ss.cOI.Valid && ss.cOI.Float64 > 0 {
			v := ss.pOI.Float64 / ss.cOI.Float64
			pOI = &v
		}
		out[i] = PCRPoint{Time: fmtTimeMMDDHHMM(m), PCRVol: pVol, PCROI: pOI}
	}
	return out, rows.Err()
}

func fmtDayMMDD(iso string) string {
	if len(iso) >= 10 {
		return iso[5:10]
	}
	return iso
}

func (s *DesktopService) buildFlowGo(ctx context.Context, q url.Values) (FlowResponse, error) {
	ticker := strings.ToUpper(strings.TrimSpace(q.Get("ticker")))
	if ticker == "" {
		ticker = "SPY"
	}
	source, err := s.store.activeSource(ctx, ticker)
	if err != nil {
		return FlowResponse{}, err
	}
	_, latest, err := s.store.latestSnapshot(ctx, ticker, source)
	if err != nil {
		return FlowResponse{}, err
	}
	if len(latest) == 0 {
		return FlowResponse{
			Ticker:     ticker,
			Empty:      true,
			Unusual:    []UnusualRow{},
			OIDelta:    []OIDeltaRow{},
			Ladder:     []LadderRow{},
			PCRHistory: []PCRPoint{},
		}, nil
	}
	spot := latest[0].UnderlyingPrice

	statsMap, _ := s.store.volumeStatsMap(ctx, ticker, source)
	unusualRows := computeUnusualActivity(latest, statsMap)

	dailyMoments, _ := s.store.collectionMoments(ctx, ticker, source, snapshotHistoryDays(), true)
	var oiRows []OIDeltaRow
	if len(dailyMoments) >= 2 {
		twoDays, err := s.store.snapshotsAt(ctx, ticker, source, dailyMoments[:2])
		if err == nil {
			oiRows = computeOIDelta(twoDays)
		}
	}
	if oiRows == nil {
		oiRows = []OIDeltaRow{}
	}

	days, _ := s.store.sessionDays(ctx, ticker, source, 400)
	ladder := []LadderRow{}
	daysLabel := ""
	if len(days) >= 1 {
		dayAfter := days[0]
		dayBefore := days[0]
		if len(days) >= 2 {
			dayBefore = days[1]
		}
		daysLabel = fmt.Sprintf("%s → %s", fmtDayMMDD(dayBefore), fmtDayMMDD(dayAfter))
		after, err1 := s.store.daySummaryOn(ctx, ticker, source, dayAfter)
		var before *daySummaryRecord
		if dayBefore != dayAfter {
			before, _ = s.store.daySummaryOn(ctx, ticker, source, dayBefore)
		}
		if err1 == nil && after != nil {
			ivAfter := s.store.momentRollupIV(ctx, ticker, source, after.CollectedAt)
			var ivBefore *float64
			if before != nil {
				ivBefore = s.store.momentRollupIV(ctx, ticker, source, before.CollectedAt)
			}
			ladder = buildChangesLadder(before, after, ivBefore, ivAfter)
		}
	}

	pcrHist, _ := s.store.putCallRatioSeries(ctx, ticker, source, snapshotHistoryDays())
	return FlowResponse{
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

func buildDaySummaryRecord(rows []StoredOptionRow, r, q float64) *daySummaryRecord {
	if len(rows) == 0 {
		return nil
	}
	withSolvedIV(rows, r, q)
	w := computeGammaWeatherPresolved(rows, r, q)
	if w == nil {
		return nil
	}
	if !sessionStartedAt(w.CollectedAt) {
		return nil
	}
	expMap := make(map[string][]StoredOptionRow)
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
	return &daySummaryRecord{
		Day:            marketDayString(w.CollectedAt),
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

func newDerivedWrite(sqlStmt string, args ...any) DerivedWrite {
	b, _ := json.Marshal(args)
	return DerivedWrite{SQL: sqlStmt, Params: b}
}

func (s *DesktopService) deriveGo(ctx context.Context, ticker, source string) error {
	tkr := strings.ToUpper(strings.TrimSpace(ticker))
	if source == "" {
		var err error
		source, err = s.store.activeSource(ctx, tkr)
		if err != nil {
			return err
		}
	}
	var writes []DerivedWrite

	// 1. Backfill pending snapshot_iv_summary rows (up to 50 newest moments).
	wherePending := "ticker = ? AND iv_model IS NULL"
	argsPending := []any{tkr}
	if source != "" {
		wherePending += " AND source = ?"
		argsPending = append(argsPending, source)
	}
	pRows, err := s.store.read.QueryContext(ctx,
		"SELECT CAST(collected_at AS TEXT) FROM snapshot_iv_summary WHERE "+wherePending+" ORDER BY collected_at DESC LIMIT ?",
		append(argsPending, ivBackfillMomentsPerPass)...)
	if err == nil {
		var pendingMoments []string
		for pRows.Next() {
			var m string
			if err := pRows.Scan(&m); err == nil {
				pendingMoments = append(pendingMoments, m)
			}
		}
		pRows.Close()
		if len(pendingMoments) > 0 {
			snapRows, err := s.store.snapshotsAt(ctx, tkr, source, pendingMoments)
			if err == nil {
				byMoment := make(map[string][]StoredOptionRow)
				for i := range snapRows {
					byMoment[snapRows[i].CollectedAt] = append(byMoment[snapRows[i].CollectedAt], snapRows[i])
				}
				modelName := "own"
				if source == "cboe" {
					modelName = "cboe_native"
				}
				for _, m := range pendingMoments {
					mRows := byMoment[m]
					withSolvedIV(mRows, defaultRiskFreeRate, 0.0)
					avg := ivWeightedAveragePresolved(mRows)
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
		}
	}

	// 2. Backfill missing ticker_day_summary rows.
	if source != "" {
		coreSHA := computeCoreSHA(s.root)
		moments, err := s.store.collectionMoments(ctx, tkr, source, daySummaryBackfillDays, true)
		if err == nil {
			for _, m := range moments {
				day := marketDayString(m)
				var existingAt, existingSHA string
				err := s.store.read.QueryRowContext(ctx,
					"SELECT COALESCE(CAST(collected_at AS TEXT),''), COALESCE(core_sha,'') FROM ticker_day_summary WHERE ticker=? AND source=? AND substr(CAST(day AS TEXT),1,10)=?",
					tkr, source, day,
				).Scan(&existingAt, &existingSHA)
				if err != nil || existingSHA != coreSHA || existingAt < m {
					mRows, err := s.store.snapshotsAt(ctx, tkr, source, []string{m})
					if err == nil && len(mRows) > 0 {
						if rec := buildDaySummaryRecord(mRows, defaultRiskFreeRate, 0.0); rec != nil {
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
								gex_by_strike=EXCLUDED.gex_by_strike, computed_at=EXCLUDED.computed_at, core_sha=EXCLUDED.core_sha`,
								tkr, source, rec.Day, rec.CollectedAt, rec.Spot, rec.NetGEX,
								rec.CallWall, rec.PutWall, rec.GammaFlip, rec.State,
								rec.ExpiryCount, rec.HorizonDTE, nearExp, rec.NearestMaxPain,
								rec.MoveLower, rec.MoveUpper, rec.Contracts,
								rec.CallVolume, rec.PutVolume, rec.CallOI, rec.PutOI,
								string(expJSON), string(gexJSON), coreSHA,
							))
						}
					}
				}
			}
		}
	}

	// 3. Rebuild contract_volume_stats when stale (through_day < lastCompletedTradingDay).
	lastCompleted := lastCompletedTradingDay(s.now())
	var maxThrough sql.NullString
	if source != "" {
		_ = s.store.read.QueryRowContext(ctx,
			"SELECT max(substr(through_day,1,10)) FROM contract_volume_stats WHERE ticker=? AND source=?",
			tkr, source).Scan(&maxThrough)
	} else {
		_ = s.store.read.QueryRowContext(ctx,
			"SELECT max(substr(through_day,1,10)) FROM contract_volume_stats WHERE ticker=?",
			tkr).Scan(&maxThrough)
	}
	if !maxThrough.Valid || maxThrough.String < lastCompleted {
		todayNY := s.now().In(marketZone).Format("2006-01-02")
		allDaily, err := s.store.collectionMoments(ctx, tkr, source, unusualHistoryDays, true)
		if err == nil {
			var priorMoments []string
			for _, m := range allDaily {
				if marketDayString(m) < todayNY {
					priorMoments = append(priorMoments, m)
				}
			}
			if len(priorMoments) > 0 {
				volWrites, err := s.store.computeVolumeStatsWrites(ctx, tkr, source, priorMoments)
				if err == nil {
					writes = append(writes, volWrites...)
				}
			}
		}
	}

	// 4. Optional realized volatility (when GEX_FETCH_RV=1).
	if os.Getenv("GEX_FETCH_RV") == "1" {
		var hasRV int
		_ = s.store.read.QueryRowContext(ctx,
			"SELECT count(*) FROM realized_volatility WHERE ticker=? AND substr(as_of_date,1,10)=?",
			tkr, lastCompleted).Scan(&hasRV)
		if hasRV == 0 {
			if rvWrites, err := s.fetchRVWrites(ctx, tkr, lastCompleted); err == nil {
				writes = append(writes, rvWrites...)
			}
		}
	}

	return s.store.applyDerived(ctx, writes)
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

func (s *Store) computeVolumeStatsWrites(ctx context.Context, ticker, source string, moments []string) ([]DerivedWrite, error) {
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
		writes = append(writes, newDerivedWrite(
			sqlStmt,
			ticker, sk.source, sk.expiry, sk.strike, sk.side, avgPtr, stdPtr, acc.n, acc.maxDay,
		))
	}
	return writes, nil
}

// --- On-demand Python helpers for Yahoo fallback & Realized Volatility ---

func findPythonBin(root string) string {
	// A distributed app carries its on-demand Yahoo helper runtime with it.
	bundledPy := filepath.Join(root, "python", "bin", "python3")
	if _, err := os.Stat(bundledPy); err == nil {
		return bundledPy
	}
	venvPy := filepath.Join(root, ".venv", "bin", "python")
	if _, err := os.Stat(venvPy); err == nil {
		return venvPy
	}
	return "python3"
}

func (s *DesktopService) pythonHelperCommand(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, findPythonBin(s.root), args...)
	cmd.Dir = s.root
	// Keep the bundle immutable and independent of shell Python configuration.
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "PYTHONHOME=") && !strings.HasPrefix(value, "PYTHONPATH=") {
			cmd.Env = append(cmd.Env, value)
		}
	}
	cmd.Env = append(cmd.Env, "PYTHONPATH="+s.root, "PYTHONNOUSERSITE=1", "PYTHONDONTWRITEBYTECODE=1",
		"GEX_NATIVE_BACKEND=1", "GEX_DB_READONLY=1", "GEX_DATABASE_PATH="+s.store.path)
	return cmd
}

func (s *DesktopService) fetchYahooOnDemand(ctx context.Context, ticker string) (Snapshot, error) {
	var raw []byte
	var err error
	if s.pythonURL != "" {
		raw, err = s.compute(ctx, "/compute/yahoo", map[string]string{"ticker": ticker})
		if err != nil {
			return Snapshot{}, err
		}
	} else {
		cmd := s.pythonHelperCommand(ctx, "-m", "app.desktop_compute", "--op=yahoo", "--ticker="+ticker)
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			msg := strings.TrimSpace(stderr.String())
			if msg == "" {
				msg = err.Error()
			}
			return Snapshot{}, fmt.Errorf("Yahoo 采集失败: %s", msg)
		}
		raw = stdout.Bytes()
	}
	var response struct {
		Snapshot
		Error       string `json:"error"`
		RateLimited bool   `json:"rate_limited"`
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		return Snapshot{}, err
	}
	if response.Error != "" {
		code := 0
		if response.RateLimited {
			code = 429
		}
		return response.Snapshot, &ProviderError{Status: code, RetryAfter: 30 * time.Minute, Message: response.Error}
	}
	return response.Snapshot, nil
}

func (s *DesktopService) fetchRVWrites(ctx context.Context, ticker, asOfDay string) ([]DerivedWrite, error) {
	cmd := s.pythonHelperCommand(ctx, "-m", "app.desktop_compute", "--op=rv", "--ticker="+ticker, "--as-of="+asOfDay)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		return nil, err
	}
	var out struct {
		Writes []DerivedWrite `json:"writes"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, err
	}
	return out.Writes, nil
}
