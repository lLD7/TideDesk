package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/provider"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"
)

type DesktopService struct {
	store               *store.Store
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	cache               *responseCache
	viewSlot            chan struct{}
	scheduleWake        chan struct{}
	versionsMu          sync.Mutex
	historyVersion      map[string]uint64
	fetch               func(context.Context, string) (provider.Snapshot, error)
	fetchYahoo          func(context.Context, string) (provider.Snapshot, error)
	yahoo               *provider.YahooClient
	now                 func() time.Time
	collectMu           sync.Mutex
	statusMu            sync.Mutex
	status, statusError string
	intradayMu          sync.Mutex
	intradayBase        map[string]*intradayBaseline
	liveSnapshots       map[string][]store.StoredOptionRow
	liveSizes           map[string]int
	liveBytes           int
	barVersion          map[string]uint64
	lastHighFreqAt      map[string]time.Time
	highFreqRetry       map[string]time.Time
	background          bool
	maintenanceWake     chan struct{}
	maintenanceMu       sync.Mutex
	pendingDerived      map[deriveTarget]struct{}
}

func newDesktopService(database *store.Store, root string) *DesktopService {
	ctx, cancel := context.WithCancel(context.Background())
	yahoo := provider.NewYahooClient(root)
	return &DesktopService{store: database, ctx: ctx, cancel: cancel, cache: newResponseCache(ctx), viewSlot: make(chan struct{}, 1), scheduleWake: make(chan struct{}, 1), historyVersion: make(map[string]uint64), fetch: provider.NewCboeClient(releaseVersion()).Fetch, fetchYahoo: yahoo.Fetch, yahoo: yahoo, now: time.Now, status: "idle",
		liveSnapshots: make(map[string][]store.StoredOptionRow), liveSizes: make(map[string]int), barVersion: make(map[string]uint64),
		lastHighFreqAt: make(map[string]time.Time), highFreqRetry: make(map[string]time.Time),
		maintenanceWake: make(chan struct{}, 1), pendingDerived: make(map[deriveTarget]struct{}),
	}
}
func (s *DesktopService) Start() {
	s.background = true
	s.wg.Add(1)
	go s.maintenanceLoop()
	if os.Getenv("GEX_DISABLE_SCHEDULER") != "1" {
		s.wg.Add(1)
		go s.scheduler()
	}
}
func (s *DesktopService) Close() {
	s.cancel()
	s.wg.Wait()
	s.collectMu.Lock()
	s.collectMu.Unlock()
	s.cache.close()
}
func (s *DesktopService) setStatus(state, message string) {
	s.statusMu.Lock()
	s.status, s.statusError = state, message
	s.statusMu.Unlock()
}

func (s *DesktopService) setWatchlist(ctx context.Context, ticker string, add bool) (string, error) {
	ticker, err := provider.NormalizeSymbol(ticker)
	if err != nil {
		return "", err
	}
	query := "DELETE FROM watchlist WHERE ticker=?"
	if add {
		if ticker == "VIX" || ticker == "VIXW" {
			return "", fmt.Errorf("暂不支持 VIX：其期权标的是期货，不能使用指数现价计算")
		}
		query = "INSERT INTO watchlist(ticker) VALUES(?) ON CONFLICT DO NOTHING"
	}
	err = s.store.Transaction(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, query, ticker); err != nil {
			return err
		}
		if !add {
			_, err := tx.ExecContext(ctx, "DELETE FROM app_settings WHERE key IN (?,?)", "high_freq_enabled:"+ticker, "high_freq_until:"+ticker)
			return err
		}
		return nil
	})
	if err == nil {
		s.wakeScheduler()
	}
	return ticker, err
}

func (s *DesktopService) pin(ctx context.Context, ticker, expiry string, strike float64, side string) error {
	ticker, err := provider.NormalizeSymbol(ticker)
	if err != nil {
		return err
	}
	if _, err = time.Parse("2006-01-02", expiry); err != nil {
		return err
	}
	if !(strike > 0) || (side != "call" && side != "put") {
		return fmt.Errorf("无效合约")
	}
	return s.store.Transaction(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO tracked_contracts(ticker,expiry,strike,option_type) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", ticker, expiry, strike, side)
		return err
	})
}

func (s *DesktopService) backup(ctx context.Context) (string, error) {
	dest := filepath.Join(filepath.Dir(s.store.Path()), "backups", "manual-"+s.now().UTC().Format("20060102-150405.000000")+".db")
	return dest, s.store.Backup(ctx, dest)
}

