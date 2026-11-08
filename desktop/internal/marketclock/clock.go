package marketclock

import (
	"fmt"
	"strings"
	"time"
)

var Zone = func() *time.Location {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(err)
	}
	return loc
}()

type ScheduleConfig struct {
	Mode    string   `json:"mode"`
	Times   []string `json:"times"`
	Minutes int      `json:"minutes"`
	Until   string   `json:"until"`
}

func DefaultSchedule() ScheduleConfig {
	return ScheduleConfig{Mode: "fixed", Times: []string{"10:00", "12:00", "14:00", "16:15"}, Minutes: 15}
}
func (cfg ScheduleConfig) Effective(now time.Time) ScheduleConfig {
	if cfg.Mode == "temporary" {
		until, e := time.Parse(time.RFC3339, cfg.Until)
		if e != nil || !now.Before(until) {
			cfg.Mode = "fixed"
			cfg.Until = ""
		}
	}
	return cfg
}
func ValidateSchedule(cfg ScheduleConfig, now time.Time) error {
	if cfg.Mode != "fixed" && cfg.Mode != "manual" && cfg.Mode != "temporary" && cfg.Mode != "interval" {
		return fmt.Errorf("请选择固定时点、定频采集、仅手动或临时频率")
	}
	if len(cfg.Times) < 1 || len(cfg.Times) > 12 {
		return fmt.Errorf("请输入 1–12 个美东采集时点")
	}
	seen := map[string]bool{}
	for _, v := range cfg.Times {
		t, e := time.Parse("15:04", v)
		if e != nil || t.Format("15:04") != v || seen[v] {
			return fmt.Errorf("时点须为不重复的 HH:MM，例如 10:00")
		}
		seen[v] = true
	}
	if cfg.Mode == "interval" {
		if cfg.Minutes != 15 && cfg.Minutes != 30 && cfg.Minutes != 60 && cfg.Minutes != 120 && cfg.Minutes != 240 {
			return fmt.Errorf("低频定频支持 15、30、60、120、240 分钟")
		}
	}
	if cfg.Mode == "temporary" {
		if cfg.Minutes != 15 && cfg.Minutes != 30 && cfg.Minutes != 60 {
			return fmt.Errorf("临时频率支持 15、30、60 分钟")
		}
		until, e := time.Parse(time.RFC3339, cfg.Until)
		if e != nil || !until.After(now) || until.Sub(now) > 12*time.Hour {
			return fmt.Errorf("临时采集时长须在 12 小时以内")
		}
	}
	return nil
}

type SessionHours struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

func localTime(day, clock string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04", day+" "+clock, Zone)
}

// No backlog replay: a wake-up may take only the latest slot within 15 minutes.
func DueSlot(now time.Time, cfg ScheduleConfig, sessions map[string]SessionHours) (string, bool) {
	cfg = cfg.Effective(now)
	if cfg.Mode == "manual" {
		return "", false
	}
	day := now.In(Zone).Format("2006-01-02")
	hours, ok := sessions[day]
	if !ok {
		return "", false
	}
	if cfg.Mode == "temporary" || cfg.Mode == "interval" {
		open, _ := localTime(day, hours.Open)
		closeAt, _ := localTime(day, hours.Close)
		if now.Before(open) || !now.Before(closeAt) {
			return "", false
		}
		mins := max(1, cfg.Minutes)
		slot := now.Unix() / int64(mins*60)
		scope := cfg.Until
		if cfg.Mode == "interval" {
			scope = "interval"
		}
		return fmt.Sprintf("interval/%s/%d/%d", scope, mins, slot), true
	}
	var latest time.Time
	for _, clock := range cfg.Times {
		at, e := localTime(day, clock)
		if e == nil && !at.After(now) && at.After(latest) {
			latest = at
		}
	}
	if latest.IsZero() || now.Sub(latest) >= 15*time.Minute {
		return "", false
	}
	return "fixed/" + latest.Format(time.RFC3339), true
}
func NextSlot(now time.Time, cfg ScheduleConfig, sessions map[string]SessionHours) string {
	cfg = cfg.Effective(now)
	if cfg.Mode == "manual" {
		return "仅手动"
	}
	if cfg.Mode == "interval" {
		return fmt.Sprintf("盘中每 %d 分钟自动低频采集", cfg.Minutes)
	}
	if cfg.Mode == "temporary" {
		until, _ := time.Parse(time.RFC3339, cfg.Until)
		return fmt.Sprintf("临时每 %d 分钟，至 %s", cfg.Minutes, until.In(Zone).Format("15:04 MST"))
	}
	for d := 0; d < 35; d++ {
		day := now.In(Zone).AddDate(0, 0, d).Format("2006-01-02")
		if _, ok := sessions[day]; !ok {
			continue
		}
		for _, clock := range cfg.Times {
			at, e := localTime(day, clock)
			if e == nil && at.After(now) {
				return at.Format("01-02 15:04 MST")
			}
		}
	}
	return "等待交易日历"
}
func ParseStamp(v string) (time.Time, error) {
	for _, layout := range []string{StampLayout, "2006-01-02 15:04:05", time.RFC3339Nano, "2006-01-02T15:04:05.999999"} {
		if t, e := time.Parse(layout, v); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", v)
}

const StampLayout = "2006-01-02 15:04:05.000000"

func Stamp(t time.Time) string { return t.UTC().Format(StampLayout) }
func IsClosedMarketRefusal(startedAt, errorMessage string) bool {
	return errorMessage != "" &&
		strings.Contains(errorMessage, "of contracts have open_interest=0") &&
		MarketState(startedAt) == "closed"
}

func Day(t time.Time) time.Time {
	ny := t.In(Zone)
	return time.Date(ny.Year(), ny.Month(), ny.Day(), 0, 0, 0, 0, Zone)
}

func SessionBounds(day time.Time, hrs SessionHours) (time.Time, time.Time, bool) {
	dayStr := day.In(Zone).Format("2006-01-02")
	openAt, err1 := localTime(dayStr, hrs.Open)
	closeAt, err2 := localTime(dayStr, hrs.Close)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return openAt, closeAt, true
}

func ParseTime(raw string) (time.Time, bool) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999",
		"2006-01-02T15:04:05",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// nyseSessionHours returns the NYSE regular trading session hours ("09:30" to
// "16:00", or "13:00" on early-close days) for the given New York calendar date,
// or false if the exchange is closed (weekends, holidays, and special closures).
func Session(day time.Time) (SessionHours, bool) {
	y, m, d := day.In(Zone).Date()
	date := time.Date(y, m, d, 0, 0, 0, 0, Zone)
	wd := date.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return SessionHours{}, false
	}
	if isNYSEHoliday(y, m, d, wd) {
		return SessionHours{}, false
	}
	if isNYSEEarlyClose(y, m, d, wd) {
		return SessionHours{Open: "09:30", Close: "13:00"}, true
	}
	return SessionHours{Open: "09:30", Close: "16:00"}, true
}

func isNYSEHoliday(y int, m time.Month, d int, wd time.Weekday) bool {
	// One-off historical/special closures recorded by XNYS.
	if y == 2025 && m == time.January && d == 9 {
		return true
	}

	// 1. New Year's Day (Jan 1; observed Monday Jan 2 when Jan 1 is Sunday;
	// Saturday Jan 1 is not observed on Friday Dec 31 per NYSE Rule 7.2).
	if m == time.January && ((d == 1 && wd != time.Saturday && wd != time.Sunday) || (d == 2 && wd == time.Monday)) {
		return true
	}

	// 2. Martin Luther King, Jr. Day (3rd Monday of January).
	if m == time.January && wd == time.Monday && d >= 15 && d <= 21 {
		return true
	}

	// 3. Washington's Birthday / Presidents' Day (3rd Monday of February).
	if m == time.February && wd == time.Monday && d >= 15 && d <= 21 {
		return true
	}

	// 4. Good Friday (two days before Easter Sunday).
	gfMonth, gfDay := goodFriday(y)
	if m == gfMonth && d == gfDay {
		return true
	}

	// 5. Memorial Day (last Monday of May).
	if m == time.May && wd == time.Monday && d >= 25 {
		return true
	}

	// 6. Juneteenth National Independence Day (June 19, observed since 2022).
	if y >= 2022 && m == time.June && isObservedFixedHoliday(d, 19, wd) {
		return true
	}

	// 7. Independence Day (July 4).
	if m == time.July && isObservedFixedHoliday(d, 4, wd) {
		return true
	}

	// 8. Labor Day (1st Monday of September).
	if m == time.September && wd == time.Monday && d <= 7 {
		return true
	}

	// 9. Thanksgiving Day (4th Thursday of November).
	if m == time.November && wd == time.Thursday && d >= 22 && d <= 28 {
		return true
	}

	// 10. Christmas Day (December 25).
	if m == time.December && isObservedFixedHoliday(d, 25, wd) {
		return true
	}

	return false
}

