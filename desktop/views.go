package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"
)

func sectionCard(c *ui.Context, title, badge string, body func()) ui.Element {
	card := ui.Column(c).Padding(10, 12).Gap(8).Radius(8).
		Background(colorSurface).Border(1, colorBorder)
	card.Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			ui.Text(c, title).FontSize(12).Bold().TextColor(colorText).SingleLine()
			ui.Spacer(c)
			if badge != "" {
				ui.Badge(c, badge).Background(colorCard).TextColor(colorMuted)
			}
		})
		body()
	})
	return card
}

func emptyCard(c *ui.Context, title, sub string) {
	ui.Column(c).Grow(1).Center().Padding(32).Gap(6).Radius(8).
		Background(colorSurface).Border(1, colorBorder).Children(func() {
		ui.Text(c, title).FontSize(13.5).Bold().TextColor(colorText)
		if sub != "" {
			ui.Text(c, sub).FontSize(12).TextColor(colorMuted)
		}
	})
}

// ============================================================================
// 1. WORKBENCH VIEW (工作台: Exposure Matrix + Synchronized Strike Profile + Expiry Board)
// ============================================================================
func (a *DesktopApp) renderWorkbenchView(c *ui.Context) {
	wb := a.wb
	if wb == nil {
		emptyCard(c, "正在加载期权矩阵…", "正在从本地 SQLite 数据库读取最新快照")
		return
	}
	if wb.Empty {
		ui.Column(c).Grow(1).Padding(14).Children(func() {
			emptyCard(c, wb.Ticker+" 尚无期权链快照", "点击右上角「采集」或左侧「全部采集」抓取 Cboe 期权全链数据")
		})
		return
	}

	ui.Column(c).Grow(1).MinHeight(0).Padding(4, 14, 12, 14).Gap(8).Children(func() {
		// Control toolbar
		ui.Row(c).Padding(6, 10).Radius(8).Background(colorSurface).Border(1, colorBorder).
			AlignItems(ui.Center).Gap(10).Wrap().Children(func() {
			ui.Text(c, "曝光维度").FontSize(11).TextColor(colorMuted)
			prevDim := a.dimIdx
			if ui.Segmented(c.Key("wb_dim"), &a.dimIdx, "GEX 伽马", "Vanna 偏度", "Charm 时间").
				Label("曝光维度").Changed() && a.dimIdx != prevDim {
				a.loadActiveView(a.selectedSym)
			}

			ui.Text(c, "方向").FontSize(11).TextColor(colorMuted)
			prevSide := a.sideIdx
			if ui.Segmented(c.Key("wb_side"), &a.sideIdx, "净值 Net", "Call 看涨", "Put 看跌").
				Label("方向").Changed() && a.sideIdx != prevSide {
				a.loadActiveView(a.selectedSym)
			}

			if len(wb.Moments) > 0 {
				labels := make([]string, len(wb.Moments))
				curLabel := wb.Moments[0].Label
				for i, m := range wb.Moments {
					labels[i] = m.Label
					if m.Value == a.momentChoice {
						curLabel = m.Label
					}
				}
				chosen := curLabel
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "快照").FontSize(11).TextColor(colorMuted)
					if ui.Select(c.Key("wb_moment"), &chosen, labels).Label("快照回放").Changed() {
						for _, m := range wb.Moments {
							if m.Label == chosen && m.Value != a.momentChoice {
								a.momentChoice = m.Value
								a.loadActiveView(a.selectedSym)
								break
							}
						}
					}
				})
			}

			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Textf(c, "到期 %d", int(math.Round(a.nExpiries))).Font("monospace").FontSize(11).TextColor(colorMuted)
				if ui.Stepper(c.Key("wb_nexp"), &a.nExpiries, 3, 24, 1).Label("到期日数").Changed() {
					a.loadActiveView(a.selectedSym)
				}
			})

			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Textf(c, "行权价 ±%d档", int(math.Round(a.strikesSide))).Font("monospace").FontSize(11).TextColor(colorMuted)
				if ui.Stepper(c.Key("wb_strk"), &a.strikesSide, 6, 40, 2).Label("上下行权价档数").Changed() {
					a.loadActiveView(a.selectedSym)
				}
			})

			ui.Spacer(c)

			// Per-Ticker Dual-Track Collection Controls (低频 / 高频独立开关与频率调整)
			a.renderInlineTickerCollectControls(c)
		})

		// Unified Workbench Cockpit: Compact GEX Matrix + Intraday Volume/Tide (2 Charts) + Volatility + Expiry Board
		ui.Scroll(c.Key("wb_main_scroll")).Grow(1).Children(func() {
			ui.Column(c).Gap(10).Children(func() {
				a.renderExposureMatrixCard(c, wb)
				a.renderIntradayTideSection(c, wb)
				a.renderWorkbenchVolatilitySection(c, a.vol)
				a.renderExpiryBoardCard(c, wb)
			})
		})
	})
}

