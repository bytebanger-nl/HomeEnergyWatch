package tado

import (
	"encoding/json"
	"testing"
)

func decodeState(t *testing.T, raw string) zoneState {
	t.Helper()
	var st zoneState
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return st
}

func find(metrics []metricView, key string) (metricView, bool) {
	for _, m := range metrics {
		if m.Key == key {
			return m, true
		}
	}
	return metricView{}, false
}

type metricView struct {
	Key   string
	Value any
	Unit  string
}

func views(st zoneState) []metricView {
	out := make([]metricView, 0)
	for _, m := range zoneMetrics(st) {
		out = append(out, metricView{Key: m.Key, Value: m.Value, Unit: m.Unit})
	}
	return out
}

func TestZoneMetricsHeatingOn(t *testing.T) {
	st := decodeState(t, `{
		"setting": {"power":"ON","temperature":{"celsius":20.5}},
		"sensorDataPoints": {"insideTemperature":{"celsius":21.3},"humidity":{"percentage":45.2}},
		"activityDataPoints": {"heatingPower":{"percentage":35}},
		"openWindow": null
	}`)
	ms := views(st)
	if m, ok := find(ms, "temp"); !ok || m.Value != 21.3 {
		t.Errorf("temp = %v (ok=%v), want 21.3", m.Value, ok)
	}
	if m, ok := find(ms, "target"); !ok || m.Value != 20.5 {
		t.Errorf("target = %v (ok=%v), want 20.5", m.Value, ok)
	}
	if m, ok := find(ms, "humidity"); !ok || m.Value != 45.2 {
		t.Errorf("humidity = %v (ok=%v), want 45.2", m.Value, ok)
	}
	if m, ok := find(ms, "heating"); !ok || m.Value != float64(35) {
		t.Errorf("heating = %v (ok=%v), want 35", m.Value, ok)
	}
	if m, ok := find(ms, "power"); !ok || m.Value != true {
		t.Errorf("power = %v (ok=%v), want true", m.Value, ok)
	}
	if _, ok := find(ms, "window"); ok {
		t.Error("window metric present, want absent when openWindow is null")
	}
}

func TestZoneMetricsOffWithOpenWindow(t *testing.T) {
	st := decodeState(t, `{
		"setting": {"power":"OFF","temperature":null},
		"sensorDataPoints": {"insideTemperature":{"celsius":18.0}},
		"activityDataPoints": {},
		"openWindow": {"detectedTime":"2026-11-07T10:00:00Z"}
	}`)
	ms := views(st)
	if _, ok := find(ms, "target"); ok {
		t.Error("target present while off, want absent")
	}
	if m, ok := find(ms, "power"); !ok || m.Value != false {
		t.Errorf("power = %v, want false", m.Value)
	}
	if m, ok := find(ms, "window"); !ok || m.Value != true {
		t.Errorf("window = %v (ok=%v), want true", m.Value, ok)
	}
}

func TestZoneMode(t *testing.T) {
	var st zoneState
	if json.Unmarshal([]byte(`{"setting":{"type":"HEATING","power":"ON"}}`), &st); zoneMode(st) != "Schema" {
		t.Fatalf("no overlay: %s", zoneMode(st))
	}
	for body, want := range map[string]string{
		`{"setting":{"type":"HEATING","power":"ON"},"overlay":{"setting":{"type":"HEATING","power":"ON"}}}`:     "Handmatig",
		`{"setting":{"type":"HEATING","power":"OFF"},"overlay":{"setting":{"type":"HEATING","power":"OFF"}}}`:   "Uit",
		`{"setting":{"type":"HOT_WATER","power":"ON"},"overlay":{"setting":{"type":"HOT_WATER","power":"ON"}}}`: "Aan",
	} {
		st = zoneState{}
		if err := json.Unmarshal([]byte(body), &st); err != nil {
			t.Fatal(err)
		}
		if got := zoneMode(st); got != want {
			t.Errorf("%s: %s, want %s", body, got, want)
		}
	}
}
