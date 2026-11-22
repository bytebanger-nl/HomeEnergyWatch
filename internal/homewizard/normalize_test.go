package homewizard

import (
	"testing"
	"time"
)

func TestParseV1P1(t *testing.T) {
	body := []byte(`{
		"wifi_ssid": "Thuis", "wifi_strength": 80, "smr_version": 50, "meter_model": "ISKRA",
		"active_tariff": 2,
		"total_power_import_t1_kwh": 1000.5, "total_power_import_t2_kwh": 2000.25,
		"total_power_export_t1_kwh": 10, "total_power_export_t2_kwh": 20,
		"active_power_w": -543, "active_power_l1_w": -200, "active_voltage_l1_v": 231.2,
		"active_current_l1_a": 1.4, "total_gas_m3": 1234.567, "gas_timestamp": 241105101500,
		"montly_power_peak_w": 4120,
		"external": [{"unique_id": "4730", "type": "gas_meter", "value": 1234.568, "unit": "m3", "timestamp": 241105102000}]
	}`)
	r, err := parseMeasurement(body, time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]float64{
		"power_w":              -543,
		"power_l1_w":           -200,
		"voltage_l1_v":         231.2,
		"current_l1_a":         1.4,
		"energy_import_kwh":    3000.75, // t1 + t2 when no total is given
		"energy_export_kwh":    30,
		"energy_import_t1_kwh": 1000.5,
		"gas_m3":               1234.568, // external gas meter wins over total_gas_m3
		"wifi_strength_pct":    80,
		"monthly_power_peak_w": 4120,
	}
	for k, v := range want {
		if got, ok := r.Values[k]; !ok || got != v {
			t.Errorf("%s = %v (present %v), want %v", k, got, ok, v)
		}
	}
	for _, k := range []string{"smr_version", "active_tariff", "gas_timestamp", "total_gas_m3"} {
		if _, ok := r.Values[k]; ok {
			t.Errorf("unexpected key %s", k)
		}
	}
	if r.Tariff == nil || *r.Tariff != 2 {
		t.Errorf("tariff = %v", r.Tariff)
	}
	if r.GasTime == nil {
		t.Fatal("gas time missing")
	}
	if got := r.GasTime.In(amsterdam()).Format("2006-01-02 15:04"); got != "2024-11-05 10:20" {
		t.Errorf("gas time = %s", got)
	}
}

func TestParseV2(t *testing.T) {
	body := []byte(`{"energy_import_kwh": 12.5, "energy_export_kwh": 1.25, "power_w": 800,
		"state_of_charge_pct": 55.5, "cycles": 12, "wifi_rssi_db": -60, "tariff": 1,
		"timestamp": "2024-11-05T10:20:00"}`)
	r, err := parseMeasurement(body, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Values["power_w"] != 800 || r.Values["state_of_charge_pct"] != 55.5 || r.Values["energy_export_kwh"] != 1.25 {
		t.Errorf("values = %v", r.Values)
	}
	if r.Values["wifi_strength_pct"] != 80 {
		t.Errorf("wifi = %v", r.Values["wifi_strength_pct"])
	}
	if r.Tariff == nil || *r.Tariff != 1 {
		t.Errorf("tariff = %v", r.Tariff)
	}
}

func TestParseWater(t *testing.T) {
	r, err := parseMeasurement([]byte(`{"wifi_ssid":"x","active_liter_lpm":7.5,"total_liter_m3":123.456}`), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if r.Values["water_lpm"] != 7.5 || r.Values["water_m3"] != 123.456 {
		t.Errorf("values = %v", r.Values)
	}
}

func TestVerificationName(t *testing.T) {
	if got := VerificationName(ProductP1, "5C2FAFABCDEF"); got != "appliance/p1dongle/5c2fafabcdef" {
		t.Errorf("got %q", got)
	}
	if got := VerificationName("HWE-NEW", "abc"); got != "" {
		t.Errorf("unknown type should give empty name, got %q", got)
	}
}

func TestEmbeddedCA(t *testing.T) {
	if HomeWizardCA() == nil {
		t.Fatal("no pool")
	}
}
