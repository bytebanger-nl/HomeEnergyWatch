package prices

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// energyZeroServer mimics the public EnergyZero API: the requested date plus
// the day before and after, electricity per quarter (EUR/kWh) or gas per gas
// day (EUR/m³). Dates after last get 404.
func energyZeroServer(t *testing.T, last time.Time, calls *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		day, err := time.ParseInLocation("02-01-2006", q.Get("date"), Amsterdam)
		if err != nil {
			t.Errorf("date %q", q.Get("date"))
		}
		gas := q.Get("energy_type") == "ENERGY_TYPE_GAS"
		if (gas && q.Get("interval") != "INTERVAL_DAY") || (!gas && q.Get("interval") != "INTERVAL_QUARTER") {
			t.Errorf("query %v", q)
		}
		if day.After(last) {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"code":5,"message":"prices do not span the whole requested date","details":[]}`)
			return
		}
		var entries []string
		add := func(from, to time.Time, v string) {
			entries = append(entries, fmt.Sprintf(`{"start":"%s","end":"%s","price":{"value":"%s"}}`,
				from.UTC().Format(time.RFC3339), to.UTC().Format(time.RFC3339), v))
		}
		interval := "RESPONSE_INTERVAL_QUARTER"
		for d := day.AddDate(0, 0, -1); !d.After(day.AddDate(0, 0, 1)) && !d.After(last); d = d.AddDate(0, 0, 1) {
			if gas {
				interval = "RESPONSE_INTERVAL_DAY"
				from, to := GasDayBounds(d)
				add(from, to, "0.859750149776")
				continue
			}
			from, to := DayBounds(d)
			for t := from; t.Before(to); t = t.Add(15 * time.Minute) {
				add(t, t.Add(15*time.Minute), "0.20013")
			}
		}
		_, _ = io.WriteString(w, `{"interval":"`+interval+`","base":[`+strings.Join(entries, ",")+`],"all_in":[]}`)
	}))
}

func newTestStore(t *testing.T) *store.Store {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func TestEnergyZeroElectricityStoresNeighbourDays(t *testing.T) {
	day := time.Date(2026, 3, 29, 12, 0, 0, 0, Amsterdam) // 23 hours
	var calls atomic.Int32
	srv := energyZeroServer(t, day.AddDate(0, 0, 1), &calls)
	defer srv.Close()
	svc := &Service{Store: newTestStore(t), Sources: []Source{&EnergyZero{BaseURL: srv.URL}}, Log: quiet}
	src, n, err := svc.Fetch(context.Background(), day)
	if err != nil || src != "energyzero" || n != 92 {
		t.Fatalf("fetch: %s %d %v", src, n, err)
	}
	from, to := DayBounds(day)
	got, _ := svc.Range(context.Background(), from, to)
	if len(got) != 92 || got[0].Value != 200.13 || got[0].Duration != 15*time.Minute {
		t.Fatalf("stored %d, first %+v", len(got), got[0])
	}
	// the day before and after came with the same response
	for _, d := range []time.Time{day.AddDate(0, 0, -1), day.AddDate(0, 0, 1)} {
		if tried, _ := svc.ensure(context.Background(), d); tried {
			t.Fatalf("%s fetched again", d.Format("2006-01-02"))
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("%d requests", calls.Load())
	}
}

func TestEnergyZeroNotPublished(t *testing.T) {
	var calls atomic.Int32
	today := time.Date(2026, 11, 5, 12, 0, 0, 0, Amsterdam)
	srv := energyZeroServer(t, today, &calls)
	defer srv.Close()
	_, err := (&EnergyZero{BaseURL: srv.URL}).Day(context.Background(), today.AddDate(0, 0, 2))
	if !errors.Is(err, ErrNotPublished) {
		t.Fatalf("err = %v", err)
	}
}

func TestEnergyZeroGasDays(t *testing.T) {
	day := time.Date(2026, 10, 24, 12, 0, 0, 0, Amsterdam) // 06:00 summer time to 06:00 winter time: 25 hours
	var calls atomic.Int32
	srv := energyZeroServer(t, day, &calls)
	defer srv.Close()
	svc := &Service{Store: newTestStore(t), Commodity: Gas, Sources: []Source{&EnergyZero{BaseURL: srv.URL, Gas: true}}, Log: quiet}
	if _, n, err := svc.Fetch(context.Background(), day); err != nil || n != 1 {
		t.Fatalf("fetch: %d %v", n, err)
	}
	from, to := GasDayBounds(day)
	if to.Sub(from) != 25*time.Hour || from.In(Amsterdam).Hour() != 6 {
		t.Fatalf("gas day %s - %s", from, to)
	}
	got, _ := svc.Range(context.Background(), from, to)
	if len(got) != 1 || got[0].Value != 0.859750149776 || got[0].Duration != 25*time.Hour {
		t.Fatalf("got %+v", got)
	}
	// gas is a market of its own
	power := &Service{Store: svc.Store, Log: quiet}
	if ps, _ := power.Range(context.Background(), from, to); len(ps) != 0 {
		t.Fatalf("gas leaked into electricity: %+v", ps)
	}
}

func TestBackfillFetchesOnlyMissingDays(t *testing.T) {
	now := time.Date(2026, 1, 20, 9, 0, 0, 0, Amsterdam)
	var calls atomic.Int32
	srv := energyZeroServer(t, now, &calls)
	defer srv.Close()
	svc := &Service{
		Store: newTestStore(t), Sources: []Source{&EnergyZero{BaseURL: srv.URL}}, Log: quiet,
		BackfillFrom: time.Date(2026, 1, 1, 0, 0, 0, 0, Amsterdam), Pause: time.Millisecond,
	}
	svc.Backfill(context.Background(), now)
	// 19 days (1-19 January), three per request
	if c := calls.Load(); c > 7 {
		t.Fatalf("%d requests for 19 days", c)
	}
	for d := time.Date(2026, 1, 1, 12, 0, 0, 0, Amsterdam); d.Before(now); d = d.AddDate(0, 0, 1) {
		from, to := DayBounds(d)
		ps, _ := svc.Range(context.Background(), from, to)
		if !complete(ps, from, to) {
			t.Fatalf("%s incomplete", d.Format("2006-01-02"))
		}
	}
	before := calls.Load()
	svc.Backfill(context.Background(), now)
	if calls.Load() != before {
		t.Fatalf("second backfill made %d requests", calls.Load()-before)
	}
}
