package main

import (
	"fmt"
	"math"
	"strings"

	"gammagrid.local/desktop/internal/viewmodel"

	"github.com/egoist/mygo/ui"
)

// ============================================================================
// Workbench Section 2: Intraday Volume/Premium Bars (Chart 1) & Weekly DTE Tide (Chart 2)
// ============================================================================

func (a *DesktopApp) activeIntradayBars(intr *viewmodel.IntradaySummary) ([]viewmodel.IntradayBar, bool) {
	if intr == nil {
		return nil, false
	}
	if a.intradayAxisIdx == 1 {
		if len(intr.StrikeBars) > 0 {
			return intr.StrikeBars, true
		}
		return intr.TimeBars, false
	}
	if len(intr.TimeBars) > 0 {
		return intr.TimeBars, false
	}
	return intr.StrikeBars, true
}

func (a *DesktopApp) renderIntradayTideSection(c *ui.Context, wb *viewmodel.WorkbenchResponse) {
	intr := &wb.Intraday
	bars, isStrikeMode := a.activeIntradayBars(intr)

	ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
		a.renderIntradayVolumeChartCard(c, wb.Ticker, intr, bars, isStrikeMode)
		a.renderWeeklyDTETideCard(c, wb.Ticker, intr, bars, isStrikeMode)
	})
}

func (a *DesktopApp) renderIntradayVolumeChartCard(c *ui.Context, ticker string, intr *viewmodel.IntradaySummary, bars []viewmodel.IntradayBar, isStrikeMode bool) {
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Radius(8).
		Background(colorSurface).Border(1, colorBorder).Clip().Children(func() {
		// Top Summary Header (matching Screenshot 1: Date | Vol | Prem | Net Prem)
		ui.Row(c).Padding(8, 12).Gap(10).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colorHair).Wrap().Children(func() {
			dateStr := intr.DateLabel
			if dateStr == "" {
				dateStr = "今日"
			}
			ui.Text(c, dateStr+":").Font("monospace").FontFeatures("tnum").FontSize(12).Bold().TextColor(colorMuted)

			ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
				ui.Text(c, "Vol:").FontSize(11.5).TextColor(colorMuted)
				ui.Text(c, intr.TotalVolStr).Font("monospace").FontFeatures("tnum").FontSize(12.5).Bold().TextColor(colorText)
			})
			ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
				ui.Text(c, "Prem:").FontSize(11.5).TextColor(colorMuted)
				ui.Text(c, intr.TotalPremStr).Font("monospace").FontFeatures("tnum").FontSize(12.5).Bold().TextColor(colorText)
			})
			ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
				ui.Text(c, "Net Prem:").FontSize(11.5).TextColor(colorMuted)
				netCol := colorPos
				if intr.NetPrem < 0 {
					netCol = colorNeg
				}
				ui.Text(c, intr.NetPremStr).Font("monospace").FontFeatures("tnum").FontSize(12.5).Bold().TextColor(netCol)
			})

			ui.Spacer(c)

			// Axis mode switcher: Intraday Time vs Strike distribution
			ui.Segmented(c.Key("intr_axis_mode"), &a.intradayAxisIdx,
				fmt.Sprintf("分时 (%d)", len(intr.TimeBars)),
				fmt.Sprintf("按行权价 (%d)", len(intr.StrikeBars)),
			).Label("横轴模式")

			btnTick := ui.Button(c, "⚡采样").Tooltip("立即执行一次轻量级分钟级采样（仅写入差量分时柱，不膨胀快照表）").Disabled(a.collecting)
			if btnTick.Clicked() {
				a.triggerHighFreqTick(ticker)
			}
		})

		// Sub-header Legend Strip: Put Vol/Prem | Call Vol/Prem | Spot
		ui.Row(c).Padding(5, 12).Gap(14).AlignItems(ui.Center).
			Background(ui.RGB(11, 14, 20)).BorderWidth(0, 0, 1, 0).BorderColor(colorHair).Wrap().Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(3, 11).Radius(1).Background(colorNeg)
				ui.Textf(c, "Put: %s Vol | %s Prem", intr.PutVolStr, intr.PutPremStr).
					Font("monospace").FontFeatures("tnum").FontSize(11).Bold().TextColor(colorText)
			})
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(3, 11).Radius(1).Background(colorPos)
				ui.Textf(c, "Call: %s Vol | %s Prem", intr.CallVolStr, intr.CallPremStr).
					Font("monospace").FontFeatures("tnum").FontSize(11).Bold().TextColor(colorText)
			})
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(99).Background(colorSpot)
				ui.Text(c, ticker).Font("monospace").FontSize(11).Bold().TextColor(colorSpot)
			})
			ui.Spacer(c)
			modeNote := "日内分时差量柱"
			if isStrikeMode {
				modeNote = "行权价截面成交分布"
			} else if len(bars) == 1 {
				modeNote = "单快照基线（开启高频实时抓取自动绘分时柱）"
			}
			ui.Text(c, modeNote).FontSize(10).TextColor(colorFaint)
		})

		// Main GPU Chart Canvas
		a.renderIntradayVolumeCanvas(c, ticker, bars, isStrikeMode)
	})
}

