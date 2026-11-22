package history

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
)

// The meter's low-tariff counter gives each quarter its low part; the quarter
// in which the gateway first saw that counter has none.
func TestComputeQuartersLowTariff(t *testing.T) {
	from := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	switchAt := from.Add(20 * time.Minute) // the meter goes to the low tariff
	minutes := linearMinutes(from.Add(-5*time.Minute), 80, nil)
	for i := range minutes {
		m := &minutes[i]
		if m.Minute.Before(from.Add(2 * time.Minute)) {
			continue // recorded by a gateway that did not keep the counter yet
		}
		low := 50.0
		if m.Last.After(switchAt) {
			low += m.Last.Sub(switchAt).Hours() // 1 kWh per hour, like the total
		}
		m.ImportLowKWh, m.ExportLowKWh = f(low), f(7)
	}
	qs := ComputeQuarters("d", minutes, from, from.Add(time.Hour), 5*time.Minute)
	if len(qs) != 4 {
		t.Fatalf("got %d quarters", len(qs))
	}
	if qs[0].ImportLowKWh != nil || qs[0].ExportLowKWh != nil {
		t.Errorf("first quarter has a split: %v %v", qs[0].ImportLowKWh, qs[0].ExportLowKWh)
	}
	if q := qs[1]; q.ImportLowKWh == nil || !near(*q.ImportLowKWh, 10.0/60) || q.ExportLowKWh == nil || *q.ExportLowKWh != 0 {
		t.Errorf("switch quarter: %+v", q)
	}
	for _, q := range qs[2:] {
		if q.ImportLowKWh == nil || !near(*q.ImportLowKWh, *q.ImportKWh) || q.Quality != QualityComplete {
			t.Errorf("%v: low %v of %v", q.Start, q.ImportLowKWh, *q.ImportKWh)
		}
	}
}

// The recorder keeps the T1 counters of a P1 meter, not of a kWh meter.
func TestRecorderKeepsLowTariffOfP1(t *testing.T) {
	st := openStore(t)
	rec := newTestRecorder(st)
	ctx := context.Background()
	t0 := time.Date(2026, 12, 25, 12, 0, 10, 0, time.UTC)
	vals := map[string]float64{"energy_import_kwh": 30, "energy_import_t1_kwh": 10, "energy_import_t2_kwh": 20,
		"energy_export_kwh": 3, "energy_export_t1_kwh": 1, "energy_export_t2_kwh": 2}
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0, vals))
	rec.Add(ctx, "kwh", homewizard.KindKWH, reading(t0, vals))
	rec.Flush(ctx)
	ms, _ := st.Minutes(ctx, "p1", t0.Add(-time.Minute), t0.Add(time.Minute))
	if len(ms) != 1 || ms[0].ImportLowKWh == nil || *ms[0].ImportLowKWh != 10 || ms[0].ExportLowKWh == nil || *ms[0].ExportLowKWh != 1 {
		t.Fatalf("p1 minutes %+v", ms)
	}
	ms, _ = st.Minutes(ctx, "kwh", t0.Add(-time.Minute), t0.Add(time.Minute))
	if len(ms) != 1 || ms[0].ImportLowKWh != nil {
		t.Fatalf("kwh minutes %+v", ms)
	}
}

// A low-tariff counter going backwards drops the split, not the quarter.
func TestComputeQuartersLowTariffBackwards(t *testing.T) {
	from := time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC)
	minutes := linearMinutes(from.Add(-5*time.Minute), 25, nil)
	for i := range minutes {
		minutes[i].ImportLowKWh = f(float64(50 - i))
	}
	qs := ComputeQuarters("d", minutes, from, from.Add(15*time.Minute), 5*time.Minute)
	if len(qs) != 1 || qs[0].Quality != QualityComplete || qs[0].ImportLowKWh != nil {
		t.Fatalf("%+v", qs)
	}
}
