// Package prices fetches Dutch day-ahead electricity prices (EnergyZero, Nord
// Pool, ENTSO-E) and gas day prices (EnergyZero) and keeps them in the store.
//
// The Netherlands is part of the single European day-ahead coupling (SDAC),
// so the NL price of a delivery period is the same at EnergyZero, Nord Pool,
// EPEX and ENTSO-E. Since 1 October 2025 the day-ahead market uses 15-minute
// periods; before that prices are hourly. Gas is priced per gas day, which
// runs from 06:00 to 06:00 Dutch time.
package prices

import (
	"context"
	"errors"
	"time"
)

// Market identifies the Dutch day-ahead electricity market in the store.
const Market = "NL-DA"

// GasMarket identifies Dutch gas day prices in the store.
const GasMarket = "NL-GAS"

// Commodity describes what a Service keeps: the market in the store, the unit
// of Price.Value and the delivery day of a calendar date.
type Commodity struct {
	Name   string // "electricity" or "gas"
	Market string
	Unit   string
	Bounds func(day time.Time) (time.Time, time.Time)
}

// Electricity is the day-ahead electricity market, in EUR/MWh per period.
var Electricity = Commodity{Name: "electricity", Market: Market, Unit: "EUR/MWh", Bounds: DayBounds}

// Gas is the gas day price, in EUR/m³ per gas day.
var Gas = Commodity{Name: "gas", Market: GasMarket, Unit: "EUR/m3", Bounds: GasDayBounds}

// Price is the price of one delivery period.
type Price struct {
	Start    time.Time     `json:"start"`
	Duration time.Duration `json:"-"`
	Value    float64       `json:"value"` // in the unit of the commodity
	Source   string        `json:"source"`
}

// End returns the end of the delivery period.
func (p Price) End() time.Time { return p.Start.Add(p.Duration) }

// ErrNotPublished means the prices for that day are not available yet.
var ErrNotPublished = errors.New("prices: not published yet")

// ErrTokenRejected means the source refused the API token.
var ErrTokenRejected = errors.New("token rejected")

// Source fetches the prices of one delivery day: a calendar day in
// Europe/Amsterdam for electricity, the gas day starting on that date for gas.
// A source may return neighbouring periods as well; they are stored too.
type Source interface {
	Name() string
	Day(ctx context.Context, day time.Time) ([]Price, error)
}

// Amsterdam is the time zone of the delivery day.
var Amsterdam = func() *time.Location {
	loc, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		panic(err) // time/tzdata is embedded by the main package
	}
	return loc
}()

// DayBounds returns the UTC start and end of a delivery day (23, 24 or 25 h).
func DayBounds(day time.Time) (time.Time, time.Time) {
	d := day.In(Amsterdam)
	start := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, Amsterdam)
	return start.UTC(), start.AddDate(0, 0, 1).UTC()
}

// GasDayBounds returns the UTC start and end of the gas day that starts on
// the calendar date of day, from 06:00 to 06:00 Dutch time (23, 24 or 25 h).
func GasDayBounds(day time.Time) (time.Time, time.Time) {
	d := day.In(Amsterdam)
	start := time.Date(d.Year(), d.Month(), d.Day(), 6, 0, 0, 0, Amsterdam)
	return start.UTC(), start.AddDate(0, 0, 1).UTC()
}

// complete reports whether prices cover [from, to) without gaps.
func complete(ps []Price, from, to time.Time) bool {
	t := from
	for _, p := range ps {
		if p.Start.After(t) {
			return false
		}
		if p.End().After(t) {
			t = p.End()
		}
	}
	return !t.Before(to)
}
