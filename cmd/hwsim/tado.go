package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

// A fake tado cloud for the tado plugin, under /tado on the price port: the
// OAuth device-code login (login.tado.com) and the parts of the REST API
// the plugin uses (my.tado.com/api/v2). One home with one heating zone,
// whose measured temperature is that of the simulated room. A manual
// setting is an overlay that ends after its timer (clock time); without
// one the zone follows its "schedule", one fixed temperature.
//
// Start the gateway with HWLOCAL_TADO_URL=http://127.0.0.1:18080/tado, add
// a tado plugin and click "Inloggen": the login is approved by itself after
// 4 seconds (or at once when the link is opened), like pressing the button
// of a battery.

const (
	tadoHome = 1
	tadoZone = 1
)

type tadoSim struct {
	mu       sync.Mutex
	room     *room
	schedule float64 // the schedule's temperature

	overlay      *tadoSetting // nil: follows the schedule
	overlayUntil time.Time

	pending map[string]time.Time // device code -> when it is approved
	tokens  map[string]bool      // valid access tokens
	refresh map[string]bool      // valid refresh tokens
}

type tadoSetting struct {
	Type        string `json:"type"`
	Power       string `json:"power"`
	Temperature *struct {
		Celsius float64 `json:"celsius"`
	} `json:"temperature,omitempty"`
}

func newTadoSim(r *room, schedule float64) *tadoSim {
	t := &tadoSim{room: r, schedule: schedule,
		pending: map[string]time.Time{}, tokens: map[string]bool{}, refresh: map[string]bool{}}
	t.toRoomLocked()
	return t
}

