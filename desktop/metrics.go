package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultRiskFreeRate                  = 0.05
	gammaFlipMinShare                    = 1e-4
	gammaFlipMaxDistance                 = 0.25
	gammaWeatherNearFlipPct              = 1.0
	gammaWeatherFarFlipPct               = 3.0
	gammaWeatherHorizonDays              = 30
	gammaWeatherMinExpiries              = 2
	minContractsForExpiryMetrics         = 10
	unusualZThreshold                    = 2.5
	unusualMinVolume                     = 50.0
	unusualMinHistoryPoints              = 5
	unusualHistoryDays                   = 60
	attributionMinPrice                  = 0.05
	ivOutlierMinHistoryPoints            = 3
	ivOutlierRelThreshold                = 0.5
	ivOutlierPriceCorroborationThreshold = 0.15
	ivOutlierMaxUnreliableFraction       = 0.3
	defaultSnapshotHistoryDays           = 365
	daySummaryBackfillDays               = 45
	ivBackfillMomentsPerPass             = 50

	weatherClear     = "clear"
	weatherFair      = "fair"
	weatherUnsettled = "unsettled"
	weatherRain      = "rain"
	weatherStorm     = "storm"

	ivFloor          = 1e-4
	ivCeiling        = 5.0
	ivMinVega        = 1e-8
	ivTolerance      = 1e-6
	ivResolution     = 1e-3
	ivNewtonSteps    = 12
	ivBisectionSteps = 60
)

// Python's weekday() is Monday=0 .. Sunday=6; Go's time.Weekday() is Sunday=0 .. Saturday=6.
var weekdaysCNByGoWeekday = []string{"日", "一", "二", "三", "四", "五", "六"}

var weatherCN = map[string][2]string{
	weatherClear:     {"晴朗", "正 Gamma，翻转点远 — 做市商对冲在压制波动"},
	weatherFair:      {"多云", "正 Gamma，但翻转点已在射程内 — 压制可能失效"},
	weatherUnsettled: {"临界", "价格贴近 Gamma 翻转点 — 状态随时可能切换"},
	weatherRain:      {"阵雨", "负 Gamma，翻转点较近 — 波动暂时被放大"},
	weatherStorm:     {"风暴", "负 Gamma，附近无边界 — 做市商对冲放大波动"},
}

var dimensionCN = map[string]string{
	"gex":   "GEX",
	"vanna": "Vanna",
	"charm": "Charm",
}

var unitCN = map[string]string{
	"gex":   "USD / 1% 标的变动",
	"vanna": "USD / 1 vol 点",
	"charm": "USD / 日",
}

func snapshotHistoryDays() int {
	if raw := os.Getenv("SNAPSHOT_HISTORY_DAYS"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n
		}
	}
	return defaultSnapshotHistoryDays
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

func floatPtr(v float64) *float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return &v
}

func valOrZero(p *float64) float64 {
	if p == nil || math.IsNaN(*p) || math.IsInf(*p, 0) {
		return 0
	}
	return *p
}

func isValidPos(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0) && *p > 0
}

func isValidFinite(p *float64) bool {
	return p != nil && !math.IsNaN(*p) && !math.IsInf(*p, 0)
}

func normCDF(x float64) float64 {
	return 0.5 * math.Erfc(-x/math.Sqrt2)
}

func normPDF(x float64) float64 {
	return math.Exp(-0.5*x*x) / math.Sqrt(2.0*math.Pi)
}

// yearsToExpiry matches metrics_core.years_to_expiry: expiry is 16:00 America/New_York
// converted to UTC, minus collected_at in UTC, divided by (365 * 86400).
func yearsToExpiry(expiryISO, collectedAt string) float64 {
	if len(expiryISO) < 10 {
		return 0
	}
	expDate, err := time.ParseInLocation("2006-01-02", expiryISO[:10], marketZone)
	if err != nil {
		return 0
	}
	expCloseUTC := time.Date(expDate.Year(), expDate.Month(), expDate.Day(), 16, 0, 0, 0, marketZone).UTC()
	colUTC, ok := parseAnyTime(collectedAt)
	if !ok {
		return 0
	}
	return expCloseUTC.Sub(colUTC).Seconds() / (365.0 * 86400.0)
}

func bsPrice(s, k, t, sigma, r float64, isCall bool, q float64) float64 {
	sqrtT := math.Sqrt(t)
	vSqrtT := sigma * sqrtT
	d1 := (math.Log(s/k) + (r-q+0.5*sigma*sigma)*t) / vSqrtT
	d2 := d1 - vSqrtT
	carry := math.Exp(-q * t)
	disc := math.Exp(-r * t)
	if isCall {
		return s*carry*normCDF(d1) - k*disc*normCDF(d2)
	}
	return k*disc*normCDF(-d2) - s*carry*normCDF(-d1)
}

