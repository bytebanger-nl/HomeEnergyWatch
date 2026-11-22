package costs

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

var ams = prices.Amsterdam

func f(v float64) *float64 { return &v }

type fixture struct {
	t  *testing.T
	st *store.Store
	cs *Contracts
	c  *Calculator
}

func newFixture(t testing.TB, now time.Time) *fixture {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	cs := &Contracts{Store: st}
	return &fixture{st: st, cs: cs, c: &Calculator{Store: st, Contracts: cs, Loc: ams, Now: func() time.Time { return now }}}
}

func (fx *fixture) contract(t testing.TB, c Contract) Contract {
	saved, err := fx.cs.Save(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func (fx *fixture) quarters(t testing.TB, qs ...store.Quarter) {
	for i := range qs {
		qs[i].DeviceID = "p1"
		if qs[i].Quality == "" {
			qs[i].Quality, qs[i].Coverage = "complete", 1
		}
	}
	if err := fx.st.SaveQuarters(context.Background(), qs); err != nil {
		t.Fatal(err)
	}
}

// historySince stores an empty quarter hour at at, so the history of the
// meter starts there (Store.MeasuredFrom): tests of missing quarter hours
// on a later day then see every unmeasured quarter as missing.
func (fx *fixture) historySince(t testing.TB, at time.Time) {
	fx.quarters(t, store.Quarter{Start: at})
}

func (fx *fixture) prices(t testing.TB, market string, from time.Time, step time.Duration, values ...float64) {
	var rows []store.MarketPrice
	for i, v := range values {
		rows = append(rows, store.MarketPrice{Start: from.Add(time.Duration(i) * step), Duration: step, Value: v, Source: "test"})
	}
	if err := fx.st.SavePrices(context.Background(), market, rows); err != nil {
		t.Fatal(err)
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestDynamicContractDayCosts(t *testing.T) {
	day := time.Date(2026, 9, 7, 0, 0, 0, 0, ams)
	fx := newFixture(t, day.AddDate(0, 0, 3))
	fx.contract(t, Contract{
		Name: "Dynamisch", Start: "2026-09-07", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 11, ExportCt: -1},
		Gas:         Gas{MarkupCt: 5, TaxCt: 70},
		Monthly:     []MonthlyCost{{Name: "Vaste leveringskosten", EUR: 6}, {Name: "Netbeheer", EUR: 36.5}, {Name: "Vermindering energiebelasting", EUR: -51.1}},
	})
	q := day.Add(10 * time.Hour)
	fx.prices(t, prices.Market, q, 15*time.Minute, 100, -50)
	gasFrom, _ := prices.GasDayBounds(day.AddDate(0, 0, -1))
	fx.prices(t, prices.GasMarket, gasFrom, 24*time.Hour, 0.5, 0.6) // gas days 6 and 7 September
	fx.historySince(t, day.AddDate(0, 0, -1))
	fx.quarters(t,
		store.Quarter{Start: q, ImportKWh: f(1), ExportKWh: f(0), GasM3: f(2)},
		store.Quarter{Start: q.Add(15 * time.Minute), ImportKWh: f(0), ExportKWh: f(2)},
	)
	res, err := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Buckets) != 1 {
		t.Fatalf("%d buckets", len(res.Buckets))
	}
	b := res.Buckets[0]
	// import: 1 kWh × (0.100 × 1.21 + 0.02); tax 1 × 0.11
	if !near(b.ElectricityEUR, 0.141) || !near(b.ElectricityTaxEUR, 0.11) {
		t.Fatalf("electricity %v tax %v", b.ElectricityEUR, b.ElectricityTaxEUR)
	}
	// export at a negative price: 2 × (-0.050 × 1.21 - 0.01) = -0.141 (you pay)
	if !near(b.ExportEUR, -0.141) {
		t.Fatalf("export %v", b.ExportEUR)
	}
	// net metering: 1 kWh nettable × energy tax
	if !near(b.NettingEUR, 0.11) {
		t.Fatalf("netting %v", b.NettingEUR)
	}
	// gas day 7 September: 2 m³ × (0.6 × 1.21 + 0.05) + 2 × 0.70
	if !near(b.GasEUR, 2*(0.6*1.21+0.05)) || !near(b.GasTaxEUR, 1.4) {
		t.Fatalf("gas %v tax %v", b.GasEUR, b.GasTaxEUR)
	}
	// (6 + 36.5 - 51.1) × 12 / 365 per day
	if !near(b.FixedEUR, -8.6*12/365) {
		t.Fatalf("fixed %v", b.FixedEUR)
	}
	want := b.ElectricityEUR + b.ElectricityTaxEUR + b.GasEUR + b.GasTaxEUR + b.FixedEUR - b.ExportEUR - b.NettingEUR
	if !near(b.TotalEUR, want) {
		t.Fatalf("total %v, want %v", b.TotalEUR, want)
	}
	// 94 of 96 quarter hours were not measured: reported, not zero
	if b.Complete || b.Missing.Quarters != 94 || b.Missing.Prices != 0 || b.Missing.ContractDays != 0 {
		t.Fatalf("missing %+v", b.Missing)
	}
}

func TestContractStartsMidMonth(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, ams)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2026-09-07", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30}, Monthly: []MonthlyCost{{Name: "Vast", EUR: 36.5}}})
	res, err := fx.c.Calculate(context.Background(), "p1", time.Date(2026, 9, 1, 0, 0, 0, 0, ams), now, BucketMonth)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Buckets[0]
	// 24 days (7-30 September) × 36.5 × 12 / 365
	if b.Missing.ContractDays != 6 || !near(b.FixedEUR, 24*1.2) {
		t.Fatalf("contract days missing %d, fixed %v", b.Missing.ContractDays, b.FixedEUR)
	}
}

func TestFixedDualRateAndNettingAcrossTheYear(t *testing.T) {
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, ams)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed,
		Electricity: Electricity{NormalCt: 30, LowCt: 20, TaxCt: 11, ExportCt: 5}})
	sat := time.Date(2026, 6, 6, 12, 0, 0, 0, ams)  // Saturday: low rate
	tue := time.Date(2026, 11, 3, 12, 0, 0, 0, ams) // Tuesday noon: normal rate
	fx.quarters(t,
		store.Quarter{Start: sat, ImportKWh: f(0), ExportKWh: f(10)},
		store.Quarter{Start: tue, ImportKWh: f(4), ExportKWh: f(0)},
	)
	// November only: the export of June is netted against November's import
	res, err := fx.c.Calculate(context.Background(), "p1", time.Date(2026, 11, 1, 0, 0, 0, 0, ams), now, BucketMonth)
	if err != nil {
		t.Fatal(err)
	}
	b := res.Buckets[0]
	if !near(b.ElectricityEUR, 4*0.30) || !near(b.ElectricityTaxEUR, 4*0.11) || b.ExportEUR != 0 {
		t.Fatalf("november %+v", b)
	}
	// 4 kWh netted, each worth 30 + 11 - 5 ct more than the feed-in compensation
	if !near(b.NettingEUR, 4*0.36) {
		t.Fatalf("netting %v", b.NettingEUR)
	}
	// the whole year: import fully netted, 6 kWh surplus at the feed-in compensation
	res, _ = fx.c.Calculate(context.Background(), "p1", time.Date(2026, 1, 1, 0, 0, 0, 0, ams), now, BucketYear)
	y := res.Total
	if !near(y.TotalEUR, 4*0.30+4*0.11-10*0.05-4*0.36) || !near(y.TotalEUR, -6*0.05) {
		t.Fatalf("year total %v", y.TotalEUR)
	}
	// low rate on Saturday
	c := Contract{Type: TypeFixed, Electricity: Electricity{NormalCt: 30, LowCt: 20}}
	if c.fixedRateCt(sat) != 20 || c.fixedRateCt(tue) != 30 || c.fixedRateCt(tue.Add(11*time.Hour)) != 20 {
		t.Fatal("low tariff window")
	}
}

