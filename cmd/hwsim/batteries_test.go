package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func put(t *testing.T, g *batteryGroup, body string) (int, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	g.put(w, httptest.NewRequest(http.MethodPut, "/api/batteries", strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func TestBatteryGroup(t *testing.T) {
	g := &batteryGroup{mode: "zero", permissions: []string{permitCharge, permitDischarge}}
	if p := g.apply(-300, 50); p != -300 {
		t.Errorf("zero: %v", p)
	}
	if code, st := put(t, g, `{"permissions":["charge_allowed"]}`); code != 200 || st["mode"] != "zero" {
		t.Fatalf("charge only: %d %v", code, st)
	}
	if p := g.apply(-300, 50); p != 0 {
		t.Errorf("charge only, discharging: %v", p)
	}
	if code, st := put(t, g, `{"permissions":[]}`); code != 200 || st["mode"] != "standby" {
		t.Fatalf("no permissions: %d %v", code, st)
	}
	if code, st := put(t, g, `{"charge_to_full":true}`); code != 200 || st["mode"] != "to_full" || st["charge_to_full"] != true {
		t.Fatalf("to full: %d %v", code, st)
	}
	if code, _ := put(t, g, `{"permissions":["charge_allowed"]}`); code != 400 {
		t.Errorf("permissions during to full: %d", code)
	}
	if p := g.apply(0, 50); p != batteryMaxW {
		t.Errorf("to full: %v", p)
	}
	// full: back to the stand before it (standby)
	g.apply(0, 100)
	if g.toFull || g.mode != "standby" {
		t.Errorf("after full: %v %s", g.toFull, g.mode)
	}
	if code, _ := put(t, g, `{"mode":"boost"}`); code != 400 {
		t.Errorf("unknown mode: %d", code)
	}
}
