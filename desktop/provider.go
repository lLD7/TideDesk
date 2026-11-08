package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type OptionRow struct {
	Expiry    string   `json:"expiry"`
	Strike    float64  `json:"strike"`
	Type      string   `json:"option_type"`
	Symbol    string   `json:"contract_symbol"`
	QuoteTime string   `json:"quote_timestamp"`
	Last      *float64 `json:"last_price"`
	Bid       *float64 `json:"bid"`
	Ask       *float64 `json:"ask"`
	Volume    *float64 `json:"volume"`
	OI        *float64 `json:"open_interest"`
	IV        *float64 `json:"implied_volatility"`
	Delta     *float64 `json:"delta"`
	Gamma     *float64 `json:"gamma"`
	Theta     *float64 `json:"theta"`
	Vega      *float64 `json:"vega"`
	ITM       bool     `json:"in_the_money"`
}
type Snapshot struct {
	Spot float64     `json:"spot"`
	Rows []OptionRow `json:"rows"`
}
type ProviderError struct {
	Status     int
	RetryAfter time.Duration
	Message    string
}

func (e *ProviderError) Error() string { return e.Message }

var symbolPattern = regexp.MustCompile(`^[A-Z0-9][A-Z0-9.\-]{0,15}$`)
var occPattern = regexp.MustCompile(`^(.+)(\d{6})([CP])(\d{8})$`)

func normalizeSymbol(value string) (string, error) {
	s := strings.TrimLeft(strings.ToUpper(strings.TrimSpace(value)), "^_")
	if !symbolPattern.MatchString(s) {
		return "", fmt.Errorf("请输入有效标的代码")
	}
	return s, nil
}
func cboeURL(ticker string) (string, error) {
	s, err := normalizeSymbol(ticker)
	if err != nil {
		return "", err
	}
	switch s {
	case "SPXW":
		s = "SPX"
	case "NDXP":
		s = "NDX"
	}
	switch s {
	case "SPX", "XSP", "NDX", "RUT", "DJX", "VIX", "OEX", "XEO":
		s = "_" + s
	}
	return "https://cdn.cboe.com/api/global/delayed_quotes/options/" + s + ".json", nil
}

// Cboe sometimes uses strings for numeric fields. Missing/invalid values stay
// NULL instead of becoming valid zero Greeks.
type feedNumber struct{ value *float64 }

func (n *feedNumber) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	f, err := strconv.ParseFloat(s, 64)
	if err == nil && !math.IsNaN(f) && !math.IsInf(f, 0) {
		n.value = &f
	}
	return nil
}
func nonnegative(n *float64) *float64 {
	if n != nil && *n < 0 {
		return nil
	}
	return n
}
func parseCboe(r io.Reader) (Snapshot, error) {
	var payload struct {
		Timestamp string `json:"timestamp"`
		Data      struct {
			Spot    feedNumber `json:"current_price"`
			Options []struct {
				Symbol string     `json:"option"`
				Last   feedNumber `json:"last_trade_price"`
				Bid    feedNumber `json:"bid"`
				Ask    feedNumber `json:"ask"`
				Volume feedNumber `json:"volume"`
				OI     feedNumber `json:"open_interest"`
				IV     feedNumber `json:"iv"`
				Delta  feedNumber `json:"delta"`
				Gamma  feedNumber `json:"gamma"`
				Theta  feedNumber `json:"theta"`
				Vega   feedNumber `json:"vega"`
			} `json:"options"`
		} `json:"data"`
	}
	dec := json.NewDecoder(io.LimitReader(r, 64<<20))
	if err := dec.Decode(&payload); err != nil {
		return Snapshot{}, err
	}
	if payload.Data.Spot.value == nil || *payload.Data.Spot.value <= 0 {
		return Snapshot{}, fmt.Errorf("Cboe 未返回有效现价")
	}
	if len(payload.Data.Options) == 0 {
		return Snapshot{}, fmt.Errorf("Cboe 返回空期权链")
	}
	out := Snapshot{Spot: *payload.Data.Spot.value, Rows: make([]OptionRow, 0, len(payload.Data.Options))}
	seen := make(map[string]bool, len(payload.Data.Options))
	dates := map[string]string{}
	for _, o := range payload.Data.Options {
		m := occPattern.FindStringSubmatch(o.Symbol)
		if m == nil || seen[o.Symbol] {
			return Snapshot{}, fmt.Errorf("Cboe 合约代码无效或重复: %.40s", o.Symbol)
		}
		seen[o.Symbol] = true
		expiry, ok := dates[m[2]]
		if !ok {
			t, e := time.Parse("060102", m[2])
			if e != nil {
				return Snapshot{}, e
			}
			expiry = t.Format("2006-01-02")
			dates[m[2]] = expiry
		}
		strike, _ := strconv.ParseFloat(m[4], 64)
		strike /= 1000
		side := "call"
		itm := out.Spot > strike
		if m[3] == "P" {
			side = "put"
			itm = out.Spot < strike
		}
		iv := o.IV.value
		if iv != nil && *iv <= 0 {
			iv = nil
		}
		delta := o.Delta.value
		if delta != nil && math.Abs(*delta) > 1 {
			delta = nil
		}
		out.Rows = append(out.Rows, OptionRow{Expiry: expiry, Strike: strike, Type: side, Symbol: o.Symbol, QuoteTime: payload.Timestamp, Last: nonnegative(o.Last.value), Bid: nonnegative(o.Bid.value), Ask: nonnegative(o.Ask.value), Volume: nonnegative(o.Volume.value), OI: nonnegative(o.OI.value), IV: iv, Delta: delta, Gamma: nonnegative(o.Gamma.value), Theta: o.Theta.value, Vega: nonnegative(o.Vega.value), ITM: itm})
	}
	return out, nil
}

type CboeClient struct {
	client *http.Client
	url    func(string) (string, error)
}

func newCboeClient() *CboeClient {
	return &CboeClient{client: &http.Client{Timeout: 45 * time.Second}, url: cboeURL}
}
func (p *CboeClient) Fetch(ctx context.Context, ticker string) (Snapshot, error) {
	endpoint, err := p.url(ticker)
	if err != nil {
		return Snapshot{}, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if e != nil {
			return Snapshot{}, e
		}
		req.Header.Set("User-Agent", "TideDesk/"+releaseVersion()+" (personal research)")
		resp, e := p.client.Do(req)
		if e == nil {
			if resp.StatusCode == 200 {
				result, parseErr := parseCboe(resp.Body)
				resp.Body.Close()
				return result, parseErr
			}
			io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			retry := 30 * time.Minute
			if seconds, parseErr := strconv.Atoi(resp.Header.Get("Retry-After")); parseErr == nil && seconds > 0 {
				retry = max(retry, time.Duration(seconds)*time.Second)
			} else if when, parseErr := http.ParseTime(resp.Header.Get("Retry-After")); parseErr == nil {
				retry = max(retry, time.Until(when))
			}
			e = &ProviderError{Status: resp.StatusCode, RetryAfter: retry, Message: fmt.Sprintf("Cboe HTTP %d", resp.StatusCode)}
			if resp.StatusCode < 500 {
				return Snapshot{}, e
			}
		}
		err = e
		if attempt < 2 {
			select {
			case <-ctx.Done():
				return Snapshot{}, ctx.Err()
			case <-time.After(time.Duration(1<<attempt) * time.Second):
			}
		}
	}
	return Snapshot{}, err
}
