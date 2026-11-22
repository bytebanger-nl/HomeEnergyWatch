package main

import (
	"context"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/rules"
)

// vapidKeyFile holds the key Web Push messages are signed with (data dir).
const vapidKeyFile = "vapid-key.pem"

// defaultPushContact is the contact push services get with every message
// (the sub claim of the VAPID token); Apple requires one.
const defaultPushContact = "https://github.com/bytebanger-nl/homeenergywatch"

// rulePrices gives the rules the metrics of the price device: market prices
// of electricity (EUR/kWh) and gas (EUR/m³), the all-in prices of the energy
// contract of the day, and today's average and ranking.
type rulePrices struct {
	power, gas *prices.Service
	calc       *costs.Calculator
	loc        *time.Location
}

func (p rulePrices) metric(ctx context.Context, metric string, t time.Time) (float64, bool) {
	switch metric {
	case rules.MetricPrice:
		if v, ok := priceAt(ctx, p.power, t); ok {
			return v / 1000, true // EUR/MWh
		}
	case rules.MetricGasPrice:
		return priceAt(ctx, p.gas, t)
	case rules.MetricPriceAllIn, rules.MetricGasPriceAllIn:
		if p.calc == nil {
			return 0, false
		}
		u, err := p.calc.UnitPrices(ctx, t)
		if err != nil {
			return 0, false
		}
		if metric == rules.MetricPriceAllIn {
			return u.ElectricityEUR, u.HasElectricity
		}
		return u.GasEUR, u.HasGas
	case rules.MetricPriceAvgToday, rules.MetricCheaperHours:
		day := dayPrices(ctx, p.power, t, p.loc)
		if len(day) == 0 {
			return 0, false
		}
		if metric == rules.MetricPriceAvgToday {
			sum, n := 0.0, 0.0
			for _, x := range day {
				sum += x.Value * x.Duration.Hours()
				n += x.Duration.Hours()
			}
			return sum / n / 1000, true
		}
		now, ok := periodAt(day, t)
		if !ok {
			return 0, false
		}
		cheaper := 0.0
		for _, x := range day {
			if x.Value < now.Value {
				cheaper += x.Duration.Hours()
			}
		}
		return cheaper, true
	}
	return 0, false
}

// priceAt returns the price at t in the unit of the service. With
// quarter-hour and hourly prices stored, the shortest period wins.
func priceAt(ctx context.Context, svc *prices.Service, t time.Time) (float64, bool) {
	if svc == nil {
		return 0, false
	}
	list, err := svc.Range(ctx, t, t.Add(time.Second))
	if err != nil {
		return 0, false
	}
	p, ok := periodAt(list, t)
	return p.Value, ok
}

// periodAt returns the shortest period of list that contains t.
func periodAt(list []prices.Price, t time.Time) (prices.Price, bool) {
	var best *prices.Price
	for i, p := range list {
		if p.Start.After(t) || !p.End().After(t) {
			continue
		}
		if best == nil || p.Duration < best.Duration {
			best = &list[i]
		}
	}
	if best == nil {
		return prices.Price{}, false
	}
	return *best, true
}

// dayPrices returns the prices of the local calendar day of t, at the finest
// resolution stored for that day.
func dayPrices(ctx context.Context, svc *prices.Service, t time.Time, loc *time.Location) []prices.Price {
	if svc == nil {
		return nil
	}
	lt := t.In(loc)
	from := time.Date(lt.Year(), lt.Month(), lt.Day(), 0, 0, 0, 0, loc)
	to := from.AddDate(0, 0, 1)
	list, err := svc.Range(ctx, from, to)
	if err != nil {
		return nil
	}
	var finest time.Duration
	for _, p := range list {
		if p.Duration > 0 && (finest == 0 || p.Duration < finest) {
			finest = p.Duration
		}
	}
	var out []prices.Price
	for _, p := range list {
		if p.Duration == finest && !p.Start.Before(from) && p.Start.Before(to) {
			out = append(out, p)
		}
	}
	return out
}