func (a *DesktopApp) renderExposureMatrixCard(c *ui.Context, wb *WorkbenchResponse) {
	ui.Column(c).Padding(8, 12).Gap(6).Radius(8).
		Background(colorSurface).Border(1, colorBorder).Children(func() {
		// Compact Header Row with Quick Jump to Strike Cumulative Pressure View
		ui.Row(c).AlignItems(ui.Center).Gap(8).Wrap().Children(func() {
			ui.Text(c, "缩写 GEX / 敞口热力矩阵").FontSize(12).Bold().TextColor(colorText).SingleLine()
			badge := fmt.Sprintf("%s · %s · %d 到期 × %d 行权价 · Σ %s", wb.DimensionCN, wb.UnitCN, len(wb.Expiries), len(wb.Strikes), wb.GrandTotalStr)
			ui.Badge(c, badge).Background(colorCard).TextColor(colorMuted)

			ui.Spacer(c)

			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "▸ATM").Font("monospace").FontSize(10).TextColor(colorSpot)
				ui.Text(c, "◆C/◆P GEX墙").Font("monospace").FontSize(10).TextColor(colorPos)
				ui.Text(c, "▲OI/▼OI 持仓墙").Font("monospace").FontSize(10).TextColor(colorMuted)
				ui.Text(c, "✕Pain 痛点").Font("monospace").FontSize(10).TextColor(colorVol)
			})

			if ui.Button(c, "▤ 行权价累计压力 ➔").Tooltip("切换到独立的行权价累计压力分布视图").Clicked() {
				a.viewIdx = 1
				a.loadActiveView(a.selectedSym)
			}
		})

		nCols := len(wb.Expiries)
		nRows := len(wb.Strikes)
		if nCols == 0 || nRows == 0 {
			ui.Text(c, "当前筛选范围内无有效曝光数据").FontSize(12).TextColor(colorMuted).Padding(8)
			return
		}

		const (
			strikeColW = float32(108)
			rowH       = float32(20)
		)

		// Header row: Strike | Expiries...
		ui.Row(c).Height(24).AlignItems(ui.Stretch).
			Background(ui.RGB(12, 16, 23)).Radius(4).Clip().Children(func() {
			ui.Row(c).Width(strikeColW).Shrink(0).Padding(0, 6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "行权价 STRIKE").Font("monospace").FontSize(10).Bold().TextColor(colorFaint).SingleLine()
			})
			for j, exp := range wb.Expiries {
				dte := ""
				if j < len(wb.ExpiryDTEs) {
					dte = wb.ExpiryDTEs[j]
				}
				ui.Row(c).Grow(1).Basis(0).MinWidth(52).Center().Gap(3).
					BorderWidth(0, 0, 0, 1).BorderColor(colorHair).Children(func() {
					ui.Text(c, exp).Font("monospace").FontSize(10).Bold().TextColor(colorText).SingleLine()
					ui.Text(c, dte).Font("monospace").FontSize(9).TextColor(colorMuted).SingleLine()
				})
			}
		})

		// Pinned Total Row
		ui.Row(c).Height(22).AlignItems(ui.Stretch).
			Background(ui.RGB(18, 24, 34)).Border(1, colorBorder).Radius(4).Clip().Children(func() {
			ui.Row(c).Width(strikeColW).Shrink(0).Padding(0, 6).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Σ "+wb.GrandTotalStr).Font("monospace").FontFeatures("tnum").FontSize(10).Bold().TextColor(colorText).SingleLine()
			})
			for j := 0; j < nCols; j++ {
				tVal := 0.0
				tStr := "—"
				if j < len(wb.Totals) {
					tVal = wb.Totals[j]
				}
				if j < len(wb.TotalsStr) {
					tStr = wb.TotalsStr[j]
				}
				col := colorMuted
				if tVal > 0 {
					col = colorPos
				} else if tVal < 0 {
					col = colorNeg
				}
				ui.Row(c).Grow(1).Basis(0).MinWidth(52).Center().
					BorderWidth(0, 0, 0, 1).BorderColor(colorHair).Children(func() {
					ui.Text(c, tStr).Font("monospace").FontFeatures("tnum").FontSize(10).Bold().TextColor(col).SingleLine()
				})
			}
		})

		// Compact scrollable matrix rows (capped at 310px height so charts below are immediately visible)
		ui.Scroll(c.Key("wb_compact_matrix_scroll")).MaxHeight(310).Children(func() {
			ui.Column(c).Gap(1).Children(func() {
				for rIdx := 0; rIdx < nRows; rIdx++ {
					strikeVal := wb.Strikes[rIdx]
					strikeLbl := wb.StrikeLabels[rIdx]
					strikeTag := ""
					if rIdx < len(wb.StrikeTags) {
						strikeTag = wb.StrikeTags[rIdx]
					}
					isATM := wb.ATMStrike != nil && math.Abs(strikeVal-*wb.ATMStrike) < 1e-4

					rowEl := ui.Row(c).Height(rowH).AlignItems(ui.Stretch)
					if isATM {
						rowEl.Border(1, colorSpot).Radius(3)
					}
					rowEl.Children(func() {
						strikeBg := ui.RGB(14, 19, 27)
						strikeCol := colorText
						if isATM {
							strikeBg = ui.RGBA(255, 181, 71, 0.16)
							strikeCol = colorSpot
						}
						ui.Row(c).Width(strikeColW).Shrink(0).Padding(0, 6).AlignItems(ui.Center).
							Background(strikeBg).Gap(3).Children(func() {
							prefix := " "
							if isATM {
								prefix = "▸"
							}
							ui.Text(c, prefix+strikeLbl).Font("monospace").FontFeatures("tnum").
								FontSize(10.5).Bold().TextColor(strikeCol).SingleLine()
							ui.Spacer(c)
							if strikeTag != "" {
								tagCol := colorFlip
								if strings.Contains(strikeTag, "◆C") || strings.Contains(strikeTag, "▲OI") {
									tagCol = colorPos
								} else if strings.Contains(strikeTag, "◆P") || strings.Contains(strikeTag, "▼OI") {
									tagCol = colorNeg
								} else if strings.Contains(strikeTag, "✕Pain") {
									tagCol = colorVol
								}
								ui.Text(c, strikeTag).Font("monospace").FontSize(9).Bold().TextColor(tagCol).SingleLine()
							}
						})

						for cIdx := 0; cIdx < nCols; cIdx++ {
							txt := ""
							norm := 0.0
							tip := ""
							if rIdx < len(wb.CellsText) && cIdx < len(wb.CellsText[rIdx]) {
								txt = wb.CellsText[rIdx][cIdx]
							}
							if rIdx < len(wb.CellsNorm) && cIdx < len(wb.CellsNorm[rIdx]) {
								norm = wb.CellsNorm[rIdx][cIdx]
							}
							if rIdx < len(wb.CellsTip) && cIdx < len(wb.CellsTip[rIdx]) {
								tip = wb.CellsTip[rIdx][cIdx]
							}
							bg := exposureColor(norm)
							fg := colorText
							if math.Abs(norm) < 0.08 {
								fg = colorMuted
							} else if math.Abs(norm) > 0.65 {
								fg = ui.RGB(250, 252, 255)
							}
							cell := ui.Row(c).Grow(1).Basis(0).MinWidth(52).Center().
								Background(bg).BorderWidth(0, 0, 0, 1).BorderColor(colorHair)
							if tip != "" {
								cell.Tooltip(tip)
							}
							cell.Children(func() {
								if txt != "" {
									ui.Text(c, txt).Font("monospace").FontFeatures("tnum").FontSize(10).TextColor(fg).SingleLine()
								}
							})
						}
					})
				}
			})
		})
	})
}