// contractPriceForIV matches metrics_core.contract_price_for_iv:
// mid of (bid, ask) when bid > 0, ask > 0, and ask >= bid; otherwise last_price.
func contractPriceForIV(row *StoredOptionRow) *float64 {
	if isValidPos(row.Bid) && isValidPos(row.Ask) && *row.Ask >= *row.Bid {
		mid := 0.5 * (*row.Bid + *row.Ask)
		return &mid
	}
	if isValidFinite(row.LastPrice) {
		v := *row.LastPrice
		return &v
	}
	return nil
}

// solveImpliedVolatility matches metrics_core.solve_implied_volatility.
func solveImpliedVolatility(price, s, k, t, r float64, optionType string, q float64) *float64 {
	if !(price > 0 && s > 0 && k > 0 && t > 0) || math.IsNaN(price) || math.IsNaN(s) || math.IsNaN(k) || math.IsNaN(t) {
		return nil
	}
	isCall := strings.EqualFold(optionType, "call")
	if !isCall && !strings.EqualFold(optionType, "put") {
		return nil
	}
	carry := math.Exp(-q * math.Max(t, 0))
	disc := math.Exp(-r * math.Max(t, 0))
	var intrinsic float64
	if isCall {
		intrinsic = math.Max(s*carry-k*disc, 0)
	} else {
		intrinsic = math.Max(k*disc-s*carry, 0)
	}
	if !(price > intrinsic+1e-9) {
		return nil
	}

	sigma := 0.5
	sqrtT := math.Sqrt(t)
	for i := 0; i < ivNewtonSteps; i++ {
		modelled := bsPrice(s, k, t, sigma, r, isCall, q)
		d1 := (math.Log(s/k) + (r-q+0.5*sigma*sigma)*t) / (sigma * sqrtT)
		vega := s * carry * normPDF(d1) * sqrtT
		step := 0.0
		if vega > ivMinVega {
			step = (modelled - price) / vega
		}
		if step < -1.0 {
			step = -1.0
		} else if step > 1.0 {
			step = 1.0
		}
		sigma = sigma - step
		if sigma < ivFloor {
			sigma = ivFloor
		} else if sigma > ivCeiling {
			sigma = ivCeiling
		}
	}

	tol := math.Max(ivTolerance, math.Abs(price)*1e-6)
	residual := math.Abs(bsPrice(s, k, t, sigma, r, isCall, q) - price)
	if !(residual <= tol) {
		low := ivFloor
		high := ivCeiling
		for i := 0; i < ivBisectionSteps; i++ {
			mid := 0.5 * (low + high)
			if bsPrice(s, k, t, mid, r, isCall, q) < price {
				low = mid
			} else {
				high = mid
			}
		}
		sigma = 0.5 * (low + high)
	}

	finalErr := math.Abs(bsPrice(s, k, t, sigma, r, isCall, q) - price)
	if !(finalErr <= tol) {
		return nil
	}
	d1Final := (math.Log(s/k) + (r-q+0.5*sigma*sigma)*t) / (sigma * sqrtT)
	vegaFinal := s * carry * normPDF(d1Final) * sqrtT
	if vegaFinal >= tol/ivResolution {
		return floatPtr(sigma)
	}
	return nil
}

// withSolvedIV matches metrics_core.with_solved_iv: Cboe chains and VIX/VIXW keep
// their native IV; other sources solve IV from contractPriceForIV.
func withSolvedIV(rows []StoredOptionRow, r float64, q float64) {
	if len(rows) == 0 {
		return
	}
	allCboe := true
	for i := range rows {
		rows[i].ProviderIV = rows[i].IV
		if rows[i].Source != "cboe" {
			allCboe = false
		}
	}
	if allCboe || strings.EqualFold(rows[0].Ticker, "VIX") || strings.EqualFold(rows[0].Ticker, "VIXW") {
		for i := range rows {
			rows[i].IVIsOurs = false
		}
		return
	}
	for i := range rows {
		row := &rows[i]
		if strings.EqualFold(row.Ticker, "VIX") || strings.EqualFold(row.Ticker, "VIXW") {
			row.IVIsOurs = false
			continue
		}
		price := contractPriceForIV(row)
		if price == nil {
			row.IVIsOurs = false
			continue
		}
		t := yearsToExpiry(row.Expiry, row.CollectedAt)
		solved := solveImpliedVolatility(*price, row.UnderlyingPrice, row.Strike, t, r, row.OptionType, q)
		if solved != nil {
			row.IV = solved
			row.IVIsOurs = true
		} else {
			row.IVIsOurs = false
		}
	}
}

type bsGreeks struct {
	delta float64
	gamma float64
	theta float64
	vega  float64
	rho   float64
	vanna float64
	charm float64
	valid bool
	zero  bool
}

