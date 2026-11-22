// Package costs keeps energy and water contracts and calculates what
// electricity, gas and water cost per day, month and year from the
// quarter-hour history and the stored market prices.
//
// All amounts a user enters are incl. VAT: tariffs in cents per kWh or m³,
// fixed costs in euros per month. Market prices are stored excl. VAT and get
// the contract's VAT rate on top. Monthly costs accrue per day as
// monthly × 12 / days in the year, from the first day of the contract.
package costs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Contract types.
const (
	TypeDynamic = "dynamic" // market price per quarter hour (or hour) / gas day + markup
	TypeFixed   = "fixed"   // fixed or variable tariff per kWh / m³
	TypeWater   = "water"   // drinking water: tariff per m³ (water timeline only)
)

// Parts of a contract that splits electricity and gas (Contract.Split): each
// has its own type and advance payments, and every fixed cost belongs to one.
const (
	PartElectricity = "electricity"
	PartGas         = "gas"
)

// WaterTaxCapM3 is the yearly volume per connection on which the tax on tap
// water (belasting op leidingwater) is levied; water above it is untaxed.
const WaterTaxCapM3 = 300

// Contract is the supplier contract during a period. Start and End are
// local calendar dates (YYYY-MM-DD); End is the last day and optional: a
// contract without End runs until the day before the next contract starts.
type Contract struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Start       string        `json:"start"`
	End         string        `json:"end,omitempty"`
	Type        string        `json:"type"`
	VATPct      float64       `json:"vat_pct"` // VAT on market prices, default 21
	Electricity Electricity   `json:"electricity"`
	Gas         Gas           `json:"gas"`
	Water       Water         `json:"water"`
	Monthly     []MonthlyCost `json:"monthly"`
	// ExportVAT: does the market price part of the feed-in compensation
	// (dynamic contracts) include VAT? nil: yes before 2027, no from 2027,
	// when private owners get the bare market price (small business scheme).
	ExportVAT *bool `json:"export_vat,omitempty"`
	// Split: electricity and gas are two contracts within this one, e.g. at
	// different suppliers. Type is then the electricity type and GasType the
	// gas type, Advance the electricity advance and GasAdvance the gas
	// advance, and each monthly cost has a Part. Both parts share the dates,
	// the chain (Continues), the settlement year, the VAT and the estimate.
	Split   bool   `json:"split,omitempty"`
	GasType string `json:"gas_type,omitempty"` // dynamic or fixed, only with Split
	// Advance: monthly advance payments to the supplier, settled per
	// contract year; nil when costs are billed afterwards (e.g. Tibber).
	// With Split: the electricity advance only.
	Advance    *Advance `json:"advance,omitempty"`
	GasAdvance *Advance `json:"gas_advance,omitempty"` // only with Split
	// Estimate: expected yearly usage for the advance forecast; zero fields
	// are derived from the measurements.
	Estimate *Estimate `json:"estimate,omitempty"`
	// Continues: same supplier as the contract right before it, only the
	// tariffs changed. A chain of such contracts is one supplier contract:
	// one settlement year from the first start date, with the advances of
	// whichever contract applies on a day.
	Continues bool `json:"continues,omitempty"`
}

// Advance is the schedule of monthly advance payments.
type Advance struct {
	Amounts []AdvanceAmount `json:"amounts"` // ordered by From
}

// AdvanceAmount is the monthly advance from a date (YYYY-MM-DD) on, in
// euros incl. VAT. A month that is only partly covered counts pro rata.
type AdvanceAmount struct {
	From string  `json:"from"`
	EUR  float64 `json:"eur"`
}

// Estimate is an expected yearly usage.
type Estimate struct {
	ImportKWh float64 `json:"import_kwh,omitempty"`
	ExportKWh float64 `json:"export_kwh,omitempty"`
	GasM3     float64 `json:"gas_m3,omitempty"`
	WaterM3   float64 `json:"water_m3,omitempty"`
}