func (a *DesktopApp) renderIntradayVolumeCanvas(c *ui.Context, ticker string, bars []viewmodel.IntradayBar, isStrikeMode bool) {
	if len(bars) == 0 {
		ui.Column(c).Height(250).Center().Children(func() {
			ui.Text(c, "暂无日内期权成交数据").FontSize(12).TextColor(colorFaint)
		})
		return
	}

	maxVol := 1.0
	minSpot, maxSpot := math.Inf(1), math.Inf(-1)
	for _, b := range bars {
		if b.TotalVol > maxVol {
			maxVol = b.TotalVol
		}
		if b.Spot > 0 {
			if b.Spot < minSpot {
				minSpot = b.Spot
			}
			if b.Spot > maxSpot {
				maxSpot = b.Spot
			}
		}
	}
	maxVol *= 1.12
	if math.IsInf(minSpot, 1) || math.IsInf(maxSpot, -1) {
		minSpot, maxSpot = 99, 101
	} else if math.Abs(maxSpot-minSpot) < 1e-4 {
		pad := math.Max(1.0, minSpot*0.005)
		minSpot -= pad
		maxSpot += pad
	} else {
		pad := (maxSpot - minSpot) * 0.14
		minSpot -= pad
		maxSpot += pad
	}

	box := ui.Box(c).FillWidth().Height(262).Background(ui.RGB(10, 13, 19))
	px, _, hasPointer := box.PointerPosition()

	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W < 120 || r.H < 90 {
			return
		}
		padL, padR, padT, padB := float32(48), float32(58), float32(14), float32(26)
		plotX, plotY := r.X+padL, r.Y+padT
		plotW, plotH := r.W-padL-padR, r.H-padT-padB
		if plotW <= 10 || plotH <= 10 {
			return
		}

		// Horizontal grid lines + Left (Volume) & Right (Spot Price) Y-axis labels
		const yTicks = 5
		for i := 0; i <= yTicks; i++ {
			frac := float32(i) / float32(yTicks)
			yy := plotY + plotH*(1-frac)
			p.Line(plotX, yy, plotX+plotW, yy, 1, colorHair)

			volVal := maxVol * float64(frac)
			vLbl := fmtVolCompact(volVal)
			if i == 0 {
				vLbl = "0"
			}
			spL := ui.Span{Text: vLbl, Font: "monospace", Size: 9.5, Color: colorMuted}
			twL, thL := p.MeasureText(0, spL)
			p.RichText(plotX-twL-5, yy-thL*0.5, 0, spL)

			spotVal := minSpot + (maxSpot-minSpot)*float64(frac)
			sLbl := fmt.Sprintf("$%.2f", spotVal)
			spR := ui.Span{Text: sLbl, Font: "monospace", Size: 9.5, Color: colorMuted}
			_, thR := p.MeasureText(0, spR)
			p.RichText(plotX+plotW+5, yy-thR*0.5, 0, spR)
		}

		n := len(bars)
		slotW := plotW / float32(max(1, n))
		barW := max(float32(3), min(float32(28), slotW*0.72))

		// Determine hovered / active bar index
		activeIdx := n - 1
		if hasPointer && px >= padL && px <= padL+plotW {
			idx := int((px - padL) / slotW)
			if idx >= 0 && idx < n {
				activeIdx = idx
			}
		}

		// Highlight band for active candle (matching Screenshot 1 vertical translucent grey bar)
		activeCenterX := plotX + (float32(activeIdx)+0.5)*slotW
		bandW := max(barW+6, min(float32(18), slotW))
		p.Fill(ui.Rect{
			X: activeCenterX - bandW*0.5,
			Y: plotY,
			W: bandW,
			H: plotH,
		}, ui.RGBA(255, 255, 255, 0.08), 0)

		// Draw Stacked Bars: bottom = CallVol (green), top = PutVol (red)
		spotPts := make([][2]float32, 0, n)
		labelStep := max(1, n/6)
		for i, b := range bars {
			cx := plotX + (float32(i)+0.5)*slotW
			bx := cx - barW*0.5

			callH := float32(b.CallVol/maxVol) * plotH
			putH := float32(b.PutVol/maxVol) * plotH
			if callH > 0 {
				p.Fill(ui.Rect{
					X: bx,
					Y: plotY + plotH - callH,
					W: barW,
					H: callH,
				}, colorPos.Alpha(0.92), 0)
			}
			if putH > 0 {
				p.Fill(ui.Rect{
					X: bx,
					Y: plotY + plotH - callH - putH,
					W: barW,
					H: putH,
				}, colorNeg.Alpha(0.92), 0)
			}

			if b.Spot > 0 {
				sy := plotY + plotH*(1-float32((b.Spot-minSpot)/(maxSpot-minSpot)))
				spotPts = append(spotPts, [2]float32{cx, sy})
			}

			if i%labelStep == 0 || i == n-1 {
				spX := ui.Span{Text: b.Time, Font: "monospace", Size: 9.5, Color: colorMuted}
				twX, _ := p.MeasureText(0, spX)
				p.RichText(cx-twX*0.5, plotY+plotH+6, 0, spX)
			}
		}

		// Draw Spot Price Line (Yellow)
		if len(spotPts) >= 2 && !isStrikeMode {
			p.StrokePolyline(spotPts, 2.1, colorSpot)
		} else if len(spotPts) == 1 {
			sy := spotPts[0][1]
			p.Line(plotX, sy, plotX+plotW, sy, 1.3, colorSpot.Alpha(0.55))
		}

		// Draw Glowing Spot Dot on Active Bar
		if activeIdx >= 0 && activeIdx < len(spotPts) {
			pt := spotPts[activeIdx]
			p.Fill(ui.Rect{X: pt[0] - 6.5, Y: pt[1] - 6.5, W: 13, H: 13}, colorSpot.Alpha(0.28), 6.5)
			p.Fill(ui.Rect{X: pt[0] - 4, Y: pt[1] - 4, W: 8, H: 8}, colorSpot, 4)
		}

		// Draw Floating Tooltip Card (matching Screenshot 1 dark box with red/rose border)
		if activeIdx >= 0 && activeIdx < n {
			b := bars[activeIdx]
			cardW, cardH := float32(228), float32(114)
			cardX := activeCenterX - cardW - 12
			if cardX < plotX+8 {
				cardX = min(plotX+plotW-cardW-8, activeCenterX+12)
			}
			if cardX < plotX+4 {
				cardX = plotX + 4
			}
			cardY := plotY + 10

			cardRect := ui.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}
			p.Fill(cardRect, ui.RGBA(8, 10, 15, 0.94), 6)
			p.Stroke(cardRect, colorNeg.Alpha(0.78), 6, 1.1)

			tx := cardX + 10
			ty := cardY + 7
			p.RichText(tx, ty, cardW-16, ui.Span{Text: b.WindowLabel, Font: "monospace", Size: 10, Weight: 600, Color: colorText})

			ty += 17
			p.Fill(ui.Rect{X: tx, Y: ty + 3, W: 6, H: 6}, colorNeg, 3)
			putTxt := fmt.Sprintf("Put: %s   ", formatIntComma(int64(math.Round(b.PutVol))))
			callTxt := fmt.Sprintf("● Call: %s", formatIntComma(int64(math.Round(b.CallVol))))
			p.RichText(tx+9, ty, cardW-20,
				ui.Span{Text: putTxt, Font: "monospace", Size: 10, Color: colorText},
				ui.Span{Text: callTxt, Font: "monospace", Size: 10, Color: colorPos},
			)

			ty += 16
			p.Fill(ui.Rect{X: tx, Y: ty + 3, W: 6, H: 6}, colorSpot, 3)
			p.RichText(tx+9, ty, cardW-20, ui.Span{Text: fmt.Sprintf("%s: %.2f", ticker, b.Spot), Font: "monospace", Size: 10, Color: colorSpot})

			ty += 16
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("P/C Ratio: %.3f", b.PCRatio), Font: "monospace", Size: 10, Color: colorMuted})

			ty += 16
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("Candle volume: %s", formatIntComma(int64(math.Round(b.TotalVol)))), Font: "monospace", Size: 10, Color: colorText})

			ty += 16
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("Candle premium: %s", fmtDollarComma(b.TotalPrem, false)), Font: "monospace", Size: 10, Weight: 600, Color: colorPos})
		}
	})
}