// calcBSGreeks matches _black_scholes_greeks_batch in metrics_core.py:
// - If any of spot, strike, t, iv is missing/NaN, returns valid=false (NaN in Python).
// - If all are present but t <= 0 or iv <= 0 or spot <= 0, returns valid=true, zero=true (0.0 in Python).
func calcBSGreeks(s, k, t, r, sigma float64, optionType string, q float64) bsGreeks {
	if math.IsNaN(s) || math.IsNaN(k) || math.IsNaN(t) || math.IsNaN(sigma) {
		return bsGreeks{}
	}
	if !(s > 0 && k > 0 && t > 0 && sigma > 0) {
		return bsGreeks{valid: true, zero: true}
	}
	if optionType != "call" && optionType != "put" {
		return bsGreeks{}
	}
	sqrtT := math.Sqrt(t)
	vSqrtT := sigma * sqrtT
	d1 := (math.Log(s/k) + (r-q+0.5*sigma*sigma)*t) / vSqrtT
	d2 := d1 - vSqrtT
	pdf1 := normPDF(d1)
	disc := math.Exp(-r * t)
	carry := math.Exp(-q * t)

	gamma := carry * pdf1 / (s * vSqrtT)
	vega := s * carry * pdf1 * sqrtT / 100.0
	vanna := -carry * pdf1 * d2 / sigma
	sharedCharm := carry * pdf1 * (2.0*(r-q)*t - d2*vSqrtT) / (2.0 * t * vSqrtT)

	if optionType == "call" {
		nd1 := normCDF(d1)
		nd2 := normCDF(d2)
		delta := carry * nd1
		theta := (-(s*carry*pdf1*sigma)/(2.0*sqrtT) - r*k*disc*nd2 + q*s*carry*nd1) / 365.0
		rho := k * t * disc * nd2 / 100.0
		charm := q*carry*nd1 - sharedCharm
		return bsGreeks{delta: delta, gamma: gamma, theta: theta, vega: vega, rho: rho, vanna: vanna, charm: charm, valid: true}
	}
	nd1 := normCDF(d1)
	nmd1 := normCDF(-d1)
	nmd2 := normCDF(-d2)
	delta := carry * (nd1 - 1.0)
	theta := (-(s*carry*pdf1*sigma)/(2.0*sqrtT) + r*k*disc*nmd2 - q*s*carry*nmd1) / 365.0
	rho := -k * t * disc * nmd2 / 100.0
	charm := -q*carry*nmd1 - sharedCharm
	return bsGreeks{delta: delta, gamma: gamma, theta: theta, vega: vega, rho: rho, vanna: vanna, charm: charm, valid: true}
}

