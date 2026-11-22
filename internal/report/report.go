// Package report builds reports over a period (usage, costs, advance and
// whatever else is measured) and renders them as PDF. A report only reads:
// every figure comes from the same calculators and store queries as the
// app's own screens, so the report and the app always agree.
package report

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Section keys, in the order of the report.
const (
	SecSummary     = "summary"
	SecElectricity = "electricity"
	SecGas         = "gas"
	SecCosts       = "costs"
	SecWater       = "water"
	SecHeating     = "heating"
	SecDevices     = "devices"
	SecScenarios   = "scenarios"
	SecGrid        = "grid"
	SecAdvance     = "advance"
)

// SectionInfo names a section for the PWA.
type SectionInfo struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
}

// Sections lists every section in report order.
var Sections = []SectionInfo{
	{SecSummary, "Samenvatting", "Kerncijfers, vergelijking met de vorige periode en vorig jaar, volledigheid"},
	{SecElectricity, "Stroom", "Afname en teruglevering, dagprofiel, sluipverbruik en pieken"},
	{SecGas, "Gas", "Verbruik per dag of maand"},
	{SecCosts, "Kosten", "Kosten per dag of maand, opbouw, betaalde marktprijs bij een dynamisch contract"},
	{SecWater, "Water", "Verbruik, kosten, opbouw, nachtverbruik en waterbelasting"},
	{SecHeating, "Verwarming", "Verbruik en kosten volgens ista, opbouw en prijzen; gepland volgt dit apart als ista nog niet alles gemeten heeft"},
	{SecDevices, "Per apparaat", "Verbruik en kosten van stopcontacten, kWh-meters en de radiatoren van ista"},
	{SecScenarios, "Wat-als", "Opgeslagen scenario's over dezelfde periode"},
	{SecGrid, "Netkwaliteit", "Spanning per fase, dips, pieken en stroomuitval"},
	{SecAdvance, "Voorschot en prognose", "Stand, prognose en advies (nu) van wat je verder kiest: stroom, gas, water, verwarming; anders alles"},
}

// ValidSection reports whether key is a section.
func ValidSection(key string) bool {
	for _, s := range Sections {
		if s.Key == key {
			return true
		}
	}
	return false
}

// AllSections returns every section key.
func AllSections() []string {
	out := make([]string, len(Sections))
	for i, s := range Sections {
		out[i] = s.Key
	}
	return out
}

// Device is a meter the report can use.
type Device struct {
	ID   string
	Name string
	Kind homewizard.Kind
}

// Builder collects the data of a report.
type Builder struct {
	Store      *store.Store
	Costs      *costs.Calculator // energy (the P1 meter)
	WaterCosts *costs.Calculator // optional
	Scenarios  *costs.Scenarios  // optional
	Ista       *ista.Service     // optional: heating
	Devices    func() []Device
	Loc        *time.Location
	Now        func() time.Time
	Version    string
}

// Request asks for a report over [From, To): local midnights.
type Request struct {
	From, To time.Time
	Sections []string // empty: all
	Title    string   // optional; default "Energierapport"
	// DeferHeating leaves the heating usage out (also of the summary and
	// the devices) because ista has not measured the period yet: it is
	// sent later in a report of its own. The heating forecast stays in.
	DeferHeating bool
}

// MaxSpan is the longest period of one report.
const MaxSpan = 3*366*24*time.Hour + time.Hour

// ErrInvalid wraps request errors.
var ErrInvalid = errors.New("ongeldig rapport")

// Report is the collected data; nil sections had nothing to show or were
// not asked for.
type Report struct {
	Title     string
	From, To  time.Time // [From, To) local midnights
	Generated time.Time
	Version   string
	Loc       *time.Location
	Bucket    string // costs.BucketDay or costs.BucketMonth
	Meter     string // the P1 meter's name

	Summary     *Summary
	Electricity *Electricity
	Gas         *Gas
	Costs       *CostSection
	Water       *Water
	Heating     *Heating
	Devices     *Devices
	Scenarios   *Scenarios
	Grid        *Grid
	Advance     *Advance

	// HeatingDeferred: heating was asked for, but follows in a report of
	// its own once ista has measured the period.
	HeatingDeferred bool
	// HeatingFollowUp: this is the heating that followed such a report.
	HeatingFollowUp bool
}

