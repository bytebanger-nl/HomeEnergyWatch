package tado

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// apiBase is a variable so tests can point it at a fake server.
var apiBase = "https://my.tado.com/api/v2"

// get does an authenticated GET and decodes JSON into v.
func (in *instance) get(ctx context.Context, path string, v any) error {
	return in.do(ctx, http.MethodGet, path, nil, v)
}

// do does an authenticated request with an optional JSON body and decodes a
// JSON answer into v (when v is not nil).
func (in *instance) do(ctx context.Context, method, path string, body, v any) error {
	token, err := in.accessTokenFor(ctx)
	if err != nil {
		return err
	}
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, apiBase+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := in.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return fmt.Errorf("tado: niet (meer) geautoriseerd (HTTP 401)")
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("tado: te veel verzoeken (HTTP 429), het daglimiet van tado is bereikt")
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("tado: %s %s gaf HTTP %d", method, path, resp.StatusCode)
	}
	if v == nil || len(answer) == 0 {
		return nil
	}
	return json.Unmarshal(answer, v)
}

type meResp struct {
	Homes []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"homes"`
}

// homeID returns the id of the account's first home.
func (in *instance) homeID(ctx context.Context) (int, error) {
	var me meResp
	if err := in.get(ctx, "/me", &me); err != nil {
		return 0, err
	}
	if len(me.Homes) == 0 {
		return 0, fmt.Errorf("tado: geen homes op dit account")
	}
	return me.Homes[0].ID, nil
}

type zone struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"` // HEATING, HOT_WATER, ...
}

func (in *instance) zones(ctx context.Context, homeID int) ([]zone, error) {
	var zs []zone
	if err := in.get(ctx, "/homes/"+strconv.Itoa(homeID)+"/zones", &zs); err != nil {
		return nil, err
	}
	return zs, nil
}

// celsius is a nested {"celsius": x} value that may be absent.
type celsius struct {
	Celsius *float64 `json:"celsius"`
}
type percentage struct {
	Percentage *float64 `json:"percentage"`
}

// setting is a zone setting: what tado heats to (or that it is off).
type setting struct {
	Type        string   `json:"type"`  // HEATING, HOT_WATER, AIR_CONDITIONING
	Power       string   `json:"power"` // ON / OFF
	Temperature *celsius `json:"temperature,omitempty"`
}

// zoneState is the part of a zone's state we read.
type zoneState struct {
	Setting setting `json:"setting"`
	// Overlay is the manual setting in force, or null when the zone
	// follows its schedule.
	Overlay *struct {
		Setting setting `json:"setting"`
	} `json:"overlay"`
	SensorDataPoints struct {
		InsideTemperature *celsius    `json:"insideTemperature"`
		Humidity          *percentage `json:"humidity"`
	} `json:"sensorDataPoints"`
	ActivityDataPoints struct {
		HeatingPower *percentage `json:"heatingPower"`
	} `json:"activityDataPoints"`
	OpenWindow json.RawMessage `json:"openWindow"` // null or an object
}

type zoneStatesResp struct {
	ZoneStates map[string]zoneState `json:"zoneStates"`
}

// zoneStates reads every zone's state in one call.
func (in *instance) zoneStates(ctx context.Context, homeID int) (map[string]zoneState, error) {
	var r zoneStatesResp
	if err := in.get(ctx, "/homes/"+strconv.Itoa(homeID)+"/zoneStates", &r); err != nil {
		return nil, err
	}
	return r.ZoneStates, nil
}

// overlayReq is the body of PUT /zones/{id}/overlay.
type overlayReq struct {
	Setting     setting `json:"setting"`
	Termination struct {
		Type     string `json:"typeSkillBasedApp"` // TIMER
		Duration int    `json:"durationInSeconds"`
	} `json:"termination"`
}

// setOverlay puts a manual setting on a zone that ends by itself after d,
// so tado falls back to its own schedule when the gateway stops renewing it.
func (in *instance) setOverlay(ctx context.Context, homeID, zoneID string, st setting, d time.Duration) error {
	var body overlayReq
	body.Setting = st
	body.Termination.Type = "TIMER"
	body.Termination.Duration = int(d.Seconds())
	return in.do(ctx, http.MethodPut, "/homes/"+homeID+"/zones/"+zoneID+"/overlay", body, nil)
}

// clearOverlay returns a zone to its schedule.
func (in *instance) clearOverlay(ctx context.Context, homeID, zoneID string) error {
	return in.do(ctx, http.MethodDelete, "/homes/"+homeID+"/zones/"+zoneID+"/overlay", nil, nil)
}
