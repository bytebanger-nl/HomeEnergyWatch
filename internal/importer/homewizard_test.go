package importer

import (
	"context"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

var ams = func() *time.Location {
	l, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		panic(err)
	}
	return l
}()

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func parse(t *testing.T, csv string) *Result {
	t.Helper()
	res, err := Parse(strings.NewReader(csv), "dev", ams)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestElectricity(t *testing.T) {
	res := parse(t, "\uFEFFtime,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh,L1 max W,L2 max W,L3 max W\r\n"+
		"2026-04-14 14:45,,,,,,,\r\n"+ // before the meter was installed
		"2026-04-14 15:00,10.000,20.000,0.000,1.000,300,,\r\n"+
		"2026-04-14 15:15,10.100,20.000,0.000,1.050,280,,\r\n"+
		"2026-04-14 15:30,10.100,20.250,0.000,1.050,,,\r\n")
	if res.Kind != KindElectricity || res.Rows != 4 || res.EmptyRows != 1 || res.Phases != 1 || !res.HasExport {
		t.Fatalf("result = %+v", res)
	}
	if res.Gaps != 0 || len(res.Quarters) != 2 {
		t.Fatalf("gaps %d quarters %d", res.Gaps, len(res.Quarters))
	}
	q := res.Quarters[0]
	if q.Start != time.Date(2026, 4, 14, 13, 0, 0, 0, time.UTC) || !near(*q.ImportKWh, 0.1) || !near(*q.ExportKWh, 0.05) || q.Source != store.SourceImport {
		t.Errorf("quarter 0 = %+v", q)
	}
	if !near(*res.Quarters[1].ImportKWh, 0.25) || !near(*res.Quarters[1].ExportKWh, 0) {
		t.Errorf("quarter 1 import %v", *res.Quarters[1].ImportKWh)
	}
}

func TestGasAcrossDST(t *testing.T) {
	// spring: 02:00-02:45 do not exist; autumn: 02:00-02:45 twice
	res := parse(t, "time,Total gas used\n"+
		"2026-03-29 01:30,100.000\n"+
		"2026-03-29 01:45,100.010\n"+
		"2026-03-29 03:00,100.030\n"+ // 15 real minutes after 01:45
		"2026-10-25 01:45,200.000\n"+
		"2026-10-25 02:00,200.010\n"+
		"2026-10-25 02:15,200.020\n"+
		"2026-10-25 02:30,200.030\n"+
		"2026-10-25 02:45,200.040\n"+
		"2026-10-25 02:00,200.050\n"+ // second occurrence (winter time)
		"2026-10-25 02:15,200.060\n"+
		"2026-10-25 02:30,200.070\n"+
		"2026-10-25 02:45,200.080\n"+
		"2026-10-25 03:00,200.090\n")
	if res.BadTimes != 0 || res.Ambiguous != 0 {
		t.Fatalf("bad %d ambiguous %d", res.BadTimes, res.Ambiguous)
	}
	starts := map[time.Time]float64{}
	for _, q := range res.Quarters {
		starts[q.Start] = *q.GasM3
	}
	spring := time.Date(2026, 3, 29, 0, 45, 0, 0, time.UTC) // 01:45 CET
	if v, ok := starts[spring]; !ok || !near(v, 0.02) {
		t.Errorf("spring quarter: %v %v", v, ok)
	}
	// 01:45 CEST .. 03:00 CET is 9 quarters in real time
	var autumn int
	for s := range starts {
		if s.After(time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC)) {
			autumn++
		}
	}
	if autumn != 9 {
		t.Errorf("autumn quarters = %d, want 9", autumn)
	}
	if _, ok := starts[time.Date(2026, 10, 25, 1, 45, 0, 0, time.UTC)]; !ok { // 02:45 CET
		t.Error("missing the second 02:45 quarter")
	}
}

func TestWaterDropoutsAreGaps(t *testing.T) {
	res := parse(t, "time,water usage dl\n"+
		"2026-05-20 03:00,4142213\n"+
		"2026-05-20 03:15,0\n"+
		"2026-05-20 03:30,0\n"+
		"2026-05-20 03:45,4142550\n"+
		"2026-05-20 04:00,4142600\n")
	if res.Kind != KindWater || res.Glitches != 2 || res.Resets != 0 {
		t.Fatalf("result = %+v", res)
	}
	if len(res.Quarters) != 1 || !near(*res.Quarters[0].WaterM3, 0.005) { // 50 dl
		t.Fatalf("quarters = %+v", res.Quarters)
	}
	if !near(res.Unattributed["water_m3"], 0.0337) { // 337 dl during the dropout
		t.Errorf("unattributed = %v", res.Unattributed)
	}
}

