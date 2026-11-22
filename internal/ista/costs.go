package ista

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// How ista settles heating (one settlement per period):
//
//	price per unit = variable energy costs of the building / all its units
//	price per dm²  = (fixed energy + other costs + ista services) / its floor area
//	your costs     = your units × price per unit + your floor area × price per dm²
//
// Prices are rounded to 6 decimals and amounts to cents, as on the
// settlement. During a period the prices are not known yet: they are taken
// from the contract, or from the last settlement (optionally corrected).

// Contract is what the user enters for one settlement period.
type Contract struct {
	Y int `json:"y"`
	// AreaDM2 is the dwelling's floor area in dm² (on the settlement).
	AreaDM2 float64 `json:"area_dm2"`
	// UnitPrice and AreaPrice, when set, are used for the estimate instead
	// of the last settlement's.
	UnitPrice *float64 `json:"unit_price,omitempty"`
	AreaPrice *float64 `json:"area_price,omitempty"`
	// CorrectionPct changes the last settlement's prices for the estimate
	// (e.g. +10 when the building's energy got dearer).
	CorrectionPct float64 `json:"correction_pct,omitempty"`
	// EstimateUnits is the expected usage of a whole period in units, for
	// the forecast: spread over the year with a heating profile (the
	// building's months, else a standard one). When set it goes before
	// last year's usage.
	EstimateUnits float64 `json:"estimate_units,omitempty"`
	// Advance is the monthly advance: each amount is paid on its date and
	// on the same day of every month after, until a later amount.
	Advance []Payment `json:"advance,omitempty"`
	// Final holds the settlement once it arrived: the period is then
	// definitive and the settlement counts, not the estimate.
	Final *Settlement `json:"final,omitempty"`
	Note  string      `json:"note,omitempty"`
}

// Payment is an advance amount from a date.
type Payment struct {
	From   string  `json:"from"`
	Amount float64 `json:"amount"`
}

// Settlement is the figures of the settlement (section "berekening
// eenheidsprijzen" and the dwelling's total).
type Settlement struct {
	VariableEnergy float64 `json:"variable_energy"`
	FixedEnergy    float64 `json:"fixed_energy"`
	Other          float64 `json:"other"`
	Services       float64 `json:"services"`
	UnitsComplex   float64 `json:"units_complex"`
	AreaComplexDM2 float64 `json:"area_complex_dm2"`
	// Units is the dwelling's billed units; the measured ones when zero.
	Units float64 `json:"units,omitempty"`
	// Total is the dwelling's total on the settlement, to check against.
	Total float64 `json:"total,omitempty"`
}

