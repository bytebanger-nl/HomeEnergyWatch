package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// HourAverage averages the quarters of the clock hour, accepts an hourly
// price as is, and knows nothing when part of the hour has no price.
func TestHourAverage(t *testing.T) {
	h := date(2026, 6, 10).Add(9 * time.Hour)
	q := func(m int, v float64) store.MarketPrice {
		return store.MarketPrice{Start: h.Add(time.Duration(m) * time.Minute), Duration: 15 * time.Minute, Value: v}
	}
	ps := []store.MarketPrice{q(-15, 1000), q(0, 10), q(15, 20), q(30, 30), q(45, 100), q(60, 1000)}
	for _, at := range []time.Time{h, h.Add(20 * time.Minute), h.Add(59 * time.Minute)} {
		if v, ok := HourAverage(ps, at); !ok || !near(v, 40) {
			t.Errorf("at %v: %v %v, want 40", at, v, ok)
		}
	}
	if v, ok := HourAverage([]store.MarketPrice{{Start: h, Duration: time.Hour, Value: 7}}, h.Add(30*time.Minute)); !ok || v != 7 {
		t.Errorf("hourly price: %v %v, want 7", v, ok)
	}
	if _, ok := HourAverage(ps[:4], h); ok {
		t.Error("an hour with a missing quarter must be unknown")
	}
}

// A dynamic contract with Hourly bills every quarter at its hour's average;
// without it, at the quarter's own price.
func TestHourlyContract(t *testing.T) {
	day := date(2026, 6, 10)
	h := day.Add(9 * time.Hour)
	for _, hourly := range []bool{false, true} {
		fx := newFixture(t, day.AddDate(0, 0, 1))
		fx.contract(t, Contract{Name: "Dynamisch", Start: "2026-01-01", Type: TypeDynamic,
			Electricity: Electricity{Hourly: hourly}})
		fx.prices(t, prices.Market, h, 15*time.Minute, 10, 20, 30, 100)
		// 1 kWh in the first quarter only (10 EUR/MWh) vs. the hour average of 40
		fx.quarters(t, store.Quarter{Start: h, ImportKWh: f(1), ExportKWh: f(0)})
		r, err := fx.c.Calculate(context.Background(), "p1", h, h.Add(time.Hour), BucketDay)
		if err != nil {
			t.Fatal(err)
		}
		want := 0.010 * 1.21
		if hourly {
			want = 0.040 * 1.21
		}
		if !near(r.Total.ElectricityEUR, want) {
			t.Errorf("hourly=%v: %v, want %v", hourly, r.Total.ElectricityEUR, want)
		}
	}
}

// Hourly only applies to dynamic contracts.
func TestHourlyNormalize(t *testing.T) {
	c := Contract{Type: TypeFixed, Electricity: Electricity{Hourly: true}}
	c.Normalize()
	if c.Electricity.Hourly {
		t.Error("a fixed contract cannot bill per hour")
	}
}
