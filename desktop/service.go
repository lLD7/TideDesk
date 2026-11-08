package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type DesktopService struct {
	store               *Store
	root, pythonURL     string
	ctx                 context.Context
	cancel              context.CancelFunc
	wg                  sync.WaitGroup
	cache               *responseCache
	computeGate         chan struct{}
	client              *http.Client
	fetch               func(context.Context, string) (Snapshot, error)
	now                 func() time.Time
	collectMu           sync.Mutex
	statusMu            sync.Mutex
	status, statusError string
	intradayMu          sync.Mutex
	intradayBase        map[string]*intradayBaseline
	liveSnapshots       map[string][]StoredOptionRow
	lastHighFreqAt      map[string]time.Time
}

func newDesktopService(store *Store, root, pythonURL string) *DesktopService {
	ctx, cancel := context.WithCancel(context.Background())
	return &DesktopService{store: store, root: root, pythonURL: pythonURL, ctx: ctx, cancel: cancel, cache: newResponseCache(), computeGate: make(chan struct{}, 1), client: &http.Client{Timeout: 120 * time.Second}, fetch: newCboeClient().Fetch, now: time.Now, status: "idle"}
}
func (s *DesktopService) Start() {
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
	s.client.CloseIdleConnections()
}
func (s *DesktopService) setStatus(state, message string) {
	s.statusMu.Lock()
	s.status, s.statusError = state, message
	s.statusMu.Unlock()
}

// Numerical calls have a single execution permit. Once Python has started, we
// drain its response even if a UI waiter goes away; releasing the permit early
// would only pile more work into Python's socket queue.
func (s *DesktopService) compute(ctx context.Context, path string, payload any) ([]byte, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ctx.Done():
		return nil, s.ctx.Err()
	case s.computeGate <- struct{}{}:
	}
	defer func() { <-s.computeGate }()
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	var body io.Reader
	method := "GET"
	if payload != nil {
		b, e := json.Marshal(payload)
		if e != nil {
			return nil, e
		}
		body = bytes.NewReader(b)
		method = "POST"
	}
	call, cancel := context.WithTimeout(s.ctx, 120*time.Second)
	defer cancel()
	req, e := http.NewRequestWithContext(call, method, s.pythonURL+path, body)
	if e != nil {
		return nil, e
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, e := s.client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if e != nil {
		return nil, e
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("计算服务: %s", string(b))
	}
	if !json.Valid(b) {
		return nil, fmt.Errorf("计算服务返回无效数据")
	}
	return b, nil
}

type serviceTransport struct{ service *DesktopService }

