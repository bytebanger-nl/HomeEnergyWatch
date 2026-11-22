package rules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

var acControls = []plugin.Control{
	{Key: "power", Label: "Aan", Kind: plugin.ControlSwitch, Metric: "power"},
	{Key: "mode", Label: "Modus", Kind: plugin.ControlSelect, Metric: "mode", Options: []plugin.Option{{Value: "HEAT", Label: "Verwarmen"}}},
	{Key: "preset", Label: "Temperatuur", Kind: plugin.ControlSetpoint, Metric: "preset", Min: 18, Max: 30, Step: 0.5, Unit: "°C"},
}

// putAC puts an air conditioner that may be regulated.
func (f *fakePlugins) putAC(on bool, preset float64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views["ac"] = plugin.DeviceView{ID: "ac", Name: "Airco", Controls: acControls, CanRegulate: true,
		Reading: &plugin.Reading{Online: true, Metrics: []plugin.Metric{
			{Key: "power", Label: "Aan", Kind: plugin.MetricBool, Value: on},
			{Key: "preset", Label: "Ingesteld", Kind: plugin.MetricNumber, Value: preset, Unit: "°C"},
		}}}
}

func (f *fakePlugins) takeSets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.sets
	f.sets = nil
	return out
}

// regulateAC regulates the air conditioner on tado: 19 ± 0.2 °C, range
// 20..23, starting at 19 + 2 = 21 (where the tests' air conditioner is set
// already, so the start sends nothing).
func regulateAC(stopWindow, offAtEnd bool) Action {
	return Action{Type: ActionRegulate, Device: "plugin:ac", Key: "preset",
		Sensor: &Operand{Device: "plugin:tado", Metric: "temp"},
		Target: 19, Band: 0.2, Step: 0.5, EverySeconds: 900, Min: 20, Max: 23, Offset: 2,
		StopWindow: stopWindow, OffAtEnd: offAtEnd}
}

func wantSets(t *testing.T, pl *fakePlugins, want ...string) {
	t.Helper()
	got := pl.takeSets()
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("sets %v, want %v", got, want)
	}
}

// The central heating warms up first; after 30 minutes (or at 19 °C) the
// air conditioner takes over and is stepped to keep tado at 19 ± 0.2 °C,
// also after a restart, until it is switched off.
func TestWaitThenRegulate(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putAC(false, 21)
	ctx := context.Background()

	saved, err := h.eng.Save(ctx, Rule{
		Name: "Verwarmen naar 19", Enabled: true,
		Conditions: []Condition{
			{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5},
			{Device: "plugin:ac", Metric: "power", Op: OpEqual, Value: 0},
		},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 19.0},
			{Type: ActionWait, Until: &Condition{Device: "plugin:tado", Metric: "temp", Op: OpAboveOrEqual, Value: 19}, MaxSeconds: 1800},
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "schedule"},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "preset", Value: 21.0},
			regulateAC(false, false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=19")
	if r := h.eng.List()[0].State.Run; r == nil || r.Type != ActionWait || r.Step != 2 {
		t.Fatalf("run %+v, want the wait (action 2)", r)
	}
	// waiting is not regulating: no period yet (the chart stays away)
	if m, err := h.eng.LastRegulated(ctx); err != nil || len(m) != 0 {
		t.Fatalf("regulated while waiting: %v %v", m, err)
	}
	// warming up, not at 19 yet: no matter that the conditions no longer hold
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18.6))
	h.step(10 * time.Minute)
	wantSets(t, pl)
	// 30 minutes: the air conditioner takes over
	h.step(21 * time.Minute)
	wantSets(t, pl, "tado.schedule=<nil>", "ac.power=true", "ac.preset=21")
	if r := h.eng.List()[0].State.Run; r == nil || r.Type != ActionRegulate {
		t.Fatalf("run %+v, want regulate", r)
	}
	all := func() []store.Period {
		t.Helper()
		p, err := h.eng.RegulatePeriods(ctx, saved.ID, time.Unix(0, 0), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := all(); len(p) != 1 || p[0].End != nil || !p[0].Start.Equal(h.now) {
		t.Fatalf("periods %+v, want one open from %v", p, h.now)
	}

	pl.putAC(true, 21)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.6))
	h.step(10 * time.Minute) // the first check is 15 minutes after the start
	wantSets(t, pl)
	h.step(6 * time.Minute)
	wantSets(t, pl, "ac.preset=20.5")
	pl.putAC(true, 20.5)
	h.step(15 * time.Minute)
	wantSets(t, pl, "ac.preset=20")
	pl.putAC(true, 20)
	h.step(15 * time.Minute) // still too warm, but 20 is the minimum
	wantSets(t, pl)
	if res := h.eng.List()[0].State.LastResult; !strings.Contains(res, "minimum") {
		t.Fatalf("result %q, want the minimum", res)
	}
	// within the band: nothing
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.1))
	h.step(15 * time.Minute)
	wantSets(t, pl)

	// a restart goes on where it was
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if r := h.eng.List()[0].State.Run; r == nil || r.Type != ActionRegulate {
		t.Fatalf("run after restart %+v, want regulate", r)
	}
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18.7))
	h.step(15 * time.Minute)
	wantSets(t, pl, "ac.preset=20.5")
	pl.putAC(true, 20.5)

	// switched off by hand: regulating stops
	pl.putAC(false, 20.5)
	h.step(2 * time.Second)
	v := h.eng.List()[0]
	if v.State.Run != nil || !strings.Contains(v.State.LastResult, "staat uit") {
		t.Fatalf("still running after switching off: %+v", v.State)
	}
	if runs, _ := h.st.RuleRuns(ctx); len(runs) != 0 {
		t.Fatalf("stored run left: %v", runs)
	}
	// the restart kept the one period; switching off ended it
	if p := all(); len(p) != 1 || p[0].End == nil {
		t.Fatalf("periods %+v, want one that ended", p)
	}
	_ = saved
}

