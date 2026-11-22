package costs

import (
	"context"
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// A split contract prices each part by its own type: dynamic electricity
// with fixed gas, and fixed electricity with dynamic gas.
func TestSplitContractTypesPerPart(t *testing.T) {
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, ams)
	q := day.Add(10 * time.Hour)
	setup := func(t *testing.T, c Contract) Bucket {
		fx := newFixture(t, day.AddDate(0, 0, 3))
		fx.contract(t, c)
		fx.prices(t, prices.Market, q, 15*time.Minute, 100)
		gasFrom, _ := prices.GasDayBounds(day.AddDate(0, 0, -1))
		fx.prices(t, prices.GasMarket, gasFrom, 24*time.Hour, 0.5, 0.6)
		fx.quarters(t, store.Quarter{Start: q, ImportKWh: f(1), ExportKWh: f(0), GasM3: f(2)})
		res, err := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
		if err != nil {
			t.Fatal(err)
		}
		return res.Buckets[0]
	}
	t.Run("dynamic electricity, fixed gas", func(t *testing.T) {
		b := setup(t, Contract{Name: "Tibber / Vattenfall", Start: "2026-09-01", Type: TypeDynamic, Split: true, GasType: TypeFixed,
			Electricity: Electricity{MarkupCt: 2, TaxCt: 11}, Gas: Gas{MarkupCt: 5, RateCt: 130, TaxCt: 70}})
		if !near(b.ElectricityEUR, 0.100*1.21+0.02) || !near(b.GasEUR, 2*1.30) || !near(b.GasTaxEUR, 1.4) {
			t.Fatalf("electricity %v gas %v tax %v", b.ElectricityEUR, b.GasEUR, b.GasTaxEUR)
		}
		if b.Missing.GasPrices != 0 || !near(b.gasVar, 2*1.30+1.4) {
			t.Fatalf("missing %+v, gas part %v", b.Missing, b.gasVar)
		}
	})
	t.Run("fixed electricity, dynamic gas", func(t *testing.T) {
		b := setup(t, Contract{Name: "Vast / Dynamisch", Start: "2026-09-01", Type: TypeFixed, Split: true, GasType: TypeDynamic,
			Electricity: Electricity{RateCt: 30, MarkupCt: 2, TaxCt: 11}, Gas: Gas{MarkupCt: 5, RateCt: 130, TaxCt: 70}})
		if !near(b.ElectricityEUR, 0.30) || !near(b.GasEUR, 2*(0.6*1.21+0.05)) {
			t.Fatalf("electricity %v gas %v", b.ElectricityEUR, b.GasEUR)
		}
	})
	t.Run("not split: one type for both", func(t *testing.T) {
		// a stale gas type is ignored without split
		b := setup(t, Contract{Name: "Dynamisch", Start: "2026-09-01", Type: TypeDynamic, GasType: TypeFixed,
			Electricity: Electricity{MarkupCt: 2, TaxCt: 11}, Gas: Gas{MarkupCt: 5, RateCt: 130, TaxCt: 70}})
		if !near(b.GasEUR, 2*(0.6*1.21+0.05)) || b.gasVar != 0 {
			t.Fatalf("gas %v, gas part %v", b.GasEUR, b.gasVar)
		}
	})
}

