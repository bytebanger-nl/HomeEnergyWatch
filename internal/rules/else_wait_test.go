package rules

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// When the conditions stop holding during a wait, the else actions run and
// the wait ends: the actions after it must not undo them.
func TestElseEndsWait(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putAC(false, 21)
	ctx := context.Background()
	if _, err := h.eng.Save(ctx, Rule{
		Name: "Eerst CV, dan airco", Enabled: true, Kind: KindAutomation,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 19.0},
			{Type: ActionWait, MaxSeconds: 1800},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
		},
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false}},
	}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=19")
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19))
	h.step(5 * time.Minute)
	wantSets(t, pl, "ac.power=false")
	if v := h.eng.List()[0]; v.State.Run != nil {
		t.Fatalf("wait still running: %+v", v.State.Run)
	}
	h.step(30 * time.Minute)
	wantSets(t, pl)
	if runs, err := h.st.RuleRuns(ctx); err != nil || len(runs) != 0 {
		t.Fatalf("stored runs %v %v", runs, err)
	}
}

// The else actions wait for the actions after a wait that are still being
// sent, so they come last.
func TestElseWaitsForRunningActions(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putAC(false, 21)
	ctx := context.Background()
	if _, err := h.eng.Save(ctx, Rule{
		Name: "Eerst CV, dan airco", Enabled: true, Kind: KindAutomation,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18.5}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado", Key: "target", Value: 19.0},
			{Type: ActionWait, MaxSeconds: 60},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
		},
		ElseActions: []Action{{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: false}},
	}); err != nil {
		t.Fatal(err)
	}
	h.step(2 * time.Second)
	wantSets(t, pl, "tado.target=19")

	release := make(chan struct{})
	pl.mu.Lock()
	pl.block = release
	pl.mu.Unlock()
	h.now = h.now.Add(61 * time.Second)
	h.eng.Evaluate(ctx, h.now) // the wait ends: "airco aan" is being sent
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19))
	h.now = h.now.Add(2 * time.Second)
	h.eng.Evaluate(ctx, h.now) // conditions false, but the else waits
	if v := h.eng.List()[0]; v.State.Armed {
		t.Fatal("else actions started while the actions after the wait were still being sent")
	}
	pl.mu.Lock()
	pl.block = nil
	pl.mu.Unlock()
	close(release)
	h.eng.wg.Wait()
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.power=true", "ac.power=false")
}
