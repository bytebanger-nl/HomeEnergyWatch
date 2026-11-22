package store

import (
	"context"
	"testing"
	"time"
)

func TestPluginMetersAndSamples(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	if err := st.SavePlugin(ctx, PluginInstance{ID: "p1", Type: "mhi", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"d1", "d2"} {
		if err := st.SavePluginDevice(ctx, PluginDevice{ID: id, PluginID: "p1", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"sock", "kwh"} {
		if err := st.SaveDevice(ctx, Device{ID: id}); err != nil {
			t.Fatal(err)
		}
	}

	// one meter per device; a new one replaces the old
	if err := st.SetPluginMeter(ctx, "d1", "sock"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPluginMeter(ctx, "d1", "kwh"); err != nil {
		t.Fatal(err)
	}
	// the same meter may measure another device too (units on one group)
	if err := st.SetPluginMeter(ctx, "d2", "kwh"); err != nil {
		t.Fatal(err)
	}
	m, err := st.PluginMeters(ctx)
	if err != nil || len(m) != 2 || m["d1"] != "kwh" || m["d2"] != "kwh" {
		t.Fatalf("meters %v %v", m, err)
	}
	if err := st.SetPluginMeter(ctx, "missing", "kwh"); err != ErrNotFound {
		t.Fatalf("unknown plugin device: %v", err)
	}
	if err := st.SetPluginMeter(ctx, "d1", "nope"); err == nil {
		t.Fatal("unknown meter accepted")
	}
	if err := st.SetPluginMeter(ctx, "d2", ""); err != nil {
		t.Fatal(err)
	}
	// a removed HomeWizard device drops its link
	if err := st.DeleteDevice(ctx, "kwh", false); err != nil {
		t.Fatal(err)
	}
	if m, _ = st.PluginMeters(ctx); len(m) != 0 {
		t.Fatalf("after unlink and delete %v", m)
	}
	if err := st.SetPluginMeter(ctx, "d1", "sock"); err != nil {
		t.Fatal(err)
	}

	t0 := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	for i, v := range []float64{20, 21, 22, 23} {
		if err := st.AddPluginSamples(ctx, "d1", t0.Add(time.Duration(i)*30*time.Second), []PluginSample{{Metric: "temp", Value: v}}); err != nil {
			t.Fatal(err)
		}
	}
	// unknown device: ignored, no error
	if err := st.AddPluginSamples(ctx, "gone", t0, []PluginSample{{Metric: "temp", Value: 1}}); err != nil {
		t.Fatal(err)
	}
	pts, err := st.PluginSeries(ctx, "d1", "temp", t0, t0.Add(2*time.Minute), time.Minute)
	if err != nil || len(pts) != 2 || pts[0].Avg != 20.5 || pts[1].Max != 23 || !pts[1].T.Equal(t0.Add(time.Minute)) {
		t.Fatalf("series %+v %v", pts, err)
	}
	if n, err := st.PrunePluginSamples(ctx, t0.Add(time.Minute)); err != nil || n != 2 {
		t.Fatalf("prune %d %v", n, err)
	}
	// samples and links go with the plugin device
	if err := st.DeletePluginDevice(ctx, "d1"); err != nil {
		t.Fatal(err)
	}
	m, _ = st.PluginMeters(ctx)
	pts, _ = st.PluginSeries(ctx, "d1", "temp", t0, t0.Add(2*time.Minute), time.Minute)
	if len(m) != 0 || len(pts) != 0 {
		t.Fatalf("cascade: %v %v", m, pts)
	}
}
