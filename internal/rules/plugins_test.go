package rules

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

type fakePlugins struct {
	mu    sync.Mutex
	views map[string]plugin.DeviceView
	sets  []string
	fail  bool
	// netFails: the next Set calls that fail as a device that does not answer
	netFails int
	// drops: per control key, the next Set calls that are accepted but do
	// not change the device
	drops  map[string]int
	ctlOff bool // Set: control not allowed
	calls  int
	block  chan struct{} // when set, Set waits until it is closed
}

func (f *fakePlugins) DeviceViews() []plugin.DeviceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []plugin.DeviceView
	for _, v := range f.views {
		out = append(out, v)
	}
	return out
}

func (f *fakePlugins) Set(_ context.Context, id, key string, value any) error {
	f.mu.Lock()
	block := f.block
	f.mu.Unlock()
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.ctlOff {
		return plugin.ErrControlOff
	}
	if f.netFails > 0 {
		f.netFails--
		return &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("i/o timeout")}
	}
	if f.fail {
		return errors.New("airco bezet")
	}
	f.sets = append(f.sets, fmt.Sprintf("%s.%s=%v", id, key, value))
	if f.drops[key] > 0 {
		f.drops[key]--
	} else {
		f.apply(id, key, value)
	}
	return nil
}

// apply shows a value that was set in the device's metric, as the next poll
// of a real device would (a select by its label).
func (f *fakePlugins) apply(id, key string, value any) {
	d, ok := f.views[id]
	if !ok || d.Reading == nil {
		return
	}
	for _, c := range d.Controls {
		if c.Key != key || c.Metric == "" {
			continue
		}
		for _, o := range c.Options {
			if o.Value == value {
				value = o.Label
			}
		}
		metrics := append([]plugin.Metric(nil), d.Reading.Metrics...)
		for i := range metrics {
			if metrics[i].Key == c.Metric {
				metrics[i].Value = value
			}
		}
		r := *d.Reading
		r.Metrics = metrics
		d.Reading = &r
		f.views[id] = d
	}
}

func (f *fakePlugins) put(id, name string, online bool, controls []plugin.Control, metrics ...plugin.Metric) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.views[id] = plugin.DeviceView{ID: id, Name: name, Controls: controls,
		Reading: &plugin.Reading{Online: online, Metrics: metrics}}
}

var (
	tadoControls = []plugin.Control{
		{Key: "target", Label: "Temperatuur", Kind: plugin.ControlSetpoint, Min: 5, Max: 25, Step: 0.5, Unit: "°C"},
		{Key: "off", Label: "Uit", Kind: plugin.ControlAction},
		{Key: "schedule", Label: "Terug naar schema", Kind: plugin.ControlAction},
	}
	mhiControls = []plugin.Control{
		{Key: "power", Label: "Aan", Kind: plugin.ControlSwitch},
		{Key: "mode", Label: "Modus", Kind: plugin.ControlSelect, Options: []plugin.Option{{Value: "HEAT", Label: "Verwarmen"}, {Value: "COOL", Label: "Koelen"}}},
		{Key: "preset", Label: "Temperatuur", Kind: plugin.ControlSetpoint, Min: 16, Max: 31, Step: 0.5, Unit: "°C"},
	}
)

func temp(v float64) plugin.Metric {
	return plugin.Metric{Key: "temp", Label: "Temperatuur", Kind: plugin.MetricNumber, Value: v, Unit: "°C"}
}

// The living room example of issue 39: warm enough for 10 minutes -> the
// central heating off and the air conditioner heating at 21 °C.
func TestPluginRuleHandsOverToAirco(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	pl.put("tado1", "Woonkamer", true, tadoControls, temp(20.0))
	pl.put("mhi1", "Airco", true, mhiControls,
		plugin.Metric{Key: "mode", Label: "Modus", Kind: plugin.MetricText, Value: "Koelen"})
	ctx := context.Background()

	rule := Rule{
		Name: "Woonkamer warm", Enabled: true, ForSeconds: 600,
		Conditions: []Condition{{Device: "plugin:tado1", Metric: "temp", Op: OpAbove, Value: 20.9}},
		Actions: []Action{
			{Type: ActionPluginSet, Device: "plugin:tado1", Key: "off"},
			{Type: ActionPluginSet, Device: "plugin:mhi1", Key: "mode", Value: "HEAT"},
			{Type: ActionPluginSet, Device: "plugin:mhi1", Key: "preset", Value: 21.2}, // rounded to 21
			{Type: ActionPluginSet, Device: "plugin:mhi1", Key: "power", Value: true},
			{Type: ActionNotify},
		},
	}
	v, err := h.eng.Save(ctx, rule)
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Actions[2].Value; got != 21.0 {
		t.Fatalf("setpoint not rounded on save: %v", got)
	}

	h.step(2 * time.Second) // 20.0: false, arms the rule
	pl.put("tado1", "Woonkamer", true, tadoControls, temp(21.1))
	h.step(2 * time.Second)
	h.step(598 * time.Second)
	if len(pl.sets) != 0 {
		t.Fatalf("fired before 10 minutes: %v", pl.sets)
	}
	h.step(2 * time.Second)
	if got := strings.Join(pl.sets, " "); got != "tado1.off=<nil> mhi1.mode=HEAT mhi1.preset=21 mhi1.power=true" {
		t.Fatalf("sets: %s", got)
	}
	msgs := h.messages()
	// the push only says why it comes; what was set is in the status line
	if len(msgs) != 1 || msgs[0].Body != "Afgegaan." {
		t.Fatalf("message: %+v", msgs)
	}

	// offline: unknown, neither fires nor arms
	pl.put("tado1", "Woonkamer", false, tadoControls)
	h.step(2 * time.Second)
	if views := h.eng.List(); views[0].State.Armed {
		t.Fatal("armed while the device was offline")
	}
}

