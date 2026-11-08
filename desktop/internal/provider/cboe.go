package provider

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

func NormalizeSymbol(value string) (string, error) {
	s := strings.TrimLeft(strings.ToUpper(strings.TrimSpace(value)), "^_")
	if !symbolPattern.MatchString(s) {
		return "", fmt.Errorf("请输入有效标的代码")
	}
	return s, nil
}
func cboeURL(ticker string) (string, error) {
	s, err := NormalizeSymbol(ticker)
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

type cboeOption struct {
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
}

func ParseCboe(r io.Reader) (Snapshot, error) {
	dec := json.NewDecoder(io.LimitReader(r, 64<<20))
	var out Snapshot
	var stamp string
	var spot feedNumber
	err := decodeCboeObject(dec, func(key string) error {
		switch key {
		case "timestamp":
			return dec.Decode(&stamp)
		case "data":
			return decodeCboeObject(dec, func(key string) error {
				switch key {
				case "current_price":
					return dec.Decode(&spot)
				case "options":
					rows, err := decodeCboeOptions(dec)
					out.Rows = rows
					return err
				default:
					return dec.Decode(new(json.RawMessage))
				}
			})
		default:
			return dec.Decode(new(json.RawMessage))
		}
	})
	if err != nil {
		return Snapshot{}, err
	}
	if spot.value == nil || *spot.value <= 0 {
		return Snapshot{}, fmt.Errorf("Cboe 未返回有效现价")
	}
	if len(out.Rows) == 0 {
		return Snapshot{}, fmt.Errorf("Cboe 返回空期权链")
	}
	out.Spot = *spot.value
	for i := range out.Rows {
		row := &out.Rows[i]
		row.QuoteTime = stamp
		row.ITM = (row.Type == "call" && out.Spot > row.Strike) ||
			(row.Type == "put" && out.Spot < row.Strike)
	}
	return out, nil
}

func readDelimiter(dec *json.Decoder, want json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return err
	}
	if token != want {
		return fmt.Errorf("Cboe JSON 缺少 %c", want)
	}
	return nil
}

func decodeCboeObject(dec *json.Decoder, field func(string) error) error {
	if err := readDelimiter(dec, '{'); err != nil {
		return err
	}
	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return err
		}
		key, ok := token.(string)
		if !ok {
			return fmt.Errorf("Cboe JSON 字段名无效")
		}
		if err := field(key); err != nil {
			return err
		}
	}
	return readDelimiter(dec, '}')
}

func decodeCboeOptions(dec *json.Decoder) ([]OptionRow, error) {
	if err := readDelimiter(dec, '['); err != nil {
		return nil, err
	}
	var rows []OptionRow
	seen := map[string]bool{}
	dates := map[string]string{}
	for dec.More() {
		var o cboeOption
		if err := dec.Decode(&o); err != nil {
			return nil, err
		}
		if len(o.Symbol) <= 15 || seen[o.Symbol] {
			return nil, fmt.Errorf("Cboe 合约代码无效或重复: %.40s", o.Symbol)
		}
		seen[o.Symbol] = true
		suffix := o.Symbol[len(o.Symbol)-15:]
		day := suffix[:6]
		expiry, ok := dates[day]
		if !ok {
			t, err := time.Parse("060102", day)
			if err != nil {
				return nil, err
			}
			expiry = t.Format("2006-01-02")
			dates[day] = expiry
		}
		side := "call"
		switch suffix[6] {
		case 'P':
			side = "put"
		case 'C':
		default:
			return nil, fmt.Errorf("Cboe 合约类型无效: %.40s", o.Symbol)
		}
		var strike int
		for _, digit := range suffix[7:] {
			if digit < '0' || digit > '9' {
				return nil, fmt.Errorf("Cboe 行权价无效: %.40s", o.Symbol)
			}
			strike = strike*10 + int(digit-'0')
		}
		iv := o.IV.value
		if iv != nil && *iv <= 0 {
			iv = nil
		}
		delta := o.Delta.value
		if delta != nil && math.Abs(*delta) > 1 {
			delta = nil
		}
		rows = append(rows, OptionRow{
			Expiry: expiry, Strike: float64(strike) / 1000, Type: side, Symbol: o.Symbol,
			Last: nonnegative(o.Last.value), Bid: nonnegative(o.Bid.value), Ask: nonnegative(o.Ask.value),
			Volume: nonnegative(o.Volume.value), OI: nonnegative(o.OI.value), IV: iv, Delta: delta,
			Gamma: nonnegative(o.Gamma.value), Theta: o.Theta.value, Vega: nonnegative(o.Vega.value),
		})
	}
	return rows, readDelimiter(dec, ']')
}

type CboeClient struct {
	client  *http.Client
	url     func(string) (string, error)
	version string
}

func NewCboeClient(version string) *CboeClient {
	return &CboeClient{client: &http.Client{Timeout: 45 * time.Second}, url: cboeURL, version: version}
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
		req.Header.Set("User-Agent", "TideDesk/"+p.version+" (personal research)")
		resp, e := p.client.Do(req)
		if e == nil {
			if resp.StatusCode == 200 {
				result, parseErr := ParseCboe(resp.Body)
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
