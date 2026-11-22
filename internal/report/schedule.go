package report

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/mail"
	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Schedule kinds.
const (
	Monthly = "month" // the previous calendar month, on the 2nd at 08:00
	Weekly  = "week"  // the previous ISO week, on Monday at 08:00
)

// sendHour is when a report is due on its day: late enough that the last
// day's quarter hours and prices are complete.
const sendHour = 8

// MaxRecipients limits the addresses of one schedule.
const MaxRecipients = 10

// Schedule is a report that is mailed every month or week.
type Schedule struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
	// Range is what the report covers, up to the end of the period that is
	// due: "" the period itself (the month or week), RangeContractYear from
	// the start of the contract year, RangeYear from 1 January.
	Range    string   `json:"range,omitempty"`
	Sections []string `json:"sections"`
	To       []string `json:"to"`
	Title    string   `json:"title,omitempty"`
	Enabled  bool     `json:"enabled"`

	// set by the scheduler, ignored when saving
	LastPeriod string     `json:"last_period,omitempty"`
	LastAt     *time.Time `json:"last_at,omitempty"`
	LastError  string     `json:"last_error,omitempty"`
	NextAt     *time.Time `json:"next_at,omitempty"`
	// WaitUntil: the period is due but the report waits for ista to have
	// measured it (heating), at most until then. Only a report with
	// nothing but heating (and the forecast) waits; any other is sent on
	// time without heating, which then follows in a report of its own.
	WaitUntil *time.Time `json:"wait_until,omitempty"`
	// HeatingPeriod: the period that was sent without heating; its heating
	// report follows once ista has measured it, at the latest at
	// HeatingUntil. HeatingAt and HeatingError are its last attempt.
	HeatingPeriod string     `json:"heating_period,omitempty"`
	HeatingUntil  *time.Time `json:"heating_until,omitempty"`
	HeatingAt     *time.Time `json:"heating_at,omitempty"`
	HeatingError  string     `json:"heating_error,omitempty"`
}

// sendMode says what a scheduled send contains.
type sendMode int

const (
	sendAll            sendMode = iota // every section
	sendWithoutHeating                 // on time, heating follows
	sendHeatingOnly                    // the heating that followed
)

// heating reports whether the schedule includes the heating section.
func (s *Schedule) heating() bool {
	for _, sec := range s.Sections {
		if sec == SecHeating {
			return true
		}
	}
	return false
}

// heatingOnly reports whether the schedule has nothing to send on time
// without heating: only heating, perhaps with the forecast.
func (s *Schedule) heatingOnly() bool {
	for _, sec := range s.Sections {
		if sec != SecHeating && sec != SecAdvance {
			return false
		}
	}
	return true
}

// Period returns the period a schedule of kind reports on when it is due at
// or before now: the last whole month (week) whose report time has passed.
func Period(kind string, now time.Time, loc *time.Location) (from, to, due time.Time) {
	now = now.In(loc)
	switch kind {
	case Weekly:
		from, to = WeekPeriod(now, loc)
		from, to = from.AddDate(0, 0, -7), from
		if due = to.Add(sendHour * time.Hour); due.After(now) {
			from, to = from.AddDate(0, 0, -7), from
			due = to.Add(sendHour * time.Hour)
		}
	default:
		from, to = MonthPeriod(now, loc)
		from, to = from.AddDate(0, -1, 0), from
		if due = to.AddDate(0, 0, 1).Add(sendHour * time.Hour); due.After(now) {
			from, to = from.AddDate(0, -1, 0), from
			due = to.AddDate(0, 0, 1).Add(sendHour * time.Hour)
		}
	}
	return from, to, due
}

// nextDue returns when the period after the one due at now is due.
func nextDue(kind string, now time.Time, loc *time.Location) time.Time {
	_, to, _ := Period(kind, now, loc)
	if kind == Weekly {
		return to.AddDate(0, 0, 7).Add(sendHour * time.Hour)
	}
	return to.AddDate(0, 1, 1).Add(sendHour * time.Hour)
}