// enrichGreeks populates missing Greeks (and merges provider Greeks where present),
// matching screener_table / merge_provider_greeks in metrics_core.py.
func enrichGreeks(rows []StoredOptionRow, r float64, q float64) {
	withSolvedIV(rows, r, q)
	for i := range rows {
		row := &rows[i]
		if row.IVUnreliable || !isValidFinite(row.IV) || !(row.UnderlyingPrice > 0) {
			if row.IVUnreliable {
				row.Delta = nil
				row.Gamma = nil
				row.Theta = nil
				row.Vega = nil
				row.Rho = nil
				row.Vanna = nil
				row.Charm = nil
			}
			continue
		}
		t := yearsToExpiry(row.Expiry, row.CollectedAt)
		g := calcBSGreeks(row.UnderlyingPrice, row.Strike, t, r, *row.IV, row.OptionType, q)
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
}

// rowExposure computes the single-contract dealer exposure for dimension ("gex", "vanna", "charm"),
// matching metrics_core.contract_exposure. Assumes withSolvedIV has run.
func rowExposure(row *StoredOptionRow, dimension string, r float64, q float64) (float64, bool) {
	if !isValidFinite(row.OI) || !(row.UnderlyingPrice > 0) {
		return 0, false
	}
	oi := *row.OI
	if oi < 0 {
		return 0, false
	}
	t := yearsToExpiry(row.Expiry, row.CollectedAt)
	if t <= 0 || oi == 0 {
		return 0, true
	}
	var sign float64
	if row.OptionType == "put" {
		sign = -1.0
	} else {
		sign = 1.0
	}
	s := row.UnderlyingPrice
	modelValid := s > 0 && row.Strike > 0 && isValidPos(row.IV) && t > 0
	var greek float64
	hasGreek := false

	switch dimension {
	case "gex":
		if isValidFinite(row.Gamma) && *row.Gamma >= 0 {
			greek = *row.Gamma
			hasGreek = true
		} else if modelValid {
			g := calcBSGreeks(s, row.Strike, t, r, *row.IV, row.OptionType, q)
			if g.valid && !g.zero {
				greek = g.gamma
				hasGreek = true
			}
		}
		if !hasGreek {
			return 0, false
		}
		return sign * greek * oi * (s * 100.0 * s * 0.01), true
	case "vanna":
		if modelValid {
			g := calcBSGreeks(s, row.Strike, t, r, *row.IV, row.OptionType, q)
			if g.valid && !g.zero {
				greek = g.vanna
				hasGreek = true
			}
		}
		if !hasGreek {
			return 0, false
		}
		return sign * greek * oi * (s * 100.0 * 0.01), true
	case "charm":
		if modelValid {
			g := calcBSGreeks(s, row.Strike, t, r, *row.IV, row.OptionType, q)
			if g.valid && !g.zero {
				greek = g.charm
				hasGreek = true
			}
		}
		if !hasGreek {
			return 0, false
		}
		return sign * greek * oi * (s * 100.0 / 365.0), true
	default:
		return 0, false
	}
}

// ExposureMatrix represents the strike x expiry exposure table, matching metrics_core.exposure_matrix.
// Note: Strikes in ExposureMatrix are sorted DESCENDING (highest strike first), matching exposure_matrix().
// Cells holds NaN when all contracts at a listed (strike, expiry) had unknown exposure (min_count=1),
// and 0.0 when no contracts were listed at (strike, expiry) (unstack fill_value=0).
type ExposureMatrix struct {
	Strikes  []float64   // sorted descending
	Expiries []string    // sorted ascending
	Cells    [][]float64 // len(Strikes) x len(Expiries)
}

func buildExposureMatrixPresolved(rows []StoredOptionRow, dimension string, allowedExpiries map[string]bool, r, q float64) ExposureMatrix {
	if len(rows) == 0 {
		return ExposureMatrix{}
	}
	strikeSet := make(map[float64]struct{}, 128)
	expirySet := make(map[string]struct{}, 32)
	type cellKey struct {
		strike float64
		expiry string
	}
	type cellAccum struct {
		sum   float64
		count int
	}
	acc := make(map[cellKey]cellAccum, 512)
	for i := range rows {
		row := &rows[i]
		exp := row.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		if allowedExpiries != nil && !allowedExpiries[exp] {
			continue
		}
		strikeSet[row.Strike] = struct{}{}
		expirySet[exp] = struct{}{}
		k := cellKey{strike: row.Strike, expiry: exp}
		cur := acc[k]
		if val, ok := rowExposure(row, dimension, r, q); ok && !math.IsNaN(val) && !math.IsInf(val, 0) {
			cur.sum += val
			cur.count++
		}
		acc[k] = cur
	}
	if len(strikeSet) == 0 || len(expirySet) == 0 {
		return ExposureMatrix{}
	}
	strikes := make([]float64, 0, len(strikeSet))
	for k := range strikeSet {
		strikes = append(strikes, k)
	}
	// Descending order to match exposure_matrix().sort_index(ascending=False)
	sort.Slice(strikes, func(i, j int) bool { return strikes[i] > strikes[j] })

	expiries := make([]string, 0, len(expirySet))
	for e := range expirySet {
		expiries = append(expiries, e)
	}
	sort.Strings(expiries)

	cells := make([][]float64, len(strikes))
	for si, k := range strikes {
		rowCells := make([]float64, len(expiries))
		for ei, e := range expiries {
			ck := cellKey{strike: k, expiry: e}
			if c, listed := acc[ck]; listed {
				if c.count > 0 {
					rowCells[ei] = c.sum
				} else {
					rowCells[ei] = math.NaN()
				}
			} else {
				rowCells[ei] = 0.0
			}
		}
		cells[si] = rowCells
	}
	return ExposureMatrix{Strikes: strikes, Expiries: expiries, Cells: cells}
}

func (m ExposureMatrix) sumAll() float64 {
	total := 0.0
	for si := range m.Cells {
		for _, v := range m.Cells[si] {
			if !math.IsNaN(v) {
				total += v
			}
		}
	}
	return total
}

// gammaFlipFromMatrix matches metrics_core.gamma_flip_from_matrix / _nearest_crossing:
// 1. Sorts strikes ascending.
// 2. Sums each strike's exposure across expiries (treating NaN as 0).
// 3. Computes cumulative sum across strikes in ascending order.
// 4. Finds crossings between strictly nonzero cumulative values whose nearest significant
//    cumulative values (> 1e-4 * max(|cumulative|)) on either side have opposite signs.
// 5. Linearly interpolates each crossing and returns the one nearest spot, provided it is
//    within GAMMA_FLIP_MAX_DISTANCE (25%) of spot.
func gammaFlipFromMatrix(mat ExposureMatrix, spot float64) *float64 {
	n := len(mat.Strikes)
	if n == 0 || len(mat.Expiries) == 0 || math.IsNaN(spot) || math.IsInf(spot, 0) {
		return nil
	}
	// Ascending strike order (mat.Strikes is descending).
	strikes := make([]float64, n)
	totals := make([]float64, n)
	for i := 0; i < n; i++ {
		srcIdx := n - 1 - i
		strikes[i] = mat.Strikes[srcIdx]
		rowSum := 0.0
		for _, v := range mat.Cells[srcIdx] {
			if !math.IsNaN(v) {
				rowSum += v
			}
		}
		totals[i] = rowSum
	}

	cumulative := make([]float64, n)
	running := 0.0
	var carrying []int
	maxMag := 0.0
	for i := 0; i < n; i++ {
		running += totals[i]
		cumulative[i] = running
		if running != 0 {
			carrying = append(carrying, i)
		}
		if mag := math.Abs(running); mag > maxMag {
			maxMag = mag
		}
	}
	if len(carrying) < 2 || maxMag == 0 {
		return nil
	}

	threshold := gammaFlipMinShare * maxMag
	var significant []int
	for i := 0; i < n; i++ {
		if math.Abs(cumulative[i]) > threshold {
			significant = append(significant, i)
		}
	}
	if len(significant) == 0 {
		return nil
	}

	var crossings []float64
	for j := 0; j < len(carrying)-1; j++ {
		bIdx := carrying[j]
		aIdx := carrying[j+1]
		y0 := cumulative[bIdx]
		y1 := cumulative[aIdx]
		if (y0 > 0 && y1 > 0) || (y0 < 0 && y1 < 0) {
			continue
		}
		// searchsorted(significant, bIdx, side="right") - 1
		left := sort.Search(len(significant), func(k int) bool { return significant[k] > bIdx }) - 1
		// searchsorted(significant, aIdx, side="left")
		right := sort.Search(len(significant), func(k int) bool { return significant[k] >= aIdx })
		if left < 0 || right >= len(significant) {
			continue
		}
		leftVal := cumulative[significant[left]]
		rightVal := cumulative[significant[right]]
		if (leftVal > 0 && rightVal > 0) || (leftVal < 0 && rightVal < 0) || leftVal == 0 || rightVal == 0 {
			continue
		}
		x0 := strikes[bIdx]
		x1 := strikes[aIdx]
		cross := x0 + (0.0-y0)*(x1-x0)/(y1-y0)
		crossings = append(crossings, cross)
	}
	if len(crossings) == 0 {
		return nil
	}

	nearest := crossings[0]
	bestDist := math.Abs(nearest - spot)
	for _, c := range crossings[1:] {
		if d := math.Abs(c - spot); d < bestDist {
			nearest = c
			bestDist = d
		}
	}
	if math.Abs(nearest-spot) > gammaFlipMaxDistance*math.Abs(spot) {
		return nil
	}
	return floatPtr(nearest)
}

// dealerWalls matches metrics_core.dealer_walls: strike with maximum open interest
// in calls and puts across the given rows (ties go to the lowest strike per pandas idxmax).
func dealerWalls(rows []StoredOptionRow, allowedExpiries map[string]bool) (*float64, *float64) {
	if len(rows) == 0 {
		return nil, nil
	}
	callOI := make(map[float64]float64)
	putOI := make(map[float64]float64)
	for i := range rows {
		r := &rows[i]
		if allowedExpiries != nil {
			exp := r.Expiry
			if len(exp) >= 10 {
				exp = exp[:10]
			}
			if !allowedExpiries[exp] {
				continue
			}
		}
		oi := valOrZero(r.OI)
		if r.OptionType == "call" {
			callOI[r.Strike] += oi
		} else if r.OptionType == "put" {
			putOI[r.Strike] += oi
		}
	}
	return idxMaxMap(callOI), idxMaxMap(putOI)
}

func idxMaxMap(m map[float64]float64) *float64 {
	if len(m) == 0 {
		return nil
	}
	keys := make([]float64, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Float64s(keys)
	bestK := keys[0]
	bestV := m[bestK]
	for _, k := range keys[1:] {
		if v := m[k]; v > bestV {
			bestV = v
			bestK = k
		}
	}
	return floatPtr(bestK)
}

// gexWallsPresolved matches metrics_core.gex_walls:
// call_gex_wall is the strike with the largest positive total call GEX,
// put_gex_wall is the strike with the most negative total put GEX.
func gexWallsPresolved(rows []StoredOptionRow, r, q float64) (*float64, *float64) {
	if len(rows) == 0 {
		return nil, nil
	}
	type accum struct {
		sum   float64
		count int
	}
	callGEX := make(map[float64]accum)
	putGEX := make(map[float64]accum)
	for i := range rows {
		row := &rows[i]
		v, ok := rowExposure(row, "gex", r, q)
		if !ok || math.IsNaN(v) {
			continue
		}
		if row.OptionType == "call" {
			a := callGEX[row.Strike]
			a.sum += v
			a.count++
			callGEX[row.Strike] = a
		} else if row.OptionType == "put" {
			a := putGEX[row.Strike]
			a.sum += v
			a.count++
			putGEX[row.Strike] = a
		}
	}
	var callWall *float64
	if len(callGEX) > 0 {
		keys := make([]float64, 0, len(callGEX))
		for k, a := range callGEX {
			if a.count > 0 && a.sum > 0 {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Float64s(keys)
			bestK := keys[0]
			bestV := callGEX[bestK].sum
			for _, k := range keys[1:] {
				if s := callGEX[k].sum; s > bestV {
					bestV = s
					bestK = k
				}
			}
			callWall = floatPtr(bestK)
		}
	}
	var putWall *float64
	if len(putGEX) > 0 {
		keys := make([]float64, 0, len(putGEX))
		for k, a := range putGEX {
			if a.count > 0 && a.sum < 0 {
				keys = append(keys, k)
			}
		}
		if len(keys) > 0 {
			sort.Float64s(keys)
			bestK := keys[0]
			bestV := putGEX[bestK].sum
			for _, k := range keys[1:] {
				if s := putGEX[k].sum; s < bestV {
					bestV = s
					bestK = k
				}
			}
			putWall = floatPtr(bestK)
		}
	}
	return callWall, putWall
}

// maxPain matches metrics_core.max_pain.
func maxPain(rows []StoredOptionRow) *float64 {
	if len(rows) == 0 {
		return nil
	}
	strikeSet := make(map[float64]struct{})
	callOI := make(map[float64]float64)
	putOI := make(map[float64]float64)
	totalOI := 0.0
	for i := range rows {
		r := &rows[i]
		strikeSet[r.Strike] = struct{}{}
		oi := valOrZero(r.OI)
		if oi > 0 {
			totalOI += oi
			if r.OptionType == "call" {
				callOI[r.Strike] += oi
			} else if r.OptionType == "put" {
				putOI[r.Strike] += oi
			}
		}
	}
	if totalOI <= 0 || len(strikeSet) == 0 {
		return nil
	}
	strikes := make([]float64, 0, len(strikeSet))
	for k := range strikeSet {
		strikes = append(strikes, k)
	}
	sort.Float64s(strikes)
	bestStrike := strikes[0]
	bestPain := math.Inf(1)
	for _, s := range strikes {
		callPain := 0.0
		putPain := 0.0
		for _, k := range strikes {
			if s > k {
				if oi := callOI[k]; oi > 0 {
					callPain += (s - k) * oi
				}
			} else if k > s {
				if oi := putOI[k]; oi > 0 {
					putPain += (k - s) * oi
				}
			}
		}
		pain := callPain + putPain
		if pain < bestPain {
			bestPain = pain
			bestStrike = s
		}
	}
	return floatPtr(bestStrike)
}

func contractsBackingExpiry(rows []StoredOptionRow) int {
	cnt := 0
	for i := range rows {
		if valOrZero(rows[i].OI) > 0 {
			cnt++
		}
	}
	return cnt
}

// atmImpliedVolatilityPresolved matches metrics.atm_implied_volatility on rows
// of a single expiry that have already run through withSolvedIV.
func atmImpliedVolatilityPresolved(rows []StoredOptionRow, spot float64) *float64 {
	if len(rows) == 0 || !(spot > 0) || math.IsNaN(spot) {
		return nil
	}
	bestDist := math.Inf(1)
	bestStrike := 0.0
	hasStrike := false
	for i := range rows {
		r := &rows[i]
		if !math.IsNaN(r.Strike) {
			d := math.Abs(r.Strike - spot)
			if !hasStrike || d < bestDist {
				bestDist = d
				bestStrike = r.Strike
				hasStrike = true
			}
		}
	}
	if !hasStrike {
		return nil
	}
	sum := 0.0
	cnt := 0
	for i := range rows {
		r := &rows[i]
		if r.Strike == bestStrike && isValidPos(r.IV) {
			sum += *r.IV
			cnt++
		}
	}
	if cnt == 0 {
		return nil
	}
	return floatPtr(sum / float64(cnt))
}

type ExpectedMoveResult struct {
	ATMIV   float64
	Years   float64
	Move    float64
	MovePct float64 // percentage (0..100), matching metrics.expected_move["move_pct"]
	Lower   float64
	Upper   float64
}

// expectedMovePresolved matches metrics.expected_move on rows of a single expiry.
func expectedMovePresolved(rows []StoredOptionRow, spot float64) *ExpectedMoveResult {
	if len(rows) == 0 || !(spot > 0) || math.IsNaN(spot) {
		return nil
	}
	iv := atmImpliedVolatilityPresolved(rows, spot)
	if iv == nil {
		return nil
	}
	years := yearsToExpiry(rows[0].Expiry, rows[0].CollectedAt)
	if !(years > 0) || math.IsNaN(years) {
		return nil
	}
	move := spot * (*iv) * math.Sqrt(years)
	return &ExpectedMoveResult{
		ATMIV:   *iv,
		Years:   years,
		Move:    move,
		MovePct: move / spot * 100.0,
		Lower:   spot - move,
		Upper:   spot + move,
	}
}

type GammaWeatherResult struct {
	CollectedAt     string
	Spot            float64
	NetGEX          float64
	CallWall        *float64
	PutWall         *float64
	GammaFlip       *float64
	FlipDistancePct *float64 // unsigned percentage: abs(flip - spot) / spot * 100
	State           string   // "clear", "fair", "unsettled", "rain", "storm"
	ExpiryCount     int
	HorizonDTE      int
	Expiries        []string
	GEXByStrike     [][2]float64
}

func weatherExpiries(expiries []string, moment string, horizonDays, minExpiries int) []string {
	if len(expiries) == 0 {
		return nil
	}
	ordered := make([]string, len(expiries))
	copy(ordered, expiries)
	sort.Strings(ordered)
	var inside []string
	for _, e := range ordered {
		if calcDTE(e, moment) <= horizonDays {
			inside = append(inside, e)
		}
	}
	if len(inside) >= minExpiries {
		return inside
	}
	if len(ordered) <= minExpiries {
		return ordered
	}
	return ordered[:minExpiries]
}

func weatherState(netGEX, spot float64, gammaFlip *float64, nearPct, farPct float64) string {
	positive := netGEX >= 0
	if gammaFlip == nil || math.IsNaN(spot) || math.IsInf(spot, 0) || spot <= 0 {
		if positive {
			return weatherClear
		}
		return weatherStorm
	}
	distance := math.Abs(*gammaFlip-spot) / spot * 100.0
	if distance <= nearPct {
		return weatherUnsettled
	}
	if positive {
		if distance >= farPct {
			return weatherClear
		}
		return weatherFair
	}
	if distance >= farPct {
		return weatherStorm
	}
	return weatherRain
}

// computeGammaWeatherPresolved matches metrics.gamma_weather on rows that already ran withSolvedIV.
func computeGammaWeatherPresolved(rows []StoredOptionRow, r, q float64) *GammaWeatherResult {
	if len(rows) == 0 {
		return nil
	}
	spot := rows[0].UnderlyingPrice
	if !(spot > 0) || math.IsNaN(spot) {
		return nil
	}
	moment := rows[0].CollectedAt
	expSet := make(map[string]struct{})
	for i := range rows {
		e := rows[i].Expiry
		if len(e) >= 10 {
			e = e[:10]
		}
		expSet[e] = struct{}{}
	}
	uniqueExp := make([]string, 0, len(expSet))
	for e := range expSet {
		uniqueExp = append(uniqueExp, e)
	}
	chosen := weatherExpiries(uniqueExp, moment, gammaWeatherHorizonDays, gammaWeatherMinExpiries)
	if len(chosen) == 0 {
		return nil
	}
	allowed := make(map[string]bool, len(chosen))
	for _, e := range chosen {
		allowed[e] = true
	}
	mat := buildExposureMatrixPresolved(rows, "gex", allowed, r, q)
	if len(mat.Strikes) == 0 {
		return nil
	}
	netGEX := mat.sumAll()
	flip := gammaFlipFromMatrix(mat, spot)
	callWall, putWall := dealerWalls(rows, allowed)

	// mat.Strikes is descending, matching matrix.sum(axis=1) in Python.
	profile := make([][2]float64, 0, len(mat.Strikes))
	for si, k := range mat.Strikes {
		s := 0.0
		for _, v := range mat.Cells[si] {
			if !math.IsNaN(v) {
				s += v
			}
		}
		if !math.IsNaN(s) && !math.IsInf(s, 0) {
			profile = append(profile, [2]float64{k, s})
		}
	}
	maxChosen := chosen[len(chosen)-1]
	horizonDTE := calcDTE(maxChosen, moment)
	var flipDist *float64
	if flip != nil {
		d := math.Abs(*flip-spot) / spot * 100.0
		flipDist = &d
	}
	return &GammaWeatherResult{
		CollectedAt:     moment,
		Spot:            spot,
		NetGEX:          netGEX,
		CallWall:        callWall,
		PutWall:         putWall,
		GammaFlip:       flip,
		FlipDistancePct: flipDist,
		State:           weatherState(netGEX, spot, flip, gammaWeatherNearFlipPct, gammaWeatherFarFlipPct),
		ExpiryCount:     len(chosen),
		HorizonDTE:      horizonDTE,
		Expiries:        chosen,
		GEXByStrike:     profile,
	}
}

// ivWeightedAveragePresolved matches metrics.iv_weighted_average on rows that already ran withSolvedIV.
func ivWeightedAveragePresolved(rows []StoredOptionRow) *float64 {
	if len(rows) == 0 {
		return nil
	}
	colDate := ""
	if len(rows[0].CollectedAt) >= 10 {
		colDate = rows[0].CollectedAt[:10]
	}
	num := 0.0
	den := 0.0
	for i := range rows {
		r := &rows[i]
		exp := r.Expiry
		if len(exp) >= 10 {
			exp = exp[:10]
		}
		if colDate != "" && exp < colDate {
			continue
		}
		if isValidFinite(r.IV) {
			vol := valOrZero(r.Volume)
			if vol > 0 {
				num += (*r.IV) * vol
				den += vol
			}
		}
	}
	if den <= 0 {
		return nil
	}
	return floatPtr(num / den)
}

// --- Formatting helpers matching app/presentation.py ---

func fmtCompact(v *float64, signed bool, digits int) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	val := *v
	sign := ""
	if signed && val > 0 {
		sign = "+"
	} else if val < 0 {
		sign = "-"
	}
	mag := math.Abs(val)
	type unit struct {
		limit  float64
		suffix string
	}
	for _, u := range []unit{{1e12, "T"}, {1e9, "B"}, {1e6, "M"}, {1e3, "K"}} {
		if mag >= u.limit {
			return fmt.Sprintf("%s%.*f%s", sign, digits, mag/u.limit, u.suffix)
		}
	}
	return fmt.Sprintf("%s%.0f", sign, mag)
}

func fmtCompactVal(val float64, signed bool, digits int) string {
	return fmtCompact(&val, signed, digits)
}

func fmtCellCompact(val float64) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return ""
	}
	if val == 0 {
		return "·"
	}
	return fmtCompactVal(val, false, 1)
}

