package store

import (
	"context"
	"testing"
)

func TestPluginCRUD(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()

	p := PluginInstance{ID: "p1", Type: "mhi", Name: "Airco", Config: map[string]string{"cidr": "192.168.1.0/24"}, Enabled: true}
	if err := st.SavePlugin(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, err := st.Plugins(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("Plugins: %v %v", got, err)
	}
	if got[0].Name != "Airco" || got[0].Config["cidr"] != "192.168.1.0/24" || !got[0].Enabled {
		t.Fatalf("roundtrip mismatch: %+v", got[0])
	}

	// update
	p.Name = "Woonkamer"
	p.Enabled = false
	if err := st.SavePlugin(ctx, p); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Plugins(ctx)
	if got[0].Name != "Woonkamer" || got[0].Enabled {
		t.Fatalf("update not applied: %+v", got[0])
	}

	// a device, then cascade delete with the plugin
	d := PluginDevice{ID: "d1", PluginID: "p1", Name: "Airco woonkamer", Address: "192.168.1.43", Config: map[string]string{"airconId": "348e89f204ba"}, Enabled: true}
	if err := st.SavePluginDevice(ctx, d); err != nil {
		t.Fatal(err)
	}
	devs, err := st.PluginDevices(ctx, "p1")
	if err != nil || len(devs) != 1 || devs[0].Config["airconId"] != "348e89f204ba" {
		t.Fatalf("PluginDevices: %v %v", devs, err)
	}

	if err := st.DeletePlugin(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	devs, _ = st.AllPluginDevices(ctx)
	if len(devs) != 0 {
		t.Fatalf("devices not cascade-deleted: %v", devs)
	}
}

// A database that reached the current schema version through a different
// branch's migration (so the plugin tables were never created) must heal
// itself on Open: ensurePluginSchema recreates the tables without changing the
// version or losing data.
func TestEnsurePluginSchemaHeals(t *testing.T) {
	st, path := openTest(t)
	ctx := context.Background()

	// Simulate the collision: drop the plugin tables but leave the schema
	// version where it is, as if another branch's migration had advanced it.
	var version int
	if err := st.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, "DROP TABLE plugin_devices; DROP TABLE plugins"); err != nil {
		t.Fatal(err)
	}
	st.Close()

	// Reopen: migrate runs nothing (version unchanged), the safety net recreates.
	st2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { st2.Close() })
	var after int
	if err := st2.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != version {
		t.Errorf("user_version changed: %d -> %d", version, after)
	}
	if err := st2.SavePlugin(ctx, PluginInstance{ID: "x", Type: "mhi", Name: "A", Enabled: true}); err != nil {
		t.Fatalf("plugins table not healed: %v", err)
	}
	if got, _ := st2.Plugins(ctx); len(got) != 1 {
		t.Fatalf("expected 1 plugin after heal, got %d", len(got))
	}
}

func TestDeletePluginNotFound(t *testing.T) {
	st, _ := openTest(t)
	if err := st.DeletePlugin(context.Background(), "nope"); err == nil {
		t.Error("want error deleting a missing plugin")
	}
}
