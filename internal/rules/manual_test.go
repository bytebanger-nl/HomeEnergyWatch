package rules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// manualHarness has an air conditioner (off, at 21 °C) and tado at 18 °C.
func manualHarness(t *testing.T) (*harness, *fakePlugins) {
	t.Helper()
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(18))
	pl.putAC(false, 21)
	return h, pl
}

func saveManual(t *testing.T, h *harness, r Rule) View {
	t.Helper()
	r.Kind, r.Manual, r.Enabled = KindAutomation, true, true
	v, err := h.eng.Save(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func state(h *harness) State { return h.eng.List()[0].State }

// A switch: on runs the actions and regulates, off runs the else actions
// and regulating stops; on again starts over.
func TestManualSwitchRegulates(t *testing.T) {
	h, pl := manualHarness(t)
	ctx := context.Background()
	v := saveManual(t, h, Rule{Name: "Woonkamer 19°",
		Actions:     []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, regulateAC(false, false)},
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false}},
	})
	if v.State.Active || v.State.Momentary || len(v.Conditions) != 0 {
		t.Fatalf("new manual action %+v", v)
	}
	h.step(10 * time.Minute)
	wantSets(t, pl) // nothing until switched on

	if _, err := h.eng.Start(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	if s := state(h); !s.Active || s.Run == nil || s.Run.Type != ActionRegulate {
		t.Fatalf("after start %+v", s)
	}
	h.step(2 * time.Second)  // seen on
	h.step(16 * time.Minute) // too cold: one step up
	wantSets(t, pl, "ac.preset=21.5")

	// a restart keeps it on and regulating
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if s := state(h); !s.Active || s.Run == nil {
		t.Fatalf("after restart %+v", s)
	}

	if _, err := h.eng.Stop(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=false")
	h.step(2 * time.Second) // regulating sees the device off
	if s := state(h); s.Active || s.Run != nil || !s.Armed {
		t.Fatalf("after stop %+v", s)
	}
	h.step(20 * time.Minute)
	wantSets(t, pl)

	if _, err := h.eng.Start(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
}

// Switched off during a wait: the else actions run and the actions after
// the wait do not.
func TestManualStopEndsWait(t *testing.T) {
	h, pl := manualHarness(t)
	ctx := context.Background()
	v := saveManual(t, h, Rule{Name: "Eerst CV",
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 20.0},
			{Type: ActionWait, MaxSeconds: 1800},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
		},
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 16.0}},
	})
	h.eng.Start(ctx, v.ID)
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=20")
	h.eng.Stop(ctx, v.ID)
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=16")
	h.step(40 * time.Minute)
	wantSets(t, pl)
	if s := state(h); s.Active || s.Run != nil {
		t.Fatalf("after stop %+v", s)
	}
}

// A switch whose wait was its last step goes off by itself once the wait
// is over, without its else actions.
func TestManualSwitchOffAfterLastWait(t *testing.T) {
	h, pl := manualHarness(t)
	ctx := context.Background()
	v := saveManual(t, h, Rule{Name: "Een uur airco",
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
			{Type: ActionWait, MaxSeconds: 3600},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false},
		},
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false}},
	})
	h.eng.Start(ctx, v.ID)
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	h.step(61 * time.Minute)
	wantSets(t, pl, "ac.power=false")
	h.step(2 * time.Second)
	wantSets(t, pl) // not the else actions as well
	if s := state(h); s.Active || s.Run != nil || !s.Armed {
		t.Fatalf("after the wait %+v", s)
	}
}

// Switched off by hand (its own remote) while regulating: the action goes
// off without its else actions.
func TestManualOffWhenDeviceSwitchedOff(t *testing.T) {
	h, pl := manualHarness(t)
	ctx := context.Background()
	v := saveManual(t, h, Rule{Name: "Woonkamer 19°",
		Actions:     []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}, regulateAC(false, false)},
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 16.0}},
	})
	h.eng.Start(ctx, v.ID)
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true")
	h.step(2 * time.Second) // seen on
	pl.putAC(false, 21)
	h.step(2 * time.Second)
	h.step(2 * time.Second)
	wantSets(t, pl)
	s := state(h)
	if s.Active || s.Run != nil || !strings.Contains(s.LastResult, "staat uit") {
		t.Fatalf("after switching the device off %+v", s)
	}
	stored, err := h.st.Rules(ctx)
	if err != nil || len(stored) != 1 || stored[0].Active {
		t.Fatalf("stored %+v %v", stored, err)
	}
}

// A button runs its actions once and is ready again at once.
func TestManualButton(t *testing.T) {
	h, pl := manualHarness(t)
	ctx := context.Background()
	v := saveManual(t, h, Rule{Name: "Alles uit", Actions: []Action{
		{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false},
		{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 16.0},
	}})
	if !v.State.Momentary {
		t.Fatal("not a button")
	}
	for i := 0; i < 2; i++ {
		if _, err := h.eng.Start(ctx, v.ID); err != nil {
			t.Fatal(err)
		}
		h.step(2 * time.Second)
		wantSets(t, pl, "ac.power=false", "tado.target=16")
		if s := state(h); s.Active {
			t.Fatalf("button still on: %+v", s)
		}
		h.step(2 * time.Second)
		wantSets(t, pl)
		if s := state(h); !s.Armed {
			t.Fatalf("button not ready again: %+v", s)
		}
	}
	// a button pressed when the gateway stopped is not pressed again
	if err := h.st.SetRuleState(ctx, v.ID, false, true); err != nil {
		t.Fatal(err)
	}
	h.eng = h.engine()
	h.eng.Plugins = pl
	if err := h.eng.Load(ctx); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl)
	if s := state(h); s.Active || !s.Armed {
		t.Fatalf("after restart %+v", s)
	}
}

func TestManualValidation(t *testing.T) {
	h, _ := manualHarness(t)
	ctx := context.Background()
	acOn := []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true}}
	if _, err := h.eng.Save(ctx, Rule{Name: "x", Kind: KindAutomation, Manual: true, Enabled: true, Actions: acOn,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18}}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manual with conditions: %v", err)
	}
	if _, err := h.eng.Save(ctx, Rule{Name: "x", Kind: KindAlert, Manual: true, Enabled: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("manual alert: %v", err)
	}
	auto, err := h.eng.Save(ctx, Rule{Name: "auto", Kind: KindAutomation, Enabled: true, Actions: acOn,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Start(ctx, auto.ID); !errors.Is(err, ErrNotManual) {
		t.Fatalf("start of an automation: %v", err)
	}
	if _, err := h.eng.Start(ctx, 999); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("start of an unknown rule: %v", err)
	}
	off, err := h.eng.Save(ctx, Rule{Name: "uit", Kind: KindAutomation, Manual: true, Actions: acOn})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.eng.Start(ctx, off.ID); !errors.Is(err, ErrInvalid) {
		t.Fatalf("start of a disabled action: %v", err)
	}

	// switching the action itself off (disabling it) switches it off
	on := saveManual(t, h, Rule{Name: "aan", Actions: acOn,
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false}}})
	if _, err := h.eng.Start(ctx, on.ID); err != nil {
		t.Fatal(err)
	}
	on.Rule.Enabled = false
	saved, err := h.eng.Save(ctx, on.Rule)
	if err != nil || saved.State.Active {
		t.Fatalf("disabled: %+v %v", saved.State, err)
	}
	stored, _ := h.st.Rules(ctx)
	for _, s := range stored {
		if s.ID == on.ID && s.Active {
			t.Fatal("disabled action still stored as on")
		}
	}
}