func round6(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// Prices returns the settlement's price per unit and per dm².
func (st Settlement) Prices() (unit, area float64) {
	if st.UnitsComplex > 0 {
		unit = round6(st.VariableEnergy / st.UnitsComplex)
	}
	if st.AreaComplexDM2 > 0 {
		area = round6((st.FixedEnergy + st.Other + st.Services) / st.AreaComplexDM2)
	}
	return unit, area
}

func (c Contract) check(periods []Period) error {
	known := false
	for _, p := range periods {
		known = known || p.Y == c.Y
	}
	if !known {
		return fmt.Errorf("onbekende afrekenperiode %d", c.Y)
	}
	if c.AreaDM2 <= 0 {
		return errors.New("vul het vloeroppervlak van de woning in (dm², staat op de afrekening)")
	}
	if (c.UnitPrice != nil && *c.UnitPrice < 0) || (c.AreaPrice != nil && *c.AreaPrice < 0) {
		return errors.New("een prijs kan niet negatief zijn")
	}
	if c.EstimateUnits < 0 {
		return errors.New("verwacht jaarverbruik kan niet negatief zijn")
	}
	if c.CorrectionPct < -90 || c.CorrectionPct > 500 {
		return errors.New("correctie moet tussen -90 en 500 procent liggen")
	}
	for _, p := range c.Advance {
		if _, err := time.Parse(DayLayout, p.From); err != nil {
			return fmt.Errorf("voorschot: ongeldige datum %q", p.From)
		}
		if p.Amount < 0 {
			return errors.New("voorschot: bedrag kan niet negatief zijn")
		}
	}
	if f := c.Final; f != nil {
		if f.VariableEnergy < 0 || f.FixedEnergy < 0 || f.Other < 0 || f.Services < 0 || f.Units < 0 || f.Total < 0 {
			return errors.New("afrekening: bedragen kunnen niet negatief zijn")
		}
		// the settlement holds the complex's totals; the prices follow from them
		if f.VariableEnergy <= 0 {
			return errors.New("afrekening: vul de variabele energiekosten van het hele complex in (een totaalbedrag in euro's, geen prijs per eenheid)")
		}
		if f.FixedEnergy+f.Other+f.Services <= 0 {
			return errors.New("afrekening: vul de vaste energiekosten, overige kosten en diensten van het hele complex in (totaalbedragen in euro's, geen prijs per dm²)")
		}
		if f.UnitsComplex <= 0 {
			return errors.New("afrekening: vul het totaal aantal eenheden van het hele complex in")
		}
		if f.AreaComplexDM2 <= 0 {
			return errors.New("afrekening: vul het vloeroppervlak van het hele complex in (dm²)")
		}
		if f.AreaComplexDM2 < c.AreaDM2 {
			return errors.New("afrekening: het vloeroppervlak van het complex is kleiner dan dat van je woning; vul het totaal van alle woningen in")
		}
		if f.Units > f.UnitsComplex {
			return errors.New("afrekening: jouw eenheden zijn meer dan die van het hele complex; vul bij eenheden complex het totaal van alle woningen in")
		}
	}
	return nil
}

// Contracts returns an address's contracts, oldest first.
func (s *Service) Contracts(ctx context.Context, cuid string) (string, []Contract, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return "", nil, err
	}
	cs, err := s.contracts(ctx, a.Cuid)
	return a.Cuid, cs, err
}

func (s *Service) contracts(ctx context.Context, cuid string) ([]Contract, error) {
	rows, err := s.Store.IstaContracts(ctx, cuid)
	if err != nil {
		return nil, err
	}
	out := make([]Contract, 0, len(rows))
	for _, r := range rows {
		var c Contract
		if err := json.Unmarshal([]byte(r.Data), &c); err != nil {
			continue
		}
		c.Y = r.Y
		out = append(out, c)
	}
	return out, nil
}

// ErrInvalid wraps a contract that cannot be saved.
type ErrInvalid struct{ Msg string }

func (e ErrInvalid) Error() string { return e.Msg }

// SaveContract stores the contract of a period.
func (s *Service) SaveContract(ctx context.Context, cuid string, c Contract) (Contract, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return c, err
	}
	sort.Slice(c.Advance, func(i, j int) bool { return c.Advance[i].From < c.Advance[j].From })
	if err := c.check(a.Periods); err != nil {
		return c, ErrInvalid{err.Error()}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	if err := s.Store.SaveIstaContract(ctx, store.IstaContract{Cuid: a.Cuid, Y: c.Y, Data: string(b)}); err != nil {
		return c, err
	}
	if s.OnData != nil {
		s.OnData(a.Cuid)
	}
	return c, nil
}

// DeleteContract removes the contract of a period.
func (s *Service) DeleteContract(ctx context.Context, cuid string, y int) error {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return err
	}
	return s.Store.DeleteIstaContract(ctx, a.Cuid, y)
}

