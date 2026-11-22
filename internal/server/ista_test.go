package server

import (
	"context"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func TestIstaAPI(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	sim := &ista.Sim{Username: "u@example.org", Password: "pw", MoveIn: time.Date(2025, 9, 1, 0, 0, 0, 0, time.UTC), Now: clock}
	portal := httptest.NewServer(sim.Handler())
	defer portal.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "ista.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if err := st.SavePlugin(ctx, store.PluginInstance{ID: "a1", Type: ista.PluginType, Name: "ista",
		Config: map[string]string{"username": sim.Username, "password": sim.Password}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s := newServer(t)
	s.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	h := s.Handler()
	const lan = "192.168.1.20:5000"

	// without the service the endpoints are absent
	if w := do(h, "GET", "/api/ista", lan, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("without ista: %d", w.Code)
	}
	s.Ista = &ista.Service{Store: st, Loc: time.UTC, BaseURL: portal.URL, Pace: -1, Now: clock, Log: s.Log}
	h = s.Handler()
	// before the first sync there is no address
	if w := do(h, "GET", "/api/ista/costs", lan, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("costs before sync: %d %s", w.Code, w.Body)
	}
	s.Ista.SyncDue(ctx)

	var ov struct {
		Accounts  []ista.Status
		Addresses []ista.Address
	}
	if w := do(h, "GET", "/api/ista", lan, "", nil); w.Code != 200 || decode(w.Body.Bytes(), &ov) != nil {
		t.Fatalf("overview: %d %s", w.Code, w.Body)
	}
	if len(ov.Accounts) != 1 || ov.Accounts[0].State != "idle" || len(ov.Addresses) != 1 || len(ov.Addresses[0].Rooms) != 4 {
		t.Fatalf("overview: %+v", ov)
	}

	var u ista.Usage
	if w := do(h, "GET", "/api/ista/usage?from=2025-10-01&to=2025-12-31&bucket=week", lan, "", nil); w.Code != 200 || decode(w.Body.Bytes(), &u) != nil {
		t.Fatalf("usage: %d %s", w.Code, w.Body)
	}
	if len(u.Buckets) != 14 || u.Buckets[0].Start != "2025-10-01" || u.Total.Total == nil {
		t.Fatalf("usage: %d buckets, first %+v", len(u.Buckets), u.Buckets[0])
	}
	for _, bad := range []string{"from=x&to=2025-01-01", "from=2025-02-01&to=2025-01-01", "from=2025-01-01&to=2025-02-01&bucket=year", "from=2020-01-01&to=2025-02-01"} {
		if w := do(h, "GET", "/api/ista/usage?"+bad, lan, "", nil); w.Code != http.StatusBadRequest {
			t.Fatalf("usage %s: %d", bad, w.Code)
		}
	}

	var wk ista.Weeks
	if w := do(h, "GET", "/api/ista/weeks?y=2026", lan, "", nil); w.Code != 200 || decode(w.Body.Bytes(), &wk) != nil || len(wk.Rows) == 0 {
		t.Fatalf("weeks: %d %s", w.Code, w.Body)
	}

	ct := map[string]string{"Content-Type": "application/json"}
	body := `{"area_dm2":7100,"unit_price":0.21,"area_price":0.095,"advance":[{"from":"2026-07-01","amount":85}]}`
	if w := do(h, "PUT", "/api/ista/contracts/2027", lan, body, ct); w.Code != 200 {
		t.Fatalf("save contract: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/api/ista/contracts/2027", lan, `{"area_dm2":0}`, ct); w.Code != http.StatusBadRequest {
		t.Fatalf("invalid contract: %d %s", w.Code, w.Body)
	}
	var c ista.Costs
	if w := do(h, "GET", "/api/ista/costs", lan, "", nil); w.Code != 200 || decode(w.Body.Bytes(), &c) != nil {
		t.Fatalf("costs: %d %s", w.Code, w.Body)
	}
	if c.Period.Y != 2027 || c.PriceSource != "contract" || c.CostSoFar == nil || c.Paid != 425 {
		t.Fatalf("costs: %+v", c)
	}
	if c.Payments != 5 || c.Monthly != 85 || c.NextPayment == "" {
		t.Fatalf("advance details: %+v", c)
	}
	// the costs per month up to the last measured day add up to the period's
	// costs so far
	var cr ista.CostRange
	if w := do(h, "GET", "/api/ista/costs?from=2026-07-01&to="+c.LastDay+"&bucket=month", lan, "", nil); w.Code != 200 || decode(w.Body.Bytes(), &cr) != nil {
		t.Fatalf("cost range: %d %s", w.Code, w.Body)
	}
	if len(cr.Buckets) == 0 || cr.Total.Total == nil || math.Abs(*cr.Total.Total-*c.CostSoFar) > 0.05 || cr.Total.Unpriced != 0 {
		t.Fatalf("cost range total %+v, period so far %v", cr.Total, *c.CostSoFar)
	}
	if w := do(h, "GET", "/api/ista/costs?from=2026-07-01&to=2026-06-01", lan, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("bad range: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/ista/contracts/2027", lan, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/ista/contracts/2027", lan, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("delete again: %d", w.Code)
	}
	if w := do(h, "POST", "/api/ista/sync", lan, "", nil); w.Code != http.StatusAccepted {
		t.Fatalf("sync: %d", w.Code)
	}
}
