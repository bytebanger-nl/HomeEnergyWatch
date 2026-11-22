package costs

import (
	"context"
	"fmt"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// NettingEndsYear is the first calendar year without net metering
// (salderen) for electricity in the Netherlands.
const NettingEndsYear = 2027

// Bucket sizes.
const (
	BucketHour  = "hour" // only for a few days (MaxHourSpan)
	BucketDay   = "day"
	BucketMonth = "month"
	BucketYear  = "year"
)

// MaxHourSpan is the longest period that can be asked per hour.
const MaxHourSpan = 8 * 24 * time.Hour

// Bucket holds energy and costs of one period. Amounts are euros incl. VAT;
// ExportEUR and NettingEUR are credits (what you get back).
type Bucket struct {
	Start     time.Time `json:"start"`
	ImportKWh float64   `json:"import_kwh"`
	ExportKWh float64   `json:"export_kwh"`
	GasM3     float64   `json:"gas_m3"`
	WaterM3   float64   `json:"water_m3"`

	ElectricityEUR    float64 `json:"electricity_eur"`     // supply: market price + markup, or the fixed rate
	ElectricityTaxEUR float64 `json:"electricity_tax_eur"` // energy tax on import
	ExportEUR         float64 `json:"export_eur"`          // credit for export
	NettingEUR        float64 `json:"netting_eur"`         // credit for net metering (salderen)
	GasEUR            float64 `json:"gas_eur"`             // supply: gas day price + markup, or the fixed rate
	GasTaxEUR         float64 `json:"gas_tax_eur"`         // energy tax on gas
	WaterEUR          float64 `json:"water_eur"`           // water supply: the rate per m³
	WaterTaxEUR       float64 `json:"water_tax_eur"`       // tax on tap water
	FixedEUR          float64 `json:"fixed_eur"`           // monthly costs, per day
	TotalEUR          float64 `json:"total_eur"`

	Complete bool    `json:"complete"`
	Missing  Missing `json:"missing"`

	// the part of the costs that belongs to the gas advance: gas supply and
	// tax (gasVar) and the gas fixed costs (gasFixed) of days under a
	// contract that splits electricity and gas; the rest of TotalEUR belongs
	// to the electricity advance
	gasVar, gasFixed float64
}

// partEUR returns the costs of one part: "" for all, PartGas for the gas
// advance, PartElectricity for the rest (on a contract without Split all
// costs belong to the electricity part).
func (b *Bucket) partEUR(part string) float64 {
	switch part {
	case PartGas:
		return b.gasVar + b.gasFixed
	case PartElectricity:
		return b.TotalEUR - b.gasVar - b.gasFixed
	}
	return b.TotalEUR
}

// partVariableEUR is partEUR without the fixed costs.
func (b *Bucket) partVariableEUR(part string) float64 {
	switch part {
	case PartGas:
		return b.gasVar
	case PartElectricity:
		return b.TotalEUR - b.FixedEUR - b.gasVar
	}
	return b.TotalEUR - b.FixedEUR
}

// partFixedEUR is the fixed costs of one part.
func (b *Bucket) partFixedEUR(part string) float64 {
	return b.partEUR(part) - b.partVariableEUR(part)
}

// Missing counts what could not be calculated. Missing data is reported,
// never counted as zero.
type Missing struct {
	Quarters     int `json:"quarters"`      // quarter hours without measurement (electricity; water on the water timeline)
	Partial      int `json:"partial"`       // quarter hours measured only partly (value estimated from the readings there are)
	Anomalous    int `json:"anomalous"`     // quarter hours where a counter went backwards (meter reset); no usage counted
	Prices       int `json:"prices"`        // quarter hours with use but without market price
	GasPrices    int `json:"gas_prices"`    // quarter hours with gas use but without gas price
	ContractDays int `json:"contract_days"` // days without contract
}

func (m Missing) any() bool { return m != Missing{} }

func (b *Bucket) add(o *Bucket) {
	b.ImportKWh += o.ImportKWh
	b.ExportKWh += o.ExportKWh
	b.GasM3 += o.GasM3
	b.WaterM3 += o.WaterM3
	b.WaterEUR += o.WaterEUR
	b.WaterTaxEUR += o.WaterTaxEUR
	b.ElectricityEUR += o.ElectricityEUR
	b.ElectricityTaxEUR += o.ElectricityTaxEUR
	b.ExportEUR += o.ExportEUR
	b.NettingEUR += o.NettingEUR
	b.GasEUR += o.GasEUR
	b.GasTaxEUR += o.GasTaxEUR
	b.FixedEUR += o.FixedEUR
	b.gasVar += o.gasVar
	b.gasFixed += o.gasFixed
	b.Missing.Quarters += o.Missing.Quarters
	b.Missing.Partial += o.Missing.Partial
	b.Missing.Anomalous += o.Missing.Anomalous
	b.Missing.Prices += o.Missing.Prices
	b.Missing.GasPrices += o.Missing.GasPrices
	b.Missing.ContractDays += o.Missing.ContractDays
}

func (b *Bucket) finish() {
	b.TotalEUR = b.ElectricityEUR + b.ElectricityTaxEUR + b.GasEUR + b.GasTaxEUR + b.WaterEUR + b.WaterTaxEUR + b.FixedEUR - b.ExportEUR - b.NettingEUR
	b.Complete = !b.Missing.any()
	for _, v := range []*float64{&b.ImportKWh, &b.ExportKWh, &b.GasM3, &b.ElectricityEUR, &b.ElectricityTaxEUR,
		&b.ExportEUR, &b.NettingEUR, &b.GasEUR, &b.GasTaxEUR, &b.WaterM3, &b.WaterEUR, &b.WaterTaxEUR, &b.FixedEUR, &b.TotalEUR} {
		*v = math.Round(*v*1e6) / 1e6
	}
}

// Result is the outcome of a calculation.
type Result struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Bucket  string    `json:"bucket"`
	Buckets []Bucket  `json:"buckets"`
	Total   Bucket    `json:"total"`
}

