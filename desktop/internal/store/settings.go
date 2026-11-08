package store

import (
	"context"
	"database/sql"
	"strconv"
	"strings"

	"gammagrid.local/desktop/internal/provider"
)

type TickerCollectConfig struct {
	Ticker          string `json:"ticker"`
	LowFreqEnabled  bool   `json:"low_freq_enabled"`
	HighFreqEnabled bool   `json:"high_freq_enabled"`
	HighFreqSec     int    `json:"high_freq_sec"`
	HighFreqMinutes int
	HighFreqUntil   string
}

func NormalizeHighFreqSec(sec int) int {
	switch sec {
	case 60, 120, 180, 300, 600:
		return sec
	default:
		if sec <= 0 {
			return 60
		}
		if sec < 90 {
			return 60
		}
		if sec < 150 {
			return 120
		}
		if sec < 240 {
			return 180
		}
		if sec < 450 {
			return 300
		}
		return 600
	}
}

func (s *Store) TickerCollectConfig(ctx context.Context, ticker string) (TickerCollectConfig, error) {
	configs, err := s.TickerCollectConfigs(ctx, []string{ticker})
	return configs[strings.ToUpper(strings.TrimSpace(ticker))], err
}

func (s *Store) TickerCollectConfigs(ctx context.Context, tickers []string) (map[string]TickerCollectConfig, error) {
	out := make(map[string]TickerCollectConfig, len(tickers))
	for _, ticker := range tickers {
		ticker = strings.ToUpper(strings.TrimSpace(ticker))
		out[ticker] = TickerCollectConfig{Ticker: ticker, LowFreqEnabled: true, HighFreqSec: 60, HighFreqMinutes: 120}
	}
	if len(out) == 0 {
		return out, nil
	}
	rows, err := s.read.QueryContext(ctx, "SELECT key,value FROM app_settings WHERE key GLOB 'high_freq_*:*' OR key GLOB 'low_freq_disabled:*'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return nil, err
		}
		field, ticker, _ := strings.Cut(key, ":")
		cfg, ok := out[ticker]
		if !ok {
			continue
		}
		switch field {
		case "low_freq_disabled":
			cfg.LowFreqEnabled = value != "1"
		case "high_freq_enabled":
			cfg.HighFreqEnabled = value == "1"
		case "high_freq_sec":
			sec, _ := strconv.Atoi(value)
			cfg.HighFreqSec = NormalizeHighFreqSec(sec)
		case "high_freq_minutes":
			if minutes, err := strconv.Atoi(value); err == nil && minutes > 0 {
				cfg.HighFreqMinutes = minutes
			}
		case "high_freq_until":
			cfg.HighFreqUntil = value
		}
		out[ticker] = cfg
	}
	return out, rows.Err()
}

func (s *Store) SaveTickerCollectConfig(ctx context.Context, cfg TickerCollectConfig) error {
	tkr, err := provider.NormalizeSymbol(cfg.Ticker)
	if err != nil {
		return err
	}
	sec := NormalizeHighFreqSec(cfg.HighFreqSec)
	lowDisVal := "0"
	if !cfg.LowFreqEnabled {
		lowDisVal = "1"
	}
	hfEnVal := "0"
	if cfg.HighFreqEnabled {
		hfEnVal = "1"
	}
	return s.Transaction(ctx, func(tx *sql.Tx) error {
		if err := SetSetting(tx, "low_freq_disabled:"+tkr, lowDisVal); err != nil {
			return err
		}
		if err := SetSetting(tx, "high_freq_enabled:"+tkr, hfEnVal); err != nil {
			return err
		}
		if err := SetSetting(tx, "high_freq_sec:"+tkr, strconv.Itoa(sec)); err != nil {
			return err
		}
		if cfg.HighFreqMinutes > 0 {
			if err := SetSetting(tx, "high_freq_minutes:"+tkr, strconv.Itoa(cfg.HighFreqMinutes)); err != nil {
				return err
			}
		}
		return SetSetting(tx, "high_freq_until:"+tkr, cfg.HighFreqUntil)
	})
}
