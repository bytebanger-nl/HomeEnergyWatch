// Package store persists devices and measurement history in SQLite.
package store

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite" // pure Go SQLite driver
)

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
	// aead encrypts plugin configs (passwords, keys) when UseKey was
	// called; see secret.go.
	aead cipher.AEAD
}

// Open opens (and creates or migrates) the database file.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	// The database holds device tokens. Create it owner-only before SQLite
	// opens it: SQLite gives the -wal and -shm files it creates the mode of
	// the database file. Files of older versions are tightened too.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	f.Close()
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Chmod(p, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	dsn := "file:" + filepath.ToSlash(path) +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite has a single writer; keeps things simple
	s := &Store{db: db}
	if err := s.migrate(context.Background(), path); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: migrate: %w", err)
	}
	// Idempotent safety net for the plugin tables. Migration 9 creates them,
	// but a database advanced to the same user_version by a different branch's
	// migration 9 (the alerts branch adds rules/push_subscriptions there) would
	// otherwise never get them: the counter is already at 9, so migration 9
	// never runs. CREATE TABLE IF NOT EXISTS closes that gap without touching
	// the version or any existing data. See docs/PLUGINS.md.
	if err := s.ensurePluginSchema(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: plugin schema: %w", err)
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	// 1: initial schema
	`CREATE TABLE devices (
		id            TEXT PRIMARY KEY,
		serial        TEXT NOT NULL DEFAULT '',
		name          TEXT NOT NULL DEFAULT '',
		product_type  TEXT NOT NULL DEFAULT '',
		product_name  TEXT NOT NULL DEFAULT '',
		firmware      TEXT NOT NULL DEFAULT '',
		host          TEXT NOT NULL,
		v2_host       TEXT NOT NULL DEFAULT '',
		api_version   INTEGER NOT NULL DEFAULT 1,
		token         TEXT NOT NULL DEFAULT '',
		v2_available  INTEGER NOT NULL DEFAULT 0,
		enabled       INTEGER NOT NULL DEFAULT 1,
		source        TEXT NOT NULL DEFAULT 'manual',
		created_at    INTEGER NOT NULL,
		updated_at    INTEGER NOT NULL
	);
	CREATE TABLE minute_samples (
		device_id   TEXT NOT NULL,
		minute      INTEGER NOT NULL,
		samples     INTEGER NOT NULL,
		rate_avg REAL,
		rate_min REAL,
		rate_max REAL,
		last_ms     INTEGER NOT NULL,
		import_kwh  REAL,
		export_kwh  REAL,
		gas_m3      REAL,
		water_m3    REAL,
		soc_pct     REAL,
		PRIMARY KEY (device_id, minute)
	) WITHOUT ROWID;
	CREATE TABLE quarter_hours (
		device_id  TEXT NOT NULL,
		start      INTEGER NOT NULL,
		import_kwh REAL,
		export_kwh REAL,
		gas_m3     REAL,
		water_m3   REAL,
		quality    TEXT NOT NULL,
		coverage   REAL NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (device_id, start)
	) WITHOUT ROWID;`,
	// 2: provenance of quarter hours (measured by the gateway or imported)
	`ALTER TABLE quarter_hours ADD COLUMN source TEXT NOT NULL DEFAULT 'measured';`,
	// 3: market prices per delivery period
	`CREATE TABLE prices (
		market     TEXT NOT NULL,
		start      INTEGER NOT NULL,
		duration_s INTEGER NOT NULL,
		eur_mwh    REAL NOT NULL,
		source     TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (market, start)
	) WITHOUT ROWID;`,
	// 4: prices of other commodities (gas per m³): the unit follows the market
	`ALTER TABLE prices RENAME COLUMN eur_mwh TO price;`,
	// 5: energy contracts (the contract itself is JSON, see package costs)
	`CREATE TABLE contracts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		start_date TEXT NOT NULL UNIQUE,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	);`,
	// 6: what-if contracts to compare costs with (JSON, see package costs)
	`CREATE TABLE scenarios (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	);`,
	// 7: water contracts, a timeline of their own next to the energy ones
	`CREATE TABLE water_contracts (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		start_date TEXT NOT NULL UNIQUE,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	);`,
	// 8: grid quality: per-phase voltage, current and power per minute (pruned
	// like minute_samples) with the meter's event counters, and the events
	// derived from those counters (kept forever)
	`CREATE TABLE grid_minutes (
		device_id TEXT NOT NULL,
		minute    INTEGER NOT NULL,
		last_ms   INTEGER NOT NULL,
		v1_min REAL, v1_avg REAL, v1_max REAL, i1_max REAL, p1_avg REAL,
		v2_min REAL, v2_avg REAL, v2_max REAL, i2_max REAL, p2_avg REAL,
		v3_min REAL, v3_avg REAL, v3_max REAL, i3_max REAL, p3_avg REAL,
		sag_l1 REAL, sag_l2 REAL, sag_l3 REAL,
		swell_l1 REAL, swell_l2 REAL, swell_l3 REAL,
		fail_any REAL, fail_long REAL,
		PRIMARY KEY (device_id, minute)
	) WITHOUT ROWID;
	CREATE TABLE grid_events (
		id        INTEGER PRIMARY KEY AUTOINCREMENT,
		device_id TEXT NOT NULL,
		kind      TEXT NOT NULL,
		phase     INTEGER NOT NULL,
		count     INTEGER NOT NULL,
		since_ms  INTEGER NOT NULL,
		at_ms     INTEGER NOT NULL
	);
	CREATE INDEX grid_events_device_at ON grid_events (device_id, at_ms);`,
	// 9: generic plugins and their devices (read-only integrations next to the
	// HomeWizard energy core; config is JSON, see package plugin). IF NOT
	// EXISTS so this stays consistent with store.ensurePluginSchema, the
	// safety net that creates the same tables on a database advanced to this
	// version by another branch's migration 9 (see docs/PLUGINS.md).
	`CREATE TABLE IF NOT EXISTS plugins (
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
	CREATE INDEX IF NOT EXISTS plugin_devices_plugin ON plugin_devices (plugin_id);`,
	// 10: alert rules (JSON, see package rules) with whether they may fire,
	// and the Web Push subscriptions of the PWA installs that get alerts.
	// IF NOT EXISTS because a database that ran the alerts branch's own
	// migration 9 already has these; this renumbered copy must be a no-op
	// there (plugins is 9 on main, so alerts moved to 10 on merge). See
	// docs/PLUGINS.md.
	`CREATE TABLE IF NOT EXISTS rules (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		data       TEXT NOT NULL,
		armed      INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE IF NOT EXISTS push_subscriptions (
		endpoint   TEXT PRIMARY KEY,
		p256dh     TEXT NOT NULL,
		auth       TEXT NOT NULL,
		label      TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL
	);`,
	// 11: HomeWizard meters (sockets, kWh meters) that measure a plugin
	// device (e.g. the indoor and outdoor unit of an air conditioner), and
	// the numeric metrics of plugin devices over time (pruned like
	// minute_samples) for the history next to the meters' usage
	`CREATE TABLE plugin_device_meters (
		plugin_device_id TEXT NOT NULL REFERENCES plugin_devices(id) ON DELETE CASCADE,
		device_id        TEXT NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
		PRIMARY KEY (plugin_device_id, device_id)
	);
	CREATE TABLE plugin_samples (
		device_id TEXT NOT NULL REFERENCES plugin_devices(id) ON DELETE CASCADE,
		metric    TEXT NOT NULL,
		ts        INTEGER NOT NULL,
		value     REAL NOT NULL,
		PRIMARY KEY (device_id, metric, ts)
	) WITHOUT ROWID;`,
	// 12: tabs of their own for plugin devices (at most a few, named by the
	// user); a device without a tab shows on the default Klimaat tab
	`CREATE TABLE plugin_tabs (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		name     TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0
	);
	ALTER TABLE plugin_devices ADD COLUMN tab_id INTEGER REFERENCES plugin_tabs(id) ON DELETE SET NULL;`,
	// 13: one meter per plugin device (a meter may still measure several
	// devices, e.g. air conditioner units on one group): keep the first
	// linked meter, and none for tado rooms, which use no electricity
	`DELETE FROM plugin_device_meters WHERE rowid NOT IN (
		SELECT MIN(rowid) FROM plugin_device_meters GROUP BY plugin_device_id);
	DELETE FROM plugin_device_meters WHERE plugin_device_id IN (
		SELECT d.id FROM plugin_devices d JOIN plugins p ON p.id = d.plugin_id WHERE p.type = 'tado');
	CREATE UNIQUE INDEX plugin_device_meters_one ON plugin_device_meters (plugin_device_id);`,

	// 14: show the meter readings (totals per tariff, gas, water) on a
	// device's card on Live
	`ALTER TABLE devices ADD COLUMN show_counters INTEGER NOT NULL DEFAULT 0;`,

	// 15: the user's own dashboard tiles with values, charts and controls
	// of plugin devices; items is a JSON array checked by the server
	`CREATE TABLE dashboard_tiles (
		id       INTEGER PRIMARY KEY AUTOINCREMENT,
		name     TEXT NOT NULL,
		position INTEGER NOT NULL DEFAULT 0,
		size     TEXT NOT NULL DEFAULT 'small',
		items    TEXT NOT NULL DEFAULT '[]'
	);`,

	// 15: the form of a dashboard tile (how its values are laid out)
	`ALTER TABLE dashboard_tiles ADD COLUMN style TEXT NOT NULL DEFAULT 'value';`,

	// 17: where an automation with wait or regulate actions is (JSON, see
	// package rules), so a restart goes on where it was
	`CREATE TABLE rule_runs (
		rule_id    INTEGER PRIMARY KEY,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	);`,

	// 18: heating via mijn.ista.nl (see docs/ISTA.md): addresses, settlement
	// periods, services, radiators, their factors per period, readings per
	// radiator per day (cumulative within the period), totals and building
	// average per day, months, the last raw answer per call, sync state and
	// the ista contracts per period
	`CREATE TABLE ista_addresses (
		cuid       TEXT PRIMARY KEY,
		account    TEXT NOT NULL,
		address    TEXT NOT NULL DEFAULT '',
		zip        TEXT NOT NULL DEFAULT '',
		city       TEXT NOT NULL DEFAULT '',
		move_in    TEXT NOT NULL DEFAULT '',
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE ista_periods (
		cuid  TEXT NOT NULL,
		y         INTEGER NOT NULL,
		start_day TEXT NOT NULL,
		end_day   TEXT NOT NULL,
		ta        REAL,
		PRIMARY KEY (cuid, y)
	);
	CREATE TABLE ista_services (
		cuid        TEXT NOT NULL,
		service_id  INTEGER NOT NULL,
		description TEXT NOT NULL DEFAULT '',
		meter_type  TEXT NOT NULL DEFAULT '',
		unit        TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (cuid, service_id)
	);
	CREATE TABLE ista_meters (
		meter_id   INTEGER PRIMARY KEY,
		cuid       TEXT NOT NULL,
		service_id INTEGER NOT NULL,
		rad_nr     INTEGER NOT NULL DEFAULT 0,
		position   TEXT NOT NULL DEFAULT '',
		meter_nr   INTEGER NOT NULL DEFAULT 0,
		art_nr     INTEGER NOT NULL DEFAULT 0,
		updated_at INTEGER NOT NULL
	);
	CREATE TABLE ista_meter_periods (
		meter_id      INTEGER NOT NULL,
		y             INTEGER NOT NULL,
		calc_factor   REAL NOT NULL,
		reduction     REAL NOT NULL,
		multiply      REAL NOT NULL,
		transfer_loss REAL NOT NULL,
		begin_value   REAL NOT NULL,
		PRIMARY KEY (meter_id, y)
	);
	CREATE TABLE ista_readings (
		meter_id   INTEGER NOT NULL,
		day        TEXT NOT NULL,
		y          INTEGER NOT NULL,
		c_value    REAL NOT NULL,
		cc_value   REAL NOT NULL,
		ccd_value  REAL NOT NULL,
		end_value  REAL NOT NULL,
		estimated  INTEGER NOT NULL DEFAULT 0,
		fetched_at INTEGER NOT NULL,
		PRIMARY KEY (meter_id, day)
	);
	CREATE TABLE ista_days (
		cuid           TEXT NOT NULL,
		day            TEXT NOT NULL,
		y              INTEGER NOT NULL,
		total_now      REAL NOT NULL,
		building_avg   REAL,
		estimated      INTEGER NOT NULL DEFAULT 0,
		fetched_at     INTEGER NOT NULL,
		avg_fetched_at INTEGER,
		PRIMARY KEY (cuid, day)
	);
	CREATE TABLE ista_months (
		cuid         TEXT NOT NULL,
		service_id   INTEGER NOT NULL,
		year         INTEGER NOT NULL,
		month        INTEGER NOT NULL,
		total        REAL NOT NULL,
		building_avg REAL,
		approx       INTEGER NOT NULL DEFAULT 0,
		avg_temp     REAL,
		fetched_at   INTEGER NOT NULL,
		PRIMARY KEY (cuid, service_id, year, month)
	);
	CREATE TABLE ista_raw (
		endpoint   TEXT NOT NULL,
		key        TEXT NOT NULL,
		fetched_at INTEGER NOT NULL,
		body       TEXT NOT NULL,
		PRIMARY KEY (endpoint, key)
	);
	CREATE TABLE ista_sync (
		account TEXT PRIMARY KEY,
		data    TEXT NOT NULL
	);
	CREATE TABLE ista_contracts (
		cuid       TEXT NOT NULL,
		y          INTEGER NOT NULL,
		data       TEXT NOT NULL,
		updated_at INTEGER NOT NULL,
		PRIMARY KEY (cuid, y)
	);`,

	// 19: e-mail (one row: the SMTP settings, sealed like plugin configs)
	// and scheduled reports (JSON, see package report) with the last period
	// sent, so a restart neither repeats nor skips one
	`CREATE TABLE mail_settings (
		id     INTEGER PRIMARY KEY CHECK (id = 1),
		config TEXT NOT NULL
	);
	CREATE TABLE report_schedules (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		data        TEXT NOT NULL,
		last_period TEXT NOT NULL DEFAULT '',
		last_at     INTEGER NOT NULL DEFAULT 0,
		last_error  TEXT NOT NULL DEFAULT ''
	);`,

	// 20: small settings by key (JSON values), e.g. the forecast settings
	`CREATE TABLE settings (
		key   TEXT PRIMARY KEY,
		value TEXT NOT NULL
	);`,

	// 21: when an automation regulated (end_ms NULL: it still does), so the
	// chart of a rule shows its own regulating, not that of another rule on
	// the same device
	`CREATE TABLE rule_regulate_periods (
		rule_id  INTEGER NOT NULL,
		start_ms INTEGER NOT NULL,
		end_ms   INTEGER
	);
	CREATE INDEX rule_regulate_periods_rule ON rule_regulate_periods (rule_id, start_ms);`,

	// 22: a scheduled report sent on time without heating (ista had not
	// measured the period yet) remembers the period whose heating report
	// still has to follow, with its own last attempt, so a restart neither
	// drops nor repeats it
	`ALTER TABLE report_schedules ADD COLUMN heating_period TEXT NOT NULL DEFAULT '';
	ALTER TABLE report_schedules ADD COLUMN heating_at INTEGER NOT NULL DEFAULT 0;
	ALTER TABLE report_schedules ADD COLUMN heating_error TEXT NOT NULL DEFAULT '';`,

	// 23: the tariff the P1 meter itself registered: its T1 (low, "dal")
	// counters per minute, and per quarter hour the part of the import and
	// export that the meter counted at the low tariff (holidays included)
	`ALTER TABLE minute_samples ADD COLUMN import_low_kwh REAL;
	ALTER TABLE minute_samples ADD COLUMN export_low_kwh REAL;
	ALTER TABLE quarter_hours ADD COLUMN import_low_kwh REAL;
	ALTER TABLE quarter_hours ADD COLUMN export_low_kwh REAL;`,

	// 24: when a device (HomeWizard or plugin) did not respond (end_ms NULL:
	// it still does not); cut: the gateway stopped during it, so its end is
	// when the gateway stopped, not when the device came back
	`CREATE TABLE device_outages (
		device_id TEXT NOT NULL,
		start_ms  INTEGER NOT NULL,
		end_ms    INTEGER,
		cut       INTEGER NOT NULL DEFAULT 0,
		message   TEXT NOT NULL DEFAULT ''
	);
	CREATE INDEX device_outages_device ON device_outages (device_id, start_ms);`,

	// 25: an outage the user confirmed as read (the overview of outages and
	// the notice on the dashboard)
	`ALTER TABLE device_outages ADD COLUMN ack INTEGER NOT NULL DEFAULT 0;`,

	// 26: messages for the user about something that went wrong (a report
	// not mailed, a backup failed, ...), with when it first and last
	// happened and how often while unread; ack: confirmed as read
	`CREATE TABLE notices (
		id       INTEGER PRIMARY KEY,
		kind     TEXT NOT NULL,
		subject  TEXT NOT NULL DEFAULT '',
		title    TEXT NOT NULL,
		text     TEXT NOT NULL DEFAULT '',
		first_ms INTEGER NOT NULL,
		last_ms  INTEGER NOT NULL,
		count    INTEGER NOT NULL DEFAULT 1,
		ack      INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX notices_last ON notices (last_ms);`,

	// 27: whether a manual action (an automation started by hand, from the
	// dashboard) is switched on
	`ALTER TABLE rules ADD COLUMN active INTEGER NOT NULL DEFAULT 0;`,
}