// Costs is where a period stands.
type Costs struct {
	Cuid     string    `json:"cuid"`
	Unit     string    `json:"unit"`
	Period   Period    `json:"period"`
	Periods  []Period  `json:"periods"`
	Contract *Contract `json:"contract,omitempty"`
	// Final: the settlement is entered; its figures count.
	Final bool `json:"final"`
	// Running: today falls in the period.
	Running bool   `json:"running"`
	LastDay string `json:"last_day,omitempty"`

	UnitPrice   *float64 `json:"unit_price,omitempty"`
	AreaPrice   *float64 `json:"area_price,omitempty"`
	PriceSource string   `json:"price_source"` // settlement, contract, previous, or "" (none)
	PriceFrom   int      `json:"price_from,omitempty"`
	AreaDM2     float64  `json:"area_dm2,omitempty"`

	UnitsSoFar    float64  `json:"units_so_far"`
	UnitsRest     *float64 `json:"units_rest,omitempty"`
	UnitsExpected *float64 `json:"units_expected,omitempty"`
	// UnitsPrevious is the whole previous period, for comparison.
	UnitsPrevious *float64 `json:"units_previous,omitempty"`
	// RestBasis: the rest of the period follows the same days of the
	// previous period (last_year) or the expected usage entered with the
	// contract (estimate); "" without forecast, ForecastError says why.
	RestBasis     string `json:"rest_basis,omitempty"`
	ForecastError string `json:"forecast_error,omitempty"`
	// PreviousCoverage: the share of the days of the previous period that
	// ista measured (when there is one and no usage is entered).
	PreviousCoverage *float64 `json:"previous_coverage,omitempty"`
	// Percentages: every percentage the forecast used, with its source.
	Percentages []forecast.Used `json:"percentages,omitempty"`
	// Profile is where the heating profile of an estimate comes from:
	// building (its months) or standard.
	Profile string `json:"profile,omitempty"`

	FixedPeriod   *float64 `json:"fixed_period,omitempty"` // the whole period's fixed part
	VariableSoFar *float64 `json:"variable_so_far,omitempty"`
	FixedSoFar    *float64 `json:"fixed_so_far,omitempty"`
	CostSoFar     *float64 `json:"cost_so_far,omitempty"`
	CostExpected  *float64 `json:"cost_expected,omitempty"`
	// CostExpected split: the fixed part (other costs by floor area) and
	// the variable part (energy by units) of the whole period
	FixedExpected    *float64 `json:"fixed_expected,omitempty"`
	VariableExpected *float64 `json:"variable_expected,omitempty"`
	CostLow          *float64 `json:"cost_low,omitempty"`
	CostHigh         *float64 `json:"cost_high,omitempty"`

	Paid         float64 `json:"paid"`
	ToPay        float64 `json:"to_pay"`
	PaymentsLeft int     `json:"payments_left"`
	// Payments counts the advances paid so far, NextPayment is the day of
	// the next one in the period and Monthly the amount that applies now.
	Payments    int      `json:"payments"`
	NextPayment string   `json:"next_payment,omitempty"`
	Monthly     float64  `json:"monthly"`
	Balance     *float64 `json:"balance,omitempty"` // expected costs − all advances: > 0 pay extra
	Advice      *float64 `json:"advice,omitempty"`  // monthly advance that would end at zero

	Check *Check `json:"check,omitempty"`
}

// Check compares the computed settlement with the stated total.
type Check struct {
	Variable float64 `json:"variable"`
	Fixed    float64 `json:"fixed"`
	Computed float64 `json:"computed"`
	Stated   float64 `json:"stated,omitempty"`
	OK       bool    `json:"ok"`
	// Prorated: the fixed part counted by the days of the tenancy matched.
	Prorated bool `json:"prorated,omitempty"`
}