func TestNoNettingFrom2027(t *testing.T) {
	now := time.Date(2027, 3, 1, 0, 0, 0, 0, ams)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2027-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 11, ExportCt: 5}})
	d := time.Date(2027, 2, 2, 12, 0, 0, 0, ams)
	fx.quarters(t, store.Quarter{Start: d, ImportKWh: f(1), ExportKWh: f(1)})
	res, _ := fx.c.Calculate(context.Background(), "p1", d, d.AddDate(0, 0, 1), BucketDay)
	if b := res.Buckets[0]; b.NettingEUR != 0 || !near(b.TotalEUR-b.FixedEUR, 0.41-0.05) {
		t.Fatalf("2027 %+v", b)
	}
}

func TestMissingPriceIsReported(t *testing.T) {
	day := time.Date(2026, 3, 29, 0, 0, 0, 0, ams) // 23 hours
	fx := newFixture(t, day.AddDate(0, 0, 1))
	fx.contract(t, Contract{Name: "Dyn", Start: "2026-01-01", Type: TypeDynamic, Electricity: Electricity{TaxCt: 10}})
	fx.historySince(t, day.AddDate(0, 0, -1))
	fx.quarters(t, store.Quarter{Start: day.Add(time.Hour), ImportKWh: f(1), ExportKWh: f(0)})
	res, _ := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	b := res.Buckets[0]
	if b.Missing.Prices != 1 || b.Missing.Quarters != 91 || b.Complete || b.ElectricityEUR != 0 || !near(b.ElectricityTaxEUR, 0.1) {
		t.Fatalf("%+v", b)
	}
}

