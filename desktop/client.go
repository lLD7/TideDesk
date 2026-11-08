package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/provider"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"
)

type workbenchQuery struct {
	Dimension, Side, AsOf string
	Expiries, Strikes     int
	RangePct              float64 // zero selects the workbench
}

type contractQuery struct {
	Expiry, Side string
	Strike       float64
}

// Views and snapshots returned from the cache are immutable, including all
// slices, pointed-to numbers and nested view data. Calculations that need to
// assign fields must use owned rows and replace pointers instead of mutating them.
type preparedSnapshot struct {
	rows []store.StoredOptionRow
}

func (s *DesktopService) snapshot(ctx context.Context, m store.SnapshotMeta) (*preparedSnapshot, error) {
	if m.At == "" {
		return &preparedSnapshot{}, nil
	}
	key := fmt.Sprintf("snapshot/%s/%s/%s/%s", m.Ticker, m.Source, m.At, ModelVersion)
	return cached(s.cache, ctx, key, func(task context.Context) (*preparedSnapshot, error) {
		rows, err := s.store.SnapshotsAt(task, m.Ticker, m.Source, []string{m.At})
		if err != nil {
			return nil, err
		}
		if err := solveSnapshotIV(task, rows); err != nil {
			return nil, err
		}
		return &preparedSnapshot{rows: rows}, nil
	})
}

func solveSnapshotIV(ctx context.Context, rows []store.StoredOptionRow) error {
	return solveIV(ctx, rows, defaultRiskFreeRate, 0)
}

func (s *DesktopService) snapshotRibbon(ctx context.Context, m store.SnapshotMeta, rows []store.StoredOptionRow) (*viewmodel.RibbonData, error) {
	key := fmt.Sprintf("ribbon/%s/%s/%s/%s", m.Ticker, m.Source, m.At, ModelVersion)
	return cached(s.cache, ctx, key, func(task context.Context) (*viewmodel.RibbonData, error) {
		if rows == nil {
			payload, err := s.store.SnapshotRibbon(task, m, ModelVersion)
			if err != nil {
				return nil, err
			}
			var saved viewmodel.RibbonData
			if len(payload) > 0 && json.Unmarshal(payload, &saved) == nil {
				return &saved, nil
			}
		}
		if rows == nil {
			snapshot, err := s.snapshot(task, m)
			if err != nil {
				return nil, err
			}
			rows = snapshot.rows
		}
		if err := task.Err(); err != nil {
			return nil, err
		}
		ribbon := buildRibbonPresolved(task, rows, defaultRiskFreeRate, 0)
		return &ribbon, task.Err()
	})
}

func (s *DesktopService) marketEpoch() string {
	now := s.now().In(marketclock.Zone)
	return fmt.Sprintf("%s/%t", now.Format("2006-01-02"), now.Hour() >= 16)
}

func (s *DesktopService) dataVersion(ticker string) uint64 {
	s.versionsMu.Lock()
	defer s.versionsMu.Unlock()
	return s.historyVersion[ticker]
}

func (s *DesktopService) historyChanged(ticker string) {
	s.versionsMu.Lock()
	s.historyVersion[ticker]++
	s.versionsMu.Unlock()
}

func (s *DesktopService) lockView(ctx context.Context) (func(), error) {
	select {
	case s.viewSlot <- struct{}{}:
		return func() { <-s.viewSlot }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func buildView[T any](s *DesktopService, ctx context.Context, key string, build func(context.Context) (T, error)) (*T, error) {
	return cached(s.cache, ctx, key, func(task context.Context) (*T, error) {
		unlock, err := s.lockView(task)
		if err != nil {
			return nil, err
		}
		defer unlock()
		if err := task.Err(); err != nil {
			return nil, err
		}
		result, err := build(task)
		return &result, err
	})
}

func (s *DesktopService) meta(ctx context.Context, ticker string) (store.SnapshotMeta, error) {
	ticker, err := provider.NormalizeSymbol(ticker)
	if err != nil {
		return store.SnapshotMeta{}, err
	}
	return s.store.Meta(ctx, ticker)
}

func (s *DesktopService) Workbench(ctx context.Context, ticker string, q workbenchQuery) (*viewmodel.WorkbenchResponse, error) {
	if q.RangePct != 0 && !(q.RangePct >= 1 && q.RangePct <= 50) {
		return nil, fmt.Errorf("行权价范围须为现价的 ±1%% 至 ±50%%")
	}
	m, err := s.meta(ctx, ticker)
	if err != nil {
		return nil, err
	}
	s.intradayMu.Lock()
	live := s.liveSnapshots[m.Ticker]
	version := s.barVersion[m.Ticker]
	s.intradayMu.Unlock()
	liveAt := ""
	if q.AsOf == "" && len(live) > 0 && live[0].CollectedAt > m.At {
		liveAt = live[0].CollectedAt
	} else {
		live = nil
	}
	key := fmt.Sprintf("workbench/%s/%s/%s/%s/%s/%d/%#v", m.Ticker, m.Source, m.At, liveAt, s.marketEpoch(), version, q)
	return buildView(s, ctx, key, func(task context.Context) (viewmodel.WorkbenchResponse, error) {
		return s.buildWorkbenchGo(task, m, live, version, q)
	})
}

func (s *DesktopService) Volatility(ctx context.Context, ticker string, width float64) (*viewmodel.VolatilityResponse, error) {
	normalized, err := provider.NormalizeSymbol(ticker)
	if err != nil {
		return nil, err
	}
	version := s.dataVersion(normalized)
	m, err := s.store.Meta(ctx, normalized)
	if err != nil {
		return nil, err
	}
	if math.IsNaN(width) || math.IsInf(width, 0) || width <= 0 {
		width = 12
	}
	key := fmt.Sprintf("volatility/%s/%s/%s/%s/%d/%g", m.Ticker, m.Source, m.At, s.marketEpoch(), version, width)
	return buildView(s, ctx, key, func(task context.Context) (viewmodel.VolatilityResponse, error) {
		return s.buildVolatilityGo(task, m, width)
	})
}

func (s *DesktopService) Contract(ctx context.Context, ticker string, q contractQuery) (*viewmodel.ContractResponse, error) {
	m, err := s.meta(ctx, ticker)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("contract/%s/%s/%s/%s/%#v", m.Ticker, m.Source, m.At, s.marketEpoch(), q)
	return buildView(s, ctx, key, func(task context.Context) (viewmodel.ContractResponse, error) {
		return s.buildContractGo(task, m, q)
	})
}

func (s *DesktopService) Flow(ctx context.Context, ticker string) (*viewmodel.FlowResponse, error) {
	normalized, err := provider.NormalizeSymbol(ticker)
	if err != nil {
		return nil, err
	}
	version := s.dataVersion(normalized)
	m, err := s.store.Meta(ctx, normalized)
	if err != nil {
		return nil, err
	}
	key := fmt.Sprintf("flow/%s/%s/%s/%s/%d", m.Ticker, m.Source, m.At, s.marketEpoch(), version)
	return buildView(s, ctx, key, func(task context.Context) (viewmodel.FlowResponse, error) {
		return s.buildFlowGo(task, m)
	})
}