func (a *DesktopApp) renderExpiryBoardCard(c *ui.Context, wb *WorkbenchResponse) {
	rows := wb.Board
	sectionCard(c, "到期日全景面板 (Expiry Term Board)", fmt.Sprintf("%d 个到期日", len(rows)), func() {
		cols := []ui.TableColumn{
			{Title: "到期日", Width: 86},
			{Title: "DTE", Width: 56, Align: ui.End},
			{Title: "净 GEX", Width: 96, Align: ui.End},
			{Title: "Call GEX", Width: 92, Align: ui.End},
			{Title: "Put GEX", Width: 92, Align: ui.End},
			{Title: "Max Pain", Width: 84, Align: ui.End},
			{Title: "ATM IV", Width: 76, Align: ui.End},
			{Title: "±1σ 振幅", Width: 86, Align: ui.End},
			{Title: "±1σ %", Width: 74, Align: ui.End},
			{Title: "Call OI", Width: 86, Align: ui.End},
			{Title: "Put OI", Width: 86, Align: ui.End},
			{Title: "P/C OI", Width: 68, Align: ui.End},
			{Title: "合约数", Width: 66, Align: ui.End},
		}
		h := float32(min(260, max(120, 32+len(rows)*26)))
		ui.Table(c.Key("board_tbl"), &a.boardTable, cols, len(rows), func(row, col int) {
			if row >= len(rows) {
				return
			}
			item := rows[row]
			switch col {
			case 0:
				ui.Text(c, item.Expiry).Font("monospace").FontSize(11.5).Bold().SingleLine()
			case 1:
				ui.Textf(c, "%dd", item.DTE).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorMuted).SingleLine()
			case 2:
				tc := colorPos
				if item.NetGEX < 0 {
					tc = colorNeg
				}
				ui.Text(c, item.NetGEXStr).Font("monospace").FontFeatures("tnum").FontSize(11.5).Bold().TextColor(tc).SingleLine()
			case 3:
				ui.Text(c, item.CallGEXStr).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorPos).SingleLine()
			case 4:
				ui.Text(c, item.PutGEXStr).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorNeg).SingleLine()
			case 5:
				ui.Text(c, item.MaxPain).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorVol).SingleLine()
			case 6:
				ui.Text(c, item.ATMIV).Font("monospace").FontFeatures("tnum").FontSize(11.5).SingleLine()
			case 7:
				ui.Text(c, item.Move).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorSpot).SingleLine()
			case 8:
				ui.Text(c, item.MovePct).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorSpot).SingleLine()
			case 9:
				ui.Text(c, item.CallOI).Font("monospace").FontFeatures("tnum").FontSize(11.5).SingleLine()
			case 10:
				ui.Text(c, item.PutOI).Font("monospace").FontFeatures("tnum").FontSize(11.5).SingleLine()
			case 11:
				ui.Text(c, item.PCR).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorMuted).SingleLine()
			case 12:
				ui.Textf(c, "%d", item.Contracts).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorMuted).SingleLine()
			}
		}).Height(h).Border(1, colorBorder).Radius(6)
	})
}

// ============================================================================
// 2. VOLATILITY VIEW (波动率: Multi-Expiry Skew + Term Structure + IV/RV History)
// ============================================================================
func (a *DesktopApp) renderVolatilityView(c *ui.Context) {
	vol := a.vol
	if vol == nil {
		emptyCard(c, "正在计算波动率曲面与期限结构…", "")
		return
	}
	if vol.Empty {
		emptyCard(c, "暂无波动率数据", "请先采集当前标的期权链")
		return
	}

	ui.Scroll(c.Key("vol_scroll")).Grow(1).Children(func() {
		ui.Column(c).Padding(6, 14, 14, 14).Gap(10).Children(func() {
			// Toolbar
			ui.Row(c).Padding(6, 10).Radius(8).Background(colorSurface).Border(1, colorBorder).
				AlignItems(ui.Center).Gap(12).Children(func() {
				ui.Text(c, "微笑曲线行权价范围 (相对现价 ±%)").FontSize(11.5).TextColor(colorMuted)
				if ui.Stepper(c.Key("vol_width"), &a.skewWidth, 5, 30, 2.5).Label("行权价范围").Changed() {
					a.loadActiveView(a.selectedSym)
				}
				ui.Textf(c, "±%.1f%%", a.skewWidth).Font("monospace").FontSize(11.5).Bold().TextColor(colorFlip)
				ui.Spacer(c)
				for _, rv := range vol.RV {
					ui.Badge(c, fmt.Sprintf("%s: %s", rv.Window, rv.Value)).Background(colorCard).TextColor(colorText)
				}
			})

			// Top 2-column charts: Skew + Term Structure
			ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
				sectionCard(c, "波动率微笑曲线 (Multi-Expiry IV Skew)", "OTM IV 均值", func() {
					a.drawSkewChart(c, vol)
				}).Grow(1).Basis(0)

				sectionCard(c, "波动率期限结构 (ATM IV & ±1σ 预期波动)", fmt.Sprintf("%d 个到期日", len(vol.Term)), func() {
					a.drawTermChart(c, vol)
				}).Grow(1).Basis(0)
			})

			// Bottom IV History chart
			sectionCard(c, "成交量加权 IV 历史走势 (Volume-Weighted IV History)", fmt.Sprintf("%d 个采样点", len(vol.IVHistory)), func() {
				a.drawIVHistoryChart(c, vol)
			})
		})
	})
}