func TestContractValidation(t *testing.T) {
	fx := newFixture(t, time.Now())
	ctx := context.Background()
	a := fx.contract(t, Contract{Name: "A", Start: "2026-01-01", End: "2026-09-06", Type: TypeDynamic})
	fx.contract(t, Contract{Name: "B", Start: "2026-09-07", Type: TypeFixed, Electricity: Electricity{RateCt: 25}})
	for name, c := range map[string]Contract{
		"overlap":    {Name: "C", Start: "2026-09-01", Type: TypeDynamic},
		"same start": {Name: "C", Start: "2026-09-07", Type: TypeDynamic},
		"bad date":   {Name: "C", Start: "7-9-2026", Type: TypeDynamic},
		"end first":  {Name: "C", Start: "2027-01-02", End: "2027-01-01", Type: TypeDynamic},
		"half dual":  {Name: "C", Start: "2027-01-01", Type: TypeFixed, Electricity: Electricity{NormalCt: 30}},
		"no name":    {Start: "2027-01-01", Type: TypeDynamic},
	} {
		if _, err := fx.cs.Save(ctx, c); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// extending A into B is an overlap; moving it is fine
	a.End = "2026-09-07"
	if _, err := fx.cs.Save(ctx, a); !errors.Is(err, ErrInvalid) {
		t.Fatalf("overlap on update: %v", err)
	}
	tl, _ := fx.cs.List(ctx)
	if c := tl.At("2026-09-06"); c == nil || c.Name != "A" {
		t.Fatalf("at 6 Sep: %+v", c)
	}
	if c := tl.At("2026-09-07"); c == nil || c.Name != "B" || c.VATPct != 21 {
		t.Fatalf("at 7 Sep: %+v", c)
	}
	if tl.At("2025-12-31") != nil {
		t.Fatal("contract before the first one")
	}
}

func TestCacheReusesDaysAndInvalidatesFromChange(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, ams)
	fx := newFixture(t, now)
	var changed []string
	fx.cs.OnChange = func(date string) { changed = append(changed, date); fx.c.InvalidateDate(date) }
	c := fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10, ExportCt: 5}})
	ctx := context.Background()
	jan5 := time.Date(2026, 1, 5, 12, 0, 0, 0, ams)
	mar1 := time.Date(2026, 3, 1, 12, 0, 0, 0, ams)
	fx.quarters(t, store.Quarter{Start: mar1, ImportKWh: f(2), ExportKWh: f(0)})
	march := func() Bucket {
		res, err := fx.c.Calculate(ctx, "p1", time.Date(2026, 3, 1, 0, 0, 0, 0, ams), now, BucketMonth)
		if err != nil {
			t.Fatal(err)
		}
		return res.Buckets[0]
	}
	if b := march(); !near(b.ElectricityEUR, 0.6) || b.NettingEUR != 0 {
		t.Fatalf("march %+v", b)
	}
	// 1 January - 9 March are finished days; 10 March (today) is never cached
	if n := fx.c.cached(); n != 68 {
		t.Fatalf("%d cached days", n)
	}
	// export in January is netted against March: only the January days and
	// later are recalculated
	fx.quarters(t, store.Quarter{Start: jan5, ImportKWh: f(0), ExportKWh: f(3)})
	fx.c.Invalidate("p1", jan5)
	if n := fx.c.cached(); n != 4 {
		t.Fatalf("%d cached days after invalidating 5 January", n)
	}
	if b := march(); !near(b.NettingEUR, 2*0.35) {
		t.Fatalf("netting after new export: %+v", b)
	}
	// a contract change drops the cache from its start date
	c.Electricity.RateCt = 40
	c.Start = "2026-02-01"
	fx.contract(t, c)
	if len(changed) != 2 || changed[1] != "2026-01-01" {
		t.Fatalf("changed %v", changed)
	}
	if b := march(); !near(b.ElectricityEUR, 0.8) {
		t.Fatalf("after contract change %+v", b)
	}
}

