package costs

import (
	"context"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Kinds of gap, one per counter of Missing.
const (
	GapQuarters  = "quarters"   // no measurement
	GapPartial   = "partial"    // measured partly, estimated
	GapAnomalous = "anomalous"  // a counter went backwards
	GapPrices    = "prices"     // use without electricity market price
	GapGasPrices = "gas_prices" // gas use without gas price
	GapContract  = "contract"   // day without contract
)

// MaxGaps is the most periods Gaps returns; the counts stay complete.
const MaxGaps = 500

// Gap is a run of consecutive quarter hours (or days, for GapContract) in
// [From, To) that are missing or estimated in the same way.
type Gap struct {
	Kind  string    `json:"kind"`
	From  time.Time `json:"from"`
	To    time.Time `json:"to"`
	Count int       `json:"count"` // quarter hours, or days for GapContract
}

// Gaps lists what Missing counts for the same period.
type Gaps struct {
	From    time.Time `json:"from"`
	To      time.Time `json:"to"`
	Missing Missing   `json:"missing"` // equals Result.Total.Missing of Calculate (or Usage)
	Gaps    []Gap     `json:"gaps"`    // oldest first per kind, kinds in the order above
	// Truncated: there were more than MaxGaps periods; the oldest are listed
	Truncated bool `json:"truncated,omitempty"`
	// MeasuredFrom: the history of the device starts after From; nothing
	// before it counts as missing (an import can add it)
	MeasuredFrom *time.Time `json:"measured_from,omitempty"`
}

// Gaps returns where the quarter hours that Calculate (usage false) or
// Usage (usage true) reports as missing or estimated in [from, to) are, by
// the same rules: every expected quarter hour up to the settled moment is
// complete, partial, anomalous or missing, and quarters with use but
// without price are found by pricing them through quarter(). Not cached.
func (c *Calculator) Gaps(ctx context.Context, deviceID string, from, to time.Time, usage bool) (*Gaps, error) {
	loc := c.loc()
	now := c.now()
	from = floor(from, BucketDay, loc)
	end := to
	if now.Before(end) {
		end = now
	}
	out := &Gaps{From: from, To: to, Gaps: []Gap{}}
	if !end.After(from) {
		return out, nil
	}
	timeline, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, err
	}
	quarters, err := c.Store.Quarters(ctx, deviceID, from, end)
	if err != nil {
		return nil, err
	}
	first, err := c.measuredFrom(ctx, deviceID)
	if err != nil {
		return nil, err
	}
	if !first.IsZero() && first.After(from) {
		out.MeasuredFrom = &first
	}
	var power, gas []store.MarketPrice
	if !c.water() {
		if power, err = c.Store.Prices(ctx, prices.Market, from, end); err != nil {
			return nil, err
		}
		// the store returns periods that overlap from (a gas day of yesterday)
		if gas, err = c.Store.Prices(ctx, prices.GasMarket, from, end); err != nil {
			return nil, err
		}
	}
	settled := now.Add(-c.Settle)
	runs := map[string][]Gap{}
	add := func(kind string, at, until time.Time) {
		rs := runs[kind]
		if n := len(rs); n > 0 && rs[n-1].To.Equal(at) {
			rs[n-1].To = until
			rs[n-1].Count++
		} else {
			rs = append(rs, Gap{Kind: kind, From: at, To: until, Count: 1})
		}
		runs[kind] = rs
	}
	byStart := make(map[int64]store.Quarter, len(quarters))
	for _, q := range quarters {
		byStart[q.Start.Unix()] = q
	}
	for d := from; d.Before(end); d = d.AddDate(0, 0, 1) {
		dayEnd := d.AddDate(0, 0, 1)
		contract := timeline.At(d.Format(dateLayout))
		if contract == nil && dayEnd.After(first) {
			add(GapContract, d, dayEnd)
			out.Missing.ContractDays++
		}
		upTo := dayEnd
		if end.Before(upTo) {
			upTo = end
		}
		expEnd := upTo
		if expEnd.After(settled) {
			expEnd = settled
		}
		expStart := later(d, first) // nothing is expected before the history starts
		// stored quarters of the day (those after the settled moment are
		// priced, not counted as missing or estimated), then the expected
		// ones without
		for t := d; t.Before(upTo); t = t.Add(15 * time.Minute) {
			qEnd := t.Add(15 * time.Minute)
			q, ok := byStart[t.Unix()]
			if ok && usage {
				q.ExportKWh, q.GasM3 = nil, nil
			}
			switch {
			case ok && q.Quality == history.QualityAnomalous:
				if !t.Before(first) {
					add(GapAnomalous, t, qEnd)
					out.Missing.Anomalous++
				}
			case !ok || !c.has(q):
				if !t.Before(expStart) && !qEnd.After(expEnd) {
					add(GapQuarters, t, qEnd)
					out.Missing.Quarters++
				}
				if !ok {
					continue
				}
				// stored without this timeline's value (gas only): compute
				// still prices what it has, so its missing prices count
			case q.Quality == history.QualityPartial:
				if !t.Before(expStart) && !qEnd.After(expEnd) { // final (see computeWith)
					add(GapPartial, t, qEnd)
					out.Missing.Partial++
				}
			}
			// prices: whatever quarter() cannot price
			var b Bucket
			c.price(&b, q, contract, power, gas, 0, 0, loc, 0)
			if b.Missing.Prices > 0 {
				add(GapPrices, t, qEnd)
				out.Missing.Prices++
			}
			if b.Missing.GasPrices > 0 {
				add(GapGasPrices, t, qEnd)
				out.Missing.GasPrices++
			}
		}
	}
	for _, kind := range []string{GapQuarters, GapPartial, GapAnomalous, GapPrices, GapGasPrices, GapContract} {
		for _, g := range runs[kind] {
			if len(out.Gaps) == MaxGaps {
				out.Truncated = true
				break
			}
			out.Gaps = append(out.Gaps, g)
		}
	}
	return out, nil
}
