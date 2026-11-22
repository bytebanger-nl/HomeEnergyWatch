package costs

import (
	"context"
	"math"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func used(list []forecast.Used, key string) *forecast.Used {
	for i := range list {
		if list[i].Key == key {
			return &list[i]
		}
	}
	return nil
}

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, ams) }

func TestSettlementPeriod(t *testing.T) {
	c := Contract{Start: "2026-03-16"}
	for _, tc := range []struct {
		name     string
		tl       Timeline
		now      time.Time
		from, to time.Time
	}{
		{"first contract year", Timeline{c}, date(2026, 11, 6), date(2026, 3, 16), date(2027, 3, 16)},
		{"second contract year", Timeline{c}, date(2027, 4, 1), date(2027, 3, 16), date(2028, 3, 16)},
		{"ends at the next contract", Timeline{c, {Start: "2027-01-01"}}, date(2026, 11, 6), date(2026, 3, 16), date(2027, 1, 1)},
		{"ends at its end date", Timeline{{Start: "2026-03-16", End: "2026-12-31"}}, date(2026, 11, 6), date(2026, 3, 16), date(2027, 1, 1)},
	} {
		from, to := settlementPeriod(tc.tl, &tc.tl[0], tc.now, ams)
		if !from.Equal(tc.from) || !to.Equal(tc.to) {
			t.Errorf("%s: [%s, %s), want [%s, %s)", tc.name, from.Format(dateLayout), to.Format(dateLayout), tc.from.Format(dateLayout), tc.to.Format(dateLayout))
		}
	}
}