// periodOf returns the period of a key made by periodKey and when it was
// due.
func periodOf(kind, key string, loc *time.Location) (from, to, due time.Time, ok bool) {
	if kind == Weekly {
		d, err := time.ParseInLocation("2006-01-02", key, loc)
		if err != nil {
			return from, to, due, false
		}
		return d, d.AddDate(0, 0, 7), d.AddDate(0, 0, 7).Add(sendHour * time.Hour), true
	}
	m, err := time.ParseInLocation("2006-01", key, loc)
	if err != nil {
		return from, to, due, false
	}
	to = m.AddDate(0, 1, 0)
	return m, to, to.AddDate(0, 0, 1).Add(sendHour * time.Hour), true
}

// periodKey identifies a period: "2026-10" for a month, the Monday for a
// week. Keys of one kind sort in time order.
func periodKey(kind string, from time.Time) string {
	if kind == Weekly {
		return from.Format("2006-01-02")
	}
	return from.Format("2006-01")
}

// Normalize trims the schedule and fills defaults.
func (s *Schedule) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	s.Title = strings.TrimSpace(s.Title)
	s.Kind = strings.TrimSpace(s.Kind)
	s.Range = strings.TrimSpace(s.Range)
	if s.Kind == "" {
		s.Kind = Monthly
	}
	if s.Name == "" {
		s.Name = "Maandrapport"
		if s.Kind == Weekly {
			s.Name = "Weekrapport"
		}
	}
	var to []string
	for _, t := range s.To {
		if t = strings.TrimSpace(t); t != "" {
			to = append(to, t)
		}
	}
	s.To = to
	s.LastPeriod, s.LastAt, s.LastError, s.NextAt, s.WaitUntil = "", nil, "", nil, nil
	s.HeatingPeriod, s.HeatingUntil, s.HeatingAt, s.HeatingError = "", nil, nil, ""
}

// Validate checks a normalized schedule.
func (s *Schedule) Validate() error {
	if len([]rune(s.Name)) > 60 {
		return fmt.Errorf("%w: naam te lang", ErrInvalid)
	}
	if len([]rune(s.Title)) > 80 {
		return fmt.Errorf("%w: titel te lang", ErrInvalid)
	}
	if s.Kind != Monthly && s.Kind != Weekly {
		return fmt.Errorf("%w: soort %q (month of week)", ErrInvalid, s.Kind)
	}
	switch s.Range {
	case "", RangeContractYear, RangeYear:
	default:
		return fmt.Errorf("%w: periode %q (leeg, contract_year of year)", ErrInvalid, s.Range)
	}
	if len(s.Sections) == 0 {
		return fmt.Errorf("%w: kies minstens één onderdeel", ErrInvalid)
	}
	for _, sec := range s.Sections {
		if !ValidSection(sec) {
			return fmt.Errorf("%w: onbekend onderdeel %q", ErrInvalid, sec)
		}
	}
	if len(s.To) == 0 {
		return fmt.Errorf("%w: geen ontvangers", ErrInvalid)
	}
	if len(s.To) > MaxRecipients {
		return fmt.Errorf("%w: hoogstens %d ontvangers", ErrInvalid, MaxRecipients)
	}
	for i, t := range s.To {
		a, err := mail.ParseAddress(t)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		s.To[i] = a.Address
	}
	return nil
}

// MailSettings reads the e-mail settings from the store.
func MailSettings(ctx context.Context, st *store.Store) (mail.Settings, error) {
	cfg, err := st.MailConfig(ctx)
	if err != nil {
		return mail.Settings{}, err
	}
	port, _ := strconv.Atoi(cfg["port"])
	return mail.Settings{
		Host: cfg["host"], Port: port, Security: cfg["security"],
		User: cfg["user"], Password: cfg["password"], From: cfg["from"],
	}, nil
}

// SaveMailSettings stores the e-mail settings (validated); empty settings
// remove them.
func SaveMailSettings(ctx context.Context, st *store.Store, s mail.Settings) error {
	s.Normalize()
	if err := s.Validate(); err != nil {
		return err
	}
	if s.Host == "" {
		return st.SaveMailConfig(ctx, nil)
	}
	return st.SaveMailConfig(ctx, map[string]string{
		"host": s.Host, "port": strconv.Itoa(s.Port), "security": s.Security,
		"user": s.User, "password": s.Password, "from": s.From,
	})
}