func TestResetAndGap(t *testing.T) {
	res := parse(t, "time,Total gas used\n"+
		"2026-06-01 10:00,5.0\n"+
		"2026-06-01 10:15,5.1\n"+
		"2026-06-01 10:45,5.3\n"+ // 30 minutes: gap
		"2026-06-01 11:00,0.2\n"+ // new meter: reset
		"2026-06-01 11:15,0.3\n")
	if res.Gaps != 1 || res.Resets != 1 || len(res.Quarters) != 2 {
		t.Fatalf("gaps %d resets %d quarters %d", res.Gaps, res.Resets, len(res.Quarters))
	}
}

func TestUnknownFormat(t *testing.T) {
	if _, err := Parse(strings.NewReader("datum,verbruik\n2026-01-01,1\n"), "d", ams); err == nil {
		t.Fatal("expected an error")
	}
	if _, err := Parse(strings.NewReader("time,Total gas used\n2026-01-01 00:00,abc\n"), "d", ams); err == nil {
		t.Fatal("expected an error for a bad number")
	}
}

func TestImportFillsOnlyMissing(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	start := time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
	measured := 9.0
	// the gateway already measured electricity for the first quarter
	if err := st.SaveQuarters(ctx, []store.Quarter{{DeviceID: "p1", Start: start, ImportKWh: &measured, Quality: "complete", Coverage: 1}}); err != nil {
		t.Fatal(err)
	}
	elec := parse(t, "time,Import T1 kWh,Import T2 kWh,Export T1 kWh,Export T2 kWh\n"+
		"2026-06-01 10:00,1,1,0,0\n2026-06-01 10:15,1.5,1,0,0\n2026-06-01 10:30,2,1,0,0\n")
	gasRes := parse(t, "time,Total gas used\n2026-06-01 10:00,1\n2026-06-01 10:15,1.25\n")
	for i := range elec.Quarters {
		elec.Quarters[i].DeviceID = "p1"
	}
	for i := range gasRes.Quarters {
		gasRes.Quarters[i].DeviceID = "p1"
	}

	preview, err := st.ImportQuarters(ctx, elec.Quarters, true)
	if err != nil || preview.Added != 2 || preview.Skipped != 0 { // q1 still gets its export value
		t.Fatalf("preview = %+v %v", preview, err)
	}
	if qs, _ := st.Quarters(ctx, "p1", start, start.Add(time.Hour)); len(qs) != 1 {
		t.Fatal("dry run wrote data")
	}
	if _, err := st.ImportQuarters(ctx, elec.Quarters, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ImportQuarters(ctx, gasRes.Quarters, false); err != nil {
		t.Fatal(err)
	}
	again, _ := st.ImportQuarters(ctx, elec.Quarters, false)
	if again.Added != 0 {
		t.Fatalf("second import added %d", again.Added)
	}
	qs, _ := st.Quarters(ctx, "p1", start, start.Add(time.Hour))
	if len(qs) != 2 {
		t.Fatalf("got %d quarters", len(qs))
	}
	if *qs[0].ImportKWh != 9 || qs[0].Source != store.SourceMeasured || !near(*qs[0].GasM3, 0.25) {
		t.Errorf("q0 = import %v source %s gas %v", *qs[0].ImportKWh, qs[0].Source, qs[0].GasM3)
	}
	if !near(*qs[1].ImportKWh, 0.5) || qs[1].Source != store.SourceImport {
		t.Errorf("q1 = %+v", qs[1])
	}
	// a later measurement wins, but keeps imported counters it does not measure
	m2 := 0.4
	if err := st.SaveQuarters(ctx, []store.Quarter{{DeviceID: "p1", Start: start, ImportKWh: &m2, Quality: "complete", Coverage: 1}}); err != nil {
		t.Fatal(err)
	}
	qs, _ = st.Quarters(ctx, "p1", start, start.Add(time.Minute))
	if *qs[0].ImportKWh != 0.4 || qs[0].GasM3 == nil {
		t.Errorf("after measurement: %+v", qs[0])
	}
}
