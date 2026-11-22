package server

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// With a contract that bills per hour, every quarter's all-in price is that
// of the hour's average market price; the market price stays per quarter.
func TestPriceRangeHourlyContract(t *testing.T) {
	s := newServer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	s.Prices = &prices.Service{Store: st}
	s.Contracts = &costs.Contracts{Store: st}
	if _, err := s.Contracts.Save(ctx, costs.Contract{Name: "Uur", Start: "2026-01-01", Type: costs.TypeDynamic, VATPct: 21,
		Electricity: costs.Electricity{Hourly: true, MarkupCt: 2}}); err != nil {
		t.Fatal(err)
	}
	h := time.Date(2026, 6, 10, 9, 0, 0, 0, prices.Amsterdam)
	var rows []store.MarketPrice
	for i, v := range []float64{100, 200, 300, 400, 500} {
		rows = append(rows, store.MarketPrice{Start: h.Add(time.Duration(i) * 15 * time.Minute), Duration: 15 * time.Minute, Value: v, Source: "test"})
	}
	if err := st.SavePrices(ctx, prices.Market, rows); err != nil {
		t.Fatal(err)
	}
	w := do(s.Handler(), "GET", "/api/prices?from="+h.UTC().Format(time.RFC3339)+"&to="+h.Add(2*time.Hour).UTC().Format(time.RFC3339), "192.168.1.20:5000", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	var res struct {
		Prices []struct {
			EURMWh float64  `json:"eur_mwh"`
			AllIn  *float64 `json:"all_in_ct"`
			Hourly bool     `json:"hourly"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || len(res.Prices) != 5 {
		t.Fatalf("%s (%v)", w.Body, err)
	}
	for i, p := range res.Prices[:4] {
		if want := 25*1.21 + 2; !p.Hourly || p.AllIn == nil || *p.AllIn < want-1e-9 || *p.AllIn > want+1e-9 || p.EURMWh != float64(100*(i+1)) {
			t.Errorf("quarter %d: %+v, want all-in %v", i, p, want)
		}
	}
	// 10:00 has one quarter only: what the hour costs is not known yet
	if p := res.Prices[4]; p.AllIn != nil {
		t.Errorf("incomplete hour: all-in %v, want none", *p.AllIn)
	}
}
