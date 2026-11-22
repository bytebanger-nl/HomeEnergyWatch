package costs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"

	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// AdvanceStatus compares the advance payments of the contract that runs
// today with its costs over the settlement period (the contract year), and
// forecasts the result of the final bill.
type AdvanceStatus struct {
	// Part: "" for all advance payments against all costs, or PartElectricity
	// / PartGas when the contract splits electricity and gas: that part's
	// advance against that part's costs.
	Part         string    `json:"part,omitempty"`
	ContractID   int64     `json:"contract_id"`
	ContractName string    `json:"contract_name"`
	From         time.Time `json:"from"` // settlement period [From, To)
	To           time.Time `json:"to"`
	Now          time.Time `json:"now"`
	MonthlyEUR   float64   `json:"monthly_eur"` // current monthly advance
	Contracts    int       `json:"contracts"`   // contracts in the chain (tariff changes + 1)

	// PaidEUR: the advance payments made up to now in this period. A payment
	// is made once a month on a fixed day (see payments) and counts in full
	// from that day on, never pro rata.
	PaidEUR     float64    `json:"paid_eur"`
	Payments    int        `json:"payments"`               // payments made in this period
	NextPayment *time.Time `json:"next_payment,omitempty"` // the next payment date in this period
	// CostEUR: costs up to now. Quarter hours without measurement are
	// estimated like the forecast does; EstimatedDays counts the days of the
	// period with such gaps.
	CostEUR       float64 `json:"cost_eur"`
	EstimatedDays int     `json:"estimated_days"`
	BalanceEUR    float64 `json:"balance_eur"` // paid − cost
	Missing       Missing `json:"missing"`     // what could not be priced

	Forecast      *Forecast `json:"forecast,omitempty"`
	ForecastError string    `json:"forecast_error,omitempty"`
}

// Forecast is the expected outcome at the end of the settlement period.
// ResultEUR > 0 means money back, < 0 an extra payment.
type Forecast struct {
	RemainingCostEUR float64 `json:"remaining_cost_eur"`
	RemainingPaidEUR float64 `json:"remaining_paid_eur"`
	TotalCostEUR     float64 `json:"total_cost_eur"`
	// TotalCostEUR split over the whole settlement period, every tariff
	// period of the chain: the fixed costs (monthly costs per day) and the
	// usage costs (measured so far plus the forecast; net of export credits)
	FixedCostEUR float64 `json:"fixed_cost_eur"`
	UsageCostEUR float64 `json:"usage_cost_eur"`
	TotalPaidEUR float64 `json:"total_paid_eur"`
	ResultEUR    float64 `json:"result_eur"`
	LowEUR       float64 `json:"low_eur"` // result in a worse case
	HighEUR      float64 `json:"high_eur"`
	// AdviceMonthlyEUR is the advance that ends the period at about zero,
	// spread over the payments still to come; nil when less than two weeks
	// or no payments are left.
	AdviceMonthlyEUR  *float64 `json:"advice_monthly_eur,omitempty"`
	RemainingPayments int      `json:"remaining_payments"`

	ImportKWh float64 `json:"import_kwh"` // expected for the rest of the period
	ExportKWh float64 `json:"export_kwh"`
	GasM3     float64 `json:"gas_m3"`
	WaterM3   float64 `json:"water_m3"`
	Method    Method  `json:"method"`
}

// Method tells how the rest of the period was estimated.
type Method struct {
	LastYearDays int `json:"last_year_days"` // days copied from the same day of the previous period
	ProfileDays  int `json:"profile_days"`   // days from a yearly usage spread with a seasonal profile
	// Sources per field (import_kwh, export_kwh, gas_m3; water_m3 on the
	// water timeline): "contract" (the yearly usage entered with the
	// contract), "previous" (the previous period), "none" (nothing measured,
	// so not needed) or "missing" (no forecast possible).
	Sources map[string]string `json:"sources,omitempty"`
	// Yearly usage per field: entered, or the previous period's (its
	// measured days scaled to the whole year by the profile).
	Yearly Estimate `json:"yearly"`
	// The previous period [PreviousFrom, PreviousTo): the 12 months before
	// the settlement period, and the share of its days that is measured.
	PreviousFrom     time.Time `json:"previous_from"`
	PreviousTo       time.Time `json:"previous_to"`
	PreviousCoverage float64   `json:"previous_coverage"`
	// PreviousYearly: the previous period's yearly usage, when it is
	// complete enough to count (also next to an entered yearly usage, to
	// compare).
	PreviousYearly *Estimate `json:"previous_yearly,omitempty"`
	// Percentages: every percentage the forecast used, with its source.
	Percentages []forecast.Used `json:"percentages,omitempty"`
	// Prices of dynamic contracts: "month_last_year", "last_30_days" or "mixed".
	Prices string `json:"prices,omitempty"`
}

// Sources of the usage of a field in the forecast.
const (
	SourceContract = "contract"
	SourcePrevious = "previous"
	SourceNone     = "none"
	SourceMissing  = "missing"
)

