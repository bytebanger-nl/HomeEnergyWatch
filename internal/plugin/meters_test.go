package plugin

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func TestMetersAndHistory(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "test", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.store.SaveDevice(ctx, store.Device{ID: "kwh1"}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetMeter(ctx, dev.ID, "kwh1"); err != nil {
		t.Fatal(err)
	}
	views := m.DeviceViews()
	if len(views) != 1 || views[0].Meter != "kwh1" || !views[0].CanLinkMeter {
		t.Fatalf("views %+v", views)
	}
	if err := m.SetMeter(ctx, "nope", ""); err != store.ErrNotFound {
		t.Fatalf("unknown device: %v", err)
	}
	// a type without electricity use (tado rooms) cannot have a meter
	ro, _ := m.AddInstance(ctx, "testctl", "Kamers", nil)
	room, _ := m.AddDevice(ctx, ro.ID, Candidate{Address: "5.6.7.8"}, "Badkamer")
	if err := m.SetMeter(ctx, room.ID, "kwh1"); !errors.Is(err, ErrNoMeter) {
		t.Fatalf("meter on a type without meters: %v", err)
	}
	for _, v := range m.DeviceViews() {
		if v.ID == room.ID && (v.CanLinkMeter || v.Meter != "") {
			t.Fatalf("room view %+v", v)
		}
	}

	now := time.Now().UTC().Truncate(time.Minute)
	m.record(ctx, dev.ID, now.Add(-90*time.Second), Reading{Online: true, Metrics: []Metric{
		{Key: "room", Kind: MetricNumber, Value: 20.0},
		{Key: "on", Kind: MetricBool, Value: true},
		{Key: "mode", Kind: MetricText, Value: "heat"},
	}})
	m.record(ctx, dev.ID, now.Add(-30*time.Second), Reading{Online: false, Metrics: []Metric{{Key: "room", Value: 99}}})
	pts, step, err := m.Series(ctx, dev.ID, "room", now.Add(-time.Hour), now, 60)
	if err != nil || step != time.Minute || len(pts) != 1 || pts[0].Avg != 20 {
		t.Fatalf("room %+v %v %v", pts, step, err)
	}
	if pts, _, _ := m.Series(ctx, dev.ID, "on", now.Add(-time.Hour), now, 60); len(pts) != 1 || pts[0].Avg != 1 {
		t.Fatalf("bool %+v", pts)
	}
	if pts, _, _ := m.Series(ctx, dev.ID, "mode", now.Add(-time.Hour), now, 60); len(pts) != 0 {
		t.Fatalf("text stored %+v", pts)
	}
	if _, _, err := m.Series(ctx, "nope", "room", now.Add(-time.Hour), now, 60); err != store.ErrNotFound {
		t.Fatalf("unknown device: %v", err)
	}
}
