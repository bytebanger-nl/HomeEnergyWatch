package store

import (
	"context"
	"testing"
)

func TestPluginTabs(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	if err := st.SavePlugin(ctx, PluginInstance{ID: "p1", Type: "mhi", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePluginDevice(ctx, PluginDevice{ID: "d1", PluginID: "p1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a, err := st.AddPluginTab(ctx, "Verwarmen")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := st.AddPluginTab(ctx, "Koelen")
	if err := st.RenamePluginTab(ctx, b.ID, "Zomer"); err != nil {
		t.Fatal(err)
	}
	tabs, _ := st.PluginTabs(ctx)
	if len(tabs) != 2 || tabs[0].Name != "Verwarmen" || tabs[1].Name != "Zomer" || tabs[1].Position != 1 {
		t.Fatalf("tabs %+v", tabs)
	}
	if err := st.SetPluginDeviceTab(ctx, "d1", a.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPluginDeviceTab(ctx, "d1", 999); err != ErrNotFound {
		t.Fatalf("unknown tab: %v", err)
	}
	devs, _ := st.AllPluginDevices(ctx)
	if devs[0].TabID != a.ID {
		t.Fatalf("tab %d", devs[0].TabID)
	}
	// a save of the device keeps its tab
	devs[0].Name = "Woonkamer"
	if err := st.SavePluginDevice(ctx, devs[0]); err != nil {
		t.Fatal(err)
	}
	// deleting the tab moves the device back to the default tab
	if err := st.DeletePluginTab(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	devs, _ = st.AllPluginDevices(ctx)
	if devs[0].TabID != 0 || devs[0].Name != "Woonkamer" {
		t.Fatalf("after delete %+v", devs[0])
	}
}
