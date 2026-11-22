package prices

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Service keeps the prices of one commodity up to date.
type Service struct {
	Store     *store.Store
	Commodity Commodity // default Electricity
	Sources   []Source  // tried in order; change with SetSources once running
	Log       *slog.Logger
	// BackfillFrom is the first delivery day to keep complete: missing days
	// from there up to yesterday are fetched on start and once a day.
	BackfillFrom time.Time
	// Pause between fetches during the backfill (default 2s).
	Pause time.Duration
	// OnSaved is called with the start of the earliest stored period after
	// prices were stored.
	OnSaved func(from time.Time)

	mu        sync.Mutex
	lastError string
	lastFetch time.Time
}

// Status describes the price service for the PWA.
type Status struct {
	Sources   []string   `json:"sources"`
	Unit      string     `json:"unit"`
	LastFetch *time.Time `json:"last_fetch,omitempty"`
	LastError string     `json:"last_error,omitempty"`
}

// SetSources replaces the sources while the service runs, e.g. after the
// ENTSO-E token was changed.
func (s *Service) SetSources(list []Source) {
	s.mu.Lock()
	s.Sources = list
	s.mu.Unlock()
}

func (s *Service) sources() []Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Sources
}

func (s *Service) commodity() Commodity {
	if s.Commodity.Market == "" {
		return Electricity
	}
	return s.Commodity
}

// Status returns the current status.
func (s *Service) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Status{Unit: s.commodity().Unit, LastError: s.lastError}
	for _, src := range s.Sources {
		st.Sources = append(st.Sources, src.Name())
	}
	if !s.lastFetch.IsZero() {
		t := s.lastFetch
		st.LastFetch = &t
	}
	return st
}

