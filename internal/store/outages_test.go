package store

import (
	"context"
	"testing"
	"time"
)

// An outage opens once, ends once, and is forgotten with its device.
func TestOutages(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	if err := st.SaveDevice(ctx, Device{ID: "p1", Host: "10.0.0.2", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for range 2 {
		if err := st.StartOutage(ctx, "p1", t0, "timeout"); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.EndOutage(ctx, "p1", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err := st.Outages(ctx, "p1", t0.Add(-time.Hour), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].End == nil || !list[0].End.Equal(t0.Add(time.Minute)) || list[0].Message != "timeout" {
		t.Fatalf("outages %+v", list)
	}
	if err := st.DeleteDevice(ctx, "p1", false); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Outages(ctx, "p1", t0.Add(-time.Hour), t0.Add(time.Hour)); len(list) != 0 {
		t.Fatalf("outages of a removed device: %+v", list)
	}
}
