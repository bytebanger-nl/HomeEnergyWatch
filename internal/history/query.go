package history

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// RatePoint is one point of a rate series (W, or L/min for water).
type RatePoint struct {
	Time time.Time `json:"t"`
	Avg  float64   `json:"avg"`
	Min  float64   `json:"min"`
	Max  float64   `json:"max"`
	Soc  *float64  `json:"soc,omitempty"`
}

// RateSeries returns the rate in [from, to), downsampled to at most
// maxPoints buckets (of whole minutes).
func (r *Recorder) RateSeries(ctx context.Context, deviceID string, from, to time.Time, maxPoints int) ([]RatePoint, time.Duration, error) {
	if maxPoints <= 0 {
		maxPoints = 720
	}
	minutes, err := r.store.Minutes(ctx, deviceID, from, to)
	if err != nil {
		return nil, 0, err
	}
	// include the open (not yet flushed) minute so charts reach "now"
	r.mu.Lock()
	kind := r.devices[deviceID]
	if acc := r.open[deviceID]; acc != nil && !acc.minute.Before(from) && acc.minute.Before(to) {
		minutes = append(minutes, acc.toMinute(deviceID))
	}
	r.mu.Unlock()
	// periods without minute data (e.g. imported history) use the average
	// of each quarter hour
	if fill, err := r.quarterMinutes(ctx, deviceID, kind, from, to, minutes); err == nil && len(fill) > 0 {
		minutes = mergeMinutes(minutes, fill)
	}

	step := time.Minute
	if span := to.Sub(from); span > time.Duration(maxPoints)*time.Minute {
		step = (span / time.Duration(maxPoints)).Truncate(time.Minute)
		if step < time.Minute {
			step = time.Minute
		}
	}
	var out []RatePoint
	var cur *RatePoint
	var weight float64
	for _, m := range minutes {
		if m.RateAvg == nil && m.SocPct == nil {
			continue
		}
		bucket := from.Add(m.Minute.Sub(from) / step * step)
		if cur == nil || !cur.Time.Equal(bucket) {
			if cur != nil {
				out = append(out, finish(cur, weight))
			}
			cur = &RatePoint{Time: bucket, Min: math.Inf(1), Max: math.Inf(-1)}
			weight = 0
		}
		if m.RateAvg != nil {
			w := float64(m.Samples)
			cur.Avg += *m.RateAvg * w
			weight += w
			cur.Min = math.Min(cur.Min, *m.RateMin)
			cur.Max = math.Max(cur.Max, *m.RateMax)
		}
		if m.SocPct != nil {
			s := *m.SocPct
			cur.Soc = &s
		}
	}
	if cur != nil {
		out = append(out, finish(cur, weight))
	}
	return out, step, nil
}

// quarterMinutes turns quarter hours that have no minute data into synthetic
// minutes carrying the quarter's average rate.
func (r *Recorder) quarterMinutes(ctx context.Context, deviceID string, kind homewizard.Kind, from, to time.Time, have []store.Minute) ([]store.Minute, error) {
	quarters, err := r.store.Quarters(ctx, deviceID, from.Truncate(QuarterDuration), to)
	if err != nil || len(quarters) == 0 {
		return nil, err
	}
	covered := map[int64]bool{}
	for _, m := range have {
		covered[m.Minute.Truncate(QuarterDuration).Unix()] = true
	}
	var out []store.Minute
	for _, q := range quarters {
		if covered[q.Start.Unix()] {
			continue
		}
		var rate float64
		switch {
		case kind == homewizard.KindWater || (q.WaterM3 != nil && q.ImportKWh == nil):
			if q.WaterM3 == nil {
				continue
			}
			rate = *q.WaterM3 * 1000 / QuarterDuration.Minutes() // L/min
		case q.ImportKWh != nil || q.ExportKWh != nil:
			var imp, exp float64
			if q.ImportKWh != nil {
				imp = *q.ImportKWh
			}
			if q.ExportKWh != nil {
				exp = *q.ExportKWh
			}
			rate = (imp - exp) * 1000 / QuarterDuration.Hours() // W
		default:
			continue
		}
		for i := 0; i < int(QuarterDuration.Minutes()); i++ {
			t := q.Start.Add(time.Duration(i) * time.Minute)
			if t.Before(from) || !t.Before(to) {
				continue
			}
			v := rate
			out = append(out, store.Minute{DeviceID: deviceID, Minute: t, Samples: 1, RateAvg: &v, RateMin: &v, RateMax: &v, Last: t})
		}
	}
	return out, nil
}

func mergeMinutes(a, b []store.Minute) []store.Minute {
	out := make([]store.Minute, 0, len(a)+len(b))
	i, j := 0, 0
	for i < len(a) || j < len(b) {
		if j >= len(b) || (i < len(a) && a[i].Minute.Before(b[j].Minute)) {
			out = append(out, a[i])
			i++
		} else {
			out = append(out, b[j])
			j++
		}
	}
	return out
}