// Electricity tariffs in cents per kWh incl. VAT.
type Electricity struct {
	MarkupCt float64 `json:"markup_ct"` // dynamic: purchase markup on the market price
	// Hourly (dynamic only): the supplier bills each clock hour at the
	// average of its quarter-hour market prices instead of per quarter hour.
	Hourly   bool    `json:"hourly,omitempty"`
	RateCt   float64 `json:"rate_ct"`   // fixed: single rate
	NormalCt float64 `json:"normal_ct"` // fixed: normal rate (dual tariff when low_ct is set)
	LowCt    float64 `json:"low_ct"`    // fixed: low rate, weekdays 23:00-07:00 and weekends
	TaxCt    float64 `json:"tax_ct"`    // energy tax (government levy)
	// ExportCt is added to every exported kWh: for a dynamic contract on top
	// of the market price (negative for feed-in costs), for a fixed contract
	// the feed-in compensation.
	ExportCt float64 `json:"export_ct"`
}

// Gas tariffs in cents per m³ incl. VAT.
type Gas struct {
	MarkupCt float64 `json:"markup_ct"` // dynamic: purchase markup on the gas day price
	RateCt   float64 `json:"rate_ct"`   // fixed: rate
	TaxCt    float64 `json:"tax_ct"`    // energy tax (government levy)
}

// Water tariffs in cents per m³ incl. VAT.
type Water struct {
	RateCt float64 `json:"rate_ct"` // supply rate
	// TaxCt: tax on tap water, levied on the first WaterTaxCapM3 of each
	// calendar year.
	TaxCt float64 `json:"tax_ct"`
}

// MonthlyCost is a fixed cost in euros per month incl. VAT, e.g. supply
// costs, grid costs, or (negative) the energy tax reduction.
type MonthlyCost struct {
	Name string  `json:"name"`
	EUR  float64 `json:"eur"`
	// Part: PartElectricity or PartGas on a contract with Split (empty
	// counts as electricity, like the energy tax reduction); unused otherwise.
	Part string `json:"part,omitempty"`
}

const dateLayout = "2006-01-02"

// vat returns the VAT factor for market prices.
func (c *Contract) vat() float64 { return 1 + c.VATPct/100 }

// exportVAT reports whether VAT is paid on the market price of exported
// electricity in the given year.
func (c *Contract) exportVAT(year int) bool {
	if c.ExportVAT != nil {
		return *c.ExportVAT
	}
	return year < NettingEndsYear
}

// gasType returns the contract type that applies to gas: GasType when the
// contract splits electricity and gas, else Type.
func (c *Contract) gasType() string {
	if c.Split {
		return c.GasType
	}
	return c.Type
}

// dual reports whether a fixed contract has normal and low rates.
func (c *Contract) dual() bool { return c.Electricity.LowCt != 0 || c.Electricity.NormalCt != 0 }

// fixedRateCt returns the fixed electricity rate at local time t by the
// clock (lowTariff); quarter() uses the meter's own tariff when it has it.
func (c *Contract) fixedRateCt(t time.Time) float64 {
	return c.fixedRateAt(clockLow(t))
}

// fixedRateAt returns the fixed electricity rate for energy of which the
// share low (0-1) was used at the low tariff.
func (c *Contract) fixedRateAt(low float64) float64 {
	if !c.dual() {
		return c.Electricity.RateCt
	}
	return low*c.Electricity.LowCt + (1-low)*c.Electricity.NormalCt
}

// lowShare returns which part (0-1) of a quarter hour's energy total was
// used at the low tariff: what the meter registered as low (part of total),
// or else by the clock at local time t.
func lowShare(part *float64, total float64, t time.Time) float64 {
	if part != nil && total > 0 {
		return min(max(*part/total, 0), 1)
	}
	return clockLow(t)
}

