package ista

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// ErrNoAddress means there is no stored ista address (yet).
var ErrNoAddress = errors.New("ista: nog geen gegevens")

// Room is one radiator as shown to the user.
type Room struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	RadNr     int     `json:"rad_nr"`
	MeterNr   int64   `json:"meter_nr"`
	Factor    float64 `json:"factor"`
	Reduction float64 `json:"reduction"`
}

// Period is a settlement period as shown to the user: from is the move-in
// when that is later than the period's start.
type Period struct {
	Y     int      `json:"y"`
	Start string   `json:"start"`
	End   string   `json:"end"`
	From  string   `json:"from"`
	TA    *float64 `json:"ta,omitempty"`
}

// Address is an address with its rooms and periods.
type Address struct {
	Cuid    string   `json:"cuid"`
	Address string   `json:"address"`
	Zip     string   `json:"zip"`
	City    string   `json:"city"`
	MoveIn  string   `json:"move_in"`
	Unit    string   `json:"unit"`
	Rooms   []Room   `json:"rooms"`
	Periods []Period `json:"periods"`
	// LastDay is the last day with a stored reading.
	LastDay string `json:"last_day,omitempty"`
	// LastMeasured is the last day whose readings ista measured; the days
	// after it are ista's estimates (fractional readings) or still running.
	LastMeasured string `json:"last_measured,omitempty"`
}