// Scheduler sends the scheduled reports.
type Scheduler struct {
	Store   *store.Store
	Builder *Builder
	Sender  *mail.Sender
	Log     *slog.Logger
	Now     func() time.Time
	// Notices gets a report that could not be sent (optional).
	Notices *notice.Log
	// Every is how often the schedules are checked (default 5 minutes).
	Every time.Duration

	mu sync.Mutex // one send at a time
}

// ErrNoMail is returned when e-mail is not set up.
var ErrNoMail = errors.New("e-mail is niet ingesteld (Instellingen › E-mail)")

func (s *Scheduler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Scheduler) loc() *time.Location { return s.Builder.loc() }

func decode(st store.StoredSchedule) (Schedule, error) {
	var sc Schedule
	if err := json.Unmarshal(st.Data, &sc); err != nil {
		return sc, err
	}
	sc.ID = st.ID
	sc.LastPeriod, sc.LastError = st.LastPeriod, st.LastError
	if !st.LastAt.IsZero() {
		t := st.LastAt
		sc.LastAt = &t
	}
	sc.HeatingPeriod, sc.HeatingError = st.HeatingPeriod, st.HeatingError
	if !st.HeatingAt.IsZero() {
		t := st.HeatingAt
		sc.HeatingAt = &t
	}
	return sc, nil
}

// List returns the schedules with when each is next sent.
func (s *Scheduler) List(ctx context.Context) ([]Schedule, error) {
	stored, err := s.Store.ReportSchedules(ctx)
	if err != nil {
		return nil, err
	}
	out := []Schedule{}
	now := s.now()
	for _, st := range stored {
		sc, err := decode(st)
		if err != nil {
			continue
		}
		if sc.Enabled {
			next := nextDue(sc.Kind, now, s.loc())
			if from, to, due := Period(sc.Kind, now, s.loc()); periodKey(sc.Kind, from) > sc.LastPeriod {
				next = due // due now: sent at the next check
				if next.Before(now) {
					next = now
				}
				if until, wait := s.waiting(ctx, sc, from, to, due, now); wait && sc.heatingOnly() {
					sc.WaitUntil = &until
				}
			}
			sc.NextAt = &next
			if _, _, due, ok := periodOf(sc.Kind, sc.HeatingPeriod, s.loc()); ok {
				until := due.Add(HeatingWait)
				sc.HeatingUntil = &until
			}
		}
		out = append(out, sc)
	}
	return out, nil
}

// Get returns one schedule.
func (s *Scheduler) Get(ctx context.Context, id int64) (Schedule, error) {
	list, err := s.List(ctx)
	if err != nil {
		return Schedule{}, err
	}
	for _, sc := range list {
		if sc.ID == id {
			return sc, nil
		}
	}
	return Schedule{}, store.ErrNotFound
}

// Save validates and stores a schedule (ID 0 = new). A new schedule starts
// after the period that is due now: it sends the next one.
func (s *Scheduler) Save(ctx context.Context, sc Schedule) (Schedule, error) {
	sc.Normalize()
	if err := sc.Validate(); err != nil {
		return sc, err
	}
	id := sc.ID
	sc.ID = 0
	data, err := json.Marshal(sc)
	if err != nil {
		return sc, err
	}
	// a new schedule, or one that changed between week and month (other
	// period keys), starts after the period that is due now
	from, _, _ := Period(sc.Kind, s.now(), s.loc())
	start := periodKey(sc.Kind, from)
	if id != 0 {
		cur, err := s.Get(ctx, id)
		if err != nil {
			return sc, err
		}
		if cur.Kind == sc.Kind {
			start = "" // keep the last sent period
		}
	}
	if id, err = s.Store.SaveReportSchedule(ctx, id, data, start); err != nil {
		return sc, err
	}
	return s.Get(ctx, id)
}

// Delete removes a schedule.
func (s *Scheduler) Delete(ctx context.Context, id int64) error {
	return s.Store.DeleteReportSchedule(ctx, id)
}

// Run checks the schedules until ctx ends.
func (s *Scheduler) Run(ctx context.Context) {
	every := s.Every
	if every <= 0 {
		every = 5 * time.Minute
	}
	t := time.NewTimer(time.Minute) // first check after the gateway has settled
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s.Check(ctx)
		t.Reset(every)
	}
}

