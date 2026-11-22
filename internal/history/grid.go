package history

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// recordsGrid reports whether grid quality is kept for a device kind: the P1
// meter and kWh meters, which measure the mains (or a group) per phase.
func recordsGrid(kind homewizard.Kind) bool {
	return kind == homewizard.KindP1 || kind == homewizard.KindKWH
}

// phaseValue returns a per-phase value; single-phase kWh meters report
// phase 1 without the suffix (voltage_v, current_a, power_w).
func phaseValue(reading homewizard.Reading, phase int, prefix, unit string) (float64, bool) {
	n := strconv.Itoa(phase)
	if v, ok := reading.Value(prefix + "_l" + n + "_" + unit); ok {
		return v, true
	}
	if phase != 1 {
		return 0, false
	}
	if _, ok := reading.Value("voltage_l1_v"); ok {
		return 0, false // a phase-aware meter that left this value out
	}
	if _, single := reading.Value("voltage_v"); !single {
		return 0, false
	}
	return reading.Value(prefix + "_" + unit)
}

// gridCounterKeys are the meter's event counters, in GridCounters order.
var gridCounterKeys = []struct {
	key   string
	kind  string
	phase int
}{
	{"voltage_sag_l1_count", store.GridSag, 1},
	{"voltage_sag_l2_count", store.GridSag, 2},
	{"voltage_sag_l3_count", store.GridSag, 3},
	{"voltage_swell_l1_count", store.GridSwell, 1},
	{"voltage_swell_l2_count", store.GridSwell, 2},
	{"voltage_swell_l3_count", store.GridSwell, 3},
	{"any_power_fail_count", store.GridFail, 0},
	{"long_power_fail_count", store.GridLongFail, 0},
}

func counterSlot(c *store.GridCounters, i int) **float64 {
	switch {
	case i < 3:
		return &c.Sag[i]
	case i < 6:
		return &c.Swell[i-3]
	case i == 6:
		return &c.AnyFail
	default:
		return &c.LongFail
	}
}

type phaseAcc struct {
	vSum, vMin, vMax float64
	vN               int
	iMax             *float64
	pSum             float64
	pN               int
}

type gridAcc struct {
	phases   [3]phaseAcc
	counters store.GridCounters
	present  bool
}

func newGridAcc() gridAcc {
	var g gridAcc
	for i := range g.phases {
		g.phases[i].vMin, g.phases[i].vMax = math.Inf(1), math.Inf(-1)
	}
	return g
}

func (g *gridAcc) add(reading homewizard.Reading) {
	for i := range g.phases {
		p := &g.phases[i]
		if v, ok := phaseValue(reading, i+1, "voltage", "v"); ok {
			p.vSum += v
			p.vN++
			p.vMin, p.vMax = math.Min(p.vMin, v), math.Max(p.vMax, v)
			g.present = true
		}
		if a, ok := phaseValue(reading, i+1, "current", "a"); ok {
			a = math.Abs(a) // v2 reports current negative while exporting
			if p.iMax == nil || a > *p.iMax {
				p.iMax = &a
			}
			g.present = true
		}
		if w, ok := phaseValue(reading, i+1, "power", "w"); ok {
			p.pSum += w
			p.pN++
			g.present = true
		}
	}
	for i, c := range gridCounterKeys {
		if v, ok := reading.Value(c.key); ok {
			*counterSlot(&g.counters, i) = &v
			g.present = true
		}
	}
}

func (g *gridAcc) toMinute(deviceID string, minute, last time.Time) store.GridMinute {
	m := store.GridMinute{DeviceID: deviceID, Minute: minute, Last: last, Counters: g.counters}
	for i, p := range g.phases {
		var out store.GridPhase
		if p.vN > 0 {
			avg, mn, mx := p.vSum/float64(p.vN), p.vMin, p.vMax
			out.VAvg, out.VMin, out.VMax = &avg, &mn, &mx
		}
		out.IMax = p.iMax
		if p.pN > 0 {
			avg := p.pSum / float64(p.pN)
			out.PAvg = &avg
		}
		m.Phases[i] = out
	}
	return m
}

// gridBaseline is the last seen value of each event counter of a device.
type gridBaseline struct {
	values map[string]float64
	at     time.Time
}

// gridEvents compares the reading's event counters with the previous ones
// and stores an event for every counter that went up. The first reading of
// a device starts from the last stored counters, so failures while the
// gateway (or, in a power failure, the dongle) was down are found too.
func (r *Recorder) gridEvents(ctx context.Context, deviceID string, reading homewizard.Reading) {
	cur := map[string]float64{}
	for _, c := range gridCounterKeys {
		if v, ok := reading.Value(c.key); ok {
			cur[c.key] = v
		}
	}
	if len(cur) == 0 {
		return
	}
	r.mu.Lock()
	base, ok := r.gridBase[deviceID]
	r.mu.Unlock()
	if !ok {
		base = r.loadGridBaseline(ctx, deviceID)
	}
	var events []store.GridEvent
	for _, c := range gridCounterKeys {
		v, ok := cur[c.key]
		if !ok {
			continue
		}
		// a lower value means a new or reset meter: start over silently
		if prev, seen := base.values[c.key]; seen && v > prev && reading.Time.After(base.at) {
			events = append(events, store.GridEvent{DeviceID: deviceID, Kind: c.kind, Phase: c.phase,
				Count: int(math.Round(v - prev)), Since: base.at, At: reading.Time})
		}
	}
	next := gridBaseline{values: map[string]float64{}, at: reading.Time}
	for k, v := range base.values {
		next.values[k] = v
	}
	for k, v := range cur {
		next.values[k] = v
	}
	// Store the events before moving the baseline: if the write fails, the
	// old baseline stays, so the next reading finds the same increase again.
	if err := r.store.AddGridEvents(ctx, events); err != nil {
		r.log.Error("save grid events", "device", deviceID, "err", err)
		return
	}
	r.mu.Lock()
	if _, known := r.devices[deviceID]; known { // not removed in the meantime
		r.gridBase[deviceID] = next
	}
	r.mu.Unlock()
	for _, e := range events {
		r.log.Info("grid event", "device", deviceID, "kind", e.Kind, "phase", e.Phase, "count", e.Count, "since", e.Since)
	}
}

