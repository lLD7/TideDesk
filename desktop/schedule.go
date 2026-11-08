package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

var marketZone = func() *time.Location {
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

func defaultSchedule() ScheduleConfig {
	return ScheduleConfig{Mode: "fixed", Times: []string{"10:00", "12:00", "14:00", "16:15"}, Minutes: 15}
}
func (cfg ScheduleConfig) effective(now time.Time) ScheduleConfig {
	if cfg.Mode == "temporary" {
		until, e := time.Parse(time.RFC3339, cfg.Until)
		if e != nil || !now.Before(until) {
			cfg.Mode = "fixed"
			cfg.Until = ""
		}
	}
	return cfg
}
func validateSchedule(cfg ScheduleConfig, now time.Time) error {
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
func (s *DesktopService) schedule(ctx context.Context) (ScheduleConfig, error) {
	raw, e := s.store.setting(ctx, "desktop_schedule_v1")
	if e != nil {
		return ScheduleConfig{}, e
	}
	cfg := defaultSchedule()
	if raw != "" {
		if e = json.Unmarshal([]byte(raw), &cfg); e != nil {
			return cfg, e
		}
	}
	cfg = cfg.effective(s.now())
	if e = validateSchedule(cfg, s.now()); e != nil {
		return cfg, e
	}
	return cfg, nil
}
func (s *DesktopService) saveSchedule(ctx context.Context, cfg ScheduleConfig) error {
	if e := validateSchedule(cfg, s.now()); e != nil {
		return e
	}
	sort.Strings(cfg.Times)
	b, _ := json.Marshal(cfg)
	return s.store.transaction(ctx, func(tx *sql.Tx) error {
		if e := setSetting(tx, "desktop_schedule_v1", string(b)); e != nil {
			return e
		}
		// The legacy browser scheduler must not silently resume an old interval.
		return setSetting(tx, "collector_interval_minutes", "0")
	})
}

type SessionHours struct {
	Open  string `json:"open"`
	Close string `json:"close"`
}

func localTime(day, clock string) (time.Time, error) {
	return time.ParseInLocation("2006-01-02 15:04", day+" "+clock, marketZone)
}

// No backlog replay: a wake-up may take only the latest slot within 15 minutes.
func dueSlot(now time.Time, cfg ScheduleConfig, sessions map[string]SessionHours) (string, bool) {
	cfg = cfg.effective(now)
	if cfg.Mode == "manual" {
		return "", false
	}
	day := now.In(marketZone).Format("2006-01-02")
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
func nextSlot(now time.Time, cfg ScheduleConfig, sessions map[string]SessionHours) string {
	cfg = cfg.effective(now)
	if cfg.Mode == "manual" {
		return "仅手动"
	}
	if cfg.Mode == "interval" {
		return fmt.Sprintf("盘中每 %d 分钟自动低频采集", cfg.Minutes)
	}
	if cfg.Mode == "temporary" {
		until, _ := time.Parse(time.RFC3339, cfg.Until)
		return fmt.Sprintf("临时每 %d 分钟，至 %s", cfg.Minutes, until.In(marketZone).Format("15:04 MST"))
	}
	for d := 0; d < 35; d++ {
		day := now.In(marketZone).AddDate(0, 0, d).Format("2006-01-02")
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
func (s *DesktopService) calendar(_ context.Context) (map[string]SessionHours, error) {
	return nyseCalendarWindow(s.now(), 45, 45), nil
}
func (s *DesktopService) scheduler() {
	defer s.wg.Done()
	timer := time.NewTicker(5 * time.Second)
	defer timer.Stop()
	for {
		s.scheduleTick()
		s.highFreqTick()
		select {
		case <-s.ctx.Done():
			return
		case <-timer.C:
		}
	}
}
func (s *DesktopService) highFreqTick() {
	watch, err := s.store.watchlist(s.ctx)
	if err != nil || len(watch) == 0 {
		return
	}
	now := s.now()
	for _, ticker := range watch {
		if s.ctx.Err() != nil {
			return
		}
		cfg, err := s.store.tickerCollectConfig(s.ctx, ticker)
		if err != nil || !cfg.HighFreqEnabled {
			continue
		}
		sec := normalizeHighFreqSec(cfg.HighFreqSec)
		s.intradayMu.Lock()
		lastAt := s.lastHighFreqAt[ticker]
		s.intradayMu.Unlock()
		if !lastAt.IsZero() && now.Sub(lastAt) < time.Duration(sec)*time.Second {
			continue
		}
		_ = s.collectHighFreqTick(s.ctx, ticker)
	}
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
	key, due := dueSlot(s.now(), cfg, sessions)
	if !due {
		return
	}
	// One pass at a time, including manual actions and maintenance.
	if !s.collectMu.TryLock() {
		return
	}
	defer s.collectMu.Unlock()
	claimed := false
	e = s.store.transaction(s.ctx, func(tx *sql.Tx) error {
		res, e := tx.ExecContext(s.ctx, `INSERT OR IGNORE INTO desktop_schedule_runs(slot_key,started_at,status) VALUES(?,?,'running')`, key, stamp(s.now()))
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		claimed = n == 1
		return e
	})
	if e != nil || !claimed {
		return
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
	_ = s.store.transaction(s.ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(s.ctx, "UPDATE desktop_schedule_runs SET finished_at=?,status=? WHERE slot_key=?", stamp(s.now()), status, key)
		return e
	})
}
