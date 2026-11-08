package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/provider"
	"gammagrid.local/desktop/internal/store"
)

func (s *DesktopService) schedule(ctx context.Context) (marketclock.ScheduleConfig, error) {
	raw, e := s.store.Setting(ctx, "desktop_schedule_v1")
	if e != nil {
		return marketclock.ScheduleConfig{}, e
	}
	cfg := marketclock.DefaultSchedule()
	if raw != "" {
		if e = json.Unmarshal([]byte(raw), &cfg); e != nil {
			return cfg, e
		}
	}
	cfg = cfg.Effective(s.now())
	if e = marketclock.ValidateSchedule(cfg, s.now()); e != nil {
		return cfg, e
	}
	return cfg, nil
}
func (s *DesktopService) saveSchedule(ctx context.Context, cfg marketclock.ScheduleConfig) error {
	if e := marketclock.ValidateSchedule(cfg, s.now()); e != nil {
		return e
	}
	sort.Strings(cfg.Times)
	b, _ := json.Marshal(cfg)
	err := s.store.Transaction(ctx, func(tx *sql.Tx) error {
		if e := store.SetSetting(tx, "desktop_schedule_v1", string(b)); e != nil {
			return e
		}
		// The legacy browser scheduler must not silently resume an old interval.
		return store.SetSetting(tx, "collector_interval_minutes", "0")
	})
	if err == nil {
		s.wakeScheduler()
	}
	return err
}

func (s *DesktopService) calendar(_ context.Context) (map[string]marketclock.SessionHours, error) {
	return marketclock.Window(s.now(), 45, 45), nil
}
func (s *DesktopService) wakeScheduler() {
	select {
	case s.scheduleWake <- struct{}{}:
	default:
	}
}

func activeHighFreq(cfg store.TickerCollectConfig, now time.Time) bool {
	until, err := time.Parse(time.RFC3339, cfg.HighFreqUntil)
	return cfg.HighFreqEnabled && err == nil && now.Before(until)
}

func (s *DesktopService) saveTickerConfig(ctx context.Context, cfg store.TickerCollectConfig) error {
	ticker, err := provider.NormalizeSymbol(cfg.Ticker)
	if err != nil {
		return err
	}
	old, err := s.store.TickerCollectConfig(ctx, ticker)
	if err != nil {
		return err
	}
	now := s.now()
	if cfg.HighFreqMinutes <= 0 {
		cfg.HighFreqMinutes = old.HighFreqMinutes
	}
	cfg.HighFreqMinutes = min(390, cfg.HighFreqMinutes)
	cfg.HighFreqUntil = ""
	if cfg.HighFreqEnabled {
		if activeHighFreq(old, now) && old.HighFreqMinutes == cfg.HighFreqMinutes {
			cfg.HighFreqUntil = old.HighFreqUntil
		} else {
			for day := now.In(marketclock.Zone); ; day = day.AddDate(0, 0, 1) {
				hours, ok := marketclock.Session(day)
				if !ok {
					continue
				}
				open, closeAt, _ := marketclock.SessionBounds(day, hours)
				if !now.Before(closeAt) {
					continue
				}
				start := now
				if start.Before(open) {
					start = open
				}
				until := start.Add(time.Duration(cfg.HighFreqMinutes) * time.Minute)
				if until.After(closeAt) {
					until = closeAt
				}
				cfg.HighFreqUntil = until.Format(time.RFC3339)
				break
			}
		}
	}
	if err := s.store.SaveTickerCollectConfig(ctx, cfg); err != nil {
		return err
	}
	s.wakeScheduler()
	return nil
}

func earlier(a, b time.Time) time.Time {
	if a.IsZero() || (!b.IsZero() && b.Before(a)) {
		return b
	}
	return a
}

// Next clock event, including a premarket wake and earlier custom fixed slots.
func nextScheduleWake(now time.Time, cfg marketclock.ScheduleConfig) time.Time {
	now = now.In(marketclock.Zone)
	cfg = cfg.Effective(now)
	if cfg.Mode == "manual" {
		return time.Time{}
	}
	var next time.Time
	if cfg.Mode == "temporary" {
		next, _ = time.Parse(time.RFC3339, cfg.Until)
	}
	for offset := 0; offset < 45; offset++ {
		day := now.AddDate(0, 0, offset)
		hours, ok := marketclock.Session(day)
		if !ok {
			continue
		}
		open, closeAt, _ := marketclock.SessionBounds(day, hours)
		if prep := open.Add(-5 * time.Minute); prep.After(now) {
			next = earlier(next, prep)
		}
		if cfg.Mode == "fixed" {
			for _, clock := range cfg.Times {
				at, err := time.ParseInLocation("2006-01-02 15:04", day.Format("2006-01-02")+" "+clock, marketclock.Zone)
				if err == nil && at.After(now) {
					next = earlier(next, at)
				}
			}
		} else {
			at := open
			if !now.Before(open) {
				period := time.Duration(cfg.Minutes) * time.Minute
				at = now.Truncate(period).Add(period)
			}
			if at.Before(closeAt) {
				next = earlier(next, at)
			}
		}
		if !next.IsZero() {
			break
		}
	}
	return next
}