func (a *DesktopApp) renderWeeklyDTETideCard(c *ui.Context, ticker string, intr *viewmodel.IntradaySummary, bars []viewmodel.IntradayBar, isStrikeMode bool) {
	ui.Column(c).Grow(1).Basis(0).MinWidth(0).Radius(8).
		Background(colorSurface).Border(1, colorBorder).Clip().Children(func() {
		// Top Summary Header for Weekly DTE Tide
		ui.Row(c).Padding(8, 12).Gap(10).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colorHair).Wrap().Children(func() {
			ui.Text(c, "Weekly DTE 资金潮汐").FontSize(12.5).Bold().TextColor(colorText)
			if intr.WeeklyScope != "" {
				ui.Badge(c, intr.WeeklyScope).Background(colorCard).TextColor(colorFlip)
			}

			ui.Spacer(c)

			ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
				ui.Text(c, "净潮汐:").FontSize(11).TextColor(colorMuted)
				tideCol := colorPos
				if intr.WeeklyNetPrem < 0 {
					tideCol = colorNeg
				}
				ui.Text(c, intr.WeeklyNetPremStr).Font("monospace").FontFeatures("tnum").FontSize(12.5).Bold().TextColor(tideCol)
			})
			ui.Row(c).Gap(4).AlignItems(ui.Baseline).Children(func() {
				ui.Text(c, "Weekly GEX:").FontSize(11).TextColor(colorMuted)
				gCol := colorPos
				if intr.WeeklyNetGEX < 0 {
					gCol = colorNeg
				}
				ui.Text(c, intr.WeeklyNetGEXStr).Font("monospace").FontFeatures("tnum").FontSize(12).Bold().TextColor(gCol)
			})
		})

		// Sub-header Legend Strip
		ui.Row(c).Padding(5, 12).Gap(14).AlignItems(ui.Center).
			Background(ui.RGB(11, 14, 20)).BorderWidth(0, 0, 1, 0).BorderColor(colorHair).Wrap().Children(func() {
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 2.5).Radius(1).Background(colorPos)
				ui.Textf(c, "Call 权利金: %s", intr.WeeklyCallPremStr).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorPos)
			})
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 2.5).Radius(1).Background(colorNeg)
				ui.Textf(c, "Put 权利金: %s", intr.WeeklyPutPremStr).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorNeg)
			})
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(8, 2.5).Radius(1).Background(colorFlip)
				ui.Text(c, "Net Tide (Call−Put)").Font("monospace").FontSize(11).Bold().TextColor(colorFlip)
			})
			ui.Row(c).Gap(5).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(99).Background(colorSpot)
				ui.Text(c, ticker).Font("monospace").FontSize(11).Bold().TextColor(colorSpot)
			})
		})

		a.renderWeeklyDTETideCanvas(c, ticker, bars, isStrikeMode)
	})
}

