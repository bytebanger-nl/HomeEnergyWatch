package plugin

import (
	"context"
	"errors"
	"testing"
)

func TestTabs(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, _ := m.AddInstance(ctx, "test", "Airco", nil)
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddTab(ctx, "  "); !errors.Is(err, ErrTabs) {
		t.Fatalf("empty name: %v", err)
	}
	var first int64
	for i, name := range []string{"Verwarmen", "Koelen", "Badkamer"} {
		tab, err := m.AddTab(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = tab.ID
		}
	}
	if _, err := m.AddTab(ctx, "Vierde"); !errors.Is(err, ErrTabs) {
		t.Fatalf("fourth tab: %v", err)
	}
	if err := m.SetDeviceTab(ctx, dev.ID, first); err != nil {
		t.Fatal(err)
	}
	if v := m.DeviceViews(); v[0].TabID != first {
		t.Fatalf("tab in view %d", v[0].TabID)
	}
	if err := m.DeleteTab(ctx, first); err != nil {
		t.Fatal(err)
	}
	if v := m.DeviceViews(); v[0].TabID != 0 {
		t.Fatalf("still on a deleted tab: %d", v[0].TabID)
	}
}
