package rules

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// regulatingAC starts a rule that regulates without boost and has stepped
// once, so the run is in its regulate phase with the setpoint at 21.5.
func regulatingAC(t *testing.T) (*harness, *fakePlugins) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18))
	pl.putAC(false, 21)
	a := regulateAC(false, false) // target 19 ± 0.2, range 20..23
	if _, err := h.eng.Save(context.Background(), Rule{
		Name: "Airco houdt 19", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 25}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, a},
	}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	pl.putAC(true, 21)
	h.step(16 * time.Minute)
	wantSets(t, pl, "ac.preset=21.5")
	pl.putAC(true, 21.5)
	return h, pl
}

func edit(h *harness, f func(a *Action)) Rule {
	v := h.eng.List()[0]
	r := v.Rule
	r.Actions = append([]Action(nil), v.Actions...)
	f(&r.Actions[len(r.Actions)-1])
	return r
}

// A narrower range moves a setpoint outside it back in at the next check,
// even while the room is within the band; through Save and through Tune.
func TestRetuneNarrowerRange(t *testing.T) {
	h, pl := regulatingAC(t)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19)) // within the band
	r := edit(h, func(a *Action) { a.Above = 2 })             // max 21
	if v, err := h.eng.Save(context.Background(), r); err != nil || v.State.Run == nil {
		t.Fatalf("save: %+v %v", v.State, err)
	}
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=21")
	pl.putAC(true, 21)
	h.step(time.Minute)
	wantSets(t, pl)

	// Tune moves the range with the target: 18 ± (1, 2) is 17..20
	if v, err := h.eng.Tune(context.Background(), r.ID, 18, 0.2); err != nil || v.State.Run == nil {
		t.Fatalf("tune: %+v %v", v.State, err)
	}
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=20")
}

// Boost switched on while regulating only applies to a new start.
func TestRetuneBoostOnWhileRegulating(t *testing.T) {
	h, pl := regulatingAC(t)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17)) // far below the band
	r := edit(h, func(a *Action) { a.Boost = true })
	v, err := h.eng.Save(context.Background(), r)
	if err != nil || v.State.Run == nil || v.State.Run.Phase != PhaseRegulate {
		t.Fatalf("save: %+v %v", v.State.Run, err)
	}
	h.step(time.Minute)
	wantSets(t, pl) // no jump to the maximum, and still inside the range
	h.step(15 * time.Minute)
	wantSets(t, pl, "ac.preset=22") // one step, as before
}

// Saving after the run ended (the air conditioner was switched off by hand)
// keeps the rule as it is: it went off already and does not switch the air
// conditioner on again while its conditions hold.
func TestRetuneAfterRunEnded(t *testing.T) {
	h, pl := regulatingAC(t)
	pl.putAC(false, 21.5) // switched off by hand: the run stops
	h.step(time.Minute)
	if v := h.eng.List()[0]; v.State.Run != nil {
		t.Fatalf("run %+v", v.State.Run)
	}
	r := edit(h, func(a *Action) { a.Target = 19.5 })
	v, err := h.eng.Save(context.Background(), r)
	if err != nil || v.State.Run != nil || v.State.Armed {
		t.Fatalf("save: %+v %v", v.State, err)
	}
	h.step(time.Minute)
	wantSets(t, pl)
}

// A correction after a retune does not push back a step that was due: the
// step follows a minute later, not a whole interval.
func TestRetuneKeepsDueStep(t *testing.T) {
	h, pl := regulatingAC(t) // stepped to 21.5; next check in 15 min
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19))
	h.step(14 * time.Minute)
	wantSets(t, pl)
	// target 21 moves the range to 22..25: 21.5 is below it
	if _, err := h.eng.Tune(context.Background(), h.eng.List()[0].ID, 21, 0.2); err != nil {
		t.Fatal(err)
	}
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=22")
	pl.putAC(true, 22)
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=22.5")
}