// Check sends every enabled schedule whose period is due and not sent yet.
// Only the latest period is sent: after a long stop older ones are skipped.
// A failed send is retried at the next check, at most once an hour. While
// ista has not measured the period, a report with heating and other
// sections goes on time without heating, and the heating follows (see
// followUp); one with only heating waits.
func (s *Scheduler) Check(ctx context.Context) {
	stored, err := s.Store.ReportSchedules(ctx)
	if err != nil {
		s.Log.Warn("rapporten: planning niet gelezen", "err", err)
		return
	}
	now := s.now()
	for _, st := range stored {
		sc, err := decode(st)
		if err != nil || !sc.Enabled {
			continue
		}
		s.followUp(ctx, sc, now)
		from, to, due := Period(sc.Kind, now, s.loc())
		if periodKey(sc.Kind, from) <= sc.LastPeriod {
			continue
		}
		mode := sendAll
		if _, wait := s.waiting(ctx, sc, from, to, due, now); wait {
			if sc.heatingOnly() {
				continue // ista has not measured the whole period yet
			}
			mode = sendWithoutHeating
		}
		if sc.LastError != "" && sc.LastAt != nil && now.Sub(*sc.LastAt) < time.Hour {
			continue
		}
		if err := s.send(ctx, sc, from, to, mode); err != nil {
			s.Log.Warn("rapport niet verstuurd", "schedule", sc.Name, "period", periodKey(sc.Kind, from), "err", err)
			s.Notices.Add(notice.KindReport, sc.Name, "Rapport niet verstuurd",
				fmt.Sprintf("%s (%s): %v. De gateway probeert het elk uur opnieuw.", sc.Name, periodKey(sc.Kind, from), err))
		}
	}
}

// followUp sends the heating of a period that was sent without it, once
// ista has measured the period or at the latest HeatingWait after it was
// due (then with what ista has). A failed send is retried at most once an
// hour; a schedule that no longer has heating drops it.
func (s *Scheduler) followUp(ctx context.Context, sc Schedule, now time.Time) {
	if sc.HeatingPeriod == "" {
		return
	}
	from, to, due, ok := periodOf(sc.Kind, sc.HeatingPeriod, s.loc())
	if !ok || !sc.heating() {
		if err := s.Store.SetReportHeatingSent(ctx, sc.ID, now, ""); err != nil {
			s.Log.Warn("rapporten: verwarming niet bijgewerkt", "schedule", sc.Name, "err", err)
		}
		return
	}
	if _, wait := s.pending(ctx, from, to, due, now); wait {
		return
	}
	if sc.HeatingError != "" && sc.HeatingAt != nil && now.Sub(*sc.HeatingAt) < time.Hour {
		return
	}
	if err := s.send(ctx, sc, from, to, sendHeatingOnly); err != nil {
		s.Log.Warn("verwarming bij rapport niet verstuurd", "schedule", sc.Name, "period", sc.HeatingPeriod, "err", err)
		s.Notices.Add(notice.KindReport, sc.Name, "Verwarmingsrapport niet verstuurd",
			fmt.Sprintf("%s (%s): %v. De gateway probeert het elk uur opnieuw.", sc.Name, sc.HeatingPeriod, err))
	}
}

// Ranges of a report up to a moment.
const (
	RangeContractYear = "contract_year" // from the start of the contract year (settlement period)
	RangeYear         = "year"          // from 1 January
)

// RangeStart returns where a report of rng that ends at to (a local
// midnight) starts: from itself for "", 1 January of the last day's year
// for RangeYear, the start of the contract year that contains the last day
// for RangeContractYear (1 January when no contract applies then).
func (b *Builder) RangeStart(ctx context.Context, rng string, from, to time.Time) (time.Time, error) {
	loc := b.loc()
	last := to.In(loc).AddDate(0, 0, -1)
	jan1 := time.Date(last.Year(), 1, 1, 0, 0, 0, 0, loc)
	switch rng {
	case RangeYear:
		return jan1, nil
	case RangeContractYear:
		if b.Costs == nil || b.Costs.Contracts == nil {
			return jan1, nil
		}
		tl, err := b.Costs.Contracts.List(ctx)
		if err != nil {
			return time.Time{}, err
		}
		if start, _, ok := tl.SettlementPeriodAt(last, loc); ok {
			return start, nil
		}
		return jan1, nil
	}
	return from, nil
}

