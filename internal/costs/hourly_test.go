package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Per hour, the hours of a day add up to the day itself: the same quarters
// priced the same way, net metering starting from the state at midnight, and
// the fixed costs spread over the hours.
func TestHourlyAddsUpToTheDay(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 2))
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10, ExportCt: 5}, Gas: Gas{RateCt: 120, TaxCt: 60},
		Monthly: []MonthlyCost{{Name: "Vast", EUR: 36.5}}})
	// the day before: export that the day nets against
	fx.quarters(t, store.Quarter{Start: day.AddDate(0, 0, -1).Add(12 * time.Hour), ImportKWh: f(0), ExportKWh: f(2)})
	var qs []store.Quarter
	for s := day; s.Before(day.AddDate(0, 0, 1)); s = s.Add(15 * time.Minute) {
		q := store.Quarter{Start: s, ImportKWh: f(0.1), ExportKWh: f(0), GasM3: f(0.01)}
		if s.Hour() == 3 && s.Minute() == 15 {
			q.Quality = "partial"
		}
		if s.Hour() == 5 && s.Minute() == 30 {
			continue // missing
		}
		qs = append(qs, q)
	}
	fx.quarters(t, qs...)
	ctx := context.Background()

	days, err := fx.c.Calculate(ctx, "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	hours, err := fx.c.Calculate(ctx, "p1", day, day.AddDate(0, 0, 1), BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	if len(hours.Buckets) != 24 {
		t.Fatalf("%d hour buckets, want 24", len(hours.Buckets))
	}
	d, hs := days.Total, hours.Total
	for name, pair := range map[string][2]float64{
		"import": {d.ImportKWh, hs.ImportKWh}, "electricity": {d.ElectricityEUR, hs.ElectricityEUR},
		"tax": {d.ElectricityTaxEUR, hs.ElectricityTaxEUR}, "netting": {d.NettingEUR, hs.NettingEUR},
		"gas": {d.GasEUR + d.GasTaxEUR, hs.GasEUR + hs.GasTaxEUR}, "fixed": {d.FixedEUR, hs.FixedEUR}, "total": {d.TotalEUR, hs.TotalEUR},
	} {
		if !near(pair[0], pair[1]) {
			t.Errorf("%s: day %v, hours %v", name, pair[0], pair[1])
		}
	}
	if d.NettingEUR == 0 {
		t.Error("no netting: the export of the day before was not carried into the day")
	}
	if want := 36.5 * 12 / 365 / 24; !near(hours.Buckets[0].FixedEUR, want) {
		t.Errorf("fixed per hour %v, want %v", hours.Buckets[0].FixedEUR, want)
	}
	if m := hours.Buckets[3].Missing; m.Partial != 1 || hours.Buckets[3].Complete {
		t.Errorf("hour 3: %+v, want 1 partial quarter", m)
	}
	if m := hours.Buckets[5].Missing; m.Quarters != 1 {
		t.Errorf("hour 5: %+v, want 1 missing quarter", m)
	}
	if hs.Missing != d.Missing {
		t.Errorf("missing per hour %+v, per day %+v", hs.Missing, d.Missing)
	}
}

// Today stops at now, and usage per hour leaves out fixed costs.
func TestHourlyUsageUpToNow(t *testing.T) {
	day := date(2026, 6, 10)
	now := day.Add(10*time.Hour + 20*time.Minute)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10},
		Monthly: []MonthlyCost{{Name: "Vast", EUR: 30}}})
	var qs []store.Quarter
	for s := day; s.Before(day.Add(10 * time.Hour)); s = s.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: s, ImportKWh: f(0.25)})
	}
	fx.quarters(t, qs...)
	res, err := fx.c.Usage(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Buckets) != 11 { // 00:00 up to and including the running hour 10:00
		t.Fatalf("%d hours, want 11", len(res.Buckets))
	}
	if b := res.Buckets[2]; !near(b.ImportKWh, 1) || !near(b.TotalEUR, 0.40) || b.FixedEUR != 0 {
		t.Errorf("hour 2: %+v, want 1 kWh for € 0,40 without fixed costs", b)
	}
	if _, err := fx.c.Usage(context.Background(), "p1", day, day.AddDate(0, 0, 9), BucketHour); err == nil {
		t.Error("9 days per hour accepted")
	}
}