func (t serviceTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if e := req.Context().Err(); e != nil {
		return nil, e
	}
	r := httptest.NewRecorder()
	t.service.ServeHTTP(r, req)
	return r.Result(), nil
}
func newLocalAPIClient(s *DesktopService) *APIClient {
	return &APIClient{base: "http://desktop.local", client: &http.Client{Transport: serviceTransport{s}, Timeout: 180 * time.Second}}
}
func sendJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func (s *DesktopService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var out any
	var err error
	ctx := r.Context()
	q := r.URL.Query()
	path := r.URL.Path
	if r.Method == http.MethodGet {
		switch path {
		case "/health", "/_stcore/health":
			out = map[string]bool{"ok": true}
		case "/api/state":
			out, err = s.state(ctx)
		case "/api/chain":
			out, err = s.chain(ctx, q)
		case "/api/search":
			out, err = s.search(ctx, q.Get("q"))
		case "/api/workbench", "/api/volatility", "/api/contract", "/api/flow":
			ticker, e := normalizeSymbol(q.Get("ticker"))
			if e != nil {
				err = e
				break
			}
			q.Set("ticker", ticker)
			// All formula inputs and replay filters are in the canonical query. Time
			// boundaries cover current-day filtering; fresh labels are attached below.
			local := s.now().In(marketZone)
			epoch := local.Format("2006-01-02")
			if local.Hour() >= 16 {
				epoch += "/closed"
			}
			key := fmt.Sprintf("view/%d/%s/%s?%s", s.store.revision.Load(), epoch, path, q.Encode())
			var b []byte
			b, err = s.cache.Do(ctx, key, func(task context.Context) ([]byte, error) { return s.buildViewJSON(task, path, q) })
			if err == nil {
				if path == "/api/workbench" {
					var wb WorkbenchResponse
					err = json.Unmarshal(b, &wb)
					if err == nil {
						wb.Dot = "rest"
						wb.StatusNote = "本地快照 · 手动刷新"
						out = &wb
					}
				} else {
					out = json.RawMessage(b)
				}
			}
		default:
			sendJSON(w, 404, map[string]string{"error": "未知查询"})
			return
		}
	} else if r.Method == http.MethodPost {
		var data map[string]json.RawMessage
		err = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&data)
		if err == nil {
			str := func(key string) string { var v string; _ = json.Unmarshal(data[key], &v); return v }
			switch path {
			case "/api/collect":
				var result map[string]string
				result, err = s.Collect(ctx, str("ticker"))
				out = map[string]any{"results": result}
			case "/api/collect_intraday":
				err = s.collectHighFreqTick(ctx, str("ticker"))
				out = map[string]bool{"ok": err == nil}
			case "/api/ticker_collect_config":
				var cfg TickerCollectConfig
				b, _ := json.Marshal(data)
				err = json.Unmarshal(b, &cfg)
				if err == nil {
					err = s.store.saveTickerCollectConfig(ctx, cfg)
					if err == nil && cfg.HighFreqEnabled && os.Getenv("GEX_DISABLE_SCHEDULER") != "1" {
						go func(tkr string) {
							_ = s.collectHighFreqTick(s.ctx, tkr)
						}(cfg.Ticker)
					}
				}
				out = map[string]bool{"ok": err == nil}
			case "/api/schedule":
				var cfg ScheduleConfig
				b, _ := json.Marshal(data)
				err = json.Unmarshal(b, &cfg)
				if err == nil {
					err = s.saveSchedule(ctx, cfg)
				}
				out = map[string]bool{"ok": err == nil}
			case "/api/watchlist/add", "/api/watchlist/remove":
				var ticker string
				ticker, err = normalizeSymbol(str("ticker"))
				if err != nil {
					break
				}
				if path == "/api/watchlist/add" && (ticker == "VIX" || ticker == "VIXW") {
					err = fmt.Errorf("暂不支持 VIX：其期权标的是期货，不能使用指数现价计算")
					break
				}
				err = s.store.transaction(ctx, func(tx *sql.Tx) error {
					query := "INSERT INTO watchlist(ticker) VALUES(?) ON CONFLICT DO NOTHING"
					if strings.HasSuffix(path, "remove") {
						query = "DELETE FROM watchlist WHERE ticker=?"
					}
					_, e := tx.ExecContext(ctx, query, ticker)
					return e
				})
				out = map[string]bool{"ok": err == nil}
			case "/api/pin":
				ticker, e := normalizeSymbol(str("ticker"))
				if e != nil {
					err = e
					break
				}
				expiry := str("expiry")
				if _, e = time.Parse("2006-01-02", expiry); e != nil {
					err = e
					break
				}
				var strike float64
				err = json.Unmarshal(data["strike"], &strike)
				if err != nil {
					break
				}
				side := str("type")
				if strike <= 0 || (side != "call" && side != "put") {
					err = fmt.Errorf("无效合约")
					break
				}
				err = s.store.transaction(ctx, func(tx *sql.Tx) error {
					_, e := tx.ExecContext(ctx, "INSERT INTO tracked_contracts(ticker,expiry,strike,option_type) VALUES(?,?,?,?) ON CONFLICT DO NOTHING", ticker, expiry, strike, side)
					return e
				})
				out = map[string]bool{"ok": err == nil}
			case "/api/unpin":
				var id int64
				err = json.Unmarshal(data["id"], &id)
				if err == nil {
					err = s.store.transaction(ctx, func(tx *sql.Tx) error {
						_, e := tx.ExecContext(ctx, "DELETE FROM tracked_contracts WHERE id=?", id)
						return e
					})
				}
				out = map[string]bool{"ok": err == nil}
			case "/api/backup":
				dest := filepath.Join(filepath.Dir(s.store.path), "backups", "manual-"+s.now().UTC().Format("20060102-150405.000000")+".db")
				err = s.store.backup(ctx, dest)
				out = map[string]string{"path": dest}
			default:
				sendJSON(w, 404, map[string]string{"error": "未知操作"})
				return
			}
		}
	} else {
		sendJSON(w, 405, map[string]string{"error": "不支持的操作"})
		return
	}
	if err != nil {
		sendJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	sendJSON(w, 200, out)
}