func (a *DesktopApp) renderWeeklyDTETideCanvas(c *ui.Context, ticker string, bars []viewmodel.IntradayBar, isStrikeMode bool) {
	if len(bars) == 0 {
		ui.Column(c).Height(250).Center().Children(func() {
			ui.Text(c, "暂无 Weekly (0–7 DTE) 潮汐数据").FontSize(12).TextColor(colorFaint)
		})
		return
	}

	minVal, maxVal := 0.0, 0.0
	minSpot, maxSpot := math.Inf(1), math.Inf(-1)
	for _, b := range bars {
		cVal := b.CumWeeklyCall
		pVal := -b.CumWeeklyPut
		nVal := b.CumWeeklyNet
		for _, v := range []float64{cVal, pVal, nVal, b.WeeklyNetPrem} {
			if v < minVal {
				minVal = v
			}
			if v > maxVal {
				maxVal = v
			}
		}
		if b.Spot > 0 {
			if b.Spot < minSpot {
				minSpot = b.Spot
			}
			if b.Spot > maxSpot {
				maxSpot = b.Spot
			}
		}
	}
	span := maxVal - minVal
	if span < 1.0 {
		maxVal += 1000
		minVal -= 1000
	} else {
		maxVal += span * 0.12
		minVal -= span * 0.12
	}
	if math.IsInf(minSpot, 1) || math.IsInf(maxSpot, -1) {
		minSpot, maxSpot = 99, 101
	} else if math.Abs(maxSpot-minSpot) < 1e-4 {
		pad := math.Max(1.0, minSpot*0.005)
		minSpot -= pad
		maxSpot += pad
	} else {
		pad := (maxSpot - minSpot) * 0.14
		minSpot -= pad
		maxSpot += pad
	}

	box := ui.Box(c).FillWidth().Height(262).Background(ui.RGB(10, 13, 19))
	px, _, hasPointer := box.PointerPosition()

	box.Draw(func(p *ui.Painter, r ui.Rect) {
		if r.W < 120 || r.H < 90 {
			return
		}
		padL, padR, padT, padB := float32(56), float32(58), float32(14), float32(26)
		plotX, plotY := r.X+padL, r.Y+padT
		plotW, plotH := r.W-padL-padR, r.H-padT-padB
		if plotW <= 10 || plotH <= 10 {
			return
		}

		valToY := func(v float64) float32 {
			t := (v - minVal) / (maxVal - minVal)
			return plotY + plotH*(1-float32(t))
		}
		zeroY := valToY(0)

		const yTicks = 5
		for i := 0; i <= yTicks; i++ {
			frac := float32(i) / float32(yTicks)
			yy := plotY + plotH*(1-frac)
			p.Line(plotX, yy, plotX+plotW, yy, 1, colorHair)

			v := minVal + (maxVal-minVal)*float64(frac)
			spL := ui.Span{Text: fmtDollarCompact(v, true), Font: "monospace", Size: 9.5, Color: colorMuted}
			twL, thL := p.MeasureText(0, spL)
			p.RichText(plotX-twL-5, yy-thL*0.5, 0, spL)

			spotVal := minSpot + (maxSpot-minSpot)*float64(frac)
			spR := ui.Span{Text: fmt.Sprintf("$%.2f", spotVal), Font: "monospace", Size: 9.5, Color: colorMuted}
			_, thR := p.MeasureText(0, spR)
			p.RichText(plotX+plotW+5, yy-thR*0.5, 0, spR)
		}

		// Zero line
		if zeroY >= plotY && zeroY <= plotY+plotH {
			p.Line(plotX, zeroY, plotX+plotW, zeroY, 1.2, ui.RGBA(255, 255, 255, 0.16))
		}

		n := len(bars)
		slotW := plotW / float32(max(1, n))
		barW := max(float32(3), min(float32(20), slotW*0.55))

		activeIdx := n - 1
		if hasPointer && px >= padL && px <= padL+plotW {
			idx := int((px - padL) / slotW)
			if idx >= 0 && idx < n {
				activeIdx = idx
			}
		}

		activeCenterX := plotX + (float32(activeIdx)+0.5)*slotW
		p.Fill(ui.Rect{
			X: activeCenterX - max(barW, 12)*0.5,
			Y: plotY,
			W: max(barW, 12),
			H: plotH,
		}, ui.RGBA(255, 255, 255, 0.07), 0)

		callPts := make([][2]float32, 0, n)
		putPts := make([][2]float32, 0, n)
		netPts := make([][2]float32, 0, n)
		spotPts := make([][2]float32, 0, n)
		labelStep := max(1, n/6)

		for i, b := range bars {
			cx := plotX + (float32(i)+0.5)*slotW
			ny := valToY(b.WeeklyNetPrem)
			topY := min(zeroY, ny)
			bh := max(float32(1.5), float32(math.Abs(float64(ny-zeroY))))
			bCol := colorPos.Alpha(0.28)
			if b.WeeklyNetPrem < 0 {
				bCol = colorNeg.Alpha(0.28)
			}
			p.Fill(ui.Rect{X: cx - barW*0.5, Y: topY, W: barW, H: bh}, bCol, 0)

			callPts = append(callPts, [2]float32{cx, valToY(b.CumWeeklyCall)})
			putPts = append(putPts, [2]float32{cx, valToY(-b.CumWeeklyPut)})
			netPts = append(netPts, [2]float32{cx, valToY(b.CumWeeklyNet)})
			if b.Spot > 0 {
				sy := plotY + plotH*(1-float32((b.Spot-minSpot)/(maxSpot-minSpot)))
				spotPts = append(spotPts, [2]float32{cx, sy})
			}

			if i%labelStep == 0 || i == n-1 {
				spX := ui.Span{Text: b.Time, Font: "monospace", Size: 9.5, Color: colorMuted}
				twX, _ := p.MeasureText(0, spX)
				p.RichText(cx-twX*0.5, plotY+plotH+6, 0, spX)
			}
		}

		if len(callPts) >= 2 {
			p.StrokePolyline(callPts, 1.7, colorPos.Alpha(0.88))
			p.StrokePolyline(putPts, 1.7, colorNeg.Alpha(0.88))
			p.StrokePolyline(netPts, 2.4, colorFlip)
		} else if len(netPts) == 1 {
			p.Fill(ui.Rect{X: callPts[0][0] - 4, Y: callPts[0][1] - 4, W: 8, H: 8}, colorPos, 4)
			p.Fill(ui.Rect{X: putPts[0][0] - 4, Y: putPts[0][1] - 4, W: 8, H: 8}, colorNeg, 4)
			p.Fill(ui.Rect{X: netPts[0][0] - 5, Y: netPts[0][1] - 5, W: 10, H: 10}, colorFlip, 5)
		}
		if len(spotPts) >= 2 && !isStrikeMode {
			p.StrokePolyline(spotPts, 1.8, colorSpot.Alpha(0.9))
		}

		if activeIdx >= 0 && activeIdx < n {
			np := netPts[activeIdx]
			cp := callPts[activeIdx]
			pp := putPts[activeIdx]
			p.Fill(ui.Rect{X: np[0] - 4.5, Y: np[1] - 4.5, W: 9, H: 9}, colorFlip, 4.5)
			p.Fill(ui.Rect{X: cp[0] - 3.5, Y: cp[1] - 3.5, W: 7, H: 7}, colorPos, 3.5)
			p.Fill(ui.Rect{X: pp[0] - 3.5, Y: pp[1] - 3.5, W: 7, H: 7}, colorNeg, 3.5)

			b := bars[activeIdx]
			cardW, cardH := float32(234), float32(100)
			cardX := activeCenterX - cardW - 12
			if cardX < plotX+8 {
				cardX = min(plotX+plotW-cardW-8, activeCenterX+12)
			}
			if cardX < plotX+4 {
				cardX = plotX + 4
			}
			cardY := plotY + 10

			cardRect := ui.Rect{X: cardX, Y: cardY, W: cardW, H: cardH}
			p.Fill(cardRect, ui.RGBA(8, 10, 15, 0.94), 6)
			p.Stroke(cardRect, colorFlip.Alpha(0.75), 6, 1.1)

			tx, ty := cardX+10, cardY+7
			p.RichText(tx, ty, cardW-16, ui.Span{Text: b.WindowLabel, Font: "monospace", Size: 10, Weight: 600, Color: colorText})
			ty += 17
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("Weekly Call 累计: %s", fmtDollarCompact(b.CumWeeklyCall, true)), Font: "monospace", Size: 10, Color: colorPos})
			ty += 16
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("Weekly Put 累计: -%s", fmtDollarCompact(b.CumWeeklyPut, false)), Font: "monospace", Size: 10, Color: colorNeg})
			ty += 16
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("Weekly 净潮汐: %s", fmtDollarCompact(b.CumWeeklyNet, true)), Font: "monospace", Size: 10, Weight: 600, Color: colorFlip})
			ty += 16
			p.RichText(tx, ty, cardW-16, ui.Span{Text: fmt.Sprintf("当柱净流: %s · GEX: %s", fmtDollarCompact(b.WeeklyNetPrem, true), fmtDollarCompact(b.WeeklyNetGEX, true)), Font: "monospace", Size: 9.5, Color: colorMuted})
		}
	})
}