func (a *DesktopApp) drawSkewChart(c *ui.Context, vol *VolatilityResponse) {
	palette := []ui.Color{colorFlip, colorPos, colorSpot, colorNeg, colorVol}
	ui.Column(c).Gap(6).Children(func() {
		// Legend row
		ui.Row(c).Gap(12).Wrap().Children(func() {
			for i, curve := range vol.Skew {
				col := palette[i%len(palette)]
				ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
					ui.Box(c).Size(10, 3).Radius(1).Background(col)
					ui.Text(c, curve.Label).Font("monospace").FontSize(10.5).TextColor(colorMuted)
				})
			}
		})

		ui.Box(c).Height(250).Radius(6).Background(ui.RGB(11, 15, 22)).Border(1, colorHair).
			Draw(func(p *ui.Painter, r ui.Rect) {
				if len(vol.Skew) == 0 {
					return
				}
				minX, maxX := math.Inf(1), math.Inf(-1)
				minY, maxY := math.Inf(1), math.Inf(-1)
				for _, cv := range vol.Skew {
					for i := range cv.Strikes {
						x, y := cv.Strikes[i], cv.IVs[i]
						if x < minX {
							minX = x
						}
						if x > maxX {
							maxX = x
						}
						if y < minY {
							minY = y
						}
						if y > maxY {
							maxY = y
						}
					}
				}
				if !(minX < maxX && minY < maxY) {
					return
				}
				padY := (maxY - minY) * 0.12
				if padY < 1 {
					padY = 1
				}
				minY = math.Max(0, minY-padY)
				maxY += padY

				plot := ui.Rect{X: r.X + 46, Y: r.Y + 12, W: r.W - 60, H: r.H - 34}
				if plot.W <= 20 || plot.H <= 20 {
					return
				}

				// Horizontal grid & Y labels
				for i := 0; i <= 4; i++ {
					frac := float32(i) / 4
					yy := plot.Y + plot.H*(1-frac)
					p.Line(plot.X, yy, plot.X+plot.W, yy, 1, colorHair)
					val := minY + float64(frac)*(maxY-minY)
					sp := ui.Span{Text: fmt.Sprintf("%.1f%%", val), Font: "monospace", Size: 9.5, Color: colorFaint}
					tw, th := p.MeasureText(0, sp)
					p.RichText(plot.X-tw-6, yy-th/2, 0, sp)
				}

				// X axis labels
				for i := 0; i <= 4; i++ {
					frac := float32(i) / 4
					xx := plot.X + plot.W*frac
					val := minX + float64(frac)*(maxX-minX)
					sp := ui.Span{Text: fmt.Sprintf("%.0f", val), Font: "monospace", Size: 9.5, Color: colorFaint}
					tw, _ := p.MeasureText(0, sp)
					p.RichText(xx-tw/2, plot.Y+plot.H+6, 0, sp)
				}

				// Spot vertical line
				if vol.Spot >= minX && vol.Spot <= maxX {
					sx := plot.X + float32((vol.Spot-minX)/(maxX-minX))*plot.W
					var dash ui.Path
					dash.MoveTo(sx, plot.Y).LineTo(sx, plot.Y+plot.H)
					p.StrokePath(dash.Dash(0, 4, 4), 1.2, colorSpot)
				}

				// Curves
				p.Clip(plot, 0, func() {
					for cIdx, cv := range vol.Skew {
						if len(cv.Strikes) < 2 {
							continue
						}
						col := palette[cIdx%len(palette)]
						var path ui.Path
						for i := range cv.Strikes {
							xx := plot.X + float32((cv.Strikes[i]-minX)/(maxX-minX))*plot.W
							yy := plot.Y + plot.H*(1-float32((cv.IVs[i]-minY)/(maxY-minY)))
							if i == 0 {
								path.MoveTo(xx, yy)
							} else {
								path.LineTo(xx, yy)
							}
						}
						p.StrokePath(&path, 2, col)
					}
				})
			})
	})
}

func (a *DesktopApp) drawTermChart(c *ui.Context, vol *VolatilityResponse) {
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Box(c).Size(10, 3).Radius(1).Background(colorVol)
				ui.Text(c, "ATM IV (%)").Font("monospace").FontSize(10.5).TextColor(colorMuted)
			})
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Box(c).Size(10, 8).Radius(2).Background(colorSpot.Alpha(0.55))
				ui.Text(c, "±1σ 隐含振幅 (%)").Font("monospace").FontSize(10.5).TextColor(colorMuted)
			})
		})

		ui.Box(c).Height(250).Radius(6).Background(ui.RGB(11, 15, 22)).Border(1, colorHair).
			Draw(func(p *ui.Painter, r ui.Rect) {
				n := len(vol.Term)
				if n == 0 {
					return
				}
				maxIV, maxMove := 1.0, 0.5
				for _, pt := range vol.Term {
					if pt.ATMIV > maxIV {
						maxIV = pt.ATMIV
					}
					if pt.MovePct > maxMove {
						maxMove = pt.MovePct
					}
				}
				maxIV *= 1.15
				maxMove *= 1.25

				plot := ui.Rect{X: r.X + 44, Y: r.Y + 12, W: r.W - 58, H: r.H - 36}
				if plot.W <= 20 || plot.H <= 20 {
					return
				}

				for i := 0; i <= 4; i++ {
					frac := float32(i) / 4
					yy := plot.Y + plot.H*(1-frac)
					p.Line(plot.X, yy, plot.X+plot.W, yy, 1, colorHair)
					val := float64(frac) * maxIV
					sp := ui.Span{Text: fmt.Sprintf("%.0f%%", val), Font: "monospace", Size: 9.5, Color: colorFaint}
					tw, th := p.MeasureText(0, sp)
					p.RichText(plot.X-tw-6, yy-th/2, 0, sp)
				}

				slotW := plot.W / float32(n)
				barW := min(28, slotW*0.52)
				var ivPath ui.Path

				for i, pt := range vol.Term {
					cx := plot.X + slotW*(float32(i)+0.5)
					// Bar for MovePct
					bh := float32(pt.MovePct/maxMove) * plot.H
					p.Fill(ui.Rect{X: cx - barW/2, Y: plot.Y + plot.H - bh, W: barW, H: bh}, colorSpot.Alpha(0.38), 3)

					// Line point for ATM IV
					iy := plot.Y + plot.H*(1-float32(pt.ATMIV/maxIV))
					if i == 0 {
						ivPath.MoveTo(cx, iy)
					} else {
						ivPath.LineTo(cx, iy)
					}
					p.Fill(ui.Rect{X: cx - 3, Y: iy - 3, W: 6, H: 6}, colorVol, 3)

					// X label
					if n <= 10 || i%2 == 0 {
						sp := ui.Span{Text: pt.Label, Font: "monospace", Size: 9, Color: colorFaint}
						tw, _ := p.MeasureText(0, sp)
						p.RichText(cx-tw/2, plot.Y+plot.H+6, 0, sp)
					}
				}
				if n > 1 {
					p.StrokePath(&ivPath, 2, colorVol)
				}
			})
	})
}

