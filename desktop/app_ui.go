package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

const (
	titleBarHeight = 46
	sidebarWidth   = 230
)

var (
	colorBg      = ui.RGB(9, 11, 15)
	colorSurface = ui.RGB(14, 18, 25)
	colorCard    = ui.RGB(19, 25, 35)
	colorHover   = ui.RGB(26, 34, 48)
	colorBorder  = ui.RGBA(255, 255, 255, 0.08)
	colorHair    = ui.RGBA(255, 255, 255, 0.05)

	colorText  = ui.RGB(232, 236, 242)
	colorMuted = ui.RGB(138, 148, 166)
	colorFaint = ui.RGB(88, 98, 116)

	colorPos    = ui.RGB(47, 214, 160)
	colorNeg    = ui.RGB(255, 92, 147)
	colorSpot   = ui.RGB(255, 181, 71)
	colorFlip   = ui.RGB(110, 168, 254)
	colorAccent = colorFlip
	colorVol    = ui.RGB(167, 139, 250)
	colorWarn   = ui.RGB(242, 193, 78)
	colorErr    = ui.RGB(255, 107, 107)
)

func pulseTheme() *ui.Theme {
	t := *ui.DarkTheme()
	t.Background = colorBg
	t.Surface = colorCard
	t.SurfaceHover = colorHover
	t.SurfacePressed = ui.RGB(32, 42, 58)
	t.Border = colorBorder
	t.Text = colorText
	t.TextMuted = colorMuted
	t.Accent = colorFlip
	t.AccentHover = ui.RGB(132, 182, 255)
	t.AccentPressed = ui.RGB(88, 148, 245)
	t.AccentText = ui.RGB(8, 12, 18)
	t.InactiveAccent = ui.RGB(50, 62, 82)
	t.Success = colorPos
	t.Danger = colorNeg
	t.Warning = colorWarn
	t.Radius = 7
	t.Spacing = 3.5
	t.FontSize = 12.5
	t.Scrollbar = ui.RGBA(255, 255, 255, 0.16)
	t.ScrollbarWidth = 6
	t.TooltipDelay = 250 * time.Millisecond
	return &t
}

func svgIcon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.35" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	iconSidebar = svgIcon(`<rect x="1.5" y="2.5" width="13" height="11" rx="2"/><path d="M5.5 2.5v11"/>`)
	iconRefresh = svgIcon(`<path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9"/><path d="M13.5 2.5v2.8h-2.8"/>`)
	iconPlus    = svgIcon(`<path d="M8 3.2v9.6M3.2 8h9.6"/>`)
	iconGrid    = svgIcon(`<rect x="2" y="2" width="5" height="5" rx="1"/><rect x="9" y="2" width="5" height="5" rx="1"/><rect x="2" y="9" width="5" height="5" rx="1"/><rect x="9" y="9" width="5" height="5" rx="1"/>`)
	iconWave    = svgIcon(`<path d="M1.5 10.5c2-5 4-5 6 0s4 5 7-3"/>`)
	iconChain   = svgIcon(`<path d="M2.5 4.5h11M2.5 8h11M2.5 11.5h11"/>`)
	iconFlow    = svgIcon(`<path d="M4.5 12.5V3.5M4.5 3.5L2 6M4.5 3.5L7 6M11.5 3.5v9M11.5 12.5L9 10M11.5 12.5L14 10"/>`)
	iconLogs    = svgIcon(`<rect x="2" y="2.5" width="12" height="11" rx="2"/><path d="M5 6h6M5 9h4"/>`)
)

// exposureColor maps normalized [-1, +1] value to our rose <-> slate <-> mint diverging scale.
func exposureColor(norm float64) ui.Color {
	if math.IsNaN(norm) || norm == 0 {
		return ui.RGB(16, 21, 30)
	}
	t := float32(math.Min(1.0, math.Abs(norm)))
	// Non-linear boost so moderate cells stand out clearly
	t = float32(math.Pow(float64(t), 0.72))
	base := ui.RGB(16, 21, 30)
	if norm > 0 {
		target := ui.RGB(38, 198, 146)
		return base.Mix(target, t*0.86)
	}
	target := ui.RGB(235, 72, 130)
	return base.Mix(target, t*0.86)
}