// Calculator calculates costs from the store. Results of finished days are
// computed lazily and cached per device and day. Invalidate drops a day and
// every day after it, because net metering makes each day depend on the days
// before it in the same year.
//
// A calculator works on one timeline: energy (the P1 meter, electricity and
// gas) or, when Contracts is the water timeline, water (the water meter).
// For water the yearly cap of the tax on tap water plays the part of net
// metering: each day depends on the volume used before it in its year.
type Calculator struct {
	Store     *store.Store
	Contracts *Contracts
	Loc       *time.Location
	Now       func() time.Time // default time.Now
	// Settle is how long after its end a quarter hour may take to be stored
	// (Recorder.Settle); quarter hours within it are not counted as missing
	Settle time.Duration
	// Forecast holds the settings of the advance forecast (nil: automatic)
	Forecast *forecast.Service

	mu   sync.Mutex
	gen  uint64                         // bumped by every invalidation
	days map[string]map[int64]*dayEntry // device -> local day start (unix) -> entry
}

// dayEntry is the result of one day plus the net metering state at its end.
type dayEntry struct {
	bucket               Bucket
	cumImport, cumExport float64
	// hours splits the day per hour (computeOpts.hours only; never cached)
	hours []Bucket
	// full: computed up to the day's end; a day cut short by the request's
	// end (or by now) is never cached
	full bool
}

// Invalidate drops cached days from the day that contains from onwards, for
// one device or (deviceID "") all devices.
func (c *Calculator) Invalidate(deviceID string, from time.Time) {
	day := floor(from, BucketDay, c.loc()).Unix()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	for dev, days := range c.days {
		if deviceID != "" && dev != deviceID {
			continue
		}
		for d := range days {
			if d >= day {
				delete(days, d)
			}
		}
	}
}

// InvalidateDate drops cached days of all devices from a local calendar date
// (YYYY-MM-DD) onwards; an invalid date drops everything.
func (c *Calculator) InvalidateDate(date string) {
	d, err := time.ParseInLocation(dateLayout, date, c.loc())
	if err != nil {
		d = time.Time{}
	}
	c.Invalidate("", d)
}

// cached returns the number of cached days (for tests).
func (c *Calculator) cached() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, days := range c.days {
		n += len(days)
	}
	return n
}

// water reports whether the calculator prices water.
func (c *Calculator) water() bool { return c.Contracts != nil && c.Contracts.Water() }

// has reports whether a quarter hour holds the measurement this calculator
// prices: electricity import, or water.
func (c *Calculator) has(q store.Quarter) bool {
	if c.water() {
		return q.WaterM3 != nil
	}
	return q.ImportKWh != nil
}