// waiting reports whether the heating of a due report waits for ista to
// have measured [from, to), and until when it waits at most.
func (s *Scheduler) waiting(ctx context.Context, sc Schedule, from, to, due, now time.Time) (time.Time, bool) {
	if !sc.heating() {
		return time.Time{}, false
	}
	return s.pending(ctx, from, to, due, now)
}

// pending reports whether ista has not measured [from, to) yet and the
// heating of the report due then still waits for it, and until when.
func (s *Scheduler) pending(ctx context.Context, from, to, due, now time.Time) (time.Time, bool) {
	until := due.Add(HeatingWait)
	if !now.Before(until) {
		return until, false
	}
	pending, err := s.Builder.HeatingPending(ctx, from, to)
	if err != nil {
		s.Log.Warn("rapporten: ista niet gelezen", "err", err)
		return until, false
	}
	return until, pending
}

// SendNow sends a schedule's latest due period at once (also when it was
// sent already or the schedule is off).
func (s *Scheduler) SendNow(ctx context.Context, id int64) error {
	sc, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	from, to, _ := Period(sc.Kind, s.now(), s.loc())
	return s.send(ctx, sc, from, to, sendAll)
}

func (s *Scheduler) send(ctx context.Context, sc Schedule, from, to time.Time, mode sendMode) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.deliver(ctx, sc, from, to, mode)
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	key, at, sctx := periodKey(sc.Kind, from), s.now(), context.WithoutCancel(ctx)
	var serr error
	switch {
	case mode == sendHeatingOnly:
		serr = s.Store.SetReportHeatingSent(sctx, sc.ID, at, msg)
	case mode == sendWithoutHeating && err == nil:
		serr = s.Store.SetReportScheduleSentWithoutHeating(sctx, sc.ID, key, at)
	default:
		serr = s.Store.SetReportScheduleSent(sctx, sc.ID, key, at, msg)
	}
	if serr != nil && err == nil {
		err = serr
	}
	if err == nil {
		what := "rapport verstuurd"
		switch mode {
		case sendWithoutHeating:
			what = "rapport verstuurd, verwarming volgt"
		case sendHeatingOnly:
			what = "verwarming bij rapport verstuurd"
		}
		s.Log.Info(what, "schedule", sc.Name, "period", key, "to", len(sc.To))
	}
	return err
}

func (s *Scheduler) deliver(ctx context.Context, sc Schedule, from, to time.Time, mode sendMode) error {
	settings, err := MailSettings(ctx, s.Store)
	if err != nil {
		return err
	}
	if !settings.Configured() {
		return ErrNoMail
	}
	if from, err = s.Builder.RangeStart(ctx, sc.Range, from, to); err != nil {
		return err
	}
	req := Request{From: from, To: to, Sections: sc.Sections, Title: sc.Title, DeferHeating: mode == sendWithoutHeating}
	if mode == sendHeatingOnly {
		req.Sections = []string{SecHeating}
	}
	r, err := s.Builder.Build(ctx, req)
	if err != nil {
		return err
	}
	r.HeatingFollowUp = mode == sendHeatingOnly
	pdf, err := r.Bytes()
	if err != nil {
		return err
	}
	text, htmlBody := r.MailBody(sc.Name)
	subject := r.Title + " " + periodText(r.From, r.To)
	if r.HeatingFollowUp {
		subject += " – verwarming"
	}
	return s.Sender.Send(ctx, settings, &mail.Message{
		To:          sc.To,
		Subject:     subject,
		Text:        text,
		HTML:        htmlBody,
		Attachments: []mail.Attachment{{Name: FileName(r.From, r.To), ContentType: "application/pdf", Data: pdf}},
	})
}

// TestMail sends a short message to check the settings.
func TestMail(ctx context.Context, snd *mail.Sender, settings mail.Settings, to string) error {
	if !settings.Configured() {
		return ErrNoMail
	}
	return snd.Send(ctx, settings, &mail.Message{
		To:      []string{to},
		Subject: "Testmail van Home Energy Watch",
		Text:    "Deze testmail laat zien dat de e-mailinstellingen van Home Energy Watch werken. Rapporten komen op dezelfde manier binnen, met de PDF als bijlage.\n",
	})
}

