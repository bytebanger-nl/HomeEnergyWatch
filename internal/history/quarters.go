// Package history turns live readings into minute aggregates and
// quarter-hour energy records, and answers history queries.
//
// Quarter hours are computed from cumulative meter readings (like the former
// Grip op Energie collector): the counter value at each quarter boundary is
// interpolated between the nearest readings around it, and the difference
// between two boundaries is the energy of that quarter. A quarter is
// "complete" when both boundaries could be interpolated between readings at
// most maxGap apart: the energy is then known, whatever is missing inside
// the quarter (Coverage tells how many of its minutes had readings). It is
// "partial" when a boundary had to be estimated, and "anomalous" when a
// counter went backwards.
package history

import (
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// QuarterDuration is the length of a quarter hour.
const QuarterDuration = 15 * time.Minute

// Quality values of a quarter.
const (
	QualityComplete  = "complete"
	QualityPartial   = "partial"
	QualityAnomalous = "anomalous"
)

// point is a counter value at a moment.
type point struct {
	at    time.Time
	value float64
}

// counterSelectors pick each counter from a minute aggregate.
var counterSelectors = []func(m *store.Minute) *float64{
	func(m *store.Minute) *float64 { return m.ImportKWh },
	func(m *store.Minute) *float64 { return m.ExportKWh },
	func(m *store.Minute) *float64 { return m.GasM3 },
	func(m *store.Minute) *float64 { return m.WaterM3 },
}

// lowSelectors pick the meter's low-tariff counters for import and export
// (the first two counterSelectors).
var lowSelectors = [2]func(m *store.Minute) *float64{
	func(m *store.Minute) *float64 { return m.ImportLowKWh },
	func(m *store.Minute) *float64 { return m.ExportLowKWh },
}

func points(minutes []store.Minute, sel func(m *store.Minute) *float64) []point {
	out := make([]point, 0, len(minutes))
	for i := range minutes {
		if v := sel(&minutes[i]); v != nil {
			out = append(out, point{at: minutes[i].Last, value: *v})
		}
	}
	return out
}

// valueAt returns the counter value at t. exact reports whether it was
// interpolated between readings at most maxGap apart (or read exactly at t).
func valueAt(pts []point, t time.Time, maxGap time.Duration) (value float64, at time.Time, exact, ok bool) {
	var before, after *point
	for i := range pts {
		p := &pts[i]
		if !p.at.After(t) {
			before = p
		} else {
			after = p
			break
		}
	}
	if before != nil && before.at.Equal(t) {
		return before.value, t, true, true
	}
	if before != nil && after != nil && after.at.Sub(before.at) <= maxGap {
		frac := float64(t.Sub(before.at)) / float64(after.at.Sub(before.at))
		return before.value + frac*(after.value-before.value), t, true, true
	}
	return 0, time.Time{}, false, false
}

// boundary returns the counter value at a quarter boundary, falling back to
// the first (start) or last (end) reading inside the quarter.
func boundary(pts []point, t, other time.Time, isStart bool, maxGap time.Duration) (float64, time.Time, bool, bool) {
	if v, at, exact, ok := valueAt(pts, t, maxGap); ok {
		return v, at, exact, true
	}
	if isStart {
		for _, p := range pts {
			if p.at.After(t) && p.at.Before(other) {
				return p.value, p.at, false, true
			}
		}
		return 0, time.Time{}, false, false
	}
	for i := len(pts) - 1; i >= 0; i-- {
		p := pts[i]
		if p.at.Before(t) && p.at.After(other) {
			return p.value, p.at, false, true
		}
	}
	return 0, time.Time{}, false, false
}

// decreases reports whether the counter went down anywhere between from and
// to (a meter reset or replacement).
func decreases(pts []point, from, to time.Time) bool {
	var prev *point
	for i := range pts {
		p := &pts[i]
		if p.at.Before(from) {
			prev = p
			continue
		}
		if prev != nil && p.value < prev.value-1e-9 {
			return true
		}
		if p.at.After(to) {
			break
		}
		prev = p
	}
	return false
}

// lowPart returns how much of total (a counter's energy in the quarter
// [start, end), between the boundary moments at0 and at1) the meter counted
// at its low tariff, from the low-tariff counter pts. nil when that is not
// known for exactly the same moments, e.g. in the quarter in which the
// gateway first recorded the counter, or when it went backwards.
func lowPart(pts []point, start, end, at0, at1 time.Time, total float64, maxGap time.Duration) *float64 {
	if len(pts) == 0 {
		return nil
	}
	v0, l0, _, ok0 := boundary(pts, start, end, true, maxGap)
	v1, l1, _, ok1 := boundary(pts, end, start, false, maxGap)
	if !ok0 || !ok1 || !l0.Equal(at0) || !l1.Equal(at1) {
		return nil
	}
	d := v1 - v0
	if d < -1e-9 || decreases(pts, l0, l1) {
		return nil
	}
	d = min(max(d, 0), total)
	return &d
}

// ComputeQuarters computes every quarter that starts in [from, to) from the
// given minutes. minutes should include some margin (maxGap) around the
// range so boundaries can be interpolated. Quarters without any counter data
// are omitted.
func ComputeQuarters(deviceID string, minutes []store.Minute, from, to time.Time, maxGap time.Duration) []store.Quarter {
	series := make([][]point, len(counterSelectors))
	for i, sel := range counterSelectors {
		series[i] = points(minutes, sel)
	}
	var lowSeries [2][]point
	for i, sel := range lowSelectors {
		lowSeries[i] = points(minutes, sel)
	}
	minuteSet := make(map[int64]bool, len(minutes))
	for _, m := range minutes {
		if m.Samples > 0 {
			minuteSet[m.Minute.Unix()] = true
		}
	}

	var out []store.Quarter
	for start := from.Truncate(QuarterDuration); start.Before(to); start = start.Add(QuarterDuration) {
		end := start.Add(QuarterDuration)
		q := store.Quarter{DeviceID: deviceID, Start: start}
		results := []**float64{&q.ImportKWh, &q.ExportKWh, &q.GasM3, &q.WaterM3}
		lowResults := [2]**float64{&q.ImportLowKWh, &q.ExportLowKWh}
		any, allExact, anomalous := false, true, false
		for i, pts := range series {
			if len(pts) == 0 {
				continue
			}
			v0, at0, exact0, ok0 := boundary(pts, start, end, true, maxGap)
			v1, at1, exact1, ok1 := boundary(pts, end, start, false, maxGap)
			if !ok0 || !ok1 || !at1.After(at0) {
				continue
			}
			delta := v1 - v0
			if delta < -1e-9 || decreases(pts, at0, at1) {
				anomalous = true
				continue
			}
			if delta < 0 {
				delta = 0
			}
			d := delta
			*results[i] = &d
			any = true
			allExact = allExact && exact0 && exact1
			if i < len(lowSeries) {
				*lowResults[i] = lowPart(lowSeries[i], start, end, at0, at1, d, maxGap)
			}
		}
		if !any && !anomalous {
			continue
		}
		covered := 0
		for m := start; m.Before(end); m = m.Add(time.Minute) {
			if minuteSet[m.Unix()] {
				covered++
			}
		}
		q.Coverage = float64(covered) / QuarterDuration.Minutes()
		switch {
		case anomalous:
			q.Quality = QualityAnomalous
		case allExact:
			// a gap inside the quarter does not matter: the energy is the
			// difference of the two boundaries, and both are known
			q.Quality = QualityComplete
		default:
			q.Quality = QualityPartial
		}
		out = append(out, q)
	}
	return out
}