func (s *DesktopService) state(ctx context.Context) (*AppStateResponse, error) {
	watch, e := s.store.watchlist(ctx)
	if e != nil {
		return nil, e
	}
	cfg, e := s.schedule(ctx)
	if e != nil {
		return nil, e
	}
	out := &AppStateResponse{Version: releaseVersion(), Watchlist: watch, Tickers: []TickerSummary{}, Runs: []RunItem{}, Cooldowns: map[string]string{}, Schedule: cfg, RefreshMode: "manual"}
	s.statusMu.Lock()
	out.CollectorState, out.CollectorError = s.status, s.statusError
	s.statusMu.Unlock()
	for _, ticker := range watch {
		m, e := s.store.meta(ctx, ticker)
		if e != nil {
			return nil, e
		}
		summary := TickerSummary{Symbol: ticker, Source: m.Source, HasData: m.At != "", NetGEXStr: "—", StateCN: "无数据"}
		if m.At != "" {
			key := "summary/" + ticker + "/" + m.Source + "/" + m.At
			b, e := s.cache.Do(ctx, key, func(task context.Context) ([]byte, error) {
				return s.buildSummaryJSON(task, ticker, m.Source)
			})
			if e != nil {
				return nil, e
			}
			if e = json.Unmarshal(b, &summary); e != nil {
				return nil, e
			}
			summary.Spot = &m.Spot
			summary.CollectedAt = displayStamp(m.At)
			summary.Since = m.Since[:min(10, len(m.Since))]
		}
		if tCfg, cfgErr := s.store.tickerCollectConfig(ctx, ticker); cfgErr == nil {
			summary.LowFreqEnabled = tCfg.LowFreqEnabled
			summary.HighFreqEnabled = tCfg.HighFreqEnabled
			summary.HighFreqSec = tCfg.HighFreqSec
		} else {
			summary.LowFreqEnabled = true
			summary.HighFreqSec = 60
		}
		out.Tickers = append(out.Tickers, summary)
	}
	rows, e := s.store.read.QueryContext(ctx, `SELECT CAST(started_at AS TEXT),ticker,source,status,COALESCE(rows_fetched,0),oi_zero_fraction,COALESCE(error_message,'') FROM collection_runs ORDER BY started_at DESC,id DESC LIMIT 25`)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var r RunItem
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
		raw, e := s.store.setting(ctx, "provider_cooldown_until:"+src)
		if e != nil {
			return nil, e
		}
		if t, e := parseStamp(raw); e == nil && t.After(s.now()) {
			out.Cooldowns[src] = t.UTC().Format("15:04 UTC")
		}
	}
	sessions, e := s.calendar(ctx)
	if e == nil {
		out.NextCollection = nextSlot(s.now(), cfg, sessions)
	} else {
		out.NextCollection = "交易日历暂不可用"
	}
	return out, nil
}
func parseStamp(v string) (time.Time, error) {
	for _, layout := range []string{stampLayout, "2006-01-02 15:04:05", time.RFC3339Nano, "2006-01-02T15:04:05.999999"} {
		if t, e := time.Parse(layout, v); e == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid timestamp %q", v)
}
func displayStamp(v string) string {
	if t, e := parseStamp(v); e == nil {
		return t.In(marketZone).Format("01-02 15:04 MST")
	}
	return v
}

func (s *DesktopService) Collect(ctx context.Context, ticker string) (map[string]string, error) {
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
	tickers, e := s.store.watchlist(pass)
	if e != nil {
		return nil, e
	}
	if ticker != "" {
		ticker, e = normalizeSymbol(ticker)
		if e != nil {
			return nil, e
		}
		tickers = []string{ticker}
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
			if tCfg, cfgErr := s.store.tickerCollectConfig(pass, t); cfgErr == nil && !tCfg.LowFreqEnabled {
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
			raw, e := s.store.setting(pass, "provider_cooldown_until:"+src)
			if e != nil {
				return results, e
			}
			if until, e := parseStamp(raw); e == nil && until.After(s.now()) {
				failures = append(failures, src+": 冷却中")
				continue
			}
			at := s.now()
			var snap Snapshot
			if src == "cboe" {
				snap, e = s.fetch(pass, t)
			} else {
				snap, e = s.fetchYahooOnDemand(pass, t)
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
				var pe *ProviderError
				if errors.As(e, &pe) && pe.Status == 429 {
					if err := s.store.setSetting(pass, "provider_cooldown_until:"+src, stamp(s.now().Add(max(pe.RetryAfter, 30*time.Minute)))); err != nil {
						return results, err
					}
				}
				if err := s.store.logFailure(pass, t, src, at, e.Error(), len(snap.Rows), zero); err != nil {
					return results, err
				}
				failures = append(failures, src+": "+e.Error())
				continue
			}
			if e = s.store.saveSnapshot(pass, t, src, at, snap, zero); e != nil {
				results[t] = "failed: SQLite: " + e.Error()
				break
			}
			_ = s.recordIntradayBar(pass, t, src, at, snap)
			results[t] = "success"
			// Calculations are independent of the committed market snapshot. A failed
			// derivation is visible, and the next collection can retry pending rows.
			if deriveErr := s.deriveGo(pass, t, src); deriveErr != nil {
				lastError = "快照已保存；派生摘要待重试: " + deriveErr.Error()
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
	if e = pass.Err(); e == nil {
		if e = s.maintenance(pass); e != nil {
			lastError = "采集完成；维护未完成: " + e.Error()
		}
	}
	return results, nil
}

func (s *DesktopService) maintenance(ctx context.Context) error {
	today := s.now().In(marketZone).Format("2006-01-02")
	last, e := s.store.setting(ctx, "desktop_archive_day")
	if e != nil {
		return e
	}
	if last != today {
		if e = s.store.archive(ctx); e != nil {
			return e
		}
		if e = s.store.setSetting(ctx, "desktop_archive_day", today); e != nil {
			return e
		}
	}
	year, week := s.now().ISOWeek()
	key := fmt.Sprintf("%d-%02d", year, week)
	last, e = s.store.setting(ctx, "desktop_backup_week")
	if e != nil {
		return e
	}
	if last != key {
		dest := filepath.Join(filepath.Dir(s.store.path), "backups", "weekly-"+key+".db")
		if _, err := os.Stat(dest); errors.Is(err, os.ErrNotExist) {
			if e = s.store.backup(ctx, dest); e != nil {
				return e
			}
		}
		if e = s.store.setSetting(ctx, "desktop_backup_week", key); e != nil {
			return e
		}
	}
	// Directory refresh is optional and isolated from option data. Once per week.
	last, e = s.store.setting(ctx, "catalogue_refreshed_at")
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
	return s.store.transaction(ctx, func(tx *sql.Tx) error {
		stmt, e := tx.Prepare("INSERT INTO option_symbols(symbol,company) VALUES(?,?) ON CONFLICT(symbol) DO UPDATE SET company=excluded.company,last_seen_at=CURRENT_TIMESTAMP")
		if e != nil {
			return e
		}
		defer stmt.Close()
		for _, row := range entries {
			if len(row) <= max(symbolCol, companyCol) {
				continue
			}
			symbol, e := normalizeSymbol(row[symbolCol])
			if e != nil {
				continue
			}
			if _, e = stmt.ExecContext(ctx, symbol, row[companyCol]); e != nil {
				return e
			}
		}
		return setSetting(tx, "catalogue_refreshed_at", today)
	})
}
func (s *DesktopService) search(ctx context.Context, q string) (any, error) {
	query := `SELECT symbol,COALESCE(company,'') FROM option_symbols WHERE instr(upper(symbol),?)>0 OR instr(upper(company),?)>0 ORDER BY CASE WHEN symbol=? THEN 0 ELSE 1 END,length(company),symbol LIMIT 25`
	q = strings.ToUpper(strings.TrimSpace(q))
	rows, e := s.store.read.QueryContext(ctx, query, q, q, q)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var sym, name string
		if e = rows.Scan(&sym, &name); e != nil {
			return nil, e
		}
		out = append(out, map[string]string{"symbol": sym, "label": sym + " · " + name})
	}
	return map[string]any{"results": out}, rows.Err()
}

func intQuery(q url.Values, key string, defaultValue, lo, hi int) int {
	v, e := strconv.Atoi(q.Get(key))
	if e != nil {
		return defaultValue
	}
	return min(hi, max(lo, v))
}
