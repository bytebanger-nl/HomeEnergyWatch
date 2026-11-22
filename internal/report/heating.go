package report

import (
	"context"
	"errors"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
)

// HeatingWait is how long a scheduled report with heating waits for ista
// to have measured the whole period; after that it is sent with what there
// is, marked incomplete.
const HeatingWait = 7 * 24 * time.Hour

// Heating is the heating usage (ista) per address.
type Heating struct {
	Addresses []HeatingAddress
}

// Complete reports whether ista measured every day of the period at every
// address.
func (h *Heating) Complete() bool {
	for _, a := range h.Addresses {
		if !a.Complete {
			return false
		}
	}
	return true
}

// HeatingAddress is one address's heating over the report's period.
type HeatingAddress struct {
	Name  string
	Unit  string
	Rooms []ista.Room
	Usage ista.Usage      // per day or month (the report's bucket)
	Costs *ista.CostRange // nil when no day of the period has prices
	// LastMeasured is the last day ista measured (zero: none); ista's
	// estimates for the days after it are never used.
	LastMeasured time.Time
	// Complete: ista measured up to the last day of the period.
	Complete bool
	// Missing counts the period's days ista has not measured yet.
	Missing int
	// Prices are the settlement periods that overlap the report's period,
	// with the prices the costs use.
	Prices []HeatingPrice
}

// HeatingPrice is one settlement period's prices.
type HeatingPrice struct {
	Start, End time.Time // the settlement period, inclusive days
	Costs      ista.Costs
}

// istaDay turns a local midnight into ista's day (a UTC date).
func istaDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func parseDay(s string) time.Time {
	t, err := time.Parse(ista.DayLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (b *Builder) heating(ctx context.Context, r *Report) (*Heating, error) {
	if b.Ista == nil {
		return nil, nil
	}
	addrs, err := b.Ista.Addresses(ctx)
	if err != nil {
		return nil, err
	}
	first, last := istaDay(r.From), istaDay(r.To.AddDate(0, 0, -1))
	bucket := ista.BucketDay
	if r.Bucket == "month" {
		bucket = ista.BucketMonth
	}
	h := &Heating{}
	for _, a := range addrs {
		if mi := parseDay(a.MoveIn); !mi.IsZero() && mi.After(last) {
			continue // moved in after the period
		}
		u, err := b.Ista.Usage(ctx, a.Cuid, first, last, bucket)
		if errors.Is(err, ista.ErrNoAddress) {
			continue
		}
		if err != nil {
			return nil, err
		}
		x := HeatingAddress{Name: a.Address, Unit: u.Unit, Rooms: u.Rooms, Usage: u,
			LastMeasured: parseDay(a.LastMeasured)}
		if x.Name == "" {
			x.Name = "Verwarming"
		}
		x.Complete = !x.LastMeasured.IsZero() && !x.LastMeasured.Before(last)
		x.Missing = u.Total.Missing
		if x.LastMeasured.Before(last) {
			from := x.LastMeasured.AddDate(0, 0, 1)
			if x.LastMeasured.IsZero() || from.Before(first) {
				from = first
			}
			if mi := parseDay(a.MoveIn); mi.After(from) {
				from = mi
			}
			x.Missing = int(last.Sub(from).Hours()/24) + 1
		}
		if u.Total.Total == nil && x.LastMeasured.IsZero() {
			continue // nothing at all from ista yet
		}
		cr, err := b.Ista.CostRange(ctx, a.Cuid, first, last, bucket)
		if err != nil {
			return nil, err
		}
		if cr.Total.Total != nil {
			x.Costs = &cr
		}
		for _, p := range a.Periods {
			ps, pe := parseDay(p.Start), parseDay(p.End)
			if ps.IsZero() || pe.IsZero() || pe.Before(first) || ps.After(last) {
				continue
			}
			c, err := b.Ista.Costs(ctx, a.Cuid, p.Y)
			if err != nil {
				return nil, err
			}
			x.Prices = append(x.Prices, HeatingPrice{Start: ps, End: pe, Costs: c})
		}
		h.Addresses = append(h.Addresses, x)
	}
	if len(h.Addresses) == 0 {
		return nil, nil
	}
	return h, nil
}

// HeatingPending reports whether ista has not yet measured every day up to
// the last day of [from, to) at an address that was lived in then. Without
// ista nothing is pending.
func (b *Builder) HeatingPending(ctx context.Context, from, to time.Time) (bool, error) {
	if b.Ista == nil {
		return false, nil
	}
	addrs, err := b.Ista.Addresses(ctx)
	if err != nil {
		return false, err
	}
	last := istaDay(to.In(b.loc()).AddDate(0, 0, -1))
	for _, a := range addrs {
		if mi := parseDay(a.MoveIn); !mi.IsZero() && mi.After(last) {
			continue
		}
		if m := parseDay(a.LastMeasured); m.IsZero() || m.Before(last) {
			return true, nil
		}
	}
	return false, nil
}
