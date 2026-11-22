package costs

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// A scenario costs the same measured quarters under another contract, only
// on the days the real contracts cover.
func TestCompareFixedScenarioWithDynamicContract(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 2))
	fx.contract(t, Contract{Name: "Dynamisch", Start: "2026-06-10", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 10}})
	// one kWh at noon on the 9th (no contract) and on the 10th
	for _, d := range []time.Time{day.AddDate(0, 0, -1), day} {
		q := d.Add(12 * time.Hour)
		fx.prices(t, prices.Market, q, 15*time.Minute, 100)
		fx.quarters(t, store.Quarter{Start: q, ImportKWh: f(1), ExportKWh: f(0)})
	}
	sc := Scenario{Name: "Vast 30 ct", Contract: Contract{Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}}}
	sc.Normalize()
	cmp, err := fx.c.Compare(context.Background(), "p1", []Scenario{sc}, day.AddDate(0, 0, -1), day.AddDate(0, 0, 2), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cmp.Actual.Total.TotalEUR, 1*(0.121+0.02)+0.10; !near(got, want) {
		t.Fatalf("actual %v, want %v", got, want)
	}
	s := cmp.Scenarios[0].Result
	if got := s.Total.TotalEUR; !near(got, 0.40) {
		t.Fatalf("scenario %v, want 0.40 (the 9th has no real contract, so not counted)", got)
	}
	if s.Total.Missing.ContractDays != cmp.Actual.Total.Missing.ContractDays {
		t.Fatalf("contract days differ: %d vs %d", s.Total.Missing.ContractDays, cmp.Actual.Total.Missing.ContractDays)
	}
}

// With the 2027 rules a scenario of 2026 has no net metering and feed-in
// at the bare market price.
func TestCompareRules2027(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 1))
	contract := Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10, ExportCt: 5}}
	fx.contract(t, contract)
	fx.quarters(t,
		store.Quarter{Start: day.Add(9 * time.Hour), ImportKWh: f(2), ExportKWh: f(0)},
		store.Quarter{Start: day.Add(12 * time.Hour), ImportKWh: f(0), ExportKWh: f(1)})
	same := Scenario{Name: "Zelfde", Contract: contract}
	future := Scenario{Name: "Zelfde in 2027", Contract: contract, Rules2027: true}
	same.Normalize()
	future.Normalize()
	cmp, err := fx.c.Compare(context.Background(), "p1", []Scenario{same, future}, day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	// 2 kWh × 40 ct − 1 kWh × 5 ct − 1 kWh netted × (40 − 5) ct
	if got := cmp.Scenarios[0].Result.Total.TotalEUR; !near(got, cmp.Actual.Total.TotalEUR) || !near(got, 0.80-0.05-0.35) {
		t.Fatalf("same contract %v, actual %v", got, cmp.Actual.Total.TotalEUR)
	}
	if got := cmp.Scenarios[1].Result.Total.TotalEUR; !near(got, 0.80-0.05) {
		t.Fatalf("2027 rules %v, want 0.75 (no net metering)", got)
	}
}

func TestScenarioValidationAndStorage(t *testing.T) {
	fx := newFixture(t, date(2026, 6, 1))
	r := &Scenarios{Store: fx.st}
	if _, err := r.Save(context.Background(), Scenario{Name: "", Contract: Contract{Type: TypeFixed}}); err == nil {
		t.Fatal("scenario without name accepted")
	}
	if _, err := r.Save(context.Background(), Scenario{Name: "x", Contract: Contract{Type: "huur"}}); err == nil {
		t.Fatal("unknown contract type accepted")
	}
	saved, err := r.Save(context.Background(), Scenario{Name: " Aanbieding ", Rules2027: true,
		Contract: Contract{Type: TypeFixed, Start: "2026-01-01", Electricity: Electricity{RateCt: 25},
			Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-01-01", EUR: 10}}}, Continues: true}})
	if err != nil {
		t.Fatal(err)
	}
	list, err := r.List(context.Background())
	if err != nil || len(list) != 1 {
		t.Fatalf("%v %v", list, err)
	}
	got := list[0]
	if got.ID != saved.ID || got.Name != "Aanbieding" || !got.Rules2027 || got.Contract.Start != "" || got.Contract.Advance != nil || got.Contract.Continues || math.Abs(got.Contract.Electricity.RateCt-25) > 0 {
		t.Fatalf("stored %+v", got)
	}
	if err := r.Delete(context.Background(), saved.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := r.List(context.Background()); len(list) != 0 {
		t.Fatalf("not deleted: %v", list)
	}
}