func (a *DesktopApp) drawIVHistoryChart(c *ui.Context, vol *VolatilityResponse) {
	pts := vol.IVHistory
	if len(pts) < 2 {
		ui.Text(c, "历史采样点不足（当前仅 1 次快照，持续定时采集后将自动绘出 IV 历史曲线）").
			FontSize(11.5).TextColor(colorMuted).Padding(8)
		return
	}
	ui.Box(c).Height(190).Radius(6).Background(ui.RGB(11, 15, 22)).Border(1, colorHair).
		Draw(func(p *ui.Painter, r ui.Rect) {
			minY, maxY := math.Inf(1), math.Inf(-1)
			for _, pt := range pts {
				if pt.IV < minY {
					minY = pt.IV
				}
				if pt.IV > maxY {
					maxY = pt.IV
				}
			}
			pad := (maxY - minY) * 0.15
			if pad < 0.5 {
				pad = 0.5
			}
			minY -= pad
			maxY += pad

			plot := ui.Rect{X: r.X + 46, Y: r.Y + 10, W: r.W - 60, H: r.H - 28}
			if plot.W <= 20 || plot.H <= 20 {
				return
			}
			for i := 0; i <= 3; i++ {
				frac := float32(i) / 3
				yy := plot.Y + plot.H*(1-frac)
				p.Line(plot.X, yy, plot.X+plot.W, yy, 1, colorHair)
				val := minY + float64(frac)*(maxY-minY)
				sp := ui.Span{Text: fmt.Sprintf("%.1f%%", val), Font: "monospace", Size: 9.5, Color: colorFaint}
				tw, th := p.MeasureText(0, sp)
				p.RichText(plot.X-tw-6, yy-th/2, 0, sp)
			}

			var line ui.Path
			for i, pt := range pts {
				xx := plot.X + plot.W*float32(i)/float32(len(pts)-1)
				yy := plot.Y + plot.H*(1-float32((pt.IV-minY)/(maxY-minY)))
				if i == 0 {
					line.MoveTo(xx, yy)
				} else {
					line.LineTo(xx, yy)
				}
			}
			p.StrokePath(&line, 2, colorFlip)
		})
}

// ============================================================================
// 3. CHAIN VIEW (期权链: Filter Bar + Virtualized Chain Table + Contract Inspector)
// ============================================================================
func (a *DesktopApp) renderChainView(c *ui.Context) {
	ch := a.chain
	if ch == nil {
		emptyCard(c, "正在加载期权全链明细…", "")
		return
	}
	if ch.Empty {
		emptyCard(c, "暂无期权链数据", "请先采集当前标的期权链")
		return
	}

	ui.Column(c).Grow(1).MinHeight(0).Padding(4, 14, 12, 14).Gap(8).Children(func() {
		// Filter bar
		ui.Row(c).Padding(6, 10).Radius(8).Background(colorSurface).Border(1, colorBorder).
			AlignItems(ui.Center).Gap(12).Wrap().Children(func() {
			if len(ch.Expiries) > 0 {
				opts := make([]string, 0, len(ch.Expiries)+1)
				opts = append(opts, "全部近期到期日")
				cur := opts[0]
				for _, e := range ch.Expiries {
					opts = append(opts, e.Label)
					if e.ISO == a.chainExpISO {
						cur = e.Label
					}
				}
				chosen := cur
				ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
					ui.Text(c, "到期日").FontSize(11).TextColor(colorMuted)
					if ui.Select(c.Key("ch_exp"), &chosen, opts).Label("到期日过滤").Changed() {
						newISO := ""
						for _, e := range ch.Expiries {
							if e.Label == chosen {
								newISO = e.ISO
								break
							}
						}
						a.chainExpISO = newISO
						a.loadActiveView(a.selectedSym)
					}
				})
			}

			prevSide := a.chainSideIdx
			if ui.Segmented(c.Key("ch_side"), &a.chainSideIdx, "全部", "Call", "Put").
				Label("期权类型").Changed() && a.chainSideIdx != prevSide {
				a.loadActiveView(a.selectedSym)
			}

			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Textf(c, "现价 ±%.0f%%", a.chainBand).Font("monospace").FontSize(11).TextColor(colorMuted)
				if ui.Stepper(c.Key("ch_band"), &a.chainBand, 2, 50, 2).Label("行权价范围").Changed() {
					a.loadActiveView(a.selectedSym)
				}
			})

			ui.Row(c).AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Textf(c, "最小 OI %d", int(math.Round(a.chainMinOI))).Font("monospace").FontSize(11).TextColor(colorMuted)
				if ui.Stepper(c.Key("ch_oi"), &a.chainMinOI, 0, 5000, 100).Label("最小持仓量").Changed() {
					a.loadActiveView(a.selectedSym)
				}
			})

			ui.Spacer(c)
			ui.Textf(c, "匹配 %d / 全链 %d 张合约（点击行查看单合约归因）", ch.TotalMatching, ch.TotalChain).
				FontSize(11).TextColor(colorMuted)
		})

		ui.Row(c).AlignItems(ui.Center).Gap(8).Children(func() {
			if ui.Button(c, "上一页").Disabled(ch.Page <= 0 || a.loading).Clicked() {
				a.chainPage = ch.Page - 1
				a.loadActiveView(a.selectedSym)
			}
			pages := max(1, (ch.TotalMatching+max(1, ch.PageSize)-1)/max(1, ch.PageSize))
			ui.Textf(c, "第 %d / %d 页", ch.Page+1, pages).FontSize(11).TextColor(colorMuted)
			if ui.Button(c, "下一页").Disabled(ch.Page+1 >= pages || a.loading).Clicked() {
				a.chainPage = ch.Page + 1
				a.loadActiveView(a.selectedSym)
			}
		})

		// Pinned contracts bar
		if len(ch.Pinned) > 0 {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Wrap().Children(func() {
				ui.Text(c, "★ 已置顶合约:").FontSize(11).Bold().TextColor(colorSpot)
				for _, p := range ch.Pinned {
					pin := p
					btn := ui.Button(c, pin.Label)
					if btn.Clicked() {
						a.loadContract(pin.ExpiryISO, pin.Strike, pin.Type)
					}
				}
			})
		}

		// Virtualized Chain Table
		rows := ch.Rows
		cols := []ui.TableColumn{
			{Title: "到期", Width: 68},
			{Title: "DTE", Width: 50, Align: ui.End},
			{Title: "行权价", Width: 78, Align: ui.End},
			{Title: "类型", Width: 54},
			{Title: "状态", Width: 54},
			{Title: "Bid", Width: 66, Align: ui.End},
			{Title: "Ask", Width: 66, Align: ui.End},
			{Title: "最新价", Width: 68, Align: ui.End},
			{Title: "成交量", Width: 76, Align: ui.End},
			{Title: "持仓 OI", Width: 80, Align: ui.End},
			{Title: "IV", Width: 68, Align: ui.End},
			{Title: "Delta", Width: 68, Align: ui.End},
			{Title: "Gamma", Width: 74, Align: ui.End},
			{Title: "Theta", Width: 68, Align: ui.End},
			{Title: "Vega", Width: 68, Align: ui.End},
			{Title: "Vanna", Width: 68, Align: ui.End},
			{Title: "Charm", Width: 68, Align: ui.End},
		}
		a.chainTable.Selected = &a.chainRowSel
		tbl := ui.Table(c.Key("chain_tbl"), &a.chainTable, cols, len(rows), func(row, col int) {
			if row >= len(rows) {
				return
			}
			r := rows[row]
			switch col {
			case 0:
				ui.Text(c, r.Expiry).Font("monospace").FontSize(11).SingleLine()
			case 1:
				ui.Textf(c, "%dd", r.DTE).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
			case 2:
				ui.Text(c, r.StrikeStr).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().SingleLine()
			case 3:
				tc := colorPos
				if r.Type == "P" {
					tc = colorNeg
				}
				ui.Text(c, r.TypeShort).Font("monospace").FontSize(11).Bold().TextColor(tc).SingleLine()
			case 4:
				mc := colorMuted
				if r.Moneyness == "ATM" {
					mc = colorSpot
				} else if r.Moneyness == "ITM" {
					mc = colorFlip
				}
				ui.Text(c, r.Moneyness).Font("monospace").FontSize(10.5).TextColor(mc).SingleLine()
			case 5:
				ui.Text(c, r.Bid).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 6:
				ui.Text(c, r.Ask).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 7:
				ui.Text(c, r.Last).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
			case 8:
				ui.Text(c, r.Volume).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 9:
				ui.Text(c, r.OI).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 10:
				ui.Text(c, r.IV).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorVol).SingleLine()
			case 11:
				ui.Text(c, r.Delta).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 12:
				ui.Text(c, r.Gamma).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 13:
				ui.Text(c, r.Theta).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 14:
				ui.Text(c, r.Vega).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
			case 15:
				ui.Text(c, r.Vanna).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
			case 16:
				ui.Text(c, r.Charm).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
			}
		}).Grow(1).Border(1, colorBorder).Radius(6)
		if tbl.Changed() && a.chainRowSel >= 0 && a.chainRowSel < len(rows) {
			sel := rows[a.chainRowSel]
			a.loadContract(sel.ExpiryISO, sel.Strike, sel.Type)
		}

		// Contract detail inspector drawer at bottom
		if a.contract != nil && !a.contract.Empty {
			a.renderContractDetailCard(c, a.contract)
		}
	})
}