// Import stores imported quarter hours without overwriting existing values.
func (r *Recorder) Import(ctx context.Context, quarters []store.Quarter, dryRun bool) (store.ImportStats, error) {
	st, err := r.store.ImportQuarters(ctx, quarters, dryRun)
	if err == nil && !dryRun && st.Added+st.Replaced > 0 {
		r.quartersChanged(quarters)
	}
	return st, err
}

// quartersChanged reports stored quarter hours to OnQuarters, per device.
func (r *Recorder) quartersChanged(quarters []store.Quarter) {
	if r.OnQuarters == nil {
		return
	}
	first := map[string]time.Time{}
	for _, q := range quarters {
		if t, ok := first[q.DeviceID]; !ok || q.Start.Before(t) {
			first[q.DeviceID] = q.Start
		}
	}
	for id, t := range first {
		r.OnQuarters(id, t)
	}
}

func finish(p *RatePoint, weight float64) RatePoint {
	if weight > 0 {
		p.Avg /= weight
	} else {
		p.Avg, p.Min, p.Max = 0, 0, 0
	}
	if math.IsInf(p.Min, 0) {
		p.Min = p.Avg
	}
	if math.IsInf(p.Max, 0) {
		p.Max = p.Avg
	}
	return *p
}

// EnergyBucket is the energy of one period.
type EnergyBucket struct {
	Start     time.Time `json:"start"`
	ImportKWh *float64  `json:"import_kwh,omitempty"`
	ExportKWh *float64  `json:"export_kwh,omitempty"`
	GasM3     *float64  `json:"gas_m3,omitempty"`
	WaterM3   *float64  `json:"water_m3,omitempty"`
	// Complete is false when any quarter in the bucket was partial,
	// anomalous or missing.
	Complete bool `json:"complete"`
}

// Bucket sizes for energy history.
const (
	BucketQuarter = "quarter"
	BucketHour    = "hour"
	BucketDay     = "day"
	BucketMonth   = "month"
)

// Energy sums quarter-hour records into buckets in the recorder's time zone.
func (r *Recorder) Energy(ctx context.Context, deviceID string, from, to time.Time, bucket string) ([]EnergyBucket, error) {
	next, err := bucketFunc(bucket, r.loc)
	if err != nil {
		return nil, err
	}
	quarters, err := r.store.Quarters(ctx, deviceID, from, to)
	if err != nil {
		return nil, err
	}
	var out []EnergyBucket
	now := time.Now()
	start := floorBucket(from, bucket, r.loc)
	qi := 0
	for start.Before(to) {
		end := next(start)
		b := EnergyBucket{Start: start, Complete: true}
		expected := int(end.Sub(start) / QuarterDuration)
		seen := 0
		for qi < len(quarters) && quarters[qi].Start.Before(end) {
			q := quarters[qi]
			qi++
			if q.Start.Before(start) {
				continue
			}
			seen++
			if q.Quality != QualityComplete {
				b.Complete = false
			}
			add(&b.ImportKWh, q.ImportKWh)
			add(&b.ExportKWh, q.ExportKWh)
			add(&b.GasM3, q.GasM3)
			add(&b.WaterM3, q.WaterM3)
		}
		if seen < expected {
			b.Complete = false
		}
		// keep empty periods up to now, so a chart's time axis stays evenly spaced
		if seen > 0 || start.Before(now) {
			out = append(out, b)
		}
		start = end
	}
	return out, nil
}

func add(dst **float64, v *float64) {
	if v == nil {
		return
	}
	if *dst == nil {
		z := 0.0
		*dst = &z
	}
	**dst += *v
}

func bucketFunc(bucket string, loc *time.Location) (func(time.Time) time.Time, error) {
	switch bucket {
	case BucketQuarter:
		return func(t time.Time) time.Time { return t.Add(QuarterDuration) }, nil
	case BucketHour:
		return func(t time.Time) time.Time { return t.Add(time.Hour) }, nil
	case BucketDay:
		return func(t time.Time) time.Time {
			lt := t.In(loc)
			return time.Date(lt.Year(), lt.Month(), lt.Day()+1, 0, 0, 0, 0, loc).UTC()
		}, nil
	case BucketMonth:
		return func(t time.Time) time.Time {
			lt := t.In(loc)
			return time.Date(lt.Year(), lt.Month()+1, 1, 0, 0, 0, 0, loc).UTC()
		}, nil
	default:
		return nil, fmt.Errorf("unknown bucket %q", bucket)
	}
}

func floorBucket(t time.Time, bucket string, loc *time.Location) time.Time {
	switch bucket {
	case BucketQuarter:
		return t.Truncate(QuarterDuration).UTC()
	case BucketHour:
		return t.Truncate(time.Hour).UTC()
	case BucketDay:
		return startOfDay(t, loc)
	case BucketMonth:
		lt := t.In(loc)
		return time.Date(lt.Year(), lt.Month(), 1, 0, 0, 0, 0, loc).UTC()
	}
	return t
}
