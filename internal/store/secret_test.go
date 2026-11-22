package store

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPluginConfigEncrypted(t *testing.T) {
	st, dir := openTest(t)
	ctx := context.Background()
	// a config stored before encryption existed
	if err := st.SavePlugin(ctx, PluginInstance{ID: "p1", Type: "ista", Config: map[string]string{"password": "geheim"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.SavePluginDevice(ctx, PluginDevice{ID: "d1", PluginID: "p1", Config: map[string]string{"local_key": "sleutel"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	key, err := LoadOrCreateKey(filepath.Join(filepath.Dir(dir), "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if again, _ := LoadOrCreateKey(filepath.Join(filepath.Dir(dir), "secret.key")); string(again) != string(key) {
		t.Fatal("key not stable")
	}
	if err := st.UseKey(ctx, key); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SELECT config FROM plugins", "SELECT config FROM plugin_devices"} {
		var raw string
		if err := st.db.QueryRowContext(ctx, q).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(raw, sealPrefix) || strings.Contains(raw, "geheim") || strings.Contains(raw, "sleutel") {
			t.Fatalf("not encrypted: %s", raw)
		}
	}
	ps, _ := st.Plugins(ctx)
	ds, _ := st.AllPluginDevices(ctx)
	if ps[0].Config["password"] != "geheim" || ds[0].Config["local_key"] != "sleutel" {
		t.Fatalf("roundtrip: %v %v", ps[0].Config, ds[0].Config)
	}
	// without the key the secrets are gone, not garbage
	st.aead = nil
	ps, _ = st.Plugins(ctx)
	if len(ps[0].Config) != 0 {
		t.Fatalf("config without key: %v", ps[0].Config)
	}
}