// Advances count per day as the monthly amount divided by the days of that
// month, so half a month is half the amount; a new amount applies from its date.
// An advance is paid in full once a month on the day of the first payment,
// not pro rata per day; a new amount or a tariff change keeps that day.
func TestPaymentsOnAFixedDay(t *testing.T) {
	dates := func(ps []payment) (out []string) {
		for _, p := range ps {
			out = append(out, p.Date.Format("01-02")+"="+strconv.FormatFloat(p.EUR, 'f', -1, 64))
		}
		return out
	}
	// contract from 16 April, first payment on 22 June
	water := Timeline{{ID: 1, Start: "2026-04-16", Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-06-22", EUR: 25}}}}}
	ps := payments(water, water, date(2026, 4, 16), date(2026, 11, 7), ams, "")
	if got := dates(ps); strings.Join(got, " ") != "06-22=25 07-22=25 08-22=25 09-22=25 10-22=25" || sumPayments(ps) != 125 {
		t.Errorf("payments up to 6 November: %v", got)
	}
	// every amount is a payment on its own date, repeated monthly until the
	// next one: 150.57 once on 14 April, then 126 on every 22nd, also under
	// the continuing contract (whose amount starts on 22 September)
	chain := Timeline{
		{ID: 1, Start: "2026-04-14", End: "2026-08-31", Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-04-14", EUR: 150.57}, {From: "2026-04-22", EUR: 126}}}},
		{ID: 2, Start: "2026-09-01", Continues: true, Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-09-22", EUR: 126}}}},
	}
	ps = payments(chain, chain, date(2026, 4, 14), date(2026, 11, 7), ams, "")
	got := dates(ps)
	if strings.Join(got, " ") != "04-14=150.57 04-22=126 05-22=126 06-22=126 07-22=126 08-22=126 09-22=126 10-22=126" || math.Abs(sumPayments(ps)-1032.57) > 1e-9 {
		t.Errorf("chain: %v = %v", got, sumPayments(ps))
	}
	// a contract without advance payments in the chain pays nothing
	chain[1].Advance = nil
	if got := dates(payments(chain, chain, date(2026, 9, 1), date(2026, 11, 7), ams, "")); len(got) != 0 {
		t.Errorf("without advance: %v", got)
	}
	// the 31st becomes the last day of a shorter month
	end := Timeline{{ID: 1, Start: "2026-01-31", Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-01-31", EUR: 10}}}}}
	got = dates(payments(end, end, date(2026, 1, 1), date(2026, 5, 1), ams, ""))
	if strings.Join(got, " ") != "01-31=10 02-28=10 03-31=10 04-30=10" {
		t.Errorf("end of month: %v", got)
	}
}

// The advance card counts the payments made so far in full, including
// today's, and spreads the advice over the payments still to come.
func TestAdvancePaidPerPayment(t *testing.T) {
	now := date(2026, 11, 6).Add(18 * time.Hour)
	fx := newWaterFixture(t, now)
	fx.contract(t, Contract{Name: "Evides", Start: "2026-04-16", Water: Water{RateCt: 132},
		Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-06-22", EUR: 25}}}, Estimate: &Estimate{WaterM3: 120}})
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st == nil || st.Forecast == nil {
		t.Fatalf("%v %+v", err, st)
	}
	if st.PaidEUR != 125 || st.Payments != 5 || st.NextPayment == nil || !st.NextPayment.Equal(date(2026, 11, 22)) {
		t.Errorf("paid %v in %d payments, next %v", st.PaidEUR, st.Payments, st.NextPayment)
	}
	f := st.Forecast
	// 22 November up to 22 March; 16 April 2027 starts the next period
	if f.RemainingPayments != 5 || f.RemainingPaidEUR != 125 || f.TotalPaidEUR != 250 {
		t.Errorf("remaining %d payments, %v; total %v", f.RemainingPayments, f.RemainingPaidEUR, f.TotalPaidEUR)
	}
	if f.AdviceMonthlyEUR == nil || math.Abs(*f.AdviceMonthlyEUR-math.Round((f.TotalCostEUR-st.PaidEUR)/5)) > 0.5 {
		t.Errorf("advice %v", f.AdviceMonthlyEUR)
	}
	// on the payment day itself that payment counts as made
	fx.c.Now = func() time.Time { return date(2026, 11, 22).Add(time.Hour) }
	if st, _ := fx.c.Advance(context.Background(), "p1"); st.PaidEUR != 150 || st.Forecast.RemainingPayments != 4 {
		t.Errorf("on 22 November: paid %v, %d to come", st.PaidEUR, st.Forecast.RemainingPayments)
	}
}

func TestAdvanceValidation(t *testing.T) {
	base := Contract{Name: "x", Start: "2026-01-01", End: "2026-12-31", Type: TypeFixed}
	for _, a := range []Advance{
		{Amounts: []AdvanceAmount{{From: "2025-12-01", EUR: 10}}}, // before the contract
		{Amounts: []AdvanceAmount{{From: "2027-01-01", EUR: 10}}}, // after its end
		{Amounts: []AdvanceAmount{{From: "2026-01-01", EUR: -1}}},
		{Amounts: []AdvanceAmount{{From: "2026-01-01", EUR: 1}, {From: "2026-01-01", EUR: 2}}},
	} {
		c := base
		c.Advance = &a
		c.Normalize()
		if c.Validate() == nil {
			t.Errorf("accepted %+v", a)
		}
	}
	c := base
	c.Advance = &Advance{Amounts: []AdvanceAmount{{}}}
	c.Normalize()
	if c.Advance != nil {
		t.Fatal("empty advance not dropped")
	}
}

// measuredDay stores a day with the same import every quarter hour.
func measuredDay(fx *fixture, t *testing.T, d time.Time, kwhPerQuarter, gasPerQuarter float64) {
	var qs []store.Quarter
	for q := d; q.Before(d.AddDate(0, 0, 1)); q = q.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: q, ImportKWh: f(kwhPerQuarter), ExportKWh: f(0), GasM3: f(gasPerQuarter)})
	}
	fx.quarters(t, qs...)
}

// measuredDays stores [from, to) like measuredDay, in one write.
func measuredDays(fx *fixture, t *testing.T, from, to time.Time, kwhPerQuarter, gasPerQuarter float64) {
	var qs []store.Quarter
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		for q := d; q.Before(d.AddDate(0, 0, 1)); q = q.Add(15 * time.Minute) {
			qs = append(qs, store.Quarter{Start: q, ImportKWh: f(kwhPerQuarter), ExportKWh: f(0), GasM3: f(gasPerQuarter)})
		}
	}
	fx.quarters(t, qs...)
}

// A fixed contract with an advance: last year's measurements stand in for
// the rest of the period, the costs follow the contract tariffs, and the
// advice makes the period end at zero.
func TestAdvanceForecastFromLastYear(t *testing.T) {
	now := date(2026, 12, 1) // contract year ends 2027-01-01: 31 days left
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", End: "2026-12-31", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10},
		Monthly:     []MonthlyCost{{Name: "Vast", EUR: 36.5}},
		Advance:     &Advance{Amounts: []AdvanceAmount{{From: "2026-01-15", EUR: 50}}}}) // paid on the 15th
	// the previous period (2025) fully measured: 0.1 kWh per quarter = 9.6
	// kWh a day; this period is not measured, so it follows 2025 too
	measuredDays(fx, t, date(2025, 1, 1), date(2026, 1, 1), 0.1, 0)
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st == nil {
		t.Fatalf("status %v, err %v", st, err)
	}
	if !st.From.Equal(date(2026, 1, 1)) || !st.To.Equal(date(2027, 1, 1)) {
		t.Fatalf("period [%s, %s)", st.From, st.To)
	}
	if math.Abs(st.PaidEUR-550) > 0.01 { // 15 January to 15 November
		t.Fatalf("paid %v, want 550", st.PaidEUR)
	}
	f := st.Forecast
	if f == nil {
		t.Fatalf("no forecast: %s", st.ForecastError)
	}
	if f.Method.LastYearDays != 31 || f.Method.ProfileDays != 0 || f.Method.Sources["import_kwh"] != SourcePrevious ||
		f.Method.PreviousCoverage != 1 {
		t.Fatalf("method %+v", f.Method)
	}
	if st.EstimatedDays != 334 {
		t.Fatalf("estimated days %d, want 334 (January to November)", st.EstimatedDays)
	}
	// December: 31 × 9.6 kWh × 40 ct + 31 days of fixed costs (36.5 × 12 / 365 a day)
	wantCost := 31*9.6*0.40 + 31*36.5*12/365
	if math.Abs(f.RemainingCostEUR-wantCost) > 0.01 || math.Abs(f.ImportKWh-297.6) > 0.5 {
		t.Fatalf("remaining cost %v (want %v), import %v", f.RemainingCostEUR, wantCost, f.ImportKWh)
	}
	if math.Abs(f.RemainingPaidEUR-50) > 0.01 || math.Abs(f.ResultEUR-(f.TotalPaidEUR-f.TotalCostEUR)) > 0.01 {
		t.Fatalf("paid %v, result %v", f.RemainingPaidEUR, f.ResultEUR)
	}
	if !(f.LowEUR < f.ResultEUR && f.ResultEUR < f.HighEUR) {
		t.Fatalf("band %v < %v < %v", f.LowEUR, f.ResultEUR, f.HighEUR)
	}
	// the whole year: 365 days of fixed costs; the rest is usage
	if math.Abs(f.FixedCostEUR-36.5*12) > 0.01 || math.Abs(f.FixedCostEUR+f.UsageCostEUR-f.TotalCostEUR) > 0.01 {
		t.Fatalf("fixed %v + usage %v, total %v", f.FixedCostEUR, f.UsageCostEUR, f.TotalCostEUR)
	}
	// one payment left (15 December)
	if f.RemainingPayments != 1 || f.AdviceMonthlyEUR == nil || *f.AdviceMonthlyEUR != math.Round(f.TotalCostEUR-st.PaidEUR) {
		t.Fatalf("advice %v", f.AdviceMonthlyEUR)
	}
}

// The entered yearly usage goes first, spread with the profile, even when
// the previous period is measured; without it the previous period counts,
// but only when at least 90% of its days is measured. Without either there
// is no forecast, with an explanation.
func TestAdvanceForecastSources(t *testing.T) {
	now := date(2026, 6, 1)
	setup := func(t *testing.T, est *Estimate) *fixture {
		fx := newFixture(t, now)
		fx.contract(t, Contract{Name: "Vast", Start: "2026-05-01", Type: TypeFixed,
			Electricity: Electricity{RateCt: 30, TaxCt: 10}, Gas: Gas{RateCt: 100, TaxCt: 50},
			Advance:  &Advance{Amounts: []AdvanceAmount{{From: "2026-05-01", EUR: 100}}},
			Estimate: est})
		return fx
	}
	// the previous period, 1 May 2025 to 1 May 2026, without its first skip days
	previous := func(fx *fixture, t *testing.T, skip int) {
		measuredDays(fx, t, date(2025, 5, 1).AddDate(0, 0, skip), date(2026, 5, 1), 0.1, 0.01)
	}
	t.Run("entered yearly usage", func(t *testing.T) {
		fx := setup(t, &Estimate{ImportKWh: 3000, GasM3: 1000})
		previous(fx, t, 0)
		st, err := fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast == nil {
			t.Fatalf("%v %v", err, st)
		}
		f := st.Forecast
		if f.Method.ProfileDays != 334 || f.Method.Sources["import_kwh"] != SourceContract || f.Method.Sources["gas_m3"] != SourceContract {
			t.Fatalf("method %+v", f.Method)
		}
		// June to April: the year minus May
		wantImp := 3000 * (1 - electricityProfile[4])
		wantGas := 1000 * (1 - gasProfile[4])
		if math.Abs(f.ImportKWh-wantImp) > 0.5 || math.Abs(f.GasM3-wantGas) > 0.5 {
			t.Fatalf("import %v (want %v), gas %v (want %v)", f.ImportKWh, wantImp, f.GasM3, wantGas)
		}
		if u := used(f.Method.Percentages, "entered"); u == nil || u.Pct != 10 || u.Source != forecast.SourceDefault {
			t.Fatalf("entered margin %+v", u)
		}
	})
	t.Run("previous period", func(t *testing.T) {
		fx := setup(t, nil)
		previous(fx, t, 30) // 335 of 365 days: 92%
		st, err := fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast == nil {
			t.Fatalf("%v %+v", err, st)
		}
		f := st.Forecast
		if f.Method.Sources["import_kwh"] != SourcePrevious || math.Abs(f.Method.PreviousCoverage-0.918) > 0.001 {
			t.Fatalf("method %+v", f.Method)
		}
		// June to April copy the same days of the previous period: 9.6 kWh a day
		if f.Method.LastYearDays != 334 || f.Method.ProfileDays != 0 {
			t.Fatalf("days %d copied, %d profile", f.Method.LastYearDays, f.Method.ProfileDays)
		}
		if want := 9.6 * 334; math.Abs(f.ImportKWh-want) > 0.5 {
			t.Fatalf("import %v, want %v", f.ImportKWh, want)
		}
		// the yearly usage of the previous period: its measured days scaled by the profile
		share := 0.0
		for d := date(2025, 5, 31); d.Before(date(2026, 5, 1)); d = d.AddDate(0, 0, 1) {
			share += dayShare(electricityProfile, d)
		}
		if want := 335 * 9.6 / share; math.Abs(f.Method.Yearly.ImportKWh-want) > 0.5 {
			t.Fatalf("yearly %v, want %v", f.Method.Yearly.ImportKWh, want)
		}
		// May 2026 is not measured: estimated from the same days of May 2025,
		// of which the 1st to the 30th are missing and come from that yearly usage
		if st.EstimatedDays != 31 {
			t.Fatalf("estimated days %d", st.EstimatedDays)
		}
		kwh := f.Method.Yearly.ImportKWh * electricityProfile[4] / 31
		m3 := f.Method.Yearly.GasM3 * gasProfile[4] / 31
		if want := (30*kwh+9.6)*0.40 + (30*m3+0.96)*1.50; math.Abs(st.CostEUR-want) > 0.02 {
			t.Fatalf("cost so far %v, want %v", st.CostEUR, want)
		}
	})
	t.Run("previous period not complete", func(t *testing.T) {
		fx := setup(t, nil)
		previous(fx, t, 40) // 325 of 365 days: 89%
		st, err := fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast != nil || !strings.Contains(st.ForecastError, "89%") || !strings.Contains(st.ForecastError, "stroomafname en gas") {
			t.Fatalf("%v %+v", err, st)
		}
		// with a lower minimum it does count
		fx.c.Forecast = &forecast.Service{Store: fx.st}
		low := 85.0
		if err := fx.c.Forecast.Save(context.Background(), forecast.Settings{PeriodMinPct: &low}); err != nil {
			t.Fatal(err)
		}
		st, err = fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast == nil {
			t.Fatalf("%v %+v", err, st)
		}
		if u := used(st.Forecast.Method.Percentages, "period_min"); u == nil || u.Pct != 85 || u.Source != forecast.SourceSetting || *u.Auto != 90 {
			t.Fatalf("period_min %+v", u)
		}
	})
	t.Run("this period does not count", func(t *testing.T) {
		// a month of this period is no longer enough for a forecast; the
		// measured costs are still shown
		fx := setup(t, nil)
		measuredDays(fx, t, date(2026, 5, 1), now, 0.1, 0.01)
		st, err := fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast != nil || st.ForecastError == "" {
			t.Fatalf("%v %+v", err, st)
		}
		if want := 31*96*0.1*0.40 + 31*96*0.01*1.50; math.Abs(st.CostEUR-want) > 0.01 {
			t.Fatalf("measured costs %v, want %v", st.CostEUR, want)
		}
	})
	t.Run("no data", func(t *testing.T) {
		fx := setup(t, nil)
		st, err := fx.c.Advance(context.Background(), "p1")
		if err != nil || st.Forecast != nil || st.ForecastError == "" {
			t.Fatalf("%v %+v", err, st)
		}
	})
}

// A contract without advance payments (billed afterwards) has no status.
func TestNoAdvanceNoStatus(t *testing.T) {
	fx := newFixture(t, date(2026, 6, 1))
	fx.contract(t, Contract{Name: "Achteraf", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30}})
	if st, err := fx.c.Advance(context.Background(), "p1"); st != nil || err != nil {
		t.Fatalf("%+v %v", st, err)
	}
}

// A dynamic contract prices the estimated quarters with the average per hour
// of the day of the last 30 days when last year's month is unknown.
func TestAdvanceForecastDynamicPrices(t *testing.T) {
	now := date(2026, 12, 1)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Dyn", Start: "2026-11-01", End: "2026-12-31", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 10},
		Advance:     &Advance{Amounts: []AdvanceAmount{{From: "2026-11-01", EUR: 40}}},
		Estimate:    &Estimate{ImportKWh: 2400}})
	var vals []float64
	for i := 0; i < 30*96; i++ {
		vals = append(vals, 100) // 100 EUR/MWh = 10 ct/kWh excl. VAT
	}
	fx.prices(t, "NL-DA", now.AddDate(0, 0, -30), 15*time.Minute, vals...)
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st.Forecast == nil {
		t.Fatalf("%v %+v", err, st)
	}
	f := st.Forecast
	if f.Method.Prices != "last_30_days" {
		t.Fatalf("prices %q", f.Method.Prices)
	}
	// December: 2400 × share × (10 × 1.21 + 2 + 10) ct
	want := 2400 * electricityProfile[11] * (12.1 + 2 + 10) / 100
	if math.Abs(f.RemainingCostEUR-want) > 0.05 {
		t.Fatalf("remaining cost %v, want %v", f.RemainingCostEUR, want)
	}
}