type DesktopApp struct {
	win *mygo.Window
	api *APIClient

	// Navigation & sidebar
	collapsed       bool
	viewIdx         int // 0=工作台, 1=压力分布, 2=期权链, 3=资金流, 4=系统日志
	selectedSym     string
	newTickerTxt    string
	addingTicker    bool
	collecting      bool
	viewKey         string
	viewSeq         uint64
	viewCancel      context.CancelFunc
	stateSeq        uint64
	stateCancel     context.CancelFunc
	contractSeq     uint64
	contractCancel  context.CancelFunc
	loading         bool
	scheduleTimes   string
	scheduleEditing bool
	chainPage       int
	chainFilterKey  string
	statusMsg       string

	// Workbench & Strike Pressure controls
	dimIdx           int // 0=gex, 1=vanna, 2=charm
	sideIdx          int // 0=net, 1=call, 2=put
	nExpiries        float64
	strikesSide      float64
	pressureRangePct float64
	momentChoice     string
	intradayAxisIdx  int // 0=time bars, 1=strike bars
	pressureModeIdx  int // 0=net+cum, 1=call/put split, 2=call, 3=put

	// Volatility controls
	skewWidth float64

	// Chain controls
	chainSideIdx int // 0=all, 1=call, 2=put
	chainBand    float64
	chainMinOI   float64
	chainExpISO  string
	chainTable   ui.ListState
	chainRowSel  int

	// Other tables
	boardTable    ui.ListState
	pressureTable ui.ListState
	unusualTable  ui.ListState
	oiDeltaTable  ui.ListState
	runsTable     ui.ListState

	// Loaded payloads (owned by main thread via win.Update)
	state    *AppStateResponse
	wb       *WorkbenchResponse
	vol      *VolatilityResponse
	chain    *ChainResponse
	contract *ContractResponse
	flow     *FlowResponse
}

func NewDesktopApp(api *APIClient) *DesktopApp {
	return &DesktopApp{
		api:              api,
		nExpiries:        8,
		strikesSide:      10,
		pressureRangePct: 10,
		skewWidth:        12,
		chainBand:        10,
		chainRowSel:      -1,
	}
}

func (a *DesktopApp) dimKey() string {
	return []string{"gex", "vanna", "charm"}[max(0, min(2, a.dimIdx))]
}

func (a *DesktopApp) sideKey() string {
	return []string{"net", "call", "put"}[max(0, min(2, a.sideIdx))]
}

func (a *DesktopApp) chainSideKey() string {
	return []string{"all", "call", "put"}[max(0, min(2, a.chainSideIdx))]
}

// Entry may come from a completed mutation; all UI state is captured on the
// window thread before starting network work.
func (a *DesktopApp) refreshAll() {
	a.update(func() {
		a.stateSeq++
		seq := a.stateSeq
		if a.stateCancel != nil {
			a.stateCancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		a.stateCancel = cancel
		a.viewKey = ""
		a.viewSeq++
		if a.viewCancel != nil {
			a.viewCancel()
		}
		go func() {
			st, err := a.api.WithContext(ctx).FetchState()
			a.update(func() {
				if seq != a.stateSeq || ctx.Err() != nil {
					return
				}
				if err != nil {
					a.statusMsg = "读取本地数据失败: " + err.Error()
					return
				}
				a.state = st
				if !a.scheduleEditing {
					a.scheduleTimes = strings.Join(st.Schedule.Times, ", ")
				}
				found := false
				for _, sym := range st.Watchlist {
					if sym == a.selectedSym {
						found = true
						break
					}
				}
				if !found {
					a.selectedSym = ""
					a.momentChoice = ""
					if len(st.Watchlist) > 0 {
						a.selectedSym = st.Watchlist[0]
					}
				}
				if a.selectedSym != "" {
					a.loadActiveView(a.selectedSym)
				}
			})
		}()
	})
}

func (a *DesktopApp) loadActiveView(sym string) {
	dim, side, asOf := a.dimKey(), a.sideKey(), a.momentChoice
	nExp, strk := int(math.Round(a.nExpiries)), int(math.Round(a.strikesSide))
	pressurePct := a.pressureRangePct
	view, width, chainSide := a.viewIdx, a.skewWidth, a.chainSideKey()
	band, oi, exp := a.chainBand, int(math.Round(a.chainMinOI)), a.chainExpISO
	filterKey := fmt.Sprintf("%s/%s/%s/%g/%d", sym, exp, chainSide, band, oi)
	if filterKey != a.chainFilterKey {
		a.chainPage = 0
		a.chainFilterKey = filterKey
	}
	page := a.chainPage
	key := fmt.Sprintf("%s/%d/%s/%s/%s/%d/%d/%g/%s/%g/%d/%s/%d/%g", sym, view, dim, side, asOf, nExp, strk, width, chainSide, band, oi, exp, page, pressurePct)
	if key == a.viewKey {
		return
	}
	a.viewKey = key
	a.viewSeq++
	seq := a.viewSeq
	if a.viewCancel != nil {
		a.viewCancel()
	}
	if a.contractCancel != nil {
		a.contractCancel()
	}
	a.contractSeq++
	a.contract = nil
	ctx, cancel := context.WithCancel(context.Background())
	a.viewCancel = cancel
	a.loading = true
	// Clear the old page before changing symbol/filter so it cannot masquerade
	// as the newly requested data while a calculation is still finishing.
	switch view {
	case 0:
		a.wb = nil
		a.vol = nil
	case 1:
		a.wb = nil
	case 2:
		a.chain = nil
		a.chainRowSel = -1
	case 3:
		a.flow = nil
	case 4:
		a.loading = false
		return
	}
	go func() {
		timer := time.NewTimer(160 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		api := a.api.WithContext(ctx)
		var wb *WorkbenchResponse
		var vol *VolatilityResponse
		var chain *ChainResponse
		var flow *FlowResponse
		var err error
		switch view {
		case 0:
			wb, err = api.FetchWorkbench(sym, dim, side, asOf, nExp, strk)
			if err == nil {
				vol, _ = api.FetchVolatility(sym, width)
			}
		case 1:
			wb, err = api.FetchStrikePressure(sym, dim, asOf, nExp, pressurePct)
		case 2:
			chain, err = api.FetchChain(sym, exp, chainSide, band, oi, page)
		case 3:
			flow, err = api.FetchFlow(sym)
		}
		a.update(func() {
			if seq != a.viewSeq || ctx.Err() != nil {
				return
			}
			a.loading = false
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					a.statusMsg = "查询失败: " + err.Error()
				}
				return
			}
			switch view {
			case 0:
				a.wb = wb
				a.vol = vol
			case 1:
				a.wb = wb
			case 2:
				a.chain = chain
			case 3:
				a.flow = flow
			}
		})
	}()
}

