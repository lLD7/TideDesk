package main

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gammagrid.local/desktop/internal/marketclock"
	"gammagrid.local/desktop/internal/store"
	"gammagrid.local/desktop/internal/viewmodel"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/lobehub/lobe-mygo/pkg/action"
	"github.com/lobehub/lobe-mygo/pkg/display"
	"github.com/lobehub/lobe-mygo/pkg/lucide"
	"github.com/lobehub/lobe-mygo/pkg/theme"
)

const (
	titleBarHeight = 48
	sidebarWidth   = 240
)

var (
	colorBg      = ui.RGB(8, 10, 14)
	colorSurface = ui.RGB(13, 17, 24)
	colorCard    = ui.RGB(18, 23, 33)
	colorChrome  ui.Color
	colorHover   = ui.RGB(24, 31, 44)
	colorBorder  = ui.RGBA(255, 255, 255, 0.075)
	colorHair    = ui.RGBA(255, 255, 255, 0.04)

	colorText  = ui.RGB(241, 245, 251)
	colorMuted = ui.RGB(142, 153, 171)
	colorFaint = ui.RGB(74, 85, 104)

	colorPos    = ui.RGB(46, 211, 183) // mint-emerald Call/Positive
	colorNeg    = ui.RGB(255, 92, 147) // vivid rose-crimson Put/Negative
	colorSpot   = ui.RGB(245, 166, 35) // luminous amber Spot
	colorFlip   = ui.RGB(99, 102, 241) // modern indigo Gamma Flip
	colorAccent = colorFlip
	colorVol    = ui.RGB(167, 139, 250) // soft purple IV
	colorWarn   = ui.RGB(245, 158, 11)
	colorErr    = ui.RGB(244, 63, 94)
)

func init() {
	tok := theme.Dark()
	colorBg = tok.BgLayout
	colorSurface = tok.BgContainer
	colorCard = tok.BgElevated
	colorHover = tok.FillSecondary
	colorBorder = tok.Border
	colorHair = tok.BorderSecondary
	colorText = tok.Text
	colorChrome = colorSurface.Mix(colorText, 0.045)
	colorMuted = tok.TextSecondary
	colorFaint = tok.TextTertiary
	colorPos = tok.Success
	colorNeg = tok.Error
	colorWarn = tok.Warning
}

var sharedPulseTheme = buildPulseTheme()

func buildPulseTheme() *ui.Theme {
	t := *ui.DarkTheme()
	tok := theme.Dark()
	t.Background = tok.BgLayout
	t.Surface = tok.BgContainer
	t.SurfaceHover = tok.FillSecondary
	t.SurfacePressed = tok.BgContainerSecondary
	t.Border = tok.Border
	t.Text = tok.Text
	t.TextMuted = tok.TextSecondary
	t.Accent = tok.Primary
	t.AccentHover = tok.PrimaryHover
	t.AccentPressed = tok.PrimaryActive
	t.AccentText = tok.BgLayout
	t.InactiveAccent = tok.Border
	t.Success = tok.Success
	t.Danger = tok.Error
	t.Warning = tok.Warning
	t.Radius = 8
	t.Spacing = 3.5
	t.FontSize = 12.5
	t.Scrollbar = ui.RGBA(255, 255, 255, 0.16)
	t.ScrollbarWidth = 6
	t.TooltipDelay = 250 * time.Millisecond
	return &t
}

func (a *DesktopApp) windowTheme(prefs ui.Preferences) *ui.Theme {
	accent := prefs.Accent
	if accent.A == 0 {
		accent = sharedPulseTheme.Accent
	}
	if a.windowPalette == nil || a.windowPalette.Accent != accent {
		t := *sharedPulseTheme
		t.Accent = accent
		t.AccentHover = accent.Mix(colorText, 0.12)
		t.AccentPressed = accent.Mix(colorBg, 0.16)
		t.InactiveAccent = colorMuted
		a.windowPalette = &t
	}
	return a.windowPalette
}

func svgIcon(shapes string) *ui.SVG {
	return ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.35" stroke-linecap="round" stroke-linejoin="round">` + shapes + `</svg>`))
}