// Addresses returns every stored address with its rooms and periods.
func (s *Service) Addresses(ctx context.Context) ([]Address, error) {
	addrs, err := s.Store.IstaAddresses(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Address, 0, len(addrs))
	for _, a := range addrs {
		x, err := s.address(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

// pickAddress returns the address with cuid, or the first one.
func (s *Service) pickAddress(ctx context.Context, cuid string) (Address, error) {
	addrs, err := s.Store.IstaAddresses(ctx)
	if err != nil {
		return Address{}, err
	}
	for _, a := range addrs {
		if cuid == "" || a.Cuid == cuid {
			return s.address(ctx, a)
		}
	}
	return Address{}, ErrNoAddress
}

func (s *Service) address(ctx context.Context, a store.IstaAddress) (Address, error) {
	x := Address{Cuid: a.Cuid, Address: a.Address, Zip: a.Zip, City: a.City, MoveIn: a.MoveIn, Rooms: []Room{}, Periods: []Period{}}
	services, err := s.Store.IstaServices(ctx, a.Cuid)
	if err != nil {
		return x, err
	}
	for _, v := range services {
		if x.Unit == "" {
			x.Unit = v.Unit
		}
	}
	meters, err := s.Store.IstaMeters(ctx, a.Cuid)
	if err != nil {
		return x, err
	}
	mps, err := s.Store.IstaMeterPeriods(ctx, a.Cuid)
	if err != nil {
		return x, err
	}
	latest := map[int64]store.IstaMeterPeriod{} // ordered by y, so the last wins
	for _, mp := range mps {
		latest[mp.MeterID] = mp
	}
	for _, m := range meters {
		r := Room{ID: m.MeterID, Name: m.Position, RadNr: m.RadNr, MeterNr: m.MeterNr}
		if mp, ok := latest[m.MeterID]; ok {
			r.Factor, r.Reduction = mp.CalcFactor, mp.Reduction
		}
		x.Rooms = append(x.Rooms, r)
	}
	periods, err := s.Store.IstaPeriods(ctx, a.Cuid)
	if err != nil {
		return x, err
	}
	for _, p := range periods {
		from := p.Start
		if a.MoveIn > from {
			from = a.MoveIn
		}
		if from > p.End {
			continue // before the move-in
		}
		x.Periods = append(x.Periods, Period{Y: p.Y, Start: p.Start, End: p.End, From: from, TA: p.TA})
	}
	days, err := s.Store.IstaDays(ctx, a.Cuid, "0000-00-00", "9999-99-99")
	if err == nil && len(days) > 0 {
		x.LastDay = days[len(days)-1].Day
		for i := len(days) - 1; i >= 0; i-- {
			if !days[i].Estimated {
				x.LastMeasured = days[i].Day
				break
			}
		}
	}
	return x, nil
}

// daily holds an address's usage per day, derived from the cumulative
// readings: a day's usage is its reading less the day before's in the same
// period (the first day of a period starts from zero).
type daily struct {
	units    map[string]map[int64]float64 // day -> room -> units (CCD)
	ticks    map[string]map[int64]float64
	total    map[string]float64 // day -> units of the address
	building map[string]float64 // day -> building average
	cum      map[string]map[int64]float64
	cumTotal map[string]float64
	cumBuild map[string]float64
	est      map[string]bool
	periodOf map[string]int
}

func (s *Service) loadDaily(ctx context.Context, a Address, from, to time.Time) (*daily, error) {
	f := from.AddDate(0, 0, -1).Format(DayLayout)
	t := to.Format(DayLayout)
	readings, err := s.Store.IstaReadings(ctx, a.Cuid, f, t)
	if err != nil {
		return nil, err
	}
	days, err := s.Store.IstaDays(ctx, a.Cuid, f, t)
	if err != nil {
		return nil, err
	}
	firsts := map[string]bool{} // first day of each period window: starts from zero
	for _, p := range a.Periods {
		firsts[p.From] = true
	}
	d := &daily{
		units: map[string]map[int64]float64{}, ticks: map[string]map[int64]float64{},
		total: map[string]float64{}, building: map[string]float64{},
		cum: map[string]map[int64]float64{}, cumTotal: map[string]float64{}, cumBuild: map[string]float64{},
		est: map[string]bool{}, periodOf: map[string]int{},
	}
	cumC := map[string]map[int64]float64{}
	for _, r := range readings {
		if d.cum[r.Day] == nil {
			d.cum[r.Day], cumC[r.Day] = map[int64]float64{}, map[int64]float64{}
		}
		d.cum[r.Day][r.MeterID] = r.CCD
		cumC[r.Day][r.MeterID] = r.C
		d.periodOf[r.Day] = r.Y
		if r.Estimated {
			d.est[r.Day] = true
		}
	}
	// the building average runs to the end of the periods, so a dip later
	// in the period evens out the days before it (smoothBuilding)
	end := t
	for _, p := range a.Periods {
		if p.From <= t && p.End >= t && p.End > end {
			end = p.End
		}
	}
	all, err := s.Store.IstaDays(ctx, a.Cuid, f, end)
	if err != nil {
		return nil, err
	}
	for day, v := range smoothBuilding(all) {
		if day <= t {
			d.cumBuild[day] = v
		}
	}
	for _, x := range days {
		d.cumTotal[x.Day] = x.TotalNow
		d.periodOf[x.Day] = x.Y
		if x.Estimated {
			d.est[x.Day] = true
		}
	}
	for day := range d.cumTotal {
		dt, _ := time.Parse(DayLayout, day)
		prev := dt.AddDate(0, 0, -1).Format(DayLayout)
		first := firsts[day]
		if !first {
			if _, ok := d.cumTotal[prev]; !ok || d.periodOf[prev] != d.periodOf[day] {
				continue // unknown: the day before is missing
			}
		}
		d.units[day], d.ticks[day] = map[int64]float64{}, map[int64]float64{}
		for id, v := range d.cum[day] {
			if first {
				d.units[day][id], d.ticks[day][id] = v, cumC[day][id]
			} else if pv, ok := d.cum[prev][id]; ok {
				d.units[day][id], d.ticks[day][id] = v-pv, cumC[day][id]-cumC[prev][id]
			}
		}
		if first {
			d.total[day] = d.cumTotal[day]
		} else {
			d.total[day] = d.cumTotal[day] - d.cumTotal[prev]
		}
		if b, ok := d.cumBuild[day]; ok {
			if first {
				d.building[day] = b
			} else if pb, ok := d.cumBuild[prev]; ok {
				d.building[day] = b - pb
			}
		}
	}
	return d, nil
}

// smoothBuilding returns the cumulative building average per day, made
// non-decreasing within each period. ista's figure goes up and down from day
// to day (by hundreds of units, apparently as dwellings' readings come and
// go), which would give days with a negative building usage. Each day gets
// the lowest value of that day and the later days of its period: the latest
// figure stays as ista gives it, so the period total is unchanged, and a
// rise that is taken back later is not counted.
func smoothBuilding(days []store.IstaDay) map[string]float64 {
	ds := append([]store.IstaDay(nil), days...)
	sort.Slice(ds, func(i, j int) bool { return ds[i].Day < ds[j].Day })
	out := map[string]float64{}
	y, low := 0, math.Inf(1)
	for i := len(ds) - 1; i >= 0; i-- {
		x := ds[i]
		if x.BuildingAvg == nil {
			continue
		}
		if x.Y != y {
			y, low = x.Y, math.Inf(1)
		}
		low = math.Min(low, *x.BuildingAvg)
		out[x.Day] = low
	}
	return out
}

// Bucket is the usage over some days.
type Bucket struct {
	Start string `json:"start"`
	End   string `json:"end"` // inclusive
	// Units and Ticks per room, in the order of Usage.Rooms; null = unknown.
	Units    []*float64 `json:"units"`
	Ticks    []*float64 `json:"ticks"`
	Total    *float64   `json:"total"`
	Building *float64   `json:"building,omitempty"`
	// LastYear is the total of the same days a year earlier.
	LastYear  *float64 `json:"last_year,omitempty"`
	Days      int      `json:"days"`
	Missing   int      `json:"missing"`
	Estimated bool     `json:"estimated,omitempty"`
	// EstimatedDays counts the days of the bucket that ista estimated.
	EstimatedDays int `json:"estimated_days,omitempty"`

	key string // the bucket's own first day, before cutting
}

// Usage is the usage of an address per bucket.
type Usage struct {
	Cuid    string   `json:"cuid"`
	Unit    string   `json:"unit"`
	Bucket  string   `json:"bucket"`
	Rooms   []Room   `json:"rooms"`
	Buckets []Bucket `json:"buckets"`
	Total   Bucket   `json:"total"`
}

// Buckets the usage can be grouped by.
const (
	BucketDay    = "day"
	BucketWeek   = "week"
	BucketMonth  = "month"
	BucketPeriod = "period"
)

// bucketStart returns the first day of the bucket a day falls in.
func bucketStart(bucket string, d time.Time, periods []Period) time.Time {
	switch bucket {
	case BucketWeek:
		wd := (int(d.Weekday()) + 6) % 7 // Monday = 0
		return d.AddDate(0, 0, -wd)
	case BucketMonth:
		return time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, time.UTC)
	case BucketPeriod:
		ds := d.Format(DayLayout)
		for _, p := range periods {
			if ds >= p.Start && ds <= p.End {
				t, _ := time.Parse(DayLayout, p.Start)
				return t
			}
		}
		return d
	}
	return d
}

func bucketEnd(bucket string, start time.Time, periods []Period) time.Time {
	switch bucket {
	case BucketWeek:
		return start.AddDate(0, 0, 6)
	case BucketMonth:
		return start.AddDate(0, 1, -1)
	case BucketPeriod:
		for _, p := range periods {
			if p.Start == start.Format(DayLayout) {
				t, _ := time.Parse(DayLayout, p.End)
				return t
			}
		}
	}
	return start
}

func ptr(v float64) *float64 { return &v }

// Usage returns the usage of an address from from to to (inclusive days),
// grouped by bucket. Days before the move-in and after the last day ista
// measured are left out.
func (s *Service) Usage(ctx context.Context, cuid string, from, to time.Time, bucket string) (Usage, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return Usage{}, err
	}
	switch bucket {
	case BucketDay, BucketWeek, BucketMonth, BucketPeriod:
	default:
		return Usage{}, fmt.Errorf("ista: onbekende indeling %q", bucket)
	}
	if mi, err := time.Parse(DayLayout, a.MoveIn); err == nil && from.Before(mi) {
		from = mi
	}
	if today := s.today(); to.After(today) {
		to = today
	}
	u := Usage{Cuid: a.Cuid, Unit: a.Unit, Bucket: bucket, Rooms: a.Rooms, Buckets: []Bucket{}}
	if to.Before(from) {
		u.Total = s.emptyBucket(a, from, to)
		return u, nil
	}
	cur, err := s.loadDaily(ctx, a, from, to)
	if err != nil {
		return u, err
	}
	prev, err := s.loadDaily(ctx, a, from.AddDate(-1, 0, 0), to.AddDate(-1, 0, 0))
	if err != nil {
		return u, err
	}
	// only the days ista measured: its estimates after the last measured day
	// (and today, which is still running) are left out everywhere
	last := from.AddDate(0, 0, -1)
	if m, err := time.Parse(DayLayout, a.LastMeasured); err == nil {
		last = to
		if m.Before(last) {
			last = m
		}
	}
	if last.Before(from) {
		u.Total = s.emptyBucket(a, from, last)
		return u, nil
	}
	u.Total = s.emptyBucket(a, from, last)
	var b *Bucket
	for d := from; !d.After(last); d = d.AddDate(0, 0, 1) {
		bs := bucketStart(bucket, d, a.Periods)
		if b == nil || b.key != bs.Format(DayLayout) {
			if b != nil {
				u.Buckets = append(u.Buckets, *b)
			}
			// a bucket is cut at the edges of the range: a week that starts
			// before the move-in starts at the move-in
			start, end := bs, bucketEnd(bucket, bs, a.Periods)
			if start.Before(from) {
				start = from
			}
			if end.After(last) {
				end = last
			}
			nb := s.emptyBucket(a, start, end)
			nb.key = bs.Format(DayLayout)
			b = &nb
		}
		add := func(x *Bucket) {
			x.Days++
			ds := d.Format(DayLayout)
			units, ok := cur.units[ds]
			if !ok {
				x.Missing++
				return
			}
			if cur.est[ds] {
				x.Estimated = true
				x.EstimatedDays++
			}
			for i, r := range a.Rooms {
				if v, ok := units[r.ID]; ok {
					x.Units[i] = addp(x.Units[i], v)
					x.Ticks[i] = addp(x.Ticks[i], cur.ticks[ds][r.ID])
				}
			}
			x.Total = addp(x.Total, cur.total[ds])
			if v, ok := cur.building[ds]; ok {
				x.Building = addp(x.Building, v)
			}
			if v, ok := prev.total[d.AddDate(-1, 0, 0).Format(DayLayout)]; ok {
				x.LastYear = addp(x.LastYear, v)
			}
		}
		add(b)
		add(&u.Total)
	}
	if b != nil {
		u.Buckets = append(u.Buckets, *b)
	}
	return u, nil
}

func (s *Service) emptyBucket(a Address, start, end time.Time) Bucket {
	return Bucket{Start: start.Format(DayLayout), End: end.Format(DayLayout),
		Units: make([]*float64, len(a.Rooms)), Ticks: make([]*float64, len(a.Rooms))}
}

func addp(p *float64, v float64) *float64 {
	if p == nil {
		return ptr(v)
	}
	*p += v
	return p
}

// WeekRow is one week of a period's week table: the readings at the end of
// the week (cumulative from the period's start) and the usage in it.
type WeekRow struct {
	Start string `json:"start"`
	End   string `json:"end"`
	// Day is the day the readings are from (the last stored day of the week).
	Day       string     `json:"day"`
	Units     []*float64 `json:"units"` // cumulative per room
	Ticks     []*float64 `json:"ticks"`
	Total     *float64   `json:"total"`
	Building  *float64   `json:"building,omitempty"`
	Used      *float64   `json:"used"` // units in this week
	Estimated bool       `json:"estimated,omitempty"`
}

// Weeks is a period's week table.
type Weeks struct {
	Cuid   string    `json:"cuid"`
	Unit   string    `json:"unit"`
	Period Period    `json:"period"`
	Rooms  []Room    `json:"rooms"`
	Rows   []WeekRow `json:"rows"`
}

// Weeks returns the week table (Monday to Sunday, cut at the period's
// edges) of a period; y 0 is the running one.
func (s *Service) Weeks(ctx context.Context, cuid string, y int) (Weeks, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return Weeks{}, err
	}
	p, ok := pickPeriod(a, y, s.today())
	if !ok {
		return Weeks{}, ErrNoAddress
	}
	w := Weeks{Cuid: a.Cuid, Unit: a.Unit, Period: p, Rooms: a.Rooms, Rows: []WeekRow{}}
	from, _ := time.Parse(DayLayout, p.From)
	to, _ := time.Parse(DayLayout, p.End)
	if today := s.today(); to.After(today) {
		to = today
	}
	// only the days ista measured
	if m, err := time.Parse(DayLayout, a.LastMeasured); err != nil {
		return w, nil
	} else if m.Before(to) {
		to = m
	}
	if to.Before(from) {
		return w, nil
	}
	readings, err := s.Store.IstaReadings(ctx, a.Cuid, p.From, to.Format(DayLayout))
	if err != nil {
		return w, err
	}
	days, err := s.Store.IstaDays(ctx, a.Cuid, p.From, to.Format(DayLayout))
	if err != nil {
		return w, err
	}
	byDay := map[string]store.IstaDay{}
	for _, d := range days {
		byDay[d.Day] = d
	}
	build := smoothBuilding(days)
	rd := map[string]map[int64]store.IstaReading{}
	for _, r := range readings {
		if rd[r.Day] == nil {
			rd[r.Day] = map[int64]store.IstaReading{}
		}
		rd[r.Day][r.MeterID] = r
	}
	var prevTotal *float64 = ptr(0)
	for ws := bucketStart(BucketWeek, from, nil); !ws.After(to); ws = ws.AddDate(0, 0, 7) {
		start, end := ws, ws.AddDate(0, 0, 6)
		if start.Before(from) {
			start = from
		}
		if end.After(to) {
			end = to
		}
		row := WeekRow{Start: start.Format(DayLayout), End: end.Format(DayLayout),
			Units: make([]*float64, len(a.Rooms)), Ticks: make([]*float64, len(a.Rooms))}
		// the last stored day of the week
		for d := end; !d.Before(start); d = d.AddDate(0, 0, -1) {
			x, ok := byDay[d.Format(DayLayout)]
			if !ok {
				continue
			}
			row.Day = x.Day
			row.Total = ptr(x.TotalNow)
			if v, ok := build[x.Day]; ok {
				row.Building = ptr(v)
			}
			row.Estimated = x.Estimated
			for i, r := range a.Rooms {
				if v, ok := rd[x.Day][r.ID]; ok {
					row.Units[i], row.Ticks[i] = ptr(v.CCD), ptr(v.C)
				}
			}
			break
		}
		if row.Total != nil && prevTotal != nil {
			row.Used = ptr(*row.Total - *prevTotal)
		}
		prevTotal = row.Total
		w.Rows = append(w.Rows, row)
	}
	return w, nil
}

// pickPeriod returns the period y, or (y 0) the one today falls in, else
// the latest.
func pickPeriod(a Address, y int, today time.Time) (Period, bool) {
	if len(a.Periods) == 0 {
		return Period{}, false
	}
	ps := append([]Period(nil), a.Periods...)
	sort.Slice(ps, func(i, j int) bool { return ps[i].Y < ps[j].Y })
	if y != 0 {
		for _, p := range ps {
			if p.Y == y {
				return p, true
			}
		}
		return Period{}, false
	}
	ts := today.Format(DayLayout)
	for _, p := range ps {
		if ts >= p.Start && ts <= p.End {
			return p, true
		}
	}
	return ps[len(ps)-1], true
}

// round2 rounds to cents.
func round2(v float64) float64 { return math.Round(v*100) / 100 }