func (a *DesktopApp) loadContract(expiryISO string, strike float64, optType string) {
	a.contractSeq++
	seq := a.contractSeq
	sym := a.selectedSym
	if a.contractCancel != nil {
		a.contractCancel()
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.contractCancel = cancel
	go func() {
		ct, err := a.api.WithContext(ctx).FetchContract(sym, expiryISO, strike, optType)
		a.update(func() {
			if seq != a.contractSeq || ctx.Err() != nil {
				return
			}
			if err != nil {
				a.statusMsg = err.Error()
				return
			}
			a.contract = ct
		})
	}()
}

func (a *DesktopApp) saveSchedule(cfg ScheduleConfig) {
	go func() {
		err := a.api.postJSON("/api/schedule", cfg, nil)
		a.update(func() {
			if err != nil {
				a.statusMsg = "设置未保存: " + err.Error()
				return
			}
			a.scheduleEditing = false
			a.statusMsg = "采集设置已保存"
			a.refreshAll()
		})
	}()
}

func (a *DesktopApp) currentTickerSummary() *TickerSummary {
	if a.state == nil || a.selectedSym == "" {
		return nil
	}
	for i := range a.state.Tickers {
		if a.state.Tickers[i].Symbol == a.selectedSym {
			return &a.state.Tickers[i]
		}
	}
	return nil
}

func (a *DesktopApp) saveTickerCollect(sym string, lowEn, highEn bool, highSec int) {
	if sym == "" {
		return
	}
	go func() {
		payload := map[string]any{
			"ticker":            sym,
			"low_freq_enabled":  lowEn,
			"high_freq_enabled": highEn,
			"high_freq_sec":     highSec,
		}
		err := a.api.postJSON("/api/ticker_collect_config", payload, nil)
		a.update(func() {
			if err != nil {
				a.statusMsg = "频率设置未保存: " + err.Error()
				return
			}
			a.statusMsg = fmt.Sprintf("%s 抓取设置已更新 (低频:%v / 高频:%v · %ds)", sym, lowEn, highEn, highSec)
			a.refreshAll()
		})
	}()
}

func (a *DesktopApp) triggerHighFreqTick(sym string) {
	if sym == "" || a.collecting {
		return
	}
	a.collecting = true
	a.statusMsg = "正在高频轻量采样 " + sym + "…"
	go func() {
		var out struct {
			Note string `json:"note"`
		}
		err := a.api.postJSON("/api/collect_intraday", map[string]any{"ticker": sym}, &out)
		a.update(func() {
			a.collecting = false
			if err != nil {
				a.statusMsg = "高频采样失败: " + err.Error()
			} else {
				a.statusMsg = fmt.Sprintf("高频采样完成 (%s: %s)", sym, out.Note)
			}
		})
		a.refreshAll()
	}()
}

func (a *DesktopApp) renderInlineTickerCollectControls(c *ui.Context) {
	ts := a.currentTickerSummary()
	if ts == nil {
		return
	}
	sym := ts.Symbol
	lowEn := ts.LowFreqEnabled
	highEn := ts.HighFreqEnabled
	highSec := ts.HighFreqSec
	if highSec <= 0 {
		highSec = 60
	}

	ui.Row(c).AlignItems(ui.Center).Gap(6).Padding(2, 8).Radius(6).
		Background(colorCard).Border(1, colorBorder).Children(func() {
		lowLabel := "低频定抓: 开"
		lowBg := ui.RGBA(46, 211, 183, 0.16)
		lowFg := colorPos
		if !lowEn {
			lowLabel = "低频定抓: 关"
			lowBg = ui.RGBA(255, 255, 255, 0.05)
			lowFg = colorMuted
		}
		lowBtn := ui.ButtonBase(c.Key("inline_low_"+sym)).Padding(3, 8).Radius(5).
			Background(lowBg).TextColor(lowFg).Label(lowLabel).
			Tooltip("开启或关闭所选股票的低频全链落库抓取")
		lowBtn.Children(func() { ui.Text(c, lowLabel).FontSize(11).Bold() })
		if lowBtn.Clicked() {
			a.saveTickerCollect(sym, !lowEn, highEn, highSec)
		}

		highLabel := "⚡高频流: 关"
		highBg := ui.RGBA(255, 255, 255, 0.05)
		highFg := colorMuted
		if highEn {
			highLabel = "⚡高频流: 开"
			highBg = ui.RGBA(245, 197, 66, 0.18)
			highFg = colorSpot
		}
		highBtn := ui.ButtonBase(c.Key("inline_high_"+sym)).Padding(3, 8).Radius(5).
			Background(highBg).TextColor(highFg).Label(highLabel).
			Tooltip("开启或关闭所选股票的分钟级高频流采样（内存差分+轻量分时落库，零全链膨胀）")
		highBtn.Children(func() { ui.Text(c, highLabel).FontSize(11).Bold() })
		if highBtn.Clicked() {
			a.saveTickerCollect(sym, lowEn, !highEn, highSec)
		}

		freqOpts := []string{"1m", "2m", "3m", "5m", "10m"}
		freqSecs := []int{60, 120, 180, 300, 600}
		chosenFreq := "1m"
		for i, s := range freqSecs {
			if s == highSec {
				chosenFreq = freqOpts[i]
				break
			}
		}
		if ui.Select(c.Key("inline_hf_sec_"+sym), &chosenFreq, freqOpts).Width(68).
			Label("高频周期").Changed() {
			newSec := 60
			for i, o := range freqOpts {
				if o == chosenFreq {
					newSec = freqSecs[i]
					break
				}
			}
			a.saveTickerCollect(sym, lowEn, highEn, newSec)
		}

		sampleBtn := ui.ButtonBase(c.Key("inline_hf_tick_"+sym)).Padding(3, 7).Radius(5).
			Background(ui.RGBA(110, 168, 254, 0.14)).TextColor(colorAccent).
			Label("立即高频采样").Tooltip("立即执行一次分钟级高频差分采样并刷新分时流")
		sampleBtn.Children(func() { ui.Text(c, "采样1次").FontSize(10.5).Bold() })
		if sampleBtn.Clicked() {
			a.triggerHighFreqTick(sym)
		}
	})
}

func (a *DesktopApp) triggerCollect(sym string) {
	if a.collecting {
		return
	}
	a.collecting = true
	a.statusMsg = "正在采集数据…"
	go func() {
		var out struct {
			Results map[string]string `json:"results"`
		}
		err := a.api.postJSON("/api/collect", map[string]any{"ticker": sym}, &out)
		a.update(func() {
			a.collecting = false
			if err != nil {
				a.statusMsg = "采集失败: " + err.Error()
			} else {
				parts := make([]string, 0, len(out.Results))
				for k, v := range out.Results {
					parts = append(parts, fmt.Sprintf("%s: %s", k, v))
				}
				a.statusMsg = "采集完成 (" + strings.Join(parts, ", ") + ")"
			}
		})
		a.refreshAll()
	}()
}

func (a *DesktopApp) triggerAddTicker() {
	sym := strings.TrimSpace(strings.ToUpper(a.newTickerTxt))
	if sym == "" || a.addingTicker {
		return
	}
	a.addingTicker = true
	go func() {
		var out struct {
			Ticker string `json:"ticker"`
			Note   string `json:"note"`
		}
		err := a.api.postJSON("/api/watchlist/add", map[string]any{"ticker": sym}, &out)
		a.update(func() {
			a.addingTicker = false
			if err != nil {
				a.statusMsg = err.Error()
			} else {
				a.newTickerTxt = ""
				a.selectedSym = sym
				a.momentChoice = ""
				if out.Note != "" {
					a.statusMsg = out.Note
				} else {
					a.statusMsg = "已添加 " + sym
				}
			}
		})
		if err == nil {
			a.refreshAll()
		}
	}()
}

func (a *DesktopApp) triggerRemoveTicker(sym string) {
	go func() {
		err := a.api.postJSON("/api/watchlist/remove", map[string]any{"ticker": sym}, nil)
		a.update(func() {
			if err != nil {
				a.statusMsg = err.Error()
				return
			}
			if a.selectedSym == sym {
				a.selectedSym = ""
				a.momentChoice = ""
			}
		})
		a.refreshAll()
	}()
}

func (a *DesktopApp) update(fn func()) {
	if a.win != nil {
		a.win.Update(fn)
	} else {
		fn()
	}
}

func b2f(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// ============================================================================
// Main View
// ============================================================================
func (a *DesktopApp) view(c *ui.Context) {
	c.SetTheme(pulseTheme())
	vibrant := c.Vibrancy()
	if vibrant {
		c.Root().Background(ui.Transparent)
	}

	bar := c.TitleBar()
	rootRow := ui.Row(c).Fill().AlignItems(ui.Stretch)
	shown := rootRow.Animate("sidebar", b2f(!a.collapsed), 220*time.Millisecond)

	rootRow.Children(func() {
		w := sidebarWidth * shown
		ui.Row(c).Width(w).Shrink(0).AlignItems(ui.Stretch).Clip().Children(func() {
			side := ui.Column(c).Width(sidebarWidth).Shrink(0).Margin(0, 0, 0, w-sidebarWidth)
			if !vibrant {
				side.Background(colorSurface)
			}
			side.Children(func() { a.renderSidebar(c, vibrant) })
		})
		if w > 0 {
			ui.Box(c).Width(1).Background(colorBorder)
		}
		a.renderMainPane(c, 16+(1-shown)*(bar.Left+38))
	})

	// Floating sidebar toggle button right next to macOS traffic lights
	toggleTip := "收起侧栏"
	if a.collapsed {
		toggleTip = "展开侧栏"
	}
	toggle := ui.ButtonBase(c.Key("sidebar_toggle")).Absolute().
		Left(bar.Left+8).Top((titleBarHeight-28)/2).Size(30, 28).
		Radius(6).Center().TextColor(colorMuted).Label(toggleTip).Tooltip(toggleTip)
	if toggle.Hovered() {
		toggle.Background(colorHover)
	}
	toggle.Children(func() { ui.Icon(c, iconSidebar).Size(15, 15) })
	if toggle.Clicked() {
		a.collapsed = !a.collapsed
	}
}

func (a *DesktopApp) renderSidebar(c *ui.Context, vibrant bool) {
	// Drag area clearing traffic lights
	ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).
		Padding(0, 12, 0, 112).DragWindow().Children(func() {
		ui.Text(c, "TIDEDESK").FontSize(11).Bold().LetterSpacing(1.2).TextColor(colorMuted).SingleLine()
	})

	ui.Column(c).Grow(1).Padding(6, 10, 10, 10).Gap(10).Children(func() {
		// Add ticker row
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			inp := ui.TextInput(c.Key("add_sym"), &a.newTickerTxt).
				Placeholder("添加代码 如 SPY / NVDA").Grow(1).Label("添加标的")
			if inp.Submitted() {
				a.triggerAddTicker()
			}
			btn := ui.Button(c, "").Label("加入自选").Tooltip("加入自选").
				Disabled(strings.TrimSpace(a.newTickerTxt) == "" || a.addingTicker)
			btn.Children(func() { ui.Icon(c, iconPlus).Size(13, 13) })
			if btn.Clicked() {
				a.triggerAddTicker()
			}
		})

		// Watchlist cards (Pulse process-list style)
		ui.Text(c, "自选监控").FontSize(10.5).Bold().TextColor(colorFaint).Margin(2, 4, 0, 4)
		ui.Scroll(c.Key("watchlist_scroll")).Grow(1).Children(func() {
			ui.Column(c).Gap(4).Children(func() {
				if a.state == nil || len(a.state.Tickers) == 0 {
					ui.Text(c, "暂无自选标的").FontSize(12).TextColor(colorFaint).Padding(8)
					return
				}
				for _, item := range a.state.Tickers {
					sym := item.Symbol
					active := sym == a.selectedSym
					row := ui.ButtonBase(c.Key("wl_"+sym)).
						Padding(7, 9).Radius(7).AlignItems(ui.Center).Gap(6).Label(sym)
					if active {
						row.Background(ui.RGBA(110, 168, 254, 0.16)).Border(1, ui.RGBA(110, 168, 254, 0.38))
					} else if row.Hovered() {
						row.Background(ui.RGBA(255, 255, 255, 0.05))
					}
					row.Children(func() {
						// Regime indicator dot
						dotColor := colorFaint
						if item.NetGEX != nil {
							if *item.NetGEX >= 0 {
								dotColor = colorPos
							} else {
								dotColor = colorNeg
							}
						}
						ui.Box(c).Size(7, 7).Radius(99).Background(dotColor).Shrink(0)
						ui.Column(c).Grow(1).Gap(1).MinWidth(0).Children(func() {
							ui.Row(c).AlignItems(ui.Baseline).Gap(6).Children(func() {
								ui.Text(c, sym).Font("monospace").FontSize(13).Bold().TextColor(colorText).SingleLine()
								if item.Spot != nil {
									ui.Textf(c, "%.2f", *item.Spot).Font("monospace").FontFeatures("tnum").
										FontSize(11.5).TextColor(colorSpot).SingleLine()
								}
								if item.HighFreqEnabled {
									ui.Text(c, "⚡").FontSize(10).TextColor(colorSpot)
								}
								if !item.LowFreqEnabled {
									ui.Text(c, "暂停低频").FontSize(9.5).TextColor(colorFaint)
								}
							})
							sub := item.StateCN
							if item.CollectedAt != "" {
								sub += " · " + item.CollectedAt
							}
							ui.Text(c, sub).FontSize(10.5).TextColor(colorMuted).SingleLine()
						})
						// Net GEX pill
						gexColor := colorMuted
						if item.NetGEX != nil {
							if *item.NetGEX >= 0 {
								gexColor = colorPos
							} else {
								gexColor = colorNeg
							}
						}
						ui.Text(c, item.NetGEXStr).Font("monospace").FontFeatures("tnum").
							FontSize(11.5).Bold().TextColor(gexColor).SingleLine()

						del := ui.ButtonBase(c.Key("del_"+sym)).Size(18, 18).Radius(4).Center().
							TextColor(colorFaint).Label("移除 " + sym).Tooltip("从自选移除")
						if del.Hovered() {
							del.Background(ui.RGBA(255, 107, 107, 0.18)).TextColor(colorErr)
						}
						del.Children(func() { ui.Text(c, "×").FontSize(12) })
						del.OnClick(func() { a.triggerRemoveTicker(sym) })
					})
					row.OnClick(func() {
						if a.selectedSym != sym {
							a.selectedSym = sym
							a.momentChoice = ""
							a.contract = nil
							a.chainRowSel = -1
							a.loadActiveView(sym)
						}
					})
				}
			})
		})

		ui.Divider(c)

		// Per-ticker Low-Freq & High-Freq controls + Global Schedule at bottom of sidebar
		ui.Column(c).Gap(6).Padding(4, 2).Children(func() {
			if ts := a.currentTickerSummary(); ts != nil {
				sym := ts.Symbol
				lowEn := ts.LowFreqEnabled
				highEn := ts.HighFreqEnabled
				highSec := ts.HighFreqSec
				if highSec <= 0 {
					highSec = 60
				}
				ui.Column(c).Gap(5).Padding(6, 8).Radius(7).Background(colorCard).Border(1, colorBorder).Children(func() {
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Textf(c, "当前标的 · %s", sym).Font("monospace").FontSize(11).Bold().TextColor(colorText).Grow(1)
						sampleBtn := ui.ButtonBase(c.Key("side_hf_now_"+sym)).Padding(2, 6).Radius(4).
							Background(ui.RGBA(110, 168, 254, 0.14)).TextColor(colorAccent).
							Label("采样1次").Tooltip("立即采样一次分时增量")
						sampleBtn.Children(func() { ui.Text(c, "⚡采样").FontSize(10).Bold() })
						if sampleBtn.Clicked() {
							a.triggerHighFreqTick(sym)
						}
					})
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						lowTxt := "低频定抓: 开"
						lowCol := colorPos
						if !lowEn {
							lowTxt = "低频定抓: 关"
							lowCol = colorMuted
						}
						bLow := ui.ButtonBase(c.Key("side_low_"+sym)).Grow(1).Padding(4, 6).Radius(5).
							Background(colorSurface).Border(1, colorBorder).Center().TextColor(lowCol).Label(lowTxt)
						bLow.Children(func() { ui.Text(c, lowTxt).FontSize(10.5).Bold() })
						if bLow.Clicked() {
							a.saveTickerCollect(sym, !lowEn, highEn, highSec)
						}

						highTxt := "高频实时: 关"
						highCol := colorMuted
						if highEn {
							highTxt = "高频实时: 开"
							highCol = colorSpot
						}
						bHigh := ui.ButtonBase(c.Key("side_high_"+sym)).Grow(1).Padding(4, 6).Radius(5).
							Background(colorSurface).Border(1, colorBorder).Center().TextColor(highCol).Label(highTxt)
						bHigh.Children(func() { ui.Text(c, highTxt).FontSize(10.5).Bold() })
						if bHigh.Clicked() {
							a.saveTickerCollect(sym, lowEn, !highEn, highSec)
						}
					})
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, "高频周期").FontSize(10.5).TextColor(colorMuted).Grow(1)
						freqOpts := []string{"每 1 分钟", "每 2 分钟", "每 3 分钟", "每 5 分钟", "每 10 分钟"}
						freqSecs := []int{60, 120, 180, 300, 600}
						chosenFreq := freqOpts[0]
						for i, s := range freqSecs {
							if s == highSec {
								chosenFreq = freqOpts[i]
								break
							}
						}
						if ui.Select(c.Key("side_hf_sec_"+sym), &chosenFreq, freqOpts).Width(116).
							Label("高频实时频率").Changed() {
							newSec := 60
							for i, o := range freqOpts {
								if o == chosenFreq {
									newSec = freqSecs[i]
									break
								}
							}
							a.saveTickerCollect(sym, lowEn, highEn, newSec)
						}
					})
				})
			}

			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				stLabel := "空闲"
				if a.state != nil && a.state.CollectorState != "" {
					stLabel = a.state.CollectorState
				}
				if a.collecting {
					ui.Spinner(c).Size(12, 12)
					stLabel = "采集中…"
				}
				ui.Textf(c, "采集器 · %s", stLabel).FontSize(11).TextColor(colorMuted).Grow(1).SingleLine()
				btnAll := ui.Button(c, "全部采集").Disabled(a.collecting)
				if btnAll.Clicked() {
					a.triggerCollect("")
				}
			})

			if a.state != nil {
				cfg := a.state.Schedule
				opts := []string{
					"固定时点（美东）",
					"每 15 分钟（盘中定频）",
					"每 30 分钟（盘中定频）",
					"每 60 分钟（盘中定频）",
					"每 120 分钟（盘中定频）",
					"仅手动采集",
					"临时每 15 分钟 · 2 小时",
					"临时每 30 分钟 · 2 小时",
				}
				chosen := opts[0]
				switch cfg.Mode {
				case "manual":
					chosen = opts[5]
				case "interval":
					switch cfg.Minutes {
					case 15:
						chosen = opts[1]
					case 30:
						chosen = opts[2]
					case 60:
						chosen = opts[3]
					case 120:
						chosen = opts[4]
					}
				case "temporary":
					if cfg.Minutes == 15 {
						chosen = opts[6]
					} else if cfg.Minutes == 30 {
						chosen = opts[7]
					}
				}
				if ui.Select(c.Key("schedule_mode"), &chosen, opts).Label("低频采集策略").Changed() {
					cfg.Until = ""
					switch chosen {
					case opts[0]:
						cfg.Mode = "fixed"
					case opts[1]:
						cfg.Mode = "interval"
						cfg.Minutes = 15
					case opts[2]:
						cfg.Mode = "interval"
						cfg.Minutes = 30
					case opts[3]:
						cfg.Mode = "interval"
						cfg.Minutes = 60
					case opts[4]:
						cfg.Mode = "interval"
						cfg.Minutes = 120
					case opts[5]:
						cfg.Mode = "manual"
					case opts[6]:
						cfg.Mode = "temporary"
						cfg.Minutes = 15
						cfg.Until = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
					case opts[7]:
						cfg.Mode = "temporary"
						cfg.Minutes = 30
						cfg.Until = time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
					}
					a.saveSchedule(cfg)
				}
				if cfg.Mode == "fixed" {
					ui.Text(c, "采集时点 · America/New_York").FontSize(10.5).TextColor(colorMuted)
					if ui.TextInput(c.Key("schedule_times"), &a.scheduleTimes).Placeholder("10:00, 12:00, 14:00, 16:15").Label("美东采集时点").Changed() {
						a.scheduleEditing = true
					}
					if a.scheduleEditing && ui.Button(c, "保存时点").Clicked() {
						cfg.Times = nil
						for _, part := range strings.Split(strings.ReplaceAll(a.scheduleTimes, "，", ","), ",") {
							cfg.Times = append(cfg.Times, strings.TrimSpace(part))
						}
						a.saveSchedule(cfg)
					}
				}
				ui.Text(c, a.state.NextCollection).FontSize(10).TextColor(colorFaint).MaxLines(2)
			}

			if a.statusMsg != "" {
				ui.Text(c, a.statusMsg).FontSize(10.5).TextColor(colorMuted).MaxLines(2)
			}
		})
	})
}

