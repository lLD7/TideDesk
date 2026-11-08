package viewmodel

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Python's weekday() is Monday=0 .. Sunday=6; Go's time.Weekday() is Sunday=0 .. Saturday=6.
var WeekdaysCN = []string{"日", "一", "二", "三", "四", "五", "六"}

// --- Formatting helpers matching app/presentation.py ---

func FmtCompact(v *float64, signed bool, digits int) string {
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

func FmtCompactVal(val float64, signed bool, digits int) string {
	return FmtCompact(&val, signed, digits)
}

func FmtCellCompact(val float64) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return ""
	}
	if val == 0 {
		return "·"
	}
	return FmtCompactVal(val, false, 1)
}

func FmtPrice(v *float64, digits int) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return FmtPriceVal(*v, digits)
}

func FmtPriceVal(val float64, digits int) string {
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

func FmtStrikeLabel(v *float64) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return FmtStrikeVal(*v)
}

func FmtStrikeVal(val float64) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return "—"
	}
	return strconv.FormatFloat(val, 'g', 6, 64)
}

func FmtPct(v *float64, digits int, signed bool) string {
	if v == nil || math.IsNaN(*v) || math.IsInf(*v, 0) {
		return "—"
	}
	return FmtPctVal(*v, digits, signed)
}

func FmtPctVal(val float64, digits int, signed bool) string {
	if math.IsNaN(val) || math.IsInf(val, 0) {
		return "—"
	}
	if signed {
		return fmt.Sprintf("%+.*f%%", digits, val*100.0)
	}
	return fmt.Sprintf("%.*f%%", digits, val*100.0)
}

func FmtExpiryShort(expiryISO string) string {
	if len(expiryISO) < 10 {
		return expiryISO
	}
	t, err := time.Parse("2006-01-02", expiryISO[:10])
	if err != nil {
		return expiryISO
	}
	return fmt.Sprintf("%s %s", t.Format("01-02"), WeekdaysCN[t.Weekday()])
}

func CalcDTE(expiryISO, moment string) int {
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
