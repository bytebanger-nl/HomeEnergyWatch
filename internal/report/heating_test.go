package report

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net"
	"net/http/httptest"
	netmail "net/mail"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// withIsta adds the fake mijn.ista.nl to the fixture: lived in from
// 1 September 2026, the last stale days are ista's estimates.
func withIsta(t *testing.T, fx *fixture, stale int) *ista.Sim {
	t.Helper()
	ctx := context.Background()
	sim := &ista.Sim{Username: "sim@example.org", Password: "sim", MoveIn: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Now: func() time.Time { return fx.now }, Stale: stale}
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	if err := fx.st.SavePlugin(ctx, store.PluginInstance{ID: "acc1", Type: ista.PluginType, Name: "ista",
		Config: map[string]string{"username": sim.Username, "password": sim.Password}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	fx.b.Ista = &ista.Service{Store: fx.st, Loc: ams, BaseURL: srv.URL, Pace: -1, Now: func() time.Time { return fx.now },
		Log: slog.New(slog.DiscardHandler)}
	fx.b.Ista.SyncDue(ctx)
	return sim
}

func TestHeatingSectionLeavesOutEstimates(t *testing.T) {
	fx := newFixture(t)
	fx.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	withIsta(t, fx, 5)
	from, to := MonthPeriod(time.Date(2026, 10, 1, 0, 0, 0, 0, ams), ams)
	r, err := fx.b.Build(context.Background(), Request{From: from, To: to, Sections: []string{SecSummary, SecHeating}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Heating == nil || len(r.Heating.Addresses) != 1 {
		t.Fatalf("heating %+v", r.Heating)
	}
	a := r.Heating.Addresses[0]
	if a.Complete || r.Heating.Complete() || a.LastMeasured.IsZero() || a.Usage.Total.Total == nil || len(a.Rooms) == 0 {
		t.Fatalf("address %+v", a)
	}
	// ista measured up to 28 October: the 3 days after it (its estimates)
	// are left out and counted as missing
	if len(a.Usage.Buckets) != 28 || a.Usage.Total.EstimatedDays != 0 || a.Missing != 3 {
		t.Fatalf("%d day buckets, %d estimated, %d missing", len(a.Usage.Buckets), a.Usage.Total.EstimatedDays, a.Missing)
	}
	text, html := r.MailBody("Maandrapport")
	if !strings.Contains(text, "Verwarming:") || !strings.Contains(text, "nog niet compleet gemeten") || !strings.Contains(html, "nog niet compleet") {
		t.Fatalf("mail:\n%s", text)
	}
	if _, err := r.Bytes(); err != nil {
		t.Fatal(err)
	}
	pending, err := fx.b.HeatingPending(context.Background(), from, to)
	if err != nil || !pending {
		t.Fatalf("pending %v %v", pending, err)
	}
}

func TestScheduleSendsAfterWaitingAWeek(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.now = time.Date(2026, 10, 20, 12, 0, 0, 0, ams)
	withIsta(t, fx, 40) // ista keeps estimating
	smtp := &mail.SimServer{}
	addr, _ := smtp.Listen("127.0.0.1:0")
	defer smtp.Close()
	host, ps, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(ps)
	_ = SaveMailSettings(ctx, fx.st, mail.Settings{Host: host, Port: port, Security: mail.None, From: "gw@example.nl"})
	sch := &Scheduler{Store: fx.st, Builder: fx.b, Sender: &mail.Sender{}, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return fx.now }}
	if _, err := sch.Save(ctx, Schedule{Sections: []string{SecHeating}, To: []string{"a@example.nl"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	fx.now = time.Date(2026, 11, 9, 7, 59, 0, 0, ams)
	sch.Check(ctx)
	if n := len(smtp.Messages()); n != 0 {
		t.Fatalf("sent before the week was over: %d", n)
	}
	fx.now = time.Date(2026, 11, 9, 8, 0, 0, 0, ams)
	sch.Check(ctx)
	m := smtp.Messages()
	if len(m) != 1 {
		t.Fatalf("%d messages after a week", len(m))
	}
	_, ps2 := parseMail(t, m[0].Data)
	if !strings.Contains(ps2, "nog niet compleet gemeten") {
		t.Fatalf("no notice in:\n%s", ps2)
	}
}

func TestContractYearRange(t *testing.T) {
	fx := newFixture(t) // contract from 1 January 2026
	ctx := context.Background()
	_, to, _ := Period(Monthly, time.Date(2026, 11, 2, 9, 0, 0, 0, ams), ams)
	for _, c := range []struct{ rng, want string }{
		{"", "2026-10-01"}, {RangeYear, "2026-01-01"}, {RangeContractYear, "2026-01-01"},
	} {
		from, err := fx.b.RangeStart(ctx, c.rng, time.Date(2026, 10, 1, 0, 0, 0, 0, ams), to)
		if err != nil || from.Format("2006-01-02") != c.want {
			t.Errorf("%q: %s %v", c.rng, from, err)
		}
	}
	// an earlier contract from 1 March 2025: its own contract year
	if _, err := fx.b.Costs.Contracts.Save(ctx, contractFrom("2025-03-01")); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ to, want string }{{"2025-06-01", "2025-03-01"}, {"2026-03-01", "2026-01-01"}, {"2026-11-01", "2026-01-01"}}
	for _, c := range cases {
		to, _ := time.ParseInLocation("2006-01-02", c.to, ams)
		from, err := fx.b.RangeStart(ctx, RangeContractYear, to, to)
		if err != nil || from.Format("2006-01-02") != c.want {
			t.Errorf("to %s: %s %v", c.to, from, err)
		}
	}
	if err := (&Schedule{Kind: Monthly, Range: "decade", Sections: []string{SecCosts}, To: []string{"a@example.nl"}}).Validate(); err == nil {
		t.Error("unknown range accepted")
	}
}

func contractFrom(start string) costs.Contract {
	return costs.Contract{Name: "Oud", Start: start, End: "2025-12-31", Type: costs.TypeFixed,
		Electricity: costs.Electricity{RateCt: 25, TaxCt: 11}, Gas: costs.Gas{RateCt: 120, TaxCt: 70}}
}

// parseMail returns the subject and the decoded text part of a message.
func parseMail(t *testing.T, data []byte) (string, string) {
	t.Helper()
	msg, err := netmail.ReadMessage(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	_, params, _ := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	var text string
	var walk func(r io.Reader, boundary string)
	walk = func(r io.Reader, boundary string) {
		mr := multipart.NewReader(r, boundary)
		for {
			p, err := mr.NextPart()
			if err != nil {
				return
			}
			mt, ps, _ := mime.ParseMediaType(p.Header.Get("Content-Type"))
			switch {
			case strings.HasPrefix(mt, "multipart/"):
				walk(p, ps["boundary"])
			case mt == "text/plain":
				b, _ := io.ReadAll(base64.NewDecoder(base64.StdEncoding, p))
				text = string(b)
			}
		}
	}
	walk(msg.Body, params["boundary"])
	return msg.Header.Get("Subject"), text
}

func TestHeatingCostsDevicesAndAdvance(t *testing.T) {
	fx := newFixture(t)
	fx.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	withIsta(t, fx, 5)
	ctx := context.Background()
	f := func(v float64) *float64 { return &v }
	if _, err := fx.b.Ista.SaveContract(ctx, "", ista.Contract{Y: 2027, AreaDM2: 7000, UnitPrice: f(0.8), AreaPrice: f(0.03),
		EstimateUnits: 1200, Advance: []ista.Payment{{From: "2026-09-01", Amount: 90}}}); err != nil {
		t.Fatal(err)
	}
	from, to := MonthPeriod(time.Date(2026, 10, 1, 0, 0, 0, 0, ams), ams)
	r, err := fx.b.Build(ctx, Request{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	a := r.Heating.Addresses[0]
	c := a.Costs
	if c == nil || c.Total.Variable == nil || len(c.Total.Rooms) != len(a.Rooms) {
		t.Fatalf("costs %+v", c)
	}
	// the radiators' costs add up to the variable part (to the cent per room)
	sum := 0.0
	for _, v := range c.Total.Rooms {
		sum += pv(v)
	}
	if d := sum - *c.Total.Variable; d > 0.01*float64(len(a.Rooms)) || d < -0.01*float64(len(a.Rooms)) {
		t.Errorf("rooms %.2f, variable %.2f", sum, *c.Total.Variable)
	}
	// ista's estimates are left out: the costs stop at the last measured day
	if c.Total.Estimated || a.Missing == 0 || c.Total.End != a.LastMeasured.Format(ista.DayLayout) {
		t.Errorf("costs up to %s (estimated %v), measured up to %s, %d days missing", c.Total.End, c.Total.Estimated, a.LastMeasured.Format(ista.DayLayout), a.Missing)
	}
	if len(a.Prices) != 1 || a.Prices[0].Costs.PriceSource != "contract" {
		t.Errorf("prices %+v", a.Prices)
	}
	if r.Devices == nil || len(r.Devices.Heating) != 1 {
		t.Fatalf("devices %+v", r.Devices)
	}
	if r.Advance == nil || len(r.Advance.Heating) != 1 || r.Advance.Heating[0].Costs.Monthly != 90 {
		t.Fatalf("advance %+v", r.Advance)
	}
	if r.Summary == nil || r.Summary.Heating == nil {
		t.Fatal("no heating in the summary")
	}
	_, total, parts, _ := (&doc{r: r}).costParts()
	if parts < 2 || total < *c.Total.Total {
		t.Errorf("cost parts %d, total %.2f", parts, total)
	}
	if _, err := r.Bytes(); err != nil {
		t.Fatal(err)
	}
	// the advance comes last
	if Sections[len(Sections)-1].Key != SecAdvance {
		t.Error("advance is not the last section")
	}
}