// Days returns the number of calendar days in the period.
func (r *Report) Days() int { return daysBetween(r.From, r.To, r.Loc) }

// Summary holds the key figures with their comparisons.
type Summary struct {
	Energy, EnergyPrev, EnergyLastYear *costs.Bucket // nil without P1 meter / data
	Water, WaterPrev, WaterLastYear    *costs.Bucket
	PrevFrom, PrevTo                   time.Time
	LastYearFrom, LastYearTo           time.Time
	// Completeness of the P1 meter's quarter hours up to now
	Expected, Missing, Partial, Anomalous, Imported int
	PriceMissing, ContractDays                      int
	Gaps                                            []costs.Gap
	// Heating is ista's heating over the period (nil without ista).
	Heating *Heating
	// the contract(s) of electricity and gas, and of water, in the period
	EnergyContracts, WaterContracts []string
}

// Electricity is import and export in detail.
type Electricity struct {
	Buckets []costs.Bucket
	Total   costs.Bucket
	// average net power (import − export, kW) per quarter of the day
	Weekday, Weekend         [96]float64
	WeekdayDays, WeekendDays int
	BaseLoadW                float64 // median of the nightly minimum
	BaseLoadNights           int
	BaseLoadYearKWh          float64
	BaseLoadYearEUR          float64 // at the period's average import price; 0 without costs
	Peaks                    []Peak
}

// Peak is one quarter hour with high import.
type Peak struct {
	Start time.Time
	KW    float64 // average import power in the quarter hour
}

// Gas is gas usage per bucket.
type Gas struct {
	Buckets       []costs.Bucket
	TotalM3       float64
	LastYearM3    float64
	HasLastYear   bool
	HighestBucket int
}

// CostSection is the costs of the connection.
type CostSection struct {
	Buckets []costs.Bucket
	Total   costs.Bucket
	// Dynamic: the market price paid (weighted by import) against the plain
	// average of the period, both bare (EUR/MWh → ct/kWh excl. VAT)
	Dynamic           *Dynamic
	Cheapest, Dearest *costs.Bucket // days (bucket day only)
	ContractNames     []string
}

// Dynamic compares the market price paid with the average.
type Dynamic struct {
	WeightedCt float64
	AverageCt  float64
	ImportKWh  float64 // import with a known price
}

// Devices is usage per socket or kWh meter, and per ista radiator.
type Devices struct {
	List          []DeviceUsage
	ConnectionKWh float64 // the P1 meter's import
	OtherKWh      float64 // import not measured separately (≥ 0)
	Incomplete    bool
	// Heating: ista's addresses, whose rooms are the radiators
	Heating []HeatingAddress
}

// DeviceUsage is one device's import and its cost.
type DeviceUsage struct {
	Name      string
	Kind      homewizard.Kind
	ImportKWh float64
	CostEUR   float64
	Complete  bool
}

// Advance is the current state of the advance payments.
type Advance struct {
	Energy  []*costs.AdvanceStatus
	Water   []*costs.AdvanceStatus
	Heating []HeatingAdvance
}

// HeatingAdvance is where ista's running settlement period stands at an
// address.
type HeatingAdvance struct {
	Name  string
	Costs ista.Costs
}

// Scenarios compares the actual costs with each scenario.
type Scenarios struct {
	ActualEUR float64
	Complete  bool
	List      []ScenarioCost
}

// ScenarioCost is one scenario's total.
type ScenarioCost struct {
	Name     string
	TotalEUR float64
	Complete bool
}

// Water is water usage in detail.
type Water struct {
	Buckets []costs.Bucket
	Total   costs.Bucket
	Priced  bool // a water contract covers (part of) the period
	// Contracts names the water contracts of the period.
	Contracts []string
	// nights (00:00-05:00) with water use in every hour, out of Nights with
	// measurements: a sign of a leak when it happens night after night
	NightsNoZero, Nights int
	YearM3               float64 // used in the calendar year up to the end of the period
	CapM3                float64
}

// Grid is the voltage and grid events of the P1 meter.
type Grid struct {
	Phases      int
	VMin, VMax  [3]*float64
	VMinAt      [3]time.Time
	VMaxAt      [3]time.Time
	MinutesFrom time.Time // first minute with data (minute data is pruned)
	Events      []store.GridEvent
	EventsTotal int
	Counts      map[string]int // per kind
}