// settlementPeriod returns the contract year of the supplier contract of c
// (its chain of tariff changes) that contains now, or the last one when it
// has ended. Years count from the first start date of the chain and are
// capped by its end: the End of its last contract, or else the start of the
// next contract.
// SettlementPeriodAt returns the settlement period (contract year, over a
// chain of continuing contracts) that contains the local day of t: [from,
// to). ok is false when no contract applies on that day.
func (tl Timeline) SettlementPeriodAt(t time.Time, loc *time.Location) (from, to time.Time, ok bool) {
	t = t.In(loc)
	c := tl.At(t.Format(dateLayout))
	if c == nil {
		return time.Time{}, time.Time{}, false
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	from, to = settlementPeriod(tl, c, day, loc)
	return from, to, true
}

func settlementPeriod(tl Timeline, c *Contract, now time.Time, loc *time.Location) (from, to time.Time) {
	chain := tl.Chain(c)
	first, last := chain[0], chain[len(chain)-1]
	start, _ := time.ParseInLocation(dateLayout, first.Start, loc)
	var end time.Time // zero: open
	if last.End != "" {
		e, _ := time.ParseInLocation(dateLayout, last.End, loc)
		end = e.AddDate(0, 0, 1)
	} else {
		for _, o := range tl {
			if o.Start > last.Start {
				end, _ = time.ParseInLocation(dateLayout, o.Start, loc)
				break
			}
		}
	}
	from = start
	for {
		next := from.AddDate(1, 0, 0)
		if !next.After(now) && (end.IsZero() || next.Before(end)) {
			from = next
			continue
		}
		break
	}
	to = from.AddDate(1, 0, 0)
	if !end.IsZero() && end.Before(to) {
		to = end
	}
	return from, to
}

// payment is one advance payment: the amount paid on a local date.
type payment struct {
	Date time.Time
	EUR  float64
}

// payments returns the advance payments of one part ("" for all) of a
// chain of contracts on local dates in [from, to), ordered by date. Every
// advance amount is a payment on its "from" date, repeated in full every
// month on that day of the month (the 31st becomes the last day of a
// shorter month) until the next amount of the same part in the chain takes
// over, also across tariff changes. So 150.57 on 14 April followed by 126
// from 22 April pays 150.57 once and then 126 on every 22nd. A date on
// which no contract of the chain with advance payments for the part runs
// pays nothing.
func payments(tl, chain Timeline, from, to time.Time, loc *time.Location, part string) []payment {
	var out []payment
	for _, p := range partsOf(part) {
		out = append(out, partPayments(tl, chain, from, to, loc, p)...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date.Before(out[j].Date) })
	return out
}

// partPayments is payments for one series: PartElectricity (Advance) or
// PartGas (GasAdvance).
func partPayments(tl, chain Timeline, from, to time.Time, loc *time.Location, part string) []payment {
	var amounts []AdvanceAmount
	inChain := map[int64]bool{}
	for _, k := range chain {
		inChain[k.ID] = true
		if adv := k.advanceFor(part); adv != nil {
			amounts = append(amounts, adv.Amounts...)
		}
	}
	sort.SliceStable(amounts, func(i, j int) bool { return amounts[i].From < amounts[j].From })
	var out []payment
	for i, a := range amounts {
		first, err := time.ParseInLocation(dateLayout, a.From, loc)
		if err != nil || a.EUR == 0 {
			continue
		}
		until := to // exclusive: the next amount takes over on its own date
		if i+1 < len(amounts) {
			if next, err := time.ParseInLocation(dateLayout, amounts[i+1].From, loc); err == nil && next.Before(until) {
				until = next
			}
		}
		for n := 0; ; n++ {
			d := addMonths(first, n)
			if !d.Before(until) {
				break
			}
			if d.Before(from) {
				continue
			}
			if k := tl.At(d.Format(dateLayout)); k != nil && inChain[k.ID] && k.advanceFor(part) != nil {
				out = append(out, payment{Date: d, EUR: a.EUR})
			}
		}
	}
	return out
}

// addMonths returns local date d plus n months on the same day of the
// month, or the last day of a month that is shorter.
func addMonths(d time.Time, n int) time.Time {
	y, m := d.Year(), d.Month()+time.Month(n)
	last := time.Date(y, m+1, 0, 0, 0, 0, 0, d.Location()).Day()
	return time.Date(y, m, min(d.Day(), last), 0, 0, 0, 0, d.Location())
}

func sumPayments(ps []payment) float64 {
	sum := 0.0
	for _, p := range ps {
		sum += p.EUR
	}
	return sum
}

// Advance returns the advance status of the contract that runs now for a
// device (the P1 meter); nil when that contract has no advance payments.
// For a contract that splits electricity and gas it is the first of
// Advances: the electricity part, or the gas part when only gas has advance
// payments.
func (c *Calculator) Advance(ctx context.Context, deviceID string) (*AdvanceStatus, error) {
	all, err := c.Advances(ctx, deviceID)
	if err != nil || len(all) == 0 {
		return nil, err
	}
	return all[0], nil
}

// Advances returns the advance status of the contract that runs now for a
// device: one status for all advance payments against all costs, or, when
// the contract splits electricity and gas, one per part that has advance
// payments in the chain (electricity first). Empty without advance payments.
func (c *Calculator) Advances(ctx context.Context, deviceID string) ([]*AdvanceStatus, error) {
	loc := c.loc()
	now := c.now()
	tl, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, err
	}
	contract := tl.At(now.In(loc).Format(dateLayout))
	if contract == nil {
		return nil, nil
	}
	chain := tl.Chain(contract)
	hasAdvance := func(part string) bool {
		for _, k := range chain {
			for _, p := range partsOf(part) {
				if k.advanceFor(p) != nil {
					return true
				}
			}
		}
		return false
	}
	var parts []string
	if contract.Split {
		for _, p := range []string{PartElectricity, PartGas} {
			if hasAdvance(p) {
				parts = append(parts, p)
			}
		}
	} else if hasAdvance("") {
		parts = []string{""}
	}
	if len(parts) == 0 {
		return nil, nil
	}
	from, to := settlementPeriod(tl, contract, now, loc)
	upTo := minTime(now, to)
	// a payment counts as made from the start of its day (today's included);
	// the payments after today are still to come
	split := minTime(floor(upTo, BucketDay, loc).AddDate(0, 0, 1), to)
	out := make([]*AdvanceStatus, 0, len(parts))
	coming := map[string][]payment{}
	for _, part := range parts {
		paid := payments(tl, chain, from, split, loc, part)
		coming[part] = payments(tl, chain, split, to, loc, part)
		st := &AdvanceStatus{
			Part:       part,
			ContractID: contract.ID, ContractName: chain[0].Name, From: from, To: to, Now: now,
			Contracts:  len(chain),
			MonthlyEUR: contract.advanceOn(now.In(loc).Format(dateLayout), part),
			PaidEUR:    sumPayments(paid),
			Payments:   len(paid),
		}
		if len(coming[part]) > 0 {
			next := coming[part][0].Date
			st.NextPayment = &next
		}
		out = append(out, st)
	}
	sim, err := c.simulate(ctx, deviceID, tl, chain, from, to, upTo)
	if err != nil {
		return nil, err
	}
	var measuredOnly *Result // costs without estimates, for parts without forecast
	for _, st := range out {
		if miss := sim.missingFor(st.Part); len(miss) > 0 {
			// no estimate possible: measured costs only, without forecast
			if measuredOnly == nil {
				if measuredOnly, err = c.Calculate(ctx, deviceID, from, upTo, BucketYear); err != nil {
					return nil, err
				}
			}
			st.CostEUR, st.Missing = measuredOnly.Total.partEUR(st.Part), partMissing(measuredOnly.Total.Missing, st.Part)
			st.BalanceEUR = st.PaidEUR - st.CostEUR
			st.ForecastError = sim.noForecastText(miss, loc)
			round(st)
			continue
		}
		st.CostEUR, st.EstimatedDays, st.Missing = sim.past.partEUR(st.Part), sim.estimatedDays, partMissing(sim.past.Missing, st.Part)
		st.BalanceEUR = st.PaidEUR - st.CostEUR

		f := &Forecast{Method: sim.method}
		f.Method.Percentages = percentagesFor(sim.method.Percentages, st.Part)
		f.RemainingCostEUR = sim.future.partEUR(st.Part)
		f.ImportKWh, f.ExportKWh, f.GasM3, f.WaterM3 = sim.future.ImportKWh, sim.future.ExportKWh, sim.future.GasM3, sim.future.WaterM3
		switch st.Part { // the usage this part pays for
		case PartElectricity:
			f.GasM3 = 0
		case PartGas:
			f.ImportKWh, f.ExportKWh = 0, 0
		}
		left := coming[st.Part]
		f.RemainingPaidEUR = sumPayments(left)
		f.RemainingPayments = len(left)
		f.TotalCostEUR = st.CostEUR + f.RemainingCostEUR
		f.FixedCostEUR = sim.past.partFixedEUR(st.Part) + sim.future.partFixedEUR(st.Part)
		f.UsageCostEUR = sim.past.partVariableEUR(st.Part) + sim.future.partVariableEUR(st.Part)
		f.TotalPaidEUR = st.PaidEUR + f.RemainingPaidEUR
		f.ResultEUR = f.TotalPaidEUR - f.TotalCostEUR
		spread := sim.spread(st.Part)
		f.LowEUR, f.HighEUR = f.ResultEUR-spread, f.ResultEUR+spread
		if days := to.Sub(now).Hours() / 24; days >= 14 && len(left) > 0 {
			advice := math.Max(0, math.Round((f.TotalCostEUR-st.PaidEUR)/float64(len(left))))
			f.AdviceMonthlyEUR = &advice
		}
		if st.Part == PartGas {
			f.Method.Prices = "" // the source of expected power prices
		}
		st.Forecast = f
		round(st)
	}
	return out, nil
}