// ============================================================================
// Workbench Section 3: Integrated Volatility Surface & Term Structure
// ============================================================================

func (a *DesktopApp) renderWorkbenchVolatilitySection(c *ui.Context, vol *viewmodel.VolatilityResponse) {
	if vol == nil || vol.Empty {
		return
	}
	// Compact section header with skew width slider & RV badges
	ui.Row(c).Padding(8, 12).Radius(8).Background(colorSurface).Border(1, colorBorder).
		Gap(10).AlignItems(ui.Center).Wrap().Children(func() {
		ui.Text(c, "波动率曲面与期限结构 · Volatility").FontSize(12.5).Bold().TextColor(colorText)
		ui.Textf(c, "微笑曲线行权价范围 ±%.1f%%", a.skewWidth).Font("monospace").FontSize(11).TextColor(colorMuted)
		if ui.Stepper(c.Key("wb_skew_w"), &a.skewWidth, 5, 30, 2.5).Label("行权价范围").Changed() {
			a.loadActiveView(a.selectedSym)
		}
		ui.Spacer(c)
		for _, rv := range vol.RV {
			ui.Badge(c, fmt.Sprintf("%s: %s", rv.Window, rv.Value)).Background(colorCard).TextColor(colorVol)
		}
	})

	ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
		sectionCard(c, "波动率微笑曲线 (Multi-Expiry IV Skew)", "OTM IV 均值", func() {
			a.drawSkewChart(c, vol)
		}).Grow(1).Basis(0)

		sectionCard(c, "波动率期限结构 (ATM IV & ±1σ 预期波动)", fmt.Sprintf("%d 个到期日", len(vol.Term)), func() {
			a.drawTermChart(c, vol)
		}).Grow(1).Basis(0)
	})

	sectionCard(c, "成交量加权 IV 历史走势 (Volume-Weighted IV History)", fmt.Sprintf("%d 个采样点", len(vol.IVHistory)), func() {
		a.drawIVHistoryChart(c, vol)
	})
}

// ============================================================================
// Tab 1: Dedicated Strike Cumulative Pressure View (单独把 行权价累计压力 作为一个视图)
// ============================================================================

