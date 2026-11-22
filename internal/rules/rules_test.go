package rules

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

type fakeDevices struct {
	mu       sync.Mutex
	views    map[string]gateway.DeviceView
	switched []string
	failOn   string
}

func (f *fakeDevices) Views() []gateway.DeviceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []gateway.DeviceView
	for _, v := range f.views {
		out = append(out, v)
	}
	return out
}

func (f *fakeDevices) SetPower(_ context.Context, id string, on bool) (gateway.DeviceView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id == f.failOn {
		return gateway.DeviceView{}, errors.New("niet bereikbaar")
	}
	state := "off"
	if on {
		state = "on"
	}
	f.switched = append(f.switched, id+"="+state)
	return f.views[id], nil
}

func (f *fakeDevices) set(id, name, status string, values map[string]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := gateway.DeviceView{ID: id, Name: name, Kind: homewizard.KindSocket, Status: status}
	if values != nil {
		v.Reading = &homewizard.Reading{Time: time.Now(), Values: values}
	}
	f.views[id] = v
}

type harness struct {
	t      *testing.T
	st     *store.Store
	dev    *fakeDevices
	eng    *Engine
	mu     sync.Mutex
	sent   []Message
	now    time.Time
	prices map[string]float64
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hwlocal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	h := &harness{t: t, st: st, dev: &fakeDevices{views: map[string]gateway.DeviceView{}}, now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	h.eng = h.engine()
	return h
}

func (h *harness) engine() *Engine {
	return &Engine{
		Store: h.st, Devices: h.dev, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		retryPause: time.Millisecond, verifyWait: 5 * time.Millisecond, verifyTick: time.Millisecond,
		Price: func(_ context.Context, metric string, _ time.Time) (float64, bool) {
			p, ok := h.prices[metric]
			return p, ok
		},
		Notify: func(_ context.Context, m Message) error {
			h.mu.Lock()
			defer h.mu.Unlock()
			h.sent = append(h.sent, m)
			return nil
		},
	}
}

// step advances the clock and evaluates, waiting for the actions.
func (h *harness) step(d time.Duration) {
	h.now = h.now.Add(d)
	h.eng.Evaluate(context.Background(), h.now)
	h.eng.wg.Wait()
}

func (h *harness) messages() []Message {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Message(nil), h.sent...)
}

func dryerRule() Rule {
	return Rule{
		Name: "Droger klaar", Enabled: true, ForSeconds: 180,
		Conditions: []Condition{{Device: "dryer", Metric: "power_w", Op: OpBelow, Value: 5}},
		Actions:    []Action{{Type: ActionNotify}},
	}
}

func TestDryerFiresOnceAfterDuration(t *testing.T) {
	h := newHarness(t)
	h.dev.set("dryer", "Droger", gateway.StatusOnline, map[string]float64{"power_w": 2})
	if _, err := h.eng.Save(context.Background(), dryerRule()); err != nil {
		t.Fatal(err)
	}
	// standby when the rule is made: it must not fire
	for range 200 {
		h.step(2 * time.Second)
	}
	if n := len(h.messages()); n != 0 {
		t.Fatalf("fired %d times while the dryer never ran", n)
	}
	// the dryer runs, then finishes
	h.dev.set("dryer", "Droger", gateway.StatusOnline, map[string]float64{"power_w": 2100})
	h.step(2 * time.Second)
	h.dev.set("dryer", "Droger", gateway.StatusOnline, map[string]float64{"power_w": 3})
	h.step(2 * time.Second) // conditions hold from here
	h.step(178 * time.Second)
	if n := len(h.messages()); n != 0 {
		t.Fatalf("fired before 3 minutes (%d)", n)
	}
	h.step(2 * time.Second)
	msgs := h.messages()
	if len(msgs) != 1 {
		t.Fatalf("got %d messages, want 1", len(msgs))
	}
	if msgs[0].Title != "Droger klaar" || !strings.Contains(msgs[0].Body, "Droger: vermogen 3 W (lager dan 5 W) sinds 3 min") {
		t.Fatalf("message %+v", msgs[0])
	}
	for range 100 {
		h.step(10 * time.Second)
	}
	if n := len(h.messages()); n != 1 {
		t.Fatalf("fired again while still below 5 W: %d", n)
	}
	v := h.eng.List()[0]
	if v.State.Armed || v.State.LastFired == nil || v.State.LastResult != "Uitgevoerd" {
		t.Fatalf("state %+v", v.State)
	}
}