// partMissing leaves out the missing prices of the other part: the gas
// part has no power prices, the electricity part no gas prices.
func partMissing(m Missing, part string) Missing {
	switch part {
	case PartGas:
		m.Prices = 0
	case PartElectricity:
		m.GasPrices = 0
	}
	return m
}

func round(st *AdvanceStatus) {
	r := func(v *float64) { *v = math.Round(*v*100) / 100 }
	r(&st.PaidEUR)
	r(&st.CostEUR)
	r(&st.BalanceEUR)
	if f := st.Forecast; f != nil {
		for _, v := range []*float64{&f.RemainingCostEUR, &f.RemainingPaidEUR, &f.TotalCostEUR, &f.TotalPaidEUR,
			&f.ResultEUR, &f.LowEUR, &f.HighEUR, &f.FixedCostEUR, &f.UsageCostEUR} {
			r(v)
		}
		for _, v := range []*float64{&f.ImportKWh, &f.ExportKWh, &f.GasM3, &f.Method.Yearly.ImportKWh,
			&f.Method.Yearly.ExportKWh, &f.Method.Yearly.GasM3} {
			*v = math.Round(*v)
		}
		// water volumes are small: one decimal
		for _, v := range []*float64{&f.WaterM3, &f.Method.Yearly.WaterM3} {
			*v = math.Round(*v*10) / 10
		}
		if p := f.Method.PreviousYearly; p != nil {
			py := Estimate{ImportKWh: math.Round(p.ImportKWh), ExportKWh: math.Round(p.ExportKWh), GasM3: math.Round(p.GasM3), WaterM3: math.Round(p.WaterM3*10) / 10}
			f.Method.PreviousYearly = &py // a copy: the parts share the method
		}
	}
}

type simulation struct {
	past, future  Bucket // [from, now) and [now, to)
	estimatedDays int    // days of the period before now with estimated quarter hours
	method        Method
	water         bool
	periodMin     float64 // PeriodMinPct that applied

	// missing: the fields without a source (no entered yearly usage and no
	// complete previous period) that were measured, so are needed
	missing []string

	// uncertainty, as fractions: of the usage of each field (u), of the
	// market-price part of future quarter hours with an expected price
	// (pricePct; marketPower and marketGas are that part in euros incl.
	// VAT), and of estimated past days, which hold estPastShare of the past
	// energy
	u                      map[string]float64
	pricePct               float64
	marketPower, marketGas float64
	estPastShare           float64
}

// percentagesFor leaves out the percentages of the other part: the gas
// part has no electricity usage, the electricity part no gas.
func percentagesFor(list []forecast.Used, part string) []forecast.Used {
	other := map[string]bool{}
	switch part {
	case PartElectricity:
		other = map[string]bool{"usage_gas": true, "trend_gas": true}
	case PartGas:
		other = map[string]bool{"usage_electricity": true, "trend_import": true, "trend_export": true}
	}
	out := make([]forecast.Used, 0, len(list))
	for _, u := range list {
		if !other[u.Key] {
			out = append(out, u)
		}
	}
	return out
}

// missingFor returns the missing fields one part ("" for all) needs.
func (s *simulation) missingFor(part string) []string {
	var out []string
	for _, f := range s.missing {
		switch {
		case part == PartElectricity && f == "gas_m3", part == PartGas && f != "gas_m3":
			continue
		}
		out = append(out, f)
	}
	return out
}

var fieldNames = map[string]string{"import_kwh": "stroomafname", "export_kwh": "teruglevering", "gas_m3": "gas", "water_m3": "water"}

var monthNames = [...]string{"januari", "februari", "maart", "april", "mei", "juni", "juli", "augustus", "september", "oktober", "november", "december"}

func longDate(t time.Time, loc *time.Location) string {
	l := t.In(loc)
	return fmt.Sprintf("%d %s %d", l.Day(), monthNames[l.Month()-1], l.Year())
}

