package homewizard

import (
	"testing"
	"time"
)

var testNow = time.Date(2026, 12, 25, 12, 0, 0, 0, time.UTC)

func TestLowTariffKWh(t *testing.T) {
	r, err := parseMeasurement([]byte(`{"active_tariff":1,"total_power_import_kwh":30,"total_power_import_t1_kwh":10,
		"total_power_import_t2_kwh":20,"total_power_export_t1_kwh":1,"total_power_export_t2_kwh":2}`), testNow)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := r.LowTariffKWh("import"); !ok || v != 10 {
		t.Errorf("import %v %v", v, ok)
	}
	if v, ok := r.LowTariffKWh("export"); !ok || v != 1 {
		t.Errorf("export %v %v", v, ok)
	}
	// a meter with one tariff counts everything in T1: not a low tariff
	r, _ = parseMeasurement([]byte(`{"total_power_import_t1_kwh":30}`), testNow)
	if _, ok := r.LowTariffKWh("import"); ok {
		t.Error("single tariff meter reports a low tariff")
	}
	if v, _ := r.Value("energy_import_kwh"); v != 30 {
		t.Errorf("total %v", v)
	}
}
