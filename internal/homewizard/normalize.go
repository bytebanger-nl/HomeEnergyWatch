package homewizard

import (
	"encoding/json"
	"strconv"
	"strings"
	"time"
)

// v1ToV2 renames API v1 fields to their v2 equivalent.
var v1ToV2 = map[string]string{
	"total_power_import_kwh":       "energy_import_kwh",
	"total_power_import_t1_kwh":    "energy_import_t1_kwh",
	"total_power_import_t2_kwh":    "energy_import_t2_kwh",
	"total_power_import_t3_kwh":    "energy_import_t3_kwh",
	"total_power_import_t4_kwh":    "energy_import_t4_kwh",
	"total_power_export_kwh":       "energy_export_kwh",
	"total_power_export_t1_kwh":    "energy_export_t1_kwh",
	"total_power_export_t2_kwh":    "energy_export_t2_kwh",
	"total_power_export_t3_kwh":    "energy_export_t3_kwh",
	"total_power_export_t4_kwh":    "energy_export_t4_kwh",
	"active_power_w":               "power_w",
	"active_power_l1_w":            "power_l1_w",
	"active_power_l2_w":            "power_l2_w",
	"active_power_l3_w":            "power_l3_w",
	"active_voltage_v":             "voltage_v",
	"active_voltage_l1_v":          "voltage_l1_v",
	"active_voltage_l2_v":          "voltage_l2_v",
	"active_voltage_l3_v":          "voltage_l3_v",
	"active_current_a":             "current_a",
	"active_current_l1_a":          "current_l1_a",
	"active_current_l2_a":          "current_l2_a",
	"active_current_l3_a":          "current_l3_a",
	"active_apparent_power_va":     "apparent_power_va",
	"active_apparent_power_l1_va":  "apparent_power_l1_va",
	"active_apparent_power_l2_va":  "apparent_power_l2_va",
	"active_apparent_power_l3_va":  "apparent_power_l3_va",
	"active_reactive_power_var":    "reactive_power_var",
	"active_reactive_power_l1_var": "reactive_power_l1_var",
	"active_reactive_power_l2_var": "reactive_power_l2_var",
	"active_reactive_power_l3_var": "reactive_power_l3_var",
	"active_power_factor":          "power_factor",
	"active_power_factor_l1":       "power_factor_l1",
	"active_power_factor_l2":       "power_factor_l2",
	"active_power_factor_l3":       "power_factor_l3",
	"active_frequency_hz":          "frequency_hz",
	"active_power_average_w":       "average_power_15m_w",
	"montly_power_peak_w":          "monthly_power_peak_w",
	"monthly_power_peak_w":         "monthly_power_peak_w",
	"wifi_strength":                "wifi_strength_pct",
	"active_liter_lpm":             "water_lpm",
	"total_liter_m3":               "water_m3",
	"total_gas_m3":                 "gas_m3",
}

// keys that are numbers but not measurements.
var ignoredNumeric = map[string]bool{
	"smr_version":      true,
	"protocol_version": true,
	"active_tariff":    true,
	"tariff":           true,
	"gas_timestamp":    true,
	"timestamp":        true,
}

type externalDevice struct {
	UniqueID  string          `json:"unique_id"`
	Type      string          `json:"type"`
	Value     *float64        `json:"value"`
	Unit      string          `json:"unit"`
	Timestamp json.RawMessage `json:"timestamp"`
}

// parseMeasurement normalises a v1 /api/v1/data or v2 /api/measurement body.
func parseMeasurement(body []byte, now time.Time) (Reading, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return Reading{}, err
	}
	r := Reading{Time: now.UTC(), Values: map[string]float64{}}

	for key, msg := range raw {
		switch key {
		case "external":
			continue // handled below, so it wins over total_gas_m3
		case "active_tariff", "tariff":
			var t int
			if json.Unmarshal(msg, &t) == nil && t >= 1 && t <= 4 {
				r.Tariff = &t
			}
			continue
		case "wifi_rssi_db":
			// v2: convert to a percentage like v1 reports.
			var db float64
			if json.Unmarshal(msg, &db) == nil {
				r.Values["wifi_strength_pct"] = rssiToPct(db)
			}
			continue
		}
		if ignoredNumeric[key] {
			continue
		}
		var v float64
		if err := json.Unmarshal(msg, &v); err != nil {
			continue // strings, objects, nulls
		}
		if renamed, ok := v1ToV2[key]; ok {
			// v1 meters without tariffs report only t1 totals.
			if _, exists := r.Values[renamed]; exists && strings.HasSuffix(key, "_t1_kwh") {
				continue
			}
			key = renamed
		}
		r.Values[key] = v
	}

	// v1 P1 meters with a single tariff only expose t1/t2 totals.
	for _, dir := range []string{"import", "export"} {
		total := "energy_" + dir + "_kwh"
		if _, ok := r.Values[total]; ok {
			continue
		}
		var sum float64
		found := false
		for i := 1; i <= 4; i++ {
			if v, ok := r.Values["energy_"+dir+"_t"+strconv.Itoa(i)+"_kwh"]; ok {
				sum += v
				found = true
			}
		}
		if found {
			r.Values[total] = sum
		}
	}

	if ext, ok := raw["external"]; ok {
		parseExternal(ext, &r)
	}
	if ts, ok := raw["gas_timestamp"]; ok && r.GasTime == nil {
		if t, ok := parseMeterTime(ts); ok {
			r.GasTime = &t
		}
	}
	return r, nil
}

func parseExternal(msg json.RawMessage, r *Reading) {
	var devices []externalDevice
	if json.Unmarshal(msg, &devices) != nil {
		return
	}
	for _, d := range devices {
		if d.Value == nil {
			continue
		}
		switch d.Type {
		case "gas_meter":
			r.Values["gas_m3"] = *d.Value
			if t, ok := parseMeterTime(d.Timestamp); ok {
				r.GasTime = &t
			}
		case "water_meter":
			r.Values["external_water_m3"] = *d.Value
		case "heat_meter", "inlet_heat_meter":
			r.Values["heat_gj"] = *d.Value
		case "warm_water_meter":
			r.Values["warm_water_m3"] = *d.Value
		}
	}
}

// parseMeterTime parses the YYMMDDhhmmss integer of DSMR or an ISO string.
// DSMR timestamps are Dutch local time.
func parseMeterTime(msg json.RawMessage) (time.Time, bool) {
	if len(msg) == 0 || string(msg) == "null" {
		return time.Time{}, false
	}
	var n int64
	if json.Unmarshal(msg, &n) == nil {
		t, err := time.ParseInLocation("060102150405", strconv.FormatInt(n, 10), amsterdam())
		return t.UTC(), err == nil
	}
	var s string
	if json.Unmarshal(msg, &s) == nil {
		for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05"} {
			if t, err := time.ParseInLocation(layout, s, amsterdam()); err == nil {
				return t.UTC(), true
			}
		}
	}
	return time.Time{}, false
}

var amsterdamLoc *time.Location

func amsterdam() *time.Location {
	if amsterdamLoc == nil {
		loc, err := time.LoadLocation("Europe/Amsterdam")
		if err != nil {
			loc = time.Local
		}
		amsterdamLoc = loc
	}
	return amsterdamLoc
}

func rssiToPct(db float64) float64 {
	switch {
	case db <= -100 || db == 0:
		return 0
	case db >= -50:
		return 100
	default:
		return 2 * (db + 100)
	}
}