func (b *Builder) loc() *time.Location {
	if b.Loc == nil {
		return prices.Amsterdam
	}
	return b.Loc
}

func (b *Builder) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

// MonthPeriod returns the calendar month containing t: [first, first of next).
func MonthPeriod(t time.Time, loc *time.Location) (time.Time, time.Time) {
	t = t.In(loc)
	from := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	return from, from.AddDate(0, 1, 0)
}

// WeekPeriod returns the ISO week (Monday–Monday) containing t.
func WeekPeriod(t time.Time, loc *time.Location) (time.Time, time.Time) {
	t = t.In(loc)
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, loc)
	off := (int(day.Weekday()) + 6) % 7
	from := day.AddDate(0, 0, -off)
	return from, from.AddDate(0, 0, 7)
}

func daysBetween(from, to time.Time, loc *time.Location) int {
	n := 0
	for d := from.In(loc); d.Before(to); d = d.AddDate(0, 0, 1) {
		n++
	}
	return n
}

// Check validates a request and normalises its sections.
func (b *Builder) Check(req *Request) error {
	loc := b.loc()
	req.From, req.To = req.From.In(loc), req.To.In(loc)
	for _, t := range []time.Time{req.From, req.To} {
		if t.Hour() != 0 || t.Minute() != 0 || t.Second() != 0 || t.Nanosecond() != 0 {
			return fmt.Errorf("%w: periode moet op middernacht beginnen en eindigen", ErrInvalid)
		}
	}
	if !req.To.After(req.From) || req.To.Sub(req.From) > MaxSpan {
		return fmt.Errorf("%w: periode (1 dag tot 3 jaar)", ErrInvalid)
	}
	if req.From.After(b.now()) {
		return fmt.Errorf("%w: periode ligt in de toekomst", ErrInvalid)
	}
	if len(req.Sections) == 0 {
		req.Sections = AllSections()
	}
	var out []string
	for _, s := range req.Sections {
		s = strings.TrimSpace(s)
		if s == "" || slices.Contains(out, s) {
			continue
		}
		if !ValidSection(s) {
			return fmt.Errorf("%w: onbekend onderdeel %q", ErrInvalid, s)
		}
		out = append(out, s)
	}
	if len(out) == 0 {
		return fmt.Errorf("%w: geen onderdelen", ErrInvalid)
	}
	req.Sections = out
	req.Title = strings.TrimSpace(req.Title)
	if len([]rune(req.Title)) > 80 {
		return fmt.Errorf("%w: titel te lang", ErrInvalid)
	}
	return nil
}

func (b *Builder) device(kind homewizard.Kind) (Device, bool) {
	if b.Devices == nil {
		return Device{}, false
	}
	for _, d := range b.Devices() {
		if d.Kind == kind {
			return d, true
		}
	}
	return Device{}, false
}