func clockLow(t time.Time) float64 {
	if lowTariff(t) {
		return 1
	}
	return 0
}

// lowTariff reports whether local time t is in the low-rate window by the
// clock: weekdays 23:00-07:00 and the whole weekend. A meter also counts
// public holidays as low, so its own tariff is preferred (lowShare).
func lowTariff(t time.Time) bool {
	switch t.Weekday() {
	case time.Saturday, time.Sunday:
		return true
	}
	return t.Hour() >= 23 || t.Hour() < 7
}

// monthlyEUR returns the sum of the monthly costs.
func (c *Contract) monthlyEUR() float64 {
	var sum float64
	for _, m := range c.Monthly {
		sum += m.EUR
	}
	return sum
}

// monthlyGasEUR returns the sum of the monthly costs that belong to gas;
// 0 unless the contract splits electricity and gas.
func (c *Contract) monthlyGasEUR() float64 {
	if !c.Split {
		return 0
	}
	var sum float64
	for _, m := range c.Monthly {
		if m.Part == PartGas {
			sum += m.EUR
		}
	}
	return sum
}

// advanceFor returns the advance payments of one part: the electricity part
// has Advance (on a contract without Split all payments), the gas part
// GasAdvance (only with Split).
func (c *Contract) advanceFor(part string) *Advance {
	if part == PartGas {
		if !c.Split {
			return nil
		}
		return c.GasAdvance
	}
	return c.Advance
}

// partsOf returns the parts whose advance payments make up a status: both
// for "" (all payments), else the part itself.
func partsOf(part string) []string {
	if part == "" {
		return []string{PartElectricity, PartGas}
	}
	return []string{part}
}

// normalizeAdvance drops empty rows, sorts by date and returns nil without
// amounts.
func normalizeAdvance(a *Advance) *Advance {
	if a == nil {
		return nil
	}
	var amounts []AdvanceAmount
	for _, x := range a.Amounts {
		x.From = strings.TrimSpace(x.From)
		if x.From == "" && x.EUR == 0 {
			continue
		}
		amounts = append(amounts, x)
	}
	if len(amounts) == 0 {
		return nil
	}
	sort.Slice(amounts, func(i, j int) bool { return amounts[i].From < amounts[j].From })
	return &Advance{Amounts: amounts}
}

// Normalize trims fields and fills defaults.
func (c *Contract) Normalize() {
	c.Name = strings.TrimSpace(c.Name)
	c.Start = strings.TrimSpace(c.Start)
	c.End = strings.TrimSpace(c.End)
	c.Type = strings.TrimSpace(strings.ToLower(c.Type))
	if c.VATPct == 0 {
		c.VATPct = 21
	}
	c.GasType = strings.TrimSpace(strings.ToLower(c.GasType))
	if !c.Split {
		c.GasType, c.GasAdvance = "", nil
	}
	if c.Type != TypeDynamic {
		c.Electricity.Hourly = false
	}
	var monthly []MonthlyCost
	for _, m := range c.Monthly {
		m.Name = strings.TrimSpace(m.Name)
		if m.Name == "" && m.EUR == 0 {
			continue
		}
		m.Part = strings.TrimSpace(strings.ToLower(m.Part))
		switch {
		case !c.Split:
			m.Part = ""
		case m.Part == "":
			m.Part = PartElectricity
		}
		monthly = append(monthly, m)
	}
	c.Monthly = monthly
	c.Advance = normalizeAdvance(c.Advance)
	c.GasAdvance = normalizeAdvance(c.GasAdvance)
	if c.Estimate != nil && *c.Estimate == (Estimate{}) {
		c.Estimate = nil
	}
}

