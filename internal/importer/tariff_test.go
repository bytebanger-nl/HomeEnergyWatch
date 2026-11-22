package importer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func TestElectricityLowTariff(t *testing.T) {
	res := parse(t, "time,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh\n"+
		"2026-12-25 12:00,10.000,20.000,1.000,2.000\n"+
		"2026-12-25 12:15,10.300,20.100,1.050,2.000\n")
	if len(res.Quarters) != 1 {
		t.Fatalf("quarters %d", len(res.Quarters))
	}
	q := res.Quarters[0]
	if !near(*q.ImportKWh, 0.4) || q.ImportLowKWh == nil || !near(*q.ImportLowKWh, 0.3) ||
		q.ExportLowKWh == nil || !near(*q.ExportLowKWh, 0.05) {
		t.Fatalf("%+v", q)
	}
	// a socket export has no tariffs
	res = parse(t, "time,Import kWh,Export kWh\n2026-12-25 12:00,1,0\n2026-12-25 12:15,1.2,0\n")
	if q := res.Quarters[0]; q.ImportLowKWh != nil || q.ExportLowKWh != nil {
		t.Fatalf("socket %+v", q)
	}
}

// Importing an export again fills in the tariff split of quarters the
// gateway measured before it kept the split, but only next to the same
// total; it never changes the measured values.
func TestImportFillsTariffSplit(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	start := time.Date(2026, 12, 25, 11, 0, 0, 0, time.UTC) // 12:00 in Amsterdam
	same, other, zero := 0.4005, 9.0, 0.0
	if err := st.SaveQuarters(ctx, []store.Quarter{
		{DeviceID: "p1", Start: start, ImportKWh: &same, ExportKWh: &zero, Quality: "complete", Coverage: 1},
		{DeviceID: "p1", Start: start.Add(15 * time.Minute), ImportKWh: &other, Quality: "complete", Coverage: 1},
	}); err != nil {
		t.Fatal(err)
	}
	res := parse(t, "time,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh\n"+
		"2026-12-25 12:00,10.000,20.000,1,2\n2026-12-25 12:15,10.400,20.000,1,2\n2026-12-25 12:30,10.500,20.000,1,2\n")
	for i := range res.Quarters {
		res.Quarters[i].DeviceID = "p1"
	}
	stats, err := st.ImportQuarters(ctx, res.Quarters, false)
	if err != nil || stats.Added != 2 {
		t.Fatalf("stats %+v %v", stats, err)
	}
	qs, _ := st.Quarters(ctx, "p1", start, start.Add(time.Hour))
	if q := qs[0]; *q.ImportKWh != same || q.Source != store.SourceMeasured || q.ImportLowKWh == nil || !near(*q.ImportLowKWh, 0.4) {
		t.Errorf("same total: %+v", q)
	}
	// another total: the split would not belong to it (the export side does)
	if q := qs[1]; *q.ImportKWh != other || q.ImportLowKWh != nil || q.ExportLowKWh == nil {
		t.Errorf("other total: %+v", q)
	}
	if again, _ := st.ImportQuarters(ctx, res.Quarters, false); again.Added != 0 {
		t.Fatalf("second import added %d", again.Added)
	}
	// a later measurement without split keeps the one stored
	if err := st.SaveQuarters(ctx, []store.Quarter{{DeviceID: "p1", Start: start, ImportKWh: &same, Quality: "complete", Coverage: 1}}); err != nil {
		t.Fatal(err)
	}
	if qs, _ = st.Quarters(ctx, "p1", start, start.Add(time.Minute)); qs[0].ImportLowKWh == nil {
		t.Errorf("measurement dropped the split")
	}
}
