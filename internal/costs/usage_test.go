package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Usage prices each quarter hour's import at the price of that moment, with
// markup, tax and VAT, and leaves out export, gas and fixed costs.
func TestUsageImportOnlyAtPriceOfTheMoment(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 1))
	fx.contract(t, Contract{Name: "Dynamisch", Start: "2026-01-01", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 10, ExportCt: -1}, Gas: Gas{MarkupCt: 5, TaxCt: 60},
		Monthly: []MonthlyCost{{Name: "Vast", EUR: 30}}})
	q1, q2, q3 := day.Add(9*time.Hour), day.Add(13*time.Hour), day.Add(20*time.Hour)
	fx.prices(t, prices.Market, q1, 15*time.Minute, 100)
	fx.prices(t, prices.Market, q2, 15*time.Minute, -50)
	fx.historySince(t, day.AddDate(0, 0, -1))
	fx.quarters(t,
		store.Quarter{Start: q1, ImportKWh: f(1), ExportKWh: f(0.5), GasM3: f(1)},
		store.Quarter{Start: q2, ImportKWh: f(2), ExportKWh: f(3)},
		store.Quarter{Start: q3, ImportKWh: f(1)}) // no price
	res, err := fx.c.Usage(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Total
	// 1 kWh × (0.100 × 1.21 + 0.02) + 2 kWh × (−0.050 × 1.21 + 0.02); tax on all 4 kWh
	if got, want := b.ElectricityEUR, 1*(0.121+0.02)+2*(-0.0605+0.02); !near(got, want) {
		t.Errorf("electricity %v, want %v", got, want)
	}
	if got, want := b.ElectricityTaxEUR, 4*0.10; !near(got, want) {
		t.Errorf("tax %v, want %v", got, want)
	}
	if b.ExportEUR != 0 || b.NettingEUR != 0 || b.GasEUR != 0 || b.GasTaxEUR != 0 || b.FixedEUR != 0 || b.ExportKWh != 0 || b.GasM3 != 0 {
		t.Errorf("export, gas or fixed counted: %+v", b)
	}
	if b.Complete || b.Missing.Prices != 1 || b.Missing.Quarters != 96-3 {
		t.Errorf("missing %+v complete %v, want 1 price and 93 quarters missing", b.Missing, b.Complete)
	}
}

// A period across New Year counts every day once.
func TestUsageAcrossYears(t *testing.T) {
	from := date(2025, 12, 31)
	fx := newFixture(t, from.AddDate(0, 0, 3))
	fx.contract(t, Contract{Name: "Vast", Start: "2025-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}})
	for i := range 3 {
		fx.quarters(t, store.Quarter{Start: from.AddDate(0, 0, i).Add(12 * time.Hour), ImportKWh: f(1)})
	}
	res, err := fx.c.Usage(context.Background(), "p1", from, from.AddDate(0, 0, 3), BucketYear)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Buckets) != 2 || !near(res.Total.ImportKWh, 3) || !near(res.Total.TotalEUR, 3*0.40) {
		t.Fatalf("buckets %d, import %v, total %v; want 2 years, 3 kWh, 1.20", len(res.Buckets), res.Total.ImportKWh, res.Total.TotalEUR)
	}
}

// Quarter hours that the recorder cannot have stored yet (within Settle of
// now) are not missing, so a measured running day is complete; the cache
// only keeps a finished day once its last quarter hour is settled.
func TestSettleNotMissing(t *testing.T) {
	day := date(2026, 6, 10)
	now := day.Add(10*time.Hour + 3*time.Minute)
	fx := newFixture(t, now)
	fx.c.Settle = 6 * time.Minute // quarters ending after 09:57 may still come
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}})
	var qs []store.Quarter
	for s := day; s.Before(day.Add(9*time.Hour + 45*time.Minute)); s = s.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: s, ImportKWh: f(0.1)}) // up to 09:30-09:45
	}
	fx.quarters(t, qs...)
	for name, calc := range map[string]func() (*Result, error){
		"costs": func() (*Result, error) {
			return fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
		},
		"usage": func() (*Result, error) {
			return fx.c.Usage(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
		},
	} {
		res, err := calc()
		if err != nil {
			t.Fatal(err)
		}
		if b := res.Total; !b.Complete || b.Missing.Quarters != 0 {
			t.Errorf("%s: missing %+v, want complete", name, b.Missing)
		}
	}
	fx.c.Settle = 0
	res, err := fx.c.Usage(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total.Missing.Quarters != 1 {
		t.Errorf("without settle: %d quarters missing, want 1 (09:45)", res.Total.Missing.Quarters)
	}

	// just after midnight the day before is finished but not settled: not cached
	fx2 := newFixture(t, day.Add(2*time.Minute))
	fx2.c.Settle = 6 * time.Minute
	fx2.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}})
	if _, err := fx2.c.Calculate(context.Background(), "p1", day.AddDate(0, 0, -1), day, BucketDay); err != nil {
		t.Fatal(err)
	}
	prev := day.AddDate(0, 0, -1)
	jan1 := date(2026, 1, 1)
	if got, want := fx2.c.cached(), int(prev.Sub(jan1).Hours()/24+0.5); got != want {
		t.Errorf("cached %d days, want %d (1 January up to the day before yesterday)", got, want)
	}
}

