package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	root, e := filepath.Abs("..")
	if e != nil {
		t.Fatal(e)
	}
	s, e := openStore(root, filepath.Join(t.TempDir(), "test.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func number(v float64) *float64 { return &v }
func fixtureSnapshot(n int) Snapshot {
	snap := Snapshot{Spot: 100}
	for d := 0; d < 3; d++ {
		expiry := time.Now().UTC().AddDate(0, 0, 30+d).Format("2006-01-02")
		for i := 0; i < n; i++ {
			for _, side := range []string{"call", "put"} {
				strike := 90 + float64(i)/10
				expiryDate, _ := time.Parse("2006-01-02", expiry)
				symbol := fmt.Sprintf("TEST%s%s%08d", expiryDate.Format("060102"), strings.ToUpper(side[:1]), int(strike*1000))
				snap.Rows = append(snap.Rows, OptionRow{Expiry: expiry, Strike: strike, Type: side, Symbol: symbol, Last: number(3), Bid: number(2.9), Ask: number(3.1), Volume: number(100), OI: number(1000), IV: number(.25), Gamma: number(.02), Delta: number(.5), ITM: side == "call" && strike < 100})
			}
		}
	}
	return snap
}
func TestStoreAtomicOwnershipArchiveBackup(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := filepath.Abs("..")
	if other, e := openStore(root, s.path); e == nil {
		other.Close()
		t.Fatal("second writer accepted")
	}
	_, e := s.write.Exec(`CREATE TRIGGER reject_run BEFORE INSERT ON collection_runs BEGIN SELECT RAISE(ABORT,'injected'); END`)
	if e != nil {
		t.Fatal(e)
	}
	snap := fixtureSnapshot(3)
	at := time.Date(2026, 11, 6, 17, 0, 0, 0, time.UTC)
	if e = s.saveSnapshot(ctx, "TEST", "cboe", at, snap, 0); e == nil {
		t.Fatal("injected failure ignored")
	}
	for _, table := range []string{"option_snapshots", "contract_registry", "snapshot_iv_summary", "collection_runs"} {
		var n int
		if e = s.read.QueryRow("SELECT count(*) FROM " + table).Scan(&n); e != nil || n != 0 {
			t.Fatalf("partial write %s: %d %v", table, n, e)
		}
	}
	if _, e = s.write.Exec("DROP TRIGGER reject_run"); e != nil {
		t.Fatal(e)
	}
	if e = s.saveSnapshot(ctx, "TEST", "cboe", at, snap, 0); e != nil {
		t.Fatal(e)
	}
	if _, e = s.read.Exec("INSERT INTO watchlist(ticker) VALUES('BAD')"); e == nil {
		t.Fatal("read pool allowed write")
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if e = s.backup(ctx, backup); e != nil {
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
	if e = s.backup(ctx, backup); e == nil {
		t.Fatal("backup overwritten")
	}
	_, e = s.write.Exec("UPDATE option_snapshots SET expiry=date(expiry,'-30 years'); UPDATE contract_registry SET expiry=date(expiry,'-30 years')")
	if e != nil {
		t.Fatal(e)
	}
	if e = s.archive(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.archive(ctx); e != nil {
		t.Fatal(e)
	}
	if e = s.read.QueryRow("SELECT count(*) FROM option_snapshots_archive").Scan(&n); e != nil || n != len(snap.Rows) {
		t.Fatalf("archive: %d %v", n, e)
	}
}
func TestCboeNativeValuesAndRateLimit(t *testing.T) {
	raw := `{"timestamp":"2026-11-06 16:24:09","data":{"current_price":100,"options":[{"option":"SPX261218C00100000","gamma":0.012,"delta":0.55,"iv":0.23,"open_interest":42,"bid":-1,"vega":null}]}}`
	snap, e := parseCboe(strings.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	row := snap.Rows[0]
	if row.Gamma == nil || *row.Gamma != .012 || row.IV == nil || *row.IV != .23 || row.Bid != nil || row.Vega != nil {
		t.Fatalf("normalization: %+v", row)
	}
	if u, e := cboeURL("^SPX"); e != nil || !strings.HasSuffix(u, "/_SPX.json") {
		t.Fatal(u, e)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "3600")
		w.WriteHeader(429)
	}))
	defer server.Close()
	client := &CboeClient{client: server.Client(), url: func(string) (string, error) { return server.URL, nil }}
	_, e = client.Fetch(context.Background(), "TEST")
	pe, ok := e.(*ProviderError)
	if !ok || pe.Status != 429 || pe.RetryAfter < time.Hour || calls.Load() != 1 {
		t.Fatalf("rate limit retries: %v / %d", e, calls.Load())
	}
	adjusted := row
	adjusted.Symbol = "TEST1261218C00100000"
	standard := row
	standard.Symbol = "TEST261218C00100000"
	rows := dedupOptions("TEST", []OptionRow{adjusted, standard})
	if len(rows) != 1 || rows[0].Symbol != standard.Symbol {
		t.Fatal("standard series not preferred")
	}
}
func TestScheduleDSTHolidayAndTemporaryExpiry(t *testing.T) {
	cfg := defaultSchedule()
	sessions := map[string]SessionHours{"2026-03-06": {"09:30", "16:00"}, "2026-03-09": {"09:30", "16:00"}, "2026-11-27": {"09:30", "13:00"}}
	for _, utc := range []string{"2026-03-06T15:00:00Z", "2026-03-09T14:00:00Z"} {
		now, _ := time.Parse(time.RFC3339, utc)
		key, ok := dueSlot(now, cfg, sessions)
		if !ok || !strings.Contains(key, "T10:00:00") {
			t.Fatal(utc, key, ok)
		}
	}
	holiday := time.Date(2026, 11, 26, 15, 0, 0, 0, time.UTC)
	if _, ok := dueSlot(holiday, cfg, sessions); ok {
		t.Fatal("holiday collected")
	}
	late := time.Date(2026, 3, 9, 14, 16, 0, 0, time.UTC)
	if _, ok := dueSlot(late, cfg, sessions); ok {
		t.Fatal("replayed old slot")
	}
	cfg.Mode = "temporary"
	cfg.Minutes = 15
	cfg.Until = "2026-11-27T20:00:00Z"
	if _, ok := dueSlot(time.Date(2026, 11, 27, 19, 0, 0, 0, time.UTC), cfg, sessions); ok {
		t.Fatal("temporary mode ignored early close")
	}
	if cfg.effective(time.Date(2026, 11, 27, 21, 0, 0, 0, time.UTC)).Mode != "fixed" {
		t.Fatal("temporary mode did not expire")
	}
	cfg = defaultSchedule()
	cfg.Times = []string{"10:00", "10:00"}
	if validateSchedule(cfg, time.Now()) == nil {
		t.Fatal("duplicate slots accepted")
	}
}
func TestCacheCoalescingCancellationAndBound(t *testing.T) {
	cache := newResponseCache()
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
		go func() { defer wg.Done(); _, e := cache.Do(context.Background(), "same", fn); errs <- e }()
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
	if _, e := cache.Do(ctx, "cancelled", fn); e == nil {
		t.Fatal("cancel ignored")
	}
	for i := 0; i < 100; i++ {
		_, e := cache.Do(context.Background(), fmt.Sprint(i), func(context.Context) ([]byte, error) { return make([]byte, 600000), nil })
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
	cache := newResponseCache()
	started := make(chan struct{})
	finishOld := make(chan struct{})
	returned := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer close(returned)
		_, _ = cache.Do(ctx, "A", func(task context.Context) ([]byte, error) {
			close(started)
			<-task.Done()
			<-finishOld
			return nil, task.Err()
		})
	}()
	<-started
	cancel()
	<-returned
	b, e := cache.Do(context.Background(), "A", func(context.Context) ([]byte, error) { return []byte("new result"), nil })
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
	base := pythonAdapter(t, s)
	root, _ := filepath.Abs("..")
	service := newDesktopService(s, root, base)
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
	if e = s.read.QueryRow("SELECT count(*),min(source) FROM option_snapshots WHERE ticker=?", ticker).Scan(&rows, &source); e != nil {
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
	if _, e := s.write.Exec("INSERT INTO watchlist(ticker) VALUES('TEST')"); e != nil {
		t.Fatal(e)
	}
	if e := s.setSetting(ctx, "desktop_archive_day", "2026-11-09"); e != nil {
		t.Fatal(e)
	}
	year, week := now.ISOWeek()
	if e := s.setSetting(ctx, "desktop_backup_week", fmt.Sprintf("%d-%02d", year, week)); e != nil {
		t.Fatal(e)
	}
	if e := s.setSetting(ctx, "catalogue_refreshed_at", "2026-11-09"); e != nil {
		t.Fatal(e)
	}
	var yahooCalls atomic.Int32
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/compute/calendar":
			sendJSON(w, 200, map[string]any{"sessions": map[string]SessionHours{"2026-11-09": {"09:30", "16:00"}}})
		case "/compute/yahoo":
			yahooCalls.Add(1)
			sendJSON(w, 200, fixtureSnapshot(3))
		case "/compute/derive":
			sendJSON(w, 200, map[string]any{"writes": []DerivedWrite{}})
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer adapter.Close()
	service := newDesktopService(s, "..", adapter.URL)
	defer service.Close()
	service.now = func() time.Time { return now }
	var cboeCalls atomic.Int32
	service.fetch = func(context.Context, string) (Snapshot, error) {
		cboeCalls.Add(1)
		return Snapshot{}, &ProviderError{Status: 429, RetryAfter: time.Hour, Message: "rate limited"}
	}
	service.scheduleTick()
	version := s.revision.Load()
	service.scheduleTick()
	restarted := newDesktopService(s, "..", adapter.URL)
	restarted.now = service.now
	restarted.fetch = service.fetch
	defer restarted.Close()
	restarted.scheduleTick()
	if cboeCalls.Load() != 1 || yahooCalls.Load() != 1 {
		t.Fatalf("duplicate automatic collection: cboe=%d yahoo=%d", cboeCalls.Load(), yahooCalls.Load())
	}
	if s.revision.Load() != version {
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
	if e := s.read.QueryRow("SELECT source FROM collection_runs WHERE status='success' ORDER BY started_at DESC LIMIT 1").Scan(&source); e != nil || source != "yahoo" {
		t.Fatal("fallback source stamp", source, e)
	}
	service.collectMu.Lock()
	_, e := service.Collect(ctx, "TEST")
	service.collectMu.Unlock()
	if e == nil {
		t.Fatal("overlapping manual pass accepted")
	}
}

func pythonAdapter(t *testing.T, s *Store) string {
	t.Helper()
	root, _ := filepath.Abs("..")
	python := filepath.Join(root, ".venv/bin/python")
	if _, e := os.Stat(python); e != nil {
		t.Skip("local Python environment unavailable")
	}
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.Command(python, "-m", "app.desktop_api", fmt.Sprintf("--port=%d", port))
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GEX_DATABASE_PATH="+s.path, "GEX_NATIVE_BACKEND=1", "GEX_DB_READONLY=1", "GEX_DISABLE_SCHEDULER=1")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Log(output.String())
		}
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: time.Second}
	for start := time.Now(); time.Since(start) < 15*time.Second; {
		res, e := client.Get(base + "/health")
		if e == nil {
			res.Body.Close()
			if res.StatusCode == 200 {
				return base
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("Python adapter did not start")
	return ""
}
func TestNativeIntegrationParityAndReadOnly(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	snap := fixtureSnapshot(80)
	now := time.Now().UTC()
	at := time.Date(now.Year(), now.Month(), now.Day(), 17, 0, 0, 0, time.UTC)
	for at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		at = at.AddDate(0, 0, -1)
	}
	for _, when := range []time.Time{at.Add(-24 * time.Hour), at} {
		if e := s.saveSnapshot(ctx, "TEST", "cboe", when, snap, 0); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := s.write.Exec("INSERT INTO watchlist(ticker) VALUES('TEST')"); e != nil {
		t.Fatal(e)
	}
	base := pythonAdapter(t, s)
	root, _ := filepath.Abs("..")
	service := newDesktopService(s, root, base)
	t.Cleanup(service.Close)
	// Use the actual date for expiry filtering in both implementations.
	service.now = time.Now
	state, e := service.state(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if state.RefreshMode != "manual" || state.Schedule.Mode != "fixed" || len(state.Tickers) != 1 {
		t.Fatalf("state: %+v", state)
	}
	before, e := service.compute(ctx, "/api/chain?ticker=TEST&band=50", nil)
	if e != nil {
		t.Fatal(e)
	}
	var old ChainResponse
	if e = json.Unmarshal(before, &old); e != nil {
		t.Fatal(e)
	}
	q := url.Values{"ticker": {"TEST"}, "band": {"50"}, "page_size": {"200"}}
	first, e := service.chain(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	q.Set("page", "1")
	second, e := service.chain(ctx, q)
	if e != nil {
		t.Fatal(e)
	}
	combined := append(append([]ChainRow{}, first.Rows...), second.Rows...)
	if !reflect.DeepEqual(combined, old.Rows) {
		t.Fatalf("paged rows differ: got=%d old=%d\nfirst=%+v\nold=%+v", len(combined), len(old.Rows), combined[0], old.Rows[0])
	}
	if first.TotalChain != old.TotalChain || first.TotalMatching != old.TotalMatching {
		t.Fatal("whole-chain counts changed")
	}
	// Pure Go derive exercises IV backfill, ticker_day_summary, and contract_volume_stats without Python.
	if e = service.deriveGo(ctx, "TEST", "cboe"); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.read.QueryRow("SELECT count(*) FROM ticker_day_summary").Scan(&n); e != nil || n == 0 {
		t.Fatal("missing day summary", e)
	}
	// Verify all 4 pure-Go views work without a Python HTTP server and match Python output.
	pureGoSvc := newDesktopService(s, root, "")
	t.Cleanup(pureGoSvc.Close)
	pureGoSvc.now = service.now
	wbGo, e := pureGoSvc.buildWorkbenchGo(ctx, url.Values{"ticker": {"TEST"}, "side": {"call"}})
	if e != nil || wbGo.Empty || len(wbGo.Strikes) == 0 || wbGo.Ribbon.Spot <= 0 {
		t.Fatalf("workbench pure Go failed: err=%v empty=%v", e, wbGo.Empty)
	}
	wbPyRaw, e := service.compute(ctx, "/api/workbench?ticker=TEST&side=call", nil)
	if e != nil {
		t.Fatal(e)
	}
	var wbPy WorkbenchResponse
	if e = json.Unmarshal(wbPyRaw, &wbPy); e != nil {
		t.Fatal(e)
	}
	if wbGo.Ribbon.NetGEXStr != wbPy.Ribbon.NetGEXStr || wbGo.Ribbon.CallOIWall != wbPy.Ribbon.CallOIWall ||
		wbGo.Ribbon.PutOIWall != wbPy.Ribbon.PutOIWall || wbGo.GrandTotalStr != wbPy.GrandTotalStr ||
		!reflect.DeepEqual(wbGo.CellsText, wbPy.CellsText) {
		t.Fatalf("workbench parity mismatch:\nGo ribbon=%+v\nPy ribbon=%+v\nGo grand=%s Py grand=%s", wbGo.Ribbon, wbPy.Ribbon, wbGo.GrandTotalStr, wbPy.GrandTotalStr)
	}
	volGo, e := pureGoSvc.buildVolatilityGo(ctx, url.Values{"ticker": {"TEST"}})
	if e != nil || volGo.Empty || len(volGo.Term) == 0 {
		t.Fatalf("volatility pure Go failed: err=%v empty=%v", e, volGo.Empty)
	}
	ctrGo, e := pureGoSvc.buildContractGo(ctx, url.Values{"ticker": {"TEST"}})
	if e != nil || ctrGo.Empty || len(ctrGo.Points) == 0 {
		t.Fatalf("contract pure Go failed: err=%v empty=%v", e, ctrGo.Empty)
	}
	flowGo, e := pureGoSvc.buildFlowGo(ctx, url.Values{"ticker": {"TEST"}})
	if e != nil || flowGo.Empty || len(flowGo.Ladder) == 0 {
		t.Fatalf("flow pure Go failed: err=%v empty=%v", e, flowGo.Empty)
	}
	flowPyRaw, e := service.compute(ctx, "/api/flow?ticker=TEST", nil)
	if e != nil {
		t.Fatal(e)
	}
	var flowPy FlowResponse
	if e = json.Unmarshal(flowPyRaw, &flowPy); e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(flowGo.Ladder, flowPy.Ladder) || len(flowGo.OIDelta) != len(flowPy.OIDelta) || !reflect.DeepEqual(flowGo.OIDelta[0], flowPy.OIDelta[0]) {
		t.Fatalf("flow parity mismatch:\nGo ladder=%+v\nPy ladder=%+v\nGo oi len=%d Py oi len=%d", flowGo.Ladder, flowPy.Ladder, len(flowGo.OIDelta), len(flowPy.OIDelta))
	}
	response, e := http.Post(base+"/api/watchlist/add", "application/json", strings.NewReader(`{"ticker":"BAD"}`))
	if e != nil {
		t.Fatal(e)
	}
	response.Body.Close()
	if response.StatusCode != 403 {
		t.Fatal("Python write route accepted", response.StatusCode)
	}
	calendar, e := service.calendar(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := calendar["2026-11-26"]; ok {
		t.Fatal("Thanksgiving included")
	}
	if h, ok := calendar["2026-11-27"]; ok && h.Close != "13:00" {
		t.Fatal("early close lost")
	}
	for i := 0; i < 30; i++ {
		if _, e = service.state(ctx); e != nil {
			t.Fatal(e)
		}
	}
	if s.read.Stats().OpenConnections > 2 || s.write.Stats().OpenConnections > 1 {
		t.Fatal("pool unbounded")
	}
}

func TestDualTrackCollectionAndIntradayTide(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	root, _ := filepath.Abs("..")
	svc := newDesktopService(s, root, "")
	t.Cleanup(svc.Close)

	now := time.Date(2026, 11, 6, 15, 10, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	if _, e := s.write.Exec("INSERT INTO watchlist(ticker) VALUES('SPY'), ('QQQ')"); e != nil {
		t.Fatal(e)
	}

	// 1. Verify per-ticker Low-Freq and High-Freq config persistence
	cfgQQQ := TickerCollectConfig{
		Ticker:          "QQQ",
		LowFreqEnabled:  false,
		HighFreqEnabled: true,
		HighFreqSec:     120,
	}
	if e := s.saveTickerCollectConfig(ctx, cfgQQQ); e != nil {
		t.Fatal(e)
	}
	gotQQQ, e := s.tickerCollectConfig(ctx, "QQQ")
	if e != nil || gotQQQ.LowFreqEnabled || !gotQQQ.HighFreqEnabled || gotQQQ.HighFreqSec != 120 {
		t.Fatalf("unexpected QQQ config: %+v (%v)", gotQQQ, e)
	}
	gotSPY, e := s.tickerCollectConfig(ctx, "SPY")
	if e != nil || !gotSPY.LowFreqEnabled || gotSPY.HighFreqEnabled || gotSPY.HighFreqSec != 60 {
		t.Fatalf("unexpected default SPY config: %+v (%v)", gotSPY, e)
	}

	// 2. Verify interval schedule validation & dueSlot
	sched := ScheduleConfig{Mode: "interval", Minutes: 30, Times: []string{"10:00", "16:15"}}
	if e := validateSchedule(sched, now); e != nil {
		t.Fatal(e)
	}
	slot, due := dueSlot(now, sched, map[string]SessionHours{"2026-11-06": {Open: "09:30", Close: "16:00"}})
	if !due || slot == "" {
		t.Fatal("expected interval schedule dueSlot during market hours")
	}

	// 3. Mock Cboe server with increasing volume across two ticks
	var tickCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := tickCount.Add(1)
		callVol := 1000 + int(n)*250
		putVol := 1200 + int(n)*400
		spot := 580.0 + float64(n)*0.5
		fmt.Fprintf(w, `{"timestamp":"2026-11-06 10:10:00","data":{"current_price":%.2f,"options":[`+
			`{"option":"SPY261109C00580000","gamma":0.035,"delta":0.52,"iv":0.18,"open_interest":15000,"volume":%d,"bid":4.10,"ask":4.30,"last_trade_price":4.20},`+
			`{"option":"SPY261109P00580000","gamma":0.034,"delta":-0.48,"iv":0.19,"open_interest":18000,"volume":%d,"bid":3.90,"ask":4.10,"last_trade_price":4.00}`+
			`]}}`, spot, callVol, putVol)
	}))
	defer server.Close()
	svc.fetch = (&CboeClient{client: server.Client(), url: func(string) (string, error) { return server.URL, nil }}).Fetch

	// 4. Low-frequency scheduled pass should collect SPY (LowFreqEnabled=true) and skip QQQ (LowFreqEnabled=false)
	res, e := svc.collectScheduledLocked(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if res["SPY"] != "success" || !strings.HasPrefix(res["QQQ"], "skipped") {
		t.Fatalf("unexpected scheduled results: %+v", res)
	}

	var snapRowsBefore int
	if e := s.read.QueryRow("SELECT count(*) FROM option_snapshots").Scan(&snapRowsBefore); e != nil || snapRowsBefore != 2 {
		t.Fatalf("expected 2 option_snapshots rows after low-freq pass, got %d (%v)", snapRowsBefore, e)
	}

	// 5. High-frequency tick on SPY should record intraday_tide_bars delta WITHOUT adding option_snapshots rows
	now = now.Add(5 * time.Minute)
	if e := svc.collectHighFreqTick(ctx, "SPY"); e != nil {
		t.Fatalf("high-freq tick failed: err=%v", e)
	}
	var snapRowsAfter int
	if e := s.read.QueryRow("SELECT count(*) FROM option_snapshots").Scan(&snapRowsAfter); e != nil || snapRowsAfter != snapRowsBefore {
		t.Fatalf("high-freq tick bloated option_snapshots: before=%d after=%d", snapRowsBefore, snapRowsAfter)
	}
	var tideRows int
	if e := s.read.QueryRow("SELECT count(*) FROM intraday_tide_bars WHERE ticker='SPY'").Scan(&tideRows); e != nil || tideRows != 2 {
		t.Fatalf("expected 2 intraday_tide_bars rows for SPY, got %d (%v)", tideRows, e)
	}

	// 6. Workbench response should include live IntradaySummary & Strike Pressure profiles
	wb, e := svc.buildWorkbenchGo(ctx, url.Values{"ticker": {"SPY"}})
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