// noForecastText says why there is no forecast for the missing fields.
func (s *simulation) noForecastText(fields []string, loc *time.Location) string {
	names := make([]string, len(fields))
	for i, f := range fields {
		names[i] = fieldNames[f]
	}
	what := names[0]
	if len(names) > 1 {
		what = strings.Join(names[:len(names)-1], ", ") + " en " + names[len(names)-1]
	}
	m := s.method
	return fmt.Sprintf("Geen prognose voor %s: er is geen verwacht jaarverbruik ingevuld bij het contract, en de vorige periode (%s t/m %s) is voor %.0f%% gemeten; daarvoor is minstens %.0f%% nodig. Vul je verwachte jaarverbruik in bij het contract, of importeer je historie onder Instellingen.",
		what, longDate(m.PreviousFrom, loc), longDate(m.PreviousTo.AddDate(0, 0, -1), loc), m.PreviousCoverage*100, s.periodMin)
}

// spread returns the uncertainty of the result of one part ("" for all) in
// euros. The errors of the usage of each field, of market prices and of
// estimated past days are independent, so they are combined as the root of
// the sum of squares rather than added.
func (s *simulation) spread(part string) float64 {
	sq := 0.0
	add := func(v float64) { sq += v * v }
	if s.water {
		w := s.u["water_m3"]
		add((s.future.WaterEUR + s.future.WaterTaxEUR) * w)
		add((s.past.WaterEUR + s.past.WaterTaxEUR) * s.estPastShare * w)
		return math.Sqrt(sq)
	}
	uImp, uExp, uGas := s.u["import_kwh"], s.u["export_kwh"], s.u["gas_m3"]
	if part != PartGas {
		for _, b := range []*Bucket{&s.future, &s.past} {
			share := 1.0
			if b == &s.past {
				share = s.estPastShare
			}
			add((b.ElectricityEUR + b.ElectricityTaxEUR) * share * uImp)
			add((b.ExportEUR + b.NettingEUR) * share * uExp)
		}
		add(s.marketPower * s.pricePct)
	}
	if part != PartElectricity {
		add((s.future.GasEUR + s.future.GasTaxEUR) * uGas)
		add((s.past.GasEUR + s.past.GasTaxEUR) * s.estPastShare * uGas)
		add(s.marketGas * s.pricePct)
	}
	return math.Sqrt(sq)
}

// fields returns the usage fields of the calculator's timeline.
func (c *Calculator) fields() []string {
	if c.water() {
		return []string{"water_m3"}
	}
	return []string{"import_kwh", "export_kwh", "gas_m3"}
}

func fieldPtr(q *store.Quarter, f string) **float64 {
	switch f {
	case "import_kwh":
		return &q.ImportKWh
	case "export_kwh":
		return &q.ExportKWh
	case "gas_m3":
		return &q.GasM3
	}
	return &q.WaterM3
}

func fieldVal(q store.Quarter, f string) float64 { return val(*fieldPtr(&q, f)) }

func setField(q *store.Quarter, f string, v float64) { *fieldPtr(q, f) = &v }

func profileOf(f string) [12]float64 {
	switch f {
	case "import_kwh":
		return electricityProfile
	case "export_kwh":
		return solarProfile
	case "gas_m3":
		return gasProfile
	}
	return waterProfile
}

func (e Estimate) get(f string) float64 {
	switch f {
	case "import_kwh":
		return e.ImportKWh
	case "export_kwh":
		return e.ExportKWh
	case "gas_m3":
		return e.GasM3
	}
	return e.WaterM3
}

func (e *Estimate) set(f string, v float64) {
	switch f {
	case "import_kwh":
		e.ImportKWh = v
	case "export_kwh":
		e.ExportKWh = v
	case "gas_m3":
		e.GasM3 = v
	default:
		e.WaterM3 = v
	}
}

// entered returns the yearly usage entered per field: the value on the
// latest contract of the chain that has one (it is a yearly figure for the
// household, so contracts are never added up).
func entered(chain Timeline) Estimate {
	var out Estimate
	for _, k := range chain {
		if e := k.Estimate; e != nil {
			for _, f := range []string{"import_kwh", "export_kwh", "gas_m3", "water_m3"} {
				if v := e.get(f); v != 0 {
					out.set(f, v)
				}
			}
		}
	}
	return out
}

// usage keys of the forecast settings per kind, and the fields of a kind
var (
	usageKinds = []struct {
		kind   string
		fields []string
		min    float64 // usage of a month last year below which it is not compared
	}{
		{"electricity", []string{"import_kwh", "export_kwh"}, 5},
		{"gas", []string{"gas_m3"}, 1},
		{"water", []string{"water_m3"}, 0.5},
	}
	trendKeys = map[string]string{"import_kwh": "trend_import", "export_kwh": "trend_export", "gas_m3": "trend_gas", "water_m3": "trend_water"}
	// least usage of the compared days of the previous period for a trend
	trendMin = map[string]float64{"import_kwh": 10, "export_kwh": 10, "gas_m3": 5, "water_m3": 1}
)

const (
	trendMinDays     = 60 // compared days before a trend is applied
	trendMinGasDays  = 30 // of which in the heating season (October-April) for gas
	spreadMinDays    = 20 // days of a month before it is compared
	spreadMinMonths  = 2
	daysPerMonthMean = 30.44
)

