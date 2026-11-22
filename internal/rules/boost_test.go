package rules

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// boostAC regulates tado at 19 ± 0.2 with the air conditioner at 19 + 1,
// between 18 and 22 (1 below and 3 above the target), boosting first.
func boostAC() Action {
	return Action{Type: ActionRegulate, Device: "plugin:ac", Key: "preset",
		Sensor: &Operand{Device: "plugin:tado", Metric: "temp"},
		Target: 19, Band: 0.2, Step: 0.5, EverySeconds: 900,
		Offset: 1, Below: 1, Above: 3, Boost: true, NotifySteps: true}
}

func boostRule(h *harness, pl *fakePlugins, room float64) {
	h.t.Helper()
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(room))
	pl.putAC(false, 21)
	if _, err := h.eng.Save(context.Background(), Rule{
		Name: "Airco houdt 19", Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 25}},
		Actions:    []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, boostAC()},
	}); err != nil {
		h.t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(h.t, pl, "ac.power=true")
	pl.putAC(true, 21)
}

func runPhase(h *harness) string {
	if r := h.eng.List()[0].State.Run; r != nil {
		return r.Phase
	}
	return "<geen>"
}

// Too cold: the air conditioner goes to the top of its range at once, back
// to target + offset once the room is within the band, and is then stepped
// as usual; a restart keeps the phase.
func TestRegulateBoostHeat(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	boostRule(h, pl, 17)

	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=22")
	pl.putAC(true, 22)
	if p := runPhase(h); p != PhaseHeat {
		t.Fatalf("phase %q, want %q", p, PhaseHeat)
	}
	if m := h.messages(); len(m) != 1 || m[0].Body != "Snel opwarmen: Airco op 22 °C." {
		t.Fatalf("messages %+v", m)
	}
	// warming up: nothing, also not after the usual 15 minutes
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18.5))
	h.step(20 * time.Minute)
	wantSets(t, pl)

	// a restart keeps boosting
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p := runPhase(h); p != PhaseHeat {
		t.Fatalf("phase after restart %q", p)
	}

	// 18.8 = target - band: back to 19 + 1
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18.8))
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=20")
	pl.putAC(true, 20)
	if p := runPhase(h); p != PhaseRegulate {
		t.Fatalf("phase %q, want %q", p, PhaseRegulate)
	}
	if m := h.messages(); len(m) != 2 || m[1].Body != "Doel bijna bereikt: Airco terug naar 20 °C." {
		t.Fatalf("messages %+v", m)
	}
	// then as usual: the next check every_s later
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18.7))
	h.step(10 * time.Minute)
	wantSets(t, pl)
	h.step(6 * time.Minute)
	wantSets(t, pl, "ac.preset=20.5")
}

// Too warm: the bottom of the range first.
func TestRegulateBoostCool(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	boostRule(h, pl, 21)
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=18")
	pl.putAC(true, 18)
	if p := runPhase(h); p != PhaseCool {
		t.Fatalf("phase %q", p)
	}
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.2))
	h.step(time.Minute)
	wantSets(t, pl, "ac.preset=20")
}

// Within the band already: no boost, regulating as usual.
func TestRegulateBoostWithinBand(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	boostRule(h, pl, 19.1)
	h.step(time.Minute)
	wantSets(t, pl)
	if p := runPhase(h); p != PhaseRegulate {
		t.Fatalf("phase %q", p)
	}
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19.5))
	h.step(15 * time.Minute)
	wantSets(t, pl, "ac.preset=20.5")
}

// The range lies around the target, not around target + offset, and
// moves when the target is tuned; a loaded rule keeps the limits it was
// stored with.
func TestRegulateRange(t *testing.T) {
	a, err := normalizeRegulate(boostAC())
	if err != nil {
		t.Fatal(err)
	}
	if a.Min != 18 || a.Max != 22 {
		t.Fatalf("range %v-%v, want 18-22", a.Min, a.Max)
	}
	more := boostAC()
	more.Offset = 3
	if a, _ := normalizeRegulate(more); a.Min != 18 || a.Max != 22 {
		t.Fatalf("offset moved the range: %v-%v", a.Min, a.Max)
	}
	plain := boostAC()
	plain.Offset, plain.Below, plain.Above = 0, 0, 0
	if a, _ := normalizeRegulate(plain); a.Min != 19-DefaultRange || a.Max != 19+DefaultRange {
		t.Fatalf("default range %v-%v", a.Min, a.Max)
	}
	// fixed limits only (before there was a range): the same limits, the
	// offset closest to 0 within them
	old := regulateAC(false, false) // 19, fixed 20-23
	old.Offset = 0
	if a, _ := normalizeRegulate(old); a.Min != 20 || a.Max != 23 || a.Offset != 1 || a.Below != -1 || a.Above != 4 {
		t.Fatalf("legacy %+v", a)
	}
	// stored by 0.1.95 (range around target + offset): the stored limits
	v95 := regulateAC(false, false)
	v95.Offset, v95.Below, v95.Above = 2.5, 1.5, 1.5
	storedRange(&v95)
	if v95.Min != 20 || v95.Max != 23 || v95.Offset != 2.5 || v95.Below != -1 || v95.Above != 4 {
		t.Fatalf("0.1.95 rule %+v", v95)
	}
	for name, bad := range map[string]func(*Action){
		"offset": func(a *Action) { a.Offset = 11 },
		"below":  func(a *Action) { a.Below = -11 },
		"above":  func(a *Action) { a.Above = 10.5 },
		"empty":  func(a *Action) { a.Below, a.Above = 2, -2 },
	} {
		x := boostAC()
		bad(&x)
		if _, err := normalizeRegulate(x); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	boostRule(h, pl, 19.1)
	v := h.eng.List()[0]
	if _, err := h.eng.Tune(context.Background(), v.ID, 20, 0.2); err != nil {
		t.Fatal(err)
	}
	if a := h.eng.List()[0].Actions[1]; a.Min != 19 || a.Max != 23 || a.Offset != 1 {
		t.Fatalf("tuned %+v, want 19-23, offset 1", a)
	}
}