// Run fetches today's prices, tomorrow's once published (the electricity
// auction closes at 12:00 and results appear around 13:00 CET), and missing
// days since BackfillFrom.
func (s *Service) Run(ctx context.Context) {
	var backfilled string
	tick := time.NewTicker(15 * time.Minute)
	defer tick.Stop()
	for {
		now := time.Now()
		if len(s.sources()) == 0 { // none (yet): wait for SetSources
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			continue
		}
		s.ensure(ctx, now)
		if local := now.In(Amsterdam); local.Hour() > 12 || (local.Hour() == 12 && local.Minute() >= 45) {
			s.ensure(ctx, now.AddDate(0, 0, 1))
		}
		if today := now.In(Amsterdam).Format("2006-01-02"); today != backfilled {
			s.Backfill(ctx, now)
			backfilled = today
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Backfill fetches every missing day from BackfillFrom up to the day before
// now, newest first. Days that are complete in the store cost no request.
func (s *Service) Backfill(ctx context.Context, now time.Time) {
	if s.BackfillFrom.IsZero() {
		return
	}
	first := s.BackfillFrom.In(Amsterdam)
	first = time.Date(first.Year(), first.Month(), first.Day(), 12, 0, 0, 0, Amsterdam)
	n := now.In(Amsterdam)
	pause := s.Pause
	if pause == 0 {
		pause = 2 * time.Second
	}
	fetched, missing := 0, 0
	for day := time.Date(n.Year(), n.Month(), n.Day()-1, 12, 0, 0, 0, Amsterdam); !day.Before(first); day = day.AddDate(0, 0, -1) {
		if ctx.Err() != nil {
			return
		}
		if s.have(ctx, day) {
			continue
		}
		// A source may return the neighbouring days too (EnergyZero does):
		// asking for the day before covers this day, that day and the one
		// before it, so going back costs one request per three days.
		target := day
		if prev := day.AddDate(0, 0, -1); !prev.Before(first) {
			target = prev
		}
		_, _, err := s.Fetch(ctx, target)
		if target != day && !s.have(ctx, day) {
			_, _, err = s.Fetch(ctx, day)
		}
		if err != nil && !errors.Is(err, ErrNotPublished) {
			s.Log.Warn("prices not fetched", "commodity", s.commodity().Name, "day", day.Format("2006-01-02"), "err", err)
		}
		fetched++
		if err != nil {
			missing++
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(pause): // be gentle with the sources
		}
	}
	if fetched > 0 {
		s.Log.Info("price backfill done", "commodity", s.commodity().Name, "from", first.Format("2006-01-02"), "fetched", fetched, "failed", missing)
	}
}

// have reports whether the store has a day completely.
func (s *Service) have(ctx context.Context, day time.Time) bool {
	c := s.commodity()
	from, to := c.Bounds(day)
	rows, err := s.Store.Prices(ctx, c.Market, from, to)
	return err == nil && complete(fromStore(rows), from, to)
}

// ensure fetches a day unless the store already has it completely. It
// reports whether a fetch was attempted.
func (s *Service) ensure(ctx context.Context, day time.Time) (bool, error) {
	c := s.commodity()
	if s.have(ctx, day) {
		return false, nil
	}
	_, _, err := s.Fetch(ctx, day)
	if err != nil && !errors.Is(err, ErrNotPublished) {
		s.Log.Warn("prices not fetched", "commodity", c.Name, "day", day.In(Amsterdam).Format("2006-01-02"), "err", err)
	}
	return true, err
}

// Fetch gets one day from the first source that has it and stores everything
// the source returned. It reports the number of periods of that day.
func (s *Service) Fetch(ctx context.Context, day time.Time) (string, int, error) {
	c := s.commodity()
	from, to := c.Bounds(day)
	var errs []string
	notPublished := true
	for _, src := range s.sources() {
		ps, err := src.Day(ctx, day)
		if err == nil && !complete(ps, from, to) {
			err = fmt.Errorf("incomplete day (%d periods)", len(ps))
		}
		if err != nil {
			if !errors.Is(err, ErrNotPublished) {
				notPublished = false
			}
			errs = append(errs, src.Name()+": "+err.Error())
			continue
		}
		rows := make([]store.MarketPrice, len(ps))
		inDay := 0
		for i, p := range ps {
			rows[i] = store.MarketPrice{Start: p.Start, Duration: p.Duration, Value: p.Value, Source: p.Source}
			if !p.Start.Before(from) && p.Start.Before(to) {
				inDay++
			}
		}
		if err := s.Store.SavePrices(ctx, c.Market, rows); err != nil {
			return "", 0, err
		}
		if s.OnSaved != nil && len(ps) > 0 {
			first := ps[0].Start
			for _, p := range ps {
				if p.Start.Before(first) {
					first = p.Start
				}
			}
			s.OnSaved(first)
		}
		s.mu.Lock()
		s.lastFetch, s.lastError = time.Now(), ""
		s.mu.Unlock()
		s.Log.Info("prices fetched", "commodity", c.Name, "day", day.In(Amsterdam).Format("2006-01-02"),
			"source", src.Name(), "periods", inDay, "stored", len(ps))
		return src.Name(), inDay, nil
	}
	err := errors.New(strings.Join(errs, "; "))
	if notPublished {
		err = fmt.Errorf("%w (%s)", ErrNotPublished, strings.Join(errs, "; "))
	} else {
		s.mu.Lock()
		s.lastError = err.Error()
		s.mu.Unlock()
	}
	return "", 0, err
}

// Range returns stored prices overlapping [from, to).
func (s *Service) Range(ctx context.Context, from, to time.Time) ([]Price, error) {
	rows, err := s.Store.Prices(ctx, s.commodity().Market, from, to)
	if err != nil {
		return nil, err
	}
	return fromStore(rows), nil
}

func fromStore(rows []store.MarketPrice) []Price {
	out := make([]Price, len(rows))
	for i, r := range rows {
		out[i] = Price{Start: r.Start, Duration: r.Duration, Value: r.Value, Source: r.Source}
	}
	return out
}