// The wait ends as soon as its condition holds.
func TestWaitEndsEarly(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putAC(false, 21)
	ctx := context.Background()
	if _, err := h.eng.Save(ctx, Rule{
		Name: "Voorverwarmen", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 19.0},
			{Type: ActionWait, Until: &Condition{Device: "plugin:tado", Metric: "temp", Op: OpAboveOrEqual, Value: 19}, MaxSeconds: 1800},
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "schedule"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=19")
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.0))
	h.step(5 * time.Minute)
	wantSets(t, pl, "tado.schedule=<nil>")
	if v := h.eng.List()[0]; v.State.Run != nil {
		t.Fatalf("run %+v after the last action", v.State.Run)
	}
}

// With StopWindow the run ends with the time window and switches the
// device off.
func TestRegulateStopsWithWindow(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Slaapkamer", true, tadoControls, temp(18))
	pl.putAC(false, 21)
	ctx := context.Background()
	if _, err := h.eng.Save(ctx, Rule{
		Name: "Airco houdt 19", Enabled: true,
		Conditions: []Condition{{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{1, 2, 3, 4, 5, 6, 7}, From: "07:00", To: "22:00"}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
			regulateAC(true, true),
		},
	}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	pl.putAC(true, 21)
	h.step(16 * time.Minute)
	wantSets(t, pl, "ac.preset=21.5")
	h.step(45 * time.Minute) // past 22:00
	wantSets(t, pl, "ac.power=false")
	if v := h.eng.List()[0]; v.State.Run != nil {
		t.Fatalf("still running after the window: %+v", v.State.Run)
	}
}

// With NotifySteps every step and the stop send a push message; without it
// regulating is silent.
func TestRegulateNotifySteps(t *testing.T) {
	h := newHarness(t)
	h.now = time.Date(2026, 11, 8, 21, 0, 0, 0, time.UTC)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18))
	pl.putAC(true, 21)
	ctx := context.Background()
	reg := regulateAC(true, true)
	reg.NotifySteps = true
	v, err := h.eng.Save(ctx, Rule{
		Name: "Airco houdt 19", Enabled: true,
		Conditions: []Condition{{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{1, 2, 3, 4, 5, 6, 7}, From: "07:00", To: "22:00"}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, reg},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Actions[1].NotifySteps {
		t.Fatal("notify_steps not kept")
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	if n := len(h.messages()); n != 0 {
		t.Fatalf("%d messages before the first step", n)
	}
	h.step(16 * time.Minute)
	wantSets(t, pl, "ac.preset=21.5")
	msgs := h.messages()
	if len(msgs) != 1 || msgs[0].Body != "Bijgeregeld van 21 °C naar 21,5 °C." {
		t.Fatalf("step message %+v", msgs)
	}
	h.step(45 * time.Minute) // past 22:00
	wantSets(t, pl, "ac.power=false")
	msgs = h.messages()
	if len(msgs) != 2 || msgs[1].Body != "Gestopt: het tijdvenster is voorbij." {
		t.Fatalf("stop message %+v", msgs)
	}

	// switched off by hand: the stop is told too
	h2 := newHarness(t)
	pl2 := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h2.eng.Plugins = pl2
	pl2.put("tado", "Woonkamer", true, tadoControls, temp(18))
	pl2.putAC(true, 21)
	if _, err := h2.eng.Save(ctx, Rule{Name: "Airco", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 19}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, func() Action { a := regulateAC(false, false); a.NotifySteps = true; return a }()},
	}); err != nil {
		t.Fatal(err)
	}
	h2.step(2 * time.Second)
	h2.step(2 * time.Second) // seen on
	pl2.putAC(false, 21)
	h2.step(2 * time.Second)
	if m := h2.messages(); len(m) != 1 || !strings.Contains(m[0].Body, "staat uit") {
		t.Fatalf("stop by hand %+v", m)
	}
	if r := h2.eng.List()[0].State.LastResult; !strings.Contains(r, "staat uit") {
		t.Fatalf("result %q", r)
	}
}

