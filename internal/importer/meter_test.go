package importer

import (
	"slices"
	"testing"
)

// Energy Socket and kWh-meter exports: one import and one export counter;
// trailing rows without readings (the rest of the export day) are empty.
func TestSocketExport(t *testing.T) {
	res := parse(t, "time,Import kWh,Export kWh\n"+
		"2026-01-01 00:00,1421.215,0.000\n"+
		"2026-01-01 00:15,1421.315,0.000\n"+
		"2026-01-01 00:30,1421.515,0.000\n"+
		"2026-01-01 00:45,,\n")
	if res.Kind != KindMeter || !slices.Equal(res.Kind.DeviceKinds(), []string{"socket", "kwh"}) {
		t.Fatalf("kind %q", res.Kind)
	}
	if res.Valid != 2 || len(res.Quarters) != 2 || res.EmptyRows != 1 || res.HasExport {
		t.Fatalf("valid %d quarters %d empty %d export %v", res.Valid, len(res.Quarters), res.EmptyRows, res.HasExport)
	}
	if q := res.Quarters[1]; !near(*q.ImportKWh, 0.2) || !near(*q.ExportKWh, 0) {
		t.Fatalf("second quarter %v %v", *q.ImportKWh, *q.ExportKWh)
	}
	if res.LastImportKWh == nil || !near(*res.LastImportKWh, 1421.515) {
		t.Fatalf("last import %v", res.LastImportKWh)
	}
}
