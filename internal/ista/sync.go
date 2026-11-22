package ista

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// PluginType is the plugin type the accounts are configured under.
const PluginType = "ista"

// Service keeps the stored ista data up to date for every enabled ista
// plugin instance (an account) and answers the usage and cost questions.
//
// Syncing: the first time every day since the move-in is fetched (the
// readings of each radiator at the end of the day, and the building average
// up to that day); after that once a day the account data, the last
// refetchDays days (ista corrects recent readings) and the months. Once a
// week the building averages of the running period are fetched again,
// because ista revises them.
type Service struct {
	Store *store.Store
	Log   *slog.Logger
	// Loc is the time zone of the days (HWLOCAL_TZ).
	Loc *time.Location
	// BaseURL is the portal (DefaultURL when empty).
	BaseURL string
	// Pace is the pause between calls while fetching history (700 ms when
	// zero, none when negative): ista is a shared portal, not an API.
	Pace time.Duration
	// Now is the clock (time.Now when nil).
	Now func() time.Time
	// Notices gets a refused login (optional).
	Notices *notice.Log
	// OnData is called after new data of an address was stored.
	OnData func(cuid string)
	// SyncHour is the local hour from which the daily sync runs (6 when 0).
	SyncHour int
	// Forecast holds the settings of the heating forecast (nil: automatic).
	Forecast *forecast.Service

	mu       sync.Mutex
	accounts map[string]*account
	wake     chan struct{}
}

const (
	refetchDays   = 14
	avgEvery      = 7 * 24 * time.Hour
	retryAfter    = 30 * time.Minute
	closedAvgDays = 90 // building averages of a closed period are refetched this long
)

type account struct {
	id         string
	name       string
	user, pass string
	client     *Client
	status     Status
	retryAt    time.Time
	running    bool
}

// Status is what an account's sync is doing, for the PWA.
type Status struct {
	Account  string     `json:"account"`
	Name     string     `json:"name"`
	State    string     `json:"state"` // idle, syncing, auth_failed, error
	Message  string     `json:"message,omitempty"`
	LastSync *time.Time `json:"last_sync,omitempty"`
	// Done and Todo count the days of the history fetch in progress;
	// RemainingS estimates the seconds it still takes.
	Done       int `json:"done,omitempty"`
	Todo       int `json:"todo,omitempty"`
	RemainingS int `json:"remaining_s,omitempty"`

	started time.Time // of the current fetch of days
}

// syncState is stored per account so a restart neither repeats nor skips.
type syncState struct {
	LastDaily  time.Time `json:"last_daily"`
	LastAvg    time.Time `json:"last_avg"`
	AuthFailed string    `json:"auth_failed,omitempty"` // fingerprint of the refused credentials
	Complete   bool      `json:"complete"`              // the history fetch finished once
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s *Service) loc() *time.Location {
	if s.Loc == nil {
		return time.Local
	}
	return s.Loc
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.Default()
	}
	return s.Log
}

// today is the current calendar day in Loc, as a UTC date.
func (s *Service) today() time.Time {
	n := s.now().In(s.loc())
	return time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC)
}

func fingerprint(user, pass string) string {
	h := sha256.Sum256([]byte(user + "\x00" + pass))
	return hex.EncodeToString(h[:8])
}

// Wake makes Run check the accounts now (after a change in the PWA).
func (s *Service) Wake() {
	s.mu.Lock()
	ch := s.wake
	s.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// Run syncs the accounts until ctx ends.
func (s *Service) Run(ctx context.Context) {
	s.mu.Lock()
	s.wake = make(chan struct{}, 1)
	wake := s.wake
	s.mu.Unlock()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		s.tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-wake:
		}
	}
}