// simulate calculates the costs of the settlement period [from, to) with
// the normal cost rules: measured quarter hours where there are (a day
// with a few missing quarter hours is filled in from the rest of it), else
// per field:
//
//  1. the yearly usage entered with the contract, spread with a seasonal
//     profile;
//  2. else the previous period (the 12 months before from), when at least
//     PeriodMinPct of its days is measured: the same day a year earlier,
//     corrected by the trend of this period so far, or for a day of it that
//     is not measured the previous period's yearly usage by the profile;
//  3. else nothing: the field is missing and the parts that need it get no
//     forecast.
//
// Net metering runs from 1 January, so the year is replayed from there; the
// same holds for the yearly cap of the water tax. Dynamic contracts use the
// stored market prices up to now and expected prices after it.
func (c *Calculator) simulate(ctx context.Context, deviceID string, tl Timeline, chain Timeline, from, to, now time.Time) (*simulation, error) {
	set, err := c.Forecast.Load(ctx)
	if err != nil {
		return nil, err
	}
	sim := &simulation{u: map[string]float64{}, water: c.water()}
	var dynamicPower, dynamicGas bool
	for _, k := range chain {
		dynamicPower = dynamicPower || k.Type == TypeDynamic
		dynamicGas = dynamicGas || k.gasType() == TypeDynamic
	}
	loc := c.loc()
	start := from
	if c.water() || from.In(loc).Year() < NettingEndsYear {
		start = floor(from, BucketYear, loc)
	}
	prevFrom := from.AddDate(-1, 0, 0)
	measured, err := c.Store.Quarters(ctx, deviceID, start, now)
	if err != nil {
		return nil, err
	}
	lastYear, err := c.Store.Quarters(ctx, deviceID, start.AddDate(-1, 0, 0), maxTime(to.AddDate(-1, 0, 0), from))
	if err != nil {
		return nil, err
	}
	group := func(qs []store.Quarter) map[string][]store.Quarter {
		m := map[string][]store.Quarter{}
		for _, q := range qs {
			k := q.Start.In(loc).Format(dateLayout)
			m[k] = append(m[k], q)
		}
		return m
	}
	measuredByDay, lastYearByDay := group(measured), group(lastYear)
	dayMin := forecast.Or(set.DayMinPct, forecast.Defaults.DayMinPct)
	sim.periodMin = forecast.Or(set.PeriodMinPct, forecast.Defaults.PeriodMinPct)
	usable := func(qs []store.Quarter, d time.Time) bool { return c.usableDay(qs, d, dayMin) }
	fields := c.fields()
	var pct []forecast.Used
	pct = append(pct, forecast.Pick("period_min", set.PeriodMinPct, forecast.Defaults.PeriodMinPct, forecast.SourceDefault, 0),
		forecast.Pick("day_min", set.DayMinPct, forecast.Defaults.DayMinPct, forecast.SourceDefault, 0))

	// the previous period: how much of it is measured, and its yearly usage
	var prevSum, prevShare Estimate
	days, measuredDays := 0, 0
	for d := prevFrom; d.Before(from); d = d.AddDate(0, 0, 1) {
		days++
		qs := lastYearByDay[d.Format(dateLayout)]
		if !usable(qs, d) {
			continue
		}
		measuredDays++
		for _, q := range c.fillDay(qs, d) {
			for _, f := range fields {
				prevSum.set(f, prevSum.get(f)+fieldVal(q, f))
			}
		}
		for _, f := range fields {
			prevShare.set(f, prevShare.get(f)+dayShare(profileOf(f), d))
		}
	}
	coverage := float64(measuredDays) / float64(max(days, 1))
	prevComplete := coverage*100 >= sim.periodMin-1e-9
	var prevYearly Estimate
	for _, f := range fields {
		if s := prevShare.get(f); s > 0 {
			prevYearly.set(f, prevSum.get(f)/s)
		}
	}
	sim.method.PreviousFrom, sim.method.PreviousTo = prevFrom, from
	sim.method.PreviousCoverage = math.Round(coverage*1000) / 1000
	if prevComplete {
		py := prevYearly
		sim.method.PreviousYearly = &py
	}

	// the source of every field
	seen := map[string]bool{} // usage measured at all
	for _, qs := range [][]store.Quarter{measured, lastYear} {
		for _, q := range qs {
			for _, f := range fields {
				if fieldVal(q, f) > 0 {
					seen[f] = true
				}
			}
		}
	}
	ent := entered(chain)
	src := map[string]string{}
	anyContract, anyPrevious := false, false
	for _, f := range fields {
		switch {
		case ent.get(f) > 0:
			src[f] = SourceContract
			anyContract = true
			sim.method.Yearly.set(f, ent.get(f))
		case prevComplete:
			src[f] = SourcePrevious
			anyPrevious = true
			sim.method.Yearly.set(f, prevYearly.get(f))
		case !seen[f] && f != c.usageKey():
			src[f] = SourceNone
		default:
			src[f] = SourceMissing
			sim.missing = append(sim.missing, f)
		}
	}
	sim.method.Sources = src

	// this period so far against the same days of the previous period: the
	// trend, and per month the spread around it
	type monthSums struct {
		cur, ly Estimate
		days    int
	}
	months := map[string]*monthSums{}
	var trendCur, trendLy Estimate
	trendDays, heatDays := 0, 0
	if anyPrevious {
		for d := from; d.Before(to) && !d.AddDate(0, 0, 1).After(now); d = d.AddDate(0, 0, 1) {
			lyd := d.AddDate(-1, 0, 0)
			cur, ly := measuredByDay[d.Format(dateLayout)], lastYearByDay[lyd.Format(dateLayout)]
			if !usable(cur, d) || !usable(ly, lyd) {
				continue
			}
			trendDays++
			if m := d.Month(); m >= time.October || m <= time.April {
				heatDays++
			}
			mk := d.Format("2006-01")
			ms := months[mk]
			if ms == nil {
				ms = &monthSums{}
				months[mk] = ms
			}
			ms.days++
			for _, q := range c.fillDay(cur, d) {
				for _, f := range fields {
					v := fieldVal(q, f)
					trendCur.set(f, trendCur.get(f)+v)
					ms.cur.set(f, ms.cur.get(f)+v)
				}
			}
			for _, q := range c.fillDay(ly, lyd) {
				for _, f := range fields {
					v := fieldVal(q, f)
					trendLy.set(f, trendLy.get(f)+v)
					ms.ly.set(f, ms.ly.get(f)+v)
				}
			}
		}
	}
	trendMonths := int(math.Round(float64(trendDays) / daysPerMonthMean))
	ratio := map[string]float64{}
	for _, f := range fields {
		ratio[f] = 1
		if src[f] != SourcePrevious || trendDays < trendMinDays || trendLy.get(f) < trendMin[f] ||
			(f == "gas_m3" && heatDays < trendMinGasDays) {
			continue
		}
		r := trendCur.get(f) / trendLy.get(f)
		change := math.Round((r-1)*1000) / 10
		u := forecast.Used{Key: trendKeys[f], Pct: change, Source: forecast.SourceComputed, Months: trendMonths}
		if set.TrendOn() {
			ratio[f] = r
		} else {
			u = forecast.Used{Key: trendKeys[f], Pct: 0, Source: forecast.SourceSetting, Auto: &change, AutoSource: forecast.SourceComputed, Months: trendMonths}
		}
		pct = append(pct, u)
	}

	// the uncertainty of usage per field
	for _, k := range usageKinds {
		previous := false
		for _, f := range k.fields {
			previous = previous || src[f] == SourcePrevious
		}
		if !previous {
			continue
		}
		// the expected ratio of the months: the trend applied to the kind
		var tc, tly float64
		for _, f := range k.fields {
			tc += trendLy.get(f) * ratio[f]
			tly += trendLy.get(f)
		}
		expect := 1.0
		if tly > 0 && tc > 0 {
			expect = tc / tly
		}
		var sw, sdev float64
		n := 0
		for _, ms := range months {
			var cur, ly float64
			for _, f := range k.fields {
				cur += ms.cur.get(f)
				ly += ms.ly.get(f)
			}
			if ms.days < spreadMinDays || ly < k.min {
				continue
			}
			dev := cur/(ly*expect) - 1
			sw += ly
			sdev += ly * dev * dev
			n++
		}
		var def float64
		var setting *float64
		switch k.kind {
		case "electricity":
			def, setting = forecast.Defaults.ElectricityPct, set.ElectricityPct
		case "gas":
			def, setting = forecast.Defaults.GasPct, set.GasPct
		default:
			def, setting = forecast.Defaults.WaterPct, set.WaterPct
		}
		v := 0.0
		if sw > 0 {
			v = 100 * math.Sqrt(sdev/sw)
		}
		auto, source := forecast.Computed(v, n >= spreadMinMonths && sw > 0, def)
		used := forecast.Pick("usage_"+k.kind, setting, auto, source, n)
		pct = append(pct, used)
		for _, f := range k.fields {
			if src[f] == SourcePrevious {
				sim.u[f] = used.Pct / 100
			}
		}
	}
	if anyContract {
		used := forecast.Pick("entered", set.EnteredPct, forecast.Defaults.EnteredPct, forecast.SourceDefault, 0)
		pct = append(pct, used)
		for _, f := range fields {
			if src[f] == SourceContract {
				sim.u[f] = used.Pct / 100
			}
		}
	}
	if dynamicPower || dynamicGas {
		used := forecast.Pick("price", set.PricePct, forecast.Defaults.PricePct, forecast.SourceDefault, 0)
		pct = append(pct, used)
		sim.pricePct = used.Pct / 100
	}
	sim.method.Percentages = pct

	bySlot := func(qs []store.Quarter) map[int64]store.Quarter {
		m := make(map[int64]store.Quarter, len(qs))
		for _, q := range qs {
			m[q.Start.Unix()] = q
		}
		return m
	}
	// estimateDay returns the estimated quarter hours of day d, and whether
	// the main field follows the same day of the previous period
	estimateDay := func(d time.Time) ([]store.Quarter, bool) {
		out := profileDay(d, Estimate{}) // the quarter hours of d
		var base, fromContract map[int64]store.Quarter
		fromLastYear := false
		if anyPrevious {
			lyd := d.AddDate(-1, 0, 0)
			if ly := lastYearByDay[lyd.Format(dateLayout)]; usable(ly, lyd) {
				base = bySlot(onDay(c.fillDay(ly, lyd), d, loc))
				fromLastYear = true
			} else {
				base = bySlot(profileDay(d, prevYearly))
			}
		}
		if anyContract {
			fromContract = bySlot(profileDay(d, ent))
		}
		for i := range out {
			q := &out[i]
			k := q.Start.Unix()
			for _, f := range fields {
				v := 0.0
				switch src[f] {
				case SourceContract:
					v = fieldVal(fromContract[k], f)
				case SourcePrevious:
					v = fieldVal(base[k], f) * ratio[f]
				}
				setField(q, f, v)
			}
		}
		return out, src[c.usageKey()] == SourcePrevious && fromLastYear
	}

	// the quarter hours of the period: measured, or estimated per slot
	var stream []store.Quarter
	var estPastEnergy, pastEnergy float64 // for the uncertainty of estimated past days
	settled := now.Add(-c.Settle)         // quarter hours ending after this may not be stored yet
	usableQ := func(q store.Quarter) bool { return c.has(q) && q.Quality != history.QualityAnomalous }
	// weight of a quarter hour for the uncertainty of estimated past days
	weight := func(q store.Quarter) float64 {
		if c.water() {
			return val(q.WaterM3)
		}
		return val(q.ImportKWh) + val(q.ExportKWh) + 10*val(q.GasM3)
	}
	for d := start; d.Before(to); d = d.AddDate(0, 0, 1) {
		dayEnd := d.AddDate(0, 0, 1)
		meas := measuredByDay[d.Format(dateLayout)]
		if !dayEnd.After(now) && usable(meas, d) {
			stream = append(stream, c.fillDay(meas, d)...)
			continue
		}
		est, fromLastYear := estimateDay(d)
		slots := map[int64]store.Quarter{}
		for _, q := range meas {
			if usableQ(q) {
				slots[q.Start.Unix()] = q
			}
		}
		gap := false
		for _, q := range est {
			if q.Start.Before(now) {
				if m, ok := slots[q.Start.Unix()]; ok {
					q = m
				} else {
					// a quarter hour the recorder cannot have stored yet is
					// estimated, but not a gap in the measurements
					if !q.Start.Add(15 * time.Minute).After(settled) {
						gap = true
					}
					if !d.Before(from) {
						estPastEnergy += weight(q)
					}
				}
			}
			stream = append(stream, q)
		}
		if gap && !d.Before(from) {
			sim.estimatedDays++
		}
		if dayEnd.After(now) && src[c.usageKey()] != SourceMissing { // (part of) the day is still to come
			if fromLastYear {
				sim.method.LastYearDays++
			} else {
				sim.method.ProfileDays++
			}
		}
	}

	// market prices: stored up to now, expected after it
	var power, gas []store.MarketPrice
	if dynamicPower || dynamicGas {
		// known prices, including today and tomorrow once published
		if power, err = c.Store.Prices(ctx, prices.Market, start, to); err != nil {
			return nil, err
		}
		if gas, err = c.Store.Prices(ctx, prices.GasMarket, start.AddDate(0, 0, -1), to); err != nil {
			return nil, err
		}
		var unknownPower, unknownGas []store.Quarter
		for _, q := range stream {
			if q.Start.Before(now) {
				continue
			}
			if _, ok := priceAt(power, q.Start); !ok && dynamicPower {
				unknownPower = append(unknownPower, q)
			}
			if _, ok := priceAt(gas, q.Start); !ok && dynamicGas && val(q.GasM3) != 0 {
				unknownGas = append(unknownGas, q)
			}
		}
		if len(unknownPower) > 0 {
			// without an electricity forecast (its usage is missing) the
			// lack of power prices is no error
			p, _, source, err := c.expectedPrices(ctx, unknownPower, now, loc, src["import_kwh"] != SourceMissing)
			if err != nil {
				return nil, err
			}
			power = append(power, p...)
			sim.method.Prices = source
		}
		if len(unknownGas) > 0 {
			// power prices are not needed for gas: a contract with fixed
			// electricity may have none
			_, g, _, err := c.expectedPrices(ctx, unknownGas, now, loc, false)
			if err != nil {
				return nil, err
			}
			gas = append(gas, g...)
		}
		sortPrices(power)
		sortPrices(gas)
		// the market-price part of the quarter hours with an expected price:
		// only that part is uncertain, not the markup or the energy tax
		mp, mg := 0.0, 0.0
		for _, q := range unknownPower {
			k := tl.At(q.Start.In(loc).Format(dateLayout))
			if k == nil || k.Type != TypeDynamic {
				continue
			}
			if p, ok := k.powerPrice(power, q.Start); ok {
				mp += p / 1000 * k.vat() * (val(q.ImportKWh) - val(q.ExportKWh))
			}
		}
		for _, q := range unknownGas {
			k := tl.At(q.Start.In(loc).Format(dateLayout))
			if k == nil || k.gasType() != TypeDynamic {
				continue
			}
			if p, ok := priceAt(gas, q.Start); ok {
				mg += p * k.vat() * val(q.GasM3)
			}
		}
		sim.marketPower, sim.marketGas = math.Abs(mp), math.Abs(mg)
	}

	// costs through the normal rules; net metering restarts every year
	var before Bucket // from 1 January up to from: only for net metering
	var cumImport, cumExport float64
	year := start.In(loc).Year()
	for _, q := range stream {
		if y := q.Start.In(loc).Year(); y != year {
			year, cumImport, cumExport = y, 0, 0
		}
		b := &sim.future
		switch {
		case q.Start.Before(from):
			b = &before
		case q.Start.Before(now):
			b = &sim.past
			pastEnergy += weight(q)
		}
		cumImport, cumExport = c.price(b, q, tl.At(q.Start.In(loc).Format(dateLayout)), power, gas, cumImport, cumExport, loc, 0)
	}
	for d := from; d.Before(to); d = d.AddDate(0, 0, 1) {
		k := tl.At(d.Format(dateLayout))
		if k == nil {
			continue
		}
		dayEnd := d.AddDate(0, 0, 1)
		perDay := k.monthlyEUR() * 12 / float64(daysInYear(d.Year()))
		gasPerDay := k.monthlyGasEUR() * 12 / float64(daysInYear(d.Year()))
		split := minTime(maxTime(now, d), dayEnd)
		pastShare := float64(split.Sub(d)) / float64(dayEnd.Sub(d))
		futureShare := float64(dayEnd.Sub(split)) / float64(dayEnd.Sub(d))
		sim.past.FixedEUR += perDay * pastShare
		sim.future.FixedEUR += perDay * futureShare
		sim.past.gasFixed += gasPerDay * pastShare
		sim.future.gasFixed += gasPerDay * futureShare
	}
	sim.past.finish()
	sim.future.finish()
	if pastEnergy > 0 { // estimated days in the past
		sim.estPastShare = estPastEnergy / pastEnergy
	}
	return sim, nil
}