// Saving a rule again (or switching it off) ends what it was doing.
func TestSaveEndsRun(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	ctx := context.Background()
	rule := Rule{
		Name: "Wachten", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 19.0},
			{Type: ActionWait, MaxSeconds: 600},
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "schedule"},
		},
	}
	v, err := h.eng.Save(ctx, rule)
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=19")
	rule.ID, rule.Enabled = v.ID, false
	if _, err := h.eng.Save(ctx, rule); err != nil {
		t.Fatal(err)
	}
	h.step(11 * time.Minute)
	wantSets(t, pl)
	if runs, _ := h.st.RuleRuns(ctx); len(runs) != 0 {
		t.Fatalf("stored run left: %v", runs)
	}
}

func TestStepValidation(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putAC(false, 21)
	cond := []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}}
	on := Action{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}
	tado := regulateAC(false, false)
	tado.Device, tado.Key = "plugin:tado", "target"
	tado.Min, tado.Max = 15, 22
	odd := regulateAC(false, false)
	odd.Step = 0.3
	wide := regulateAC(false, false)
	wide.Max = 35
	cases := map[string]Rule{
		"thermostat":         {Actions: []Action{on, tado}},
		"not last":           {Actions: []Action{regulateAC(false, false), on}},
		"two":                {Actions: []Action{regulateAC(false, false), regulateAC(false, false)}},
		"step":               {Actions: []Action{on, odd}},
		"range":              {Actions: []Action{on, wide}},
		"window missing":     {Actions: []Action{on, regulateAC(true, false)}},
		"off without window": {Actions: []Action{on, regulateAC(false, true)}},
		"wait in else":       {Actions: []Action{on}, ElseActions: []Action{{Type: ActionWait, MaxSeconds: 600}}},
		"wait too short":     {Actions: []Action{on, {Type: ActionWait, MaxSeconds: 10}}},
		"wait only":          {Actions: []Action{{Type: ActionWait, MaxSeconds: 600}, {Type: ActionNotify}}},
	}
	for name, r := range cases {
		r.Name, r.Enabled, r.Kind, r.Conditions = name, true, KindAutomation, cond
		if _, err := h.eng.Save(context.Background(), r); err == nil {
			t.Errorf("%s: saved, want an error", name)
		}
	}
	// and the right one is fine
	if _, err := h.eng.Save(context.Background(), Rule{Name: "ok", Enabled: true, Conditions: cond,
		Actions: []Action{on, regulateAC(false, false)}}); err != nil {
		t.Fatal(err)
	}
}

// Tune changes the target without ending the run or re-arming the rule.
func TestTuneKeepsRun(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.1))
	pl.putAC(false, 21)
	ctx := context.Background()
	v, err := h.eng.Save(ctx, Rule{
		Name: "Airco houdt 19", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 25}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, regulateAC(false, false)},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	pl.putAC(true, 21)
	h.step(16 * time.Minute) // 19.1 is within 19 ± 0.2
	wantSets(t, pl)
	if _, err := h.eng.Tune(ctx, v.ID, 18.5, 0.3); err != nil {
		t.Fatal(err)
	}
	got := h.eng.List()[0]
	if got.State.Run == nil || got.State.Armed {
		t.Fatalf("tune ended the run or re-armed: %+v", got.State)
	}
	h.step(15 * time.Minute) // now 19.1 > 18.8: one step down
	wantSets(t, pl, "ac.preset=20.5")
	// stored: a restart has the new target
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if a := h.eng.List()[0].Actions[1]; a.Target != 18.5 || a.Band != 0.3 {
		t.Fatalf("stored %v ± %v, want 18.5 ± 0.3", a.Target, a.Band)
	}
	if _, err := h.eng.Tune(ctx, v.ID, 19, 0); err == nil {
		t.Fatal("band 0 accepted")
	}
}