func (r *Recorder) loadGridBaseline(ctx context.Context, deviceID string) gridBaseline {
	base := gridBaseline{values: map[string]float64{}}
	m, err := r.store.LastGridCounters(ctx, deviceID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			r.log.Error("read grid counters", "device", deviceID, "err", err)
		}
		return base
	}
	for i, c := range gridCounterKeys {
		if v := *counterSlot(&m.Counters, i); v != nil {
			base.values[c.key] = *v
		}
	}
	base.at = m.Last
	return base
}

// GridPhasePoint is one phase in one bucket of a grid series.
type GridPhasePoint struct {
	VAvg *float64 `json:"v_avg,omitempty"`
	VMin *float64 `json:"v_min,omitempty"`
	VMax *float64 `json:"v_max,omitempty"`
	IMax *float64 `json:"i_max,omitempty"`
	PAvg *float64 `json:"p_avg,omitempty"`
}

// GridPoint is one bucket of a grid series; Phases has an entry per phase
// (L1..L3), nil when that phase has no data in the bucket.
type GridPoint struct {
	Time   time.Time         `json:"t"`
	Phases []*GridPhasePoint `json:"phases"`
}

// GridSeries returns per-phase voltage, current and power in [from, to),
// downsampled to at most maxPoints buckets of whole minutes, and the number
// of phases seen.
func (r *Recorder) GridSeries(ctx context.Context, deviceID string, from, to time.Time, maxPoints int) ([]GridPoint, time.Duration, int, error) {
	if maxPoints <= 0 {
		maxPoints = 720
	}
	minutes, err := r.store.GridMinutes(ctx, deviceID, from, to)
	if err != nil {
		return nil, 0, 0, err
	}
	r.mu.Lock()
	if acc := r.open[deviceID]; acc != nil && acc.grid.present && !acc.minute.Before(from) && acc.minute.Before(to) {
		minutes = append(minutes, acc.grid.toMinute(deviceID, acc.minute, acc.last))
	}
	r.mu.Unlock()

	step := time.Minute
	if span := to.Sub(from); span > time.Duration(maxPoints)*time.Minute {
		step = max((span / time.Duration(maxPoints)).Truncate(time.Minute), time.Minute)
	}
	type phaseSum struct {
		vSum, vMin, vMax, iMax, pSum float64
		vN, iN, pN                   int
	}
	phases := 0
	var out []GridPoint
	var sums [3]phaseSum
	var bucket time.Time
	open := false
	emit := func() {
		pt := GridPoint{Time: bucket, Phases: make([]*GridPhasePoint, 3)}
		for i, s := range sums {
			if s.vN == 0 && s.iN == 0 && s.pN == 0 {
				continue
			}
			p := &GridPhasePoint{}
			if s.vN > 0 {
				avg, mn, mx := s.vSum/float64(s.vN), s.vMin, s.vMax
				p.VAvg, p.VMin, p.VMax = &avg, &mn, &mx
			}
			if s.iN > 0 {
				mx := s.iMax
				p.IMax = &mx
			}
			if s.pN > 0 {
				avg := s.pSum / float64(s.pN)
				p.PAvg = &avg
			}
			pt.Phases[i] = p
			phases = max(phases, i+1)
		}
		out = append(out, pt)
	}
	for _, m := range minutes {
		if m.Phases[0].Empty() && m.Phases[1].Empty() && m.Phases[2].Empty() {
			continue // counters only
		}
		b := from.Add(m.Minute.Sub(from) / step * step)
		if !open || !b.Equal(bucket) {
			if open {
				emit()
			}
			bucket, open = b, true
			for i := range sums {
				sums[i] = phaseSum{vMin: math.Inf(1), vMax: math.Inf(-1)}
			}
		}
		for i, p := range m.Phases {
			s := &sums[i]
			if p.VAvg != nil {
				s.vSum += *p.VAvg
				s.vN++
				s.vMin, s.vMax = math.Min(s.vMin, *p.VMin), math.Max(s.vMax, *p.VMax)
			}
			if p.IMax != nil {
				s.iMax = math.Max(s.iMax, *p.IMax)
				s.iN++
			}
			if p.PAvg != nil {
				s.pSum += *p.PAvg
				s.pN++
			}
		}
	}
	if open {
		emit()
	}
	for i := range out {
		out[i].Phases = out[i].Phases[:phases]
	}
	return out, step, phases, nil
}

// GridEvents returns the grid events of a device with from <= at < to.
func (r *Recorder) GridEvents(ctx context.Context, deviceID string, from, to time.Time) ([]store.GridEvent, error) {
	return r.store.GridEvents(ctx, deviceID, from, to)
}
