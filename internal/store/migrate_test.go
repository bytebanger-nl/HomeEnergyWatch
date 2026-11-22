package store

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func userVersion(t *testing.T, path string) int {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var v int
	if err := s.db.QueryRowContext(context.Background(), "PRAGMA user_version").Scan(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

// An update that adds a migration backs up the old database first.
func TestMigrationBacksUpOldSchema(t *testing.T) {
	all := migrations
	t.Cleanup(func() { migrations = all })
	path := filepath.Join(t.TempDir(), "hwlocal.db")

	migrations = all[:len(all)-1] // the previous release
	old := userVersion(t, path)
	migrations = all
	if got := userVersion(t, path); got != len(all) {
		t.Fatalf("schema %d after update, want %d", got, len(all))
	}
	backup := path + ".v" + itoa(old) + ".bak"
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	migrations = all[:len(all)-1]
	if got := userVersion(t, backup); got != old {
		t.Fatalf("backup has schema %d, want %d", got, old)
	}
	migrations = all

	// a fresh database needs no backup
	fresh := filepath.Join(t.TempDir(), "hwlocal.db")
	userVersion(t, fresh)
	if m, _ := filepath.Glob(fresh + ".*.bak"); len(m) != 0 {
		t.Fatalf("backup of a new database: %v", m)
	}
}

// Running an older release against a newer database is refused.
func TestNewerSchemaRefused(t *testing.T) {
	all := migrations
	t.Cleanup(func() { migrations = all })
	path := filepath.Join(t.TempDir(), "hwlocal.db")
	userVersion(t, path)
	migrations = all[:len(all)-1]
	_, err := Open(path)
	if err == nil || !strings.Contains(err.Error(), "nieuwere versie") {
		t.Fatalf("got %v, want a refusal", err)
	}
}

// Migration 13 keeps one meter per plugin device and none for tado rooms.
func TestMigrationSingleMeter(t *testing.T) {
	all := migrations
	t.Cleanup(func() { migrations = all })
	path := filepath.Join(t.TempDir(), "hwlocal.db")
	migrations = all[:12]
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO devices (id, host, created_at, updated_at) VALUES ('kwh', 'h1', 0, 0), ('sock', 'h2', 0, 0)`,
		`INSERT INTO plugins (id, type, created_at, updated_at) VALUES ('m', 'mhi', 0, 0), ('t', 'tado', 0, 0)`,
		`INSERT INTO plugin_devices (id, plugin_id, created_at, updated_at) VALUES ('airco', 'm', 0, 0), ('room', 't', 0, 0)`,
		`INSERT INTO plugin_device_meters (plugin_device_id, device_id) VALUES ('airco', 'kwh'), ('airco', 'sock'), ('room', 'sock')`,
	} {
		if _, err := s.db.Exec(q); err != nil {
			s.Close()
			t.Fatal(q, err)
		}
	}
	s.Close()
	migrations = all
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m, err := s.PluginMeters(context.Background())
	if err != nil || len(m) != 1 || m["airco"] != "kwh" {
		t.Fatalf("after migration %v %v", m, err)
	}
}

func itoa(n int) string { return strconv.Itoa(n) }