var (
	iconSidebar = svgIcon(`<rect x="1.5" y="2.5" width="13" height="11" rx="2"/><path d="M5.5 2.5v11"/>`)
	iconRefresh = svgIcon(`<path d="M13.5 8a5.5 5.5 0 1 1-1.6-3.9"/><path d="M13.5 2.5v2.8h-2.8"/>`)
	iconRemove  = lucide.X()
)

var workspaceLabels = [...]string{"工作台", "压力分布", "期权链", "资金流", "日志"}

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
	win     *mygo.Window
	service *DesktopService

	// Navigation & sidebar
	collapsed       bool
	windowPalette   *ui.Theme
	viewIdx         int // 0=工作台, 1=压力分布, 2=期权链, 3=资金流, 4=系统日志
	selectedSym     string
	newTickerTxt    string
	addingTicker    bool
	collecting      bool
	viewKey         string
	volKey          string
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
	heatmapRows   ui.ListState
	boardTable    ui.ListState
	pressureTable ui.ListState
	unusualTable  ui.ListState
	oiDeltaTable  ui.ListState
	runsTable     ui.ListState

	pressurePlot pressurePlot
	intradayPlot intradayPlot

	// Loaded payloads (owned by main thread via win.Update)
	state    *viewmodel.AppStateResponse
	wb       *viewmodel.WorkbenchResponse
	vol      *viewmodel.VolatilityResponse
	chain    *viewmodel.ChainResponse
	contract *viewmodel.ContractResponse
	flow     *viewmodel.FlowResponse
}

func NewDesktopApp(service *DesktopService) *DesktopApp {
	return &DesktopApp{
		service:          service,
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
		ctx, cancel := context.WithCancel(a.service.ctx)
		a.stateCancel = cancel
		a.viewKey = ""
		a.volKey = ""
		a.viewSeq++
		if a.viewCancel != nil {
			a.viewCancel()
		}
		go func() {
			st, err := a.service.state(ctx)
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
	key := fmt.Sprintf("%s/%d", sym, view)
	switch view {
	case 0:
		key += fmt.Sprintf("/%s/%s/%s/%d/%d/%g", dim, side, asOf, nExp, strk, width)
	case 1:
		key += fmt.Sprintf("/%s/%s/%d/%g", dim, asOf, nExp, pressurePct)
	case 2:
		key += fmt.Sprintf("/%s/%d", filterKey, page)
	}
	volKey := fmt.Sprintf("%s/%g", sym, width)
	needVol := a.vol == nil || a.volKey != volKey
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
	ctx, cancel := context.WithCancel(a.service.ctx)
	a.viewCancel = cancel
	a.loading = true
	// Clear the old page before changing symbol/filter so it cannot masquerade
	// as the newly requested data while a calculation is still finishing.
	switch view {
	case 0:
		a.wb = nil
		if a.vol != nil && a.vol.Ticker != sym {
			a.vol = nil
		}
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
		var wb *viewmodel.WorkbenchResponse
		var chain *viewmodel.ChainResponse
		var flow *viewmodel.FlowResponse
		var err error
		switch view {
		case 0:
			wb, err = a.service.Workbench(ctx, sym, workbenchQuery{Dimension: dim, Side: side, AsOf: asOf, Expiries: nExp, Strikes: strk})
		case 1:
			wb, err = a.service.Workbench(ctx, sym, workbenchQuery{Dimension: dim, AsOf: asOf, Expiries: nExp, RangePct: pressurePct})
		case 2:
			chain, err = a.service.Chain(ctx, sym, chainQuery{Expiries: exp, Side: chainSide, Band: band, MinOI: oi, Page: page})
		case 3:
			flow, err = a.service.Flow(ctx, sym)
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
			case 1:
				a.wb = wb
			case 2:
				a.chain = chain
			case 3:
				a.flow = flow
			}
		})
		if view == 0 && needVol && err == nil && ctx.Err() == nil {
			vol, volErr := a.service.Volatility(ctx, sym, width)
			if volErr == nil {
				a.update(func() {
					if seq == a.viewSeq && ctx.Err() == nil {
						a.vol = vol
						a.volKey = volKey
					}
				})
			}
		}

	}()
}

