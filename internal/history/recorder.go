package history

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Recorder aggregates readings per minute, writes them to the store and
// maintains quarter-hour records.
type Recorder struct {
	store     *store.Store
	log       *slog.Logger
	maxGap    time.Duration
	retention time.Duration
	loc       *time.Location

	// OnQuarters is called with the device and the start of the earliest
	// quarter hour after quarter hours were stored or imported.
	OnQuarters func(deviceID string, from time.Time)

	mu       sync.Mutex
	open     map[string]*accumulator
	devices  map[string]homewizard.Kind
	days     map[string]dayStart
	gridBase map[string]gridBaseline
}

type accumulator struct {
	minute   time.Time
	samples  int
	rateSum  float64
	rateN    int
	rateMin  float64
	rateMax  float64
	last     time.Time
	counters [4]*float64
	low      [2]*float64 // the meter's low-tariff import and export counters
	soc      *float64
	grid     gridAcc
}

type dayStart struct {
	day    time.Time
	values map[string]float64
}

// Config configures a Recorder.
type Config struct {
	// MaxGap is the largest distance between two readings that may be
	// interpolated across a quarter boundary.
	MaxGap time.Duration
	// Retention of minute aggregates. Quarter hours are kept forever.
	Retention time.Duration
	// Location used for "today" and day/month buckets.
	Location *time.Location
}

// NewRecorder creates a recorder.
func NewRecorder(st *store.Store, log *slog.Logger, cfg Config) *Recorder {
	if cfg.MaxGap <= 0 {
		cfg.MaxGap = 5 * time.Minute
	}
	if cfg.Location == nil {
		cfg.Location = time.Local
	}
	return &Recorder{
		store: st, log: log, maxGap: cfg.MaxGap, retention: cfg.Retention, loc: cfg.Location,
		open: map[string]*accumulator{}, devices: map[string]homewizard.Kind{}, days: map[string]dayStart{},
		gridBase: map[string]gridBaseline{},
	}
}

// Location returns the configured time zone.
func (r *Recorder) Location() *time.Location { return r.loc }

// counterKeys maps reading values to accumulator counter slots.
var counterKeys = [4]string{"energy_import_kwh", "energy_export_kwh", "gas_m3", "water_m3"}

// RateKey returns the reading value that is the live "rate" for a device kind.
func RateKey(kind homewizard.Kind) string {
	if kind == homewizard.KindWater {
		return "water_lpm"
	}
	return "power_w"
}

// Add records one reading. It flushes the previous minute when a new minute
// starts.
func (r *Recorder) Add(ctx context.Context, deviceID string, kind homewizard.Kind, reading homewizard.Reading) {
	minute := reading.Time.Truncate(time.Minute)
	r.mu.Lock()
	r.devices[deviceID] = kind
	acc := r.open[deviceID]
	var flush *store.Minute
	var flushGrid *store.GridMinute
	if acc != nil && !acc.minute.Equal(minute) {
		if minute.Before(acc.minute) {
			r.mu.Unlock()
			return // clock went backwards; ignore
		}
		m := acc.toMinute(deviceID)
		flush = &m
		if acc.grid.present {
			g := acc.grid.toMinute(deviceID, acc.minute, acc.last)
			flushGrid = &g
		}
		acc = nil
	}
	if acc == nil {
		acc = &accumulator{minute: minute, rateMin: math.Inf(1), rateMax: math.Inf(-1), grid: newGridAcc()}
		r.open[deviceID] = acc
	}
	acc.samples++
	acc.last = reading.Time
	if v, ok := reading.Value(RateKey(kind)); ok {
		acc.rateSum += v
		acc.rateN++
		acc.rateMin = math.Min(acc.rateMin, v)
		acc.rateMax = math.Max(acc.rateMax, v)
	}
	for i, key := range counterKeys {
		if v, ok := reading.Value(key); ok {
			val := v
			acc.counters[i] = &val
		}
	}
	if kind == homewizard.KindP1 {
		for i, dir := range [2]string{"import", "export"} {
			if v, ok := reading.LowTariffKWh(dir); ok {
				acc.low[i] = &v
			}
		}
	}
	if v, ok := reading.Value("state_of_charge_pct"); ok {
		acc.soc = &v
	}
	grid := recordsGrid(kind)
	if grid {
		acc.grid.add(reading)
	}
	r.mu.Unlock()

	if flush != nil {
		if err := r.store.SaveMinute(ctx, *flush); err != nil {
			r.log.Error("save minute", "device", deviceID, "err", err)
		}
	}
	if flushGrid != nil {
		if err := r.store.SaveGridMinute(ctx, *flushGrid); err != nil {
			r.log.Error("save grid minute", "device", deviceID, "err", err)
		}
	}
	if grid {
		r.gridEvents(ctx, deviceID, reading)
	}
}

