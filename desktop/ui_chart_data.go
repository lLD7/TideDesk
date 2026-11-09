package main

import (
	"fmt"
	"math"

	"gammagrid.local/desktop/internal/viewmodel"

	"github.com/egoist/mygo/ui"
)

type pressurePlot struct {
	response   *viewmodel.WorkbenchResponse
	maxBar     float64
	maxRunning float64
	running    []float64
	points     [][2]float32
}

// View responses are immutable, so these totals only change with the response.
func (a *DesktopApp) pressurePlotFor(wb *viewmodel.WorkbenchResponse) *pressurePlot {
	p := &a.pressurePlot
	if p.response == wb {
		return p
	}
	n := len(wb.Strikes)
	*p = pressurePlot{
		response: wb, maxBar: 1, maxRunning: 1,
		running: make([]float64, n),
		points:  p.points[:0],
	}
	for _, profile := range [][]float64{wb.Profile, wb.CallProfile, wb.PutProfile} {
		for _, v := range profile {
			if magnitude := math.Abs(v); magnitude > p.maxBar {
				p.maxBar = magnitude
			}
		}
	}
	reverse := n > 1 && wb.Strikes[0] > wb.Strikes[n-1]
	var sum float64
	for i := range n {
		j := i
		if reverse {
			j = n - 1 - i
		}
		sum += wb.Profile[j]
		p.running[j] = sum
		if magnitude := math.Abs(sum); magnitude > p.maxRunning {
			p.maxRunning = magnitude
		}
	}
	return p
}

func visiblePlotRows(clip ui.Rect, top, height float32, count int) (first, end int) {
	first = int(math.Floor(float64((clip.Y - top) / height)))
	end = int(math.Ceil(float64((clip.Y + clip.H - top) / height)))
	return max(0, min(count, first)), max(0, min(count, end))
}

const plotTickCount = 6

type intradayPlot struct {
	first                  *viewmodel.IntradayBar
	count                  int
	maxVolume              float64
	minTide, maxTide       float64
	minSpot, maxSpot       float64
	volumeTicks, tideTicks [plotTickCount]string
	spotTicks              [plotTickCount]string
	volumeSpot             [][2]float32
	weekly                 [4][][2]float32
}

func (a *DesktopApp) intradayPlotFor(bars []viewmodel.IntradayBar) *intradayPlot {
	p := &a.intradayPlot
	if p.first == &bars[0] && p.count == len(bars) {
		return p
	}
	*p = intradayPlot{
		first: &bars[0], count: len(bars), maxVolume: 1,
		minSpot: math.Inf(1), maxSpot: math.Inf(-1),
		volumeSpot: p.volumeSpot[:0], weekly: p.weekly,
	}
	for _, b := range bars {
		if b.TotalVol > p.maxVolume {
			p.maxVolume = b.TotalVol
		}
		for _, v := range [...]float64{b.CumWeeklyCall, -b.CumWeeklyPut, b.CumWeeklyNet, b.WeeklyNetPrem} {
			if v < p.minTide {
				p.minTide = v
			}
			if v > p.maxTide {
				p.maxTide = v
			}
		}
		if b.Spot > 0 {
			p.minSpot = min(p.minSpot, b.Spot)
			p.maxSpot = max(p.maxSpot, b.Spot)
		}
	}
	p.maxVolume *= 1.12
	if span := p.maxTide - p.minTide; span < 1 {
		p.maxTide += 1000
		p.minTide -= 1000
	} else {
		p.maxTide += span * 0.12
		p.minTide -= span * 0.12
	}
	if math.IsInf(p.minSpot, 1) {
		p.minSpot, p.maxSpot = 99, 101
	} else {
		pad := (p.maxSpot - p.minSpot) * 0.14
		if math.Abs(p.maxSpot-p.minSpot) < 1e-4 {
			pad = max(1, p.minSpot*0.005)
		}
		p.minSpot -= pad
		p.maxSpot += pad
	}
	for i := range plotTickCount {
		fraction := float64(float32(i) / float32(plotTickCount-1))
		p.volumeTicks[i] = fmtVolCompact(p.maxVolume * fraction)
		p.tideTicks[i] = fmtDollarCompact(p.minTide+(p.maxTide-p.minTide)*fraction, true)
		p.spotTicks[i] = fmt.Sprintf("$%.2f", p.minSpot+(p.maxSpot-p.minSpot)*fraction)
	}
	p.volumeTicks[0] = "0"
	return p
}