func (a *DesktopApp) renderStrikePressureView(c *ui.Context) {
	wb := a.wb
	if wb == nil {
		ui.Column(c).Grow(1).Padding(14).Children(func() {
			emptyCard(c, "正在计算行权价累计压力分布…", "正在从本地 SQLite 数据库读取最新快照")
		})
		return
	}
	if wb.Empty {
		ui.Column(c).Grow(1).Padding(14).Children(func() {
			emptyCard(c, wb.Ticker+" 尚无期权链快照", "点击右上角「采集」或左侧「全部采集」抓取 Cboe 期权全链数据")
		})
		return
	}

	ui.Column(c).Grow(1).Padding(6, 14, 14, 14).Gap(10).MinHeight(0).Children(func() {
		// Control toolbar for Strike Cumulative Pressure
		ui.Row(c).Padding(7, 12).Radius(8).Background(colorSurface).Border(1, colorBorder).
			Gap(12).AlignItems(ui.Center).Wrap().Children(func() {
			ui.Text(c, "行权价累计压力 · Strike Cumulative Pressure").FontSize(13).Bold().TextColor(colorText)

			prevDim := a.dimIdx
			if ui.Segmented(c.Key("sp_dim_seg"), &a.dimIdx, "GEX 伽马", "Vanna", "Charm").Label("敞口维度").Changed() && a.dimIdx != prevDim {
				a.loadActiveView(a.selectedSym)
			}

			ui.Segmented(c.Key("sp_mode_seg"), &a.pressureModeIdx,
				"净压力合力 + 累计曲线",
				"Call / Put 双侧镜像",
				"仅 Call 压力",
				"仅 Put 压力",
			).Label("展示模式")

			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Textf(c, "汇总 %d 到期", int(math.Round(a.nExpiries))).Font("monospace").FontSize(11).TextColor(colorMuted)
				if ui.Stepper(c.Key("sp_n_exp"), &a.nExpiries, 1, 24, 1).Label("汇总到期日").Changed() {
					a.loadActiveView(a.selectedSym)
				}
			})

			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Textf(c, "现价 ±%.0f%%", a.pressureRangePct).Font("monospace").FontSize(11).TextColor(colorMuted)
				if ui.Stepper(c.Key("sp_range_pct"), &a.pressureRangePct, 1, 50, 1).Label("行权价范围（相对现价百分比）").Changed() {
					a.loadActiveView(a.selectedSym)
				}
			})
			if wb.StrikeLower != nil && wb.StrikeUpper != nil {
				ui.Textf(c, "$%s – $%s", viewmodel.FmtPriceVal(*wb.StrikeLower, 2), viewmodel.FmtPriceVal(*wb.StrikeUpper, 2)).
					Font("monospace").FontSize(11).TextColor(colorMuted)
			}

			ui.Spacer(c)

			ui.Badge(c, fmt.Sprintf("区间合力 %s", wb.GrandTotalStr)).Background(colorCard).TextColor(colorText)
			if wb.Ribbon.CallGEXWall != "" && wb.Ribbon.CallGEXWall != "—" {
				ui.Badge(c, "◆C 墙 "+wb.Ribbon.CallGEXWall).Background(colorPos.Alpha(0.16)).TextColor(colorPos)
			}
			if wb.Ribbon.PutGEXWall != "" && wb.Ribbon.PutGEXWall != "—" {
				ui.Badge(c, "◆P 墙 "+wb.Ribbon.PutGEXWall).Background(colorNeg.Alpha(0.16)).TextColor(colorNeg)
			}
		})

		// Main 2-column body: Left = Large GPU Horizontal Pressure Chart, Right = Strike Breakdown Table
		ui.Row(c).Grow(1).Gap(10).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
			a.renderStrikePressureChartCard(c, wb)
			a.renderStrikePressureTableCard(c, wb)
		})
	})
}

