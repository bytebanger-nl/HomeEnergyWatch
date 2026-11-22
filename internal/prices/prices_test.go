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
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// nordPoolDay builds a response in the format of the data portal API.
func nordPoolDay(day time.Time, step time.Duration) string {
	from, to := DayBounds(day)
	var entries []string
	i := 0
	for t := from; t.Before(to); t = t.Add(step) {
		entries = append(entries, fmt.Sprintf(`{"deliveryStart":"%s","deliveryEnd":"%s","entryPerArea":{"NL":%d.5}}`,
			t.Format(time.RFC3339), t.Add(step).Format(time.RFC3339), 50+i))
		i++
	}
	return `{"deliveryDateCET":"` + day.In(Amsterdam).Format("2006-01-02") + `","version":2,"market":"DayAhead",
		"deliveryAreas":["NL"],"currency":"EUR","exchangeRate":1,"multiAreaEntries":[` + strings.Join(entries, ",") + `],
		"areaStates":[{"state":"Final","areas":["NL"]}]}`
}

func TestNordPoolDSTDayHas92Quarters(t *testing.T) {
	day := time.Date(2026, 3, 29, 12, 0, 0, 0, Amsterdam) // clocks go forward: 23 hours
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("date") != "2026-03-29" || r.URL.Query().Get("deliveryArea") != "NL" {
			t.Errorf("query %v", r.URL.Query())
		}
		_, _ = io.WriteString(w, nordPoolDay(day, 15*time.Minute))
	}))
	defer srv.Close()
	ps, err := (&NordPool{BaseURL: srv.URL}).Day(context.Background(), day)
	if err != nil {
		t.Fatal(err)
	}
	from, to := DayBounds(day)
	if len(ps) != 92 || !complete(ps, from, to) || ps[0].Value != 50.5 || ps[0].Duration != 15*time.Minute {
		t.Fatalf("got %d periods, first %+v", len(ps), ps[0])
	}
}

func TestNordPoolNotPublished(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer srv.Close()
	if _, err := (&NordPool{BaseURL: srv.URL}).Day(context.Background(), time.Now()); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("err = %v", err)
	}
}

const entsoeXML = `<?xml version="1.0" encoding="UTF-8"?>
<Publication_MarketDocument xmlns="urn:iec62325.351:tc57wg16:451-3:publicationdocument:7:3">
  <TimeSeries>
    <currency_Unit.name>EUR</currency_Unit.name>
    <price_Measure_Unit.name>MWH</price_Measure_Unit.name>
    <curveType>A03</curveType>
    <Period>
      <timeInterval><start>2026-11-04T23:00Z</start><end>2026-11-05T00:00Z</end></timeInterval>
      <resolution>PT15M</resolution>
      <Point><position>1</position><price.amount>80.10</price.amount></Point>
      <Point><position>2</position><price.amount>75.00</price.amount></Point>
      <Point><position>4</position><price.amount>-1.25</price.amount></Point>
    </Period>
  </TimeSeries>
  <TimeSeries>
    <currency_Unit.name>EUR</currency_Unit.name>
    <curveType>A03</curveType>
    <Period>
      <timeInterval><start>2026-11-04T23:00Z</start><end>2026-11-05T00:00Z</end></timeInterval>
      <resolution>PT60M</resolution>
      <Point><position>1</position><price.amount>70.00</price.amount></Point>
    </Period>
  </TimeSeries>
</Publication_MarketDocument>`

func TestParseEntsoEFillsOmittedPositionsAndPrefersQuarters(t *testing.T) {
	ps, err := parseEntsoE([]byte(entsoeXML))
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{80.10, 75.00, 75.00, -1.25} // position 3 repeats position 2
	if len(ps) != 4 {
		t.Fatalf("got %d prices: %+v", len(ps), ps)
	}
	for i, p := range ps {
		if p.Value != want[i] || p.Duration != 15*time.Minute {
			t.Errorf("%d: %+v", i, p)
		}
	}
	if ps[0].Start != time.Date(2026, 11, 4, 23, 0, 0, 0, time.UTC) {
		t.Errorf("start %v", ps[0].Start)
	}
}

func TestParseEntsoENoData(t *testing.T) {
	ack := `<Acknowledgement_MarketDocument><Reason><code>999</code><text>No matching data found for Data item Day-ahead Prices</text></Reason></Acknowledgement_MarketDocument>`
	if _, err := parseEntsoE([]byte(ack)); !errors.Is(err, ErrNotPublished) {
		t.Fatalf("err = %v", err)
	}
}

type fakeSource struct {
	name  string
	err   error
	calls int
	step  time.Duration
}

func (f *fakeSource) Name() string { return f.name }
func (f *fakeSource) Day(_ context.Context, day time.Time) ([]Price, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	from, to := DayBounds(day)
	var ps []Price
	for t := from; t.Before(to); t = t.Add(f.step) {
		ps = append(ps, Price{Start: t, Duration: f.step, Value: 100, Source: f.name})
	}
	return ps, nil
}

func TestServiceFallbackAndStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	primary := &fakeSource{name: "nordpool", err: errors.New("HTTP 500")}
	fallback := &fakeSource{name: "entsoe", step: 15 * time.Minute}
	svc := &Service{Store: st, Sources: []Source{primary, fallback}, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	day := time.Date(2026, 10, 25, 12, 0, 0, 0, Amsterdam) // 25 hours
	src, n, err := svc.Fetch(context.Background(), day)
	if err != nil || src != "entsoe" || n != 100 {
		t.Fatalf("fetch: %s %d %v", src, n, err)
	}
	from, to := DayBounds(day)
	got, err := svc.Range(context.Background(), from, to)
	if err != nil || len(got) != 100 {
		t.Fatalf("stored %d %v", len(got), err)
	}
	// ensure does not fetch a day that is already complete
	svc.ensure(context.Background(), day)
	if fallback.calls != 1 {
		t.Fatalf("refetched: %d calls", fallback.calls)
	}
	// hourly data is replaced by quarter-hour data for the same period
	hourly := &fakeSource{name: "import", step: time.Hour}
	ps, _ := hourly.Day(context.Background(), day)
	rows := []store.MarketPrice{{Start: ps[0].Start, Duration: time.Hour, Value: 1, Source: "import"}}
	if err := st.SavePrices(context.Background(), Market, rows); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePrices(context.Background(), Market, []store.MarketPrice{{Start: ps[0].Start, Duration: 15 * time.Minute, Value: 2, Source: "x"}}); err != nil {
		t.Fatal(err)
	}
	got, _ = svc.Range(context.Background(), from, from.Add(time.Hour))
	if len(got) != 4 || got[0].Value != 2 || got[1].Value != 100 {
		t.Fatalf("hourly must not replace quarters: %+v", got)
	}
	// on an empty period, quarter hours replace an hourly price entirely
	empty := from.AddDate(0, 0, 10)
	_ = st.SavePrices(context.Background(), Market, []store.MarketPrice{{Start: empty, Duration: time.Hour, Value: 1, Source: "import"}})
	_ = st.SavePrices(context.Background(), Market, []store.MarketPrice{
		{Start: empty, Duration: 15 * time.Minute, Value: 3, Source: "x"},
		{Start: empty.Add(15 * time.Minute), Duration: 15 * time.Minute, Value: 4, Source: "x"},
	})
	got, _ = svc.Range(context.Background(), empty, empty.Add(time.Hour))
	if len(got) != 2 || got[0].Value != 3 {
		t.Fatalf("quarters should replace hourly: %+v", got)
	}
}