func (a *DesktopApp) renderContractDetailCard(c *ui.Context, ct *ContractResponse) {
	sectionCard(c, "单合约深度追踪 · "+ct.Name, fmt.Sprintf("%d 个快照样本", ct.Samples), func() {
		ui.Row(c).Gap(14).AlignItems(ui.Center).Wrap().Children(func() {
			kv := func(k, v string, col ui.Color) {
				ui.Row(c).Gap(5).AlignItems(ui.Baseline).Children(func() {
					ui.Text(c, k).FontSize(10.5).TextColor(colorFaint)
					ui.Text(c, v).Font("monospace").FontFeatures("tnum").FontSize(12.5).Bold().TextColor(col)
				})
			}
			kv("中间价", ct.PriceStr, colorSpot)
			kv("IV", ct.IVStr, colorVol)
			kv("Delta", ct.DeltaStr, colorText)
			kv("Gamma", ct.GammaStr, colorText)
			kv("Theta", ct.ThetaStr, colorNeg)
			kv("Vega", ct.VegaStr, colorFlip)

			ui.Spacer(c)
			btnPin := ui.Button(c, "★ 置顶该合约")
			if btnPin.Clicked() {
				sym := a.selectedSym
				exp := ct.ExpiryISO
				strk := ct.Strike
				tp := ct.Type
				go func() {
					_ = a.api.postJSON("/api/pin", map[string]any{
						"ticker": sym, "expiry": exp, "strike": strk, "type": tp,
					}, nil)
					a.refreshAll()
				}()
			}
		})

		if len(ct.Attribution) > 0 {
			ui.Row(c).Gap(6).AlignItems(ui.Stretch).Children(func() {
				for _, step := range ct.Attribution {
					col := colorText
					switch step.Kind {
					case "total":
						col = colorFlip
					case "pos":
						col = colorPos
					case "neg":
						col = colorNeg
					}
					ui.Column(c).Grow(1).Basis(0).Padding(6, 8).Radius(6).
						Background(ui.RGB(13, 17, 24)).Border(1, colorHair).Gap(2).Children(func() {
						ui.Text(c, step.Label).FontSize(10).TextColor(colorMuted).SingleLine()
						ui.Text(c, step.Str).Font("monospace").FontFeatures("tnum").FontSize(12).Bold().TextColor(col).SingleLine()
					})
				}
			})
		}
	})
}

