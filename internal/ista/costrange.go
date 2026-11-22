package ista

import (
	"context"
	"fmt"
	"time"
)

// CostBucket is the heating costs over some days: the units of those days
// at the price per unit of their period, plus the fixed part (floor area ×
// price per dm²) spread evenly over the days of the period the address was
// lived in.
type CostBucket struct {
	Start    string   `json:"start"`
	End      string   `json:"end"` // inclusive
	Units    *float64 `json:"units"`
	Variable *float64 `json:"variable"`
	Fixed    *float64 `json:"fixed"`
	Total    *float64 `json:"total"`
	// Rooms is the variable part per room, in the order of Usage.Rooms
	// (the fixed part belongs to the dwelling, not to a radiator).
	Rooms []*float64 `json:"rooms,omitempty"`
	Days  int        `json:"days"`
	// Missing counts the days without a reading; Unpriced the days whose
	// period has no prices (no contract and no earlier settlement).
	Missing   int  `json:"missing"`
	Unpriced  int  `json:"unpriced"`
	Estimated bool `json:"estimated,omitempty"`
}

// CostRange is the heating costs of an address per bucket.
type CostRange struct {
	Cuid    string       `json:"cuid"`
	Unit    string       `json:"unit"`
	Bucket  string       `json:"bucket"`
	Buckets []CostBucket `json:"buckets"`
	Total   CostBucket   `json:"total"`
}

type dayPrice struct {
	unit, fixedPerDay float64
	ok                bool
}

// CostRange returns the costs from from to to (inclusive days), grouped
// by bucket, with the prices Costs uses for each period (the settlement,
// the contract or the last settlement). Only the days ista measured count:
// the days after the last measured one (ista's estimates, and today, which
// is still running) are left out, as are the days after the last reading.
func (s *Service) CostRange(ctx context.Context, cuid string, from, to time.Time, bucket string) (CostRange, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return CostRange{}, err
	}
	if m, err := time.Parse(DayLayout, a.LastMeasured); err != nil {
		to = from.AddDate(0, 0, -1) // nothing measured yet
	} else if m.Before(to) {
		to = m
	}
	u, err := s.Usage(ctx, a.Cuid, from, to, BucketDay)
	if err != nil {
		return CostRange{}, err
	}
	switch bucket {
	case BucketDay, BucketWeek, BucketMonth, BucketPeriod:
	default:
		return CostRange{}, fmt.Errorf("ista: onbekende indeling %q", bucket)
	}
	prices := map[int]dayPrice{}
	priceOf := func(day time.Time) (dayPrice, error) {
		p, ok := periodOfDay(a, day)
		if !ok {
			return dayPrice{}, nil
		}
		if dp, ok := prices[p.Y]; ok {
			return dp, nil
		}
		c, err := s.Costs(ctx, a.Cuid, p.Y)
		if err != nil {
			return dayPrice{}, err
		}
		var dp dayPrice
		if c.UnitPrice != nil && c.FixedPeriod != nil {
			pf, _ := time.Parse(DayLayout, p.From)
			pe, _ := time.Parse(DayLayout, p.End)
			lived := pe.Sub(pf).Hours()/24 + 1
			dp = dayPrice{unit: *c.UnitPrice, fixedPerDay: *c.FixedPeriod / lived, ok: true}
		}
		prices[p.Y] = dp
		return dp, nil
	}

	out := CostRange{Cuid: u.Cuid, Unit: u.Unit, Bucket: bucket, Buckets: []CostBucket{}}
	if len(u.Buckets) == 0 {
		out.Total = CostBucket{Start: u.Total.Start, End: u.Total.End}
		return out, nil
	}
	out.Total = CostBucket{Start: u.Buckets[0].Start, End: u.Buckets[len(u.Buckets)-1].End, Rooms: make([]*float64, len(u.Rooms))}
	var b *CostBucket
	key := ""
	for _, d := range u.Buckets {
		day, _ := time.Parse(DayLayout, d.Start)
		bs := bucketStart(bucket, day, a.Periods)
		if b == nil || key != bs.Format(DayLayout) {
			if b != nil {
				out.Buckets = append(out.Buckets, *b)
			}
			key = bs.Format(DayLayout)
			start, end := bs, bucketEnd(bucket, bs, a.Periods)
			if first, _ := time.Parse(DayLayout, out.Total.Start); start.Before(first) {
				start = first
			}
			if last, _ := time.Parse(DayLayout, out.Total.End); end.After(last) {
				end = last
			}
			b = &CostBucket{Start: start.Format(DayLayout), End: end.Format(DayLayout), Rooms: make([]*float64, len(u.Rooms))}
		}
		dp, err := priceOf(day)
		if err != nil {
			return out, err
		}
		for _, x := range []*CostBucket{b, &out.Total} {
			x.Days++
			if d.Estimated {
				x.Estimated = true
			}
			if !dp.ok {
				x.Unpriced++
				if d.Total == nil {
					x.Missing++
				}
				continue
			}
			x.Fixed = addp(x.Fixed, dp.fixedPerDay)
			if d.Total == nil {
				x.Missing++
				continue
			}
			x.Units = addp(x.Units, *d.Total)
			x.Variable = addp(x.Variable, *d.Total*dp.unit)
			for j, v := range d.Units {
				if v != nil && j < len(x.Rooms) {
					x.Rooms[j] = addp(x.Rooms[j], *v*dp.unit)
				}
			}
		}
	}
	out.Buckets = append(out.Buckets, *b)
	for i := range out.Buckets {
		out.Buckets[i].round()
	}
	out.Total.round()
	return out, nil
}

func (b *CostBucket) round() {
	for _, p := range append([]*float64{b.Variable, b.Fixed}, b.Rooms...) {
		if p != nil {
			*p = round2(*p)
		}
	}
	if b.Variable != nil || b.Fixed != nil {
		t := 0.0
		if b.Variable != nil {
			t += *b.Variable
		}
		if b.Fixed != nil {
			t += *b.Fixed
		}
		b.Total = ptr(round2(t))
	}
}

// periodOfDay returns the period whose window (from the move-in) holds day.
func periodOfDay(a Address, day time.Time) (Period, bool) {
	ds := day.Format(DayLayout)
	for _, p := range a.Periods {
		if ds >= p.From && ds <= p.End {
			return p, true
		}
	}
	return Period{}, false
}