// accountsFromStore returns the enabled ista instances and drops the state
// of the ones that are gone.
func (s *Service) accountsFromStore(ctx context.Context) ([]*account, error) {
	recs, err := s.Store.Plugins(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.accounts == nil {
		s.accounts = map[string]*account{}
	}
	seen := map[string]bool{}
	var out []*account
	for _, r := range recs {
		if r.Type != PluginType || !r.Enabled {
			continue
		}
		seen[r.ID] = true
		a := s.accounts[r.ID]
		user, pass := r.Config["username"], r.Config["password"]
		if a == nil || a.user != user || a.pass != pass {
			// new account or new credentials: a new session, retry at once
			a = &account{id: r.ID, user: user, pass: pass, status: Status{Account: r.ID, State: "idle"}}
			s.accounts[r.ID] = a
		}
		a.name = r.Name
		a.status.Name = r.Name
		a.client = s.client(a)
		out = append(out, a)
	}
	for id := range s.accounts {
		if !seen[id] {
			delete(s.accounts, id)
		}
	}
	return out, nil
}

func (s *Service) client(a *account) *Client {
	if a.client != nil {
		return a.client
	}
	return &Client{BaseURL: s.BaseURL, Username: a.user, Password: a.pass}
}

// SyncDue syncs every account that is due, now (Run does this every
// minute).
func (s *Service) SyncDue(ctx context.Context) { s.tick(ctx) }

func (s *Service) tick(ctx context.Context) {
	accts, err := s.accountsFromStore(ctx)
	if err != nil {
		s.log().Warn("ista: accounts lezen mislukt", "err", err)
		return
	}
	for _, a := range accts {
		if ctx.Err() != nil {
			return
		}
		st := s.loadState(ctx, a.id)
		if !s.due(a, st) {
			continue
		}
		s.runSync(ctx, a, st)
	}
}

func (s *Service) loadState(ctx context.Context, id string) syncState {
	var st syncState
	raw, err := s.Store.IstaSyncState(ctx, id)
	if err == nil && raw != "" {
		_ = json.Unmarshal([]byte(raw), &st)
	}
	return st
}

func (s *Service) saveState(ctx context.Context, id string, st syncState) {
	b, _ := json.Marshal(st)
	if err := s.Store.SaveIstaSyncState(ctx, id, string(b)); err != nil {
		s.log().Warn("ista: status opslaan mislukt", "err", err)
	}
}

func (s *Service) syncHour() int {
	if s.SyncHour == 0 {
		return 6
	}
	return s.SyncHour
}

// due reports whether an account should sync now.
func (s *Service) due(a *account, st syncState) bool {
	if a.user == "" || a.pass == "" {
		return false
	}
	if st.AuthFailed != "" && st.AuthFailed == fingerprint(a.user, a.pass) {
		s.setStatus(a, func(x *Status) {
			x.State = "auth_failed"
			x.Message = "ista weigerde de gebruikersnaam of het wachtwoord"
		})
		return false
	}
	now := s.now()
	if now.Before(a.retryAt) {
		return false
	}
	if !st.Complete || st.LastDaily.IsZero() {
		return true
	}
	local := now.In(s.loc())
	last := st.LastDaily.In(s.loc())
	sameDay := last.Year() == local.Year() && last.YearDay() == local.YearDay()
	return !sameDay && local.Hour() >= s.syncHour()
}

func (s *Service) setStatus(a *account, fn func(*Status)) {
	s.mu.Lock()
	fn(&a.status)
	s.mu.Unlock()
}

// Statuses returns the sync status of every account.
func (s *Service) Statuses(ctx context.Context) []Status {
	if _, err := s.accountsFromStore(ctx); err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Status, 0, len(s.accounts))
	for _, a := range s.accounts {
		st := a.status
		if st.LastSync == nil {
			if ls := s.loadState(ctx, a.id); !ls.LastDaily.IsZero() {
				t := ls.LastDaily
				st.LastSync = &t
			}
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Service) runSync(ctx context.Context, a *account, st syncState) {
	s.mu.Lock()
	if a.running {
		s.mu.Unlock()
		return
	}
	a.running = true
	a.status.State, a.status.Message = "syncing", ""
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		a.running = false
		s.mu.Unlock()
	}()
	err := s.sync(ctx, a, &st)
	switch {
	case err == nil:
		now := s.now()
		st.LastDaily, st.Complete, st.AuthFailed = now, true, ""
		s.saveState(ctx, a.id, st)
		s.setStatus(a, func(x *Status) {
			x.State = "idle"
			x.Message = ""
			x.LastSync = &now
			x.Done, x.Todo, x.RemainingS = 0, 0, 0
		})
	case errors.Is(err, ErrAuth):
		st.AuthFailed = fingerprint(a.user, a.pass)
		s.saveState(ctx, a.id, st)
		s.log().Warn("ista: inloggen geweigerd; pas gebruikersnaam of wachtwoord aan bij de plugin", "account", a.name)
		s.Notices.Add(notice.KindLogin, "ista:"+a.id, "ista weigert het inloggen",
			a.name+": pas de gebruikersnaam of het wachtwoord aan bij de plugin (Instellingen › Plugins).")
		s.setStatus(a, func(x *Status) { x.State = "auth_failed"; x.Message = err.Error() })
	case ctx.Err() != nil:
	default:
		s.saveState(ctx, a.id, st) // keeps LastAvg of a partial run
		a.retryAt = s.now().Add(retryAfter)
		s.log().Warn("ista: bijwerken mislukt, later opnieuw", "account", a.name, "err", err)
		s.setStatus(a, func(x *Status) { x.State = "error"; x.Message = err.Error() })
	}
}

// Discover logs in with the given credentials, stores what the account
// tells about its addresses and returns them with their radiators. It is
// the plugin's scan.
func (s *Service) Discover(ctx context.Context, accountID, user, pass string) ([]store.IstaAddress, map[string][]store.IstaMeter, error) {
	c := &Client{BaseURL: s.BaseURL, Username: user, Password: pass}
	uv, raw, err := c.UserValues(ctx)
	if err != nil {
		return nil, nil, err
	}
	_ = s.Store.SaveIstaRaw(ctx, "UserValues", accountID, raw)
	var addrs []store.IstaAddress
	meters := map[string][]store.IstaMeter{}
	for _, cu := range uv.Cus {
		meta := metaOf(accountID, cu)
		if err := s.Store.SaveIstaMeta(ctx, meta); err != nil {
			return nil, nil, err
		}
		addrs = append(addrs, meta.Address)
		meters[cu.Cuid] = meta.Meters
	}
	// working credentials clear an earlier refusal
	if st := s.loadState(ctx, accountID); st.AuthFailed != "" {
		st.AuthFailed = ""
		s.saveState(ctx, accountID, st)
	}
	s.Wake()
	return addrs, meters, nil
}

// metaOf turns a UserValues address into what the store keeps.
func metaOf(accountID string, cu Customer) store.IstaMeta {
	m := store.IstaMeta{Address: store.IstaAddress{Cuid: cu.Cuid, Account: accountID, Address: cu.Adress, Zip: cu.Zip, City: cu.City}}
	if d, err := ParseDate(cu.DateStart); err == nil {
		m.Address.MoveIn = d.Format(DayLayout)
	}
	for _, p := range cu.CurConsumption.BillingPeriods {
		start, err1 := p.Start()
		end, err2 := p.End()
		if err1 != nil || err2 != nil {
			continue
		}
		m.Periods = append(m.Periods, store.IstaPeriod{Cuid: cu.Cuid, Y: p.Y, Start: start.Format(DayLayout), End: end.Format(DayLayout), TA: p.TA})
	}
	for _, v := range cu.CurConsumption.Billingservices {
		m.Services = append(m.Services, store.IstaService{Cuid: cu.Cuid, ServiceID: v.ID, Description: v.Description, MeterType: v.MeterType, Unit: v.Unit})
	}
	seen := map[int64]bool{}
	for _, sc := range cu.CurConsumption.ServicesComp {
		for _, mt := range append(append([]Meter{}, sc.CurMeters...), sc.CompMeters...) {
			if seen[mt.MeterID] {
				continue
			}
			seen[mt.MeterID] = true
			sid := mt.ServiceID
			if sid == 0 {
				sid = sc.ID
			}
			m.Meters = append(m.Meters, store.IstaMeter{MeterID: mt.MeterID, Cuid: cu.Cuid, ServiceID: sid, RadNr: mt.RadNr,
				Position: mt.Position, MeterNr: mt.MeterNr, ArtNr: mt.ArtNr})
		}
	}
	return m
}

// sync brings one account's data up to date.
func (s *Service) sync(ctx context.Context, a *account, st *syncState) error {
	c := a.client
	uv, raw, err := c.UserValues(ctx)
	if err != nil {
		return err
	}
	_ = s.Store.SaveIstaRaw(ctx, "UserValues", a.id, raw)
	refreshAvg := s.now().Sub(st.LastAvg) >= avgEvery
	for _, cu := range uv.Cus {
		meta := metaOf(a.id, cu)
		if err := s.Store.SaveIstaMeta(ctx, meta); err != nil {
			return err
		}
		// the months first (one call): an overview at once, before the
		// history of days, which may take half an hour the first time
		if err := s.syncMonths(ctx, a, cu.Cuid); err != nil {
			// months are a nice-to-have; the days are the data
			s.log().Info("ista: maandwaarden niet opgehaald", "err", err)
		} else if s.OnData != nil {
			s.OnData(cu.Cuid)
		}
		if err := s.syncDays(ctx, a, cu, meta); err != nil {
			return err
		}
		if refreshAvg {
			if err := s.refreshAverages(ctx, a, cu, meta); err != nil {
				return err
			}
		}
		if s.OnData != nil {
			s.OnData(cu.Cuid)
		}
	}
	if refreshAvg {
		st.LastAvg = s.now()
	}
	return nil
}

// window is the part of a period this address lived in, up to today.
func (s *Service) window(p BillingPeriod, moveIn string) (from, to time.Time, ok bool) {
	start, err1 := p.Start()
	end, err2 := p.End()
	if err1 != nil || err2 != nil {
		return
	}
	from, to = start, end
	if mi, err := time.Parse(DayLayout, moveIn); err == nil && mi.After(from) {
		from = mi
	}
	if today := s.today(); to.After(today) {
		to = today
	}
	return from, to, !to.Before(from)
}

func (s *Service) pause(ctx context.Context) error {
	d := s.Pace
	if d == 0 {
		d = 700 * time.Millisecond
	}
	if d < 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// syncDays fetches every day of every period that is not stored yet, is a
// reading in progress, or is recent enough that ista may still correct it.
// Newest first: the running period is usable soon, older history follows;
// what is stored stays, so a restart goes on where it was.
func (s *Service) syncDays(ctx context.Context, a *account, cu Customer, meta store.IstaMeta) error {
	type job struct {
		p    BillingPeriod
		from time.Time
		day  time.Time
	}
	var jobs []job
	recent := s.today().AddDate(0, 0, -refetchDays)
	periods := append([]BillingPeriod(nil), cu.CurConsumption.BillingPeriods...)
	sort.Slice(periods, func(i, j int) bool { return periods[i].Y > periods[j].Y })
	for _, p := range periods {
		from, to, ok := s.window(p, meta.Address.MoveIn)
		if !ok {
			continue
		}
		stored, err := s.Store.IstaDays(ctx, cu.Cuid, from.Format(DayLayout), to.Format(DayLayout))
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, d := range stored {
			have[d.Day] = !d.Estimated && d.BuildingAvg != nil
		}
		for d := to; !d.Before(from); d = d.AddDate(0, 0, -1) {
			if !have[d.Format(DayLayout)] || !d.Before(recent) {
				jobs = append(jobs, job{p, from, d})
			}
		}
	}
	s.setStatus(a, func(x *Status) { x.Done, x.Todo, x.started = 0, len(jobs), time.Now() })
	if len(jobs) > refetchDays+2 {
		s.log().Info("ista: historie ophalen", "account", a.name, "dagen", len(jobs))
	}
	for i, j := range jobs {
		if err := s.fetchDay(ctx, a.client, cu.Cuid, j.p, j.from, j.day); err != nil {
			return fmt.Errorf("%s: %w", j.day.Format(DayLayout), err)
		}
		s.setStatus(a, func(x *Status) {
			x.Done = i + 1
			if el := time.Since(x.started); x.Done >= 3 {
				x.RemainingS = int(el.Seconds() / float64(x.Done) * float64(x.Todo-x.Done))
			}
		})
		if err := s.pause(ctx); err != nil {
			return err
		}
	}
	return nil
}

// fetchDay stores the readings at the end of day and the building average
// from the start of the address's window in the period up to day.
func (s *Service) fetchDay(ctx context.Context, c *Client, cuid string, p BillingPeriod, from, day time.Time) error {
	cv, raw, err := c.Consumption(ctx, cuid, p, day)
	if err != nil {
		return err
	}
	ds := day.Format(DayLayout)
	_ = s.Store.SaveIstaRaw(ctx, "ConsumptionValues", cuid+"/"+ds, raw)
	if err := s.pause(ctx); err != nil {
		return err
	}
	av, rawAvg, err := c.Averages(ctx, cuid, from, day)
	if err != nil {
		return err
	}
	_ = s.Store.SaveIstaRaw(ctx, "ConsumptionAverages", cuid+"/"+from.Format(DayLayout)+"/"+ds, rawAvg)
	estimated := day.Equal(s.today())
	d := store.IstaDay{Cuid: cuid, Day: ds, Y: p.Y}
	var readings []store.IstaReading
	var mps []store.IstaMeterPeriod
	for _, sc := range cv.ServicesComp {
		d.TotalNow += sc.TotalNow
		for _, m := range sc.CurMeters {
			est := estimated || m.CValue != math.Trunc(m.CValue)
			if est {
				d.Estimated = true
			}
			readings = append(readings, store.IstaReading{MeterID: m.MeterID, Day: ds, Y: p.Y, C: m.CValue, CC: m.CCValue,
				CCD: m.CCDValue, End: m.EndValue, Estimated: est})
			mps = append(mps, store.IstaMeterPeriod{MeterID: m.MeterID, Y: p.Y, CalcFactor: m.CalcFactor, Reduction: m.Reduction,
				Multiply: m.Multiply, TransferLoss: m.TransferLoss, BeginValue: m.BeginValue})
		}
	}
	d.Estimated = d.Estimated || estimated
	for _, x := range av.Averages {
		v := x.NormalizedValue
		if d.BuildingAvg == nil {
			d.BuildingAvg = &v
		} else {
			*d.BuildingAvg += v
		}
	}
	return s.Store.SaveIstaDay(ctx, d, readings, mps)
}

// refreshAverages fetches the building averages again for the running
// period and periods that ended recently: ista revises them afterwards.
func (s *Service) refreshAverages(ctx context.Context, a *account, cu Customer, meta store.IstaMeta) error {
	limit := s.today().AddDate(0, 0, -closedAvgDays)
	for _, p := range cu.CurConsumption.BillingPeriods {
		from, to, ok := s.window(p, meta.Address.MoveIn)
		end, err := p.End()
		if !ok || err != nil || end.Before(limit) {
			continue
		}
		days, err := s.Store.IstaDays(ctx, cu.Cuid, from.Format(DayLayout), to.Format(DayLayout))
		if err != nil {
			return err
		}
		for _, d := range days {
			if d.AvgFetchedAt != nil && s.now().Sub(*d.AvgFetchedAt) < 24*time.Hour {
				continue // fetched with the day itself
			}
			day, _ := time.Parse(DayLayout, d.Day)
			av, _, err := a.client.Averages(ctx, cu.Cuid, from, day)
			if err != nil {
				return err
			}
			total := 0.0
			for _, x := range av.Averages {
				total += x.NormalizedValue
			}
			if err := s.Store.SetIstaBuildingAvg(ctx, cu.Cuid, d.Day, total); err != nil {
				return err
			}
			if err := s.pause(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) syncMonths(ctx context.Context, a *account, cuid string) error {
	mv, raw, _, err := a.client.MonthValues(ctx, cuid)
	if err != nil {
		return err
	}
	_ = s.Store.SaveIstaRaw(ctx, "MonthValues", cuid, raw)
	var months []store.IstaMonth
	for _, m := range mv.Months {
		for _, sc := range m.Services {
			months = append(months, store.IstaMonth{Cuid: cuid, ServiceID: sc.ServiceID, Year: m.Year, Month: m.Month,
				Total: sc.TotalConsumption, BuildingAvg: sc.BuldingAverage, Approx: sc.HasApproximation, AvgTemp: m.AverageTemp})
		}
	}
	return s.Store.SaveIstaMonths(ctx, months)
}