// Build collects the report.
func (b *Builder) Build(ctx context.Context, req Request) (*Report, error) {
	if err := b.Check(&req); err != nil {
		return nil, err
	}
	loc := b.loc()
	r := &Report{
		Title: req.Title, From: req.From, To: req.To, Generated: b.now().In(loc),
		Version: b.Version, Loc: loc, Bucket: costs.BucketDay,
	}
	if r.Title == "" {
		r.Title = "Energierapport"
	}
	if r.Days() > 62 {
		r.Bucket = costs.BucketMonth
	}
	want := func(s string) bool { return slices.Contains(req.Sections, s) }

	p1, hasP1 := b.device(homewizard.KindP1)
	var energy, energyDay *costs.Result
	if hasP1 && b.Costs != nil {
		r.Meter = p1.Name
		var err error
		if energy, err = b.Costs.Calculate(ctx, p1.ID, r.From, r.To, r.Bucket); err != nil {
			return nil, fmt.Errorf("kosten: %w", err)
		}
		energyDay = energy
		if r.Bucket != costs.BucketDay && (want(SecCosts) || want(SecSummary)) {
			if energyDay, err = b.Costs.Calculate(ctx, p1.ID, r.From, r.To, costs.BucketDay); err != nil {
				return nil, fmt.Errorf("kosten: %w", err)
			}
		}
	}
	wm, hasWater := b.device(homewizard.KindWater)
	var water *costs.Result
	if hasWater && b.WaterCosts != nil {
		var err error
		if water, err = b.WaterCosts.Calculate(ctx, wm.ID, r.From, r.To, r.Bucket); err != nil {
			return nil, fmt.Errorf("waterkosten: %w", err)
		}
	}
	var quarters []store.Quarter
	if hasP1 && (want(SecElectricity) || want(SecSummary) || want(SecCosts)) {
		var err error
		if quarters, err = b.Store.Quarters(ctx, p1.ID, r.From, r.To); err != nil {
			return nil, err
		}
	}

	var heat *Heating
	r.HeatingDeferred = req.DeferHeating && want(SecHeating)
	if !req.DeferHeating && (want(SecHeating) || want(SecSummary) || want(SecDevices)) {
		var err error
		if heat, err = b.heating(ctx, r); err != nil {
			return nil, fmt.Errorf("%s: %w", SecHeating, err)
		}
	}

	for _, s := range req.Sections {
		var err error
		switch s {
		case SecSummary:
			r.Summary, err = b.summary(ctx, r, p1.ID, wm.ID, energy, water, quarters, heat)
		case SecElectricity:
			if energy != nil && (energy.Total.ImportKWh > 0 || energy.Total.ExportKWh > 0) {
				r.Electricity = b.electricity(r, energy, quarters)
			}
		case SecGas:
			if energy != nil {
				r.Gas, err = b.gas(ctx, r, p1.ID, energy)
			}
		case SecCosts:
			if energy != nil {
				r.Costs, err = b.costSection(ctx, r, energy, energyDay, quarters)
			}
		case SecDevices:
			r.Devices, err = b.devices(ctx, r, energy, heat)
		case SecAdvance:
			r.Advance, err = b.advance(ctx, advanceParts(req.Sections), p1.ID, hasP1, wm.ID, hasWater)
		case SecScenarios:
			if energy != nil && b.Scenarios != nil {
				r.Scenarios, err = b.scenarios(ctx, r, p1.ID, energy)
			}
		case SecWater:
			if water != nil {
				r.Water, err = b.water(ctx, r, wm.ID, water)
			}
		case SecHeating:
			r.Heating = heat
		case SecGrid:
			if hasP1 {
				r.Grid, err = b.grid(ctx, r, p1.ID)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s, err)
		}
	}
	return r, nil
}

// totalOf calculates the total of another period, nil when nothing was
// measured in it.
func totalOf(ctx context.Context, c *costs.Calculator, id string, from, to time.Time, water bool) (*costs.Bucket, error) {
	res, err := c.Calculate(ctx, id, from, to, costs.BucketMonth)
	if err != nil {
		return nil, err
	}
	t := res.Total
	if water && t.WaterM3 == 0 || !water && t.ImportKWh == 0 && t.ExportKWh == 0 && t.GasM3 == 0 {
		return nil, nil
	}
	return &t, nil
}

func (b *Builder) summary(ctx context.Context, r *Report, p1, wm string, energy, water *costs.Result, quarters []store.Quarter, heat *Heating) (*Summary, error) {
	s := &Summary{Heating: heat}
	days := r.Days()
	s.PrevFrom, s.PrevTo = r.From.AddDate(0, 0, -days), r.From
	if r.From.Day() == 1 && r.To.Day() == 1 { // whole months: the months before
		months := (r.To.Year()-r.From.Year())*12 + int(r.To.Month()-r.From.Month())
		s.PrevFrom = r.From.AddDate(0, -months, 0)
	}
	s.LastYearFrom, s.LastYearTo = r.From.AddDate(-1, 0, 0), r.To.AddDate(-1, 0, 0)
	var err error
	if energy != nil {
		t := energy.Total
		s.Energy = &t
		if s.EnergyPrev, err = totalOf(ctx, b.Costs, p1, s.PrevFrom, s.PrevTo, false); err != nil {
			return nil, err
		}
		if s.EnergyLastYear, err = totalOf(ctx, b.Costs, p1, s.LastYearFrom, s.LastYearTo, false); err != nil {
			return nil, err
		}
		end := r.To
		if now := b.now(); now.Before(end) {
			end = now
		}
		s.Expected = int(end.Sub(r.From) / (15 * time.Minute))
		for _, q := range quarters {
			if q.Source == store.SourceImport {
				s.Imported++
			}
		}
		g, err := b.Costs.Gaps(ctx, p1, r.From, r.To, false)
		if err != nil {
			return nil, err
		}
		s.Missing, s.Partial, s.Anomalous = g.Missing.Quarters, g.Missing.Partial, g.Missing.Anomalous
		s.PriceMissing = g.Missing.Prices + g.Missing.GasPrices
		s.ContractDays = g.Missing.ContractDays
		s.Gaps = g.Gaps
		if s.EnergyContracts, _, err = contractsIn(ctx, b.Costs, r); err != nil {
			return nil, err
		}
	}
	if water != nil {
		t := water.Total
		s.Water = &t
		if s.WaterPrev, err = totalOf(ctx, b.WaterCosts, wm, s.PrevFrom, s.PrevTo, true); err != nil {
			return nil, err
		}
		if s.WaterLastYear, err = totalOf(ctx, b.WaterCosts, wm, s.LastYearFrom, s.LastYearTo, true); err != nil {
			return nil, err
		}
		if s.WaterContracts, _, err = contractsIn(ctx, b.WaterCosts, r); err != nil {
			return nil, err
		}
	}
	if s.Energy == nil && s.Water == nil && s.Heating == nil {
		return nil, nil
	}
	return s, nil
}