func (a *accumulator) toMinute(deviceID string) store.Minute {
	m := store.Minute{DeviceID: deviceID, Minute: a.minute, Samples: a.samples, Last: a.last,
		ImportKWh: a.counters[0], ExportKWh: a.counters[1], GasM3: a.counters[2], WaterM3: a.counters[3],
		SocPct: a.soc, ImportLowKWh: a.low[0], ExportLowKWh: a.low[1]}
	if a.rateN > 0 {
		avg, mn, mx := a.rateSum/float64(a.rateN), a.rateMin, a.rateMax
		m.RateAvg, m.RateMin, m.RateMax = &avg, &mn, &mx
	}
	return m
}

// Flush writes all open minutes (used on shutdown).
func (r *Recorder) Flush(ctx context.Context) {
	r.mu.Lock()
	var pending []store.Minute
	var grid []store.GridMinute
	for id, acc := range r.open {
		pending = append(pending, acc.toMinute(id))
		if acc.grid.present {
			grid = append(grid, acc.grid.toMinute(id, acc.minute, acc.last))
		}
	}
	r.mu.Unlock()
	for _, m := range pending {
		if err := r.store.SaveMinute(ctx, m); err != nil {
			r.log.Error("flush minute", "device", m.DeviceID, "err", err)
		}
	}
	for _, m := range grid {
		if err := r.store.SaveGridMinute(ctx, m); err != nil {
			r.log.Error("flush grid minute", "device", m.DeviceID, "err", err)
		}
	}
}

// Forget drops in-memory state of a removed device.
func (r *Recorder) Forget(deviceID string) {
	r.mu.Lock()
	delete(r.open, deviceID)
	delete(r.devices, deviceID)
	delete(r.days, deviceID)
	delete(r.gridBase, deviceID)
	r.mu.Unlock()
}

// recompute is how far back Run recomputes the quarter hours every minute:
// a quarter is first stored once its end is MaxGap in the past, but a value
// that comes later (a gas meter that reports every 5 minutes or every hour,
// a minute written late) can still complete or add to it within this window.
const recompute = time.Hour

// Settle is how long after its end a quarter hour may still change: Run
// recomputes the quarters of the last hour every minute. Until then a
// quarter that is missing or estimated is not final (the costs do not count
// it as a gap yet).
func (r *Recorder) Settle() time.Duration {
	return max(recompute, r.maxGap) + time.Minute
}

// Run computes quarter hours every minute and prunes old minutes daily,
// until ctx is cancelled. On start it recomputes the last two days, and every
// older partial quarter that still has minute data.
func (r *Recorder) Run(ctx context.Context, deviceIDs func() []string) {
	r.aggregate(ctx, deviceIDs(), 48*time.Hour)
	r.repairPartial(ctx, deviceIDs())
	r.prune(ctx)
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	lastPrune := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			r.aggregate(ctx, deviceIDs(), max(recompute, r.maxGap))
			if time.Since(lastPrune) > 24*time.Hour {
				r.prune(ctx)
				lastPrune = time.Now()
			}
		}
	}
}

func (r *Recorder) prune(ctx context.Context) {
	if r.retention <= 0 {
		return
	}
	n, err := r.store.PruneMinutes(ctx, time.Now().Add(-r.retention))
	if err != nil {
		r.log.Error("prune minutes", "err", err)
	} else if n > 0 {
		r.log.Info("pruned minute history", "rows", n)
	}
}

