package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// dualContract is a fixed contract with normal and low rates.
func dualContract(fx *fixture, t *testing.T) {
	fx.contract(t, Contract{Name: "Vast", Start: "2026-01-01", Type: TypeFixed,
		Electricity: Electricity{NormalCt: 30, LowCt: 20, TaxCt: 10, ExportCt: 5}})
}

func dayCosts(t *testing.T, fx *fixture, day time.Time) Bucket {
	t.Helper()
	res, err := fx.c.Calculate(context.Background(), "p1", day, day.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	return res.Buckets[0]
}

// The meter's own tariff wins over the clock: Christmas (a Friday) is low
// all day, which the clock does not know.
func TestMeterTariffPricesHoliday(t *testing.T) {
	fx := newFixture(t, time.Date(2027, 1, 5, 0, 0, 0, 0, ams))
	dualContract(fx, t)
	xmas := time.Date(2026, 12, 25, 0, 0, 0, 0, ams)
	tue := time.Date(2026, 12, 22, 0, 0, 0, 0, ams)
	wed := time.Date(2026, 12, 23, 0, 0, 0, 0, ams)
	fx.quarters(t,
		store.Quarter{Start: xmas.Add(12 * time.Hour), ImportKWh: f(1), ImportLowKWh: f(1)},
		store.Quarter{Start: tue.Add(12 * time.Hour), ImportKWh: f(1)},                                      // old data: the clock
		store.Quarter{Start: wed.Add(22*time.Hour + 45*time.Minute), ImportKWh: f(1), ImportLowKWh: f(0.4)}, // switch inside the quarter
		store.Quarter{Start: wed.Add(23 * time.Hour), ImportKWh: f(1), ImportLowKWh: f(0)},                  // meter at normal although the clock says low
	)
	if b := dayCosts(t, fx, xmas); !near(b.ElectricityEUR, 0.20) || !near(b.ElectricityTaxEUR, 0.10) {
		t.Fatalf("christmas %+v", b)
	}
	if b := dayCosts(t, fx, tue); !near(b.ElectricityEUR, 0.30) {
		t.Fatalf("tuesday without split %+v", b)
	}
	if b := dayCosts(t, fx, wed); !near(b.ElectricityEUR, 0.4*0.20+0.6*0.30+0.30) {
		t.Fatalf("wednesday %+v", b)
	}
}

// Netted kWh are worth the rate of the tariff in which they were netted;
// without import that quarter the export tells it.
func TestMeterTariffNetting(t *testing.T) {
	fx := newFixture(t, time.Date(2027, 1, 5, 0, 0, 0, 0, ams))
	dualContract(fx, t)
	tue := time.Date(2026, 12, 22, 12, 0, 0, 0, ams)
	xmas := time.Date(2026, 12, 25, 12, 0, 0, 0, ams)
	fx.quarters(t,
		store.Quarter{Start: tue, ImportKWh: f(1), ExportKWh: f(0)},
		store.Quarter{Start: xmas, ImportKWh: f(0), ExportKWh: f(1), ImportLowKWh: f(0), ExportLowKWh: f(1)},
	)
	res, err := fx.c.Calculate(context.Background(), "p1", tue.Truncate(24*time.Hour).Add(-time.Hour), xmas.AddDate(0, 0, 1), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	var netting float64
	for _, b := range res.Buckets {
		netting += b.NettingEUR
	}
	if !near(netting, (20+10-5)/100.0) {
		t.Fatalf("netting %v, want the low rate", netting)
	}
}

// DST days (100 and 92 quarter hours, both Sundays the clock prices low)
// price every quarter by the meter's tariff.
func TestMeterTariffDSTDays(t *testing.T) {
	for _, c := range []struct {
		day      time.Time
		quarters int
	}{
		{time.Date(2026, 10, 25, 0, 0, 0, 0, ams), 100},
		{time.Date(2026, 3, 29, 0, 0, 0, 0, ams), 92},
	} {
		fx := newFixture(t, c.day.AddDate(0, 0, 2))
		dualContract(fx, t)
		var qs []store.Quarter
		for q := c.day; q.Before(c.day.AddDate(0, 0, 1)); q = q.Add(15 * time.Minute) {
			qs = append(qs, store.Quarter{Start: q, ImportKWh: f(0.1), ImportLowKWh: f(0)})
		}
		if len(qs) != c.quarters {
			t.Fatalf("%s: %d quarters", c.day.Format(dateLayout), len(qs))
		}
		fx.quarters(t, qs...)
		b := dayCosts(t, fx, c.day)
		if !b.Complete || !near(b.ImportKWh, 0.1*float64(c.quarters)) || !near(b.ElectricityEUR, 0.1*float64(c.quarters)*0.30) {
			t.Fatalf("%s: %+v", c.day.Format(dateLayout), b)
		}
	}
}

func TestLowShare(t *testing.T) {
	sat := time.Date(2026, 12, 19, 12, 0, 0, 0, ams)
	tue := time.Date(2026, 12, 22, 12, 0, 0, 0, ams)
	for _, c := range []struct {
		part  *float64
		total float64
		at    time.Time
		want  float64
	}{
		{nil, 1, tue, 0},
		{nil, 1, sat, 1},
		{f(0.25), 1, tue, 0.25},
		{f(2), 1, tue, 1}, // more than the total: clamped
		{f(0), 0, sat, 1}, // nothing used: the clock
	} {
		if got := lowShare(c.part, c.total, c.at); !near(got, c.want) {
			t.Errorf("lowShare(%v, %v, %v) = %v, want %v", c.part, c.total, c.at, got, c.want)
		}
	}
}