func val(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func (b *Builder) electricity(r *Report, energy *costs.Result, quarters []store.Quarter) *Electricity {
	e := &Electricity{Buckets: energy.Buckets, Total: energy.Total}
	var wdSum, weSum, wdN, weN [96]float64
	wdDays, weDays := map[string]bool{}, map[string]bool{}
	nightMin := map[string]float64{}
	var peaks []Peak
	for _, q := range quarters {
		if q.ImportKWh == nil || q.Quality == "anomalous" {
			continue
		}
		lt := q.Start.In(r.Loc)
		slot := lt.Hour()*4 + lt.Minute()/15
		net := (val(q.ImportKWh) - val(q.ExportKWh)) * 4
		day := lt.Format("2006-01-02")
		if wd := lt.Weekday(); wd == time.Saturday || wd == time.Sunday {
			weSum[slot] += net
			weN[slot]++
			weDays[day] = true
		} else {
			wdSum[slot] += net
			wdN[slot]++
			wdDays[day] = true
		}
		if lt.Hour() < 5 && q.Quality == "complete" && val(q.ExportKWh) == 0 {
			w := val(q.ImportKWh) * 4000
			if m, ok := nightMin[day]; !ok || w < m {
				nightMin[day] = w
			}
		}
		peaks = append(peaks, Peak{Start: q.Start, KW: val(q.ImportKWh) * 4})
	}
	for i := range 96 {
		if wdN[i] > 0 {
			e.Weekday[i] = wdSum[i] / wdN[i]
		}
		if weN[i] > 0 {
			e.Weekend[i] = weSum[i] / weN[i]
		}
	}
	e.WeekdayDays, e.WeekendDays = len(wdDays), len(weDays)
	if len(nightMin) > 0 {
		mins := make([]float64, 0, len(nightMin))
		for _, v := range nightMin {
			mins = append(mins, v)
		}
		sort.Float64s(mins)
		mid := len(mins) / 2
		e.BaseLoadW = mins[mid]
		if len(mins)%2 == 0 {
			e.BaseLoadW = (mins[mid-1] + mins[mid]) / 2
		}
		e.BaseLoadNights = len(mins)
		e.BaseLoadYearKWh = e.BaseLoadW / 1000 * 24 * 365
		t := energy.Total
		if t.ImportKWh > 0 && t.ElectricityEUR+t.ElectricityTaxEUR > 0 && t.Missing.ContractDays == 0 {
			e.BaseLoadYearEUR = e.BaseLoadYearKWh * (t.ElectricityEUR + t.ElectricityTaxEUR) / t.ImportKWh
		}
	}
	sort.SliceStable(peaks, func(i, j int) bool { return peaks[i].KW > peaks[j].KW })
	if len(peaks) > 5 {
		peaks = peaks[:5]
	}
	for _, p := range peaks {
		if p.KW > 0 {
			e.Peaks = append(e.Peaks, p)
		}
	}
	return e
}

func (b *Builder) gas(ctx context.Context, r *Report, p1 string, energy *costs.Result) (*Gas, error) {
	g := &Gas{Buckets: energy.Buckets, TotalM3: energy.Total.GasM3, HighestBucket: -1}
	ly, err := totalOf(ctx, b.Costs, p1, r.From.AddDate(-1, 0, 0), r.To.AddDate(-1, 0, 0), false)
	if err != nil {
		return nil, err
	}
	if ly != nil && ly.GasM3 > 0 {
		g.LastYearM3, g.HasLastYear = ly.GasM3, true
	}
	if g.TotalM3 == 0 && !g.HasLastYear {
		return nil, nil
	}
	for i, bk := range g.Buckets {
		if g.HighestBucket < 0 || bk.GasM3 > g.Buckets[g.HighestBucket].GasM3 {
			g.HighestBucket = i
		}
	}
	return g, nil
}

func (b *Builder) costSection(ctx context.Context, r *Report, energy, days *costs.Result, quarters []store.Quarter) (*CostSection, error) {
	t := energy.Total
	if r.Days() > 0 && t.Missing.ContractDays >= r.Days() {
		return nil, nil // no contract at all: nothing to price
	}
	c := &CostSection{Buckets: energy.Buckets, Total: t}
	names, dynamic, err := contractsIn(ctx, b.Costs, r)
	if err != nil {
		return nil, err
	}
	c.ContractNames = names
	if dynamic {
		ps, err := b.Store.Prices(ctx, prices.Market, r.From, r.To)
		if err != nil {
			return nil, err
		}
		c.Dynamic = weighted(ps, quarters)
	}
	if days != nil {
		now := b.now()
		for i := range days.Buckets {
			d := &days.Buckets[i]
			if !d.Complete || d.ImportKWh == 0 || d.Start.AddDate(0, 0, 1).After(now) {
				continue
			}
			if c.Cheapest == nil || d.TotalEUR < c.Cheapest.TotalEUR {
				c.Cheapest = d
			}
			if c.Dearest == nil || d.TotalEUR > c.Dearest.TotalEUR {
				c.Dearest = d
			}
		}
		if c.Cheapest == c.Dearest {
			c.Cheapest, c.Dearest = nil, nil
		}
	}
	return c, nil
}

// contractsIn names the calculator's contracts that cover part of the
// report's period, and whether one of them is dynamic.
func contractsIn(ctx context.Context, c *costs.Calculator, r *Report) ([]string, bool, error) {
	if c == nil || c.Contracts == nil {
		return nil, false, nil
	}
	tl, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, false, err
	}
	var names []string
	dynamic := false
	for _, k := range tl {
		start, _ := time.ParseInLocation("2006-01-02", k.Start, r.Loc)
		end := r.To
		if k.End != "" {
			if e, err := time.ParseInLocation("2006-01-02", k.End, r.Loc); err == nil {
				end = e.AddDate(0, 0, 1)
			}
		}
		if start.Before(r.To) && end.After(r.From) {
			names = append(names, k.Name)
			if k.Type == costs.TypeDynamic {
				dynamic = true
			}
		}
	}
	return names, dynamic, nil
}

