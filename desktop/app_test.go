package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/viewmodel"

	"github.com/egoist/mygo/ui"
)

func sampleDesktopApp() *DesktopApp {
	spot := 567.82
	gex := -1690000000.0
	flip := 574.50
	atm := 568.0
	app := NewDesktopApp(newDesktopService(nil, ""))
	app.selectedSym = "SPY"
	app.state = &viewmodel.AppStateResponse{
		Schedule:        marketclock.DefaultSchedule(),
		RefreshMode:     "manual",
		NextCollection:  "11-09 10:00 EST",
		Version:         "0.3.0",
		Watchlist:       []string{"SPY", "SPX", "QQQ"},
		CollectorState:  "idle",
		IntervalMinutes: 15,
		IntervalChoices: []viewmodel.IntervalChoice{
			{Label: "关闭", Minutes: 0},
			{Label: "每 15 分钟", Minutes: 15},
			{Label: "每 30 分钟", Minutes: 30},
		},
		GrowthGBPerMonth: 0.42,
		Tickers: []viewmodel.TickerSummary{
			{Symbol: "SPY", Source: "CBOE", HasData: true, Spot: &spot, NetGEX: &gex, NetGEXStr: "-1.69B", StateCN: "负 Gamma · 波动放大", CollectedAt: "11-06 16:14 ET", LowFreqEnabled: true, HighFreqEnabled: true, HighFreqSec: 60},
			{Symbol: "SPX", Source: "CBOE", HasData: true, Spot: &spot, NetGEX: &gex, NetGEXStr: "-3.12B", StateCN: "负 Gamma · 波动放大", CollectedAt: "11-06 16:14 ET", LowFreqEnabled: true, HighFreqEnabled: false, HighFreqSec: 60},
		},
		Runs: []viewmodel.RunItem{
			{StartedAt: "11-06 16:14:22", Ticker: "SPY", Source: "cboe", Status: "ok", Rows: 8142, OIZero: "12%"},
			{StartedAt: "11-06 16:14:25", Ticker: "SPX", Source: "cboe", Status: "ok", Rows: 14520, OIZero: "9%"},
		},
	}
	app.scheduleTimes = "10:00, 12:00, 14:00, 16:15"
	app.wb = &viewmodel.WorkbenchResponse{
		Ticker:         "SPY",
		Source:         "CBOE",
		Empty:          false,
		CollectedAt:    "11-06 16:14 ET · CBOE",
		AsOf:           "2026-11-06T21:14:00",
		AsOfDisplay:    "11-06 16:14 ET",
		Moments:        []viewmodel.MomentOption{{Value: "2026-11-06T21:14:00", Label: "11-06 16:14 ET"}},
		ContractsCount: 8142,
		Ribbon: viewmodel.RibbonData{
			Spot:         spot,
			SpotStr:      "567.82",
			State:        "storm",
			StateCN:      "负 Gamma · 趋势加速",
			StateSub:     "做市商顺势对冲（助涨助跌），日内波动易放大",
			Positive:     false,
			Scope:        "12 到期 · ±20 档",
			NetGEX:       &gex,
			NetGEXStr:    "-1.69B",
			GammaFlip:    &flip,
			GammaFlipStr: "574.50",
			FlipDistStr:  "+1.18%",
			CallGEXWall:  "575",
			PutGEXWall:   "560",
			CallOIWall:   "580",
			PutOIWall:    "555",
			MaxPain:      "566",
			MaxPainExp:   "11-07",
			MoveStr:      "±6.42",
			MovePctStr:   "±1.13%",
			ATMIVStr:     "18.4%",
			PCROIStr:     "1.42",
			PCRVolStr:    "1.18",
		},
		Dimension:     "gex",
		DimensionCN:   "GEX 伽马敞口",
		UnitCN:        "USD / 1% 标的波动",
		Side:          "net",
		MaxExpiries:   12,
		ShownExpiries: 4,
		Spot:          spot,
		ATMStrike:     &atm,
		Flip:          &flip,
		Strikes:       []float64{575, 570, 568, 565, 560},
		StrikeLabels:  []string{"575", "570", "568", "565", "560"},
		StrikeTags:    []string{"◆C · ▲OI", "", "", "✕Pain", "◆P · ▼OI"},
		Expiries:      []string{"11-07", "11-10", "11-14", "11-21"},
		ExpiryDTEs:    []string{"1d", "4d", "8d", "15d"},
		Totals:        []float64{-620e6, -340e6, 210e6, -940e6},
		TotalsStr:     []string{"-620.0M", "-340.0M", "+210.0M", "-940.0M"},
		GrandTotal:    -1690e6,
		GrandTotalStr: "-1.69B",
		CellsText: [][]string{
			{"+380.2M", "+120.5M", "+440.0M", "+190.1M"},
			{"+95.4M", "-42.0M", "+88.1M", "-12.4M"},
			{"-180.5M", "-110.2M", "+32.0M", "-240.0M"},
			{"-410.0M", "-190.4M", "-110.5M", "-380.2M"},
			{"-505.1M", "-117.9M", "-239.6M", "-497.5M"},
		},
		CellsNorm: [][]float64{
			{0.75, 0.24, 0.88, 0.38},
			{0.19, -0.08, 0.17, -0.03},
			{-0.36, -0.22, 0.06, -0.48},
			{-0.81, -0.38, -0.22, -0.75},
			{-1.0, -0.23, -0.47, -0.98},
		},
		CellsTip: [][]string{
			{"行权价 575 · 到期 11-07: +380.2M", "", "", ""},
			{"", "", "", ""},
			{"", "", "", ""},
			{"", "", "", ""},
			{"", "", "", ""},
		},
		Profile:        []float64{1130.8e6, 129.1e6, -498.7e6, -1091.1e6, -1360.1e6},
		ProfileStr:     []string{"+1.13B", "+129.1M", "-498.7M", "-1.09B", "-1.36B"},
		ProfileMax:     1360.1e6,
		CallProfile:    []float64{1250.0e6, 480.0e6, 210.0e6, 95.0e6, 40.0e6},
		CallProfileStr: []string{"+1.25B", "+480.0M", "+210.0M", "+95.0M", "+40.0M"},
		PutProfile:     []float64{-119.2e6, -350.9e6, -708.7e6, -1186.1e6, -1400.1e6},
		PutProfileStr:  []string{"-119.2M", "-350.9M", "-708.7M", "-1.19B", "-1.40B"},
		StrikeCallOI:   []string{"84.2K", "51.0K", "39.4K", "22.1K", "14.8K"},
		StrikePutOI:    []string{"18.1K", "34.2K", "62.8K", "91.4K", "128.5K"},
		Intraday: viewmodel.IntradaySummary{
			DateLabel:         "11/06/2026",
			TotalVol:          9570000,
			TotalVolStr:       "9.57M",
			TotalPrem:         2897855971,
			TotalPremStr:      "$2,897,855,971",
			NetPrem:           66413673,
			NetPremStr:        "+$66,413,673",
			PutVolStr:         "5.09M",
			PutPremStr:        "$1.54B",
			CallVolStr:        "4.48M",
			CallPremStr:       "$1.36B",
			WeeklyScope:       "0 <= DTE <= 7 · 2 个近期到期日",
			WeeklyNetPrem:     48200000,
			WeeklyNetPremStr:  "+$48,200,000",
			WeeklyCallPremStr: "$920.4M",
			WeeklyPutPremStr:  "$872.2M",
			WeeklyNetGEX:      -960000000,
			WeeklyNetGEXStr:   "-960.0M",
			TimeBars: []viewmodel.IntradayBar{
				{Time: "09:35", WindowLabel: "11/06/2026 09:30 AM - 09:35 AM", Spot: 565.20, CallVol: 148000, PutVol: 186000, CallPrem: 42e6, PutPrem: 51e6, PCRatio: 1.257, TotalVol: 334000, TotalPrem: 93e6, CumWeeklyCall: 120e6, CumWeeklyPut: -140e6, CumWeeklyNet: -20e6, WeeklyNetGEX: -820e6},
				{Time: "10:30", WindowLabel: "11/06/2026 10:25 AM - 10:30 AM", Spot: 569.10, CallVol: 182000, PutVol: 134000, CallPrem: 58e6, PutPrem: 39e6, PCRatio: 0.736, TotalVol: 316000, TotalPrem: 97e6, CumWeeklyCall: 310e6, CumWeeklyPut: -290e6, CumWeeklyNet: 20e6, WeeklyNetGEX: -790e6},
				{Time: "11:30", WindowLabel: "11/06/2026 11:25 AM - 11:30 AM", Spot: 566.40, CallVol: 96000, PutVol: 128000, CallPrem: 29e6, PutPrem: 38e6, PCRatio: 1.333, TotalVol: 224000, TotalPrem: 67e6, CumWeeklyCall: 490e6, CumWeeklyPut: -460e6, CumWeeklyNet: 30e6, WeeklyNetGEX: -880e6},
				{Time: "13:00", WindowLabel: "11/06/2026 12:55 PM - 01:00 PM", Spot: 568.90, CallVol: 112000, PutVol: 94000, CallPrem: 34e6, PutPrem: 28e6, PCRatio: 0.839, TotalVol: 206000, TotalPrem: 62e6, CumWeeklyCall: 670e6, CumWeeklyPut: -630e6, CumWeeklyNet: 40e6, WeeklyNetGEX: -910e6},
				{Time: "14:15", WindowLabel: "11/06/2026 02:10 PM - 02:15 PM", Spot: 567.10, CallVol: 41934, PutVol: 47871, CallPrem: 8.2e6, PutPrem: 9.46e6, PCRatio: 1.142, TotalVol: 89805, TotalPrem: 17664771, CumWeeklyCall: 810e6, CumWeeklyPut: -760e6, CumWeeklyNet: 50e6, WeeklyNetGEX: -940e6},
				{Time: "15:45", WindowLabel: "11/06/2026 03:40 PM - 03:45 PM", Spot: 567.82, CallVol: 124000, PutVol: 118000, CallPrem: 37e6, PutPrem: 35e6, PCRatio: 0.952, TotalVol: 242000, TotalPrem: 72e6, CumWeeklyCall: 920.4e6, CumWeeklyPut: -872.2e6, CumWeeklyNet: 48.2e6, WeeklyNetGEX: -960e6},
			},
			StrikeBars: []viewmodel.IntradayBar{
				{Time: "560", WindowLabel: "Strike 560 · Spot 567.82", Spot: 567.82, CallVol: 28000, PutVol: 142000, PCRatio: 5.071, TotalVol: 170000, TotalPrem: 41e6},
				{Time: "565", WindowLabel: "Strike 565 · Spot 567.82", Spot: 567.82, CallVol: 64000, PutVol: 118000, PCRatio: 1.844, TotalVol: 182000, TotalPrem: 52e6},
				{Time: "568", WindowLabel: "Strike 568 · Spot 567.82", Spot: 567.82, CallVol: 132000, PutVol: 129000, PCRatio: 0.977, TotalVol: 261000, TotalPrem: 84e6},
				{Time: "570", WindowLabel: "Strike 570 · Spot 567.82", Spot: 567.82, CallVol: 164000, PutVol: 62000, PCRatio: 0.378, TotalVol: 226000, TotalPrem: 68e6},
				{Time: "575", WindowLabel: "Strike 575 · Spot 567.82", Spot: 567.82, CallVol: 192000, PutVol: 24000, PCRatio: 0.125, TotalVol: 216000, TotalPrem: 49e6},
			},
		},
		Board: []viewmodel.ExpiryBoardRow{
			{Expiry: "11-07", ExpiryISO: "2026-11-07", DTE: 1, NetGEX: -620e6, NetGEXStr: "-620.0M", CallGEXStr: "+510.0M", PutGEXStr: "-1.13B", MaxPain: "566", ATMIV: "18.4%", Move: "±6.42", MovePct: "±1.13%", CallOI: "245.1K", PutOI: "382.4K", PCR: "1.56", Contracts: 284},
			{Expiry: "11-10", ExpiryISO: "2026-11-10", DTE: 4, NetGEX: -340e6, NetGEXStr: "-340.0M", CallGEXStr: "+280.0M", PutGEXStr: "-620.0M", MaxPain: "568", ATMIV: "17.9%", Move: "±9.85", MovePct: "±1.73%", CallOI: "142.0K", PutOI: "198.2K", PCR: "1.40", Contracts: 240},
		},
	}
	app.vol = &viewmodel.VolatilityResponse{
		Ticker: "SPY",
		Spot:   spot,
		Skew: []viewmodel.SkewCurve{
			{Label: "11-07 (1d)", Strikes: []float64{550, 560, 568, 575, 585}, IVs: []float64{24.5, 20.8, 18.4, 17.1, 16.8}},
			{Label: "11-14 (8d)", Strikes: []float64{550, 560, 568, 575, 585}, IVs: []float64{22.1, 19.6, 17.9, 16.9, 16.5}},
		},
		Term: []viewmodel.TermPoint{
			{Label: "11-07", DTE: 1, ATMIV: 18.4, MovePct: 1.13, Move: 6.42},
			{Label: "11-10", DTE: 4, ATMIV: 17.9, MovePct: 1.73, Move: 9.85},
			{Label: "11-14", DTE: 8, ATMIV: 17.5, MovePct: 2.38, Move: 13.51},
		},
		IVHistory: []viewmodel.IVHistoryPoint{
			{Time: "10:00", IV: 19.2},
			{Time: "12:00", IV: 18.7},
			{Time: "16:00", IV: 18.4},
		},
		RV: []viewmodel.RVItem{
			{Window: "RV 5D", Value: "14.2%", Raw: 14.2},
			{Window: "RV 20D", Value: "15.8%", Raw: 15.8},
		},
	}
	app.chain = &viewmodel.ChainResponse{
		Ticker:        "SPY",
		Spot:          spot,
		TotalChain:    8142,
		TotalMatching: 2,
		Expiries:      []viewmodel.ExpiryOption{{ISO: "2026-11-07", Label: "11-07"}},
		Rows: []viewmodel.ChainRow{
			{ExpiryISO: "2026-11-07", Expiry: "11-07", DTE: 1, Strike: 568, StrikeStr: "568", Type: "C", TypeShort: "CALL", Moneyness: "ATM", Bid: "3.40", Ask: "3.45", Last: "3.42", Volume: "48,210", OI: "19,400", IV: "18.4%", Delta: "+0.512", Gamma: "0.0612", Theta: "-1.420", Vega: "0.224", Vanna: "+0.012", Charm: "-0.031"},
			{ExpiryISO: "2026-11-07", Expiry: "11-07", DTE: 1, Strike: 568, StrikeStr: "568", Type: "P", TypeShort: "PUT", Moneyness: "ATM", Bid: "3.18", Ask: "3.22", Last: "3.20", Volume: "52,900", OI: "24,100", IV: "18.5%", Delta: "-0.488", Gamma: "0.0612", Theta: "-1.390", Vega: "0.224", Vanna: "-0.014", Charm: "+0.029"},
		},
	}
	app.flow = &viewmodel.FlowResponse{
		Ticker: "SPY",
		Spot:   spot,
		Unusual: []viewmodel.UnusualRow{
			{Expiry: "11-07", Strike: "570", Type: "CALL", Volume: "84,120", OI: "18,400", AvgVol: "12,300", ZScore: "4.8σ", VolOI: "4.6×"},
		},
		OIDelta: []viewmodel.OIDeltaRow{
			{Expiry: "11-07", Strike: "560", Type: "PUT", OI: "42,100", OIDelta: 12400, OIDeltaStr: "+12,400", OIPctStr: "+41.8%"},
		},
		DaysCount: 2,
		DaysLabel: "11-05 → 11-06",
		Ladder: []viewmodel.LadderRow{
			{Label: "标的现价 Spot", Before: "562.10", After: "567.82", Delta: "+5.72 (+1.02%)", Tone: "pos"},
			{Label: "净 GEX (USD/1%)", Before: "-2.14B", After: "-1.69B", Delta: "+450.0M", Tone: "pos"},
		},
	}
	return app
}

