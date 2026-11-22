package tado

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

type fakeTado struct {
	mu       sync.Mutex
	requests []string // "METHOD path body"
	overlay  string   // JSON of the zone's overlay, "null" when none
}

func (f *fakeTado) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path+" "+string(body))
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/homes/1/zoneStates":
		io.WriteString(w, `{"zoneStates":{"7":{"setting":{"type":"HEATING","power":"ON","temperature":{"celsius":21}},"overlay":`+f.overlay+`,"sensorDataPoints":{},"activityDataPoints":{}}}}`)
	case r.Method == http.MethodPut:
		var req overlayReq
		json.Unmarshal(body, &req)
		b, _ := json.Marshal(map[string]any{"setting": req.Setting})
		f.overlay = string(b)
		io.WriteString(w, "{}")
	case r.Method == http.MethodDelete:
		f.overlay = "null"
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeTado) reset() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	r := f.requests
	f.requests = nil
	return r
}

func newTestInstance(t *testing.T) (*instance, *fakeTado) {
	t.Helper()
	f := &fakeTado{overlay: "null"}
	srv := httptest.NewServer(http.HandlerFunc(f.handler))
	t.Cleanup(srv.Close)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
	in := &instance{http: srv.Client(), overlayFor: overlayDuration("120"), overlays: map[string]*overlay{},
		accessToken: "x", accessExpiry: time.Now().Add(time.Hour)}
	return in, f
}

func TestTadoControl(t *testing.T) {
	in, f := newTestInstance(t)
	ctx := context.Background()
	dev := plugin.Device{ID: "d", Config: map[string]string{"home_id": "1", "zone_id": "7", "type": "HEATING"}}

	cs := in.Controls(dev)
	if len(cs) != 4 || cs[0].Key != "target" || cs[0].Max != 25 || cs[1].Key != "mode" || cs[1].Hidden ||
		!cs[2].Hidden || !cs[3].Hidden {
		t.Fatalf("controls %+v", cs)
	}
	if err := in.Set(ctx, dev, "target", 19.5); err != nil {
		t.Fatal(err)
	}
	reqs := f.reset()
	if len(reqs) != 1 || reqs[0] != `PUT /homes/1/zones/7/overlay {"setting":{"type":"HEATING","power":"ON","temperature":{"celsius":19.5}},"termination":{"typeSkillBasedApp":"TIMER","durationInSeconds":7200}}` {
		t.Fatalf("set: %q", reqs)
	}

	// not near the end yet: no renewal
	if _, err := in.PollAll(ctx, []plugin.Device{dev}); err != nil {
		t.Fatal(err)
	}
	if reqs := f.reset(); len(reqs) != 1 {
		t.Fatalf("poll renewed too early: %q", reqs)
	}
	// near the end: renewed
	in.overlays["1:7"].until = time.Now().Add(10 * time.Minute)
	in.PollAll(ctx, []plugin.Device{dev})
	if reqs := f.reset(); len(reqs) != 2 || reqs[1][:4] != "PUT " {
		t.Fatalf("no renewal: %q", reqs)
	}
	if time.Until(in.overlays["1:7"].until) < time.Hour {
		t.Fatal("until not extended")
	}

	// changed elsewhere (tado app set 22 °C): let go, no renewal
	f.overlay = `{"setting":{"type":"HEATING","power":"ON","temperature":{"celsius":22}}}`
	in.overlays["1:7"].until = time.Now().Add(10 * time.Minute)
	in.PollAll(ctx, []plugin.Device{dev})
	if reqs := f.reset(); len(reqs) != 1 || len(in.overlays) != 0 {
		t.Fatalf("kept an overlay changed elsewhere: %q %v", reqs, in.overlays)
	}

	// off, then back to schedule
	if err := in.Set(ctx, dev, "off", nil); err != nil {
		t.Fatal(err)
	}
	if err := in.Set(ctx, dev, "schedule", nil); err != nil {
		t.Fatal(err)
	}
	reqs = f.reset()
	if len(reqs) != 2 || reqs[0] != `PUT /homes/1/zones/7/overlay {"setting":{"type":"HEATING","power":"OFF"},"termination":{"typeSkillBasedApp":"TIMER","durationInSeconds":7200}}` ||
		reqs[1] != "DELETE /homes/1/zones/7/overlay " || len(in.overlays) != 0 {
		t.Fatalf("off/schedule: %q %v", reqs, in.overlays)
	}

	// the mode: off, manual (holds the temperature seen at the last poll:
	// the fake zone reports 21 °C) and schedule
	for _, c := range []struct{ mode, want string }{
		{"off", `PUT /homes/1/zones/7/overlay {"setting":{"type":"HEATING","power":"OFF"},"termination":{"typeSkillBasedApp":"TIMER","durationInSeconds":7200}}`},
		{"manual", `PUT /homes/1/zones/7/overlay {"setting":{"type":"HEATING","power":"ON","temperature":{"celsius":21}},"termination":{"typeSkillBasedApp":"TIMER","durationInSeconds":7200}}`},
		{"schedule", "DELETE /homes/1/zones/7/overlay "},
	} {
		if err := in.Set(ctx, dev, "mode", c.mode); err != nil {
			t.Fatal(err)
		}
		if reqs := f.reset(); len(reqs) != 1 || reqs[0] != c.want {
			t.Fatalf("mode %s: %q", c.mode, reqs)
		}
	}
	if err := in.Set(ctx, dev, "mode", "boost"); err != plugin.ErrNoControl {
		t.Fatalf("unknown mode: %v", err)
	}
	// a zone never seen on holds the default
	in.targets = map[string]float64{}
	in.Set(ctx, dev, "mode", "manual")
	if reqs := f.reset(); len(reqs) != 1 || !strings.Contains(reqs[0], `"celsius":20}`) {
		t.Fatalf("manual without a known temperature: %q", reqs)
	}
	// hot water: on without a temperature
	hw := plugin.Device{ID: "w", Config: map[string]string{"home_id": "1", "zone_id": "0", "type": "HOT_WATER"}}
	if cs := in.Controls(hw); len(cs) != 3 || cs[0].Options[1].Label != "Aan" {
		t.Fatalf("hot water controls %+v", cs)
	}
	in.Set(ctx, hw, "mode", "manual")
	if reqs := f.reset(); len(reqs) != 1 || !strings.Contains(reqs[0], `{"type":"HOT_WATER","power":"ON"}`) {
		t.Fatalf("hot water on: %q", reqs)
	}
}

func TestOverlayDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 2 * time.Hour, "x": 2 * time.Hour, "5": 30 * time.Minute, "90": 90 * time.Minute, "10000": 12 * time.Hour} {
		if got := overlayDuration(in); got != want {
			t.Errorf("%q: %v, want %v", in, got, want)
		}
	}
}
