package report

import (
	"context"
	"log/slog"
	"mime"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
)

func TestAdvanceParts(t *testing.T) {
	all := forecastParts{true, true, true, true}
	for _, c := range []struct {
		sections []string
		want     forecastParts
	}{
		{[]string{SecAdvance}, all},
		{[]string{SecGrid, SecAdvance}, all},
		{[]string{SecElectricity, SecGas, SecAdvance}, forecastParts{electricity: true, gas: true}},
		{[]string{SecGas, SecAdvance}, forecastParts{gas: true}},
		{[]string{SecHeating, SecAdvance}, forecastParts{heating: true}},
		{[]string{SecWater, SecAdvance}, forecastParts{water: true}},
		{[]string{SecSummary, SecAdvance}, forecastParts{electricity: true, gas: true}},
		{[]string{SecDevices, SecAdvance}, forecastParts{electricity: true, heating: true}},
	} {
		if got := advanceParts(c.sections); got != c.want {
			t.Errorf("%v: %+v, want %+v", c.sections, got, c.want)
		}
	}
	gas := forecastParts{gas: true}
	if !gas.energy(costs.PartGas) || !gas.energy("") || gas.energy(costs.PartElectricity) {
		t.Error("only gas: gas and combined contracts")
	}
}

// TestAdvanceFollowsSections: the forecast only shows what the report is
// about.
func TestAdvanceFollowsSections(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	withIsta(t, fx, 0)
	f := func(v float64) *float64 { return &v }
	if _, err := fx.b.Ista.SaveContract(ctx, "", ista.Contract{Y: 2027, AreaDM2: 7000, UnitPrice: f(0.8), AreaPrice: f(0.03),
		EstimateUnits: 1200, Advance: []ista.Payment{{From: "2026-09-01", Amount: 90}}}); err != nil {
		t.Fatal(err)
	}
	adv := func(eur float64) *costs.Advance {
		return &costs.Advance{Amounts: []costs.AdvanceAmount{{From: "2026-11-01", EUR: eur}}}
	}
	if _, err := fx.b.Costs.Contracts.Save(ctx, costs.Contract{
		Name: "Gesplitst", Start: "2026-11-01", Type: costs.TypeDynamic, Split: true, GasType: costs.TypeDynamic,
		Electricity: costs.Electricity{MarkupCt: 2, TaxCt: 11}, Gas: costs.Gas{MarkupCt: 5, TaxCt: 70},
		Advance: adv(80), GasAdvance: adv(60),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := fx.b.WaterCosts.Contracts.Save(ctx, costs.Contract{
		Name: "Water", Start: "2026-01-01", Type: costs.TypeFixed, Water: costs.Water{RateCt: 100}, Advance: adv(15),
	}); err != nil {
		t.Fatal(err)
	}
	from, to := MonthPeriod(time.Date(2026, 10, 1, 0, 0, 0, 0, ams), ams)
	build := func(sections ...string) *Advance {
		t.Helper()
		r, err := fx.b.Build(ctx, Request{From: from, To: to, Sections: sections})
		if err != nil {
			t.Fatal(err)
		}
		return r.Advance
	}
	parts := func(a *Advance) (string, int, int) {
		var ps []string
		if a == nil {
			return "", 0, 0
		}
		for _, st := range a.Energy {
			ps = append(ps, st.Part)
		}
		return strings.Join(ps, ","), len(a.Water), len(a.Heating)
	}
	for _, c := range []struct {
		sections      []string
		energy        string
		water, heated int
	}{
		{[]string{SecAdvance}, "electricity,gas", 1, 1},
		{[]string{SecElectricity, SecGas, SecAdvance}, "electricity,gas", 0, 0},
		{[]string{SecGas, SecAdvance}, "gas", 0, 0},
		{[]string{SecWater, SecAdvance}, "", 1, 0},
		{[]string{SecHeating, SecAdvance}, "", 0, 1},
	} {
		e, w, h := parts(build(c.sections...))
		if e != c.energy || w != c.water || h != c.heated {
			t.Errorf("%v: energy %q, water %d, heating %d; want %q, %d, %d", c.sections, e, w, h, c.energy, c.water, c.heated)
		}
	}
}

type mailFixture struct {
	*fixture
	sim  *ista.Sim
	smtp *mail.SimServer
	sch  *Scheduler
}

func newMailFixture(t *testing.T, stale int) *mailFixture {
	t.Helper()
	fx := newFixture(t)
	ctx := context.Background()
	fx.now = time.Date(2026, 10, 20, 12, 0, 0, 0, ams)
	sim := withIsta(t, fx, stale)
	smtp := &mail.SimServer{}
	addr, err := smtp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { smtp.Close() })
	host, ps, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(ps)
	if err := SaveMailSettings(ctx, fx.st, mail.Settings{Host: host, Port: port, Security: mail.None, From: "gw@example.nl"}); err != nil {
		t.Fatal(err)
	}
	return &mailFixture{fixture: fx, sim: sim, smtp: smtp, sch: newScheduler(fx)}
}

func newScheduler(fx *fixture) *Scheduler {
	return &Scheduler{Store: fx.st, Builder: fx.b, Sender: &mail.Sender{}, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return fx.now }}
}

