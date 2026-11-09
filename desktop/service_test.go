package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/provider"
	"gammagrid.local/desktop/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	root, e := filepath.Abs("..")
	if e != nil {
		t.Fatal(e)
	}
	s, e := store.Open(root, filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func number(v float64) *float64 { return &v }
func fixtureSnapshot(n int) provider.Snapshot {
	snap := provider.Snapshot{Spot: 100}
	for d := 0; d < 3; d++ {
		expiry := time.Now().UTC().AddDate(0, 0, 30+d).Format("2006-01-02")
		for i := 0; i < n; i++ {
			for _, side := range []string{"call", "put"} {
				strike := 90 + float64(i)/10
				expiryDate, _ := time.Parse("2006-01-02", expiry)
				symbol := fmt.Sprintf("TEST%s%s%08d", expiryDate.Format("060102"), strings.ToUpper(side[:1]), int(strike*1000))
				snap.Rows = append(snap.Rows, provider.OptionRow{Expiry: expiry, Strike: strike, Type: side, Symbol: symbol, Last: number(3), Bid: number(2.9), Ask: number(3.1), Volume: number(100), OI: number(1000), IV: number(.25), Gamma: number(.02), Delta: number(.5), ITM: side == "call" && strike < 100})
			}
		}
	}
	return snap
}
func TestStoreAtomicOwnershipArchiveBackup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := filepath.Abs("..")
	if other, e := store.Open(root, s.Path()); e == nil {
		other.Close()
		t.Fatal("second writer accepted")
	}
	_, e := testExec(s, `CREATE TRIGGER reject_run BEFORE INSERT ON collection_runs BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if e != nil {
		t.Fatal(e)
	}
	snap := fixtureSnapshot(3)
	at := time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)
	if e = s.SaveSnapshot(ctx, "TEST", "cboe", at, snap, 0); e == nil {
		t.Fatal("injected failure ignored")
	}
	for _, table := range []string{"option_snapshots", "contract_registry", "snapshot_iv_summary", "collection_runs"} {
		var n int
		if e = s.Read().QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("partial write %s: %d %v", table, n, e)
		}
	}
	if _, e = testExec(s, "DROP TRIGGER reject_run"); e != nil {
		t.Fatal(e)
	}
	if e = s.SaveSnapshot(ctx, "TEST", "cboe", at, snap, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Read().Exec("INSERT INTO watchlist(ticker) VALUES('BAD')"); e == nil {
		t.Fatal("read pool allowed write")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if e = s.Backup(ctx, backup); e != nil {
		t.Fatal(e)
	}
	db, e := sql.Open("sqlite3", backup+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var n int
	if e = db.QueryRow("SELECT count(*) FROM option_snapshots").Scan(&n); e != nil || n != len(snap.Rows) {
		t.Fatalf("backup: %d %v", n, e)
	}
	if e = s.Backup(ctx, backup); e == nil {
		t.Fatal("backup overwritten")
	}
	_, e = testExec(s, "UPDATE option_snapshots SET expiry=date(expiry,'-30 years'); UPDATE contract_registry SET expiry=date(expiry,'-30 years')")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Archive(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Archive(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.Read().QueryRow("SELECT count(*) FROM option_snapshots_archive").Scan(&n); e != nil || n != len(snap.Rows) {
		t.Fatalf("archive: %d %v", n, e)
	}
}
func TestScheduleDSTHolidayAndTemporaryExpiry(t *testing.T) {
	cfg := marketclock.DefaultSchedule()
	sessions := map[string]marketclock.SessionHours{
		"2026-03-06": {Open: "09:30", Close: "16:00"},
		"2026-03-09": {Open: "09:30", Close: "16:00"},
		"2026-11-27": {Open: "09:30", Close: "13:00"},
	}
	for _, utc := range []string{"2026-03-06T15:00:00Z", "2026-03-09T14:00:00Z"} {
		now, _ := time.Parse(time.RFC3339, utc)
		key, ok := marketclock.DueSlot(now, cfg, sessions)
		if !ok || !strings.Contains(key, "T10:00:00") {
			t.Fatal(utc, key, ok)
		}
	}
	holiday := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	if _, ok := marketclock.DueSlot(holiday, cfg, sessions); ok {
		t.Fatal("holiday collected")
	}
	late := time.Date(2026, 3, 9, 14, 16, 0, 0, time.UTC)
	if _, ok := marketclock.DueSlot(late, cfg, sessions); ok {
		t.Fatal("replayed old slot")
	}
	cfg.Mode = "temporary"
	cfg.Minutes = 15
	cfg.Until = "2026-11-27T20:00:00Z"
	if _, ok := marketclock.DueSlot(time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC), cfg, sessions); ok {
		t.Fatal("temporary mode ignored early close")
	}
	if cfg.Effective(time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)).Mode != "fixed" {
		t.Fatal("temporary mode did not expire")
	}
	cfg = marketclock.DefaultSchedule()
	cfg.Times = []string{"10:00", "10:00"}
	if marketclock.ValidateSchedule(cfg, time.Now()) == nil {
		t.Fatal("duplicate slots accepted")
	}
}
func TestCacheCoalescingCancellationAndBound(t *testing.T) {
	cache := newResponseCache(context.Background())
	entered := make(chan struct{})
	release := make(chan struct{})
	var count atomic.Int32
	fn := func(context.Context) ([]byte, error) {
		if count.Add(1) == 1 {
			close(entered)
		}
		<-release
		return []byte(`{"ok":true}`), nil
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := cached(cache, context.Background(), "same", fn); errs <- e }()
	}
	<-entered
	close(release)
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	if count.Load() != 1 {
		t.Fatal("duplicate work", count.Load())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := cached(cache, ctx, "cancelled", fn); e == nil {
		t.Fatal("cancel ignored")
	}
	for i := 0; i < 100; i++ {
		_, e := cached(cache, context.Background(), fmt.Sprint(i), func(context.Context) ([]byte, error) { return make([]byte, 600000), nil })
		if e != nil {
			t.Fatal(e)
		}
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if len(cache.values) > 64 || cache.bytes > 32<<20 {
		t.Fatal("cache unbounded")
	}
}

func TestCancelledQueryCanBeSelectedAgain(t *testing.T) {
	cache := newResponseCache(context.Background())
	started := make(chan struct{})
	finishOld := make(chan struct{})
	returned := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(returned)
		_, _ = cached(cache, ctx, "A", func(task context.Context) ([]byte, error) {
			close(started)
			<-task.Done()
			<-finishOld
			return nil, task.Err()
		})
	}()
	<-started
	cancel()
	<-returned
	b, e := cached(cache, context.Background(), "A", func(context.Context) ([]byte, error) { return []byte("new result"), nil })
	close(finishOld)
	if e != nil || string(b) != "new result" {
		t.Fatalf("reselected query attached to abandoned request: %q %v", b, e)
	}
}

func TestLiveCboeCollection(t *testing.T) {
	ticker := os.Getenv("GEX_LIVE_COLLECT")
	if ticker == "" {
		t.Skip("explicit opt-in required for a live provider request")
	}
	t.Setenv("GEX_PROVIDER", "cboe")
	s := testStore(t)
	root, _ := filepath.Abs("..")
	service := newDesktopService(s, root)
	defer service.Close()
	results, e := service.Collect(context.Background(), ticker)
	if e != nil {
		t.Fatal(e)
	}
	if results[ticker] != "success" {
		t.Fatal(results)
	}
	var rows int
	var source string
	if e = s.Read().QueryRow("SELECT count(*),min(source) FROM option_snapshots WHERE ticker=?", ticker).Scan(&rows, &source); e != nil {
		t.Fatal(e)
	}
	if rows < 100 || source != "cboe" {
		t.Fatalf("snapshot: %d %s", rows, source)
	}
	t.Logf("live %s: %d stored Cboe contracts; status=%s; maintenance=%s", ticker, rows, service.status, service.statusError)
	if service.statusError != "" {
		t.Fatal(service.statusError)
	}
}

func TestCollectionFallbackCooldownAndDurableSlots(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 9, 15, 0, 0, 0, time.UTC)
	if _, e := testExec(s, "INSERT INTO watchlist(ticker) VALUES('TEST')"); e != nil {
		t.Fatal(e)
	}
	if e := s.SetSetting(ctx, "desktop_archive_day", "2026-11-09"); e != nil {
		t.Fatal(e)
	}
	year, week := now.ISOWeek()
	if e := s.SetSetting(ctx, "desktop_backup_week", fmt.Sprintf("%d-%02d", year, week)); e != nil {
		t.Fatal(e)
	}
	if e := s.SetSetting(ctx, "catalogue_refreshed_at", "2026-11-09"); e != nil {
		t.Fatal(e)
	}
	var yahooCalls atomic.Int32
	service := newDesktopService(s, "..")
	defer service.Close()
	service.fetchYahoo = func(context.Context, string) (provider.Snapshot, error) {
		yahooCalls.Add(1)
		return fixtureSnapshot(3), nil
	}
	service.now = func() time.Time { return now }
	var cboeCalls atomic.Int32
	service.fetch = func(context.Context, string) (provider.Snapshot, error) {
		cboeCalls.Add(1)
		return provider.Snapshot{}, &provider.ProviderError{Status: 429, RetryAfter: time.Hour, Message: "rate limited"}
	}
	service.scheduleTick()
	version := s.Revision()
	service.scheduleTick()
	restarted := newDesktopService(s, "..")
	restarted.now = service.now
	restarted.fetch = service.fetch
	restarted.fetchYahoo = service.fetchYahoo
	defer restarted.Close()
	restarted.scheduleTick()
	if cboeCalls.Load() != 1 || yahooCalls.Load() != 1 {
		t.Fatalf("duplicate automatic collection: cboe=%d yahoo=%d", cboeCalls.Load(), yahooCalls.Load())
	}
	if s.Revision() != version {
		t.Fatal("duplicate slot invalidated caches")
	}
	now = now.Add(time.Minute)
	if _, e := service.Collect(ctx, "TEST"); e != nil {
		t.Fatal(e)
	}
	if cboeCalls.Load() != 1 || yahooCalls.Load() != 2 {
		t.Fatal("provider cooldown ignored")
	}
	var source string
	if e := s.Read().QueryRow("SELECT source FROM collection_runs WHERE status='success' ORDER BY started_at DESC LIMIT 1").Scan(&source); e != nil || source != "yahoo" {
		t.Fatal("fallback source stamp", source, e)
	}
	service.collectMu.Lock()
	_, e := service.Collect(ctx, "TEST")
	service.collectMu.Unlock()
	if e == nil {
		t.Fatal("overlapping manual pass accepted")
	}
}

func TestDualTrackCollectionAndIntradayTide(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := filepath.Abs("..")
	svc := newDesktopService(s, root)
	t.Cleanup(svc.Close)

	now := time.Date(2026, 11, 6, 15, 10, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	if _, e := testExec(s, "INSERT INTO watchlist(ticker) VALUES('SPY'), ('QQQ')"); e != nil {
		t.Fatal(e)
	}

	// 1. Verify per-ticker Low-Freq and High-Freq config persistence
	cfgQQQ := store.TickerCollectConfig{
		Ticker:          "QQQ",
		LowFreqEnabled:  false,
		HighFreqEnabled: true,
		HighFreqSec:     120,
	}
	if e := s.SaveTickerCollectConfig(ctx, cfgQQQ); e != nil {
		t.Fatal(e)
	}
	gotQQQ, e := s.TickerCollectConfig(ctx, "QQQ")
	if e != nil || gotQQQ.LowFreqEnabled || !gotQQQ.HighFreqEnabled || gotQQQ.HighFreqSec != 120 {
		t.Fatalf("unexpected QQQ config: %+v (%v)", gotQQQ, e)
	}
	gotSPY, e := s.TickerCollectConfig(ctx, "SPY")
	if e != nil || !gotSPY.LowFreqEnabled || gotSPY.HighFreqEnabled || gotSPY.HighFreqSec != 60 {
		t.Fatalf("unexpected default SPY config: %+v (%v)", gotSPY, e)
	}

	// 2. Verify interval schedule validation & dueSlot
	sched := marketclock.ScheduleConfig{Mode: "interval", Minutes: 30, Times: []string{"10:00", "16:15"}}
	if e := marketclock.ValidateSchedule(sched, now); e != nil {
		t.Fatal(e)
	}
	slot, due := marketclock.DueSlot(now, sched, map[string]marketclock.SessionHours{"2026-11-06": {Open: "09:30", Close: "16:00"}})
	if !due || slot == "" {
		t.Fatal("expected interval schedule dueSlot during market hours")
	}

	// 3. Mock Cboe server with increasing volume across two ticks
	var tickCount atomic.Int32
	svc.fetch = func(context.Context, string) (provider.Snapshot, error) {
		n := tickCount.Add(1)
		callVol := 1000 + int(n)*250
		putVol := 1200 + int(n)*400
		spot := 580.0 + float64(n)*0.5
		raw := fmt.Sprintf(`{"timestamp":"2026-11-06 10:10:00","data":{"current_price":%.2f,"options":[`+
			`{"option":"SPY261109C00580000","gamma":0.035,"delta":0.52,"iv":0.18,"open_interest":15000,"volume":%d,"bid":4.10,"ask":4.30,"last_trade_price":4.20},`+
			`{"option":"SPY261109P00580000","gamma":0.034,"delta":-0.48,"iv":0.19,"open_interest":18000,"volume":%d,"bid":3.90,"ask":4.10,"last_trade_price":4.00}`+
			`]}}`, spot, callVol, putVol)
		return provider.ParseCboe(strings.NewReader(raw))
	}

	// 4. Low-frequency scheduled pass should collect SPY (LowFreqEnabled=true) and skip QQQ (LowFreqEnabled=false)
	res, e := svc.collectScheduledLocked(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if res["SPY"] != "success" || !strings.HasPrefix(res["QQQ"], "skipped") {
		t.Fatalf("unexpected scheduled results: %+v", res)
	}

	var snapRowsBefore int
	if e := s.Read().QueryRow("SELECT count(*) FROM option_snapshots").Scan(&snapRowsBefore); e != nil || snapRowsBefore != 2 {
		t.Fatalf("expected 2 option_snapshots rows after low-freq pass, got %d (%v)", snapRowsBefore, e)
	}

	// 5. High-frequency tick on SPY should record intraday_tide_bars delta WITHOUT adding option_snapshots rows
	now = now.Add(5 * time.Minute)
	if e := svc.collectHighFreqTick(ctx, "SPY"); e != nil {
		t.Fatalf("high-freq tick failed: err=%v", e)
	}
	var snapRowsAfter int
	if e := s.Read().QueryRow("SELECT count(*) FROM option_snapshots").Scan(&snapRowsAfter); e != nil || snapRowsAfter != snapRowsBefore {
		t.Fatalf("high-freq tick bloated option_snapshots: before=%d after=%d", snapRowsBefore, snapRowsAfter)
	}
	var tideRows int
	if e := s.Read().QueryRow("SELECT count(*) FROM intraday_tide_bars WHERE ticker='SPY'").Scan(&tideRows); e != nil || tideRows != 2 {
		t.Fatalf("expected 2 intraday_tide_bars rows for SPY, got %d (%v)", tideRows, e)
	}

	// 6. Workbench response should include live IntradaySummary & Strike Pressure profiles
	wb, e := svc.Workbench(ctx, "SPY", workbenchQuery{})
	if e != nil || wb.Empty || wb.Intraday.TotalVol <= 0 {
		t.Fatalf("workbench missing intraday summary: err=%v empty=%v intraday=%+v", e, wb.Empty, wb.Intraday)
	}
	if len(wb.Intraday.TimeBars) < 2 || len(wb.CallProfile) == 0 || len(wb.PutProfile) == 0 {
		t.Fatalf("unexpected intraday bars or strike profiles: bars=%d callProf=%d", len(wb.Intraday.TimeBars), len(wb.CallProfile))
	}
	lastBar := wb.Intraday.TimeBars[len(wb.Intraday.TimeBars)-1]
	// Tick 1 had callVol=1250, putVol=1600; Tick 2 had callVol=1500, putVol=2000 -> delta should be +250 call, +400 put
	if lastBar.CallVol != 250 || lastBar.PutVol != 400 {
		t.Fatalf("expected delta volumes 250 call / 400 put, got %.0f / %.0f", lastBar.CallVol, lastBar.PutVol)
	}
}
func TestStandardSeriesPreferred(t *testing.T) {
	row := provider.OptionRow{Strike: 100, Expiry: "2026-12-18", Type: "call"}
	adjusted := row
	adjusted.Symbol = "TEST1261218C00100000"
	standard := row
	standard.Symbol = "TEST261218C00100000"
	rows := store.DedupOptions("TEST", []provider.OptionRow{adjusted, standard})
	if len(rows) != 1 || rows[0].Symbol != standard.Symbol {
		t.Fatal("standard series not preferred")
	}
}

func testExec(s *store.Store, statement string) (result sql.Result, err error) {
	err = s.Transaction(context.Background(), func(tx *sql.Tx) error {
		var e error
		result, e = tx.Exec(statement)
		return e
	})
	return
}

func TestTimerAndHighFreqExpiry(t *testing.T) {
	// Friday afternoon before the spring DST change.
	friday := time.Date(2026, 3, 6, 16, 16, 0, 0, marketclock.Zone)
	want := time.Date(2026, 3, 9, 9, 25, 0, 0, marketclock.Zone)
	if got := nextScheduleWake(friday, marketclock.DefaultSchedule()); !got.Equal(want) {
		t.Fatalf("next premarket wake = %v, want %v", got, want)
	}

	t.Setenv("GEX_PROVIDER", "cboe")
	ctx := context.Background()
	svc := newDesktopService(testStore(t), "..")
	t.Cleanup(svc.Close)
	now := time.Date(2026, 11, 27, 12, 45, 0, 0, marketclock.Zone)
	svc.now = func() time.Time { return now }
	cfg := store.TickerCollectConfig{Ticker: "SPY", LowFreqEnabled: true, HighFreqEnabled: true, HighFreqSec: 60}
	if err := svc.saveTickerConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err := svc.store.TickerCollectConfig(ctx, "SPY")
	if err != nil {
		t.Fatal(err)
	}
	closeAt := time.Date(2026, 11, 27, 13, 0, 0, 0, marketclock.Zone)
	if cfg.HighFreqUntil != closeAt.Format(time.RFC3339) {
		t.Fatalf("early close should cap high frequency: %+v", cfg)
	}
	calls := 0
	svc.fetch = func(context.Context, string) (provider.Snapshot, error) {
		calls++
		return provider.Snapshot{}, fmt.Errorf("offline")
	}
	next := svc.runHighFreqIfDue(ctx, "SPY", cfg)
	if !next.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("failure backoff = %v", next.Sub(now))
	}
	now = now.Add(time.Minute)
	svc.runHighFreqIfDue(ctx, "SPY", cfg)
	now = closeAt
	next = svc.runHighFreqIfDue(ctx, "SPY", cfg)
	if calls != 1 || !next.IsZero() {
		t.Fatalf("retried too early or ran after close: calls=%d next=%v", calls, next)
	}
	now = friday
	cfg.Ticker = "QQQ"
	if err := svc.saveTickerConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	cfg, err = svc.store.TickerCollectConfig(ctx, "QQQ")
	if err != nil {
		t.Fatal(err)
	}
	if next := svc.runHighFreqIfDue(ctx, "QQQ", cfg); !next.Equal(want) || calls != 1 {
		t.Fatalf("after-hours activation should wait for next premarket: next=%v calls=%d", next, calls)
	}
}

func TestMaxPainPrefixKeepsLowestStrikeOnTie(t *testing.T) {
	rows := []store.StoredOptionRow{
		{Strike: 100.25, OptionType: "put", OI: number(100)},
		{Strike: 90.25, OptionType: "call", OI: number(100)},
		{Strike: 95.25, OptionType: "call"},
		{Strike: 85.25, OptionType: "put", OI: number(0)},
	}
	if got := maxPain(rows); got == nil || *got != 90.25 {
		t.Fatalf("flat minimum should select the lowest minimizing strike: %v", got)
	}
	rows[0].OI = number(101)
	if got := maxPain(rows); got == nil || *got != 100.25 {
		t.Fatalf("larger put position should move the minimum to 100.25: %v", got)
	}
}

func TestPCRBackfillPreservesMissingAndZero(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	now := time.Now()
	for i, putVol := range []*float64{nil, number(0)} {
		snapshot := provider.Snapshot{Spot: 100, Rows: []provider.OptionRow{
			{Expiry: now.AddDate(0, 0, 14).Format("2006-01-02"), Strike: 100, Type: "call", Volume: number(10), OI: number(10)},
			{Expiry: now.AddDate(0, 0, 14).Format("2006-01-02"), Strike: 100, Type: "put", Volume: putVol, OI: number(20)},
		}}
		if err := s.SaveSnapshot(ctx, "SPY", "cboe", now.Add(time.Duration(i)*time.Minute), snapshot, 0); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testExec(s, "UPDATE snapshot_iv_summary SET pcr_ready=0,call_volume=NULL,put_volume=NULL,call_oi=NULL,put_oi=NULL"); err != nil {
		t.Fatal(err)
	}
	for pass := 0; pass < 2; pass++ {
		points, err := s.PutCallRatioSeries(ctx, "SPY", "cboe", 0)
		if err != nil || len(points) != 2 {
			t.Fatalf("pass %d, points=%+v err=%v", pass, points, err)
		}
		if points[0].PCRVol != nil || points[1].PCRVol == nil || *points[1].PCRVol != 0 ||
			points[0].PCROI == nil || *points[0].PCROI != 2 {
			t.Fatalf("unexpected PCR values on pass %d: %+v", pass, points)
		}
		if pass == 0 {
			changed, err := s.BackfillPCR(ctx, "SPY", "cboe")
			if err != nil || !changed {
				t.Fatalf("backfill failed: changed=%v err=%v", changed, err)
			}
		}
	}
}

func TestTypedViewCacheIgnoresUnrelatedWrites(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	svc := newDesktopService(s, "..")
	t.Cleanup(svc.Close)
	at := time.Now()
	snapshot := fixtureSnapshot(3)
	if err := s.SaveSnapshot(ctx, "TEST", "cboe", at, snapshot, 0); err != nil {
		t.Fatal(err)
	}
	q := workbenchQuery{RangePct: 20}
	before, err := svc.Workbench(ctx, "TEST", q)
	if err != nil || before.Empty || len(before.Profile) == 0 {
		t.Fatalf("expected a populated pressure view: %v", err)
	}
	if err := s.SetSetting(ctx, "high_freq_sec:QQQ", "120"); err != nil {
		t.Fatal(err)
	}
	after, err := svc.Workbench(ctx, "TEST", q)
	if err != nil || before != after {
		t.Fatalf("unrelated settings write invalidated cached pressure view: %v", err)
	}
	snapshot.Spot = 103
	if err := s.SaveSnapshot(ctx, "TEST", "cboe", at.Add(time.Minute), snapshot, 0); err != nil {
		t.Fatal(err)
	}
	fresh, err := svc.Workbench(ctx, "TEST", q)
	if err != nil || fresh.Spot != 103 || before.Spot != 100 {
		t.Fatalf("new snapshot was not isolated from prior cached view: %v", err)
	}
}