func TestPluginRuleTextAndFailures(t *testing.T) {
	h := newHarness(t)
	pl := &fakePlugins{views: map[string]plugin.DeviceView{}}
	h.eng.Plugins = pl
	ctx := context.Background()
	pl.put("mhi1", "Airco", true, mhiControls,
		plugin.Metric{Key: "mode", Label: "Modus", Kind: plugin.MetricText, Value: "Verwarmen"},
		plugin.Metric{Key: "power", Label: "Aan", Kind: plugin.MetricBool, Value: true})
	pl.put("tado1", "Woonkamer", true, tadoControls, plugin.Metric{Key: "heating", Label: "Verwarming", Kind: plugin.MetricNumber, Value: 40.0, Unit: "%"})

	// CV and air conditioner heating at the same time: a warning
	_, err := h.eng.Save(ctx, Rule{Name: "CV en airco tegelijk", Enabled: true,
		Conditions: []Condition{
			{Device: "plugin:mhi1", Metric: "power", Op: OpEqual, Value: 1},
			{Device: "plugin:mhi1", Metric: "mode", Op: OpEqual, Text: "verwarmen"},
			{Device: "plugin:tado1", Metric: "heating", Op: OpAbove, Value: 0},
		},
		Actions: []Action{{Type: ActionNotify}, {Type: ActionPluginSet, Device: "plugin:mhi1", Key: "power", Value: false}},
	})
	if err != nil {
		t.Fatal(err)
	}
	pl.put("tado1", "Woonkamer", true, tadoControls, plugin.Metric{Key: "heating", Label: "Verwarming", Kind: plugin.MetricNumber, Value: 0.0, Unit: "%"})
	h.step(2 * time.Second) // false: arms
	pl.put("tado1", "Woonkamer", true, tadoControls, plugin.Metric{Key: "heating", Label: "Verwarming", Kind: plugin.MetricNumber, Value: 40.0, Unit: "%"})
	pl.fail = true
	h.step(2 * time.Second)
	msgs := h.messages()
	if len(msgs) != 1 || !strings.Contains(msgs[0].Body, "Airco: Aan uit mislukt na 3 pogingen: airco bezet") {
		t.Fatalf("message: %+v", msgs)
	}

	// validation on save
	for _, bad := range []Rule{
		{Name: "x", Conditions: []Condition{{Device: "plugin:mhi1", Metric: "mode", Op: OpAbove, Text: "Koelen"}}, Actions: []Action{{Type: ActionNotify}}},
		{Name: "x", Conditions: []Condition{{Device: "sock", Metric: "power_w", Op: OpEqual, Text: "aan"}}, Actions: []Action{{Type: ActionNotify}}},
		{Name: "x", Conditions: []Condition{{Device: "price", Metric: "price", Op: OpBelow}}, Actions: []Action{{Type: ActionPluginSet, Device: "plugin:mhi1", Key: "preset", Value: 40.0}}},
		{Name: "x", Conditions: []Condition{{Device: "price", Metric: "price", Op: OpBelow}}, Actions: []Action{{Type: ActionPluginSet, Device: "plugin:mhi1", Key: "fan", Value: "HIGH"}}},
		{Name: "x", Conditions: []Condition{{Device: "price", Metric: "price", Op: OpBelow}}, Actions: []Action{{Type: ActionPluginSet, Device: "plugin:gone", Key: "power", Value: true}}},
		{Name: "x", Conditions: []Condition{{Device: "price", Metric: "price", Op: OpBelow}}, Actions: []Action{{Type: ActionPluginSet, Device: "mhi1", Key: "power", Value: true}}},
	} {
		if _, err := h.eng.Save(ctx, bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
	// control not allowed for the device: refused with a clear reason
	pl.put("mhi1", "Airco", true, nil)
	_, err = h.eng.Save(ctx, Rule{Name: "x", Conditions: []Condition{{Device: "price", Metric: "price", Op: OpBelow}},
		Actions: []Action{{Type: ActionPluginSet, Device: "plugin:mhi1", Key: "power", Value: true}}})
	if err == nil || !strings.Contains(err.Error(), "bediening staat uit") {
		t.Fatalf("control off: %v", err)
	}
}