func (a *DesktopApp) renderStrikePressureChartCard(c *ui.Context, wb *viewmodel.WorkbenchResponse) {
	ui.Column(c).Grow(1.5).Basis(0).MinWidth(0).MinHeight(0).Radius(8).
		Background(colorSurface).Border(1, colorBorder).Clip().Children(func() {
		ui.Row(c).Padding(8, 12).Gap(10).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colorHair).Wrap().Children(func() {
			ui.Textf(c, "%s 行权价压力横截面 (%s)", wb.DimensionCN, wb.UnitCN).FontSize(12.5).Bold().TextColor(colorText)
			ui.Spacer(c)
			ui.Row(c).Gap(12).AlignItems(ui.Center).Children(func() {
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(8, 8).Radius(2).Background(colorPos)
					ui.Text(c, "+γ 阻力 / Call").FontSize(10.5).TextColor(colorMuted)
				})
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(8, 8).Radius(2).Background(colorNeg)
					ui.Text(c, "−γ 加速 / Put").FontSize(10.5).TextColor(colorMuted)
				})
				if a.pressureModeIdx == 0 {
					ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
						ui.Box(c).Size(10, 2.5).Radius(1).Background(colorFlip)
						ui.Text(c, "累计净压力积分线").FontSize(10.5).TextColor(colorFlip)
					})
				}
				ui.Row(c).Gap(4).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(8, 2.5).Radius(1).Background(colorSpot)
					ui.Textf(c, "快照现价 %s", viewmodel.FmtPriceVal(wb.Spot, 2)).Font("monospace").FontSize(10.5).TextColor(colorSpot)
				})
			})
		})

		n := len(wb.Strikes)
		if n == 0 {
			ui.Column(c).Grow(1).Center().Children(func() {
				ui.Text(c, "所选百分比区间内无可用行权价").FontSize(12).TextColor(colorFaint)
			})
			return
		}

		maxAbs := 1.0
		for i := 0; i < n; i++ {
			vNet := math.Abs(wb.Profile[i])
			var vC, vP float64
			if i < len(wb.CallProfile) {
				vC = math.Abs(wb.CallProfile[i])
			}
			if i < len(wb.PutProfile) {
				vP = math.Abs(wb.PutProfile[i])
			}
			for _, v := range []float64{vNet, vC, vP} {
				if v > maxAbs {
					maxAbs = v
				}
			}
		}

		cumNet := make([]float64, n)
		maxCumAbs := 1.0
		if n > 1 && wb.Strikes[0] > wb.Strikes[n-1] {
			acc := 0.0
			for i := n - 1; i >= 0; i-- {
				acc += wb.Profile[i]
				cumNet[i] = acc
				if math.Abs(acc) > maxCumAbs {
					maxCumAbs = math.Abs(acc)
				}
			}
		} else {
			acc := 0.0
			for i := 0; i < n; i++ {
				acc += wb.Profile[i]
				cumNet[i] = acc
				if math.Abs(acc) > maxCumAbs {
					maxCumAbs = math.Abs(acc)
				}
			}
		}

		rowH := float32(24)
		canvasH := max(float32(420), float32(n)*rowH+28)

		ui.Scroll(c.Key("sp_chart_scroll")).Grow(1).Children(func() {
			box := ui.Box(c).FillWidth().Height(canvasH).Background(ui.RGB(11, 14, 21))
			_, py, hasPointer := box.PointerPosition()

			box.Draw(func(p *ui.Painter, r ui.Rect) {
				if r.W < 160 || r.H < 80 {
					return
				}
				padL, padR, padT, padB := float32(118), float32(92), float32(12), float32(14)
				plotX, plotY := r.X+padL, r.Y+padT
				plotW, plotH := r.W-padL-padR, r.H-padT-padB
				if plotW <= 20 || plotH <= 20 {
					return
				}
				midX := plotX + plotW*0.5
				stepY := plotH / float32(max(1, n))

				for _, frac := range []float32{-1.0, -0.5, 0, 0.5, 1.0} {
					gx := midX + frac*(plotW*0.46)
					col := colorHair
					w := float32(1)
					if frac == 0 {
						col = ui.RGBA(255, 255, 255, 0.18)
						w = 1.3
					}
					p.Line(gx, plotY, gx, plotY+plotH, w, col)
				}

				hoverRow := -1
				if hasPointer && py >= padT && py <= padT+plotH {
					idx := int((py - padT) / stepY)
					if idx >= 0 && idx < n {
						hoverRow = idx
					}
				}

				cumPts := make([][2]float32, 0, n)

				for i := 0; i < n; i++ {
					ry := plotY + float32(i)*stepY
					cy := ry + stepY*0.5
					strike := wb.Strikes[i]
					isATM := wb.ATMStrike != nil && math.Abs(strike-*wb.ATMStrike) < 1e-4

					if i == hoverRow {
						p.Fill(ui.Rect{X: r.X, Y: ry, W: r.W, H: stepY}, ui.RGBA(255, 255, 255, 0.06), 0)
					} else if isATM {
						p.Fill(ui.Rect{X: r.X, Y: ry, W: r.W, H: stepY}, ui.RGBA(255, 181, 71, 0.09), 0)
					} else if i%2 == 1 {
						p.Fill(ui.Rect{X: r.X, Y: ry, W: r.W, H: stepY}, ui.RGBA(255, 255, 255, 0.015), 0)
					}

					sLbl := wb.StrikeLabels[i]
					if isATM {
						sLbl = "▸ " + sLbl
					}
					lblCol := colorText
					if isATM {
						lblCol = colorSpot
					}
					p.RichText(r.X+8, cy-6, 60, ui.Span{Text: sLbl, Font: "monospace", Size: 10.5, Weight: 600, Color: lblCol})

					if tag := wb.StrikeTags[i]; tag != "" {
						tagCol := colorFlip
						if strings.Contains(tag, "◆C") || strings.Contains(tag, "▲OI") {
							tagCol = colorPos
						} else if strings.Contains(tag, "◆P") || strings.Contains(tag, "▼OI") {
							tagCol = colorNeg
						}
						p.RichText(r.X+64, cy-6, 54, ui.Span{Text: tag, Font: "monospace", Size: 9.5, Weight: 600, Color: tagCol})
					}

					barH := max(float32(5), min(float32(15), stepY*0.64))
					halfW := plotW * 0.46

					switch a.pressureModeIdx {
					case 0: // Net Exposure + Cumulative Curve
						val := wb.Profile[i]
						w := float32(math.Min(1.0, math.Abs(val)/maxAbs)) * halfW
						if w > 0.5 {
							if val >= 0 {
								p.Fill(ui.Rect{X: midX, Y: cy - barH*0.5, W: w, H: barH}, colorPos.Alpha(0.86), 2.5)
							} else {
								p.Fill(ui.Rect{X: midX - w, Y: cy - barH*0.5, W: w, H: barH}, colorNeg.Alpha(0.86), 2.5)
							}
						}
						cxPt := midX + float32(cumNet[i]/maxCumAbs)*halfW
						cumPts = append(cumPts, [2]float32{cxPt, cy})

						valCol := colorPos
						if val < 0 {
							valCol = colorNeg
						}
						p.RichText(plotX+plotW+8, cy-6, padR-8, ui.Span{Text: wb.ProfileStr[i], Font: "monospace", Size: 10, Weight: 600, Color: valCol})

					case 1: // Call / Put Butterfly Split
						var cVal, pVal float64
						if i < len(wb.CallProfile) {
							cVal = wb.CallProfile[i]
						}
						if i < len(wb.PutProfile) {
							pVal = wb.PutProfile[i]
						}
						cw := float32(math.Min(1.0, math.Abs(cVal)/maxAbs)) * halfW
						pw := float32(math.Min(1.0, math.Abs(pVal)/maxAbs)) * halfW
						if cw > 0.5 {
							p.Fill(ui.Rect{X: midX, Y: cy - barH*0.5, W: cw, H: barH}, colorPos.Alpha(0.85), 2.5)
						}
						if pw > 0.5 {
							p.Fill(ui.Rect{X: midX - pw, Y: cy - barH*0.5, W: pw, H: barH}, colorNeg.Alpha(0.85), 2.5)
						}
						netX := midX + float32((cVal+pVal)/maxAbs)*halfW
						p.Fill(ui.Rect{X: netX - 3.2, Y: cy - 3.2, W: 6.4, H: 6.4}, colorText, 3.2)

						p.RichText(plotX+plotW+8, cy-6, padR-8, ui.Span{Text: wb.ProfileStr[i], Font: "monospace", Size: 10, Weight: 600, Color: colorText})

					case 2: // Call Only
						var cVal float64
						cStr := "—"
						if i < len(wb.CallProfile) {
							cVal = wb.CallProfile[i]
							cStr = wb.CallProfileStr[i]
						}
						cw := float32(math.Min(1.0, math.Abs(cVal)/maxAbs)) * halfW
						if cw > 0.5 {
							if cVal >= 0 {
								p.Fill(ui.Rect{X: midX, Y: cy - barH*0.5, W: cw, H: barH}, colorPos.Alpha(0.88), 2.5)
							} else {
								p.Fill(ui.Rect{X: midX - cw, Y: cy - barH*0.5, W: cw, H: barH}, colorNeg.Alpha(0.88), 2.5)
							}
						}
						p.RichText(plotX+plotW+8, cy-6, padR-8, ui.Span{Text: cStr, Font: "monospace", Size: 10, Weight: 600, Color: colorPos})

					case 3: // Put Only
						var pVal float64
						pStr := "—"
						if i < len(wb.PutProfile) {
							pVal = wb.PutProfile[i]
							pStr = wb.PutProfileStr[i]
						}
						pw := float32(math.Min(1.0, math.Abs(pVal)/maxAbs)) * halfW
						if pw > 0.5 {
							if pVal >= 0 {
								p.Fill(ui.Rect{X: midX, Y: cy - barH*0.5, W: pw, H: barH}, colorPos.Alpha(0.88), 2.5)
							} else {
								p.Fill(ui.Rect{X: midX - pw, Y: cy - barH*0.5, W: pw, H: barH}, colorNeg.Alpha(0.88), 2.5)
							}
						}
						p.RichText(plotX+plotW+8, cy-6, padR-8, ui.Span{Text: pStr, Font: "monospace", Size: 10, Weight: 600, Color: colorNeg})
					}

					if isATM {
						p.Line(plotX, cy, plotX+plotW, cy, 1.2, colorSpot.Alpha(0.65))
					}
					if wb.Flip != nil && i+1 < n {
						s0, s1 := wb.Strikes[i], wb.Strikes[i+1]
						loS, hiS := min(s0, s1), max(s0, s1)
						if *wb.Flip >= loS && *wb.Flip <= hiS {
							fy := ry + stepY
							p.Line(plotX, fy, plotX+plotW, fy, 1.5, colorFlip.Alpha(0.85))
						}
					}
				}

				if a.pressureModeIdx == 0 && len(cumPts) >= 2 {
					p.StrokePolyline(cumPts, 2.0, colorFlip)
					for _, pt := range cumPts {
						p.Fill(ui.Rect{X: pt[0] - 2.5, Y: pt[1] - 2.5, W: 5, H: 5}, colorFlip, 2.5)
					}
				}
			})
		})
	})
}

