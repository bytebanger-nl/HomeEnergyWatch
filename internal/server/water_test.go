package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Water contracts live under /api/water, apart from the energy contracts,
// and water costs need a water meter.
func TestWaterContractAPI(t *testing.T) {
	s := newServer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "w.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s.Contracts = &costs.Contracts{Store: st}
	s.Costs = &costs.Calculator{Store: st, Contracts: s.Contracts}
	s.WaterContracts = &costs.Contracts{Store: st, Table: store.WaterContracts}
	s.WaterCosts = &costs.Calculator{Store: st, Contracts: s.WaterContracts}
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	lan := "192.168.1.20:5000"

	w := do(h, "POST", "/api/water/contracts", lan, `{"name":"Vitens","start":"2026-01-01","water":{"rate_ct":110,"tax_ct":50},"monthly":[{"name":"Vastrecht","eur":7}]}`, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var c costs.Contract
	if err := json.Unmarshal(w.Body.Bytes(), &c); err != nil || c.ID == 0 || c.Type != costs.TypeWater || c.Water.RateCt != 110 {
		t.Fatalf("created %+v (%v)", c, err)
	}
	var list struct{ Contracts []costs.Contract }
	if err := json.Unmarshal(do(h, "GET", "/api/contracts", lan, "", nil).Body.Bytes(), &list); err != nil || len(list.Contracts) != 0 {
		t.Fatalf("energy contracts %+v (%v)", list, err)
	}
	if err := json.Unmarshal(do(h, "GET", "/api/water/contracts", lan, "", nil).Body.Bytes(), &list); err != nil || len(list.Contracts) != 1 {
		t.Fatalf("water contracts %+v (%v)", list, err)
	}
	if w := do(h, "GET", "/api/water/costs?from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z", lan, "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("costs without water meter: %d %s", w.Code, w.Body)
	}
	// usage costs of one device exist for electricity only
	if w := do(h, "GET", "/api/water/costs?scope=usage&from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z", lan, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("water usage scope: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/costs?scope=alles&from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z", lan, "", nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown scope: %d %s", w.Code, w.Body)
	}
	if w := do(h, "DELETE", "/api/water/contracts/"+itoa(c.ID), lan, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(h, "POST", "/api/water/contracts", "8.8.8.8:5000", `{"name":"x","start":"2026-01-01"}`, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("from outside the LAN: %d", w.Code)
	}
}