func TestDesktopAppViewsRenderCleanly(t *testing.T) {
	app := sampleDesktopApp()
	tt := ui.NewTester(app.view, 1520, 1080)
	if !tt.HasText("TideDesk") || !tt.HasText("SPY") {
		t.Fatalf("expected header texts, got %v", tt.Texts())
	}
	if !tt.HasText("Weekly DTE 资金潮汐") || !tt.HasText("波动率曲面与期限结构 · Volatility") {
		t.Fatalf("expected intraday tide and volatility sections in workbench, got %v", tt.Texts())
	}

	outDir := os.Getenv("GEX_SNAPSHOT_DIR")
	saveSnap := func(name string) {
		if outDir == "" {
			return
		}
		_ = os.MkdirAll(outDir, 0755)
		f, err := os.Create(filepath.Join(outDir, name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
	}

	saveSnap("01_workbench")

	for idx, name := range []string{"01_workbench", "02_strike_pressure", "03_chain", "04_flow", "05_logs"} {
		app.viewIdx = idx
		tt.Frame()
		if idx == 1 {
			if !tt.HasText("行权价累计压力 · Strike Cumulative Pressure") {
				t.Fatalf("expected dedicated strike pressure view, got %v", tt.Texts())
			}
			for m := 0; m < 4; m++ {
				app.pressureModeIdx = m
				tt.Frame()
			}
			app.pressureModeIdx = 0
			tt.Frame()
		}
		saveSnap(name)
	}
	// Use the minimum window size and system settings that disable animation.
	app.viewIdx = 0
	tt = ui.NewTester(app.view, 1080, 680)
	tt.SetPreferences(ui.Preferences{Accent: ui.RGB(10, 132, 255), ReduceMotion: true, ReduceTransparency: true})
	saveSnap("06_compact_workbench")
	if err := tt.Click("收起侧栏"); err != nil {
		t.Fatal(err)
	}
	if tt.HasText("自选") || !tt.HasText("工作区") {
		t.Fatal("collapsed sidebar should leave the workspace visible")
	}
	saveSnap("07_collapsed_sidebar")
	if err := tt.Click("展开侧栏"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("自选") {
		t.Fatal("sidebar did not return")
	}
}