func randomToken(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// settingLocked is what the zone heats to now. t.mu must be held.
func (t *tadoSim) settingLocked() (on bool, target float64) {
	if t.overlay != nil && time.Now().After(t.overlayUntil) {
		log.Printf("tado     overlay ended, back to the schedule")
		t.overlay = nil
		t.toRoomLocked()
	}
	if t.overlay == nil {
		return true, t.schedule
	}
	if t.overlay.Power != "ON" || t.overlay.Temperature == nil {
		return false, 0
	}
	return true, t.overlay.Temperature.Celsius
}

func (t *tadoSim) toRoomLocked() {
	on, target := true, t.schedule
	if t.overlay != nil {
		on = t.overlay.Power == "ON" && t.overlay.Temperature != nil
		if on {
			target = t.overlay.Temperature.Celsius
		}
	}
	t.room.SetCV(on, target)
}

func (t *tadoSim) register(mux *http.ServeMux, base string) {
	mux.HandleFunc("POST /tado/oauth2/device_authorize", func(w http.ResponseWriter, r *http.Request) {
		code := randomToken("dc-")
		t.mu.Lock()
		t.pending[code] = time.Now().Add(4 * time.Second)
		t.mu.Unlock()
		user := strings.ToUpper(code[3:9])
		writeJSON(w, 200, map[string]any{
			"device_code": code, "user_code": user,
			"verification_uri":          base + "/tado/verify",
			"verification_uri_complete": base + "/tado/verify?user_code=" + user,
			"expires_in":                300, "interval": 5,
		})
	})
	// Opening the link approves every pending login at once.
	mux.HandleFunc("GET /tado/verify", func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		for code := range t.pending {
			t.pending[code] = time.Now()
		}
		t.mu.Unlock()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, "<p>Nep-tado (hwsim): inloggen goedgekeurd. Je kunt dit venster sluiten.</p>")
	})
	mux.HandleFunc("POST /tado/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		t.mu.Lock()
		defer t.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			code := r.Form.Get("device_code")
			at, ok := t.pending[code]
			switch {
			case !ok:
				writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			case time.Now().Before(at):
				writeJSON(w, 400, map[string]any{"error": "authorization_pending"})
				return
			}
			delete(t.pending, code)
			log.Printf("tado     login approved")
		case "refresh_token":
			old := r.Form.Get("refresh_token")
			if !t.refresh[old] {
				writeJSON(w, 400, map[string]any{"error": "invalid_grant"})
				return
			}
			delete(t.refresh, old) // refresh tokens rotate
		default:
			writeJSON(w, 400, map[string]any{"error": "unsupported_grant_type"})
			return
		}
		access, refresh := randomToken("at-"), randomToken("rt-")
		t.tokens[access], t.refresh[refresh] = true, true
		writeJSON(w, 200, map[string]any{"access_token": access, "refresh_token": refresh,
			"token_type": "bearer", "expires_in": 600})
	})

	api := func(pattern string, h func(w http.ResponseWriter, r *http.Request)) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			t.mu.Lock()
			ok := t.tokens[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
			t.mu.Unlock()
			if !ok {
				writeJSON(w, 401, map[string]any{"errors": []map[string]string{{"code": "unauthorized"}}})
				return
			}
			h(w, r)
		})
	}
	home := fmt.Sprintf("/tado/api/v2/homes/%d", tadoHome)
	zone := fmt.Sprintf("%s/zones/%d", home, tadoZone)
	api("GET /tado/api/v2/me", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"homes": []map[string]any{{"id": tadoHome, "name": "Sim-huis"}}})
	})
	api("GET "+home+"/zones", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, []map[string]any{{"id": tadoZone, "name": "Woonkamer (sim)", "type": "HEATING"}})
	})
	api("GET "+home+"/zoneStates", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"zoneStates": map[string]any{fmt.Sprint(tadoZone): t.zoneState()}})
	})
	api("PUT "+zone+"/overlay", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Setting     tadoSetting `json:"setting"`
			Termination struct {
				Duration int `json:"durationInSeconds"`
			} `json:"termination"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.Setting.Power == "" {
			writeJSON(w, 422, map[string]any{"errors": []map[string]string{{"code": "invalid"}}})
			return
		}
		d := time.Duration(body.Termination.Duration) * time.Second
		if d <= 0 {
			d = time.Hour
		}
		t.mu.Lock()
		renew := t.overlay != nil && sameTadoSetting(*t.overlay, body.Setting)
		st := body.Setting
		t.overlay, t.overlayUntil = &st, time.Now().Add(d)
		t.toRoomLocked()
		t.mu.Unlock()
		if renew {
			log.Printf("tado     overlay renewed for %s", d)
		} else if st.Power == "ON" && st.Temperature != nil {
			log.Printf("tado     overlay %.1f °C for %s", st.Temperature.Celsius, d)
		} else {
			log.Printf("tado     overlay off for %s", d)
		}
		writeJSON(w, 200, map[string]any{"setting": st})
	})
	api("DELETE "+zone+"/overlay", func(w http.ResponseWriter, r *http.Request) {
		t.mu.Lock()
		t.overlay = nil
		t.toRoomLocked()
		t.mu.Unlock()
		log.Printf("tado     back to the schedule (%.1f °C)", t.schedule)
		w.WriteHeader(http.StatusNoContent)
	})
}

func sameTadoSetting(a, b tadoSetting) bool {
	if a.Power != b.Power || (a.Temperature == nil) != (b.Temperature == nil) {
		return false
	}
	return a.Temperature == nil || a.Temperature.Celsius == b.Temperature.Celsius
}

// zoneState is the zone in the shape of GET /zoneStates.
func (t *tadoSim) zoneState() map[string]any {
	temp := t.room.Temp()
	cv, _ := t.room.Heating()
	t.mu.Lock()
	on, target := t.settingLocked()
	var overlay any
	if t.overlay != nil {
		overlay = map[string]any{"setting": t.overlay}
	}
	t.mu.Unlock()
	setting := map[string]any{"type": "HEATING", "power": "OFF"}
	if on {
		setting = map[string]any{"type": "HEATING", "power": "ON", "temperature": map[string]any{"celsius": target}}
	}
	power := 0.0
	if cv {
		power = 100
	}
	return map[string]any{
		"setting": setting,
		"overlay": overlay,
		"sensorDataPoints": map[string]any{
			"insideTemperature": map[string]any{"celsius": round2(temp)},
			"humidity":          map[string]any{"percentage": 50.0},
		},
		"activityDataPoints": map[string]any{"heatingPower": map[string]any{"percentage": power}},
		"openWindow":         nil,
	}
}

func round2(v float64) float64 { return float64(int(v*100+0.5)) / 100 }