func fmtPrice(v *float64, digits int) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return fmtPriceVal(*v, digits)
}

func fmtPriceVal(val float64, digits int) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return "—"
	}
	neg := val < 0
	mag := math.Abs(val)
	raw := fmt.Sprintf("%.*f", digits, mag)
	parts := strings.SplitN(raw, ".", 2)
	intPart := parts[0]
	if len(intPart) > 3 {
		var b strings.Builder
		rem := len(intPart) % 3
		if rem > 0 {
			b.WriteString(intPart[:rem])
			if len(intPart) > rem {
				b.WriteByte(',')
			}
		}
		for i := rem; i < len(intPart); i += 3 {
			if i > rem {
				b.WriteByte(',')
			}
			b.WriteString(intPart[i : i+3])
		}
		intPart = b.String()
	}
	prefix := ""
	if neg {
		prefix = "-"
	}
	if len(parts) == 2 {
		return prefix + intPart + "." + parts[1]
	}
	return prefix + intPart
}

func fmtStrikeLabel(v *float64) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return fmtStrikeVal(*v)
}

func fmtStrikeVal(val float64) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return "—"
	}
	return strconv.FormatFloat(val, 'g', 6, 64)
}

func fmtPct(v *float64, digits int, signed bool) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return fmtPctVal(*v, digits, signed)
}