// Validate checks one contract on its own.
func (c *Contract) Validate() error {
	if c.Name == "" {
		return errors.New("naam ontbreekt")
	}
	start, err := time.Parse(dateLayout, c.Start)
	if err != nil {
		return fmt.Errorf("ongeldige begindatum %q (JJJJ-MM-DD)", c.Start)
	}
	if c.End != "" {
		end, err := time.Parse(dateLayout, c.End)
		if err != nil {
			return fmt.Errorf("ongeldige einddatum %q (JJJJ-MM-DD)", c.End)
		}
		if end.Before(start) {
			return errors.New("einddatum ligt voor de begindatum")
		}
	}
	switch c.Type {
	case TypeDynamic, TypeFixed, TypeWater:
	default:
		return fmt.Errorf("onbekend contracttype %q (dynamic, fixed, water)", c.Type)
	}
	if c.Split {
		if c.Type == TypeWater {
			return errors.New("een watercontract kan niet in stroom en gas gesplitst worden")
		}
		switch c.GasType {
		case TypeDynamic, TypeFixed:
		default:
			return fmt.Errorf("onbekend contracttype voor gas %q (dynamic, fixed)", c.GasType)
		}
	}
	if c.Water.RateCt < 0 || c.Water.TaxCt < 0 {
		return errors.New("een watertarief kan niet negatief zijn")
	}
	if c.VATPct < 0 || c.VATPct > 100 {
		return errors.New("ongeldig btw-percentage")
	}
	if c.Type == TypeFixed && c.dual() && (c.Electricity.NormalCt == 0 || c.Electricity.LowCt == 0) {
		return errors.New("vul bij normaal/dal beide tarieven in")
	}
	for _, m := range c.Monthly {
		if m.Name == "" {
			return errors.New("geef elke vaste kostenpost een naam")
		}
		if m.Part != "" && m.Part != PartElectricity && m.Part != PartGas {
			return fmt.Errorf("vaste kosten %q horen bij stroom of gas, niet bij %q", m.Name, m.Part)
		}
	}
	label := "voorschot"
	if c.Split {
		label = "voorschot stroom"
	}
	if err := c.validateAdvance(c.Advance, label); err != nil {
		return err
	}
	if err := c.validateAdvance(c.GasAdvance, "voorschot gas"); err != nil {
		return err
	}
	if e := c.Estimate; e != nil && (e.ImportKWh < 0 || e.ExportKWh < 0 || e.GasM3 < 0 || e.WaterM3 < 0) {
		return errors.New("verwacht jaarverbruik kan niet negatief zijn")
	}
	return nil
}

// validateAdvance checks one series of advance payments; label names it in
// the error ("voorschot", "voorschot gas").
func (c *Contract) validateAdvance(adv *Advance, label string) error {
	if adv == nil {
		return nil
	}
	for i, a := range adv.Amounts {
		if _, err := time.Parse(dateLayout, a.From); err != nil {
			return fmt.Errorf("ongeldige ingangsdatum %s %q (JJJJ-MM-DD)", label, a.From)
		}
		if a.From < c.Start || (c.End != "" && a.From > c.End) {
			return fmt.Errorf("het %s vanaf %s valt buiten het contract", label, a.From)
		}
		if a.EUR < 0 {
			return fmt.Errorf("een %s kan niet negatief zijn", label)
		}
		if i > 0 && adv.Amounts[i-1].From == a.From {
			return fmt.Errorf("er staan twee keer een %s vanaf %s", label, a.From)
		}
	}
	return nil
}

// advanceOn returns the monthly advance of a part ("" for all payments) on
// a local date (YYYY-MM-DD); 0 before the first amount or without advance
// payments.
func (c *Contract) advanceOn(day, part string) float64 {
	sum := 0.0
	for _, p := range partsOf(part) {
		adv := c.advanceFor(p)
		if adv == nil {
			continue
		}
		eur := 0.0
		for _, a := range adv.Amounts {
			if a.From > day {
				break
			}
			eur = a.EUR
		}
		sum += eur
	}
	return sum
}

// Timeline is the set of contracts, ordered by start date, without overlap.
type Timeline []Contract