func (a *DesktopApp) loadContract(expiryISO string, strike float64, optType string) {
	a.contractSeq++
	seq := a.contractSeq
	sym := a.selectedSym
	if a.contractCancel != nil {
		a.contractCancel()
	}
	ctx, cancel := context.WithCancel(a.service.ctx)
	a.contractCancel = cancel
	go func() {
		ct, err := a.service.Contract(ctx, sym, contractQuery{Expiry: expiryISO, Strike: strike, Side: optType})
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

func (a *DesktopApp) saveSchedule(cfg marketclock.ScheduleConfig) {
	go func() {
		err := a.service.saveSchedule(a.service.ctx, cfg)
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

func (a *DesktopApp) currentTickerSummary() *viewmodel.TickerSummary {
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

func (a *DesktopApp) saveTickerCollect(sym string, lowEn, highEn bool, highSec, highMinutes int) {
	if sym == "" {
		return
	}
	go func() {
		cfg := store.TickerCollectConfig{Ticker: sym, LowFreqEnabled: lowEn, HighFreqEnabled: highEn, HighFreqSec: highSec, HighFreqMinutes: highMinutes}
		err := a.service.saveTickerConfig(a.service.ctx, cfg)
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
		err := a.service.collectHighFreqTick(a.service.ctx, sym)
		a.update(func() {
			a.collecting = false
			if err != nil {
				a.statusMsg = "高频采样失败: " + err.Error()
			} else {
				a.statusMsg = "高频采样完成 · " + sym
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
	highEn := ts.HighFreqEnabled && activeHighFreq(store.TickerCollectConfig{HighFreqEnabled: true, HighFreqUntil: ts.HighFreqUntil}, a.service.now())
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
			a.saveTickerCollect(sym, !lowEn, highEn, highSec, 0)
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
			Tooltip("按侧栏设置运行高频采样，到期或收盘后自动停止")
		highBtn.Children(func() { ui.Text(c, highLabel).FontSize(11).Bold() })
		if highBtn.Clicked() {
			a.saveTickerCollect(sym, lowEn, !highEn, highSec, 0)
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
			a.saveTickerCollect(sym, lowEn, highEn, newSec, 0)
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
		results, err := a.service.Collect(a.service.ctx, sym)
		a.update(func() {
			a.collecting = false
			if err != nil {
				a.statusMsg = "采集失败: " + err.Error()
			} else {
				parts := make([]string, 0, len(results))
				for k, v := range results {
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
		ticker, err := a.service.setWatchlist(a.service.ctx, sym, true)
		a.update(func() {
			a.addingTicker = false
			if err != nil {
				a.statusMsg = err.Error()
			} else {
				a.newTickerTxt = ""
				a.selectedSym = ticker
				a.momentChoice = ""
				a.statusMsg = "已添加 " + ticker
			}
		})
		if err == nil {
			a.refreshAll()
		}
	}()
}

func (a *DesktopApp) triggerRemoveTicker(sym string) {
	go func() {
		_, err := a.service.setWatchlist(a.service.ctx, sym, false)
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
func (a *DesktopApp) selectWorkspace(idx int) {
	if idx == a.viewIdx {
		return
	}
	a.viewIdx = idx
	if idx != 0 {
		a.intradayPlot = intradayPlot{}
	}
	if idx != 1 {
		a.pressurePlot = pressurePlot{}
	}
	if a.selectedSym != "" {
		a.loadActiveView(a.selectedSym)
	}
}

func (a *DesktopApp) view(c *ui.Context) {
	prefs := c.Preferences()
	c.SetTheme(a.windowTheme(prefs))
	material := c.Vibrancy() && !prefs.ReduceTransparency
	background := colorBg
	sidebar := colorChrome
	if material {
		background = ui.Transparent
		sidebar = colorChrome.Alpha(0.62)
	}
	c.Root().Background(background)

	bar := c.TitleBar()
	height := max(titleBarHeight, bar.Height)
	toggleLeft := max(10, bar.Left+6)
	rootRow := ui.Row(c).Fill().AlignItems(ui.Stretch)
	shown := rootRow.Animate("sidebar", b2f(!a.collapsed), 160*time.Millisecond)
	rootRow.Children(func() {
		w := sidebarWidth * shown
		if w > 0 {
			ui.Row(c).Width(w).Shrink(0).AlignItems(ui.Stretch).Clip().Children(func() {
				ui.Column(c).Width(sidebarWidth).Shrink(0).Margin(0, 0, 0, w-sidebarWidth).
					Background(sidebar).Children(func() { a.renderSidebar(c, height, toggleLeft+36) })
			})
			ui.Box(c).Width(1).Background(colorBorder)
		}
		left := float32(16)
		if w < toggleLeft+36 {
			left = max(left, toggleLeft+46-w)
		}
		width, _ := c.Size()
		a.renderMainPane(c, height, left, width-w)
	})

	toggleTip := "收起侧栏"
	if a.collapsed {
		toggleTip = "展开侧栏"
	}
	toggle := ui.ButtonBase(c.Key("sidebar_toggle")).Absolute().
		Left(toggleLeft).Top((height-28)/2).Size(30, 28).
		Radius(7).Center().TextColor(colorMuted).Label(toggleTip).Tooltip(toggleTip + " · ⌥⌘S")
	if toggle.Hovered() {
		toggle.Background(colorHover).TextColor(colorText)
	}
	toggle.Children(func() { ui.Icon(c, iconSidebar).Size(16, 16) })
	if toggle.Clicked() {
		a.collapsed = !a.collapsed
	}
}

func (a *DesktopApp) renderSidebar(c *ui.Context, titleHeight, titleLeft float32) {
	ui.Row(c).Height(titleHeight).Shrink(0).AlignItems(ui.Center).
		Padding(0, 12, 0, titleLeft).DragWindow().Children(func() {
		ui.Text(c, "TideDesk").FontSize(13).Bold().TextColor(colorText).SingleLine()
	})

	ui.Column(c).Grow(1).Padding(4, 10, 10, 10).Gap(10).Children(func() {
		// Add ticker row
		ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
			inp := ui.TextInput(c.Key("add_sym"), &a.newTickerTxt).
				Placeholder("添加标的 SPY / QQQ").Grow(1).Label("添加标的")
			if inp.Submitted() {
				a.triggerAddTicker()
			}
			btn := action.Button(c, "添加", action.ButtonProps{
				Variant: action.Primary,
				Size:    theme.Small,
			}).Disabled(strings.TrimSpace(a.newTickerTxt) == "" || a.addingTicker)
			if btn.Clicked() {
				a.triggerAddTicker()
			}
		})

		// Watchlist cards (Pulse process-list style)
		ui.Row(c).AlignItems(ui.Center).Padding(2, 4, 0, 4).Children(func() {
			ui.Text(c, "自选").FontSize(11).Bold().TextColor(colorMuted)
			ui.Spacer(c)
			if a.state != nil && len(a.state.Tickers) > 0 {
				ui.Textf(c, "%d 个标的", len(a.state.Tickers)).Font("monospace").FontSize(10).TextColor(colorFaint)
			}
		})

		ui.Scroll(c.Key("watchlist_scroll")).Grow(1).Children(func() {
			ui.Column(c).Gap(5).Children(func() {
				if a.state == nil || len(a.state.Tickers) == 0 {
					ui.Column(c).Center().Padding(16).Gap(4).Children(func() {
						ui.Text(c, "暂无自选标的").FontSize(12).TextColor(colorMuted)
						ui.Text(c, "输入代码回车即可添加").FontSize(10.5).TextColor(colorFaint)
					})
					return
				}
				for _, item := range a.state.Tickers {
					sym := item.Symbol
					active := sym == a.selectedSym
					row := ui.ButtonBase(c.Key("wl_"+sym)).
						Padding(8, 10).Radius(8).AlignItems(ui.Center).Gap(7).Label(sym)
					if active {
						accent := c.Theme().Accent
						if !c.WindowActive() {
							accent = colorMuted
						}
						row.Background(accent.Alpha(0.18)).Border(1, accent.Alpha(0.32))
					} else if row.Hovered() {
						row.Background(colorHover)
					}
					row.Children(func() {
						// Regime indicator dot / bar
						dotColor := colorFaint
						tagColor := display.TagGray
						if item.NetGEX != nil {
							if *item.NetGEX >= 0 {
								dotColor = colorPos
								tagColor = display.TagGreen
							} else {
								dotColor = colorNeg
								tagColor = display.TagRed
							}
						}
						ui.Box(c).Size(4, 26).Radius(2).Background(dotColor).Shrink(0)

						ui.Column(c).Grow(1).Gap(2).MinWidth(0).Children(func() {
							ui.Row(c).AlignItems(ui.Baseline).Gap(6).Children(func() {
								ui.Text(c, sym).Font("monospace").FontSize(13.5).Bold().TextColor(colorText).SingleLine()
								if item.Spot != nil {
									ui.Textf(c, "$%.2f", *item.Spot).Font("monospace").FontFeatures("tnum").
										FontSize(11.5).Bold().TextColor(colorSpot).SingleLine()
								}
								if item.HighFreqEnabled {
									ui.Text(c, "⚡").FontSize(10).TextColor(colorSpot).Tooltip("高频轻量分时抓取中")
								}
							})
							sub := item.StateCN
							if item.CollectedAt != "" {
								sub += " · " + item.CollectedAt
							}
							ui.Text(c, sub).FontSize(10).TextColor(colorMuted).SingleLine()
						})

						// Net GEX pill tag
						if item.NetGEXStr != "" && item.NetGEXStr != "—" {
							display.Tag(c, item.NetGEXStr, display.TagProps{
								Variant: display.TagFilled,
								Color:   tagColor,
								Size:    theme.Small,
							})
						}

						del := ui.ButtonBase(c.Key("del_"+sym)).Size(20, 20).Radius(4).Center().
							TextColor(colorFaint).Label("移除 " + sym).Tooltip("从自选移除")
						if del.Hovered() {
							del.Background(colorNeg.Alpha(0.18)).TextColor(colorNeg)
						}
						del.Children(func() { ui.Icon(c, iconRemove).Size(11, 11) })
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
				highEn := ts.HighFreqEnabled && activeHighFreq(store.TickerCollectConfig{HighFreqEnabled: true, HighFreqUntil: ts.HighFreqUntil}, a.service.now())
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
						lowBg := colorPos.Alpha(0.12)
						lowBorder := colorPos.Alpha(0.35)
						if !lowEn {
							lowTxt = "低频定抓: 关"
							lowCol = colorMuted
							lowBg = ui.RGBA(255, 255, 255, 0.03)
							lowBorder = colorHair
						}
						bLow := ui.ButtonBase(c.Key("side_low_"+sym)).Grow(1).Padding(4, 6).Radius(5).
							Background(lowBg).Border(1, lowBorder).Center().TextColor(lowCol).Label(lowTxt)
						bLow.Children(func() { ui.Text(c, lowTxt).FontSize(10.5).Bold() })
						if bLow.Clicked() {
							a.saveTickerCollect(sym, !lowEn, highEn, highSec, 0)
						}

						highTxt := "高频实时: 关"
						highCol := colorMuted
						highBg := ui.RGBA(255, 255, 255, 0.03)
						highBorder := colorHair
						if highEn {
							highTxt = "高频实时: 开"
							highCol = colorSpot
							highBg = colorSpot.Alpha(0.12)
							highBorder = colorSpot.Alpha(0.35)
						}
						bHigh := ui.ButtonBase(c.Key("side_high_"+sym)).Grow(1).Padding(4, 6).Radius(5).
							Background(highBg).Border(1, highBorder).Center().TextColor(highCol).Label(highTxt)
						bHigh.Children(func() { ui.Text(c, highTxt).FontSize(10.5).Bold() })
						if bHigh.Clicked() {
							a.saveTickerCollect(sym, lowEn, !highEn, highSec, 0)
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
							a.saveTickerCollect(sym, lowEn, highEn, newSec, 0)
						}
					})
					ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
						ui.Text(c, "高频时长").FontSize(10.5).TextColor(colorMuted).Grow(1)
						durations := []string{"30 分钟", "1 小时", "2 小时", "4 小时"}
						minutes := []int{30, 60, 120, 240}
						chosen := durations[2]
						for i, duration := range minutes {
							if duration == ts.HighFreqMinutes {
								chosen = durations[i]
							}
						}
						if ui.Select(c.Key("side_hf_duration_"+sym), &chosen, durations).Width(116).
							Label("高频运行时长，最晚收盘停止").Changed() {
							for i, label := range durations {
								if label == chosen {
									a.saveTickerCollect(sym, lowEn, highEn, highSec, minutes[i])
									break
								}
							}
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
				btnAll := action.Button(c, "全部采集", action.ButtonProps{
					Variant: action.Filled,
					Size:    theme.Small,
				}).Disabled(a.collecting)
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

func (a *DesktopApp) renderMainPane(c *ui.Context, titleHeight, titleLeft, width float32) {
	bar := c.TitleBar()
	summary := a.currentTickerSummary()
	pane := ui.Column(c).Grow(1).MinWidth(0).Background(colorBg)
	pane.Children(func() {
		ui.Row(c).Height(titleHeight).Shrink(0).AlignItems(ui.Center).
			Padding(0, bar.Right+14, 0, titleLeft).Gap(10).
			Background(colorChrome).DragWindow().Children(func() {
			sym := a.selectedSym
			if sym == "" {
				sym = "TideDesk"
			}
			ui.Text(c, sym).FontSize(17).Bold().TextColor(colorText).SingleLine()
			if summary != nil && summary.HasData && summary.Spot != nil {
				ui.Textf(c, "$%.2f", *summary.Spot).Font("monospace").FontFeatures("tnum").
					FontSize(14).TextColor(colorSpot).SingleLine()
			}
			ui.Spacer(c)
			if a.collecting {
				ui.Spinner(c).Size(13, 13)
			}
			refresh := ui.ButtonBase(c.Key("toolbar_refresh")).Size(30, 28).Radius(7).
				Center().TextColor(colorMuted).Label("刷新").Tooltip("刷新本地数据 · ⌘R")
			if refresh.Hovered() {
				refresh.Background(colorHover).TextColor(colorText)
			}
			refresh.Children(func() { ui.Icon(c, iconRefresh).Size(15, 15) })
			if refresh.Clicked() {
				a.refreshAll()
			}
			if a.selectedSym != "" {
				btn := action.Button(c, "采集", action.ButtonProps{Variant: action.Primary, Size: theme.Small}).
					Tooltip("立即采集 " + a.selectedSym + " 期权全链").Disabled(a.collecting)
				if btn.Clicked() {
					a.triggerCollect(a.selectedSym)
				}
			}
		})
		ui.Row(c).Height(39).Shrink(0).AlignItems(ui.Center).Padding(0, 14).Gap(8).
			Background(colorChrome).BorderWidth(0, 0, 1, 0).BorderColor(colorBorder).Children(func() {
			selected := a.viewIdx
			if ui.Segmented(c.Key("main_nav"), &selected, workspaceLabels[:]...).Label("工作区").Changed() {
				a.selectWorkspace(selected)
			}
			ui.Spacer(c)
			if summary != nil && summary.HasData {
				ui.Text(c, strings.ToUpper(summary.Source)).FontSize(10).Bold().TextColor(colorMuted).SingleLine()
				if summary.CollectedAt != "" {
					ui.Text(c, summary.CollectedAt).Font("monospace").FontSize(10.5).TextColor(colorMuted).SingleLine()
				}
			}
		})

		// Body
		if a.selectedSym == "" {
			ui.Column(c).Grow(1).Center().Gap(10).Children(func() {
				ui.Text(c, "从左侧边栏添加或选择标的开始看盘").FontSize(14).Bold().TextColor(colorText)
				ui.Text(c, "支持美股期权全链 Cboe 优先抓取与本地 SQLite 快照回放").FontSize(12).TextColor(colorMuted)
			})
			return
		}

		// Every tab shares a cached summary, not a complete matrix request.
		var ribbon *viewmodel.RibbonData
		if summary != nil {
			ribbon = summary.Ribbon
		}
		if (a.viewIdx == 0 || a.viewIdx == 1) && a.wb != nil && !a.wb.Empty && a.wb.Ticker == a.selectedSym {
			ribbon = &a.wb.Ribbon
		}
		if ribbon != nil {
			a.renderRibbon(c, ribbon, width)
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

func (a *DesktopApp) renderRibbon(c *ui.Context, r *viewmodel.RibbonData, width float32) {
	hero := func() {
		accent := colorNeg
		badge := "−γ 放大"
		tagColor := display.TagRed
		if r.Positive {
			accent = colorPos
			badge = "+γ 压制"
			tagColor = display.TagGreen
		}
		ui.Column(c).Width(230).Shrink(0).Padding(7, 12).Radius(8).
			Background(colorSurface).Border(1, accent.Alpha(0.48)).Gap(2).
			Tooltip(r.StateSub).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Text(c, r.StateCN).FontSize(13.5).Bold().TextColor(colorText).SingleLine()
				ui.Spacer(c)
				display.Tag(c, badge, display.TagProps{
					Variant: display.TagFilled,
					Color:   tagColor,
					Size:    theme.Small,
				})
			})
			ui.Text(c, r.Scope).Font("monospace").FontSize(10).TextColor(colorMuted).SingleLine()
		})
	}

	gexColor := colorNeg
	if r.Positive {
		gexColor = colorPos
	}
	tiles := [...]struct {
		title, value, detail string
		color                ui.Color
	}{
		{"现价 SPOT", r.SpotStr, a.selectedSym, colorSpot},
		{"净 GEX", r.NetGEXStr, "USD / 1%", gexColor},
		{"GAMMA 翻转", r.GammaFlipStr, r.FlipDistStr, colorFlip},
		{"CALL 墙", r.CallGEXWall, "OI " + r.CallOIWall, colorPos},
		{"PUT 墙", r.PutGEXWall, "OI " + r.PutOIWall, colorNeg},
		{"MAX PAIN", r.MaxPain, r.MaxPainExp, colorVol},
		{"±1σ 预期", r.MoveStr, r.MovePctStr, colorSpot},
		{"ATM IV / P·C", r.ATMIVStr, "P/C " + r.PCROIStr, colorText},
	}
	drawTiles := func(start, end int) {
		for _, tile := range tiles[start:end] {
			ui.Column(c).Grow(1).Basis(0).MinWidth(80).Padding(6, 9).Radius(8).
				Background(colorSurface).Border(1, colorBorder).Gap(1).Children(func() {
				ui.Text(c, tile.title).FontSize(9.5).Bold().LetterSpacing(0.6).TextColor(colorMuted).SingleLine()
				ui.Text(c, tile.value).Font("monospace").FontFeatures("tnum").FontSize(15).Bold().TextColor(tile.color).SingleLine()
				ui.Text(c, tile.detail).Font("monospace").FontFeatures("tnum").FontSize(9.5).TextColor(colorMuted).SingleLine()
			})
		}
	}
	ui.Row(c).Padding(6, 14, 2, 14).Gap(6).AlignItems(ui.Stretch).Children(func() {
		hero()
		// Leave room for all eight tiles at the minimum window width.
		if width >= 980 {
			drawTiles(0, len(tiles))
			return
		}
		ui.Column(c).Grow(1).MinWidth(0).Gap(6).Children(func() {
			ui.Row(c).Gap(6).Children(func() { drawTiles(0, 4) })
			ui.Row(c).Gap(6).Children(func() { drawTiles(4, len(tiles)) })
		})
	})
}