// aggregate recomputes all closed quarters in the last lookback period.
func (r *Recorder) aggregate(ctx context.Context, ids []string, lookback time.Duration) {
	now := time.Now().UTC()
	to := now.Add(-r.maxGap).Truncate(QuarterDuration) // quarters whose end is safely in the past
	from := now.Add(-lookback).Truncate(QuarterDuration)
	if !to.After(from) {
		return
	}
	for _, id := range ids {
		minutes, err := r.store.Minutes(ctx, id, from.Add(-r.maxGap), to.Add(r.maxGap+time.Minute))
		if err != nil {
			r.log.Error("read minutes", "device", id, "err", err)
			continue
		}
		quarters := ComputeQuarters(id, minutes, from, to, r.maxGap)
		if err := r.store.SaveQuarters(ctx, quarters); err != nil {
			r.log.Error("save quarters", "device", id, "err", err)
			continue
		}
		r.quartersChanged(quarters)
	}
}

// repairPartial recomputes the stored partial quarters from their minutes and
// stores those that are complete now (e.g. after the rules for "complete"
// changed). Quarters whose minutes were pruned stay as they are.
func (r *Recorder) repairPartial(ctx context.Context, ids []string) {
	var since time.Time
	if r.retention > 0 {
		since = time.Now().Add(-r.retention)
	}
	for _, id := range ids {
		starts, err := r.store.PartialQuarters(ctx, id, since)
		if err != nil {
			r.log.Error("read partial quarters", "device", id, "err", err)
			continue
		}
		var fixed []store.Quarter
		for _, start := range starts {
			end := start.Add(QuarterDuration)
			minutes, err := r.store.Minutes(ctx, id, start.Add(-r.maxGap), end.Add(r.maxGap+time.Minute))
			if err != nil {
				r.log.Error("read minutes", "device", id, "err", err)
				break
			}
			for _, q := range ComputeQuarters(id, minutes, start, end, r.maxGap) {
				if q.Quality == QualityComplete {
					fixed = append(fixed, q)
				}
			}
		}
		if len(fixed) == 0 {
			continue
		}
		if err := r.store.SaveQuarters(ctx, fixed); err != nil {
			r.log.Error("save quarters", "device", id, "err", err)
			continue
		}
		r.log.Info("partial quarters now complete", "device", id, "quarters", len(fixed))
		r.quartersChanged(fixed)
	}
}

// Today returns how much each counter increased since local midnight, given
// the current reading.
func (r *Recorder) Today(ctx context.Context, deviceID string, current homewizard.Reading) map[string]float64 {
	midnight := startOfDay(current.Time, r.loc)
	r.mu.Lock()
	ds, ok := r.days[deviceID]
	r.mu.Unlock()
	if !ok || !ds.day.Equal(midnight) || len(ds.values) < countPresent(current) {
		ds = dayStart{day: midnight, values: r.countersAt(ctx, deviceID, midnight, current)}
		r.mu.Lock()
		r.days[deviceID] = ds
		r.mu.Unlock()
	}
	out := map[string]float64{}
	for key, start := range ds.values {
		if v, ok := current.Value(key); ok && v >= start {
			out[key] = v - start
		}
	}
	return out
}

func countPresent(reading homewizard.Reading) int {
	n := 0
	for _, key := range counterKeys {
		if _, ok := reading.Value(key); ok {
			n++
		}
	}
	return n
}

// countersAt returns counter values at t, from stored minutes around t, or
// the first value after t, or the current reading as a last resort.
func (r *Recorder) countersAt(ctx context.Context, deviceID string, t time.Time, current homewizard.Reading) map[string]float64 {
	out := map[string]float64{}
	minutes, err := r.store.Minutes(ctx, deviceID, t.Add(-r.maxGap), current.Time.Add(time.Minute))
	if err != nil {
		r.log.Error("read minutes", "device", deviceID, "err", err)
	}
	for i, sel := range counterSelectors {
		key := counterKeys[i]
		pts := points(minutes, sel)
		if v, _, _, ok := boundary(pts, t, current.Time.Add(time.Minute), true, r.maxGap); ok {
			out[key] = v
		} else if v, ok := current.Value(key); ok {
			out[key] = v
		}
	}
	return out
}

func startOfDay(t time.Time, loc *time.Location) time.Time {
	lt := t.In(loc)
	return time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc).UTC()
}
