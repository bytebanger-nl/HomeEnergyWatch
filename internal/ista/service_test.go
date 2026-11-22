package ista

import (
	"context"
	"math"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

type fixture struct {
	sim *Sim
	svc *Service
	st  *store.Store
	now *time.Time
}

func newFixture(t *testing.T, moveIn string) *fixture {
	t.Helper()
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	f := &fixture{now: &now}
	f.sim = &Sim{Username: "bewoner@example.org", Password: "geheim", MoveIn: day(moveIn),
		Now: func() time.Time { return *f.now }, Lag: 0}
	srv := httptest.NewServer(f.sim.Handler())
	t.Cleanup(srv.Close)
	st, err := store.Open(filepath.Join(t.TempDir(), "hwlocal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f.st = st
	f.svc = &Service{Store: st, Loc: time.UTC, BaseURL: srv.URL, Pace: -1, Now: func() time.Time { return *f.now }}
	return f
}

func (f *fixture) addAccount(t *testing.T, pass string) {
	t.Helper()
	if err := f.st.SavePlugin(context.Background(), store.PluginInstance{ID: "acc1", Type: PluginType, Name: "ista",
		Config: map[string]string{"username": f.sim.Username, "password": pass}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
}

func day(s string) time.Time { d, _ := time.Parse(DayLayout, s); return d }

func TestSyncBackfillAndUsage(t *testing.T) {
	f := newFixture(t, "2024-11-11")
	ctx := context.Background()
	f.addAccount(t, f.sim.Password)
	f.svc.tick(ctx)
	st := f.svc.Statuses(ctx)
	if len(st) != 1 || st[0].State != "idle" || st[0].LastSync == nil {
		t.Fatalf("status %+v", st)
	}
	days, _ := f.st.IstaDays(ctx, SimCuid, "2000-01-01", "2100-01-01")
	if want := int(day("2026-11-11").Sub(day("2024-11-11")).Hours()/24) + 1; len(days) != want {
		t.Fatalf("%d days stored, want %d", len(days), want)
	}
	calls := f.sim.Calls["/api/Values/ConsumptionValues"]

	// the daily usage adds up to the readings at the end of each period
	addrs, err := f.svc.Addresses(ctx)
	if err != nil || len(addrs) != 1 || len(addrs[0].Rooms) != 4 || len(addrs[0].Periods) != 3 {
		t.Fatalf("addresses %+v %v", addrs, err)
	}
	u, err := f.svc.Usage(ctx, SimCuid, day("2025-07-01"), day("2026-06-30"), BucketMonth)
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Buckets) != 12 || u.Total.Missing != 0 {
		t.Fatalf("buckets %d, missing %d", len(u.Buckets), u.Total.Missing)
	}
	end := days[0]
	for _, d := range days {
		if d.Day == "2026-06-30" {
			end = d
		}
	}
	if *u.Total.Total != end.TotalNow {
		t.Fatalf("usage total %v, reading at period end %v", *u.Total.Total, end.TotalNow)
	}
	sumRooms := 0.0
	for _, v := range u.Total.Units {
		sumRooms += *v
	}
	if sumRooms != *u.Total.Total {
		t.Fatalf("rooms %v != total %v", sumRooms, *u.Total.Total)
	}
	if u.Total.LastYear == nil || u.Total.Building == nil {
		t.Fatalf("no comparison: %+v", u.Total)
	}

	// weeks run Monday to Sunday and are cut at the period's start
	w, err := f.svc.Weeks(ctx, "", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if w.Rows[0].Start != "2025-07-01" || w.Rows[0].End != "2025-07-06" || w.Rows[1].Start != "2025-07-07" {
		t.Fatalf("weeks %+v %+v", w.Rows[0], w.Rows[1])
	}
	last := w.Rows[len(w.Rows)-1]
	if last.End != "2026-06-30" || *last.Total != end.TotalNow {
		t.Fatalf("last week %+v", last)
	}

	// the next day only the recent days are fetched again
	*f.now = f.now.Add(24 * time.Hour)
	f.svc.tick(ctx)
	if got := f.sim.Calls["/api/Values/ConsumptionValues"] - calls; got != refetchDays+1 {
		t.Fatalf("second sync fetched %d days, want %d", got, refetchDays+1)
	}
	// and not again the same day
	before := f.sim.Calls["/api/Values/UserValues"]
	f.svc.tick(ctx)
	if f.sim.Calls["/api/Values/UserValues"] != before {
		t.Fatal("synced twice on one day")
	}
}

// When ista has not measured the last days (fractional readings), the
// address says up to which day it did, and the usage leaves the estimated
// days out; once ista measures them, the refetch of the recent days replaces them.
func TestLastMeasured(t *testing.T) {
	f := newFixture(t, "2026-07-01")
	f.sim.Stale = 5
	ctx := context.Background()
	f.addAccount(t, f.sim.Password)
	f.svc.tick(ctx)
	a, err := f.svc.pickAddress(ctx, "")
	if err != nil || a.LastDay != "2026-11-11" || a.LastMeasured != "2026-11-06" {
		t.Fatalf("last day %q, measured %q (%v)", a.LastDay, a.LastMeasured, err)
	}
	u, err := f.svc.Usage(ctx, SimCuid, day("2026-11-01"), day("2026-11-11"), BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if u.Total.EstimatedDays != 0 || len(u.Buckets) != 6 || u.Total.End != "2026-11-06" {
		t.Fatalf("estimates not left out: %d estimated, %d days up to %s", u.Total.EstimatedDays, len(u.Buckets), u.Total.End)
	}
	{
		r, err := f.svc.totalReading(ctx, "")
		found := false
		for _, m := range r.Metrics {
			if m.Key == "last_measured" && m.Value == "06-11-2026" {
				found = true
			}
		}
		if err != nil || !found {
			t.Fatalf("no last_measured metric: %+v %v", r.Metrics, err)
		}
	}
	// the season, the building average and the costs so far stop at the last
	// measured day: ista's estimates after it do not count
	{
		p, _ := pickPeriod(a, 0, f.svc.today())
		m, err := f.svc.Usage(ctx, SimCuid, day(p.From), day(a.LastMeasured), BucketPeriod)
		if err != nil || m.Total.Total == nil || m.Total.Building == nil || m.Total.EstimatedDays != 0 {
			t.Fatalf("measured season %+v %v", m.Total, err)
		}
		r, _ := f.svc.totalReading(ctx, "")
		got := map[string]any{}
		for _, x := range r.Metrics {
			got[x.Key] = x.Value
		}
		if got["units_period"] != *m.Total.Total || got["building_period"] != *m.Total.Building {
			t.Fatalf("season metrics %v, measured %v / %v", got, *m.Total.Total, *m.Total.Building)
		}
		c, err := f.svc.Costs(ctx, "", 0)
		if err != nil || c.LastDay != a.LastMeasured || c.UnitsSoFar != *m.Total.Total {
			t.Fatalf("costs so far %v up to %q, measured %v (%v)", c.UnitsSoFar, c.LastDay, *m.Total.Total, err)
		}
	}

	// ista catches up: the next day's sync replaces the estimates; only
	// today is still running
	f.sim.Stale = 0
	*f.now = f.now.Add(24 * time.Hour)
	f.svc.tick(ctx)
	a, _ = f.svc.pickAddress(ctx, "")
	if a.LastDay != "2026-11-12" || a.LastMeasured != "2026-11-11" {
		t.Fatalf("after catching up: last day %q, measured %q", a.LastDay, a.LastMeasured)
	}
}

func TestSyncWrongPasswordStops(t *testing.T) {
	f := newFixture(t, "2026-09-01")
	ctx := context.Background()
	f.addAccount(t, "fout")
	f.svc.tick(ctx)
	st := f.svc.Statuses(ctx)
	if len(st) != 1 || st[0].State != "auth_failed" {
		t.Fatalf("status %+v", st)
	}
	*f.now = f.now.Add(48 * time.Hour)
	f.svc.tick(ctx)
	if f.sim.Calls["/api/Values/UserValues"] != 0 {
		t.Fatal("kept trying with refused credentials")
	}
	// new credentials try again
	f.addAccount(t, f.sim.Password)
	f.svc.Pace = -1
	f.svc.tick(ctx)
	if st := f.svc.Statuses(ctx); st[0].State != "idle" {
		t.Fatalf("after fix: %+v", st)
	}
}

func TestCostsEstimateAndSettlement(t *testing.T) {
	f := newFixture(t, "2025-06-01")
	ctx := context.Background()
	f.addAccount(t, f.sim.Password)
	f.svc.tick(ctx)

	// the settlement of 2025/26, figures as on a real one
	final := Contract{Y: 2026, AreaDM2: 7100, Final: &Settlement{
		VariableEnergy: 15748.88, FixedEnergy: 8480.16, Other: 5006.84, Services: 2686.25,
		UnitsComplex: 75970, AreaComplexDM2: 170400, Units: 1785, Total: 1043.92}}
	if _, err := f.svc.SaveContract(ctx, "", final); err != nil {
		t.Fatal(err)
	}
	c, err := f.svc.Costs(ctx, "", 2026)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Final || *c.UnitPrice != 0.207304 || *c.AreaPrice != 0.094913 {
		t.Fatalf("prices %v %v", *c.UnitPrice, *c.AreaPrice)
	}
	if c.Check == nil || !c.Check.OK || c.Check.Variable != 370.04 || c.Check.Fixed != 673.88 {
		t.Fatalf("check %+v", c.Check)
	}
	if c.FixedExpected == nil || *c.FixedExpected != 673.88 || c.VariableExpected == nil || math.Abs(*c.VariableExpected+673.88-*c.CostExpected) > 0.005 {
		t.Fatalf("final split %v %v of %v", c.FixedExpected, c.VariableExpected, c.CostExpected)
	}

	// moved in during the period (1 June of 2024/25): the fixed part by the
	// days also matches
	part := final
	part.Y = 2025
	set := *final.Final
	set.Units = 10
	pu, pa := set.Prices()
	set.Total = round2(round2(10*pu) + round2(7100*pa*30/365))
	part.Final = &set
	if _, err := f.svc.SaveContract(ctx, "", part); err != nil {
		t.Fatal(err)
	}
	if c, err := f.svc.Costs(ctx, "", 2025); err != nil || c.Check == nil || !c.Check.OK || !c.Check.Prorated {
		t.Fatalf("prorated check %+v %v", c.Check, err)
	}

	// the running period: last settlement's prices, +10%, with an advance
	run := Contract{Y: 2027, AreaDM2: 7100, CorrectionPct: 10, Advance: []Payment{{From: "2026-07-01", Amount: 90}}}
	if _, err := f.svc.SaveContract(ctx, "", run); err != nil {
		t.Fatal(err)
	}
	c, err = f.svc.Costs(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.Period.Y != 2027 || c.Final || !c.Running || c.PriceSource != "previous" || c.PriceFrom != 2026 {
		t.Fatalf("running %+v", c)
	}
	if math.Abs(*c.UnitPrice-round6(0.207304*1.1)) > 1e-9 {
		t.Fatalf("unit price %v", *c.UnitPrice)
	}
	if c.RestBasis != "last_year" || c.UnitsExpected == nil || *c.UnitsExpected <= c.UnitsSoFar || c.ForecastError != "" {
		t.Fatalf("forecast %+v", c)
	}
	// the margin: usage against the previous period, and the price taken
	// over from the previous settlement
	keys := map[string]forecast.Used{}
	for _, u := range c.Percentages {
		keys[u.Key] = u
	}
	if u, ok := keys["usage_heating"]; !ok || u.Pct < forecast.MinComputedPct || u.Pct > forecast.MaxComputedPct {
		t.Fatalf("usage margin %+v", c.Percentages)
	}
	if u, ok := keys["price_heating"]; !ok || u.Pct != 10 || keys["period_min"].Pct != 90 {
		t.Fatalf("price margin %+v", c.Percentages)
	}
	usage := keys["usage_heating"].Pct / 100
	want := math.Hypot(*c.UnitsRest*usage**c.UnitPrice, *c.UnitsExpected*0.10**c.UnitPrice)
	if math.Abs((*c.CostHigh-*c.CostLow)/2-want) > 0.011 {
		t.Fatalf("half band %v, want %v", (*c.CostHigh-*c.CostLow)/2, want)
	}
	// paid 1 Jul .. 1 Nov = 5 payments, 1 Dec .. 1 Jun = 7 to go
	if c.Paid != 450 || c.ToPay != 630 || c.PaymentsLeft != 7 {
		t.Fatalf("advance paid %v to pay %v left %d", c.Paid, c.ToPay, c.PaymentsLeft)
	}
	if c.Balance == nil || c.Advice == nil || *c.CostLow > *c.CostExpected || *c.CostHigh < *c.CostExpected {
		t.Fatalf("balance %+v", c)
	}
	if c.FixedExpected == nil || c.VariableExpected == nil || *c.FixedExpected != *c.FixedPeriod ||
		math.Abs(*c.FixedExpected+*c.VariableExpected-*c.CostExpected) > 0.011 {
		t.Fatalf("expected split %+v", c)
	}
	if c.Payments != 5 || c.Monthly != 90 || c.NextPayment != "2026-12-01" {
		t.Fatalf("advance details %+v", c)
	}

	// an expected yearly usage goes before last year's
	run.EstimateUnits = 3000
	if _, err := f.svc.SaveContract(ctx, "", run); err != nil {
		t.Fatal(err)
	}
	if c, err = f.svc.Costs(ctx, "", 0); err != nil || c.RestBasis != "estimate" {
		t.Fatalf("estimate with a year before: %+v %v", c, err)
	}
	run.EstimateUnits = 0
	if _, err := f.svc.SaveContract(ctx, "", run); err != nil {
		t.Fatal(err)
	}

	// an invalid contract is refused
	if _, err := f.svc.SaveContract(ctx, "", Contract{Y: 2027}); err == nil {
		t.Fatal("contract without area accepted")
	}
	if _, err := f.svc.SaveContract(ctx, "", Contract{Y: 1999, AreaDM2: 1}); err == nil {
		t.Fatal("unknown period accepted")
	}
	// a settlement filled in with the prices instead of the complex's totals
	for i, s := range []Settlement{
		{VariableEnergy: 0.207304, Other: 0.094913, AreaComplexDM2: 7100, Units: 1785},              // no units of the complex
		{VariableEnergy: 15748.88, FixedEnergy: 8480.16, UnitsComplex: 75970, AreaComplexDM2: 7000}, // area below the dwelling's
		{VariableEnergy: 15748.88, FixedEnergy: 8480.16, UnitsComplex: 1000, AreaComplexDM2: 170400, Units: 1785},
		{FixedEnergy: 8480.16, UnitsComplex: 75970, AreaComplexDM2: 170400},
		{VariableEnergy: 15748.88, UnitsComplex: 75970, AreaComplexDM2: 170400},
	} {
		if _, err := f.svc.SaveContract(ctx, "", Contract{Y: 2027, AreaDM2: 7100, Final: &s}); err == nil {
			t.Errorf("settlement %d accepted", i)
		}
	}
}

func TestPaymentDays(t *testing.T) {
	ps := paymentDays([]Payment{{"2025-01-31", 10}, {"2025-04-15", 20}}, day("2025-01-01"), day("2025-06-30"))
	var got []string
	for _, p := range ps {
		got = append(got, p.day.Format(DayLayout))
	}
	want := []string{"2025-01-31", "2025-02-28", "2025-03-31", "2025-04-15", "2025-05-15", "2025-06-15"}
	if len(got) != len(want) {
		t.Fatalf("%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%v", got)
		}
	}
}

func TestProviderScanAndPoll(t *testing.T) {
	f := newFixture(t, "2026-09-01")
	ctx := context.Background()
	f.addAccount(t, f.sim.Password)
	p := Provider{Svc: f.svc}
	inst, err := p.Instance(map[string]string{"username": f.sim.Username, "password": f.sim.Password}, plugin.Env{InstanceID: "acc1"})
	if err != nil {
		t.Fatal(err)
	}
	cands, err := inst.Scan(ctx)
	if err != nil || len(cands) != 5 {
		t.Fatalf("scan %v %v", cands, err)
	}
	// before the first sync there is nothing to show
	r, err := inst.Poll(ctx, plugin.Device{Address: cands[1].Address, Config: cands[1].Config})
	if err != nil || r.Online {
		t.Fatalf("poll before sync %+v %v", r, err)
	}
	f.svc.tick(ctx)
	for _, c := range cands {
		r, err := inst.Poll(ctx, plugin.Device{Address: c.Address, Config: c.Config})
		if err != nil || !r.Online || len(r.Metrics) < 4 {
			t.Fatalf("poll %s: %+v %v", c.Address, r, err)
		}
	}
	bad, _ := p.Instance(map[string]string{"username": f.sim.Username, "password": "fout"}, plugin.Env{InstanceID: "acc1"})
	if _, err := bad.Scan(ctx); err == nil {
		t.Fatal("scan with a wrong password worked")
	}
}

func TestForecastFromEstimate(t *testing.T) {
	f := newFixture(t, "2026-09-01") // no year before to follow
	ctx := context.Background()
	f.addAccount(t, f.sim.Password)
	f.svc.SyncDue(ctx)
	c, err := f.svc.Costs(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if c.UnitsRest != nil || c.RestBasis != "" || !strings.HasPrefix(c.ForecastError, "Geen prognose") {
		t.Fatalf("forecast without estimate or previous period: %+v", c)
	}
	if _, err := f.svc.SaveContract(ctx, "", Contract{Y: 2027, AreaDM2: 7000, UnitPrice: ptr(0.2), AreaPrice: ptr(0.09), EstimateUnits: 2000}); err != nil {
		t.Fatal(err)
	}
	c, err = f.svc.Costs(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	// 11 Nov (today is still running, so not measured) .. 30 Jun of the
	// standard profile: most of the winter
	share := 0.0
	for d := day("2026-11-11"); !d.After(day("2027-06-30")); d = d.AddDate(0, 0, 1) {
		share += standardProfile[d.Month()-1] / float64(daysIn(d.Year(), d.Month()))
	}
	if c.RestBasis != "estimate" || c.Profile != "standard" || *c.UnitsRest != math.Round(2000*share) {
		t.Fatalf("rest %v (%s, %s), want %v", c.UnitsRest, c.RestBasis, c.Profile, math.Round(2000*share))
	}
	// prices from the contract: only the usage margin of an entered usage
	if want := *c.UnitsRest * 0.20 * 0.2; math.Abs(*c.CostHigh-*c.CostExpected-want) > 0.011 {
		t.Fatalf("margin %v, want %v: %+v", *c.CostHigh-*c.CostExpected, want, c.Percentages)
	}
}

func TestHeatingProfile(t *testing.T) {
	sum := 0.0
	for _, v := range standardProfile {
		sum += v
	}
	if math.Abs(sum-1) > 1e-9 {
		t.Fatalf("standard profile sums to %v", sum)
	}
	// twelve complete months of the building make the profile
	var months []store.IstaMonth
	for m := 1; m <= 12; m++ {
		v := float64(13 - m)
		months = append(months, store.IstaMonth{Year: 2026, Month: m, BuildingAvg: &v})
	}
	p, src := heatingProfile(months)
	if src != "building" || math.Abs(p[0]-12.0/78) > 1e-9 {
		t.Fatalf("%s %v", src, p)
	}
	// one month missing: the standard one
	if _, src := heatingProfile(months[1:]); src != "standard" {
		t.Fatalf("incomplete: %s", src)
	}
}