// price adds one quarter hour to b: through quarter() for energy, through
// waterQuarter() for water, where cumImport carries the water volume of the
// year (for the tax cap) and cumExport is unused.
func (c *Calculator) price(b *Bucket, q store.Quarter, contract *Contract, power, gas []store.MarketPrice, cumImport, cumExport float64, loc *time.Location, rulesYear int) (float64, float64) {
	if c.water() {
		return waterQuarter(b, q, contract, cumImport), 0
	}
	return quarter(b, q, contract, power, gas, cumImport, cumExport, loc, rulesYear)
}

func (c *Calculator) loc() *time.Location {
	if c.Loc == nil {
		return prices.Amsterdam
	}
	return c.Loc
}

// Calculate returns the costs of a device (the P1 meter) in [from, to), in
// buckets of a day, month or year in the calculator's time zone. Nothing
// after now is counted. Net metering is settled per calendar year, so the
// days from 1 January of the year of from are needed; finished days come
// from the cache when possible.
func (c *Calculator) Calculate(ctx context.Context, deviceID string, from, to time.Time, bucket string) (*Result, error) {
	if err := checkBucket(bucket); err != nil {
		return nil, err
	}
	if bucket == BucketHour {
		return c.hourly(ctx, deviceID, from, to, false)
	}
	now := c.now()
	return c.aggregate(from, to, bucket, now, func(ys, end time.Time) ([]*dayEntry, error) {
		return c.year(ctx, deviceID, ys, end, now)
	})
}

// Usage returns what the electricity a device took (a socket or kWh meter)
// cost in [from, to): each quarter hour's import priced through quarter()
// with the contract of that day, so with the market price of that moment
// on a dynamic contract, plus markup, energy tax and VAT. Fixed costs,
// export, net metering and gas are left out: it is a breakdown of the
// connection's costs, not part of them. Not cached; energy timeline only.
func (c *Calculator) Usage(ctx context.Context, deviceID string, from, to time.Time, bucket string) (*Result, error) {
	if c.water() {
		return nil, fmt.Errorf("apparaatkosten alleen voor stroom")
	}
	if err := checkBucket(bucket); err != nil {
		return nil, err
	}
	if bucket == BucketHour {
		return c.hourly(ctx, deviceID, from, to, true)
	}
	timeline, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, err
	}
	start := floor(from, BucketDay, c.loc())
	return c.aggregate(from, to, bucket, c.now(), func(ys, end time.Time) ([]*dayEntry, error) {
		ye := yearEnd(ys, end)
		if ys.Before(start) {
			ys = start // no net metering, so no need to start on 1 January
		}
		return c.computeWith(ctx, deviceID, timeline, ys, ye, 0, 0, computeOpts{usage: true})
	})
}

// now returns the calculator's clock.
func (c *Calculator) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// checkBucket refuses an unknown bucket size.
func checkBucket(bucket string) error {
	switch bucket {
	case BucketHour, BucketDay, BucketMonth, BucketYear:
		return nil
	}
	return fmt.Errorf("onbekende indeling %q (hour, day, month, year)", bucket)
}

// hourly returns the costs of [from, min(to, now)) per hour, for at most
// MaxHourSpan. The days are calculated as always (through quarter(), with
// the net metering state at the start of the first day from the cached days
// before it) and split per hour; usage is Usage's variant. Fixed costs are
// spread evenly over the hours of a day. Not cached.
func (c *Calculator) hourly(ctx context.Context, deviceID string, from, to time.Time, usage bool) (*Result, error) {
	if usage && c.water() {
		return nil, fmt.Errorf("apparaatkosten alleen voor stroom")
	}
	if to.Sub(from) > MaxHourSpan {
		return nil, fmt.Errorf("per uur kan hooguit %d dagen", int(MaxHourSpan.Hours()/24))
	}
	loc := c.loc()
	now := c.now()
	day := floor(from, BucketDay, loc)
	end := to
	if now.Before(end) {
		end = now
	}
	res := &Result{From: day, To: to, Bucket: BucketHour}
	if !end.After(day) {
		return res, nil
	}
	timeline, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, err
	}
	var cumImport, cumExport float64
	if ys := floor(day, BucketYear, loc); !usage && day.After(ys) {
		// net metering (and the water tax cap) run per calendar year
		before, err := c.year(ctx, deviceID, ys, day, now)
		if err != nil {
			return nil, err
		}
		if len(before) > 0 {
			last := before[len(before)-1]
			cumImport, cumExport = last.cumImport, last.cumExport
		}
	}
	// per calendar year: net metering and the water tax cap start over on
	// 1 January, also when the hours span it
	var days []*dayEntry
	for start := day; start.Before(end); {
		segEnd := yearEnd(floor(start, BucketYear, loc), end)
		seg, err := c.computeWith(ctx, deviceID, timeline, start, segEnd, cumImport, cumExport, computeOpts{usage: usage, hours: true})
		if err != nil {
			return nil, err
		}
		days = append(days, seg...)
		start, cumImport, cumExport = segEnd, 0, 0
	}
	first := from.Truncate(time.Hour)
	for _, d := range days {
		for i := range d.hours {
			hb := d.hours[i]
			if hb.Start.Before(first) {
				continue
			}
			hb.finish()
			res.Buckets = append(res.Buckets, hb)
			res.Total.add(&hb)
		}
	}
	if len(res.Buckets) > 0 {
		res.Total.Start = res.Buckets[0].Start
	}
	res.Total.finish()
	return res, nil
}

