package report

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

var ams = prices.Amsterdam

func fp(v float64) *float64 { return &v }

type fixture struct {
	st  *store.Store
	b   *Builder
	now time.Time
}

// newFixture: a P1 meter, a socket and a water meter with October 2026
// fully measured, a dynamic contract with prices for every quarter hour.
func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fx := &fixture{st: st, now: time.Date(2026, 11, 5, 12, 0, 0, 0, ams)}
	now := func() time.Time { return fx.now }
	contracts := &costs.Contracts{Store: st}
	if _, err := contracts.Save(ctx, costs.Contract{
		Name: "Dynamisch", Start: "2026-01-01", Type: costs.TypeDynamic,
		Electricity: costs.Electricity{MarkupCt: 2, TaxCt: 11, ExportCt: 1},
		Gas:         costs.Gas{MarkupCt: 5, TaxCt: 70},
		Monthly:     []costs.MonthlyCost{{Name: "Vast", EUR: 10}},
	}); err != nil {
		t.Fatal(err)
	}
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, ams)
	to := from.AddDate(0, 1, 0)
	var p1, sock, water []store.Quarter
	var ps []store.MarketPrice
	for q := from; q.Before(to); q = q.Add(15 * time.Minute) {
		lt := q.In(ams)
		h := lt.Hour()
		imp, exp := 0.05, 0.0 // 200 W at night
		switch {
		case h >= 11 && h < 15:
			imp, exp = 0, 0.3 // solar
		case h >= 18 && h < 21:
			imp = 0.5 // 2 kW
		case h >= 7:
			imp = 0.1
		}
		if lt.Day() == 15 && h == 18 && lt.Minute() == 30 {
			imp = 1.25 // the peak: 5 kW
		}
		gas := 0.0
		if h >= 6 && h < 23 {
			gas = 0.05
		}
		p1 = append(p1, store.Quarter{DeviceID: "p1", Start: q, ImportKWh: fp(imp), ExportKWh: fp(exp), GasM3: fp(gas), Quality: "complete", Coverage: 1, Source: store.SourceMeasured})
		sock = append(sock, store.Quarter{DeviceID: "s1", Start: q, ImportKWh: fp(0.01), ExportKWh: fp(0), Quality: "complete", Coverage: 1, Source: store.SourceMeasured})
		w := 0.0
		if h >= 7 && h < 22 {
			w = 0.004
		}
		water = append(water, store.Quarter{DeviceID: "w1", Start: q, WaterM3: fp(w), Quality: "complete", Coverage: 1, Source: store.SourceMeasured})
		price := 80.0
		if h >= 17 && h < 21 {
			price = 200 // expensive evening
		}
		ps = append(ps, store.MarketPrice{Start: q, Duration: 15 * time.Minute, Value: price, Source: "test"})
	}
	for _, qs := range [][]store.Quarter{p1, sock, water} {
		if err := st.SaveQuarters(ctx, qs); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SavePrices(ctx, prices.Market, ps); err != nil {
		t.Fatal(err)
	}
	var gps []store.MarketPrice
	for d := from.AddDate(0, 0, -1); d.Before(to); d = d.AddDate(0, 0, 1) {
		s, _ := prices.GasDayBounds(d)
		gps = append(gps, store.MarketPrice{Start: s, Duration: 24 * time.Hour, Value: 0.5, Source: "test"})
	}
	if err := st.SavePrices(ctx, prices.GasMarket, gps); err != nil {
		t.Fatal(err)
	}
	fx.b = &Builder{
		Store:      st,
		Costs:      &costs.Calculator{Store: st, Contracts: contracts, Loc: ams, Now: now},
		WaterCosts: &costs.Calculator{Store: st, Contracts: &costs.Contracts{Store: st, Table: store.WaterContracts}, Loc: ams, Now: now},
		Scenarios:  &costs.Scenarios{Store: st},
		Loc:        ams, Now: now, Version: "test",
		Devices: func() []Device {
			return []Device{{"p1", "Slimme meter", homewizard.KindP1}, {"s1", "Koelkast", homewizard.KindSocket}, {"w1", "Water", homewizard.KindWater}}
		},
	}
	return fx
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestReportMatchesCalculator(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	from, to := MonthPeriod(time.Date(2026, 10, 10, 0, 0, 0, 0, ams), ams)
	r, err := fx.b.Build(ctx, Request{From: from, To: to})
	if err != nil {
		t.Fatal(err)
	}
	want, err := fx.b.Costs.Calculate(ctx, "p1", from, to, costs.BucketDay)
	if err != nil {
		t.Fatal(err)
	}
	if r.Summary == nil || r.Summary.Energy == nil || r.Costs == nil || r.Electricity == nil || r.Gas == nil || r.Devices == nil || r.Water == nil {
		t.Fatalf("missing sections: %+v", r)
	}
	if !near(r.Summary.Energy.TotalEUR, want.Total.TotalEUR) || !near(r.Costs.Total.TotalEUR, want.Total.TotalEUR) || !near(r.Summary.Energy.ImportKWh, want.Total.ImportKWh) {
		t.Fatalf("report %v / %v, calculator %v", r.Summary.Energy.TotalEUR, r.Costs.Total.TotalEUR, want.Total.TotalEUR)
	}
	if len(r.Costs.Buckets) != 31 || r.Bucket != costs.BucketDay {
		t.Fatalf("%d buckets of %s", len(r.Costs.Buckets), r.Bucket)
	}
	if s := r.Summary; s.Expected != 31*96+4 || s.Missing != 0 || s.Imported != 0 { // 25 October has 100 quarters
		t.Fatalf("completeness %+v", s)
	}
	e := r.Electricity
	if !near(e.BaseLoadW, 200) || e.BaseLoadNights != 31 {
		t.Fatalf("base load %v W over %d nights", e.BaseLoadW, e.BaseLoadNights)
	}
	if len(e.Peaks) != 5 || !near(e.Peaks[0].KW, 5) || e.Peaks[0].Start.In(ams).Day() != 15 {
		t.Fatalf("peaks %+v", e.Peaks)
	}
	if e.Weekday[4*19] <= 1.9 || e.Weekday[4*12] >= 0 {
		t.Fatalf("profile 19:00 %v 12:00 %v", e.Weekday[4*19], e.Weekday[4*12])
	}
	// the evening is expensive and has most import: paid above average
	if d := r.Costs.Dynamic; d == nil || d.WeightedCt <= d.AverageCt || !near(d.ImportKWh, want.Total.ImportKWh) {
		t.Fatalf("dynamic %+v", d)
	}
	dev := r.Devices
	if len(dev.List) != 1 || !near(dev.List[0].ImportKWh, 0.01*float64(31*96+4)) || dev.OtherKWh <= 0 {
		t.Fatalf("devices %+v", dev)
	}
	// water: no water contract, so not priced; never zero at night: no leak
	if w := r.Water; w.Priced || w.Nights != 31 || w.NightsNoZero != 0 || !near(w.Total.WaterM3, w.YearM3) {
		t.Fatalf("water %+v", w)
	}
	if r.Grid != nil || r.Advance != nil || r.Scenarios != nil {
		t.Fatal("sections without data are not left out")
	}

	pdf, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || len(pdf) < 5000 {
		t.Fatalf("pdf %d bytes", len(pdf))
	}
}

func TestReportSectionsAndMonthBuckets(t *testing.T) {
	fx := newFixture(t)
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, ams)
	r, err := fx.b.Build(context.Background(), Request{From: from, To: from.AddDate(1, 0, 0), Sections: []string{SecGas, SecGas, SecCosts}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Bucket != costs.BucketMonth || r.Summary != nil || r.Electricity != nil || r.Gas == nil || r.Costs == nil {
		t.Fatalf("bucket %s, sections %+v", r.Bucket, r)
	}
	if len(r.Gas.Buckets) != 11 || r.Gas.HighestBucket != 9 { // nothing after now (5 November)
		t.Fatalf("gas %d buckets, highest %d", len(r.Gas.Buckets), r.Gas.HighestBucket)
	}
	if _, err := r.Bytes(); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyReport(t *testing.T) {
	fx := newFixture(t)
	fx.b.Devices = func() []Device { return nil }
	from := time.Date(2026, 10, 1, 0, 0, 0, 0, ams)
	r, err := fx.b.Build(context.Background(), Request{From: from, To: from.AddDate(0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	pdf, err := r.Bytes()
	if err != nil || !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatalf("%v", err)
	}
}

func TestCheckRequest(t *testing.T) {
	fx := newFixture(t)
	day := time.Date(2026, 10, 1, 0, 0, 0, 0, ams)
	for _, c := range []struct {
		req Request
		ok  bool
	}{
		{Request{From: day, To: day.AddDate(0, 0, 1)}, true},
		{Request{From: day, To: day}, false},
		{Request{From: day.Add(time.Hour), To: day.AddDate(0, 0, 1)}, false},
		{Request{From: day, To: day.AddDate(4, 0, 0)}, false},
		{Request{From: day.AddDate(1, 0, 0), To: day.AddDate(1, 0, 1)}, false}, // in the future
		{Request{From: day, To: day.AddDate(0, 0, 1), Sections: []string{"nope"}}, false},
		{Request{From: day, To: day.AddDate(0, 0, 1), Title: strings.Repeat("x", 81)}, false},
	} {
		req := c.req
		if err := fx.b.Check(&req); (err == nil) != c.ok {
			t.Errorf("%+v: err = %v", c.req, err)
		}
	}
}

func TestFormat(t *testing.T) {
	for _, c := range []struct{ got, want string }{
		{num(1234.5, 1), "1.234,5"}, {num(-0.04, 1), "0,0"}, {num(-1234567, 0), "-1.234.567"},
		{eur(-12.3), "-€ 12,30"}, {eur(0.004), "€ 0,00"},
		{qty(1234.4, "kWh"), "1.234 kWh"}, {qty(9.876, "m³"), "9,88 m³"},
		{tickText(2.5, 2.5), "2,5"}, {tickText(200, 100), "200"}, {tickText(0.25, 0.05), "0,25"},
	} {
		if c.got != c.want {
			t.Errorf("got %q, want %q", c.got, c.want)
		}
	}
	d := func(y, m, day int) time.Time { return time.Date(y, time.Month(m), day, 0, 0, 0, 0, ams) }
	for _, c := range []struct {
		from, to   time.Time
		text, file string
	}{
		{d(2026, 10, 1), d(2026, 11, 1), "oktober 2026", "rapport-2026-10.pdf"},
		{d(2026, 11, 2), d(2026, 11, 9), "2 t/m 8 november 2026", "rapport-2026-11-02_2026-11-08.pdf"},
		{d(2026, 1, 1), d(2027, 1, 1), "2026", "rapport-2026-01-01_2026-12-31.pdf"},
		{d(2026, 1, 1), d(2026, 11, 1), "januari t/m oktober 2026", "rapport-2026-01-01_2026-10-31.pdf"},
		{d(2026, 10, 30), d(2026, 11, 3), "30 oktober t/m 2 november 2026", "rapport-2026-10-30_2026-11-02.pdf"},
		{d(2026, 10, 5), d(2026, 10, 6), "5 oktober 2026", "rapport-2026-10-05.pdf"},
	} {
		if got := periodText(c.from, c.to); got != c.text {
			t.Errorf("periodText = %q, want %q", got, c.text)
		}
		if got := FileName(c.from, c.to); got != c.file {
			t.Errorf("FileName = %q, want %q", got, c.file)
		}
	}
	if got := cp1252("€ ë – ✓"); got != "\x80 \xeb \x96 ?" {
		t.Errorf("cp1252 = %q", got)
	}
}

func TestPeriod(t *testing.T) {
	at := func(m, d, h, min int) time.Time { return time.Date(2026, time.Month(m), d, h, min, 0, 0, ams) }
	for _, c := range []struct {
		kind     string
		now      time.Time
		from, to string
	}{
		{Monthly, at(11, 2, 7, 59), "2026-09-01", "2026-10-01"},
		{Monthly, at(11, 2, 8, 0), "2026-10-01", "2026-11-01"},
		{Monthly, at(11, 30, 23, 0), "2026-10-01", "2026-11-01"},
		{Weekly, at(11, 9, 7, 59), "2026-10-26", "2026-11-02"}, // Monday 9 November
		{Weekly, at(11, 9, 8, 0), "2026-11-02", "2026-11-09"},
		{Weekly, at(11, 15, 22, 0), "2026-11-02", "2026-11-09"},
	} {
		from, to, due := Period(c.kind, c.now, ams)
		if from.Format("2006-01-02") != c.from || to.Format("2006-01-02") != c.to || due.After(c.now) {
			t.Errorf("%s at %s: %s – %s due %s", c.kind, c.now, from, to, due)
		}
	}
	if next := nextDue(Monthly, at(11, 5, 0, 0), ams); !next.Equal(at(12, 2, 8, 0)) {
		t.Errorf("next monthly %s", next)
	}
	if next := nextDue(Weekly, at(11, 5, 0, 0), ams); !next.Equal(at(11, 9, 8, 0)) {
		t.Errorf("next weekly %s", next)
	}
}

func TestSchedulerSendsOncePerPeriod(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	srv := &mail.SimServer{}
	addr, err := srv.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, ps, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(ps)

	fx.now = time.Date(2026, 10, 20, 12, 0, 0, 0, ams)
	sch := &Scheduler{Store: fx.st, Builder: fx.b, Sender: &mail.Sender{}, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return fx.now }}
	sc, err := sch.Save(ctx, Schedule{Kind: Monthly, Sections: []string{SecSummary, SecCosts}, To: []string{"Ik <ik@example.nl>"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if sc.Name != "Maandrapport" || sc.To[0] != "ik@example.nl" || sc.LastPeriod != "2026-09" || sc.NextAt == nil || !sc.NextAt.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, ams)) {
		t.Fatalf("saved %+v", sc)
	}
	// due, but e-mail is not set up: the error is kept and not retried within the hour
	fx.now = time.Date(2026, 11, 2, 9, 0, 0, 0, ams)
	sch.Check(ctx)
	if got, _ := sch.Get(ctx, sc.ID); got.LastError == "" || got.LastPeriod != "2026-09" {
		t.Fatalf("after failed send %+v", got)
	}
	if err := SaveMailSettings(ctx, fx.st, mail.Settings{Host: host, Port: port, Security: mail.None, From: "gw@example.nl"}); err != nil {
		t.Fatal(err)
	}
	sch.Check(ctx)
	if n := len(srv.Messages()); n != 0 {
		t.Fatalf("retried within the hour: %d", n)
	}
	fx.now = fx.now.Add(time.Hour)
	sch.Check(ctx)
	sch.Check(ctx)
	msgs := srv.Messages()
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	data := string(msgs[0].Data)
	if !strings.Contains(data, "rapport-2026-10.pdf") || !strings.Contains(data, "application/pdf") || msgs[0].To[0] != "ik@example.nl" {
		t.Fatalf("message:\n%.600s", data)
	}
	got, _ := sch.Get(ctx, sc.ID)
	if got.LastPeriod != "2026-10" || got.LastError != "" || got.LastAt == nil {
		t.Fatalf("after send %+v", got)
	}
	// sending now repeats the latest period; a disabled schedule is skipped
	if err := sch.SendNow(ctx, sc.ID); err != nil {
		t.Fatal(err)
	}
	got.Enabled = false
	if _, err := sch.Save(ctx, got); err != nil {
		t.Fatal(err)
	}
	fx.now = time.Date(2026, 12, 2, 9, 0, 0, 0, ams)
	sch.Check(ctx)
	if n := len(srv.Messages()); n != 2 {
		t.Fatalf("%d messages", n)
	}
}

func TestScheduleValidation(t *testing.T) {
	ok := Schedule{Kind: Monthly, Sections: []string{SecSummary}, To: []string{"a@example.nl"}}
	for _, c := range []struct {
		edit func(*Schedule)
		ok   bool
	}{
		{func(*Schedule) {}, true},
		{func(s *Schedule) { s.Kind = "day" }, false},
		{func(s *Schedule) { s.Sections = nil }, false},
		{func(s *Schedule) { s.Sections = []string{"x"} }, false},
		{func(s *Schedule) { s.To = nil }, false},
		{func(s *Schedule) { s.To = []string{"geen adres"} }, false},
		{func(s *Schedule) { s.To = strings.Split(strings.Repeat("a@example.nl,", 11), ",")[:11] }, false},
	} {
		s := ok
		s.Sections = append([]string(nil), ok.Sections...)
		s.To = append([]string(nil), ok.To...)
		c.edit(&s)
		s.Normalize()
		if err := s.Validate(); (err == nil) != c.ok {
			t.Errorf("%+v: err = %v", s, err)
		}
	}
}

// A weekly schedule switched to monthly starts after the month due now; the
// last week's key does not make the coming month count as sent.
func TestScheduleKindChange(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()
	fx.now = time.Date(2026, 11, 1, 12, 0, 0, 0, ams) // Sunday; October due 2 Nov
	sch := &Scheduler{Store: fx.st, Builder: fx.b, Sender: &mail.Sender{}, Log: slog.New(slog.DiscardHandler), Now: func() time.Time { return fx.now }}
	sc, err := sch.Save(ctx, Schedule{Kind: Weekly, Sections: []string{SecSummary}, To: []string{"ik@example.nl"}, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if sc.LastPeriod != "2026-10-19" {
		t.Fatalf("weekly starts at %q", sc.LastPeriod)
	}
	if err := fx.st.SetReportScheduleSent(ctx, sc.ID, "2026-10-26", fx.now, ""); err != nil {
		t.Fatal(err)
	}
	sc, _ = sch.Get(ctx, sc.ID)
	sc.Kind = Monthly
	got, err := sch.Save(ctx, sc)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastPeriod != "2026-09" || got.NextAt == nil || !got.NextAt.Equal(time.Date(2026, 11, 2, 8, 0, 0, 0, ams)) {
		t.Fatalf("after switch: last %q, next %v", got.LastPeriod, got.NextAt)
	}
	// an edit that keeps the kind keeps the last period
	got.Name = "Maand"
	again, err := sch.Save(ctx, got)
	if err != nil || again.LastPeriod != "2026-09" {
		t.Fatalf("edit: %q %v", again.LastPeriod, err)
	}
}