// Costs returns where period y (0: the running one) stands.
func (s *Service) Costs(ctx context.Context, cuid string, y int) (Costs, error) {
	a, err := s.pickAddress(ctx, cuid)
	if err != nil {
		return Costs{}, err
	}
	today := s.today()
	p, ok := pickPeriod(a, y, today)
	if !ok {
		return Costs{}, ErrNoAddress
	}
	out := Costs{Cuid: a.Cuid, Unit: a.Unit, Period: p, Periods: a.Periods}
	contracts, err := s.contracts(ctx, a.Cuid)
	if err != nil {
		return out, err
	}
	var prevFinal *Contract
	for i := range contracts {
		c := contracts[i]
		if c.Y == p.Y {
			out.Contract = &c
		}
		if c.Y < p.Y && c.Final != nil {
			prevFinal = &c
		}
	}
	from, _ := time.Parse(DayLayout, p.From)
	end, _ := time.Parse(DayLayout, p.End)
	out.Running = !today.Before(from) && !today.After(end)

	// measured units up to the last day of the period ista measured: the
	// days after it are ista's estimates and do not count; the rest of the
	// period is forecast from that day
	stop := end
	if today.Before(stop) {
		stop = today
	}
	days, err := s.Store.IstaDays(ctx, a.Cuid, p.From, stop.Format(DayLayout))
	if err != nil {
		return out, err
	}
	last := from.AddDate(0, 0, -1)
	for i := len(days) - 1; i >= 0; i-- {
		if days[i].Estimated {
			continue
		}
		out.UnitsSoFar = days[i].TotalNow
		out.LastDay = days[i].Day
		last, _ = time.Parse(DayLayout, out.LastDay)
		break
	}
	for _, q := range a.Periods {
		if q.Y == p.Y-1 {
			if ds, err := s.Store.IstaDays(ctx, a.Cuid, q.From, q.End); err == nil && len(ds) > 0 {
				out.UnitsPrevious = ptr(ds[len(ds)-1].TotalNow)
			}
		}
	}

	// the rest of the period
	set, err := s.Forecast.Load(ctx)
	if err != nil {
		return out, err
	}
	periodMin := forecast.Or(set.PeriodMinPct, forecast.Defaults.PeriodMinPct)
	var r rest
	if last.Before(end) {
		estimate := 0.0
		if out.Contract != nil {
			estimate = out.Contract.EstimateUnits
		}
		if r, err = s.restUnits(ctx, a, p, last, end, estimate, periodMin); err != nil {
			return out, err
		}
		if r.hasPrev && r.basis != "estimate" {
			out.PreviousCoverage = ptr(math.Round(r.coverage*1000) / 1000)
		}
		if r.basis != "" {
			out.UnitsRest, out.RestBasis, out.Profile = ptr(math.Round(r.units)), r.basis, r.profile
			out.UnitsExpected = ptr(out.UnitsSoFar + math.Round(r.units))
		} else if c := out.Contract; c == nil || c.Final == nil {
			out.ForecastError = r.noForecast(periodMin)
		}
	} else {
		out.UnitsRest, out.UnitsExpected = ptr(0), ptr(out.UnitsSoFar)
	}

	// prices
	c := out.Contract
	if c != nil {
		out.AreaDM2 = c.AreaDM2
	} else if prevFinal != nil {
		out.AreaDM2 = prevFinal.AreaDM2
	}
	switch {
	case c != nil && c.Final != nil:
		u, ar := c.Final.Prices()
		out.UnitPrice, out.AreaPrice, out.PriceSource, out.Final = ptr(u), ptr(ar), "settlement", true
	case c != nil && c.UnitPrice != nil && c.AreaPrice != nil:
		out.UnitPrice, out.AreaPrice, out.PriceSource = ptr(*c.UnitPrice), ptr(*c.AreaPrice), "contract"
	case prevFinal != nil:
		u, ar := prevFinal.Final.Prices()
		f := 1.0
		if c != nil {
			f += c.CorrectionPct / 100
		}
		out.UnitPrice, out.AreaPrice, out.PriceSource, out.PriceFrom = ptr(round6(u*f)), ptr(round6(ar*f)), "previous", prevFinal.Y
		if c != nil && c.UnitPrice != nil {
			out.UnitPrice = ptr(*c.UnitPrice)
		}
		if c != nil && c.AreaPrice != nil {
			out.AreaPrice = ptr(*c.AreaPrice)
		}
	}

	if out.UnitPrice != nil && out.AreaPrice != nil && out.AreaDM2 > 0 {
		periodStart, _ := time.Parse(DayLayout, p.Start)
		inPeriod := end.Sub(periodStart).Hours()/24 + 1
		lived := end.Sub(from).Hours()/24 + 1
		fixed := round2(out.AreaDM2 * *out.AreaPrice * lived / inPeriod)
		out.FixedPeriod = ptr(fixed)
		elapsed := math.Max(0, math.Min(lived, last.Sub(from).Hours()/24+1))
		out.FixedSoFar = ptr(round2(fixed * elapsed / lived))
		out.VariableSoFar = ptr(round2(out.UnitsSoFar * *out.UnitPrice))
		out.CostSoFar = ptr(round2(*out.FixedSoFar + *out.VariableSoFar))
		if out.Final {
			units := out.UnitsSoFar
			if c.Final.Units > 0 {
				units = c.Final.Units
			}
			// the fixed part for a whole year; when the tenancy began during
			// the period ista may count it by the days instead
			ck := &Check{Variable: round2(units * *out.UnitPrice), Fixed: round2(out.AreaDM2 * *out.AreaPrice), Stated: c.Final.Total}
			ck.Computed = round2(ck.Variable + ck.Fixed)
			if c.Final.Total > 0 && math.Abs(ck.Computed-c.Final.Total) >= 0.005 && fixed != ck.Fixed &&
				math.Abs(round2(ck.Variable+fixed)-c.Final.Total) < 0.005 {
				ck.Fixed, ck.Computed, ck.Prorated = fixed, round2(ck.Variable+fixed), true
			}
			ck.OK = c.Final.Total == 0 || math.Abs(ck.Computed-c.Final.Total) < 0.005
			out.Check = ck
			total := ck.Computed
			if c.Final.Total > 0 {
				total = c.Final.Total
			}
			out.CostExpected, out.CostLow, out.CostHigh = ptr(total), ptr(total), ptr(total)
			out.FixedExpected, out.VariableExpected = ptr(ck.Fixed), ptr(round2(total-ck.Fixed))
		} else if out.UnitsExpected != nil {
			exp := round2(*out.UnitsExpected**out.UnitPrice + fixed)
			margin := s.margin(&out, r, set, periodMin)
			out.CostExpected = ptr(exp)
			out.FixedExpected, out.VariableExpected = ptr(fixed), ptr(round2(exp-fixed))
			out.CostLow, out.CostHigh = ptr(round2(exp-margin)), ptr(round2(exp+margin))
		}
	}

	// advances
	if c != nil {
		periodStart, _ := time.Parse(DayLayout, p.Start)
		for _, d := range paymentDays(c.Advance, periodStart, end) {
			if !d.day.After(today) {
				out.Paid += d.amount
				out.Payments++
				out.Monthly = d.amount
			} else {
				if out.PaymentsLeft == 0 {
					out.NextPayment = d.day.Format(DayLayout)
					if out.Payments == 0 {
						out.Monthly = d.amount
					}
				}
				out.ToPay += d.amount
				out.PaymentsLeft++
			}
		}
		out.Paid, out.ToPay = round2(out.Paid), round2(out.ToPay)
		if out.CostExpected != nil && len(c.Advance) > 0 {
			out.Balance = ptr(round2(*out.CostExpected - out.Paid - out.ToPay))
			if !out.Final && out.PaymentsLeft > 0 {
				out.Advice = ptr(math.Ceil((*out.CostExpected - out.Paid) / float64(out.PaymentsLeft)))
			}
		}
	}
	return out, nil
}