func (a *DesktopApp) renderStrikePressureTableCard(c *ui.Context, wb *viewmodel.WorkbenchResponse) {
	ui.Column(c).Grow(1.0).Basis(0).MinWidth(0).MinHeight(0).Radius(8).
		Background(colorSurface).Border(1, colorBorder).Clip().Children(func() {
		ui.Row(c).Padding(8, 12).Gap(8).AlignItems(ui.Center).
			BorderWidth(0, 0, 1, 0).BorderColor(colorHair).Children(func() {
			ui.Text(c, "行权价压力与持仓明细表").FontSize(12.5).Bold().TextColor(colorText)
			ui.Spacer(c)
			ui.Textf(c, "共 %d 档行权价", len(wb.Strikes)).FontSize(10.5).TextColor(colorMuted)
		})

		cols := []struct {
			name string
			w    float32
		}{
			{"行权价", 68},
			{"点位标记", 68},
			{"净压力 Net", 82},
			{"Call 压力", 78},
			{"Put 压力", 78},
			{"Call OI", 66},
			{"Put OI", 66},
		}

		ui.Row(c).Height(26).Padding(0, 10).AlignItems(ui.Center).
			Background(ui.RGB(12, 16, 23)).BorderWidth(0, 0, 1, 0).BorderColor(colorBorder).Children(func() {
			for _, col := range cols {
				ui.Text(c, col.name).Width(col.w).FontSize(10).Bold().TextColor(colorFaint).SingleLine()
			}
		})

		ui.List(c.Key("sp_detail_list"), &a.pressureTable, len(wb.Strikes), func(i int) {
			strike := wb.Strikes[i]
			isATM := wb.ATMStrike != nil && math.Abs(strike-*wb.ATMStrike) < 1e-4
			bg := ui.Transparent
			if isATM {
				bg = ui.RGBA(255, 181, 71, 0.10)
			} else if i%2 == 1 {
				bg = ui.RGBA(255, 255, 255, 0.015)
			}
			cStr, pStr, cOI, pOI := "—", "—", "—", "—"
			if i < len(wb.CallProfileStr) {
				cStr = wb.CallProfileStr[i]
			}
			if i < len(wb.PutProfileStr) {
				pStr = wb.PutProfileStr[i]
			}
			if i < len(wb.StrikeCallOI) {
				cOI = wb.StrikeCallOI[i]
			}
			if i < len(wb.StrikePutOI) {
				pOI = wb.StrikePutOI[i]
			}
			netCol := colorPos
			if wb.Profile[i] < 0 {
				netCol = colorNeg
			}
			sCol := colorText
			if isATM {
				sCol = colorSpot
			}
			ui.Row(c).Height(25).Padding(0, 10).AlignItems(ui.Center).Background(bg).Children(func() {
				ui.Text(c, wb.StrikeLabels[i]).Width(cols[0].w).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().TextColor(sCol).SingleLine()
				ui.Text(c, wb.StrikeTags[i]).Width(cols[1].w).Font("monospace").FontSize(10).Bold().TextColor(colorFlip).SingleLine()
				ui.Text(c, wb.ProfileStr[i]).Width(cols[2].w).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().TextColor(netCol).SingleLine()
				ui.Text(c, cStr).Width(cols[3].w).Font("monospace").FontFeatures("tnum").FontSize(10.5).TextColor(colorPos).SingleLine()
				ui.Text(c, pStr).Width(cols[4].w).Font("monospace").FontFeatures("tnum").FontSize(10.5).TextColor(colorNeg).SingleLine()
				ui.Text(c, cOI).Width(cols[5].w).Font("monospace").FontFeatures("tnum").FontSize(10.5).TextColor(colorMuted).SingleLine()
				ui.Text(c, pOI).Width(cols[6].w).Font("monospace").FontFeatures("tnum").FontSize(10.5).TextColor(colorMuted).SingleLine()
			})
		}).Grow(1)
	})
}