// Today (the day that is not over yet) counts up to now: the fixed costs
// pro rata, missing quarters only up to now, and it is never cached, so a
// new quarter shows up without an invalidation.
func TestTodaySoFar(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 7, 0, 0, ams)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30}, Monthly: []MonthlyCost{{Name: "Vast", EUR: 36.5}}})
	ctx := context.Background()
	q := time.Date(2026, 3, 10, 10, 0, 0, 0, ams)
	fx.historySince(t, q.AddDate(0, 0, -1))
	fx.quarters(t,
		store.Quarter{Start: q, ImportKWh: f(1), ExportKWh: f(0)},
		store.Quarter{Start: q.Add(15 * time.Minute), ImportKWh: f(0.5), ExportKWh: f(0), Quality: "partial", Coverage: 0.5},
	)
	today := func() Bucket {
		// as the PWA asks: from now (floored to the local day) to a day later
		res, err := fx.c.Calculate(ctx, "p1", now, now.AddDate(0, 0, 1), BucketDay)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Buckets) != 1 || !res.Buckets[0].Start.Equal(time.Date(2026, 3, 10, 0, 0, 0, 0, ams)) {
			t.Fatalf("buckets %+v", res.Buckets)
		}
		return res.Buckets[0]
	}
	b := today()
	// 12:07 is 48 whole quarters into the day, two of them measured
	share := (12*60 + 7) / (24 * 60.0)
	if !near(b.ElectricityEUR, 1.5*0.30) || !near(b.FixedEUR, 1.2*share) || b.Complete ||
		b.Missing.Quarters != 46 || b.Missing.Partial != 1 || b.Missing.ContractDays != 0 {
		t.Fatalf("today %+v", b)
	}
	if n := fx.c.cached(); n != 68 { // 1 January - 9 March
		t.Fatalf("%d cached days", n)
	}
	fx.quarters(t, store.Quarter{Start: q.Add(30 * time.Minute), ImportKWh: f(2), ExportKWh: f(0)})
	if b := today(); !near(b.ElectricityEUR, 3.5*0.30) || b.Missing.Quarters != 45 {
		t.Fatalf("after a new quarter %+v", b)
	}
}

func TestPriceYouPay(t *testing.T) {
	tl := Timeline{
		{Name: "Dyn", Start: "2026-01-01", End: "2026-08-31", Type: TypeDynamic, VATPct: 21,
			Electricity: Electricity{MarkupCt: 2, TaxCt: 11}, Gas: Gas{MarkupCt: 5, TaxCt: 70}},
		{Name: "Vast", Start: "2026-09-01", Type: TypeFixed, VATPct: 21,
			Electricity: Electricity{NormalCt: 30, LowCt: 20, TaxCt: 11}, Gas: Gas{RateCt: 120, TaxCt: 70}},
	}
	jun := time.Date(2026, 6, 3, 12, 0, 0, 0, ams)
	if v, ok := tl.ElectricityPriceCt(jun, ams, 100); !ok || !near(v, 12.1+2+11) {
		t.Fatalf("dynamic %v %v", v, ok)
	}
	if v, ok := tl.GasPriceEUR(jun, ams, 0.5); !ok || !near(v, 0.605+0.75) {
		t.Fatalf("dynamic gas %v", v)
	}
	night := time.Date(2026, 9, 2, 23, 30, 0, 0, ams) // Wednesday night: low rate
	if v, _ := tl.ElectricityPriceCt(night, ams, 100); !near(v, 31) {
		t.Fatalf("fixed low %v", v)
	}
	if v, _ := tl.GasPriceEUR(night, ams, 0.5); !near(v, 1.9) {
		t.Fatalf("fixed gas %v", v)
	}
	if _, ok := tl.ElectricityPriceCt(time.Date(2025, 12, 31, 12, 0, 0, 0, ams), ams, 100); ok {
		t.Fatal("price without contract")
	}
}

