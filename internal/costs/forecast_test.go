package costs

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// When this period so far uses less than the same days of the previous
// period, the rest of the period follows that trend; the spread of the
// months around it is the usage margin (at least 3%). Both can be switched
// off or overridden in the settings, and the forecast still shows what
// was computed.
func TestForecastTrendAndComputedSpread(t *testing.T) {
	now := date(2026, 5, 1)
	setup := func(t *testing.T) *fixture {
		fx := newFixture(t, now)
		fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed,
			Electricity: Electricity{RateCt: 30, TaxCt: 10},
			Advance:     &Advance{Amounts: []AdvanceAmount{{From: "2026-01-01", EUR: 100}}}})
		measuredDays(fx, t, date(2025, 1, 1), date(2026, 1, 1), 0.1, 0) // previous period
		measuredDays(fx, t, date(2026, 1, 1), now, 0.09, 0)             // 10% less so far
		fx.c.Forecast = &forecast.Service{Store: fx.st}
		return fx
	}
	rest := 9.6 * 245 // 1 May to 31 December at last year's usage
	t.Run("automatic", func(t *testing.T) {
		fx := setup(t)
		st, err := fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast == nil {
			t.Fatalf("%v %+v", err, st)
		}
		f := st.Forecast
		if math.Abs(f.ImportKWh-0.9*rest) > 0.5 {
			t.Fatalf("import %v, want %v", f.ImportKWh, 0.9*rest)
		}
		if u := used(f.Method.Percentages, "trend_import"); u == nil || u.Pct != -10 || u.Source != forecast.SourceComputed || u.Months != 4 {
			t.Fatalf("trend %+v", u)
		}
		// every month is exactly 10% lower: no spread around the trend, so the minimum
		u := used(f.Method.Percentages, "usage_electricity")
		if u == nil || u.Pct != forecast.MinComputedPct || u.Source != forecast.SourceComputed || u.Months != 4 {
			t.Fatalf("usage margin %+v", u)
		}
		if half := (f.HighEUR - f.LowEUR) / 2; math.Abs(half-0.03*f.RemainingCostEUR) > 0.02 {
			t.Fatalf("half band %v of %v", half, f.RemainingCostEUR)
		}
	})
	t.Run("trend off", func(t *testing.T) {
		fx := setup(t)
		off := false
		if err := fx.c.Forecast.Save(context.Background(), forecast.Settings{Trend: &off}); err != nil {
			t.Fatal(err)
		}
		st, _ := fx.c.Advance(context.Background(), "p1")
		f := st.Forecast
		if math.Abs(f.ImportKWh-rest) > 0.5 {
			t.Fatalf("import %v, want %v", f.ImportKWh, rest)
		}
		if u := used(f.Method.Percentages, "trend_import"); u == nil || u.Pct != 0 || u.Source != forecast.SourceSetting || u.Auto == nil || *u.Auto != -10 {
			t.Fatalf("trend %+v", u)
		}
		// without the trend the months are 10% off what is expected
		if u := used(f.Method.Percentages, "usage_electricity"); u == nil || math.Abs(u.Pct-10) > 0.05 {
			t.Fatalf("usage margin %+v", u)
		}
	})
	t.Run("own margin", func(t *testing.T) {
		fx := setup(t)
		five := 5.0
		if err := fx.c.Forecast.Save(context.Background(), forecast.Settings{ElectricityPct: &five}); err != nil {
			t.Fatal(err)
		}
		st, _ := fx.c.Advance(context.Background(), "p1")
		f := st.Forecast
		u := used(f.Method.Percentages, "usage_electricity")
		if u == nil || u.Pct != 5 || u.Source != forecast.SourceSetting || *u.Auto != forecast.MinComputedPct || u.AutoSource != forecast.SourceComputed {
			t.Fatalf("usage margin %+v", u)
		}
		if half := (f.HighEUR - f.LowEUR) / 2; math.Abs(half-0.05*f.RemainingCostEUR) > 0.02 {
			t.Fatalf("half band %v of %v", half, f.RemainingCostEUR)
		}
	})
	t.Run("too short for a trend", func(t *testing.T) {
		fx := setup(t)
		fx.c.Now = func() time.Time { return date(2026, 2, 1) } // one month compared
		st, _ := fx.c.Advance(context.Background(), "p1")
		f := st.Forecast
		if used(f.Method.Percentages, "trend_import") != nil {
			t.Fatalf("trend after a month: %+v", f.Method.Percentages)
		}
		if u := used(f.Method.Percentages, "usage_electricity"); u == nil || u.Pct != forecast.Defaults.ElectricityPct || u.Source != forecast.SourceDefault {
			t.Fatalf("usage margin %+v", u)
		}
	})
}

// A measured day with a few quarter hours missing counts as measured, and
// the missing ones are filled in with the day's average.
func TestFillDay(t *testing.T) {
	fx := newFixture(t, date(2026, 6, 1))
	d := date(2026, 3, 3)
	var qs []store.Quarter
	for i, q := 0, d; q.Before(d.AddDate(0, 0, 1)); i, q = i+1, q.Add(15*time.Minute) {
		if i%16 == 0 { // 6 of 96 missing
			continue
		}
		qs = append(qs, store.Quarter{Start: q, ImportKWh: f(0.1), ExportKWh: f(0), GasM3: f(0.01), Quality: "complete"})
	}
	if !fx.c.usableDay(qs, d, 92) || fx.c.usableDay(qs[:80], d, 92) {
		t.Fatal("usable")
	}
	filled := fx.c.fillDay(qs, d)
	sum, gas := 0.0, 0.0
	for _, q := range filled {
		sum += val(q.ImportKWh)
		gas += val(q.GasM3)
	}
	if len(filled) != 96 || math.Abs(sum-9.6) > 1e-9 || math.Abs(gas-0.96) > 1e-9 {
		t.Fatalf("%d quarters, %v kWh, %v m³", len(filled), sum, gas)
	}
}

// Without a complete previous period a split contract with only the
// electricity usage entered forecasts electricity, but not gas; export,
// never measured, is not needed.
func TestForecastMissingGasOnly(t *testing.T) {
	now := date(2026, 6, 1)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Stroom / Gas", Start: "2026-05-01", Type: TypeFixed, Split: true, GasType: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10}, Gas: Gas{RateCt: 100, TaxCt: 50},
		Advance:    &Advance{Amounts: []AdvanceAmount{{From: "2026-05-01", EUR: 80}}},
		GasAdvance: &Advance{Amounts: []AdvanceAmount{{From: "2026-05-01", EUR: 40}}},
		Estimate:   &Estimate{ImportKWh: 3000}})
	measuredDays(fx, t, date(2026, 5, 1), now, 0.1, 0.01)
	parts, err := fx.c.Advances(context.Background(), "p1")
	if err != nil || len(parts) != 2 {
		t.Fatalf("%v %+v", err, parts)
	}
	el, gas := parts[0], parts[1]
	if el.Forecast == nil || el.ForecastError != "" {
		t.Fatalf("electricity: %+v", el)
	}
	if src := el.Forecast.Method.Sources; src["import_kwh"] != SourceContract || src["export_kwh"] != SourceNone || src["gas_m3"] != SourceMissing {
		t.Fatalf("sources %v", src)
	}
	if gas.Forecast != nil || !strings.HasPrefix(gas.ForecastError, "Geen prognose voor gas:") {
		t.Fatalf("gas: %+v", gas)
	}
	if want := 31 * 96 * 0.01 * 1.50; math.Abs(gas.CostEUR-want) > 0.01 {
		t.Fatalf("gas measured %v, want %v", gas.CostEUR, want)
	}
}
