package report

import (
	"math"
)

// series is one set of bars; negative values are drawn below the axis.
type series struct {
	name   string
	color  rgb
	values []float64
}

// barChart is a stacked bar chart over the full content width.
type barChart struct {
	labels []string
	series []series
	height float64
	unit   string
}

// niceStep returns a round step for about n ticks over span.
func niceStep(span float64, n int) float64 {
	if span <= 0 {
		return 1
	}
	raw := span / float64(n)
	mag := math.Pow(10, math.Floor(math.Log10(raw)))
	for _, m := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= m*mag {
			return m * mag
		}
	}
	return 10 * mag
}

// axis returns the rounded range and step for values lo..hi (lo ≤ 0 ≤ hi).
func axis(lo, hi float64) (float64, float64, float64) {
	if hi == lo {
		hi = lo + 1
	}
	step := niceStep(hi-lo, 5)
	lo = math.Floor(lo/step) * step
	hi = math.Ceil(hi/step) * step
	if hi == 0 && lo == 0 {
		hi = step
	}
	return lo, hi, step
}

// tickText formats a tick with the decimals the step needs (2,5 → 1).
func tickText(v, step float64) string {
	dec := 0
	for dec < 6 && math.Abs(step*math.Pow(10, float64(dec))-math.Round(step*math.Pow(10, float64(dec)))) > 1e-9 {
		dec++
	}
	return num(v, dec)
}

// legend draws the series names right-aligned above a chart at y.
func (d *doc) legend(y float64, names []string, colors []rgb) {
	d.font("", 7.5)
	x := float64(pageW - marginX)
	for i := len(names) - 1; i >= 0; i-- {
		w := d.GetStringWidth(names[i])
		x -= w
		d.color(cText2)
		d.Text(x, y+2.6, names[i])
		x -= 4
		d.fill(colors[i])
		d.Rect(x, y, 2.8, 2.8, "F")
		x -= 4
	}
}

// plotFrame draws the horizontal grid and y labels; returns a function that
// maps a value to y.
func (d *doc) plotFrame(x, y, w, h, lo, hi, step float64) func(float64) float64 {
	yOf := func(v float64) float64 { return y + h - (v-lo)/(hi-lo)*h }
	d.font("", 7)
	d.SetLineWidth(0.15)
	for v := lo; v <= hi+step/2; v += step {
		yy := yOf(v)
		d.draw(cGrid)
		if math.Abs(v) < step/1000 {
			d.draw(cAxis)
		}
		d.Line(x, yy, x+w, yy)
		d.color(cMuted)
		txt := tickText(v, step)
		d.Text(x-1.5-d.GetStringWidth(txt), yy+1.1, txt)
	}
	return yOf
}

func (d *doc) barChart(c barChart) {
	n := len(c.labels)
	if n == 0 {
		return
	}
	var names []string
	var colors []rgb
	lo, hi := 0.0, 0.0
	for i := range n {
		pos, neg := 0.0, 0.0
		for _, s := range c.series {
			if v := s.values[i]; v > 0 {
				pos += v
			} else {
				neg += v
			}
		}
		hi, lo = math.Max(hi, pos), math.Min(lo, neg)
	}
	for _, s := range c.series {
		nonzero := false
		for _, v := range s.values {
			nonzero = nonzero || v != 0
		}
		if nonzero {
			names = append(names, s.name)
			colors = append(colors, s.color)
		}
	}
	lo, hi, step := axis(lo, hi)
	const legendH, labelH, left = 5.0, 5.0, 12.0
	d.need(legendH + c.height + labelH)
	top := d.GetY()
	if len(names) > 1 {
		d.legend(top, names, colors)
	}
	x, y, w, h := marginX+left, top+legendH, contentW-left, c.height-legendH-labelH
	yOf := d.plotFrame(x, y, w, h, lo, hi, step)
	slot := w / float64(n)
	bw := slot * 0.7
	if bw > 9 {
		bw = 9
	}
	every := int(math.Ceil(float64(n) / (w / 9))) // a label at least every 9 mm
	for i := range n {
		bx := x + slot*float64(i) + (slot-bw)/2
		pos, neg := 0.0, 0.0
		for _, s := range c.series {
			v := s.values[i]
			if v == 0 {
				continue
			}
			d.fill(s.color)
			if v > 0 {
				d.Rect(bx, yOf(pos+v), bw, yOf(pos)-yOf(pos+v), "F")
				pos += v
			} else {
				d.Rect(bx, yOf(neg), bw, yOf(neg+v)-yOf(neg), "F")
				neg += v
			}
		}
		if i%every == 0 {
			d.font("", 6.5)
			d.color(cMuted)
			lw := d.GetStringWidth(c.labels[i])
			d.Text(x+slot*float64(i)+slot/2-lw/2, y+h+3.6, c.labels[i])
		}
	}
	d.SetY(top + c.height + 1)
}

// lineSeries is one line over the 96 quarters of a day.
type lineSeries struct {
	name   string
	color  rgb
	values []float64
}

// lineChart draws day profiles: x is the time of day 00:00-24:00.
func (d *doc) lineChart(height float64, unit string, ls []lineSeries) {
	if len(ls) == 0 {
		return
	}
	lo, hi := 0.0, 0.0
	for _, s := range ls {
		for _, v := range s.values {
			lo, hi = math.Min(lo, v), math.Max(hi, v)
		}
	}
	lo, hi, step := axis(lo, hi)
	const legendH, labelH, left = 5.0, 5.0, 12.0
	d.need(height)
	top := d.GetY()
	names := make([]string, len(ls))
	colors := make([]rgb, len(ls))
	for i, s := range ls {
		names[i], colors[i] = s.name, s.color
	}
	d.legend(top, names, colors)
	x, y, w, h := marginX+left, top+legendH, contentW-left, height-legendH-labelH
	yOf := d.plotFrame(x, y, w, h, lo, hi, step)
	d.font("", 6.5)
	d.color(cMuted)
	for hr := 0; hr <= 24; hr += 3 {
		xx := x + w*float64(hr)/24
		txt := num(float64(hr), 0) + ":00"
		d.Text(xx-d.GetStringWidth(txt)/2, y+h+3.6, txt)
	}
	d.SetLineWidth(0.45)
	for _, s := range ls {
		d.draw(s.color)
		n := len(s.values)
		for i := 1; i < n; i++ {
			x0 := x + w*(float64(i-1)+0.5)/float64(n)
			x1 := x + w*(float64(i)+0.5)/float64(n)
			d.Line(x0, yOf(s.values[i-1]), x1, yOf(s.values[i]))
		}
	}
	d.SetLineWidth(0.2)
	d.SetY(top + height + 1)
}