// Days without measurements before now are estimated rather than counted
// as zero, and reported as estimated.
func TestAdvanceFillsGapsInThePast(t *testing.T) {
	now := date(2026, 3, 1)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2026-02-01", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 10},
		Advance:     &Advance{Amounts: []AdvanceAmount{{From: "2026-02-01", EUR: 100}}},
		Estimate:    &Estimate{ImportKWh: 3000}})
	// February: measured except the 10th to the 14th
	for d := date(2026, 2, 1); d.Before(now); d = d.AddDate(0, 0, 1) {
		if d.Day() >= 10 && d.Day() <= 14 {
			continue
		}
		measuredDay(fx, t, d, 0.1, 0)
	}
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st.Forecast == nil {
		t.Fatalf("%v %+v", err, st)
	}
	if st.EstimatedDays != 5 {
		t.Fatalf("estimated days %d, want 5", st.EstimatedDays)
	}
	perDay := 3000 * electricityProfile[1] / 28 // February share per day
	want := (23*9.6 + 5*perDay) * 0.40
	if math.Abs(st.CostEUR-want) > 0.01 {
		t.Fatalf("cost so far %v, want %v", st.CostEUR, want)
	}
}

// Today's quarter hours that the recorder cannot have stored yet (the
// running one, and those within Settle) are not a gap in the measurements;
// an older missing quarter hour of today is.
func TestAdvanceTodayNotEstimatedWhileSettling(t *testing.T) {
	now := date(2026, 3, 1).Add(10*time.Hour + 2*time.Minute)
	for _, tc := range []struct {
		name string
		skip time.Duration // offset from midnight of a missing quarter hour, or -1
		want int
	}{
		{"complete", -1, 0},
		{"gap", 3 * time.Hour, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, now)
			fx.c.Settle = 6 * time.Minute // the 09:45 quarter (ends 10:00) may still come
			fx.contract(t, Contract{Name: "Vast", Start: "2026-02-01", Type: TypeFixed,
				Electricity: Electricity{RateCt: 30, TaxCt: 10},
				Advance:     &Advance{Amounts: []AdvanceAmount{{From: "2026-02-01", EUR: 100}}},
				Estimate:    &Estimate{ImportKWh: 3000}})
			for d := date(2026, 1, 1); d.Before(date(2026, 3, 1)); d = d.AddDate(0, 0, 1) {
				measuredDay(fx, t, d, 0.1, 0)
			}
			var qs []store.Quarter
			today := date(2026, 3, 1)
			for o := time.Duration(0); o < 9*time.Hour+45*time.Minute; o += 15 * time.Minute {
				if o != tc.skip {
					qs = append(qs, store.Quarter{Start: today.Add(o), ImportKWh: f(0.1), ExportKWh: f(0), GasM3: f(0)})
				}
			}
			fx.quarters(t, qs...)
			st, err := fx.c.Advance(context.Background(), "p1")
			if err != nil {
				t.Fatal(err)
			}
			if st.EstimatedDays != tc.want {
				t.Fatalf("estimated days %d, want %d", st.EstimatedDays, tc.want)
			}
		})
	}
}

