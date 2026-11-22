package rules

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/control"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// putACMode is putAC with the mode as well, so that both can be checked.
func (f *fakePlugins) putACMode(on bool) {
	f.putAC(on, 21)
	f.mu.Lock()
	defer f.mu.Unlock()
	d := f.views["ac"]
	d.Reading.Metrics = append(d.Reading.Metrics, plugin.Metric{Key: "mode", Label: "Modus", Kind: plugin.MetricText, Value: "Ventileren"})
	f.views["ac"] = d
}

func retryHarness(t *testing.T) (*harness, *fakePlugins) {
	t.Helper()
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.putACMode(false)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	if _, err := h.eng.Save(context.Background(), Rule{Name: "Airco aan", Kind: KindAutomation, Enabled: true,
		Conditions: []Condition{{Device: "plugin:tado", Metric: "temp", Op: OpBelow, Value: 18}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "mode", Value: "HEAT"},
			{Type: ActionPluginSet, Device: "plugin:ac", Key: "power", Value: true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	return h, pl
}

// again fires the rule once more: its condition false, then true.
func (h *harness) again(pl *fakePlugins) {
	pl.put("tado", "Woonkamer", true, tadoControls, temp(19))
	h.step(2 * time.Second)
	pl.put("tado", "Woonkamer", true, tadoControls, temp(17))
	pl.putACMode(false)
	h.step(2 * time.Second)
}

// A command that is accepted but not seen on the device is sent again; no
// message when it then arrives.
func TestSetCheckedAndRetried(t *testing.T) {
	h, pl := retryHarness(t)
	pl.drops = map[string]int{"mode": 1, "power": 1} // both arrive the second time
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.mode=HEAT", "ac.mode=HEAT", "ac.power=true", "ac.power=true")
	res := h.eng.List()[0].State.LastResult
	if !strings.Contains(res, "Modus Verwarmen (poging 2)") || !strings.Contains(res, "Aan aan (poging 2)") {
		t.Fatalf("result %q, want the second tries", res)
	}
	if msgs := h.messages(); len(msgs) != 0 {
		t.Fatalf("messages %+v, want none", msgs)
	}
}

// A device that does not answer is tried again too.
func TestSetRetriedWhenNoAnswer(t *testing.T) {
	h, pl := retryHarness(t)
	pl.netFails = 2
	h.step(2 * time.Second)
	wantSets(t, pl, "ac.mode=HEAT", "ac.power=true")
	if res := h.eng.List()[0].State.LastResult; !strings.Contains(res, "Modus Verwarmen (poging 3)") {
		t.Fatalf("result %q, want the third try", res)
	}
}

// Three tries at most; then a push message, although the rule has no
// notify action.
func TestSetGivesUpAndPushes(t *testing.T) {
	h, pl := retryHarness(t)
	h.step(2 * time.Second)
	pl.takeSets()
	pl.calls, pl.drops = 0, map[string]int{"mode": 100, "power": 100}
	h.again(pl)
	if pl.calls != 2*control.DefaultAttempts {
		t.Fatalf("%d tries, want %d", pl.calls, 2*control.DefaultAttempts)
	}
	msgs := h.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "Airco: Aan aan mislukt na 3 pogingen: het apparaat neemt de nieuwe stand niet over") {
		t.Fatalf("messages %+v, want one about the failure", msgs)
	}
}

// A control that cannot be used is not tried again.
func TestSetNotRetriedWhenControlOff(t *testing.T) {
	h, pl := retryHarness(t)
	pl.ctlOff = true
	h.step(2 * time.Second)
	if pl.calls != 2 {
		t.Fatalf("%d calls, want one per action", pl.calls)
	}
	if msgs := h.messages(); len(msgs) != 1 {
		t.Fatalf("messages %+v, want one", msgs)
	}
}
