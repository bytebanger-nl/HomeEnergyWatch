package costs

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
)

// UnitPrices prices one kWh and one m³ all-in at the moment asked, with the
// gas day price that started at 06:00.
func TestUnitPricesDynamic(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 1))
	fx.contract(t, Contract{Name: "Dynamisch", Start: "2026-01-01", Type: TypeDynamic,
		Electricity: Electricity{MarkupCt: 2, TaxCt: 10}, Gas: Gas{MarkupCt: 5, TaxCt: 60}})
	at := day.Add(9*time.Hour + 20*time.Minute)
	fx.prices(t, prices.Market, day.Add(9*time.Hour+15*time.Minute), 15*time.Minute, 100)
	fx.prices(t, prices.GasMarket, day.Add(6*time.Hour), 24*time.Hour, 0.5)

	u, err := fx.c.UnitPrices(context.Background(), at)
	if err != nil {
		t.Fatal(err)
	}
	if !u.HasElectricity || !near(u.ElectricityEUR, 0.1*1.21+0.02+0.10) {
		t.Errorf("electricity %+v, want 0.241", u)
	}
	if !u.HasGas || !near(u.GasEUR, 0.5*1.21+0.05+0.60) {
		t.Errorf("gas %+v, want 1.255", u)
	}

	// no electricity price for the next hour
	u, err = fx.c.UnitPrices(context.Background(), at.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.HasElectricity || !u.HasGas {
		t.Errorf("an hour later %+v, want gas only", u)
	}
}

// Without a contract for the day there is no unit price.
func TestUnitPricesNoContract(t *testing.T) {
	day := date(2026, 6, 10)
	fx := newFixture(t, day.AddDate(0, 0, 1))
	fx.contract(t, Contract{Name: "Vast", Start: "2026-07-01", Type: TypeFixed, Electricity: Electricity{RateCt: 30, TaxCt: 10}})
	u, err := fx.c.UnitPrices(context.Background(), day.Add(12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if u.HasElectricity || u.HasGas {
		t.Errorf("%+v, want nothing before the contract", u)
	}
}