func (s *DesktopService) state(ctx context.Context) (*viewmodel.AppStateResponse, error) {
	unlock, err := s.lockView(ctx)
	if err != nil {
		return nil, err
	}
	defer unlock()

	watch, e := s.store.Watchlist(ctx)
	if e != nil {
		return nil, e
	}
	cfg, e := s.schedule(ctx)
	if e != nil {
		return nil, e
	}
	out := &viewmodel.AppStateResponse{Version: releaseVersion(), Watchlist: watch, Tickers: []viewmodel.TickerSummary{}, Runs: []viewmodel.RunItem{}, Cooldowns: map[string]string{}, Schedule: cfg, RefreshMode: "manual"}
	s.statusMu.Lock()
	out.CollectorState, out.CollectorError = s.status, s.statusError
	s.statusMu.Unlock()
	configs, e := s.store.TickerCollectConfigs(ctx, watch)
	if e != nil {
		return nil, e
	}
	metas, e := s.store.WatchlistMetas(ctx)
	if e != nil {
		return nil, e
	}
	for _, ticker := range watch {
		m := metas[ticker]
		summary := viewmodel.TickerSummary{Symbol: ticker, Source: m.Source, HasData: m.At != "", NetGEXStr: "—", StateCN: "无数据"}
		if m.At != "" {
			ribbon, err := s.snapshotRibbon(ctx, m, nil)
			if err != nil {
				return nil, err
			}
			summary.Ribbon = ribbon
			summary.NetGEX, summary.NetGEXStr = ribbon.NetGEX, ribbon.NetGEXStr
			summary.State, summary.StateCN = ribbon.State, ribbon.StateCN
			summary.Spot = &m.Spot
			summary.CollectedAt = displayStamp(m.At)
			summary.Since = m.Since[:min(10, len(m.Since))]
		}
		tCfg := configs[ticker]
		summary.LowFreqEnabled = tCfg.LowFreqEnabled
		summary.HighFreqEnabled = activeHighFreq(tCfg, s.now())
		summary.HighFreqSec = tCfg.HighFreqSec
		summary.HighFreqMinutes = tCfg.HighFreqMinutes
		summary.HighFreqUntil = tCfg.HighFreqUntil
		out.Tickers = append(out.Tickers, summary)
	}
	rows, e := s.store.Read().QueryContext(ctx, `SELECT CAST(started_at AS TEXT),ticker,source,status,COALESCE(rows_fetched,0),oi_zero_fraction,COALESCE(error_message,'') FROM collection_runs ORDER BY started_at DESC,id DESC LIMIT 25`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var r viewmodel.RunItem
		var zero sql.NullFloat64
		if e = rows.Scan(&r.StartedAt, &r.Ticker, &r.Source, &r.Status, &r.Rows, &zero, &r.Error); e != nil {
			rows.Close()
			return nil, e
		}
		r.StartedAt = displayStamp(r.StartedAt)
		r.OIZero = "—"
		if zero.Valid {
			r.OIZero = fmt.Sprintf("%.0f%%", zero.Float64*100)
		}
		out.Runs = append(out.Runs, r)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for _, src := range []string{"cboe", "yahoo"} {
		raw, e := s.store.Setting(ctx, "provider_cooldown_until:"+src)
		if e != nil {
			return nil, e
		}
		if t, e := marketclock.ParseStamp(raw); e == nil && t.After(s.now()) {
			out.Cooldowns[src] = t.UTC().Format("15:04 UTC")
		}
	}
	sessions, e := s.calendar(ctx)
	if e == nil {
		out.NextCollection = marketclock.NextSlot(s.now(), cfg, sessions)
	} else {
		out.NextCollection = "交易日历暂不可用"
	}
	return out, nil
}
func displayStamp(v string) string {
	if t, e := marketclock.ParseStamp(v); e == nil {
		return t.In(marketclock.Zone).Format("01-02 15:04 MST")
	}
	return v
}

func (s *DesktopService) Collect(ctx context.Context, ticker string) (map[string]string, error) {
	defer s.wakeScheduler()
	if !s.collectMu.TryLock() {
		return nil, fmt.Errorf("已有采集任务在进行，请等待完成")
	}
	defer s.collectMu.Unlock()
	return s.collectLocked(ctx, ticker)
}
func (s *DesktopService) collectScheduledLocked(ctx context.Context) (map[string]string, error) {
	return s.collectPassLocked(ctx, "", true)
}
func (s *DesktopService) collectLocked(ctx context.Context, ticker string) (map[string]string, error) {
	return s.collectPassLocked(ctx, ticker, false)
}
func (s *DesktopService) collectPassLocked(ctx context.Context, ticker string, respectLowFreqToggle bool) (map[string]string, error) {
	pass, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	tickers, e := s.store.Watchlist(pass)
	if e != nil {
		return nil, e
	}
	if ticker != "" {
		ticker, e = provider.NormalizeSymbol(ticker)
		if e != nil {
			return nil, e
		}
		tickers = []string{ticker}
	}
	configs, e := s.store.TickerCollectConfigs(pass, tickers)
	if e != nil {
		return nil, e
	}
	s.setStatus("collecting", "")
	results := map[string]string{}
	var lastError string
	defer func() {
		if lastError != "" {
			s.setStatus("error", lastError)
		} else {
			s.setStatus("idle", "")
		}
	}()
	for _, t := range tickers {
		if e = pass.Err(); e != nil {
			return results, e
		}
		if respectLowFreqToggle && ticker == "" {
			if !configs[t].LowFreqEnabled {
				results[t] = "skipped: 低频抓取已关闭"
				continue
			}
		}
		var failures []string
		if t == "VIX" || t == "VIXW" {
			results[t] = "failed: 不支持 VIX 期权"
			continue
		}
		candidates := []string{"cboe", "yahoo"}
		switch os.Getenv("GEX_PROVIDER") {
		case "cboe":
			candidates = []string{"cboe"}
		case "yahoo":
			candidates = []string{"yahoo"}
		}
		for _, src := range candidates {
			raw, e := s.store.Setting(pass, "provider_cooldown_until:"+src)
			if e != nil {
				return results, e
			}
			if until, e := marketclock.ParseStamp(raw); e == nil && until.After(s.now()) {
				failures = append(failures, src+": 冷却中")
				continue
			}
			at := s.now()
			var snap provider.Snapshot
			if src == "cboe" {
				snap, e = s.fetch(pass, t)
			} else {
				snap, e = s.fetchYahoo(pass, t)
			}
			zero := 0.0
			if e == nil {
				if len(snap.Rows) == 0 || snap.Spot <= 0 {
					e = fmt.Errorf("空期权链或无效现价")
				} else {
					for _, row := range snap.Rows {
						if row.OI == nil || *row.OI == 0 {
							zero++
						}
					}
					zero /= float64(len(snap.Rows))
					threshold := 0.99
					if src == "yahoo" {
						threshold = 0.5
					}
					if zero > threshold {
						e = fmt.Errorf("%.0f%% of contracts have open_interest=0 (threshold %.0f%%), snapshot refused", zero*100, threshold*100)
					}
				}
			}
			if e != nil {
				var pe *provider.ProviderError
				if errors.As(e, &pe) && pe.Status == 429 {
					if err := s.store.SetSetting(pass, "provider_cooldown_until:"+src, marketclock.Stamp(s.now().Add(max(pe.RetryAfter, 30*time.Minute)))); err != nil {
						return results, err
					}
				}
				if err := s.store.LogFailure(pass, t, src, at, e.Error(), len(snap.Rows), zero); err != nil {
					return results, err
				}
				failures = append(failures, src+": "+e.Error())
				continue
			}
			if e = s.store.SaveSnapshot(pass, t, src, at, snap, zero); e != nil {
				results[t] = "failed: SQLite: " + e.Error()
				break
			}
			if err := s.recordIntradayBar(pass, t, src, at, snap, false); err != nil {
				lastError = "快照已保存；日内摘要未更新: " + err.Error()
			}
			s.markSampled(t, s.now())
			results[t] = "success"
			// Calculations are independent of the committed market snapshot. A failed
			// derivation is visible, and the next collection can retry pending rows.
			if deriveErr := s.deriveGo(pass, t, src, s.background); deriveErr != nil {
				lastError = "快照已保存；派生摘要待重试: " + deriveErr.Error()
			}
			if s.background {
				s.queueMaintenance(deriveTarget{t, src})
			}
			break
		}
		if _, ok := results[t]; !ok {
			results[t] = "failed: " + strings.Join(failures, "; ")
		}
		if !strings.HasPrefix(results[t], "success") && !strings.HasPrefix(results[t], "skipped") {
			lastError = results[t]
		}
	}
	if e = pass.Err(); e == nil && !s.background {
		if e = s.maintenance(pass); e != nil {
			lastError = "采集完成；维护未完成: " + e.Error()
		}
	}
	return results, nil
}

type deriveTarget struct{ ticker, source string }

func (s *DesktopService) queueMaintenance(target deriveTarget) {
	s.maintenanceMu.Lock()
	s.pendingDerived[target] = struct{}{}
	s.maintenanceMu.Unlock()
	select {
	case s.maintenanceWake <- struct{}{}:
	default:
	}
}

func (s *DesktopService) maintenanceLoop() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.maintenanceWake:
		}
		s.maintenanceMu.Lock()
		targets := s.pendingDerived
		s.pendingDerived = make(map[deriveTarget]struct{})
		s.maintenanceMu.Unlock()
		for target := range targets {
			if s.ctx.Err() != nil {
				return
			}
			err := s.deriveGo(s.ctx, target.ticker, target.source, false)
			if err == nil {
				_, err = s.store.BackfillPCR(s.ctx, target.ticker, target.source)
			}
			if err != nil && s.ctx.Err() == nil {
				s.setStatus("error", "派生摘要待重试: "+err.Error())
			}
		}
		if err := s.maintenance(s.ctx); err != nil && s.ctx.Err() == nil {
			s.setStatus("error", "维护未完成: "+err.Error())
		}
	}
}

