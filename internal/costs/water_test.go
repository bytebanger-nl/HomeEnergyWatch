package costs

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// newWaterFixture is newFixture on the water timeline.
func newWaterFixture(t testing.TB, now time.Time) *fixture {
	fx := newFixture(t, now)
	fx.cs = &Contracts{Store: fx.st, Table: store.WaterContracts}
	fx.c = &Calculator{Store: fx.st, Contracts: fx.cs, Loc: ams, Now: func() time.Time { return now }}
	return fx
}

func waterDay(day time.Time, m3PerQuarter float64) []store.Quarter {
	var qs []store.Quarter
	for t := day; t.Before(day.AddDate(0, 0, 1)); t = t.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: t, WaterM3: f(m3PerQuarter)})
	}
	return qs
}

func TestWaterDayCosts(t *testing.T) {
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, ams)
	fx := newWaterFixture(t, day.AddDate(0, 0, 3))
	fx.contract(t, Contract{
		Name: "Vitens", Start: "2026-09-01",
		Water:   Water{RateCt: 110, TaxCt: 50},
		Monthly: []MonthlyCost{{Name: "Vastrecht", EUR: 7.3}},
	})
	fx.quarters(t, waterDay(day, 0.002)...) // 192 litres
	res, err := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Total
	if !near(b.WaterM3, 0.192) || !near(b.WaterEUR, 0.192*1.10) || !near(b.WaterTaxEUR, 0.192*0.50) {
		t.Errorf("water %v m³, %v + %v tax", b.WaterM3, b.WaterEUR, b.WaterTaxEUR)
	}
	if fixed := 7.3 * 12 / 365; !near(b.FixedEUR, fixed) || !near(b.TotalEUR, 0.192*1.60+fixed) {
		t.Errorf("fixed %v, total %v", b.FixedEUR, b.TotalEUR)
	}
	if !b.Complete || b.Missing.Quarters != 0 {
		t.Errorf("complete %v, missing %+v: water quarters without electricity must count as measured", b.Complete, b.Missing)
	}
	if b.ElectricityEUR != 0 || b.GasEUR != 0 {
		t.Errorf("no energy on the water timeline: %+v", b)
	}
}

func TestWaterMissingQuarters(t *testing.T) {
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, ams)
	fx := newWaterFixture(t, day.AddDate(0, 0, 1))
	fx.contract(t, Contract{Name: "Water", Start: "2026-01-01", Water: Water{RateCt: 100}})
	// an electricity-only quarter hour on the device does not count for water
	fx.quarters(t, append(waterDay(day, 0.001)[:90], store.Quarter{Start: day.Add(23 * time.Hour), ImportKWh: f(1)})...)
	res, err := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total.Missing.Quarters != 6 || res.Total.Complete {
		t.Errorf("missing %+v", res.Total.Missing)
	}
}