func TestUnknownNeitherFiresNorArms(t *testing.T) {
	h := newHarness(t)
	h.dev.set("dryer", "Droger", gateway.StatusOffline, nil)
	if _, err := h.eng.Save(context.Background(), dryerRule()); err != nil {
		t.Fatal(err)
	}
	h.step(time.Minute)
	if h.eng.List()[0].State.Armed {
		t.Fatal("armed while the device is offline")
	}
	h.dev.set("dryer", "Droger", gateway.StatusOnline, map[string]float64{"power_w": 900})
	h.step(2 * time.Second)
	h.dev.set("dryer", "Droger", gateway.StatusOnline, map[string]float64{"power_w": 1})
	h.step(2 * time.Second)
	h.step(100 * time.Second)
	// an offline spell restarts the timer
	h.dev.set("dryer", "Droger", gateway.StatusOffline, map[string]float64{"power_w": 1})
	h.step(2 * time.Second)
	h.dev.set("dryer", "Droger", gateway.StatusOnline, map[string]float64{"power_w": 1})
	h.step(2 * time.Second)
	h.step(100 * time.Second)
	if n := len(h.messages()); n != 0 {
		t.Fatalf("fired although the timer was interrupted (%d)", n)
	}
	h.step(80 * time.Second)
	if n := len(h.messages()); n != 1 {
		t.Fatalf("got %d messages, want 1", n)
	}
}