func fmtPctVal(val float64, digits int, signed bool) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return "—"
	}
	if signed {
		return fmt.Sprintf("%+.*f%%", digits, val*100.0)
	}
	return fmt.Sprintf("%.*f%%", digits, val*100.0)
}

func fmtExpiryShort(expiryISO string) string {
	if len(expiryISO) < 10 {
		return expiryISO
	}
	t, err := time.Parse("2006-01-02", expiryISO[:10])
	if err != nil {
		return expiryISO
	}
	return fmt.Sprintf("%s %s", t.Format("01-02"), weekdaysCNByGoWeekday[t.Weekday()])
}

func calcDTE(expiryISO, moment string) int {
	if len(expiryISO) < 10 || len(moment) < 10 {
		return 0
	}
	eDate, err1 := time.Parse("2006-01-02", expiryISO[:10])
	mDate, err2 := time.Parse("2006-01-02", moment[:10])
	if err1 != nil || err2 != nil {
		return 0
	}
	return int(eDate.Sub(mDate).Hours() / 24)
}

var (
	coreSHAOnce sync.Once
	cachedSHA   string
)

// computeCoreSHA computes the exact same SHA-256 over metrics_core.py, metrics.py,
// and day_summary.py as app/day_summary.py:CODE_SHA.
func computeCoreSHA(root string) string {
	coreSHAOnce.Do(func() {
		h := sha256.New()
		for _, name := range []string{"metrics_core.py", "metrics.py", "day_summary.py"} {
			b, err := os.ReadFile(filepath.Join(root, "app", name))
			if err == nil {
				_, _ = h.Write(b)
			}
		}
		cachedSHA = hex.EncodeToString(h.Sum(nil))
	})
	return cachedSHA
}