// Tariff changes as continuing contracts form one supplier contract: one
// settlement year from the first start, with each day's own advance.
func TestAdvanceOverContractChain(t *testing.T) {
	now := date(2027, 2, 1)
	fx := newFixture(t, now)
	dyn := func(c Contract) Contract {
		c.Type, c.Electricity = TypeDynamic, Electricity{MarkupCt: 1.8, TaxCt: 11.1}
		c.Estimate = &Estimate{ImportKWh: 2400}
		return c
	}
	fx.contract(t, dyn(Contract{Name: "ANWB", Start: "2026-04-14", End: "2026-08-31",
		Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-04-14", EUR: 150.57}, {From: "2026-05-14", EUR: 126}}}}))
	fx.contract(t, dyn(Contract{Name: "ANWB sep", Start: "2026-09-01", Continues: true,
		Advance: &Advance{Amounts: []AdvanceAmount{{From: "2026-09-01", EUR: 126}}}}))
	fx.contract(t, dyn(Contract{Name: "ANWB 2027", Start: "2027-01-01", Continues: true,
		Advance: &Advance{Amounts: []AdvanceAmount{{From: "2027-01-01", EUR: 130}}}}))
	var vals []float64
	for i := 0; i < 30*96; i++ {
		vals = append(vals, 100)
	}
	fx.prices(t, "NL-DA", now.AddDate(0, 0, -30), 15*time.Minute, vals...)

	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st == nil {
		t.Fatalf("%v %+v", err, st)
	}
	if !st.From.Equal(date(2026, 4, 14)) || !st.To.Equal(date(2027, 4, 14)) {
		t.Fatalf("period [%s, %s), want the ANWB year from 14 April", st.From.Format(dateLayout), st.To.Format(dateLayout))
	}
	if st.ContractName != "ANWB" || st.Contracts != 3 || st.MonthlyEUR != 130 {
		t.Fatalf("name %q, contracts %d, monthly %v", st.ContractName, st.Contracts, st.MonthlyEUR)
	}
	// each amount is paid on its own day: 14 April 150.57, 14 May to 14
	// August 126, then the continuing contracts on the 1st: September to
	// December 126, January and (today) February 130
	want := 150.57 + 8*126 + 2*130
	if math.Abs(st.PaidEUR-want) > 0.01 || st.Payments != 11 {
		t.Fatalf("paid %v in %d payments, want %v", st.PaidEUR, st.Payments, want)
	}
	if st.Forecast == nil || math.Abs(st.Forecast.RemainingPaidEUR-130*2) > 0.01 { // 1 March and 1 April
		t.Fatalf("forecast %+v", st.Forecast)
	}
}

func TestContinuingContractValidation(t *testing.T) {
	fx := newFixture(t, date(2026, 6, 1))
	if _, err := fx.cs.Save(context.Background(), Contract{Name: "Los", Start: "2026-01-01", Type: TypeFixed, Continues: true}); err == nil {
		t.Fatal("first contract accepted as a continuation")
	}
	fx.contract(t, Contract{Name: "A", Start: "2026-01-01", End: "2026-03-31", Type: TypeFixed})
	if _, err := fx.cs.Save(context.Background(), Contract{Name: "B", Start: "2026-04-10", Type: TypeFixed, Continues: true}); err == nil {
		t.Fatal("continuation after a gap accepted")
	}
	fx.contract(t, Contract{Name: "B", Start: "2026-04-01", Type: TypeFixed, Continues: true})
	// a contract that does not continue starts its own settlement year
	tl, _ := fx.cs.List(context.Background())
	tl = append(tl, Contract{Name: "C", Start: "2026-05-01", Type: TypeFixed})
	from, _ := settlementPeriod(tl, &tl[2], date(2026, 6, 1), ams)
	if !from.Equal(date(2026, 5, 1)) {
		t.Fatalf("new supplier starts %s", from.Format(dateLayout))
	}
	from, to := settlementPeriod(tl, &tl[1], date(2026, 4, 20), ams)
	if !from.Equal(date(2026, 1, 1)) || !to.Equal(date(2026, 5, 1)) {
		t.Fatalf("chain A+B: [%s, %s)", from.Format(dateLayout), to.Format(dateLayout))
	}
}

// Yearly usage entered on several contracts of a chain is not added up: per
// field the latest entered value counts for the whole period.
func TestYearlyUsageOverChain(t *testing.T) {
	chain := Timeline{
		{Start: "2026-04-14", Estimate: &Estimate{ImportKWh: 2000, GasM3: 600}},
		{Start: "2026-09-01", Continues: true, Estimate: &Estimate{ImportKWh: 2500}},
	}
	if y := entered(chain); y.ImportKWh != 2500 || y.GasM3 != 600 {
		t.Fatalf("yearly %+v", y)
	}
}

// The band combines usage and price uncertainty as independent errors, and
// the price uncertainty only applies to the market-price part (not to the
// markup or the energy tax).
func TestAdvanceBandWidth(t *testing.T) {
	now := date(2026, 11, 1)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Dyn", Start: "2026-04-14", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 1.8, TaxCt: 11.1},
		Advance:     &Advance{Amounts: []AdvanceAmount{{From: "2026-04-14", EUR: 126}}},
		Estimate:    &Estimate{ImportKWh: 3500}})
	for d := date(2026, 4, 14); d.Before(now); d = d.AddDate(0, 0, 1) {
		measuredDay(fx, t, d, 0.1, 0)
	}
	var vals []float64
	for i := 0; i < 30*96; i++ {
		vals = append(vals, 100)
	}
	fx.prices(t, "NL-DA", now.AddDate(0, 0, -30), 15*time.Minute, vals...)
	st, err := fx.c.Advance(context.Background(), "p1")
	if err != nil || st.Forecast == nil {
		t.Fatalf("%v %+v", err, st)
	}
	f := st.Forecast
	usage := f.RemainingCostEUR          // no fixed costs on this contract
	market := f.ImportKWh * 0.100 * 1.21 // 100 EUR/MWh with VAT
	if got, want := (f.HighEUR-f.LowEUR)/2, math.Hypot(0.10*usage, 0.10*market); math.Abs(got-want) > 0.02 {
		t.Fatalf("half band %v, want %v (of %v, market %v)", got, want, usage, market)
	}
	if u := used(f.Method.Percentages, "price"); u == nil || u.Pct != 10 {
		t.Fatalf("price margin %+v", u)
	}
}

// SettlementPeriodAt finds the contract year of a day over a chain of
// continuing contracts; a new contract (not continuing) starts its own year.
func TestSettlementPeriodAt(t *testing.T) {
	tl := Timeline{
		{ID: 1, Start: "2025-03-15", Type: TypeFixed},
		{ID: 2, Start: "2025-09-01", Type: TypeFixed, Continues: true},
		{ID: 3, Start: "2026-06-01", End: "2026-12-31", Type: TypeFixed},
	}
	d := func(s string) time.Time { t, _ := time.ParseInLocation(dateLayout, s, ams); return t }
	for _, c := range []struct{ day, from, to string }{
		{"2025-03-15", "2025-03-15", "2026-03-15"},
		{"2026-02-10", "2025-03-15", "2026-03-15"},
		{"2026-03-20", "2026-03-15", "2026-06-01"}, // the chain ends where the new contract starts
		{"2026-11-30", "2026-06-01", "2027-01-01"},
	} {
		from, to, ok := tl.SettlementPeriodAt(d(c.day).Add(15*time.Hour), ams)
		if !ok || !from.Equal(d(c.from)) || !to.Equal(d(c.to)) {
			t.Errorf("%s: %s – %s (%v)", c.day, from, to, ok)
		}
	}
	if _, _, ok := tl.SettlementPeriodAt(d("2025-01-01"), ams); ok {
		t.Error("a day without contract has a settlement period")
	}
}