// weighted returns the market price weighted by import against the plain
// average over the same quarter hours with a price (ct/kWh excl. VAT).
func weighted(ps []store.MarketPrice, quarters []store.Quarter) *Dynamic {
	if len(ps) == 0 {
		return nil
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Start.Before(ps[j].Start) })
	var sumW, sumKWh, sumP, n float64
	i := 0
	for _, q := range quarters {
		for i < len(ps) && !q.Start.Before(ps[i].Start.Add(ps[i].Duration)) {
			i++
		}
		if i >= len(ps) || q.Start.Before(ps[i].Start) {
			continue
		}
		p := ps[i].Value / 10 // EUR/MWh -> ct/kWh
		sumP += p
		n++
		if kwh := val(q.ImportKWh); kwh > 0 && q.Quality != "anomalous" {
			sumW += p * kwh
			sumKWh += kwh
		}
	}
	if n == 0 || sumKWh == 0 {
		return nil
	}
	return &Dynamic{WeightedCt: sumW / sumKWh, AverageCt: sumP / n, ImportKWh: sumKWh}
}

func (b *Builder) devices(ctx context.Context, r *Report, energy *costs.Result, heat *Heating) (*Devices, error) {
	d := &Devices{}
	if heat != nil {
		for _, a := range heat.Addresses {
			if len(a.Rooms) > 0 && a.Usage.Total.Total != nil && *a.Usage.Total.Total > 0 {
				d.Heating = append(d.Heating, a)
			}
		}
	}
	if energy == nil || b.Devices == nil {
		if len(d.Heating) == 0 {
			return nil, nil
		}
		return d, nil
	}
	connection := energy.Total.ImportKWh
	sum := 0.0
	for _, dev := range b.Devices() {
		if dev.Kind != homewizard.KindSocket && dev.Kind != homewizard.KindKWH {
			continue
		}
		res, err := b.Costs.Usage(ctx, dev.ID, r.From, r.To, costs.BucketMonth)
		if err != nil {
			return nil, err
		}
		u := DeviceUsage{Name: dev.Name, Kind: dev.Kind, ImportKWh: res.Total.ImportKWh, CostEUR: res.Total.TotalEUR, Complete: res.Total.Complete}
		sum += u.ImportKWh
		if u.ImportKWh < 0.005 {
			continue // e.g. a kWh meter on solar panels: nothing used
		}
		if !u.Complete {
			d.Incomplete = true
		}
		d.List = append(d.List, u)
	}
	if len(d.List) == 0 {
		if len(d.Heating) == 0 {
			return nil, nil
		}
		return d, nil // no devices: no electricity part
	}
	d.ConnectionKWh = connection
	sort.SliceStable(d.List, func(i, j int) bool { return d.List[i].ImportKWh > d.List[j].ImportKWh })
	d.OtherKWh = math.Max(0, d.ConnectionKWh-sum)
	return d, nil
}