// usableDay reports whether a day of measurements (local day d) is complete
// enough to count as measured, or to stand in for the same day a year
// later: at least minPct of its quarter hours.
func (c *Calculator) usableDay(qs []store.Quarter, d time.Time, minPct float64) bool {
	n := 0
	for _, q := range qs {
		if c.has(q) && q.Quality != history.QualityAnomalous {
			n++
		}
	}
	slots := float64(d.AddDate(0, 0, 1).Sub(d) / (15 * time.Minute))
	return n > 0 && float64(n) >= math.Round(slots*minPct/100)
}

// fillDay returns the quarter hours of a usable day d: the measured ones,
// and for each one that is missing (or anomalous) the day's average, export
// spread by daylight like the measured export of that day.
func (c *Calculator) fillDay(qs []store.Quarter, d time.Time) []store.Quarter {
	loc := d.Location()
	dayEnd := d.AddDate(0, 0, 1)
	all := []string{"import_kwh", "export_kwh", "gas_m3", "water_m3"}
	use := map[int64]store.Quarter{}
	var sum [4]float64
	var has [4]bool
	sun := 0.0
	for _, q := range qs {
		if !c.has(q) || q.Quality == history.QualityAnomalous {
			continue
		}
		if _, dup := use[q.Start.Unix()]; dup {
			continue
		}
		use[q.Start.Unix()] = q
		for i, f := range all {
			if p := *fieldPtr(&q, f); p != nil {
				sum[i] += *p
				has[i] = true
			}
		}
		sun += sunWeight(q.Start, loc)
	}
	n := float64(len(use))
	out := make([]store.Quarter, 0, int(dayEnd.Sub(d)/(15*time.Minute)))
	for t := d; t.Before(dayEnd); t = t.Add(15 * time.Minute) {
		if q, ok := use[t.Unix()]; ok {
			out = append(out, q)
			continue
		}
		q := store.Quarter{Start: t, Quality: history.QualityComplete}
		for i, f := range all {
			if !has[i] || n == 0 {
				continue
			}
			v := sum[i] / n
			if f == "export_kwh" {
				v = 0
				if sun > 0 {
					v = sum[i] * sunWeight(t, loc) / sun
				}
			}
			setField(&q, f, v)
		}
		out = append(out, q)
	}
	return out
}