func (a *DesktopApp) renderMainPane(c *ui.Context, titleLeft float32) {
	bar := c.TitleBar()
	pane := ui.Column(c).Grow(1).MinWidth(0).Background(colorBg)
	pane.Children(func() {
		// Unified Pulse-style header bar (draggable)
		ui.Row(c).Height(titleBarHeight).Shrink(0).AlignItems(ui.Center).
			Padding(0, bar.Right+14, 0, titleLeft).Gap(12).
			BorderWidth(0, 0, 1, 0).BorderColor(colorBorder).DragWindow().Children(func() {
			// Active symbol & quick status
			sym := a.selectedSym
			if sym == "" {
				sym = "TideDesk"
			}
			ui.Text(c, sym).Font("monospace").FontSize(16).Bold().TextColor(colorText).SingleLine()
			if a.state != nil {
				for _, summary := range a.state.Tickers {
					if summary.Symbol == a.selectedSym && summary.HasData {
						ui.Badge(c, strings.ToUpper(summary.Source)).Background(colorCard).TextColor(colorMuted)
						ui.Text(c, summary.CollectedAt).Font("monospace").FontSize(11).TextColor(colorMuted).SingleLine()
					}
				}
			}

			ui.Spacer(c)

			// Workspace segmented switcher in the title bar
			prevView := a.viewIdx
			if ui.Segmented(c.Key("main_nav"), &a.viewIdx, "▦ 工作台", "▤ 压力分布", "☰ 期权链", "⇅ 资金流", "▤ 日志").
				Label("工作区").Changed() && a.viewIdx != prevView && a.selectedSym != "" {
				a.loadActiveView(a.selectedSym)
			}

			ui.Spacer(c)

			// Right action cluster
			if ui.Button(c, "刷新").Tooltip("手动刷新本地数据，不访问行情源").Clicked() {
				a.refreshAll()
			}
			if a.selectedSym != "" {
				btn := ui.Button(c, "").Label("立即采集当前标的").Tooltip("立即采集 " + a.selectedSym).Disabled(a.collecting)
				btn.Children(func() {
					ui.Icon(c, iconRefresh).Size(13, 13)
					ui.Text(c, "采集").FontSize(12)
				})
				if btn.Clicked() {
					a.triggerCollect(a.selectedSym)
				}
			}
		})

		// Body
		if a.selectedSym == "" {
			ui.Column(c).Grow(1).Center().Gap(8).Children(func() {
				ui.Text(c, "从左侧边栏添加或选择标的开始看盘").FontSize(14).Bold()
				ui.Text(c, "支持美股期权全链 Cboe 优先抓取与本地 SQLite 快照回放").FontSize(12).TextColor(colorMuted)
			})
			return
		}

		// Every tab shares a cached summary, not a complete matrix request.
		var ribbon *RibbonData
		if a.state != nil {
			for _, summary := range a.state.Tickers {
				if summary.Symbol == a.selectedSym {
					ribbon = summary.Ribbon
					break
				}
			}
		}
		if (a.viewIdx == 0 || a.viewIdx == 1) && a.wb != nil && !a.wb.Empty && a.wb.Ticker == a.selectedSym {
			ribbon = &a.wb.Ribbon
		}
		if ribbon != nil {
			a.renderRibbon(c, ribbon)
		}

		switch a.viewIdx {
		case 0:
			a.renderWorkbenchView(c)
		case 1:
			a.renderStrikePressureView(c)
		case 2:
			a.renderChainView(c)
		case 3:
			a.renderFlowView(c)
		case 4:
			a.renderLogsView(c)
		}
	})
}

