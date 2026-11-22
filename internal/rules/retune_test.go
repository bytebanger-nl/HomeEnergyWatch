package rules

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// Saving a regulating automation keeps the run (also after a restart) and
// does not set it off again, also when other actions changed.
func TestSaveKeepsRegulating(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	boostRule(h, pl, 19.1) // within the band: regulating, no boost
	ctx := context.Background()
	v := h.eng.List()[0]
	if v.State.Run == nil || v.State.Armed {
		t.Fatalf("not regulating: %+v", v.State)
	}

	// the rule is edited while regulating: target 18.5, band 0.3, offset,
	// range and name: still regulating
	r := v.Rule
	r.Actions = append([]Action(nil), v.Actions...)
	reg := r.Actions[1]
	reg.Target, reg.Band, reg.Offset, reg.Below = 18.5, 0.3, 2, 0.5
	r.Actions[1], r.Name = reg, "Airco houdt 18,5"
	got, err := h.eng.Save(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Run == nil || got.State.Armed {
		t.Fatalf("save ended the run: %+v", got.State)
	}
	if runs, _ := h.st.RuleRuns(ctx); len(runs) != 1 {
		t.Fatalf("stored runs %v", runs)
	}
	// the run had not decided its boost phase yet and boost is still on:
	// 19.1 > 18.8, so it cools at the bottom of the new range (18)
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=18")
	pl.putAC(true, 18)

	// a restart has the new settings and the run
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	cur := h.eng.List()[0]
	if cur.State.Run == nil || cur.Actions[1].Target != 18.5 || cur.Name != "Airco houdt 18,5" {
		t.Fatalf("after restart %+v", cur)
	}

	// another device action changed: still regulating, and that action is
	// not sent now (the rule does not go off again)
	r = cur.Rule
	r.Actions = append([]Action(nil), cur.Actions...)
	r.Actions[0] = Action{Type: ActionPluginSet, Device: "plugin:ac", Key: "mode", Value: "HEAT"}
	got, err = h.eng.Save(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Run == nil || got.State.Armed {
		t.Fatalf("other change ended the run: %+v", got.State)
	}
	h.step(time.Minute)
	wantSets(t, pl)
}

// A condition change keeps regulating without going off again; switching
// the rule off ends the run; regulating on another measured value starts
// the rule over.
func TestSaveConditionEndsRegulating(t *testing.T) {
	for name, want := range map[string]struct {
		change func(*Rule)
		run    bool
		armed  bool
	}{
		"condition": {func(r *Rule) { r.Conditions[0].Value = 24 }, true, false},
		"disabled":  {func(r *Rule) { r.Enabled = false }, false, true},
		"sensor":    {func(r *Rule) { r.Actions[1].Sensor = &Operand{Device: "plugin:ac", Metric: "preset"} }, false, true},
	} {
		h := newHarness(t)
		pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
		boostRule(h, pl, 19.1)
		v := h.eng.List()[0]
		r := v.Rule
		r.Conditions = append([]Condition(nil), v.Conditions...)
		r.Actions = append([]Action(nil), v.Actions...)
		want.change(&r)
		got, err := h.eng.Save(context.Background(), r)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if (got.State.Run != nil) != want.run || got.State.Armed != want.armed {
			t.Errorf("%s: run %v armed %v", name, got.State.Run != nil, got.State.Armed)
		}
	}
}

// Boost switched off while boosting: the setpoint lands on target + offset
// at the next check, then it is stepped as usual.
func TestSaveBoostOff(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	boostRule(h, pl, 17)
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=22")
	pl.putAC(true, 22)
	v := h.eng.List()[0]
	r := v.Rule
	r.Actions = append([]Action(nil), v.Actions...)
	r.Actions[1].Boost = false
	got, err := h.eng.Save(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	if got.State.Run == nil || got.State.Run.Phase != PhaseRegulate {
		t.Fatalf("run %+v", got.State.Run)
	}
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=20") // 19 + 1, not one step down from 22
	pl.putAC(true, 20)
	h.step(10 * time.Minute)
	wantSets(t, pl) // the next step only after the interval
	h.step(6 * time.Minute)
	wantSets(t, pl, "ac.preset=20.5") // still 17 °C: one step up
}
