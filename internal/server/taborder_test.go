package server

import (
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The tab order is stored, survives a restart and rejects unknown keys.
func TestTabOrder(t *testing.T) {
	s := newServer(t)
	o, err := LoadTabOrder(filepath.Join(t.TempDir(), "tab-order.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.TabOrder = o
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	const client = "192.168.1.20:5000"

	if w := do(h, "GET", "/api/settings/tabs", client, "", nil); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"order":[]`) {
		t.Fatalf("default: %d %s", w.Code, w.Body)
	}
	want := []string{"dashboard", "tab:3", "verbruik", "meldingen", "automatisering", "apparaten", "klimaat", "instellingen"}
	w := do(h, "PUT", "/api/settings/tabs", client, `{"order":["dashboard","tab:3","verbruik","meldingen","automatisering","apparaten","klimaat","instellingen"]}`, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	again, err := LoadTabOrder(o.path)
	if err != nil || !slices.Equal(again.Order(), want) {
		t.Fatalf("reload: %v %v", again.Order(), err)
	}

	for _, body := range []string{
		`{"order":["dashboard","dashboard"]}`,
		`{"order":["nope"]}`,
		`{"order":["tab:0"]}`,
		`{"order":["tab:01"]}`,
		`{"order":["tab:x"]}`,
	} {
		if w := do(h, "PUT", "/api/settings/tabs", client, body, hdr); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", body, w.Code)
		}
	}
	if !slices.Equal(o.Order(), want) {
		t.Fatalf("changed by a refused request: %v", o.Order())
	}

	// an empty list restores the default
	if w := do(h, "PUT", "/api/settings/tabs", client, `{"order":[]}`, hdr); w.Code != http.StatusOK || len(o.Order()) != 0 {
		t.Fatalf("reset: %d %v", w.Code, o.Order())
	}
}