func isObservedFixedHoliday(day, holidayDay int, wd time.Weekday) bool {
	if day == holidayDay && wd != time.Saturday && wd != time.Sunday {
		return true
	}
	if day == holidayDay-1 && wd == time.Friday {
		return true
	}
	if day == holidayDay+1 && wd == time.Monday {
		return true
	}
	return false
}

func isNYSEEarlyClose(y int, m time.Month, d int, wd time.Weekday) bool {
	// 1. Day after Thanksgiving (Black Friday: Friday following 4th Thursday of November).
	if m == time.November && wd == time.Friday {
		thuDay := d - 1
		if thuDay >= 22 && thuDay <= 28 {
			return true
		}
	}

	// 2. July 3 when July 4 falls on Tue, Wed, Thu, or Fri (so July 3 is Mon..Thu).
	if m == time.July && d == 3 && (wd >= time.Monday && wd <= time.Thursday) {
		return true
	}

	// 3. Christmas Eve (Dec 24) when Dec 25 falls on Tue, Wed, Thu, or Fri (so Dec 24 is Mon..Thu).
	if m == time.December && d == 24 && (wd >= time.Monday && wd <= time.Thursday) {
		return true
	}

	_ = y
	return false
}

// goodFriday computes the month and day of Good Friday for Gregorian year y
// using the anonymous Gregorian (Meeus/Jones/Butcher) Easter algorithm.
func goodFriday(y int) (time.Month, int) {
	a := y % 19
	b := y / 100
	c := y % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	easterMonth := (h + l - 7*m + 114) / 31
	easterDay := ((h + l - 7*m + 114) % 31) + 1
	easter := time.Date(y, time.Month(easterMonth), easterDay, 0, 0, 0, 0, time.UTC)
	gf := easter.AddDate(0, 0, -2)
	return gf.Month(), gf.Day()
}

// nyseCalendarWindow builds a map of YYYY-MM-DD -> SessionHours for [startDay - backDays, startDay + forwardDays].
func Window(center time.Time, backDays, forwardDays int) map[string]SessionHours {
	base := Day(center)
	out := make(map[string]SessionHours, backDays+forwardDays+1)
	for offset := -backDays; offset <= forwardDays; offset++ {
		d := base.AddDate(0, 0, offset)
		if hrs, ok := Session(d); ok {
			out[d.Format("2006-01-02")] = hrs
		}
	}
	return out
}

// lastCompletedTradingDay returns the most recent NYSE trading day whose close
// has already passed as of now, matching market_calendar.last_completed_trading_day.
func LastCompletedTradingDay(now time.Time) string {
	ny := now.In(Zone)
	day := Day(ny)
	if hrs, ok := Session(day); ok {
		_, closeAt, okBounds := SessionBounds(day, hrs)
		if okBounds && !ny.Before(closeAt) {
			return day.Format("2006-01-02")
		}
	}
	for i := 1; i <= 14; i++ {
		prev := day.AddDate(0, 0, -i)
		if _, ok := Session(prev); ok {
			return prev.Format("2006-01-02")
		}
	}
	return day.AddDate(0, 0, -1).Format("2006-01-02")
}

// marketDayString returns the YYYY-MM-DD date in America/New_York for a stored timestamp string.
func MarketDay(stamp string) string {
	t, ok := ParseTime(stamp)
	if !ok {
		if len(stamp) >= 10 {
			return stamp[:10]
		}
		return stamp
	}
	return t.In(Zone).Format("2006-01-02")
}

// sessionStartedAt reports whether the given timestamp occurred on a valid NYSE
// trading day at or after the market open (09:30 ET), matching market_calendar.session_started.
func SessionStarted(stamp string) bool {
	t, ok := ParseTime(stamp)
	if !ok {
		return false
	}
	ny := t.In(Zone)
	day := Day(ny)
	hrs, isSession := Session(day)
	if !isSession {
		return false
	}
	openAt, _, okBounds := SessionBounds(day, hrs)
	if !okBounds {
		return false
	}
	return !ny.Before(openAt)
}

// marketStateFromClock returns "open" if the timestamp falls within NYSE regular
// session hours [open, close), or "closed" otherwise, matching market_calendar.state_from_clock.
func MarketState(stamp string) string {
	t, ok := ParseTime(stamp)
	if !ok {
		return "closed"
	}
	ny := t.In(Zone)
	day := Day(ny)
	hrs, isSession := Session(day)
	if !isSession {
		return "closed"
	}
	openAt, closeAt, okBounds := SessionBounds(day, hrs)
	if !okBounds {
		return "closed"
	}
	if !ny.Before(openAt) && ny.Before(closeAt) {
		return "open"
	}
	return "closed"
}
