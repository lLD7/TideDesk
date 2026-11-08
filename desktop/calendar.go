package main

import (
	"strings"
	"time"
)

func nyDay(t time.Time) time.Time {
	ny := t.In(marketZone)
	return time.Date(ny.Year(), ny.Month(), ny.Day(), 0, 0, 0, 0, marketZone)
}

func parseSessionBounds(day time.Time, hrs SessionHours) (time.Time, time.Time, bool) {
	dayStr := day.In(marketZone).Format("2006-01-02")
	openAt, err1 := localTime(dayStr, hrs.Open)
	closeAt, err2 := localTime(dayStr, hrs.Close)
	if err1 != nil || err2 != nil {
		return time.Time{}, time.Time{}, false
	}
	return openAt, closeAt, true
}

func parseAnyTime(raw string) (time.Time, bool) {
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
func nyseSessionHours(day time.Time) (SessionHours, bool) {
	y, m, d := day.In(marketZone).Date()
	date := time.Date(y, m, d, 0, 0, 0, 0, marketZone)
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
func nyseCalendarWindow(center time.Time, backDays, forwardDays int) map[string]SessionHours {
	base := nyDay(center)
	out := make(map[string]SessionHours, backDays+forwardDays+1)
	for offset := -backDays; offset <= forwardDays; offset++ {
		d := base.AddDate(0, 0, offset)
		if hrs, ok := nyseSessionHours(d); ok {
			out[d.Format("2006-01-02")] = hrs
		}
	}
	return out
}

// lastCompletedTradingDay returns the most recent NYSE trading day whose close
// has already passed as of now, matching market_calendar.last_completed_trading_day.
func lastCompletedTradingDay(now time.Time) string {
	ny := now.In(marketZone)
	day := nyDay(ny)
	if hrs, ok := nyseSessionHours(day); ok {
		_, closeAt, okBounds := parseSessionBounds(day, hrs)
		if okBounds && !ny.Before(closeAt) {
			return day.Format("2006-01-02")
		}
	}
	for i := 1; i <= 14; i++ {
		prev := day.AddDate(0, 0, -i)
		if _, ok := nyseSessionHours(prev); ok {
			return prev.Format("2006-01-02")
		}
	}
	return day.AddDate(0, 0, -1).Format("2006-01-02")
}

// marketDayString returns the YYYY-MM-DD date in America/New_York for a stored timestamp string.
func marketDayString(stamp string) string {
	t, ok := parseAnyTime(stamp)
	if !ok {
		if len(stamp) >= 10 {
			return stamp[:10]
		}
		return stamp
	}
	return t.In(marketZone).Format("2006-01-02")
}

// sessionStartedAt reports whether the given timestamp occurred on a valid NYSE
// trading day at or after the market open (09:30 ET), matching market_calendar.session_started.
func sessionStartedAt(stamp string) bool {
	t, ok := parseAnyTime(stamp)
	if !ok {
		return false
	}
	ny := t.In(marketZone)
	day := nyDay(ny)
	hrs, isSession := nyseSessionHours(day)
	if !isSession {
		return false
	}
	openAt, _, okBounds := parseSessionBounds(day, hrs)
	if !okBounds {
		return false
	}
	return !ny.Before(openAt)
}

// marketStateFromClock returns "open" if the timestamp falls within NYSE regular
// session hours [open, close), or "closed" otherwise, matching market_calendar.state_from_clock.
func marketStateFromClock(stamp string) string {
	t, ok := parseAnyTime(stamp)
	if !ok {
		return "closed"
	}
	ny := t.In(marketZone)
	day := nyDay(ny)
	hrs, isSession := nyseSessionHours(day)
	if !isSession {
		return "closed"
	}
	openAt, closeAt, okBounds := parseSessionBounds(day, hrs)
	if !okBounds {
		return "closed"
	}
	if !ny.Before(openAt) && ny.Before(closeAt) {
		return "open"
	}
	return "closed"
}
