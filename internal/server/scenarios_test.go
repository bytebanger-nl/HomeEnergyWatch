package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Scenarios can be created, listed, changed and deleted; the dates and
// advance of a contract do not stick to a scenario.
func TestScenarioAPI(t *testing.T) {
	s := newServer(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	s.Scenarios = &costs.Scenarios{Store: st}
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	lan := "192.168.1.20:5000"

	w := do(h, "POST", "/api/scenarios", lan, `{"name":"Vast","rules_2027":true,"contract":{"type":"fixed","start":"2026-01-01","electricity":{"rate_ct":25},"advance":{"amounts":[{"from":"2026-01-01","eur":10}]}}}`, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	var sc costs.Scenario
	if err := json.Unmarshal(w.Body.Bytes(), &sc); err != nil || sc.ID == 0 || sc.Contract.Start != "" || sc.Contract.Advance != nil || !sc.Rules2027 {
		t.Fatalf("created %+v (%v)", sc, err)
	}
	if w := do(h, "POST", "/api/scenarios", lan, `{"name":"","contract":{"type":"fixed"}}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("nameless: %d", w.Code)
	}
	if w := do(h, "PUT", "/api/scenarios/999", lan, `{"name":"x","contract":{"type":"fixed"}}`, hdr); w.Code != http.StatusNotFound {
		t.Fatalf("unknown id: %d", w.Code)
	}
	w = do(h, "GET", "/api/scenarios", lan, "", nil)
	var list struct{ Scenarios []costs.Scenario }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Scenarios) != 1 {
		t.Fatalf("list %s", w.Body)
	}
	if w := do(h, "DELETE", "/api/scenarios/"+itoa(sc.ID), lan, "", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", w.Code)
	}
	if w := do(h, "PUT", "/api/scenarios/1", "8.8.8.8:5000", `{"name":"x","contract":{"type":"fixed"}}`, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("from outside the LAN: %d", w.Code)
	}
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }
