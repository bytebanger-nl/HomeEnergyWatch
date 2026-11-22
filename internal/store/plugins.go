package store

import (
	"context"
	"encoding/json"
	"time"
)

// PluginInstance is a configured plugin (one provider type with its config).
type PluginInstance struct {
	ID        string
	Type      string
	Name      string
	Config    map[string]string
	Enabled   bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// PluginDevice is one device belonging to a plugin instance.
type PluginDevice struct {
	ID        string
	PluginID  string
	Name      string
	Address   string
	Config    map[string]string
	Enabled   bool
	TabID     int64 // 0 = the default tab
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ensurePluginSchema creates the plugin tables if they are absent. It mirrors
// migration 9, but with IF NOT EXISTS, and runs on every Open regardless of
// the schema version. It exists because the schema version is a single
// counter shared by every branch: a database that reached version 9 through
// the alerts branch's migration 9 (rules/push_subscriptions) has no plugin
// tables, yet migration 9 (plugins) will never run on it. This heals that
// case without data loss. It is a no-op once the tables exist.
func (s *Store) ensurePluginSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS plugins (
			id         TEXT PRIMARY KEY,
			type       TEXT NOT NULL,
			name       TEXT NOT NULL DEFAULT '',
			config     TEXT NOT NULL DEFAULT '{}',
			enabled    INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS plugin_devices (
			id         TEXT PRIMARY KEY,
			plugin_id  TEXT NOT NULL REFERENCES plugins(id) ON DELETE CASCADE,
			name       TEXT NOT NULL DEFAULT '',
			address    TEXT NOT NULL DEFAULT '',
			config     TEXT NOT NULL DEFAULT '{}',
			enabled    INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);
		CREATE INDEX IF NOT EXISTS plugin_devices_plugin ON plugin_devices (plugin_id);`)
	return err
}

func marshalConfig(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func unmarshalConfig(s string) map[string]string {
	m := map[string]string{}
	if s == "" {
		return m
	}
	_ = json.Unmarshal([]byte(s), &m)
	return m
}

// Plugins returns every configured plugin instance, oldest first.
func (s *Store) Plugins(ctx context.Context) ([]PluginInstance, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, type, name, config, enabled, created_at, updated_at FROM plugins ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PluginInstance
	for rows.Next() {
		var p PluginInstance
		var cfg string
		var created, updated int64
		if err := rows.Scan(&p.ID, &p.Type, &p.Name, &cfg, &p.Enabled, &created, &updated); err != nil {
			return nil, err
		}
		p.Config = s.openConfig(cfg)
		p.CreatedAt = time.UnixMilli(created).UTC()
		p.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePlugin inserts or updates a plugin instance.
func (s *Store) SavePlugin(ctx context.Context, p PluginInstance) error {
	now := time.Now().UTC()
	if p.CreatedAt.IsZero() {
		p.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO plugins (id, type, name, config, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET type=excluded.type, name=excluded.name,
			config=excluded.config, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		p.ID, p.Type, p.Name, s.sealConfig(p.Config), p.Enabled, p.CreatedAt.UnixMilli(), now.UnixMilli())
	return err
}

// DeletePlugin removes a plugin instance and its devices (ON DELETE CASCADE).
func (s *Store) DeletePlugin(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM plugins WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return pruneOutages(ctx, s.db)
}

// PluginDevices returns the devices of one plugin instance, oldest first.
func (s *Store) PluginDevices(ctx context.Context, pluginID string) ([]PluginDevice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, plugin_id, name, address, config, enabled, COALESCE(tab_id, 0), created_at, updated_at
		 FROM plugin_devices WHERE plugin_id = ? ORDER BY created_at, id`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanPluginDevices(rows)
}

// AllPluginDevices returns every plugin device, oldest first.
func (s *Store) AllPluginDevices(ctx context.Context) ([]PluginDevice, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, plugin_id, name, address, config, enabled, COALESCE(tab_id, 0), created_at, updated_at
		 FROM plugin_devices ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanPluginDevices(rows)
}

func (s *Store) scanPluginDevices(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]PluginDevice, error) {
	var out []PluginDevice
	for rows.Next() {
		var d PluginDevice
		var cfg string
		var created, updated int64
		if err := rows.Scan(&d.ID, &d.PluginID, &d.Name, &d.Address, &cfg, &d.Enabled, &d.TabID, &created, &updated); err != nil {
			return nil, err
		}
		d.Config = s.openConfig(cfg)
		d.CreatedAt = time.UnixMilli(created).UTC()
		d.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, d)
	}
	return out, rows.Err()
}

// SavePluginDevice inserts or updates a plugin device.
func (s *Store) SavePluginDevice(ctx context.Context, d PluginDevice) error {
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO plugin_devices (id, plugin_id, name, address, config, enabled, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, address=excluded.address,
			config=excluded.config, enabled=excluded.enabled, updated_at=excluded.updated_at`,
		d.ID, d.PluginID, d.Name, d.Address, s.sealConfig(d.Config), d.Enabled, d.CreatedAt.UnixMilli(), now.UnixMilli())
	return err
}

// DeletePluginDevice removes one plugin device.
func (s *Store) DeletePluginDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM plugin_devices WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return pruneOutages(ctx, s.db)
}