func (a *DesktopApp) renderRibbon(c *ui.Context, r *RibbonData) {
	ui.Row(c).Padding(8, 14, 4, 14).Gap(6).AlignItems(ui.Stretch).Children(func() {
		// Regime card
		accent := colorNeg
		badge := "−γ 放大"
		if r.Positive {
			accent = colorPos
			badge = "+γ 压制"
		}
		ui.Column(c).Width(234).Shrink(0).Padding(7, 11).Radius(8).
			Background(colorSurface).Border(1, accent.Alpha(0.42)).Gap(2).
			Tooltip(r.StateSub).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, r.StateCN).FontSize(13).Bold().TextColor(colorText).SingleLine()
				ui.Spacer(c)
				ui.Badge(c, badge).Background(accent.Mix(colorBg, 0.8)).TextColor(accent)
			})
			ui.Text(c, r.Scope).Font("monospace").FontSize(10).TextColor(colorMuted).SingleLine()
		})

		tile := func(title, val, sub string, valColor ui.Color) {
			ui.Column(c).Grow(1).Basis(0).MinWidth(82).Padding(7, 10).Radius(8).
				Background(colorSurface).Border(1, colorBorder).Gap(2).Children(func() {
				ui.Text(c, title).FontSize(10).Bold().TextColor(colorFaint).SingleLine()
				ui.Text(c, val).Font("monospace").FontFeatures("tnum").FontSize(14.5).Bold().TextColor(valColor).SingleLine()
				ui.Text(c, sub).Font("monospace").FontFeatures("tnum").FontSize(10).TextColor(colorMuted).SingleLine()
			})
		}

		gexCol := colorNeg
		if r.Positive {
			gexCol = colorPos
		}
		tile("现价 SPOT", r.SpotStr, a.selectedSym, colorSpot)
		tile("净 GEX", r.NetGEXStr, "USD / 1%", gexCol)
		tile("GAMMA 翻转", r.GammaFlipStr, r.FlipDistStr, colorFlip)
		tile("CALL 墙", r.CallGEXWall, "OI "+r.CallOIWall, colorPos)
		tile("PUT 墙", r.PutGEXWall, "OI "+r.PutOIWall, colorNeg)
		tile("MAX PAIN", r.MaxPain, r.MaxPainExp, colorVol)
		tile("±1σ 预期", r.MoveStr, r.MovePctStr, colorSpot)
		tile("ATM IV / P·C", r.ATMIVStr, "P/C "+r.PCROIStr, colorText)
	})
}
