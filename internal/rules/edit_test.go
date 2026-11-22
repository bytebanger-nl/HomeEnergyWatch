package rules

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// Without boost, regulating starts at target + offset and steps from there.
func TestStartWithoutBoost(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.1))
	pl.putAC(false, 23)
	a := regulateAC(false, false)
	a.Offset = 1 // start at 20
	if _, err := h.eng.Save(context.Background(), Rule{
		Name: "Airco houdt 19", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 25}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, a},
	}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	pl.putAC(true, 23)
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=20") // within the band, still to the start value
	pl.putAC(true, 20)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18.5))
	h.step(14 * time.Minute)
	wantSets(t, pl)
	h.step(2 * time.Minute)
	wantSets(t, pl, "ac.preset=20.5")
}

// A rule edited while it waits goes on waiting: the actions before the
// wait are not sent again, those after it (as edited) follow.
func TestEditWhileWaiting(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	ctx := context.Background()
	v, err := h.eng.Save(ctx, Rule{
		Name: "Wachten", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 19.0},
			{Type: ActionWait, MaxSeconds: 600},
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "schedule"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=19")

	r := v.Rule
	r.Name = "Wachten tot warm"
	r.Conditions = []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18}}
	r.Actions = []Action{
		{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 20.0},
		{Type: ActionWait, MaxSeconds: 900},
		{Type: ActionPluginSet, Device: "plugin:tado", Key: "schedule"},
	}
	got, err := h.eng.Save(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Armed || got.State.Run == nil || got.State.Run.Type != ActionWait {
		t.Fatalf("state %+v", got.State)
	}
	h.step(time.Minute)
	wantSets(t, pl) // not set off again
	h.step(10 * time.Minute)
	wantSets(t, pl) // the new wait: 15 minutes from the start
	h.step(5 * time.Minute)
	wantSets(t, pl, "tado.schedule=<nil>")
}

// An automation that went off and is edited while its conditions still
// hold does not go off again; it does once they were false.
func TestEditAfterFiring(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putAC(false, 21)
	ctx := context.Background()
	v, err := h.eng.Save(ctx, Rule{
		Name: "Airco aan", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")

	r := v.Rule
	r.Actions = []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "mode", Value: "HEAT"}}
	if got, err := h.eng.Save(ctx, r); err != nil || got.State.Armed {
		t.Fatalf("save: %+v %v", got.State, err)
	}
	h.step(time.Minute)
	wantSets(t, pl)

	pl.put("tado", "Woonkamer", true, tadoControls, temp(19))
	h.step(time.Minute)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	h.step(time.Minute)
	wantSets(t, pl, "ac.mode=HEAT")
}

// A run stored by a version without the start at target + offset goes on
// from where it is after an upgrade.
func TestStoredRunWithoutPhase(t *testing.T) {
	h, pl := regulatingAC(t) // at 21.5; start value 21
	ctx := context.Background()
	runs, err := h.st.RuleRuns(ctx)
	if err != nil || len(runs) != 1 {
		t.Fatalf("runs %v %v", runs, err)
	}
	for id, data := range runs {
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		delete(m, "phase")
		data, _ = json.Marshal(m)
		if err := h.st.SaveRuleRun(ctx, id, data); err != nil {
			t.Fatal(err)
		}
	}
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19))
	h.step(time.Minute)
	wantSets(t, pl)
}