func (s *DesktopService) maintenance(ctx context.Context) error {
	today := s.now().In(marketclock.Zone).Format("2006-01-02")
	last, e := s.store.Setting(ctx, "desktop_archive_day")
	if e != nil {
		return e
	}
	if last != today {
		if e = s.store.Archive(ctx); e != nil {
			return e
		}
		if e = s.store.SetSetting(ctx, "desktop_archive_day", today); e != nil {
			return e
		}
	}
	year, week := s.now().ISOWeek()
	key := fmt.Sprintf("%d-%02d", year, week)
	last, e = s.store.Setting(ctx, "desktop_backup_week")
	if e != nil {
		return e
	}
	if last != key {
		dest := filepath.Join(filepath.Dir(s.store.Path()), "backups", "weekly-"+key+".db")
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			if e = s.store.Backup(ctx, dest); e != nil {
				return e
			}
		}
		if e = s.store.SetSetting(ctx, "desktop_backup_week", key); e != nil {
			return e
		}
	}
	// Directory refresh is optional and isolated from option data. Once per week.
	last, e = s.store.Setting(ctx, "catalogue_refreshed_at")
	if e != nil {
		return e
	}
	if at, e := time.Parse("2006-01-02", last); e == nil && s.now().Sub(at) < 7*24*time.Hour {
		return nil
	}
	return s.refreshDirectory(ctx, today)
}
func (s *DesktopService) refreshDirectory(ctx context.Context, today string) error {
	call, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(call, "GET", "https://www.cboe.com/us/options/symboldir/equity_index_options/?download=csv", nil)
	if e != nil {
		return e
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("标的目录 HTTP %d", resp.StatusCode)
	}
	reader := csv.NewReader(io.LimitReader(resp.Body, 10<<20))
	reader.TrimLeadingSpace = true
	header, e := reader.Read()
	if e != nil {
		return e
	}
	symbolCol, companyCol := -1, -1
	for i, name := range header {
		switch strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")) {
		case "Stock Symbol":
			symbolCol = i
		case "Company Name":
			companyCol = i
		}
	}
	if symbolCol < 0 || companyCol < 0 {
		return fmt.Errorf("标的目录字段已变化")
	}
	entries, e := reader.ReadAll()
	if e != nil {
		return e
	}
	if len(entries) == 0 {
		return fmt.Errorf("标的目录为空")
	}
	return s.store.Transaction(ctx, func(tx *sql.Tx) error {
		stmt, e := tx.Prepare("INSERT INTO option_symbols(symbol,company) VALUES(?,?) ON CONFLICT(symbol) DO UPDATE SET company=excluded.company,last_seen_at=CURRENT_TIMESTAMP")
		if e != nil {
			return e
		}
		defer stmt.Close()
		for _, row := range entries {
			if len(row) <= max(symbolCol, companyCol) {
				continue
			}
			symbol, e := provider.NormalizeSymbol(row[symbolCol])
			if e != nil {
				continue
			}
			if _, e = stmt.ExecContext(ctx, symbol, row[companyCol]); e != nil {
				return e
			}
		}
		return store.SetSetting(tx, "catalogue_refreshed_at", today)
	})
}