// migrate brings the schema up to date. Before changing an existing
// database it writes a copy to <path>.v<old version>.bak, so an update that
// goes wrong can be undone by putting that file back. A database written by
// a newer version is refused rather than used with an older schema.
func (s *Store) migrate(ctx context.Context, path string) error {
	var version int
	if err := s.db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("database heeft schemaversie %d, dit programma kent tot en met %d: installeer de nieuwere versie van hwlocal", version, len(migrations))
	}
	if version > 0 && version < len(migrations) {
		backup := fmt.Sprintf("%s.v%d.bak", path, version)
		if _, err := os.Stat(backup); errors.Is(err, os.ErrNotExist) {
			if _, err := s.db.ExecContext(ctx, "VACUUM INTO ?", backup); err != nil {
				return fmt.Errorf("backup voor migratie: %w", err)
			}
			if err := os.Chmod(backup, 0o600); err != nil {
				return err
			}
		}
	}
	for i := version; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", i+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// Device is a configured HomeWizard device.
type Device struct {
	ID          string
	Serial      string
	Name        string
	ProductType string
	ProductName string
	Firmware    string
	Host        string
	// V2Host is the address of the API v2 (HTTPS) endpoint, when known.
	V2Host      string
	APIVersion  int
	Token       string
	V2Available bool
	Enabled     bool
	Source      string
	// ShowCounters shows the meter readings on the device's card on Live.
	ShowCounters bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// ErrNotFound is returned when a device does not exist.
var ErrNotFound = errors.New("store: not found")

const deviceColumns = `id, serial, name, product_type, product_name, firmware, host, v2_host, api_version,
	token, v2_available, enabled, source, show_counters, created_at, updated_at`

func scanDevice(row interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var created, updated int64
	err := row.Scan(&d.ID, &d.Serial, &d.Name, &d.ProductType, &d.ProductName, &d.Firmware,
		&d.Host, &d.V2Host, &d.APIVersion, &d.Token, &d.V2Available, &d.Enabled, &d.Source, &d.ShowCounters, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	d.CreatedAt = time.UnixMilli(created).UTC()
	d.UpdatedAt = time.UnixMilli(updated).UTC()
	return d, err
}

// Devices returns all devices ordered by creation.
func (s *Store) Devices(ctx context.Context) ([]Device, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT "+deviceColumns+" FROM devices ORDER BY created_at, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Device returns one device.
func (s *Store) Device(ctx context.Context, id string) (Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, "SELECT "+deviceColumns+" FROM devices WHERE id = ?", id))
}

// SaveDevice inserts or updates a device.
func (s *Store) SaveDevice(ctx context.Context, d Device) error {
	now := time.Now().UTC()
	if d.CreatedAt.IsZero() {
		d.CreatedAt = now
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO devices (`+deviceColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET serial=excluded.serial, name=excluded.name,
			product_type=excluded.product_type, product_name=excluded.product_name,
			firmware=excluded.firmware, host=excluded.host, v2_host=excluded.v2_host, api_version=excluded.api_version,
			token=excluded.token, v2_available=excluded.v2_available, enabled=excluded.enabled,
			source=excluded.source, show_counters=excluded.show_counters, updated_at=excluded.updated_at`,
		d.ID, d.Serial, d.Name, d.ProductType, d.ProductName, d.Firmware, d.Host, d.V2Host, d.APIVersion,
		d.Token, d.V2Available, d.Enabled, d.Source, d.ShowCounters, d.CreatedAt.UnixMilli(), now.UnixMilli())
	return err
}

// DeleteDevice removes a device and, if purge is set, its history.
func (s *Store) DeleteDevice(ctx context.Context, id string, purge bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, "DELETE FROM devices WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if purge {
		if _, err := tx.ExecContext(ctx, "DELETE FROM minute_samples WHERE device_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM quarter_hours WHERE device_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM grid_minutes WHERE device_id = ?", id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM grid_events WHERE device_id = ?", id); err != nil {
			return err
		}
	}
	if err := pruneOutages(ctx, tx); err != nil {
		return err
	}
	return tx.Commit()
}