// yearEnd returns the end of the calendar year that starts at ys, or end
// when that is earlier.
func yearEnd(ys, end time.Time) time.Time {
	ye := ys.AddDate(1, 0, 0)
	if end.Before(ye) {
		return end
	}
	return ye
}

// aggregate sums the days of [from, min(to, now)) into buckets; days gives
// the days of the calendar year that starts at ys, up to end.
func (c *Calculator) aggregate(from, to time.Time, bucket string, now time.Time, days func(ys, end time.Time) ([]*dayEntry, error)) (*Result, error) {
	loc := c.loc()
	from = floor(from, BucketDay, loc)
	end := to
	if now.Before(end) {
		end = now
	}
	res := &Result{From: from, To: to, Bucket: bucket}
	if !end.After(from) {
		return res, nil
	}

	var buckets []Bucket
	index := map[int64]int{}
	for s := floor(from, bucket, loc); s.Before(end); s = next(s, bucket) {
		index[s.Unix()] = len(buckets)
		buckets = append(buckets, Bucket{Start: s})
	}
	for ys := floor(from, BucketYear, loc); ys.Before(end); ys = ys.AddDate(1, 0, 0) {
		days, err := days(ys, end)
		if err != nil {
			return nil, err
		}
		for _, d := range days {
			if d.bucket.Start.Before(from) {
				continue
			}
			if i, ok := index[floor(d.bucket.Start, bucket, loc).Unix()]; ok {
				buckets[i].add(&d.bucket)
			}
		}
	}
	for i := range buckets {
		buckets[i].finish()
		res.Total.add(&buckets[i])
	}
	if len(buckets) > 0 {
		res.Total.Start = buckets[0].Start
	}
	res.Total.finish()
	res.Buckets = buckets
	return res, nil
}

// year returns the days of the year that starts at ys, up to end: the cached
// finished days from 1 January plus the newly calculated rest.
func (c *Calculator) year(ctx context.Context, deviceID string, ys, end, now time.Time) ([]*dayEntry, error) {
	ye := yearEnd(ys, end)
	var out []*dayEntry
	c.mu.Lock()
	gen := c.gen
	cache := c.days[deviceID]
	d := ys
	for ; d.Before(ye); d = d.AddDate(0, 0, 1) {
		e, ok := cache[d.Unix()]
		if !ok {
			break
		}
		out = append(out, e)
	}
	c.mu.Unlock()
	if !d.Before(ye) {
		return out, nil
	}
	var cumImport, cumExport float64
	if len(out) > 0 {
		last := out[len(out)-1]
		cumImport, cumExport = last.cumImport, last.cumExport
	}
	fresh, err := c.compute(ctx, deviceID, d, ye, cumImport, cumExport)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.gen == gen { // nothing changed while calculating
		if c.days == nil {
			c.days = map[string]map[int64]*dayEntry{}
		}
		if c.days[deviceID] == nil {
			c.days[deviceID] = map[int64]*dayEntry{}
		}
		settled := now.Add(-c.Settle)
		for _, e := range fresh {
			// finished days only, once their last quarter hour can be stored,
			// and only when computed up to their end
			if e.full && !e.bucket.Start.AddDate(0, 0, 1).After(settled) {
				c.days[deviceID][e.bucket.Start.Unix()] = e
			}
		}
	}
	c.mu.Unlock()
	return append(out, fresh...), nil
}

