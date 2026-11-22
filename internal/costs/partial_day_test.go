package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// A past day asked for only in part (a request ending mid-day, e.g. a
// browser in another time zone) is not cached as the whole day.
func TestPartialDayNotCached(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 5))
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10}, Monthly: []MonthlyCost{{Name: "Vast", EUR: 30}}})
	var qs []store.Quarter
	for s := day; s.Before(day.AddDate(0, 0, 1)); s = s.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: s, ImportKWh: f(0.1)})
	}
	fx.quarters(t, qs...)
	ctx := context.Background()
	half, err := fx.c.Calculate(ctx, "p1", day, day.Add(12*time.Hour), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if !near(half.Total.ImportKWh, 4.8) {
		t.Fatalf("half day: %v kWh", half.Total.ImportKWh)
	}
	full, err := fx.c.Calculate(ctx, "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if !near(full.Total.ImportKWh, 9.6) || !near(full.Total.FixedEUR, 30*12/365.0) {
		t.Fatalf("whole day after half: %v kWh, %v fixed", full.Total.ImportKWh, full.Total.FixedEUR)
	}
}

// Hours across 1 January: net metering starts over, as in the day view.
func TestHourlyAcrossNewYear(t *testing.T) {
	dec31 := date(2025, 12, 31)
	jan1 := date(2026, 1, 1)
	fx := newFixture(t, jan1.AddDate(0, 0, 3))
	fx.contract(t, Contract{Name: "Vast", Start: "2025-01-01", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10, ExportCt: 5}})
	fx.quarters(t, store.Quarter{Start: dec31.Add(12 * time.Hour), ImportKWh: f(0), ExportKWh: f(5)})
	fx.quarters(t, store.Quarter{Start: jan1.Add(12 * time.Hour), ImportKWh: f(2), ExportKWh: f(0)})
	ctx := context.Background()
	hours, err := fx.c.Calculate(ctx, "p1", dec31, jan1.AddDate(0, 0, 1), BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	days, err := fx.c.Calculate(ctx, "p1", jan1, jan1.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	var jan1Netting float64
	for _, b := range hours.Buckets {
		if !b.Start.Before(jan1) {
			jan1Netting += b.NettingEUR
		}
	}
	if !near(jan1Netting, days.Total.NettingEUR) {
		t.Fatalf("1 January netting per hour %v, per day %v", jan1Netting, days.Total.NettingEUR)
	}
	if days.Total.NettingEUR != 0 {
		t.Fatalf("netting %v on 1 January: last year's export carried over", days.Total.NettingEUR)
	}
}

// Last year's day on a DST day this year: one quarter hour per slot that
// exists this year, none twice and none missing.
func TestOnDayDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip(err)
	}
	day := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, loc) }
	quarters := func(d time.Time) []store.Quarter {
		var qs []store.Quarter
		for s := d; s.Before(d.AddDate(0, 0, 1)); s = s.Add(15 * time.Minute) {
			qs = append(qs, store.Quarter{Start: s, ImportKWh: f(0.1)})
		}
		return qs
	}
	for _, c := range []struct {
		last, this time.Time
		want       int
	}{
		{day(2025, 3, 29), day(2026, 3, 29), 92},    // this year springs forward
		{day(2025, 10, 25), day(2026, 10, 25), 100}, // this year falls back
		{day(2025, 3, 30), day(2026, 3, 30), 96},    // last year sprang forward
	} {
		out := onDay(quarters(c.last), c.this, loc)
		seen := map[int64]bool{}
		for _, q := range out {
			if seen[q.Start.Unix()] || q.Start.Before(c.this) || !q.Start.Before(c.this.AddDate(0, 0, 1)) {
				t.Fatalf("%s: quarter %v twice or outside the day", c.this.Format("2006-01-02"), q.Start)
			}
			seen[q.Start.Unix()] = true
		}
		if len(out) != c.want {
			t.Errorf("%s: %d quarters, want %d", c.this.Format("2006-01-02"), len(out), c.want)
		}
	}
}
