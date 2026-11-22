package homewizard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// Battery group modes of /api/batteries (API v2, on a P1 meter or kWh
// meter; not on the Plug-In Battery itself).
const (
	BatteryZero       = "zero"       // keep the home's net power near zero
	BatteryPredictive = "predictive" // HomeWizard's cloud plan (API 2.3.0)
	BatteryStandby    = "standby"    // legacy: neither charge nor discharge
	BatteryToFull     = "to_full"    // legacy: charge to 100%, then zero
)

// Battery permissions (API 2.2.0).
const (
	PermitCharge    = "charge_allowed"
	PermitDischarge = "discharge_allowed"
)

// Batteries is the state of the battery group a meter controls. Fields an
// older firmware does not report are nil.
type Batteries struct {
	Mode            string    `json:"mode"`
	Permissions     *[]string `json:"permissions,omitempty"`    // API 2.2.0
	ChargeToFull    *bool     `json:"charge_to_full,omitempty"` // API 2.3.0
	BatteryCount    *int      `json:"battery_count,omitempty"`  // API 2.2.0
	PowerW          float64   `json:"power_w"`
	TargetPowerW    float64   `json:"target_power_w"`
	MaxConsumptionW float64   `json:"max_consumption_w"`
	MaxProductionW  float64   `json:"max_production_w"`
}

// Permitted reports whether permission p is given; without permissions
// (older firmware) zero allows both and standby neither.
func (b Batteries) Permitted(p string) bool {
	if b.Permissions == nil {
		return b.Mode == BatteryZero || b.Mode == BatteryToFull && p == PermitCharge
	}
	for _, x := range *b.Permissions {
		if x == p {
			return true
		}
	}
	return false
}

// BatteriesUpdate changes the battery group; nil fields are left alone.
type BatteriesUpdate struct {
	Mode         string    `json:"mode,omitempty"`
	Permissions  *[]string `json:"permissions,omitempty"`
	ChargeToFull *bool     `json:"charge_to_full,omitempty"`
}

// Batteries returns GET /api/batteries; ErrUnsupported when the device has
// no battery control (or speaks API v1).
func (c *Client) Batteries(ctx context.Context) (Batteries, error) {
	if c.APIVersion < 2 {
		return Batteries{}, ErrUnsupported
	}
	data, err := c.get(ctx, "/api/batteries")
	if err != nil {
		return Batteries{}, err
	}
	return parseBatteries(data)
}

// SetBatteries sends PUT /api/batteries and returns the new state.
func (c *Client) SetBatteries(ctx context.Context, u BatteriesUpdate) (Batteries, error) {
	if c.APIVersion < 2 {
		return Batteries{}, ErrUnsupported
	}
	data, status, err := c.do(ctx, http.MethodPut, "/api/batteries", u)
	if err != nil {
		return Batteries{}, err
	}
	if err := statusError(status, data); err != nil {
		return Batteries{}, err
	}
	return parseBatteries(data)
}

func parseBatteries(data []byte) (Batteries, error) {
	var b Batteries
	if err := json.Unmarshal(data, &b); err != nil {
		return Batteries{}, fmt.Errorf("homewizard: invalid /api/batteries response: %w", err)
	}
	if b.Mode == "" {
		return Batteries{}, ErrUnsupported
	}
	return b, nil
}