// compute calculates the days in [from, end); from is a local midnight and
// the net metering state is the one at from.
func (c *Calculator) compute(ctx context.Context, deviceID string, from, end time.Time, cumImport, cumExport float64) ([]*dayEntry, error) {
	timeline, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, err
	}
	return c.computeWith(ctx, deviceID, timeline, from, end, cumImport, cumExport, computeOpts{})
}

// computeOpts are the variants of computeWith.
type computeOpts struct {
	// rulesYear > 0 applies the rules of that year (net metering, feed-in
	// VAT) to earlier years too (a scenario under future rules)
	rulesYear int
	// usage prices the electricity import only: no export, gas or fixed
	// costs (see Usage)
	usage bool
	// hours also splits each day per hour (dayEntry.hours)
	hours bool
}

// computeWith is compute with a given timeline.
func (c *Calculator) computeWith(ctx context.Context, deviceID string, timeline Timeline, from, end time.Time, cumImport, cumExport float64, opts computeOpts) ([]*dayEntry, error) {
	loc := c.loc()
	// quarter hours that ended after this may not be stored yet: they are
	// not counted as missing
	settled := c.now().Add(-c.Settle)
	quarters, err := c.Store.Quarters(ctx, deviceID, from, end)
	if err != nil {
		return nil, err
	}
	first, err := c.measuredFrom(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	var power, gas []store.MarketPrice
	if !c.water() {
		if power, err = c.Store.Prices(ctx, prices.Market, from, end); err != nil {
			return nil, err
		}
		if gas, err = c.Store.Prices(ctx, prices.GasMarket, from, end); err != nil {
			return nil, err
		}
	}
	qi := 0
	var out []*dayEntry
	for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
		dayEnd := d.AddDate(0, 0, 1)
		upTo := dayEnd
		if end.Before(upTo) {
			upTo = end
		}
		e := &dayEntry{bucket: Bucket{Start: d}, full: upTo.Equal(dayEnd)}
		b := &e.bucket
		contract := timeline.At(d.Format(dateLayout))
		measured := 0
		// per hour: each quarter is priced into its hour, the day is their sum
		var hourMeasured []int
		if opts.hours {
			for t := d; t.Before(upTo); t = t.Add(time.Hour) {
				e.hours = append(e.hours, Bucket{Start: t})
			}
			hourMeasured = make([]int, len(e.hours))
		}
		for ; qi < len(quarters) && quarters[qi].Start.Before(dayEnd); qi++ {
			q := quarters[qi]
			target := b
			hi := -1
			if opts.hours {
				if hi = int(q.Start.Sub(d) / time.Hour); hi >= 0 && hi < len(e.hours) {
					target = &e.hours[hi]
				} else {
					hi = -1
				}
			}
			if opts.usage {
				// without export there is nothing to net either
				q.ExportKWh, q.GasM3 = nil, nil
			}
			// every expected quarter hour is exactly one of: complete,
			// partial (kept, but estimated), anomalous (counter went
			// backwards: no usage counted) or missing. A quarter that may
			// still change (within Settle) is priced, but only counted
			// once it is final, so a gap that fills itself is never shown.
			// Before the history starts nothing is expected: a quarter
			// there is priced, never a gap.
			final := !q.Start.Add(15*time.Minute).After(settled) && !q.Start.Before(first)
			counted := true
			switch {
			case q.Quality == history.QualityAnomalous:
				if !q.Start.Before(first) {
					target.Missing.Anomalous++
				}
			case !c.has(q):
				// no value for this timeline: counted as missing below
				counted = false
			case q.Quality == history.QualityPartial:
				if final {
					target.Missing.Partial++
				}
			}
			if counted && final {
				measured++
				if hi >= 0 {
					hourMeasured[hi]++
				}
			}
			cumImport, cumExport = c.price(target, q, contract, power, gas, cumImport, cumExport, loc, opts.rulesYear)
		}
		for i := range e.hours {
			b.add(&e.hours[i])
		}
		// expected quarter hours up to now (a DST day has 92 or 100), without
		// the ones that may still change
		expEnd := upTo
		if expEnd.After(settled) {
			expEnd = settled
		}
		if expected := quartersIn(later(d, first), expEnd); expected > measured {
			b.Missing.Quarters = expected - measured
		}
		if contract == nil {
			if dayEnd.After(first) { // nothing to price before the history starts
				b.Missing.ContractDays = 1
			}
		} else if !opts.usage {
			share := float64(upTo.Sub(d)) / float64(dayEnd.Sub(d)) // today: up to now
			b.FixedEUR = contract.monthlyEUR() * 12 / float64(daysInYear(d.Year())) * share
			b.gasFixed = contract.monthlyGasEUR() * 12 / float64(daysInYear(d.Year())) * share
		}
		for i := range e.hours {
			hb := &e.hours[i]
			hEnd := hb.Start.Add(time.Hour)
			if hEnd.After(upTo) {
				hEnd = upTo
			}
			if contract == nil {
				if i == 0 && dayEnd.After(first) {
					hb.Missing.ContractDays = 1 // once per day, as in the day bucket
				}
			} else if !opts.usage {
				// fixed costs spread evenly over the day
				part := float64(hEnd.Sub(hb.Start)) / float64(dayEnd.Sub(d))
				hb.FixedEUR = contract.monthlyEUR() * 12 / float64(daysInYear(d.Year())) * part
				hb.gasFixed = contract.monthlyGasEUR() * 12 / float64(daysInYear(d.Year())) * part
			}
			expHour := hEnd
			if expHour.After(settled) {
				expHour = settled
			}
			if expected := quartersIn(later(hb.Start, first), expHour); expected > hourMeasured[i] {
				hb.Missing.Quarters = expected - hourMeasured[i]
			}
		}
		e.cumImport, e.cumExport = cumImport, cumExport
		out = append(out, e)
	}
	return out, nil
}

