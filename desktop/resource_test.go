package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in local measurement. GEX_PERF_DB must name a disposable SQLite backup.
// No provider requests are made; automatic collection is never started here.
func TestResourceAudit(t *testing.T) {
	path := os.Getenv("GEX_PERF_DB")
	if path == "" {
		t.Skip("set GEX_PERF_DB to a disposable database copy")
	}
	root, _ := filepath.Abs("..")
	store, e := openStore(root, path)
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	service := newDesktopService(store, root, "")
	defer service.Close()
	client := newLocalAPIClient(service)
	process := func() string {
		b, _ := exec.Command("ps", "-axo", "pid=,ppid=,rss=,time=,comm=").Output()
		var out []string
		for _, line := range strings.Split(string(b), "\n") {
			parts := strings.Fields(line)
			if len(parts) > 2 && (parts[0] == strconv.Itoa(os.Getpid()) || parts[1] == strconv.Itoa(os.Getpid())) && !strings.Contains(line, "/ps") {
				out = append(out, line)
			}
		}
		return strings.Join(out, "\n")
	}
	result := map[string]any{"before": process()}
	timings := []float64{}
	for i := 0; i < 100; i++ {
		start := time.Now()
		if _, e := client.FetchState(); e != nil {
			t.Fatal(e)
		}
		timings = append(timings, float64(time.Since(start).Microseconds())/1000)
	}
	result["state_100_ms"] = timings
	warm := append([]float64{}, timings[1:]...)
	sort.Float64s(warm)
	result["state_warm_median_ms"] = warm[len(warm)/2]
	result["after_state"] = process()
	start := time.Now()
	if _, e := client.FetchWorkbench("SPX", "gex", "net", "", 12, 20); e != nil {
		t.Fatal(e)
	}
	result["workbench_cold_ms"] = float64(time.Since(start).Microseconds()) / 1000
	start = time.Now()
	if _, e := client.FetchWorkbench("SPX", "gex", "net", "", 12, 20); e != nil {
		t.Fatal(e)
	}
	result["workbench_cached_ms"] = float64(time.Since(start).Microseconds()) / 1000
	start = time.Now()
	if _, e := client.FetchChain("SPX", "", "all", 10, 0); e != nil {
		t.Fatal(e)
	}
	result["chain_page_cold_ms"] = float64(time.Since(start).Microseconds()) / 1000
	start = time.Now()
	if _, e := client.FetchChain("SPX", "", "all", 10, 0); e != nil {
		t.Fatal(e)
	}
	result["chain_page_cached_ms"] = float64(time.Since(start).Microseconds()) / 1000
	var wg sync.WaitGroup
	burst := make([]float64, 6)
	errs := make([]error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			start := time.Now()
			_, errs[i] = client.FetchWorkbench("SPX", "vanna", "net", "", 24, 40)
			burst[i] = float64(time.Since(start).Microseconds()) / 1000
		}(i)
	}
	wg.Wait()
	for _, e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	result["burst_6_ms"] = burst
	result["after_burst"] = process()
	result["read_pool"] = store.read.Stats()
	result["write_pool"] = store.write.Stats()
	if raw := os.Getenv("GEX_CBOE_FIXTURE"); raw != "" {
		b, e := os.ReadFile(raw)
		if e != nil {
			t.Fatal(e)
		}
		start := time.Now()
		snap, e := parseCboe(strings.NewReader(string(b)))
		if e != nil {
			t.Fatal(e)
		}
		result["cboe_decode_and_parse_ms"] = float64(time.Since(start).Microseconds()) / 1000
		result["cboe_rows"] = len(snap.Rows)
	}
	b, e := json.MarshalIndent(result, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	out := os.Getenv("GEX_PERF_OUTPUT")
	if out == "" {
		t.Log(string(b))
	} else if e = os.WriteFile(out, b, 0600); e != nil {
		t.Fatal(e)
	}
}