// ValidateTimeline checks that contracts do not overlap.
func ValidateTimeline(cs []Contract) error {
	sorted := append([]Contract(nil), cs...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Start < sorted[j].Start })
	if len(sorted) > 0 && sorted[0].Continues {
		return fmt.Errorf("%q is een vervolg op een eerder contract, maar er is geen eerder contract", sorted[0].Name)
	}
	for i := 1; i < len(sorted); i++ {
		prev, cur := sorted[i-1], sorted[i]
		if prev.Start == cur.Start {
			return fmt.Errorf("er is al een contract dat op %s begint", cur.Start)
		}
		if prev.End != "" && prev.End >= cur.Start {
			return fmt.Errorf("%q loopt tot en met %s en overlapt met %q dat op %s begint", prev.Name, prev.End, cur.Name, cur.Start)
		}
		if cur.Continues && prev.End != "" && nextDay(prev.End) != cur.Start {
			return fmt.Errorf("%q is een vervolg op %q en moet dan op %s beginnen, de dag na de einddatum", cur.Name, prev.Name, nextDay(prev.End))
		}
	}
	return nil
}

// nextDay returns the date after a YYYY-MM-DD date.
func nextDay(date string) string {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, 1).Format(dateLayout)
}

// Chain returns the contracts that form one supplier contract with c: the
// contracts before it that it continues, and those after it that continue
// it, ordered by start date.
func (tl Timeline) Chain(c *Contract) Timeline {
	i := sort.Search(len(tl), func(i int) bool { return tl[i].Start >= c.Start })
	if i == len(tl) || tl[i].Start != c.Start {
		return Timeline{*c}
	}
	first, last := i, i
	for first > 0 && tl[first].Continues {
		first--
	}
	for last+1 < len(tl) && tl[last+1].Continues {
		last++
	}
	return tl[first : last+1]
}

// At returns the contract that applies on the local calendar date day
// (YYYY-MM-DD), or nil.
func (tl Timeline) At(day string) *Contract {
	i := sort.Search(len(tl), func(i int) bool { return tl[i].Start > day }) - 1
	if i < 0 {
		return nil
	}
	if tl[i].End != "" && day > tl[i].End {
		return nil
	}
	return &tl[i]
}

// ElectricityType returns the type (dynamic or fixed) of the electricity
// part of the contract that applies at t, or "" without contract.
func (tl Timeline) ElectricityType(t time.Time, loc *time.Location) string {
	if c := tl.At(t.In(loc).Format(dateLayout)); c != nil {
		return c.Type
	}
	return ""
}

// HourlyAt reports whether the contract that applies at t bills
// electricity per hour at the average of the hour's market prices
// (Electricity.Hourly).
func (tl Timeline) HourlyAt(t time.Time, loc *time.Location) bool {
	c := tl.At(t.In(loc).Format(dateLayout))
	return c != nil && c.Type == TypeDynamic && c.Electricity.Hourly
}

// GasType returns the type (dynamic or fixed) of the gas part of the
// contract that applies at t, or "" without contract.
func (tl Timeline) GasType(t time.Time, loc *time.Location) string {
	if c := tl.At(t.In(loc).Format(dateLayout)); c != nil {
		return c.gasType()
	}
	return ""
}

// ElectricityPriceCt returns what one kWh imported at t costs under the
// contract of that day, in cents incl. VAT, markup and energy tax, given the
// market price in EUR/MWh. ok is false without contract.
func (tl Timeline) ElectricityPriceCt(t time.Time, loc *time.Location, marketEURMWh float64) (ct float64, ok bool) {
	lt := t.In(loc)
	c := tl.At(lt.Format(dateLayout))
	if c == nil {
		return 0, false
	}
	if c.Type == TypeFixed {
		return c.fixedRateCt(lt) + c.Electricity.TaxCt, true
	}
	return marketEURMWh/10*c.vat() + c.Electricity.MarkupCt + c.Electricity.TaxCt, true
}