func (s *DesktopService) scheduler() {
	defer s.wg.Done()
	var lastSlot string
	var cfg marketclock.ScheduleConfig
	var tickers map[string]store.TickerCollectConfig
	reload := true
	for s.ctx.Err() == nil {
		if reload {
			var err error
			cfg, err = s.schedule(s.ctx)
			if err != nil {
				s.setStatus("error", err.Error())
				cfg.Mode = "manual"
			}
			watch, err := s.store.Watchlist(s.ctx)
			if err == nil {
				tickers, err = s.store.TickerCollectConfigs(s.ctx, watch)
			}
			if err != nil {
				s.setStatus("error", err.Error())
			}
		}
		now := s.now()
		wakeAt := nextScheduleWake(now, cfg)
		s.cleanupIntraday(marketclock.Day(now).Format("2006-01-02"), tickers)
		day := now.In(marketclock.Zone).Format("2006-01-02")
		hours, trading := marketclock.Session(now)
		if trading {
			sessions := map[string]marketclock.SessionHours{day: hours}
			if key, due := marketclock.DueSlot(now, cfg, sessions); due && key != lastSlot && s.runScheduleSlot(key) {
				lastSlot = key
			}
		}
		for ticker, config := range tickers {
			wakeAt = earlier(wakeAt, s.runHighFreqIfDue(s.ctx, ticker, config))
		}
		reload = false
		var timer *time.Timer
		var due <-chan time.Time
		if !wakeAt.IsZero() {
			timer = time.NewTimer(max(0, wakeAt.Sub(s.now())))
			due = timer.C
		}
		select {
		case <-s.ctx.Done():
		case <-s.scheduleWake:
			reload = true
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

func (s *DesktopService) runHighFreqIfDue(ctx context.Context, ticker string, cfg store.TickerCollectConfig) time.Time {
	now := s.now()
	if !activeHighFreq(cfg, now) {
		return time.Time{}
	}
	until, _ := time.Parse(time.RFC3339, cfg.HighFreqUntil)
	hours, trading := marketclock.Session(until)
	if !trading {
		return time.Time{}
	}
	open, closeAt, _ := marketclock.SessionBounds(until, hours)
	if !now.Before(closeAt) {
		return time.Time{}
	}
	if now.Before(open) {
		if prep := open.Add(-5 * time.Minute); prep.After(now) {
			return earlier(prep, until)
		}
		return earlier(open, until)
	}
	interval := time.Duration(cfg.HighFreqSec) * time.Second
	s.intradayMu.Lock()
	last := s.lastHighFreqAt[ticker]
	retryAt := s.highFreqRetry[ticker]
	s.intradayMu.Unlock()
	next := last.Add(interval)
	if retryAt.After(next) {
		next = retryAt
	}
	if next.After(now) {
		return earlier(next, earlier(until, closeAt))
	}
	if !s.collectMu.TryLock() {
		// The manual collection signals the scheduler when it releases the lock.
		return earlier(until, closeAt)
	}
	until = earlier(until, closeAt)
	task, cancel := context.WithTimeout(ctx, until.Sub(now))
	err := s.collectHighFreqLocked(task, ticker)
	cancel()
	s.collectMu.Unlock()
	finished := s.now()
	if err != nil {
		next = finished.Add(max(interval, 5*time.Minute))
		s.intradayMu.Lock()
		s.highFreqRetry[ticker] = next
		s.intradayMu.Unlock()
	} else {
		next = finished.Add(interval)
	}
	return earlier(next, earlier(until, closeAt))
}
func (s *DesktopService) scheduleTick() {
	cfg, e := s.schedule(s.ctx)
	if e != nil {
		s.setStatus("error", e.Error())
		return
	}
	if cfg.Mode == "manual" {
		return
	}
	sessions, e := s.calendar(s.ctx)
	if e != nil {
		s.setStatus("error", "交易日历暂不可用: "+e.Error())
		return
	}
	key, due := marketclock.DueSlot(s.now(), cfg, sessions)
	if !due {
		return
	}
	s.runScheduleSlot(key)
}

func (s *DesktopService) runScheduleSlot(key string) bool {
	// One pass at a time, including manual actions and maintenance.
	if !s.collectMu.TryLock() {
		return false
	}
	defer s.collectMu.Unlock()
	claimed := false
	e := s.store.Transaction(s.ctx, func(tx *sql.Tx) error {
		res, e := tx.ExecContext(s.ctx, `INSERT OR IGNORE INTO desktop_schedule_runs(slot_key,started_at,status) VALUES(?,?,'running')`, key, marketclock.Stamp(s.now()))
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		claimed = n == 1
		return e
	})
	if e != nil || !claimed {
		return e == nil
	}
	results, e := s.collectScheduledLocked(s.ctx)
	status := "success"
	if e != nil {
		status = "failed"
	}
	for _, v := range results {
		if !strings.HasPrefix(v, "success") && !strings.HasPrefix(v, "skipped") {
			status = "failed"
		}
	}
	_ = s.store.Transaction(s.ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(s.ctx, "UPDATE desktop_schedule_runs SET finished_at=?,status=? WHERE slot_key=?", marketclock.Stamp(s.now()), status, key)
		return e
	})
	return true
}