// later returns the later of a and b.
func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// quartersIn is the number of whole quarter hours in [from, to).
func quartersIn(from, to time.Time) int {
	if !to.After(from) {
		return 0
	}
	return int(to.Sub(from) / (15 * time.Minute))
}

// measuredFrom is when the history of a device starts (Store.MeasuredFrom):
// before it no quarter hour is expected, so the days before a device was
// added (or before the oldest imported export) and the first quarter hours
// of its first day are not missing. A device without any history has
// everything missing (the zero time).
func (c *Calculator) measuredFrom(ctx context.Context, deviceID string) (time.Time, error) {
	first, ok, err := c.Store.MeasuredFrom(ctx, deviceID)
	if err != nil || !ok {
		return time.Time{}, err
	}
	return first, nil
}

// quarter adds the energy and costs of one quarter hour to b and returns
// the new net metering state (cumulative import and export of the year).
// The rules of the quarter's year apply, or those of rulesYear when later
// (a what-if under future rules).
func quarter(b *Bucket, q store.Quarter, contract *Contract, power, gas []store.MarketPrice, cumImport, cumExport float64, loc *time.Location, rulesYear int) (float64, float64) {
	lt := q.Start.In(loc)
	year := max(lt.Year(), rulesYear)
	imp, exp, gm3 := val(q.ImportKWh), val(q.ExportKWh), val(q.GasM3)
	b.ImportKWh += imp
	b.ExportKWh += exp
	b.GasM3 += gm3

	// net metering: kWh that become nettable this quarter hour
	netted := 0.0
	if year < NettingEndsYear {
		n0 := math.Min(cumImport, cumExport)
		cumImport += imp
		cumExport += exp
		netted = math.Min(cumImport, cumExport) - n0
	}
	if contract == nil {
		return cumImport, cumExport
	}
	e := contract.Electricity
	switch contract.Type {
	case TypeDynamic:
		if imp != 0 || exp != 0 {
			if p, ok := contract.powerPrice(power, q.Start); ok {
				market := p / 1000 // EUR/kWh excl. VAT
				b.ElectricityEUR += imp * (market*contract.vat() + e.MarkupCt/100)
				feedIn := market
				if contract.exportVAT(year) {
					feedIn *= contract.vat()
				}
				if year >= NettingEndsYear && feedIn < 0 {
					// from 2027 the compensation may not be negative; what a
					// supplier charges for feeding in goes in ExportCt
					feedIn = 0
				}
				b.ExportEUR += exp * (feedIn + e.ExportCt/100)
			} else {
				b.Missing.Prices++
			}
		}
		b.ElectricityTaxEUR += imp * e.TaxCt / 100
		b.NettingEUR += netted * e.TaxCt / 100
	case TypeFixed:
		// the tariff the meter registered (holidays are low too), or the clock
		rate := contract.fixedRateAt(lowShare(q.ImportLowKWh, imp, lt))
		b.ElectricityEUR += imp * rate / 100
		b.ElectricityTaxEUR += imp * e.TaxCt / 100
		b.ExportEUR += exp * e.ExportCt / 100
		// netted kWh are worth the full import price instead of the feed-in
		// compensation; without import this quarter the export tells the tariff
		netRate := rate
		if imp == 0 {
			netRate = contract.fixedRateAt(lowShare(q.ExportLowKWh, exp, lt))
		}
		b.NettingEUR += netted * (netRate + e.TaxCt - e.ExportCt) / 100
	}
	if gm3 != 0 {
		g := contract.Gas
		cost := 0.0
		switch contract.gasType() {
		case TypeDynamic:
			if p, ok := priceAt(gas, q.Start); ok {
				cost = gm3 * (p*contract.vat() + g.MarkupCt/100)
			} else {
				b.Missing.GasPrices++
			}
		case TypeFixed:
			cost = gm3 * g.RateCt / 100
		}
		tax := gm3 * g.TaxCt / 100
		b.GasEUR += cost
		b.GasTaxEUR += tax
		if contract.Split {
			b.gasVar += cost + tax
		}
	}
	return cumImport, cumExport
}