// forecastParts says which forecasts the advance section shows.
type forecastParts struct {
	electricity, gas, water, heating bool
}

// advanceParts follows the other sections of the report: stroom and gas
// with electricity, gas, costs, the summary or scenarios (only gas: the
// gas contracts), water with water, heating with heating, and the devices
// with electricity and heating (sockets and radiators). When no other
// section says what (none, or only the grid) it shows everything.
func advanceParts(sections []string) forecastParts {
	var p forecastParts
	for _, s := range sections {
		switch s {
		case SecElectricity:
			p.electricity = true
		case SecGas:
			p.gas = true
		case SecCosts, SecSummary, SecScenarios:
			p.electricity, p.gas = true, true
		case SecDevices:
			p.electricity, p.heating = true, true
		case SecWater:
			p.water = true
		case SecHeating:
			p.heating = true
		}
	}
	if p == (forecastParts{}) {
		return forecastParts{true, true, true, true}
	}
	return p
}

// energy reports whether an energy contract of part is shown; a combined
// contract ("") with either.
func (p forecastParts) energy(part string) bool {
	switch part {
	case costs.PartElectricity:
		return p.electricity
	case costs.PartGas:
		return p.gas
	}
	return p.electricity || p.gas
}

func (b *Builder) advance(ctx context.Context, parts forecastParts, p1 string, hasP1 bool, wm string, hasWater bool) (*Advance, error) {
	a := &Advance{}
	if hasP1 && b.Costs != nil && (parts.electricity || parts.gas) {
		list, err := b.Costs.Advances(ctx, p1)
		if err != nil {
			return nil, err
		}
		for _, st := range list {
			if parts.energy(st.Part) {
				a.Energy = append(a.Energy, st)
			}
		}
	}
	var err error
	if hasWater && b.WaterCosts != nil && parts.water {
		if a.Water, err = b.WaterCosts.Advances(ctx, wm); err != nil {
			return nil, err
		}
	}
	if b.Ista != nil && parts.heating {
		addrs, err := b.Ista.Addresses(ctx)
		if err != nil {
			return nil, err
		}
		for _, ad := range addrs {
			c, err := b.Ista.Costs(ctx, ad.Cuid, 0)
			if errors.Is(err, ista.ErrNoAddress) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if c.CostExpected == nil && c.CostSoFar == nil && (c.Contract == nil || len(c.Contract.Advance) == 0) {
				continue // no prices and no advance: nothing to show
			}
			name := ad.Address
			if name == "" {
				name = "Verwarming"
			}
			a.Heating = append(a.Heating, HeatingAdvance{Name: name, Costs: c})
		}
	}
	if len(a.Energy) == 0 && len(a.Water) == 0 && len(a.Heating) == 0 {
		return nil, nil
	}
	return a, nil
}