// usageKey is the field of the yearly usage the forecast mainly rests on.
func (c *Calculator) usageKey() string {
	if c.water() {
		return "water_m3"
	}
	return "import_kwh"
}

// sunWeight is the share of daylight of the quarter hour at t: a sine from
// 07:00 to 19:00 local time, 0 outside it.
func sunWeight(t time.Time, loc *time.Location) float64 {
	l := t.In(loc)
	h := float64(l.Hour()) + float64(l.Minute())/60 + 0.125
	if h > 7 && h < 19 {
		return math.Sin(math.Pi * (h - 7) / 12)
	}
	return 0
}

// profileDay spreads the share of the yearly usage of day d over its
// quarter hours: import, gas and water evenly, export over daylight.
func profileDay(d time.Time, y Estimate) []store.Quarter {
	dayEnd := d.AddDate(0, 0, 1)
	imp := y.ImportKWh * dayShare(electricityProfile, d)
	exp := y.ExportKWh * dayShare(solarProfile, d)
	gm3 := y.GasM3 * dayShare(gasProfile, d)
	wm3 := y.WaterM3 * dayShare(waterProfile, d)
	var starts []time.Time
	var sun []float64
	sunSum := 0.0
	for t := d; t.Before(dayEnd); t = t.Add(15 * time.Minute) {
		starts = append(starts, t)
		w := sunWeight(t, d.Location())
		sun = append(sun, w)
		sunSum += w
	}
	n := float64(len(starts))
	out := make([]store.Quarter, 0, len(starts))
	for i, t := range starts {
		qi, qg, qw := imp/n, gm3/n, wm3/n
		qe := 0.0
		if sunSum > 0 {
			qe = exp * sun[i] / sunSum
		}
		out = append(out, store.Quarter{Start: t, ImportKWh: &qi, ExportKWh: &qe, GasM3: &qg, WaterM3: &qw, Quality: history.QualityComplete})
	}
	return out
}