type payDay struct {
	day    time.Time
	amount float64
}

// paymentDays lists the advance payments from start to end (inclusive).
func paymentDays(amounts []Payment, start, end time.Time) []payDay {
	var out []payDay
	for i, a := range amounts {
		first, err := time.Parse(DayLayout, a.From)
		if err != nil {
			continue
		}
		until := end
		if i+1 < len(amounts) {
			if next, err := time.Parse(DayLayout, amounts[i+1].From); err == nil {
				until = next.AddDate(0, 0, -1)
			}
		}
		for m := 0; ; m++ {
			d := addMonthsClamped(first, m)
			if d.After(until) || d.After(end) {
				break
			}
			if !d.Before(start) {
				out = append(out, payDay{d, a.Amount})
			}
		}
	}
	return out
}

// addMonthsClamped adds months, keeping the day of the month or the last
// day of a shorter month (the 31st becomes the 30th in April).
func addMonthsClamped(d time.Time, months int) time.Time {
	first := time.Date(d.Year(), d.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	lastDay := first.AddDate(0, 1, -1).Day()
	day := d.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(first.Year(), first.Month(), day, 0, 0, 0, 0, time.UTC)
}

// rest is the estimate of the units of the rest of a period.
type rest struct {
	units   float64
	basis   string // last_year, estimate, or "" (no forecast)
	profile string // building or standard (basis estimate)

	// the previous period: whether there is one, its days and the share of
	// them that ista measured
	hasPrev      bool
	prevStart    string
	prevEnd      string
	coverage     float64
	spreadPct    float64 // the months so far against the previous period
	spreadOK     bool
	spreadMonths int
}

// restUnits estimates the units from after last to end, in this order:
//  1. the expected usage of the period (estimate, from the contract) times
//     the share of those days in a heating profile: what the user expects
//     goes before the previous period;
//  2. else the previous period, when at least periodMin % of its days is
//     measured by ista: the same days a year earlier, and for a day of it
//     that is not measured its yearly usage by the heating profile;
//  3. else nothing: there is no forecast.
//
// It also compares the months of this period so far with the same months of
// the previous period, for the margin.
func (s *Service) restUnits(ctx context.Context, a Address, p Period, last, end time.Time, estimate, periodMin float64) (rest, error) {
	var r rest
	from := last.AddDate(0, 0, 1)
	months, err := s.Store.IstaMonths(ctx, a.Cuid)
	if err != nil {
		return r, err
	}
	shares, source := heatingProfile(months)
	dayShare := func(d time.Time) float64 { return shares[d.Month()-1] / float64(daysIn(d.Year(), d.Month())) }
	if estimate > 0 {
		sum := 0.0
		for d := from; !d.After(end); d = d.AddDate(0, 0, 1) {
			sum += dayShare(d)
		}
		r.units, r.basis, r.profile = estimate*sum, "estimate", source
		return r, nil
	}
	var prev *Period
	for i := range a.Periods {
		if a.Periods[i].Y == p.Y-1 {
			prev = &a.Periods[i]
		}
	}
	if prev == nil {
		return r, nil
	}
	qStart, err1 := time.Parse(DayLayout, prev.Start)
	qEnd, err2 := time.Parse(DayLayout, prev.End)
	if err1 != nil || err2 != nil {
		return r, nil
	}
	r.hasPrev, r.prevStart, r.prevEnd = true, prev.Start, prev.End
	ly, err := s.loadDaily(ctx, a, qStart, qEnd)
	if err != nil {
		return r, err
	}
	known := func(d *daily, day time.Time) (float64, bool) {
		k := day.Format(DayLayout)
		v, ok := d.total[k]
		return v, ok && !d.est[k]
	}
	inPrev := func(d time.Time) bool { return !d.Before(qStart) && !d.After(qEnd) }
	days, have := 0, 0
	sum, share := 0.0, 0.0
	for d := qStart; !d.After(qEnd); d = d.AddDate(0, 0, 1) {
		days++
		if v, ok := known(ly, d); ok {
			have++
			sum += v
			share += dayShare(d)
		}
	}
	r.coverage = float64(have) / float64(max(days, 1))
	if r.coverage*100 < periodMin-1e-9 {
		return r, nil
	}
	yearly := sum
	if share > 0 {
		yearly = sum / share
	}
	for d := from; !d.After(end); d = d.AddDate(0, 0, 1) {
		lyd := d.AddDate(-1, 0, 0)
		if v, ok := known(ly, lyd); ok && inPrev(lyd) {
			r.units += v
		} else {
			r.units += yearly * dayShare(d)
		}
	}
	r.basis = "last_year"

	// this period so far per month against the same days a year earlier
	pFrom, _ := time.Parse(DayLayout, p.From)
	if !last.Before(pFrom) {
		cur, err := s.loadDaily(ctx, a, pFrom, last)
		if err != nil {
			return r, err
		}
		type sums struct {
			cur, ly float64
			days    int
		}
		byMonth := map[string]*sums{}
		for d := pFrom; !d.After(last); d = d.AddDate(0, 0, 1) {
			lyd := d.AddDate(-1, 0, 0)
			c, ok1 := known(cur, d)
			l, ok2 := known(ly, lyd)
			if !ok1 || !ok2 || !inPrev(lyd) {
				continue
			}
			k := d.Format("2006-01")
			m := byMonth[k]
			if m == nil {
				m = &sums{}
				byMonth[k] = m
			}
			m.cur, m.ly, m.days = m.cur+c, m.ly+l, m.days+1
		}
		var sw, sdev float64
		for _, m := range byMonth {
			// a month without heating last year says nothing
			if m.days < 20 || m.ly <= 0 || m.ly < 0.01*sum {
				continue
			}
			dev := m.cur/m.ly - 1
			sw += m.ly
			sdev += m.ly * dev * dev
			r.spreadMonths++
		}
		if r.spreadMonths >= 2 && sw > 0 {
			r.spreadPct, r.spreadOK = 100*math.Sqrt(sdev/sw), true
		}
	}
	return r, nil
}

var monthNames = [...]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func longDay(s string) string {
	d, err := time.Parse(DayLayout, s)
	if err != nil {
		return s
	}
	return fmt.Sprintf("%d %s %d", d.Day(), monthNames[d.Month()-1], d.Year())
}

// noForecast says why there is no forecast of the rest of the period.
func (r rest) noForecast(periodMin float64) string {
	if !r.hasPrev {
		return "Geen prognose: er is geen verwacht jaarverbruik ingevuld bij het contract en geen vorige periode om op terug te vallen. Vul je verwachte jaarverbruik in bij het contract."
	}
	return fmt.Sprintf("Geen prognose: er is geen verwacht jaarverbruik ingevuld bij het contract, en de vorige periode (%s t/m %s) is voor %.0f%% gemeten; daarvoor is minstens %.0f%% nodig. Vul je verwachte jaarverbruik in bij het contract.",
		longDay(r.prevStart), longDay(r.prevEnd), r.coverage*100, periodMin)
}

func daysIn(y int, m time.Month) int { return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day() }

// standardProfile is the share of a year's heating per month (January
// first) in a Dutch home, from degree days: almost nothing May–September.
var standardProfile = [12]float64{0.18, 0.16, 0.13, 0.08, 0.03, 0.005, 0, 0, 0.015, 0.07, 0.14, 0.19}

// heatingProfile returns the share of a year's heating per month: from the
// building's average of the last twelve complete months when ista has them,
// else standardProfile.
func heatingProfile(months []store.IstaMonth) ([12]float64, string) {
	type ym struct{ y, m int }
	byMonth := map[ym]float64{}
	var latest ym
	for _, m := range months {
		if m.BuildingAvg == nil || m.Approx {
			continue
		}
		k := ym{m.Year, m.Month}
		byMonth[k] += *m.BuildingAvg
		if k.y*12+k.m > latest.y*12+latest.m {
			latest = k
		}
	}
	var shares [12]float64
	sum := 0.0
	k := latest
	for i := 0; i < 12; i++ {
		v, ok := byMonth[k]
		if !ok || v < 0 {
			return standardProfile, "standard"
		}
		shares[k.m-1] = v
		sum += v
		if k.m--; k.m == 0 {
			k.m, k.y = 12, k.y-1
		}
	}
	if sum <= 0 {
		return standardProfile, "standard"
	}
	for i := range shares {
		shares[i] /= sum
	}
	return shares, "building"
}

// margin returns the ± on the expected costs, and records the percentages
// it used: the uncertainty of the usage of the rest of the period (computed
// from the months so far against the previous period, or for an entered
// usage a fixed one) and, while the prices are taken over from the previous
// settlement, of the unit price. They are independent, so they are combined
// as the root of the sum of squares.
func (s *Service) margin(out *Costs, r rest, set forecast.Settings, periodMin float64) float64 {
	pct := []forecast.Used{forecast.Pick("period_min", set.PeriodMinPct, forecast.Defaults.PeriodMinPct, forecast.SourceDefault, 0)}
	usage := 0.0
	switch r.basis {
	case "last_year":
		auto, src := forecast.Computed(r.spreadPct, r.spreadOK, forecast.Defaults.HeatingPct)
		u := forecast.Pick("usage_heating", set.HeatingPct, auto, src, r.spreadMonths)
		pct, usage = append(pct, u), u.Pct/100
	case "estimate":
		u := forecast.Pick("entered_heating", set.HeatingEnteredPct, forecast.Defaults.HeatingEnteredPct, forecast.SourceDefault, 0)
		pct, usage = append(pct, u), u.Pct/100
	}
	price := 0.0
	if out.PriceSource == "previous" {
		u := forecast.Pick("price_heating", set.HeatingPricePct, forecast.Defaults.HeatingPricePct, forecast.SourceDefault, 0)
		pct, price = append(pct, u), u.Pct/100
	}
	out.Percentages = pct
	return math.Hypot(*out.UnitsRest*usage**out.UnitPrice, *out.UnitsExpected*price**out.UnitPrice)
}
