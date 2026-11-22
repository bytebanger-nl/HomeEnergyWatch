package costs

import (
	"context"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// UnitPrices is what one more kWh of electricity and one more m³ of gas cost
// at a moment under the contract of that day, all-in: market price or fixed
// rate, supplier markup, energy tax and VAT. Net metering is left out (an
// extra kWh is priced as bought), and so are fixed costs.
type UnitPrices struct {
	ElectricityEUR float64 // per kWh
	GasEUR         float64 // per m³
	// HasElectricity and HasGas are false without a contract for the day or
	// without the market price a dynamic contract needs.
	HasElectricity, HasGas bool
}

// UnitPrices prices one kWh and one m³ at t through quarter(), so it follows
// the cost rules of the cost overview. Only for the energy timeline.
func (c *Calculator) UnitPrices(ctx context.Context, t time.Time) (UnitPrices, error) {
	var out UnitPrices
	if c.water() {
		return out, nil
	}
	timeline, err := c.Contracts.List(ctx)
	if err != nil {
		return out, err
	}
	loc := c.loc()
	contract := timeline.At(t.In(loc).Format(dateLayout))
	if contract == nil {
		return out, nil
	}
	// a quarter that starts at t, with prices around it (a gas day starts at
	// 06:00 the day before at the latest)
	from, to := t.Add(-30*time.Hour), t.Add(time.Hour)
	power, err := c.Store.Prices(ctx, prices.Market, from, to)
	if err != nil {
		return out, err
	}
	gas, err := c.Store.Prices(ctx, prices.GasMarket, from, to)
	if err != nil {
		return out, err
	}
	one := 1.0
	var e Bucket
	quarter(&e, store.Quarter{Start: t, ImportKWh: &one}, contract, power, gas, 0, 0, loc, 0)
	if e.Missing.Prices == 0 {
		out.ElectricityEUR, out.HasElectricity = e.ElectricityEUR+e.ElectricityTaxEUR, true
	}
	var g Bucket
	quarter(&g, store.Quarter{Start: t, GasM3: &one}, contract, power, gas, 0, 0, loc, 0)
	if g.Missing.GasPrices == 0 {
		out.GasEUR, out.HasGas = g.GasEUR+g.GasTaxEUR, true
	}
	return out, nil
}