func (m *mailFixture) mails(t *testing.T) []mailOut {
	t.Helper()
	var out []mailOut
	dec := new(mime.WordDecoder)
	for _, msg := range m.smtp.Messages() {
		subject, text := parseMail(t, msg.Data)
		if s, err := dec.DecodeHeader(subject); err == nil {
			subject = s
		}
		out = append(out, mailOut{to: msg.To[0], subject: subject, text: text})
	}
	return out
}

type mailOut struct{ to, subject, text string }

// TestScheduleSendsOnTimeHeatingFollows: while ista has not measured the
// period, a report with heating and more goes on time without heating, and
// the heating follows once ista has; a report with only heating waits.
func TestScheduleSendsOnTimeHeatingFollows(t *testing.T) {
	m := newMailFixture(t, 5)
	ctx := context.Background()
	withHeat, err := m.sch.Save(ctx, Schedule{Name: "Met verwarming", Sections: []string{SecSummary, SecHeating, SecAdvance}, To: []string{"a@example.nl"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	only, err := m.sch.Save(ctx, Schedule{Name: "Alleen verwarming", Sections: []string{SecHeating, SecAdvance}, To: []string{"c@example.nl"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.sch.Save(ctx, Schedule{Name: "Zonder", Sections: []string{SecSummary}, To: []string{"b@example.nl"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	// due: both reports with more than heating go; heating-only waits
	m.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	m.b.Ista.SyncDue(ctx)
	m.sch.Check(ctx)
	got := m.mails(t)
	if len(got) != 2 || got[0].to != "a@example.nl" || got[1].to != "b@example.nl" {
		t.Fatalf("mails %+v", got)
	}
	if !strings.Contains(got[0].text, "het overzicht verwarming volgt") || strings.Contains(got[0].text, "Kosten verwarming") || strings.Contains(got[0].subject, "verwarming") {
		t.Fatalf("on time without heating:\n%s\n%s", got[0].subject, got[0].text)
	}
	until := time.Date(2026, 11, 9, 8, 0, 0, 0, ams)
	sc, _ := m.sch.Get(ctx, withHeat.ID)
	if sc.LastPeriod != "2026-10" || sc.WaitUntil != nil || sc.HeatingPeriod != "2026-10" || sc.HeatingUntil == nil || !sc.HeatingUntil.Equal(until) {
		t.Fatalf("after the on-time send %+v", sc)
	}
	if o, _ := m.sch.Get(ctx, only.ID); o.WaitUntil == nil || !o.WaitUntil.Equal(until) || o.LastPeriod != "2026-09" {
		t.Fatalf("heating only %+v", o)
	}
	// a restart keeps the heating that is to follow
	m.sch = newScheduler(m.fixture)
	m.sch.Check(ctx)
	if n := len(m.smtp.Messages()); n != 2 {
		t.Fatalf("%d mails before ista caught up", n)
	}

	// ista catches up: the heating follows, the heating-only report goes
	m.sim.Stale = 0
	m.now = time.Date(2026, 11, 3, 9, 0, 0, 0, ams)
	m.b.Ista.SyncDue(ctx)
	m.sch.Check(ctx)
	got = m.mails(t)
	if len(got) != 4 {
		t.Fatalf("%d mails", len(got))
	}
	follow := got[2]
	if follow.to != "a@example.nl" || !strings.HasSuffix(follow.subject, "– verwarming") || !strings.Contains(follow.text, "nog ontbrak") ||
		!strings.Contains(follow.text, "Verwarming:") || strings.Contains(follow.text, "Stroom afgenomen") || strings.Contains(follow.text, "nog niet compleet") {
		t.Fatalf("heating that followed:\n%s\n%s", follow.subject, follow.text)
	}
	if got[3].to != "c@example.nl" {
		t.Fatalf("heating only %+v", got[3])
	}
	if sc, _ := m.sch.Get(ctx, withHeat.ID); sc.HeatingPeriod != "" || sc.HeatingUntil != nil || sc.LastPeriod != "2026-10" {
		t.Fatalf("after the heating %+v", sc)
	}
	m.now = m.now.Add(2 * time.Hour)
	m.sch.Check(ctx)
	if n := len(m.smtp.Messages()); n != 4 {
		t.Fatalf("sent again: %d", n)
	}
}

// TestHeatingFollowsAfterAWeek: when ista keeps estimating, the heating
// follows a week after the report was due, with what ista has.
func TestHeatingFollowsAfterAWeek(t *testing.T) {
	m := newMailFixture(t, 40)
	ctx := context.Background()
	sc, err := m.sch.Save(ctx, Schedule{Sections: []string{SecSummary, SecHeating}, To: []string{"a@example.nl"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	m.sch.Check(ctx)
	m.now = time.Date(2026, 11, 9, 7, 59, 0, 0, ams)
	m.sch.Check(ctx)
	if n := len(m.smtp.Messages()); n != 1 {
		t.Fatalf("%d mails before the week was over", n)
	}
	m.now = time.Date(2026, 11, 9, 8, 0, 0, 0, ams)
	m.sch.Check(ctx)
	got := m.mails(t)
	if len(got) != 2 || !strings.Contains(got[1].text, "nog niet compleet gemeten") || !strings.HasSuffix(got[1].subject, "– verwarming") {
		t.Fatalf("mails %+v", got)
	}
	if s, _ := m.sch.Get(ctx, sc.ID); s.HeatingPeriod != "" {
		t.Fatalf("still to follow %+v", s)
	}
}

// TestHeatingFollowUpDropped: sending the whole period now, or taking
// heating out of the schedule, settles the heating that was to follow.
func TestHeatingFollowUpDropped(t *testing.T) {
	m := newMailFixture(t, 5)
	ctx := context.Background()
	sc, err := m.sch.Save(ctx, Schedule{Sections: []string{SecSummary, SecHeating}, To: []string{"a@example.nl"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	m.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	m.sch.Check(ctx)
	if err := m.sch.SendNow(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	if s, _ := m.sch.Get(ctx, sc.ID); s.HeatingPeriod != "" {
		t.Fatalf("after sending now %+v", s)
	}

	// next month: on time without heating, then heating is taken out
	m.now = time.Date(2026, 12, 2, 9, 0, 0, 0, ams)
	m.b.Ista.SyncDue(ctx)
	m.sch.Check(ctx)
	s, _ := m.sch.Get(ctx, sc.ID)
	if s.HeatingPeriod != "2026-11" {
		t.Fatalf("november %+v", s)
	}
	s.Sections = []string{SecSummary}
	if _, err := m.sch.Save(ctx, s); err != nil {
		t.Fatal(err)
	}
	m.sim.Stale = 0
	m.now = m.now.Add(24 * time.Hour)
	m.b.Ista.SyncDue(ctx)
	n := len(m.smtp.Messages())
	m.sch.Check(ctx)
	if len(m.smtp.Messages()) != n {
		t.Fatal("heating sent for a schedule without heating")
	}
	if s, _ := m.sch.Get(ctx, sc.ID); s.HeatingPeriod != "" {
		t.Fatalf("not dropped %+v", s)
	}
}
