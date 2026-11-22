package main

import (
	"encoding/json"
	"net/http"
	"slices"
	"sync"
)

// batteryGroup is the battery control of the P1 meter (/api/batteries, API
// v2): the mode and permissions it sets decide what the simulated Plug-In
// Battery does. It follows the API as documented: standby is zero without
// permissions, charge_to_full charges to 100% and then returns to the mode
// before it (to_full while it runs, during the deprecation).
type batteryGroup struct {
	mu          sync.Mutex
	mode        string
	permissions []string
	toFull      bool
	before      string   // the mode before a "to full"
	beforePerms []string // and its permissions
	power       float64  // the battery's latest power, + charging
}

var batteries = &batteryGroup{mode: "zero", permissions: []string{"charge_allowed", "discharge_allowed"}}

const (
	permitCharge    = "charge_allowed"
	permitDischarge = "discharge_allowed"
	batteryMaxW     = 800
)

// apply turns the battery's own profile p (+ charging) into what it does in
// the group's stand; soc is its state of charge.
func (g *batteryGroup) apply(p, soc float64) float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.toFull {
		if soc >= 100 {
			g.endToFull()
		} else {
			p = batteryMaxW
		}
	}
	if !g.toFull {
		if p > 0 && !slices.Contains(g.permissions, permitCharge) || p < 0 && !slices.Contains(g.permissions, permitDischarge) {
			p = 0
		}
	}
	g.power = p
	return p
}

func (g *batteryGroup) endToFull() {
	g.toFull = false
	g.mode, g.permissions = g.before, g.beforePerms
}

func (g *batteryGroup) state() map[string]any {
	mode := g.mode
	if g.toFull {
		mode = "to_full"
	}
	return map[string]any{
		"mode": mode, "permissions": g.permissions, "charge_to_full": g.toFull, "battery_count": 1,
		"power_w": round(g.power, 0), "target_power_w": round(g.power, 0),
		"max_consumption_w": batteryMaxW, "max_production_w": batteryMaxW,
	}
}

func (g *batteryGroup) get(w http.ResponseWriter, _ *http.Request) {
	g.mu.Lock()
	defer g.mu.Unlock()
	writeJSON(w, 200, g.state())
}

func (g *batteryGroup) put(w http.ResponseWriter, r *http.Request) {
	var u struct {
		Mode         *string   `json:"mode"`
		Permissions  *[]string `json:"permissions"`
		ChargeToFull *bool     `json:"charge_to_full"`
	}
	if err := json.NewDecoder(r.Body).Decode(&u); err != nil {
		writeJSON(w, 400, map[string]string{"error": "request:json-invalid"})
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if u.Mode != nil && !slices.Contains([]string{"zero", "predictive", "standby", "to_full"}, *u.Mode) {
		writeJSON(w, 400, map[string]string{"error": "request:value-invalid"})
		return
	}
	if u.Permissions != nil {
		for _, p := range *u.Permissions {
			if p != permitCharge && p != permitDischarge {
				writeJSON(w, 400, map[string]string{"error": "request:value-invalid"})
				return
			}
		}
		if g.toFull || u.Mode != nil && *u.Mode == "to_full" {
			// permissions are read-only in to_full
			writeJSON(w, 400, map[string]string{"error": "request:value-invalid"})
			return
		}
	}
	full := u.ChargeToFull != nil && *u.ChargeToFull || u.Mode != nil && *u.Mode == "to_full"
	switch {
	case full && !g.toFull:
		g.before, g.beforePerms, g.toFull = g.mode, g.permissions, true
	case !full && g.toFull && (u.ChargeToFull != nil || u.Mode != nil):
		g.endToFull()
	}
	if u.Mode != nil && *u.Mode != "to_full" {
		g.mode = *u.Mode
		switch g.mode {
		case "standby":
			g.permissions = []string{}
		case "zero":
			if u.Permissions == nil && len(g.permissions) == 0 {
				g.permissions = []string{permitCharge, permitDischarge}
			}
		}
	}
	if u.Permissions != nil {
		g.permissions = append([]string{}, *u.Permissions...)
		if g.mode != "predictive" {
			g.mode = "zero"
			if len(g.permissions) == 0 {
				g.mode = "standby"
			}
		}
	}
	writeJSON(w, 200, g.state())
}