func TestSplitNormalizeAndValidate(t *testing.T) {
	adv := &Advance{Amounts: []AdvanceAmount{{From: "2026-01-01", EUR: 40}}}
	// without split the gas fields are dropped
	c := Contract{Name: "x", Start: "2026-01-01", Type: TypeFixed, GasType: TypeDynamic, GasAdvance: adv,
		Monthly: []MonthlyCost{{Name: "Vastrecht gas", EUR: 6, Part: PartGas}}}
	c.Normalize()
	if c.GasType != "" || c.GasAdvance != nil || c.Monthly[0].Part != "" {
		t.Fatalf("not dropped: %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	// with split a cost without part belongs to electricity
	c = Contract{Name: "x", Start: "2026-01-01", Type: TypeDynamic, Split: true, GasType: " Fixed ", GasAdvance: adv,
		Monthly: []MonthlyCost{{Name: "Vermindering energiebelasting", EUR: -50}, {Name: "Vastrecht gas", EUR: 6, Part: PartGas}}}
	c.Normalize()
	if c.GasType != TypeFixed || c.Monthly[0].Part != PartElectricity || c.GasAdvance == nil {
		t.Fatalf("normalized %+v", c)
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.monthlyGasEUR(); got != 6 {
		t.Fatalf("gas monthly %v", got)
	}
	for name, bad := range map[string]Contract{
		"no gas type":     {Name: "x", Start: "2026-01-01", Type: TypeFixed, Split: true},
		"bad gas type":    {Name: "x", Start: "2026-01-01", Type: TypeFixed, Split: true, GasType: TypeWater},
		"water split":     {Name: "x", Start: "2026-01-01", Type: TypeWater, Split: true, GasType: TypeFixed},
		"bad part":        {Name: "x", Start: "2026-01-01", Type: TypeFixed, Split: true, GasType: TypeFixed, Monthly: []MonthlyCost{{Name: "y", EUR: 1, Part: "water"}}},
		"gas advance out": {Name: "x", Start: "2026-01-01", Type: TypeFixed, Split: true, GasType: TypeFixed, GasAdvance: &Advance{Amounts: []AdvanceAmount{{From: "2025-12-01", EUR: 1}}}},
	} {
		bad.Normalize()
		if bad.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	// a contract stored before split existed reads as not split
	var old Contract
	if err := json.Unmarshal([]byte(`{"name":"x","start":"2026-01-01","type":"fixed","advance":{"amounts":[{"from":"2026-01-01","eur":100}]}}`), &old); err != nil {
		t.Fatal(err)
	}
	if old.Split || old.gasType() != TypeFixed || old.advanceOn("2026-03-01", "") != 100 || old.advanceOn("2026-03-01", PartGas) != 0 {
		t.Fatalf("old contract %+v", old)
	}
}

// A split contract has a status per part: each part's advance against its
// own costs (the gas part: gas supply, gas tax and the gas fixed costs),
// and together they add up to the costs of the whole contract.
func TestSplitAdvances(t *testing.T) {
	now := date(2026, 12, 1)
	// November measured, December from last year
	base := Contract{Name: "Stroom / Gas", Start: "2026-11-01", End: "2026-12-31", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10}, Gas: Gas{RateCt: 100, TaxCt: 50},
		Monthly: []MonthlyCost{{Name: "Netbeheer stroom", EUR: 36.5}, {Name: "Vermindering energiebelasting", EUR: -30},
			{Name: "Vastrecht gas", EUR: 18.25, Part: PartGas}}}
	setup := func(t *testing.T, c Contract) *fixture {
		fx := newFixture(t, now)
		fx.contract(t, c)
		// the previous period (from 1 November 2025) and this November
		measuredDays(fx, t, date(2025, 11, 1), now, 0.1, 0.02)
		return fx
	}

	whole := base
	whole.Advance = &Advance{Amounts: []AdvanceAmount{{From: "2026-11-15", EUR: 150}}}
	all, err := setup(t, whole).c.Advances(context.Background(), "p1")
	if err != nil || len(all) != 1 || all[0].Part != "" {
		t.Fatalf("not split: %v %+v", err, all)
	}
	total := all[0]

	split := base
	split.Split, split.GasType = true, TypeFixed
	split.Advance = &Advance{Amounts: []AdvanceAmount{{From: "2026-11-15", EUR: 90}}}
	split.GasAdvance = &Advance{Amounts: []AdvanceAmount{{From: "2026-11-20", EUR: 60}}}
	fx := setup(t, split)
	parts, err := fx.c.Advances(context.Background(), "p1")
	if err != nil || len(parts) != 2 {
		t.Fatalf("split: %v %+v", err, parts)
	}
	el, gas := parts[0], parts[1]
	if el.Part != PartElectricity || gas.Part != PartGas {
		t.Fatalf("parts %q %q", el.Part, gas.Part)
	}
	if el.MonthlyEUR != 90 || gas.MonthlyEUR != 60 || el.PaidEUR != 90 || gas.PaidEUR != 60 {
		t.Fatalf("monthly %v/%v paid %v/%v", el.MonthlyEUR, gas.MonthlyEUR, el.PaidEUR, gas.PaidEUR)
	}
	if !gas.NextPayment.Equal(date(2026, 12, 20)) || !el.NextPayment.Equal(date(2026, 12, 15)) {
		t.Fatalf("next %v %v", el.NextPayment, gas.NextPayment)
	}
	// gas in November: 30 days × 96 × 0.02 m³ × 1.50 plus 30 days of 18.25 × 12 / 365
	wantGas := 30*96*0.02*1.50 + 30*18.25*12/365
	if math.Abs(gas.CostEUR-wantGas) > 0.01 {
		t.Fatalf("gas cost %v, want %v", gas.CostEUR, wantGas)
	}
	if math.Abs(el.CostEUR+gas.CostEUR-total.CostEUR) > 0.02 {
		t.Fatalf("parts %v + %v, whole %v", el.CostEUR, gas.CostEUR, total.CostEUR)
	}
	fe, fg, ft := el.Forecast, gas.Forecast, total.Forecast
	if fe == nil || fg == nil || ft == nil {
		t.Fatal("no forecast")
	}
	if math.Abs(fe.RemainingCostEUR+fg.RemainingCostEUR-ft.RemainingCostEUR) > 0.02 {
		t.Fatalf("remaining %v + %v, whole %v", fe.RemainingCostEUR, fg.RemainingCostEUR, ft.RemainingCostEUR)
	}
	// fixed and usage costs per part add up to the whole; gas fixed is 61 days of 18.25 × 12 / 365
	if math.Abs(fe.FixedCostEUR+fg.FixedCostEUR-ft.FixedCostEUR) > 0.02 || math.Abs(fe.UsageCostEUR+fg.UsageCostEUR-ft.UsageCostEUR) > 0.02 ||
		math.Abs(fg.FixedCostEUR-61*18.25*12/365) > 0.01 || math.Abs(fe.FixedCostEUR+fe.UsageCostEUR-fe.TotalCostEUR) > 0.01 {
		t.Fatalf("fixed %v/%v of %v, usage %v/%v of %v", fe.FixedCostEUR, fg.FixedCostEUR, ft.FixedCostEUR, fe.UsageCostEUR, fg.UsageCostEUR, ft.UsageCostEUR)
	}
	if fg.ImportKWh != 0 || fe.GasM3 != 0 || fg.GasM3 == 0 {
		t.Fatalf("usage per part: gas %v kWh, electricity %v m³, gas %v m³", fg.ImportKWh, fe.GasM3, fg.GasM3)
	}
	if fg.AdviceMonthlyEUR == nil || *fg.AdviceMonthlyEUR != math.Round(fg.TotalCostEUR-gas.PaidEUR) {
		t.Fatalf("gas advice %v", fg.AdviceMonthlyEUR)
	}
	// Advance (the old single status) is the first part
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st.Part != PartElectricity {
		t.Fatalf("advance %v %+v", err, st)
	}

	// only gas has an advance (electricity billed afterwards)
	split.Advance = nil
	parts, err = setup(t, split).c.Advances(context.Background(), "p1")
	if err != nil || len(parts) != 1 || parts[0].Part != PartGas {
		t.Fatalf("gas only: %v %+v", err, parts)
	}
}

// Dynamic gas with fixed electricity forecasts without any power prices,
// and only the dynamic part gets the price margin.
func TestSplitDynamicGasForecast(t *testing.T) {
	now := date(2026, 12, 1)
	fx := newFixture(t, now)
	// December only, from last year's measurements
	fx.contract(t, Contract{Name: "Vast / Dynamisch", Start: "2026-12-01", End: "2026-12-31", Type: TypeFixed, Split: true, GasType: TypeDynamic,
		Electricity: Electricity{RateCt: 30, TaxCt: 10}, Gas: Gas{MarkupCt: 5, TaxCt: 50},
		Advance:    &Advance{Amounts: []AdvanceAmount{{From: "2026-12-01", EUR: 50}}},
		GasAdvance: &Advance{Amounts: []AdvanceAmount{{From: "2026-12-01", EUR: 50}}}})
	measuredDays(fx, t, date(2025, 12, 1), date(2026, 12, 1), 0.1, 0.02) // the previous period
	gasFrom, _ := prices.GasDayBounds(now.AddDate(0, 0, -40))
	var gp []float64
	for range 41 {
		gp = append(gp, 0.5)
	}
	fx.prices(t, prices.GasMarket, gasFrom, 24*time.Hour, gp...)
	parts, err := fx.c.Advances(context.Background(), "p1")
	if err != nil || len(parts) != 2 {
		t.Fatalf("%v %+v", err, parts)
	}
	el, gas := parts[0].Forecast, parts[1].Forecast
	if el == nil || gas == nil {
		t.Fatalf("no forecast: %q %q", parts[0].ForecastError, parts[1].ForecastError)
	}
	// December gas: 31 × 96 × 0.02 m³ × (0.5 × 1.21 + 0.05 + 0.50)
	if want := 31 * 96 * 0.02 * (0.5*1.21 + 0.55); math.Abs(gas.RemainingCostEUR-want) > 0.05 {
		t.Fatalf("gas remaining %v, want %v", gas.RemainingCostEUR, want)
	}
	// fixed electricity from last year: only the usage margin of 8%
	if spread := el.HighEUR - el.ResultEUR; math.Abs(spread-0.08*el.RemainingCostEUR) > 0.02 {
		t.Fatalf("electricity spread %v, remaining %v", spread, el.RemainingCostEUR)
	}
	// only the market price of the quarter hours without a known gas price
	// (after the last gas day stored) is uncertain
	unknown := date(2027, 1, 1).Sub(gasFrom.AddDate(0, 0, 41)).Hours() * 4
	market := unknown * 0.02 * 0.5 * 1.21
	if spread := gas.HighEUR - gas.ResultEUR; math.Abs(spread-math.Hypot(0.08*gas.RemainingCostEUR, 0.10*market)) > 0.02 {
		t.Fatalf("gas spread %v, remaining %v", spread, gas.RemainingCostEUR)
	}
}

// The price card shows a market price chart only for the dynamic part of
// the contract of that day.
func TestTimelineTypes(t *testing.T) {
	tl := Timeline{
		{Name: "Dynamisch", Start: "2026-01-01", End: "2026-08-31", Type: TypeDynamic},
		{Name: "Gesplitst", Start: "2026-09-01", Type: TypeDynamic, Split: true, GasType: TypeFixed},
	}
	loc := prices.Amsterdam
	cases := []struct {
		at         time.Time
		power, gas string
	}{
		{time.Date(2025, 12, 31, 12, 0, 0, 0, loc), "", ""},
		{time.Date(2026, 6, 1, 12, 0, 0, 0, loc), TypeDynamic, TypeDynamic},
		{time.Date(2026, 9, 1, 0, 30, 0, 0, loc), TypeDynamic, TypeFixed},
	}
	for _, c := range cases {
		if got := tl.ElectricityType(c.at, loc); got != c.power {
			t.Errorf("%v electricity %q, want %q", c.at, got, c.power)
		}
		if got := tl.GasType(c.at, loc); got != c.gas {
			t.Errorf("%v gas %q, want %q", c.at, got, c.gas)
		}
	}
}