// MailBody returns the text and HTML of the mail that carries the report:
// the key figures, with the PDF attached.
func (r *Report) MailBody(schedule string) (string, string) {
	var rows [][2]string
	if s := r.Summary; s != nil {
		if e := s.Energy; e != nil {
			rows = append(rows, [2]string{"Stroom afgenomen", qty(e.ImportKWh, "kWh")})
			if e.ExportKWh > 0 {
				rows = append(rows, [2]string{"Stroom teruggeleverd", qty(e.ExportKWh, "kWh")})
			}
			if e.GasM3 > 0 {
				rows = append(rows, [2]string{"Gas", qty(e.GasM3, "m³")})
			}
			if e.Missing.ContractDays < r.Days() {
				rows = append(rows, [2]string{"Kosten stroom en gas", eur(e.TotalEUR)})
			}
		}
		if w := s.Water; w != nil {
			rows = append(rows, [2]string{"Water", qty(w.WaterM3, "m³")})
			if w.Missing.ContractDays < r.Days() {
				rows = append(rows, [2]string{"Kosten water", eur(w.TotalEUR)})
			}
		}
	}
	var notes []string
	if r.HeatingFollowUp {
		notes = append(notes, "Dit is het overzicht verwarming dat bij het eerdere rapport over deze periode nog ontbrak, omdat ista toen nog niet alles gemeten had.")
	}
	if r.HeatingDeferred {
		notes = append(notes, "Verwarming: "+HeatingFollowsNote)
	}
	if h := r.Heating; h != nil {
		for _, a := range h.Addresses {
			if t := a.Usage.Total.Total; t != nil {
				rows = append(rows, [2]string{"Verwarming", num(*t, 0) + " " + unitOf(a.Unit)})
			}
			if a.Costs != nil && a.Costs.Total.Total != nil {
				rows = append(rows, [2]string{"Kosten verwarming", eur(*a.Costs.Total.Total)})
			}
			if !a.Complete {
				n := "Let op: ista heeft deze periode nog niet compleet gemeten"
				if !a.LastMeasured.IsZero() {
					n += " (gemeten t/m " + dateLong(a.LastMeasured) + ")"
				}
				notes = append(notes, n+". De verwarming kan nog veranderen; de rest van het rapport is compleet.")
			}
		}
	}
	period := periodText(r.From, r.To)
	var t strings.Builder
	fmt.Fprintf(&t, "%s %s\n\n", r.Title, period)
	for _, row := range rows {
		fmt.Fprintf(&t, "%-22s %s\n", row[0]+":", row[1])
	}
	if len(rows) > 0 {
		t.WriteString("\n")
	}
	for _, n := range notes {
		t.WriteString(n + "\n\n")
	}
	t.WriteString("Het volledige rapport zit als PDF in de bijlage.\n\n")
	fmt.Fprintf(&t, "-- \nVerstuurd door Home Energy Watch (%s). Aanpassen of uitzetten: Instellingen › Rapporten.\n", schedule)

	var h strings.Builder
	h.WriteString(`<div style="font-family:Segoe UI,Roboto,Helvetica,Arial,sans-serif;color:#0b0b0b;font-size:14px">`)
	fmt.Fprintf(&h, `<h2 style="margin:0 0 4px">%s</h2><p style="margin:0 0 16px;color:#52514e">%s</p>`, html.EscapeString(r.Title), html.EscapeString(period))
	if len(rows) > 0 {
		h.WriteString(`<table style="border-collapse:collapse;margin-bottom:16px">`)
		for _, row := range rows {
			fmt.Fprintf(&h, `<tr><td style="padding:4px 24px 4px 0;color:#52514e">%s</td><td style="padding:4px 0;font-weight:600;text-align:right">%s</td></tr>`,
				html.EscapeString(row[0]), html.EscapeString(row[1]))
		}
		h.WriteString(`</table>`)
	}
	for _, n := range notes {
		fmt.Fprintf(&h, `<p style="color:#8a5a00">%s</p>`, html.EscapeString(n))
	}
	h.WriteString(`<p>Het volledige rapport zit als PDF in de bijlage.</p>`)
	fmt.Fprintf(&h, `<p style="color:#898781;font-size:12px">Verstuurd door Home Energy Watch (%s). Aanpassen of uitzetten: Instellingen › Rapporten.</p></div>`, html.EscapeString(schedule))
	return t.String(), h.String()
}