func TestWaterTaxCapPerYear(t *testing.T) {
	fx := newWaterFixture(t, time.Date(2027, 1, 3, 0, 0, 0, 0, ams))
	fx.contract(t, Contract{Name: "Water", Start: "2026-01-01", Water: Water{RateCt: 100, TaxCt: 50}})
	at := func(y int, m time.Month, d int, m3 float64) store.Quarter {
		return store.Quarter{Start: time.Date(y, m, d, 12, 0, 0, 0, ams), WaterM3: f(m3)}
	}
	fx.quarters(t, at(2026, 3, 1, 290), at(2026, 6, 1, 20), at(2027, 1, 2, 5))
	ctx := context.Background()
	res, err := fx.c.Calculate(ctx, "p1", time.Date(2026, 1, 1, 0, 0, 0, 0, ams), time.Date(2027, 1, 3, 0, 0, 0, 0, ams), BucketYear)
	if err != nil {
		t.Fatal(err)
	}
	y26, y27 := res.Buckets[0], res.Buckets[1]
	if !near(y26.WaterEUR, 310) || !near(y26.WaterTaxEUR, 150) { // tax on 300 of 310 m³
		t.Errorf("2026: %v + %v tax", y26.WaterEUR, y26.WaterTaxEUR)
	}
	if !near(y27.WaterTaxEUR, 2.5) { // the cap starts again on 1 January
		t.Errorf("2027 tax %v", y27.WaterTaxEUR)
	}
	// the June day alone still knows the volume of the months before it
	jun := time.Date(2026, 6, 1, 0, 0, 0, 0, ams)
	res, err = fx.c.Calculate(ctx, "p1", jun, jun.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if !near(res.Total.WaterTaxEUR, 5) {
		t.Errorf("June tax %v, want 10 m³ × 0.50", res.Total.WaterTaxEUR)
	}
}

func TestWaterTimelineIsSeparate(t *testing.T) {
	fx := newWaterFixture(t, time.Date(2026, 9, 1, 0, 0, 0, 0, ams))
	energy := &Contracts{Store: fx.st}
	ctx := context.Background()
	if _, err := energy.Save(ctx, Contract{Name: "Stroom", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30}}); err != nil {
		t.Fatal(err)
	}
	// the same start date is fine: water contracts do not overlap energy ones
	w, err := fx.cs.Save(ctx, Contract{Name: "Water", Start: "2026-01-01", Type: TypeDynamic, Water: Water{RateCt: 100}})
	if err != nil {
		t.Fatal(err)
	}
	if w.Type != TypeWater {
		t.Errorf("type %q, want water on the water timeline", w.Type)
	}
	if _, err := energy.Save(ctx, Contract{Name: "Water", Start: "2026-06-01", Type: TypeWater}); !errors.Is(err, ErrInvalid) {
		t.Errorf("water contract on the energy timeline: %v", err)
	}
	if _, err := fx.cs.Save(ctx, Contract{Name: "Fout", Start: "2026-02-01", Water: Water{RateCt: -1}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("negative water rate: %v", err)
	}
	es, _ := energy.List(ctx)
	ws, _ := fx.cs.List(ctx)
	if len(es) != 1 || len(ws) != 1 || ws[0].Name != "Water" {
		t.Errorf("energy %v, water %v", es, ws)
	}
}

func TestWaterAdvanceOverChain(t *testing.T) {
	now := time.Date(2026, 7, 1, 0, 0, 0, 0, ams)
	fx := newWaterFixture(t, now)
	fx.contract(t, Contract{
		Name: "Water 2026", Start: "2026-01-01", End: "2026-06-30",
		Water: Water{RateCt: 100}, Monthly: []MonthlyCost{{Name: "Vastrecht", EUR: 5}},
		Advance:  &Advance{Amounts: []AdvanceAmount{{From: "2026-01-01", EUR: 15}}},
		Estimate: &Estimate{WaterM3: 120},
	})
	fx.contract(t, Contract{
		Name: "Water 2026 nieuw tarief", Start: "2026-07-01", Continues: true,
		Water: Water{RateCt: 200}, Monthly: []MonthlyCost{{Name: "Vastrecht", EUR: 5}},
		Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-07-01", EUR: 20}}},
	})
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil {
		t.Fatal(err)
	}
	if st == nil || st.Forecast == nil {
		t.Fatalf("status %+v", st)
	}
	if st.Contracts != 2 || st.ContractName != "Water 2026" {
		t.Errorf("chain: %d contracts, name %q", st.Contracts, st.ContractName)
	}
	f := st.Forecast
	if f.Method.Sources["water_m3"] != SourceContract || f.Method.Yearly.WaterM3 != 120 {
		t.Errorf("method %+v", f.Method)
	}
	// no measurements: the whole year comes from the profile, 120 m³ in all
	if total := st.CostEUR + f.RemainingCostEUR; total < 60+120 {
		t.Errorf("total cost %v", total)
	}
	if f.WaterM3 < 55 || f.WaterM3 > 65 { // second half of the year, a bit more than half by the profile
		t.Errorf("remaining water %v m³", f.WaterM3)
	}
	if !near(f.TotalPaidEUR, 6*15+6*20) {
		t.Errorf("paid %v", f.TotalPaidEUR)
	}
}