// expectedPrices returns a market price for every estimated quarter hour:
// the average for that hour of the day in the same month last year when at
// least 20 days of it are known, else of the last 30 days. Without any
// power prices it fails when needPower is set.
func (c *Calculator) expectedPrices(ctx context.Context, est []store.Quarter, now time.Time, loc *time.Location, needPower bool) (power, gas []store.MarketPrice, source string, err error) {
	first, last := est[0].Start, est[len(est)-1].Start
	load := func(market string, from, to time.Time) []store.MarketPrice {
		if err != nil {
			return nil
		}
		var ps []store.MarketPrice
		ps, err = c.Store.Prices(ctx, market, from, to)
		return ps
	}
	histPower := load(prices.Market, first.AddDate(-1, -1, 0), last.AddDate(-1, 1, 0))
	histGas := load(prices.GasMarket, first.AddDate(-1, -1, 0), last.AddDate(-1, 1, 0))
	recentPower := load(prices.Market, now.AddDate(0, 0, -30), now)
	recentGas := load(prices.GasMarket, now.AddDate(0, 0, -30), now)
	if err != nil {
		return nil, nil, "", err
	}
	hourly := func(ps []store.MarketPrice) (avg [24]float64, days int, ok bool) {
		var sum, n [24]float64
		seen := map[string]bool{}
		for _, p := range ps {
			l := p.Start.In(loc)
			sum[l.Hour()] += p.Value
			n[l.Hour()]++
			seen[l.Format(dateLayout)] = true
		}
		for h := range avg {
			if n[h] == 0 {
				return avg, len(seen), false
			}
			avg[h] = sum[h] / n[h]
		}
		return avg, len(seen), true
	}
	mean := func(ps []store.MarketPrice) (float64, bool) {
		if len(ps) == 0 {
			return 0, false
		}
		s := 0.0
		for _, p := range ps {
			s += p.Value
		}
		return s / float64(len(ps)), true
	}
	recentHourly, _, recentOK := hourly(recentPower)
	recentGasAvg, recentGasOK := mean(recentGas)

	type monthPrices struct {
		hourly    [24]float64
		gas       float64
		ok, gasOK bool
	}
	months := map[string]*monthPrices{}
	month := func(t time.Time) *monthPrices {
		l := t.In(loc).AddDate(-1, 0, 0)
		key := l.Format("2006-01")
		if m, ok := months[key]; ok {
			return m
		}
		ms := time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
		me := ms.AddDate(0, 1, 0)
		within := func(ps []store.MarketPrice) []store.MarketPrice {
			var out []store.MarketPrice
			for _, p := range ps {
				if !p.Start.Before(ms) && p.Start.Before(me) {
					out = append(out, p)
				}
			}
			return out
		}
		m := &monthPrices{}
		var days int
		m.hourly, days, m.ok = hourly(within(histPower))
		m.ok = m.ok && days >= 20
		m.gas, m.gasOK = mean(within(histGas))
		months[key] = m
		return m
	}
	usedMonth, usedRecent := false, false
	for _, q := range est {
		m := month(q.Start)
		h := q.Start.In(loc).Hour()
		switch {
		case m.ok:
			power = append(power, store.MarketPrice{Start: q.Start, Duration: 15 * time.Minute, Value: m.hourly[h]})
			usedMonth = true
		case recentOK:
			power = append(power, store.MarketPrice{Start: q.Start, Duration: 15 * time.Minute, Value: recentHourly[h]})
			usedRecent = true
		}
		switch {
		case m.gasOK:
			gas = append(gas, store.MarketPrice{Start: q.Start, Duration: 15 * time.Minute, Value: m.gas})
		case recentGasOK:
			gas = append(gas, store.MarketPrice{Start: q.Start, Duration: 15 * time.Minute, Value: recentGasAvg})
		}
	}
	switch {
	case usedMonth && usedRecent:
		source = "mixed"
	case usedMonth:
		source = "month_last_year"
	case usedRecent:
		source = "last_30_days"
	case needPower:
		return nil, nil, "", errors.New("geen stroomprijzen bekend om de prognose mee te maken")
	}
	return power, gas, source, nil
}

func sortPrices(ps []store.MarketPrice) {
	sort.SliceStable(ps, func(i, j int) bool { return ps[i].Start.Before(ps[j].Start) })
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}

// onDay puts the quarter hours of a day last year on the quarter hours of
// day d, by local clock time. The two days can differ in length (the DST
// switch falls on another date): a clock time d does not have is left out,
// an hour d has twice reuses last year's hour, and an hour last year did
// not have (it sprang forward) takes the hour before it.
func onDay(qs []store.Quarter, d time.Time, loc *time.Location) []store.Quarter {
	byClock := map[string][]store.Quarter{}
	for _, q := range qs {
		k := q.Start.In(loc).Format("15:04")
		byClock[k] = append(byClock[k], q)
	}
	seen := map[string]int{}
	end := d.AddDate(0, 0, 1)
	out := make([]store.Quarter, 0, len(qs))
	for t := d; t.Before(end); t = t.Add(15 * time.Minute) {
		k := t.In(loc).Format("15:04")
		list := byClock[k]
		if len(list) == 0 {
			k = t.Add(-time.Hour).In(loc).Format("15:04")
			if list = byClock[k]; len(list) == 0 {
				continue
			}
		}
		q := list[min(seen[k], len(list)-1)]
		seen[k]++
		q.Start = t
		q.Quality = history.QualityComplete
		// last year's tariff (a holiday then) does not move along: d is
		// priced by the clock
		q.ImportLowKWh, q.ExportLowKWh = nil, nil
		out = append(out, q)
	}
	return out
}