// Switching supplier mid-year: net metering still runs over the calendar
// year, each kWh is priced by the contract of its own day, and VAT is added
// to the market price only (entered amounts are already incl. VAT).
func TestSupplierSwitchMidYear(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 12, 1, 0, 0, 0, 0, ams)
	fx := newFixture(t, now)
	fx.contract(t, Contract{Name: "Dynamisch A", Start: "2026-01-01", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 11, ExportCt: 1}})
	fx.contract(t, Contract{Name: "Vast B", Start: "2026-07-15", Type: TypeFixed,
		Electricity: Electricity{RateCt: 30, TaxCt: 11, ExportCt: 5}})
	may := time.Date(2026, 5, 20, 13, 0, 0, 0, ams)
	oct := time.Date(2026, 10, 7, 19, 0, 0, 0, ams)
	fx.prices(t, prices.Market, may, 15*time.Minute, 100) // 100 EUR/MWh excl. VAT
	fx.quarters(t,
		store.Quarter{Start: may, ImportKWh: f(0), ExportKWh: f(5)},
		store.Quarter{Start: oct, ImportKWh: f(3), ExportKWh: f(0)},
	)
	res, err := fx.c.Calculate(ctx, "p1", time.Date(2026, 1, 1, 0, 0, 0, 0, ams), now, BucketMonth)
	if err != nil {
		t.Fatal(err)
	}
	m := map[time.Month]Bucket{}
	for _, b := range res.Buckets {
		m[b.Start.Month()] = b
	}
	// May (dynamic): 5 kWh × (0.100 × 1.21 + 0.01); nothing to net yet
	if b := m[time.May]; !near(b.ExportEUR, 5*(0.121+0.01)) || b.NettingEUR != 0 {
		t.Fatalf("may %+v", b)
	}
	// October (fixed): 3 kWh × 30 ct + 3 × 11 ct, and 3 kWh of May's export
	// netted at the October contract: 30 + 11 - 5 ct each
	if b := m[time.October]; !near(b.ElectricityEUR, 0.9) || !near(b.ElectricityTaxEUR, 0.33) || !near(b.NettingEUR, 3*0.36) {
		t.Fatalf("october %+v", b)
	}
	if res.Total.Missing.ContractDays != 0 {
		t.Fatalf("days without contract: %d", res.Total.Missing.ContractDays)
	}
}

// fullDay returns the 96 complete quarter hours of a winter day, 0.1 kWh each.
func fullDay(day time.Time) []store.Quarter {
	var qs []store.Quarter
	for t := day; t.Before(day.AddDate(0, 0, 1)); t = t.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: t, ImportKWh: f(0.1), ExportKWh: f(0)})
	}
	return qs
}

func TestQuarterQualityDecidesCompleteness(t *testing.T) {
	ctx := context.Background()
	day := time.Date(2026, 2, 10, 0, 0, 0, 0, ams)
	setup := func(t *testing.T, change func(qs []store.Quarter)) Bucket {
		fx := newFixture(t, day.AddDate(0, 0, 2))
		fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30}})
		qs := fullDay(day)
		change(qs)
		var keep []store.Quarter
		for _, q := range qs {
			if q.Quality != "drop" {
				keep = append(keep, q)
			}
		}
		fx.quarters(t, keep...)
		res, err := fx.c.Calculate(ctx, "p1", day, day.AddDate(0, 0, 1), BucketDay)
		if err != nil {
			t.Fatal(err)
		}
		return res.Buckets[0]
	}
	t.Run("all measured", func(t *testing.T) {
		b := setup(t, func([]store.Quarter) {})
		if !b.Complete || b.Missing != (Missing{}) || !near(b.ElectricityEUR, 96*0.1*0.30) {
			t.Fatalf("%+v", b)
		}
	})
	t.Run("missing quarter", func(t *testing.T) {
		b := setup(t, func(qs []store.Quarter) { qs[10].Quality = "drop" })
		if b.Complete || b.Missing.Quarters != 1 {
			t.Fatalf("%+v", b)
		}
	})
	t.Run("partial quarter is kept but not complete", func(t *testing.T) {
		b := setup(t, func(qs []store.Quarter) { qs[10].Quality, qs[10].Coverage = "partial", 0.4 })
		if b.Complete || b.Missing.Partial != 1 || b.Missing.Quarters != 0 || !near(b.ElectricityEUR, 96*0.1*0.30) {
			t.Fatalf("%+v", b)
		}
	})
	t.Run("meter reset gives no usage and is not complete", func(t *testing.T) {
		b := setup(t, func(qs []store.Quarter) {
			qs[10].Quality, qs[10].Coverage, qs[10].ImportKWh, qs[10].ExportKWh = "anomalous", 1, nil, nil
		})
		if b.Complete || b.Missing.Anomalous != 1 || b.Missing.Quarters != 0 || !near(b.ImportKWh, 95*0.1) {
			t.Fatalf("%+v", b)
		}
	})
}