// A quarter hour the recorder still recomputes (within Settle) is priced, but
// neither counted as estimated nor as missing until it is final: a gap that
// fills itself a few minutes later is never shown. Gaps agrees.
func TestSettleRecentPartialNotCounted(t *testing.T) {
	day := date(2026, 6, 10)
	now := day.Add(10*time.Hour + 3*time.Minute)
	fx := newFixture(t, now)
	fx.c.Settle = 61 * time.Minute // quarters ending after 09:02 may still change
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}})
	var qs []store.Quarter
	for s := day; s.Before(day.Add(9 * time.Hour)); s = s.Add(15 * time.Minute) {
		q := store.Quarter{Start: s, ImportKWh: f(0.1), Quality: history.QualityComplete}
		if s.Equal(day.Add(8 * time.Hour)) {
			q.Quality = history.QualityPartial // final: counted
		}
		qs = append(qs, q)
	}
	// 09:00 estimated and 09:30 missing, both still within Settle
	qs = append(qs, store.Quarter{Start: day.Add(9 * time.Hour), ImportKWh: f(0.1), Quality: history.QualityPartial},
		store.Quarter{Start: day.Add(9*time.Hour + 15*time.Minute), ImportKWh: f(0.1), Quality: history.QualityComplete})
	fx.quarters(t, qs...)
	res, err := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Total.Missing
	if m.Partial != 1 || m.Quarters != 0 {
		t.Errorf("missing %+v, want only the final 08:00 quarter estimated", m)
	}
	if !near(res.Total.ImportKWh, 3.8) {
		t.Errorf("import %v, want 3.8 (recent quarters priced)", res.Total.ImportKWh)
	}
	gaps, err := fx.c.Gaps(context.Background(), "p1", day, day.AddDate(0, 0, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if gaps.Missing != m {
		t.Errorf("gaps %+v, calculate %+v", gaps.Missing, m)
	}
}

// Before the history of a meter starts (its first complete quarter hour)
// nothing is missing: not the days before it, also without a contract, and
// not the quarter hours of its first day before it, nor the estimated first
// quarter of the gateway's start. After it every gap counts. Gaps agrees.
func TestNothingMissingBeforeHistory(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 1))
	fx.contract(t, Contract{Name: "Vast", Start: "2026-06-10", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}})
	first := day.Add(10 * time.Hour)
	qs := []store.Quarter{{Start: first.Add(-15 * time.Minute), ImportKWh: f(0.05), Quality: history.QualityPartial}}
	for s := first; s.Before(first.Add(2 * time.Hour)); s = s.Add(15 * time.Minute) {
		if !s.Equal(first.Add(time.Hour)) { // 11:00 not measured
			qs = append(qs, store.Quarter{Start: s, ImportKWh: f(0.1)})
		}
	}
	fx.quarters(t, qs...)
	from := day.AddDate(0, 0, -2) // two days without contract or data
	res, err := fx.c.Calculate(context.Background(), "p1", from, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Total.Missing
	// 10:00-24:00 is 56 quarters, 7 measured
	if m.Quarters != 49 || m.Partial != 0 || m.ContractDays != 0 {
		t.Errorf("missing %+v, want 49 quarters after the start, nothing before it", m)
	}
	if !near(res.Total.ImportKWh, 0.75) {
		t.Errorf("import %v, want 0.75 (the estimated first quarter still priced)", res.Total.ImportKWh)
	}
	gaps, err := fx.c.Gaps(context.Background(), "p1", from, day.AddDate(0, 0, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if gaps.Missing != m || gaps.MeasuredFrom == nil || !gaps.MeasuredFrom.Equal(first) {
		t.Errorf("gaps %+v (from %v), calculate %+v", gaps.Missing, gaps.MeasuredFrom, m)
	}
}
