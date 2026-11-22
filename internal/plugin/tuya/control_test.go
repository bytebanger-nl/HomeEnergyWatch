package tuya

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// radiatorModel is a thing model as /v2.0/cloud/thing/{id}/model returns it
// (the "model" string) for an electric radiator.
const radiatorModel = `{"services":[{"properties":[
 {"abilityId":1,"code":"switch","accessMode":"rw","typeSpec":{"type":"bool"}},
 {"abilityId":2,"code":"temp_set","accessMode":"rw","typeSpec":{"type":"value","min":50,"max":350,"step":5,"scale":1,"unit":"℃"}},
 {"abilityId":3,"code":"temp_current","accessMode":"ro","typeSpec":{"type":"value","min":0,"max":500,"step":1,"scale":1,"unit":"℃"}},
 {"abilityId":4,"code":"mode","accessMode":"rw","typeSpec":{"type":"enum","range":["comfort","eco","anti_frost"]}},
 {"abilityId":7,"code":"child_lock","accessMode":"rw","typeSpec":{"type":"bool"}},
 {"abilityId":19,"code":"temp_correction","accessMode":"rw","typeSpec":{"type":"value","min":-9,"max":9,"step":1,"scale":0}},
 {"abilityId":22,"code":"fault","accessMode":"ro","typeSpec":{"type":"bitmap"}}
]}]}`

func radiatorSchema(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(parseModel(radiatorModel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestControlsFromModel(t *testing.T) {
	in := &instance{watch: plugin.NewWatchdog("")}
	got := in.Controls(plugin.Device{Config: map[string]string{"schema": radiatorSchema(t)}})
	if len(got) != 3 {
		t.Fatalf("controls = %+v, want switch, temp_set and mode", got)
	}
	sw, sp, mode := got[0], got[1], got[2]
	if sw.Key != "dp1" || sw.Kind != plugin.ControlSwitch || sw.Metric != "dp1" || sw.Label != "Aan" {
		t.Errorf("switch = %+v", sw)
	}
	if sp.Key != "dp2" || sp.Kind != plugin.ControlSetpoint || sp.Min != 5 || sp.Max != 35 || sp.Step != 0.5 || sp.Unit != "°C" {
		t.Errorf("setpoint = %+v", sp)
	}
	if mode.Key != "dp4" || mode.Kind != plugin.ControlSelect || len(mode.Options) != 3 ||
		mode.Options[2].Value != "anti_frost" || mode.Options[2].Label != "Vorstbeveiliging" {
		t.Errorf("mode = %+v", mode)
	}
	// old schema (no access/type) and no schema: no controls
	for _, schema := range []string{`{"1":{"code":"switch"}}`, ""} {
		if c := in.Controls(plugin.Device{Config: map[string]string{"schema": schema}}); len(c) != 0 {
			t.Errorf("schema %q: controls = %+v", schema, c)
		}
	}
}

func TestEnumMetricShowsLabel(t *testing.T) {
	m := metricsFromDPS(map[string]any{"4": "eco", "2": float64(215)}, parseModel(radiatorModel))
	if m[0].Value != 21.5 || m[1].Value != "Eco" {
		t.Fatalf("metrics = %+v", m)
	}
}

func simRadiator(t *testing.T, hours string) (*instance, plugin.Device, *SimDevice) {
	t.Helper()
	sim := NewSimDevice("bfradiator", testKey, V34, map[string]any{
		"1": false, "2": float64(180), "3": float64(194), "4": "eco", "7": false,
	})
	d := startSim(t, sim)
	dev := plugin.Device{ID: "r1", Address: d.Addr, Config: map[string]string{
		"device_id": sim.ID, "local_key": string(testKey), "version": "3.4", "schema": radiatorSchema(t),
	}}
	return &instance{watch: plugin.NewWatchdog(hours), control: true}, dev, sim
}

func TestSetWritesRawValues(t *testing.T) {
	in, dev, sim := simRadiator(t, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, c := range []struct {
		key  string
		val  any
		dp   string
		want any
	}{
		{"dp1", true, "1", true},
		{"dp2", 21.5, "2", float64(215)},
		{"dp4", "comfort", "4", "comfort"},
	} {
		if err := in.Set(ctx, dev, c.key, c.val); err != nil {
			t.Fatalf("set %s: %v", c.key, err)
		}
		if got := sim.DPS()[c.dp]; got != c.want {
			t.Fatalf("dp %s = %v, want %v", c.dp, got, c.want)
		}
	}
	for _, key := range []string{"dp7", "dp3", "dp99", "power"} {
		if err := in.Set(ctx, dev, key, true); err != plugin.ErrNoControl {
			t.Errorf("set %s: err = %v, want ErrNoControl", key, err)
		}
	}
	r, err := in.Poll(ctx, dev)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Online || r.Metrics[1].Value != 21.5 || r.Metrics[3].Value != "Comfort" {
		t.Fatalf("poll = %+v", r)
	}
}

// The watchdog switches off a radiator the app switched on or turned up,
// once the time is up; not one the app turned down.
func TestWatchdogSwitchesOff(t *testing.T) {
	in, dev, sim := simRadiator(t, "0.0000001") // expires at once
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	sim.Update(func(dps map[string]any) { dps["1"] = true })
	if err := in.Set(ctx, dev, "dp2", 17.0); err != nil { // lower: not watched
		t.Fatal(err)
	}
	if _, err := in.Poll(ctx, dev); err != nil || sim.DPS()["1"] != true {
		t.Fatalf("turned down: switched off (err %v)", err)
	}
	if err := in.Set(ctx, dev, "dp2", 23.0); err != nil { // higher: watched
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := in.Poll(ctx, dev); err != nil || sim.DPS()["1"] != false {
		t.Fatalf("turned up: not switched off (err %v, dps %v)", err, sim.DPS())
	}
	// without control allowed the watchdog does nothing
	in.control = false
	if err := in.Set(ctx, dev, "dp1", true); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	if _, err := in.Poll(ctx, dev); err != nil || sim.DPS()["1"] != true {
		t.Fatalf("control off: watchdog acted (err %v)", err)
	}
}