// BenchmarkYear calculates a full year of quarter hours with prices: the
// worst case of the cost page (a year per month).
func BenchmarkYear(b *testing.B) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, ams)
	to := from.AddDate(1, 0, 0)
	fx := newFixture(b, to)
	fx.contract(b, Contract{Name: "Dyn", Start: "2026-01-01", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 11}, Gas: Gas{TaxCt: 70}, Monthly: []MonthlyCost{{Name: "x", EUR: 10}}})
	var qs []store.Quarter
	var ps []float64
	for t := from; t.Before(to); t = t.Add(15 * time.Minute) {
		qs = append(qs, store.Quarter{Start: t, ImportKWh: f(0.1), ExportKWh: f(0.05), GasM3: f(0.02)})
		ps = append(ps, 80)
	}
	fx.quarters(b, qs...)
	fx.prices(b, prices.Market, from, 15*time.Minute, ps...)
	b.Run("cold", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			fx.c.Invalidate("", from)
			if res, err := fx.c.Calculate(context.Background(), "p1", from, to, BucketMonth); err != nil || len(res.Buckets) != 12 {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if res, err := fx.c.Calculate(context.Background(), "p1", from, to, BucketMonth); err != nil || len(res.Buckets) != 12 {
				b.Fatal(err)
			}
		}
	})
}

// From 2027 the market price part of the feed-in compensation has no VAT and
// is not negative; before 2027 it keeps VAT (unchanged results), and the
// contract can override both ways.
func TestFeedInVATFrom2027(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		day       time.Time
		exportVAT *bool
		price     float64 // EUR/MWh
		want      float64 // EUR for 1 kWh exported, ExportCt -1
	}{
		{"2026 default incl. VAT", time.Date(2026, 6, 1, 0, 0, 0, 0, ams), nil, 100, 0.121 - 0.01},
		{"2027 default bare price", time.Date(2027, 6, 1, 0, 0, 0, 0, ams), nil, 100, 0.100 - 0.01},
		{"2027 incl. VAT by contract", time.Date(2027, 6, 1, 0, 0, 0, 0, ams), &yes, 100, 0.121 - 0.01},
		{"2026 excl. VAT by contract", time.Date(2026, 6, 1, 0, 0, 0, 0, ams), &no, 100, 0.100 - 0.01},
		{"2027 negative price floored", time.Date(2027, 6, 1, 0, 0, 0, 0, ams), nil, -50, 0 - 0.01},
		{"2026 negative price kept", time.Date(2026, 6, 1, 0, 0, 0, 0, ams), nil, -50, -0.0605 - 0.01},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := newFixture(t, tc.day.AddDate(0, 0, 1))
			fx.contract(t, Contract{Name: "Dyn", Start: tc.day.Format(dateLayout), Type: TypeDynamic,
				Electricity: Electricity{ExportCt: -1}, ExportVAT: tc.exportVAT})
			q := tc.day.Add(12 * time.Hour)
			fx.prices(t, prices.Market, q, 15*time.Minute, tc.price)
			fx.quarters(t, store.Quarter{Start: q, ImportKWh: f(0), ExportKWh: f(1)})
			res, err := fx.c.Calculate(context.Background(), "p1", tc.day, tc.day.AddDate(0, 0, 1), BucketDay)
			if err != nil {
				t.Fatal(err)
			}
			if got := res.Total.ExportEUR; !near(got, tc.want) {
				t.Fatalf("export credit %v, want %v", got, tc.want)
			}
		})
	}
}
