package plugin

import (
	"context"
	"errors"
	"sync"
	"testing"
)

// a provider whose devices can be controlled
type ctlProvider struct{}

func (ctlProvider) Info() ProviderInfo {
	return ProviderInfo{Type: "testctl", Name: "Test control", CanAddManual: true, IntervalSeconds: 30}
}
func (ctlProvider) Instance(map[string]string, Env) (Instance, error) { return ctlInstance{}, nil }

type ctlInstance struct{ fakeInstance }

var (
	setMu  sync.Mutex
	setLog []any
)

func (ctlInstance) Controls(Device) []Control {
	return []Control{
		{Key: "on", Label: "Aan", Kind: ControlSwitch},
		{Key: "temp", Label: "Temperatuur", Kind: ControlSetpoint, Min: 16, Max: 30, Step: 0.5},
		{Key: "mode", Label: "Modus", Kind: ControlSelect, Options: []Option{{Value: "HEAT"}, {Value: "COOL"}}},
		{Key: "reset", Label: "Schema", Kind: ControlAction},
	}
}

func (ctlInstance) Set(_ context.Context, _ Device, key string, value any) error {
	setMu.Lock()
	defer setMu.Unlock()
	setLog = append(setLog, key, value)
	return nil
}

func init() { Register(ctlProvider{}) }

func TestManagerSet(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "testctl", "Airco", map[string]string{controlKey: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if !inst.CanControl || inst.Control {
		t.Fatalf("a new instance must be controllable but not allowed: %+v", inst)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Set(ctx, dev.ID, "on", true); !errors.Is(err, ErrControlOff) {
		t.Fatalf("control off: %v", err)
	}
	if v := m.DeviceViews(); len(v[0].Controls) != 0 {
		t.Fatal("controls shown while control is off")
	}

	on := true
	if _, err := m.UpdateInstance(ctx, inst.ID, nil, nil, &on, nil); err != nil {
		t.Fatal(err)
	}
	if v := m.DeviceViews(); len(v[0].Controls) != 4 {
		t.Fatalf("controls %+v", v[0].Controls)
	}
	// a config change keeps the control flag
	if _, err := m.UpdateInstance(ctx, inst.ID, nil, nil, nil, map[string]string{"x": "y"}); err != nil {
		t.Fatal(err)
	}
	setLog = nil
	for _, c := range []struct {
		key   string
		value any
		ok    bool
	}{
		{"on", true, true},
		{"on", "yes", false},
		{"temp", 21.3, true}, // rounded to 21.5
		{"temp", 35.0, false},
		{"mode", "COOL", true},
		{"mode", "FAN", false},
		{"reset", nil, true},
		{"nope", 1.0, false},
	} {
		err := m.Set(ctx, dev.ID, c.key, c.value)
		if (err == nil) != c.ok {
			t.Errorf("Set(%s, %v): %v", c.key, c.value, err)
		}
	}
	setMu.Lock()
	got := setLog
	setMu.Unlock()
	want := []any{"on", true, "temp", 21.5, "mode", "COOL", "reset", nil}
	if len(got) != len(want) {
		t.Fatalf("set log %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("set log %v, want %v", got, want)
		}
	}
	if err := m.Set(ctx, "missing", "on", true); err == nil {
		t.Fatal("unknown device accepted")
	}

	// a read-only provider cannot be switched to control
	ro, _ := m.AddInstance(ctx, "test", "RO", nil)
	if _, err := m.UpdateInstance(ctx, ro.ID, nil, nil, &on, nil); !errors.Is(err, ErrNoControl) {
		t.Fatalf("read-only provider: %v", err)
	}
}

// Control can be turned off per device; the instance switch comes first.
func TestDeviceControlSwitch(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "testctl", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	// a scan cannot smuggle in the device switch
	a, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "a", Config: map[string]string{controlKey: "false"}}, "A")
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "b"}, "B")
	if err != nil {
		t.Fatal(err)
	}
	view := func(id string) DeviceView {
		for _, v := range m.DeviceViews() {
			if v.ID == id {
				return v
			}
		}
		t.Fatalf("no view for %s", id)
		return DeviceView{}
	}
	if v := view(a.ID); v.CanControl || v.Control || len(v.Controls) != 0 {
		t.Fatalf("instance control off: %+v", v)
	}
	on, off := true, false
	if _, err := m.UpdateInstance(ctx, inst.ID, nil, nil, &on, nil); err != nil {
		t.Fatal(err)
	}
	if v := view(a.ID); !v.CanControl || !v.Control || len(v.Controls) == 0 {
		t.Fatalf("device control on by default: %+v", v)
	}
	if err := m.UpdateDevice(ctx, a.ID, nil, nil, &off); err != nil {
		t.Fatal(err)
	}
	if v := view(a.ID); !v.CanControl || v.Control || len(v.Controls) != 0 {
		t.Fatalf("device control off: %+v", v)
	}
	if err := m.Set(ctx, a.ID, "on", true); !errors.Is(err, ErrControlOff) {
		t.Fatalf("set on a device with control off: %v", err)
	}
	if err := m.Set(ctx, b.ID, "on", true); err != nil {
		t.Fatalf("other device: %v", err)
	}
	if err := m.UpdateDevice(ctx, a.ID, nil, nil, &on); err != nil {
		t.Fatal(err)
	}
	if err := m.Set(ctx, a.ID, "on", true); err != nil {
		t.Fatalf("control back on: %v", err)
	}
}
