package history

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newTestRecorder(st *store.Store) *Recorder {
	return NewRecorder(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Location: time.UTC})
}

func reading(at time.Time, values map[string]float64) homewizard.Reading {
	return homewizard.Reading{Time: at, Values: values}
}

func TestGridSeriesPerPhase(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rec := newTestRecorder(st)
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	for i, v := range []float64{228, 232, 230} {
		rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(time.Duration(i)*20*time.Second), map[string]float64{
			"power_w": 900, "voltage_l1_v": v, "voltage_l2_v": 231, "voltage_l3_v": 229,
			"current_l1_a": -float64(i + 1), "current_l2_a": 1, "current_l3_a": 0,
			"power_l1_w": 600, "power_l2_w": 200, "power_l3_w": float64(100 * i),
		}))
	}
	// the open minute is part of the series before it is flushed
	pts, step, phases, err := rec.GridSeries(ctx, "p1", t0, t0.Add(time.Hour), 720)
	if err != nil {
		t.Fatal(err)
	}
	if step != time.Minute || phases != 3 || len(pts) != 1 {
		t.Fatalf("step %v, phases %d, %d points", step, phases, len(pts))
	}
	l1 := pts[0].Phases[0]
	if *l1.VMin != 228 || *l1.VMax != 232 || !near(*l1.VAvg, 230) {
		t.Fatalf("L1 voltage %v/%v/%v", *l1.VMin, *l1.VAvg, *l1.VMax)
	}
	if *l1.IMax != 3 { // absolute: v2 reports export as negative current
		t.Fatalf("L1 current max %v, want 3", *l1.IMax)
	}
	if l3 := pts[0].Phases[2]; !near(*l3.PAvg, 100) {
		t.Fatalf("L3 power avg %v", *l3.PAvg)
	}

	// next minute flushes the first to the store
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(time.Minute), map[string]float64{"voltage_l1_v": 240}))
	stored, err := st.GridMinutes(ctx, "p1", t0, t0.Add(time.Hour))
	if err != nil || len(stored) != 1 || *stored[0].Phases[1].VAvg != 231 {
		t.Fatalf("stored %+v, %v", stored, err)
	}
	pts, _, phases, _ = rec.GridSeries(ctx, "p1", t0, t0.Add(time.Hour), 720)
	if len(pts) != 2 || phases != 3 || pts[1].Phases[1] != nil || *pts[1].Phases[0].VAvg != 240 {
		t.Fatalf("series after flush: %d points, phases %d", len(pts), phases)
	}
}

func TestGridSinglePhaseMeter(t *testing.T) {
	ctx := context.Background()
	rec := newTestRecorder(openStore(t))
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	rec.Add(ctx, "kwh", homewizard.KindKWH, reading(t0, map[string]float64{"power_w": 500, "voltage_v": 230.4, "current_a": 2.2}))
	rec.Add(ctx, "skt", homewizard.KindSocket, reading(t0, map[string]float64{"power_w": 50, "voltage_v": 230.4}))
	pts, _, phases, _ := rec.GridSeries(ctx, "kwh", t0, t0.Add(time.Hour), 720)
	if phases != 1 || len(pts) != 1 || *pts[0].Phases[0].VAvg != 230.4 || *pts[0].Phases[0].PAvg != 500 {
		t.Fatalf("single phase: phases %d, %+v", phases, pts)
	}
	if pts, _, _, _ := rec.GridSeries(ctx, "skt", t0, t0.Add(time.Hour), 720); len(pts) != 0 {
		t.Fatal("sockets have no grid series")
	}
}

func TestGridEventsFromCounters(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	rec := newTestRecorder(st)
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	counters := func(sagL2, fail float64) map[string]float64 {
		return map[string]float64{"voltage_l1_v": 230, "voltage_sag_l1_count": 0, "voltage_sag_l2_count": sagL2,
			"voltage_swell_l1_count": 0, "any_power_fail_count": fail, "long_power_fail_count": 1}
	}
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0, counters(3, 5))) // first sight: baseline only
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(time.Second), counters(3, 5)))
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(2*time.Second), counters(4, 5)))
	events, err := st.GridEvents(ctx, "p1", t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != store.GridSag || events[0].Phase != 2 || events[0].Count != 1 ||
		!events[0].Since.Equal(t0.Add(time.Second)) || !events[0].At.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("events %+v", events)
	}

	// the gateway stops, the power fails, and a new gateway finds it
	rec.Flush(ctx)
	rec2 := newTestRecorder(st)
	back := t0.Add(10 * time.Minute)
	rec2.Add(ctx, "p1", homewizard.KindP1, reading(back, counters(4, 6)))
	events, _ = st.GridEvents(ctx, "p1", t0, t0.Add(time.Hour))
	if len(events) != 2 || events[1].Kind != store.GridFail || events[1].Phase != 0 ||
		!events[1].Since.Equal(t0.Add(2*time.Second)) || !events[1].At.Equal(back) {
		t.Fatalf("event across restart %+v", events)
	}

	// a lower counter (new meter) is a new baseline, not an event
	rec2.Add(ctx, "p1", homewizard.KindP1, reading(back.Add(time.Second), counters(0, 0)))
	rec2.Add(ctx, "p1", homewizard.KindP1, reading(back.Add(2*time.Second), counters(0, 0)))
	if events, _ = st.GridEvents(ctx, "p1", t0, t0.Add(time.Hour)); len(events) != 2 {
		t.Fatalf("reset made events: %+v", events)
	}
}