// ============================================================================
// 4. FLOW VIEW (资金流: Unusual Activity + OI Delta + Positioning Ladder + PCR)
// ============================================================================
func (a *DesktopApp) renderFlowView(c *ui.Context) {
	fl := a.flow
	if fl == nil {
		emptyCard(c, "正在加载异动与持仓变化数据…", "")
		return
	}
	if fl.Empty {
		emptyCard(c, "暂无资金流与持仓对比数据", "请先采集当前标的期权链")
		return
	}

	ui.Scroll(c.Key("flow_scroll")).Grow(1).Children(func() {
		ui.Column(c).Padding(6, 14, 14, 14).Gap(10).Children(func() {
			// Top 2 columns: Unusual Volume + OI Delta
			ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
				sectionCard(c, "异常放量合约 (Unusual Activity)", fmt.Sprintf("%d 张异动", len(fl.Unusual)), func() {
					if len(fl.Unusual) == 0 {
						ui.Text(c, "当前快照未检测到显著异常放量合约 (Volume > 2×OI 或 Z-score 突增)").
							FontSize(11.5).TextColor(colorMuted).Padding(8)
						return
					}
					cols := []ui.TableColumn{
						{Title: "到期", Width: 68},
						{Title: "行权价", Width: 74, Align: ui.End},
						{Title: "类型", Width: 56},
						{Title: "成交量", Width: 78, Align: ui.End},
						{Title: "持仓 OI", Width: 78, Align: ui.End},
						{Title: "Vol/OI", Width: 68, Align: ui.End},
						{Title: "Z-Score", Width: 68, Align: ui.End},
					}
					ui.Table(c.Key("unusual_tbl"), &a.unusualTable, cols, len(fl.Unusual), func(row, col int) {
						if row >= len(fl.Unusual) {
							return
						}
						u := fl.Unusual[row]
						switch col {
						case 0:
							ui.Text(c, u.Expiry).Font("monospace").FontSize(11).SingleLine()
						case 1:
							ui.Text(c, u.Strike).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().SingleLine()
						case 2:
							tc := colorPos
							if strings.HasPrefix(u.Type, "P") {
								tc = colorNeg
							}
							ui.Text(c, u.Type).Font("monospace").FontSize(11).Bold().TextColor(tc).SingleLine()
						case 3:
							ui.Text(c, u.Volume).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().TextColor(colorSpot).SingleLine()
						case 4:
							ui.Text(c, u.OI).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
						case 5:
							ui.Text(c, u.VolOI).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorFlip).SingleLine()
						case 6:
							ui.Text(c, u.ZScore).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
						}
					}).Height(230).Border(1, colorBorder).Radius(6)
				}).Grow(1).Basis(0)

				sectionCard(c, "未平仓量变化排名 (OI Delta)", fmt.Sprintf("Top %d", len(fl.OIDelta)), func() {
					if len(fl.OIDelta) == 0 {
						ui.Text(c, "需要至少 2 个交易日的快照才能计算跨日 ΔOI 增减仓排名").
							FontSize(11.5).TextColor(colorMuted).Padding(8)
						return
					}
					cols := []ui.TableColumn{
						{Title: "到期", Width: 68},
						{Title: "行权价", Width: 74, Align: ui.End},
						{Title: "类型", Width: 56},
						{Title: "当前 OI", Width: 82, Align: ui.End},
						{Title: "ΔOI 变动", Width: 86, Align: ui.End},
						{Title: "变动幅度", Width: 76, Align: ui.End},
					}
					ui.Table(c.Key("oidelta_tbl"), &a.oiDeltaTable, cols, len(fl.OIDelta), func(row, col int) {
						if row >= len(fl.OIDelta) {
							return
						}
						d := fl.OIDelta[row]
						switch col {
						case 0:
							ui.Text(c, d.Expiry).Font("monospace").FontSize(11).SingleLine()
						case 1:
							ui.Text(c, d.Strike).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().SingleLine()
						case 2:
							tc := colorPos
							if strings.HasPrefix(d.Type, "P") {
								tc = colorNeg
							}
							ui.Text(c, d.Type).Font("monospace").FontSize(11).Bold().TextColor(tc).SingleLine()
						case 3:
							ui.Text(c, d.OI).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
						case 4:
							dc := colorPos
							if d.OIDelta < 0 {
								dc = colorNeg
							}
							ui.Text(c, d.OIDeltaStr).Font("monospace").FontFeatures("tnum").FontSize(11).Bold().TextColor(dc).SingleLine()
						case 5:
							ui.Text(c, d.OIPctStr).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
						}
					}).Height(230).Border(1, colorBorder).Radius(6)
				}).Grow(1).Basis(0)
			})

			// Bottom 2 columns: Day-over-Day Positioning Ladder + PCR History
			ui.Row(c).Gap(10).AlignItems(ui.Stretch).Children(func() {
				badge := "等待多日数据"
				if fl.DaysLabel != "" {
					badge = fl.DaysLabel
				}
				sectionCard(c, "跨日关键点位迁移阶梯 (Positioning Ladder)", badge, func() {
					if len(fl.Ladder) == 0 {
						ui.Text(c, fmt.Sprintf("当前已积累 %d 个交易日，跨日对比将在第 2 个交易日采集后自动解锁", fl.DaysCount)).
							FontSize(11.5).TextColor(colorMuted).Padding(8)
						return
					}
					ui.Column(c).Gap(4).Children(func() {
						for _, row := range fl.Ladder {
							dc := colorMuted
							if row.Tone == "pos" {
								dc = colorPos
							} else if row.Tone == "neg" {
								dc = colorNeg
							}
							ui.Row(c).Padding(6, 10).Radius(6).Background(ui.RGB(13, 17, 24)).
								AlignItems(ui.Center).Gap(10).Children(func() {
								ui.Text(c, row.Label).FontSize(11.5).TextColor(colorMuted).Width(110)
								ui.Text(c, row.Before).Font("monospace").FontFeatures("tnum").FontSize(11.5).TextColor(colorFaint)
								ui.Text(c, "→").FontSize(11).TextColor(colorFaint)
								ui.Text(c, row.After).Font("monospace").FontFeatures("tnum").FontSize(12).Bold().TextColor(colorText)
								ui.Spacer(c)
								ui.Text(c, row.Delta).Font("monospace").FontFeatures("tnum").FontSize(11.5).Bold().TextColor(dc)
							})
						}
					})
				}).Grow(1).Basis(0)

				sectionCard(c, "Put / Call Ratio 历史趋势", fmt.Sprintf("%d 个样本", len(fl.PCRHistory)), func() {
					a.drawPCRChart(c, fl.PCRHistory)
				}).Grow(1).Basis(0)
			})
		})
	})
}