// waterQuarter adds the water and its costs of one quarter hour to b and
// returns the volume of the year so far. The tax on tap water only applies
// to the first WaterTaxCapM3 of a calendar year.
func waterQuarter(b *Bucket, q store.Quarter, contract *Contract, cumM3 float64) float64 {
	m3 := val(q.WaterM3)
	b.WaterM3 += m3
	taxed := math.Max(0, math.Min(cumM3+m3, WaterTaxCapM3)-math.Min(cumM3, WaterTaxCapM3))
	cumM3 += m3
	if contract == nil || m3 == 0 {
		return cumM3
	}
	b.WaterEUR += m3 * contract.Water.RateCt / 100
	b.WaterTaxEUR += taxed * contract.Water.TaxCt / 100
	return cumM3
}

func val(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// powerPrice returns the electricity market price that the contract bills
// for the quarter hour at t: the price of that quarter, or with
// Electricity.Hourly the average of the clock hour around it.
func (c *Contract) powerPrice(ps []store.MarketPrice, t time.Time) (float64, bool) {
	if c != nil && c.Electricity.Hourly {
		return HourAverage(ps, t)
	}
	return priceAt(ps, t)
}

// HourAverage returns the time-weighted average price of the clock hour that
// contains t (hours start on the whole UTC hour, which is a whole local hour
// in every zone with an offset of whole hours). It is only known when the
// prices cover the whole hour, so a supplier's hourly price is never guessed.
func HourAverage(ps []store.MarketPrice, t time.Time) (float64, bool) {
	from := t.Truncate(time.Hour)
	to := from.Add(time.Hour)
	i := sort.Search(len(ps), func(i int) bool { return ps[i].Start.After(from) }) - 1
	if i < 0 {
		i = 0
	}
	var sum float64
	var covered time.Duration
	for ; i < len(ps) && ps[i].Start.Before(to); i++ {
		s, e := ps[i].Start, ps[i].Start.Add(ps[i].Duration)
		if s.Before(from) {
			s = from
		}
		if e.After(to) {
			e = to
		}
		if !e.After(s) {
			continue
		}
		d := e.Sub(s)
		sum += ps[i].Value * float64(d)
		covered += d
	}
	if covered < time.Hour {
		return 0, false
	}
	return sum / float64(covered), true
}

// priceAt returns the price of the period that contains t.
func priceAt(ps []store.MarketPrice, t time.Time) (float64, bool) {
	i := sort.Search(len(ps), func(i int) bool { return ps[i].Start.After(t) }) - 1
	if i < 0 || !ps[i].Start.Add(ps[i].Duration).After(t) {
		return 0, false
	}
	return ps[i].Value, true
}

func floor(t time.Time, bucket string, loc *time.Location) time.Time {
	l := t.In(loc)
	switch bucket {
	case BucketYear:
		return time.Date(l.Year(), 1, 1, 0, 0, 0, 0, loc)
	case BucketMonth:
		return time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
	default:
		return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	}
}

func next(t time.Time, bucket string) time.Time {
	switch bucket {
	case BucketYear:
		return t.AddDate(1, 0, 0)
	case BucketMonth:
		return t.AddDate(0, 1, 0)
	default:
		return t.AddDate(0, 0, 1)
	}
}

func daysInYear(y int) int {
	return time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC).YearDay()
}
