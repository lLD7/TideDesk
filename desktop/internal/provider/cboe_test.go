package provider

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCboeNativeValuesAndRateLimit(t *testing.T) {
	raw := `{"timestamp":"2026-11-06 16:24:09","data":{"current_price":100,"options":[{"option":"SPX261218C00100000","gamma":0.012,"delta":0.55,"iv":0.23,"open_interest":42,"bid":-1,"vega":null}]}}`
	snap, e := ParseCboe(strings.NewReader(raw))
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
}