func (a *DesktopApp) drawPCRChart(c *ui.Context, pts []PCRPoint) {
	if len(pts) < 2 {
		ui.Text(c, "需要至少 2 次快照以绘制 Put/Call Ratio 走势曲线").FontSize(11.5).TextColor(colorMuted).Padding(8)
		return
	}
	ui.Column(c).Gap(6).Children(func() {
		ui.Row(c).Gap(14).Children(func() {
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Box(c).Size(10, 3).Radius(1).Background(colorFlip)
				ui.Text(c, "成交量 PCR (Vol)").Font("monospace").FontSize(10.5).TextColor(colorMuted)
			})
			ui.Row(c).AlignItems(ui.Center).Gap(5).Children(func() {
				ui.Box(c).Size(10, 3).Radius(1).Background(colorSpot)
				ui.Text(c, "持仓量 PCR (OI)").Font("monospace").FontSize(10.5).TextColor(colorMuted)
			})
		})

		ui.Box(c).Height(200).Radius(6).Background(ui.RGB(11, 15, 22)).Border(1, colorHair).
			Draw(func(p *ui.Painter, r ui.Rect) {
				minY, maxY := 0.3, 1.8
				for _, pt := range pts {
					if pt.PCRVol != nil {
						minY = math.Min(minY, *pt.PCRVol)
						maxY = math.Max(maxY, *pt.PCRVol)
					}
					if pt.PCROI != nil {
						minY = math.Min(minY, *pt.PCROI)
						maxY = math.Max(maxY, *pt.PCROI)
					}
				}
				pad := (maxY - minY) * 0.15
				minY = math.Max(0, minY-pad)
				maxY += pad

				plot := ui.Rect{X: r.X + 42, Y: r.Y + 10, W: r.W - 54, H: r.H - 24}
				if plot.W <= 20 || plot.H <= 20 {
					return
				}
				for i := 0; i <= 3; i++ {
					frac := float32(i) / 3
					yy := plot.Y + plot.H*(1-frac)
					p.Line(plot.X, yy, plot.X+plot.W, yy, 1, colorHair)
					val := minY + float64(frac)*(maxY-minY)
					sp := ui.Span{Text: fmt.Sprintf("%.2f", val), Font: "monospace", Size: 9.5, Color: colorFaint}
					tw, th := p.MeasureText(0, sp)
					p.RichText(plot.X-tw-6, yy-th/2, 0, sp)
				}

				var volPath, oiPath ui.Path
				volStarted, oiStarted := false, false
				for i, pt := range pts {
					xx := plot.X + plot.W*float32(i)/float32(len(pts)-1)
					if pt.PCRVol != nil {
						yy := plot.Y + plot.H*(1-float32((*pt.PCRVol-minY)/(maxY-minY)))
						if !volStarted {
							volPath.MoveTo(xx, yy)
							volStarted = true
						} else {
							volPath.LineTo(xx, yy)
						}
					}
					if pt.PCROI != nil {
						yy := plot.Y + plot.H*(1-float32((*pt.PCROI-minY)/(maxY-minY)))
						if !oiStarted {
							oiPath.MoveTo(xx, yy)
							oiStarted = true
						} else {
							oiPath.LineTo(xx, yy)
						}
					}
				}
				if volStarted {
					p.StrokePath(&volPath, 2, colorFlip)
				}
				if oiStarted {
					p.StrokePath(&oiPath, 2, colorSpot)
				}
			})
	})
}

// ============================================================================
// 5. LOGS VIEW (系统日志与采集监控)
// ============================================================================
func (a *DesktopApp) renderLogsView(c *ui.Context) {
	st := a.state
	if st == nil {
		emptyCard(c, "正在读取采集器状态…", "")
		return
	}

	ui.Column(c).Grow(1).MinHeight(0).Padding(6, 14, 14, 14).Gap(10).Children(func() {
		a.renderUpdateSettings(c)
		sectionCard(c, "本地采集引擎与存储状态", "v"+st.Version, func() {
			ui.Row(c).Gap(16).Wrap().Children(func() {
				ui.Textf(c, "采集器状态: %s", st.CollectorState).Font("monospace").FontSize(12)
				ui.Text(c, "下次采集: "+st.NextCollection).Font("monospace").FontSize(12).TextColor(colorMuted)
				if ui.Button(c, "备份数据库").Disabled(a.collecting).Clicked() {
					a.statusMsg = "正在备份数据库…"
					go func() {
						var result struct {
							Path string `json:"path"`
						}
						err := a.api.postJSON("/api/backup", map[string]any{}, &result)
						a.update(func() {
							if err != nil {
								a.statusMsg = "备份失败: " + err.Error()
							} else {
								a.statusMsg = "备份已保存: " + result.Path
							}
						})
					}()
				}
				if st.CollectorError != "" {
					ui.Textf(c, "最近异常: %s", st.CollectorError).Font("monospace").FontSize(12).TextColor(colorErr)
				}
			})
		})

		runs := st.Runs
		sectionCard(c, "最近 25 次数据抓取日志 (Collection Runs)", fmt.Sprintf("%d 条记录", len(runs)), func() {
			cols := []ui.TableColumn{
				{Title: "时间", Width: 115},
				{Title: "标的", Width: 76},
				{Title: "数据源", Width: 96},
				{Title: "状态", Width: 76},
				{Title: "合约行数", Width: 86, Align: ui.End},
				{Title: "OI=0 占比", Width: 86, Align: ui.End},
				{Title: "错误信息", Width: 320},
			}
			ui.Table(c.Key("runs_tbl"), &a.runsTable, cols, len(runs), func(row, col int) {
				if row >= len(runs) {
					return
				}
				r := runs[row]
				switch col {
				case 0:
					ui.Text(c, r.StartedAt).Font("monospace").FontSize(11).TextColor(colorMuted).SingleLine()
				case 1:
					ui.Text(c, r.Ticker).Font("monospace").FontSize(11).Bold().SingleLine()
				case 2:
					ui.Text(c, r.Source).Font("monospace").FontSize(11).SingleLine()
				case 3:
					sc := colorPos
					if r.Status != "ok" && r.Status != "success" {
						sc = colorErr
					}
					ui.Text(c, r.Status).Font("monospace").FontSize(11).Bold().TextColor(sc).SingleLine()
				case 4:
					ui.Textf(c, "%d", r.Rows).Font("monospace").FontFeatures("tnum").FontSize(11).SingleLine()
				case 5:
					ui.Text(c, r.OIZero).Font("monospace").FontFeatures("tnum").FontSize(11).TextColor(colorMuted).SingleLine()
				case 6:
					ui.Text(c, r.Error).Font("monospace").FontSize(11).TextColor(colorErr).SingleLine()
				}
			}).Grow(1).Border(1, colorBorder).Radius(6)
		}).Grow(1)
	})
}