func TestAndWithPriceAndSwitch(t *testing.T) {
	h := newHarness(t)
	h.dev.set("bat", "Accu", gateway.StatusOnline, map[string]float64{"state_of_charge_pct": 100})
	h.dev.set("boiler", "Boiler", gateway.StatusOnline, map[string]float64{"power_w": 0})
	h.prices = map[string]float64{MetricPrice: 0.05}
	_, err := h.eng.Save(context.Background(), Rule{
		Name: "Overschot", Enabled: true,
		Conditions: []Condition{
			{Device: "bat", Metric: "state_of_charge_pct", Op: OpEqual, Value: 100},
			{Device: PriceDevice, Metric: MetricPrice, Op: OpBelow, Value: 0},
		},
		Actions: []Action{{Type: ActionSwitch, Device: "boiler", On: true}, {Type: ActionNotify}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second) // price positive: false, arms
	h.prices = map[string]float64{MetricPrice: -0.012}
	h.step(2 * time.Second)
	if got := h.dev.switched; len(got) != 1 || got[0] != "boiler=on" {
		t.Fatalf("switched %v", got)
	}
	msgs := h.messages()
	if len(msgs) != 1 || msgs[0].Body != "Afgegaan." {
		t.Fatalf("messages %+v", msgs)
	}
}

func TestSwitchFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.dev.set("w", "Waterleiding", gateway.StatusOnline, map[string]float64{"water_lpm": 0})
	h.dev.set("pump", "Pomp", gateway.StatusOnline, nil)
	h.dev.failOn = "pump"
	_, err := h.eng.Save(context.Background(), Rule{
		Name: "Lek", Enabled: true, ForSeconds: 1800, Message: "Er loopt al een half uur water.",
		Conditions: []Condition{{Device: "w", Metric: "water_lpm", Op: OpAbove, Value: 0}},
		Actions:    []Action{{Type: ActionNotify}, {Type: ActionSwitch, Device: "pump"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	h.dev.set("w", "Waterleiding", gateway.StatusOnline, map[string]float64{"water_lpm": 0.4})
	h.step(2 * time.Second)
	h.step(30 * time.Minute)
	msgs := h.messages()
	if len(msgs) != 1 || !strings.HasPrefix(msgs[0].Body, "Er loopt al een half uur water.") || !strings.Contains(msgs[0].Body, "Pomp uitzetten mislukt") {
		t.Fatalf("messages %+v", msgs)
	}
	if r := h.eng.List()[0].State.LastResult; !strings.Contains(r, "mislukt") {
		t.Fatalf("result %q", r)
	}
}

func TestArmedSurvivesRestart(t *testing.T) {
	h := newHarness(t)
	h.dev.set("w", "Water", gateway.StatusOnline, map[string]float64{"water_lpm": 0})
	rule := Rule{
		Name: "Lek", Enabled: true, ForSeconds: 60,
		Conditions: []Condition{{Device: "w", Metric: "water_lpm", Op: OpAbove, Value: 0}},
		Actions:    []Action{{Type: ActionNotify}},
	}
	if _, err := h.eng.Save(context.Background(), rule); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second) // armed
	// restart while water runs: an armed rule still fires
	h.eng = h.engine()
	if err := h.eng.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !h.eng.List()[0].State.Armed {
		t.Fatal("armed state lost on restart")
	}
	h.dev.set("w", "Water", gateway.StatusOnline, map[string]float64{"water_lpm": 1})
	h.step(2 * time.Second)
	h.step(time.Minute)
	if n := len(h.messages()); n != 1 {
		t.Fatalf("got %d messages, want 1", n)
	}
	// and after it fired, a restart does not repeat it
	h.eng = h.engine()
	if err := h.eng.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	h.step(2 * time.Minute)
	if n := len(h.messages()); n != 1 {
		t.Fatalf("repeated after restart: %d", n)
	}
}

func TestValidation(t *testing.T) {
	h := newHarness(t)
	bad := []Rule{
		{Name: "", Conditions: dryerRule().Conditions, Actions: dryerRule().Actions},
		{Name: "x", Actions: dryerRule().Actions},
		{Name: "x", Kind: KindAutomation, Conditions: dryerRule().Conditions, Actions: dryerRule().Actions},
		{Name: "x", Kind: KindAlert, Conditions: dryerRule().Conditions, Actions: []Action{{Type: ActionSwitch, Device: "pump"}}},
		{Name: "x", Kind: "robot", Conditions: dryerRule().Conditions},
		{Name: "x", Match: "some", Conditions: dryerRule().Conditions},
		{Name: "x", Conditions: []Condition{{Device: PriceDevice, Metric: "oil_price", Op: OpBelow}}},
		{Name: "x", Conditions: []Condition{{Device: "d", Metric: "power_w", Op: OpBelow, Ref: &Operand{Device: TimeDevice, Metric: MetricWindow}}}},
		{Name: "x", Conditions: []Condition{{Device: "d", Metric: "power_w", Op: OpBelow, Ref: &Operand{Device: ""}}}},
		{Name: "x", Conditions: []Condition{{Device: "d", Metric: "power_w", Op: "~"}}, Actions: dryerRule().Actions},
		{Name: "x", Conditions: []Condition{{Device: PriceDevice, Metric: "power_w", Op: OpBelow}}, Actions: dryerRule().Actions},
		{Name: "x", Conditions: dryerRule().Conditions, Actions: []Action{{Type: ActionSwitch}}},
		{Name: "x", Conditions: dryerRule().Conditions, Actions: []Action{{Type: "mail"}}},
	}
	for i, r := range bad {
		if _, err := h.eng.Save(context.Background(), r); !errors.Is(err, ErrInvalid) {
			t.Errorf("rule %d: err %v, want ErrInvalid", i, err)
		}
	}
	if err := h.eng.Delete(context.Background(), 99); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("delete missing: %v", err)
	}
}