func (b *Builder) scenarios(ctx context.Context, r *Report, p1 string, energy *costs.Result) (*Scenarios, error) {
	list, err := b.Scenarios.List(ctx)
	if err != nil || len(list) == 0 {
		return nil, err
	}
	if r.Days() > 0 && energy.Total.Missing.ContractDays >= r.Days() {
		return nil, nil
	}
	cmp, err := b.Costs.Compare(ctx, p1, list, r.From, r.To, costs.BucketMonth)
	if err != nil {
		return nil, err
	}
	s := &Scenarios{ActualEUR: cmp.Actual.Total.TotalEUR, Complete: cmp.Actual.Total.Complete}
	for _, sc := range cmp.Scenarios {
		s.List = append(s.List, ScenarioCost{Name: sc.Scenario.Name, TotalEUR: sc.Result.Total.TotalEUR, Complete: sc.Result.Total.Complete})
	}
	sort.SliceStable(s.List, func(i, j int) bool { return s.List[i].TotalEUR < s.List[j].TotalEUR })
	return s, nil
}

func (b *Builder) water(ctx context.Context, r *Report, wm string, water *costs.Result) (*Water, error) {
	w := &Water{Buckets: water.Buckets, Total: water.Total, CapM3: costs.WaterTaxCapM3}
	w.Priced = r.Days() == 0 || water.Total.Missing.ContractDays < r.Days()
	var err error
	if w.Contracts, _, err = contractsIn(ctx, b.WaterCosts, r); err != nil {
		return nil, err
	}
	qs, err := b.Store.Quarters(ctx, wm, r.From, r.To)
	if err != nil {
		return nil, err
	}
	// per night: the hours 0-4 with water use; a night counts when all five
	// hours were measured
	type night struct{ hours, used [5]bool }
	nights := map[string]*night{}
	for _, q := range qs {
		lt := q.Start.In(r.Loc)
		if lt.Hour() >= 5 || q.WaterM3 == nil || q.Quality == "anomalous" {
			continue
		}
		key := lt.Format("2006-01-02")
		n := nights[key]
		if n == nil {
			n = &night{}
			nights[key] = n
		}
		n.hours[lt.Hour()] = true
		if *q.WaterM3 > 0 {
			n.used[lt.Hour()] = true
		}
	}
	for _, n := range nights {
		if n.hours != [5]bool{true, true, true, true, true} {
			continue
		}
		w.Nights++
		if n.used == [5]bool{true, true, true, true, true} {
			w.NightsNoZero++
		}
	}
	ys := time.Date(r.To.AddDate(0, 0, -1).Year(), 1, 1, 0, 0, 0, 0, r.Loc)
	if yr, err := totalOf(ctx, b.WaterCosts, wm, ys, r.To, true); err != nil {
		return nil, err
	} else if yr != nil {
		w.YearM3 = yr.WaterM3
	}
	if w.Total.WaterM3 == 0 && w.Nights == 0 {
		return nil, nil
	}
	return w, nil
}

func (b *Builder) grid(ctx context.Context, r *Report, p1 string) (*Grid, error) {
	g := &Grid{Counts: map[string]int{}}
	mins, err := b.Store.GridMinutes(ctx, p1, r.From, r.To)
	if err != nil {
		return nil, err
	}
	for _, m := range mins {
		if g.MinutesFrom.IsZero() || m.Minute.Before(g.MinutesFrom) {
			g.MinutesFrom = m.Minute
		}
		for i, p := range m.Phases {
			if p.VMin == nil && p.VMax == nil {
				continue
			}
			g.Phases = max(g.Phases, i+1)
			if p.VMin != nil && (g.VMin[i] == nil || *p.VMin < *g.VMin[i]) {
				v := *p.VMin
				g.VMin[i], g.VMinAt[i] = &v, m.Minute
			}
			if p.VMax != nil && (g.VMax[i] == nil || *p.VMax > *g.VMax[i]) {
				v := *p.VMax
				g.VMax[i], g.VMaxAt[i] = &v, m.Minute
			}
		}
	}
	ev, err := b.Store.GridEvents(ctx, p1, r.From, r.To)
	if err != nil {
		return nil, err
	}
	g.EventsTotal = len(ev)
	for _, e := range ev {
		g.Counts[e.Kind] += max(e.Count, 1)
	}
	sort.SliceStable(ev, func(i, j int) bool { return ev[i].At.After(ev[j].At) })
	if len(ev) > 25 {
		ev = ev[:25]
	}
	g.Events = ev
	if g.Phases == 0 && g.EventsTotal == 0 {
		return nil, nil
	}
	return g, nil
}
