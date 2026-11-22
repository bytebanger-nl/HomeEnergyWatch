// Package homewizard talks to HomeWizard Energy devices over their local API
// (v1 over HTTP, v2 over HTTPS) and normalises their measurements.
package homewizard

import (
	"errors"
	"strings"
	"time"
)

// Product types as reported by the devices.
const (
	ProductP1        = "HWE-P1"
	ProductSocket    = "HWE-SKT"
	ProductWater     = "HWE-WTR"
	ProductDisplay   = "HWE-DSP"
	ProductKWH1      = "HWE-KWH1"
	ProductKWH3      = "HWE-KWH3"
	ProductSDM230    = "SDM230-wifi"
	ProductSDM630    = "SDM630-wifi"
	ProductBattery   = "HWE-BAT"
	productIDUnknown = "unknown"
)

// Kind is a coarse device category used by the gateway and the PWA.
type Kind string

const (
	KindP1      Kind = "p1"
	KindSocket  Kind = "socket"
	KindKWH     Kind = "kwh"
	KindWater   Kind = "water"
	KindBattery Kind = "battery"
	KindOther   Kind = "other"
)

// KindOf maps a product type to its Kind.
func KindOf(productType string) Kind {
	switch productType {
	case ProductP1:
		return KindP1
	case ProductSocket:
		return KindSocket
	case ProductKWH1, ProductKWH3, ProductSDM230, ProductSDM630:
		return KindKWH
	case ProductWater:
		return KindWater
	case ProductBattery:
		return KindBattery
	default:
		return KindOther
	}
}

// ModelName returns a human friendly model name.
func ModelName(productType string) string {
	switch productType {
	case ProductP1:
		return "P1-meter"
	case ProductSocket:
		return "Energy Socket"
	case ProductWater:
		return "Watermeter"
	case ProductDisplay:
		return "Energy Display"
	case ProductKWH1, ProductSDM230:
		return "kWh-meter 1-fase"
	case ProductKWH3, ProductSDM630:
		return "kWh-meter 3-fase"
	case ProductBattery:
		return "Plug-In Battery"
	default:
		return productType
	}
}

// applianceID maps a product type to the segment used in the v2 TLS identity.
func applianceID(productType string) string {
	switch productType {
	case ProductP1:
		return "p1dongle"
	case ProductSocket:
		return "energysocket"
	case ProductWater:
		return "watermeter"
	case ProductDisplay:
		return "display"
	case ProductKWH1, ProductKWH3, ProductSDM230, ProductSDM630:
		return "energymeter"
	case ProductBattery:
		return "battery"
	default:
		return productIDUnknown
	}
}

// VerificationName is the identity a v2 device presents in its certificate:
// appliance/<type>/<serial>.
func VerificationName(productType, serial string) string {
	id := applianceID(productType)
	if id == productIDUnknown || serial == "" {
		return ""
	}
	return "appliance/" + id + "/" + strings.ToLower(serial)
}

// Info describes a device as returned by GET /api.
type Info struct {
	ProductName     string `json:"product_name"`
	ProductType     string `json:"product_type"`
	Serial          string `json:"serial"`
	FirmwareVersion string `json:"firmware_version"`
	APIVersion      string `json:"api_version"`
}

// Reading is one normalised measurement of a device.
//
// Values uses the v2 field names (power_w, energy_import_kwh, ...) for every
// device and API version, plus gas_m3 for the gas meter connected to a P1 and
// water_m3/water_lpm for the watermeter.
type Reading struct {
	Time    time.Time          `json:"time"`
	Values  map[string]float64 `json:"values"`
	PowerOn *bool              `json:"power_on,omitempty"`
	Locked  *bool              `json:"switch_lock,omitempty"`
	Tariff  *int               `json:"tariff,omitempty"`
	GasTime *time.Time         `json:"gas_time,omitempty"`
}

// Value returns a value and whether it was present.
func (r Reading) Value(key string) (float64, bool) {
	v, ok := r.Values[key]
	return v, ok
}

// Errors returned by clients.
var (
	ErrUnauthorized  = errors.New("homewizard: token rejected or missing")
	ErrAPIDisabled   = errors.New("homewizard: local API disabled on device")
	ErrButtonPending = errors.New("homewizard: press the button on the device")
	ErrUnsupported   = errors.New("homewizard: not supported by this device")
)

// LowTariffKWh returns the meter's low-tariff counter for dir ("import" or
// "export"): T1, which a Dutch smart meter uses for the low ("dal") rate. It
// is only reported for a meter that registers two tariffs (T1 and T2 both
// present and no T3/T4), so a single-tariff meter that counts everything in
// T1 is not mistaken for one that is always at the low rate.
func (r Reading) LowTariffKWh(dir string) (float64, bool) {
	t1, ok1 := r.Values["energy_"+dir+"_t1_kwh"]
	_, ok2 := r.Values["energy_"+dir+"_t2_kwh"]
	_, ok3 := r.Values["energy_"+dir+"_t3_kwh"]
	_, ok4 := r.Values["energy_"+dir+"_t4_kwh"]
	if !ok1 || !ok2 || ok3 || ok4 {
		return 0, false
	}
	return t1, true
}