// GasPriceEUR returns what one m³ of gas used at t costs under the contract
// of that day, in euros incl. VAT, markup and energy tax, given the gas day
// price in EUR/m³. ok is false without contract.
func (tl Timeline) GasPriceEUR(t time.Time, loc *time.Location, marketEURM3 float64) (eur float64, ok bool) {
	c := tl.At(t.In(loc).Format(dateLayout))
	if c == nil {
		return 0, false
	}
	if c.gasType() == TypeFixed {
		return (c.Gas.RateCt + c.Gas.TaxCt) / 100, true
	}
	return marketEURM3*c.vat() + (c.Gas.MarkupCt+c.Gas.TaxCt)/100, true
}

// Contracts reads, writes and validates one timeline of contracts in the
// store: energy (the default) or water.
type Contracts struct {
	Store *store.Store
	Table store.ContractTable // default store.EnergyContracts
	// OnChange is called with the first affected date (YYYY-MM-DD) after a
	// contract was saved or deleted.
	OnChange func(date string)
}

func (r *Contracts) table() store.ContractTable {
	if r.Table == "" {
		return store.EnergyContracts
	}
	return r.Table
}

// Water reports whether this is the water timeline.
func (r *Contracts) Water() bool { return r.table() == store.WaterContracts }

func (r *Contracts) changed(dates ...string) {
	if r.OnChange == nil {
		return
	}
	first := ""
	for _, d := range dates {
		if d != "" && (first == "" || d < first) {
			first = d
		}
	}
	r.OnChange(first)
}

// List returns all contracts, ordered by start date.
func (r *Contracts) List(ctx context.Context) (Timeline, error) {
	rows, err := r.Store.Contracts(ctx, r.table())
	if err != nil {
		return nil, err
	}
	out := make(Timeline, 0, len(rows))
	for _, row := range rows {
		var c Contract
		if err := json.Unmarshal(row.Data, &c); err != nil {
			return nil, fmt.Errorf("contract %d: %w", row.ID, err)
		}
		c.ID, c.Start = row.ID, row.Start
		out = append(out, c)
	}
	return out, nil
}

// ErrInvalid wraps validation errors, which are the user's to fix.
var ErrInvalid = errors.New("ongeldig contract")

// Save validates and stores a contract (ID 0 creates one) and returns it.
func (r *Contracts) Save(ctx context.Context, c Contract) (Contract, error) {
	c.Normalize()
	if r.Water() {
		c.Type = TypeWater // the only kind on this timeline
	}
	if err := c.Validate(); err != nil {
		return c, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if (c.Type == TypeWater) != r.Water() {
		return c, fmt.Errorf("%w: een watercontract hoort bij de waterkosten", ErrInvalid)
	}
	all, err := r.List(ctx)
	if err != nil {
		return c, err
	}
	others := make([]Contract, 0, len(all)+1)
	found := c.ID == 0
	oldStart := ""
	for _, o := range all {
		if o.ID == c.ID {
			found, oldStart = true, o.Start
			continue
		}
		others = append(others, o)
	}
	if !found {
		return c, store.ErrNotFound
	}
	if err := ValidateTimeline(append(others, c)); err != nil {
		return c, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	id, err := r.Store.SaveContract(ctx, r.table(), store.StoredContract{ID: c.ID, Start: c.Start, Data: data})
	if err != nil {
		return c, err
	}
	c.ID = id
	r.changed(oldStart, c.Start)
	return c, nil
}

// Delete removes a contract.
func (r *Contracts) Delete(ctx context.Context, id int64) error {
	all, err := r.List(ctx)
	if err != nil {
		return err
	}
	if err := r.Store.DeleteContract(ctx, r.table(), id); err != nil {
		return err
	}
	for _, c := range all {
		if c.ID == id {
			r.changed(c.Start)
		}
	}
	return nil
}
