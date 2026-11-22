package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Gaps finds the same as Missing of Calculate, as merged periods.
func TestGapsMatchCalculate(t *testing.T) {
	day := date(2026, 9, 7)
	next := day.AddDate(0, 0, 1)
	fx := newFixture(t, next.Add(12*time.Hour))
	// no contract on the first day
	fx.contract(t, Contract{Name: "Dynamisch", Start: "2026-09-08", Type: TypeDynamic, Electricity: Electricity{MarkupCt: 2}})
	var qs []store.Quarter
	for s := day; s.Before(next.Add(12 * time.Hour)); s = s.Add(15 * time.Minute) {
		if !s.Before(next) && s.Before(next.Add(time.Hour)) {
			continue // 00:00-01:00 on the second day not measured
		}
		q := store.Quarter{Start: s, ImportKWh: f(0.1), ExportKWh: f(0)}
		switch s {
		case next.Add(3 * time.Hour):
			q.Quality = "partial"
		case next.Add(4 * time.Hour):
			q.Quality, q.ImportKWh = "anomalous", nil
		}
		qs = append(qs, q)
	}
	fx.quarters(t, qs...)
	// prices for the second day, except 10:00-10:30
	var ps []float64
	for i := 0; i < 96; i++ {
		ps = append(ps, 100)
	}
	fx.prices(t, prices.Market, next, 15*time.Minute, ps[:40]...)
	fx.prices(t, prices.Market, next.Add(10*time.Hour+30*time.Minute), 15*time.Minute, ps[42:]...)

	ctx := context.Background()
	res, err := fx.c.Calculate(ctx, "p1", day, next.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	g, err := fx.c.Gaps(ctx, "p1", day, next.AddDate(0, 0, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if g.Missing != res.Total.Missing {
		t.Errorf("gaps count %+v, costs %+v", g.Missing, res.Total.Missing)
	}
	want := []Gap{
		{Kind: GapQuarters, From: next, To: next.Add(time.Hour), Count: 4},
		{Kind: GapPartial, From: next.Add(3 * time.Hour), To: next.Add(3*time.Hour + 15*time.Minute), Count: 1},
		{Kind: GapAnomalous, From: next.Add(4 * time.Hour), To: next.Add(4*time.Hour + 15*time.Minute), Count: 1},
		{Kind: GapPrices, From: next.Add(10 * time.Hour), To: next.Add(10*time.Hour + 30*time.Minute), Count: 2},
		{Kind: GapContract, From: day, To: next, Count: 1},
	}
	if len(g.Gaps) != len(want) {
		t.Fatalf("gaps %+v", g.Gaps)
	}
	for i, w := range want {
		if got := g.Gaps[i]; got.Kind != w.Kind || !got.From.Equal(w.From) || !got.To.Equal(w.To) || got.Count != w.Count {
			t.Errorf("gap %d: %+v, want %+v", i, got, w)
		}
	}
}

// Quarters with only gas (an imported gas export) count their missing gas
// prices in Gaps as in Calculate.
func TestGapsGasOnly(t *testing.T) {
	day := date(2025, 11, 3)
	fx := newFixture(t, day.AddDate(0, 0, 2))
	fx.contract(t, Contract{Name: "Dynamisch", Start: "2025-01-01", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2}, Gas: Gas{MarkupCt: 5}})
	var qs []store.Quarter
	for s := day; s.Before(day.AddDate(0, 0, 1)); s = s.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: s, GasM3: f(0.01)})
	}
	fx.quarters(t, qs...)
	ctx := context.Background()
	res, err := fx.c.Calculate(ctx, "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	g, err := fx.c.Gaps(ctx, "p1", day, day.AddDate(0, 0, 1), false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total.Missing.GasPrices == 0 {
		t.Fatalf("no missing gas prices in the costs: %+v", res.Total.Missing)
	}
	if g.Missing != res.Total.Missing {
		t.Errorf("gaps count %+v, costs %+v", g.Missing, res.Total.Missing)
	}
}
