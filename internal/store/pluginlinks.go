package store

import (
	"context"
	"time"
)

// PluginMeters returns, per plugin device id, the HomeWizard device (socket
// or kWh meter) linked to it as its meter. One meter can measure several
// plugin devices.
func (s *Store) PluginMeters(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plugin_device_id, device_id FROM plugin_device_meters`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var pd, d string
		if err := rows.Scan(&pd, &d); err != nil {
			return nil, err
		}
		out[pd] = d
	}
	return out, rows.Err()
}

// SetPluginMeter links one meter to a plugin device, replacing any other;
// an empty deviceID removes the link. An unknown HomeWizard device fails on
// the foreign key.
func (s *Store) SetPluginMeter(ctx context.Context, pluginDeviceID, deviceID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM plugin_devices WHERE id = ?", pluginDeviceID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM plugin_device_meters WHERE plugin_device_id = ?", pluginDeviceID); err != nil {
		return err
	}
	if deviceID != "" {
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO plugin_device_meters (plugin_device_id, device_id) VALUES (?, ?)", pluginDeviceID, deviceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PluginSample is one numeric metric value of a plugin device.
type PluginSample struct {
	Metric string
	Value  float64
}

// AddPluginSamples stores the numeric metrics of one plugin reading at t.
// A device that no longer exists is ignored (the foreign key would fail).
func (s *Store) AddPluginSamples(ctx context.Context, deviceID string, t time.Time, samples []PluginSample) error {
	if len(samples) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, smp := range samples {
		if _, err := tx.ExecContext(ctx,
			`INSERT OR REPLACE INTO plugin_samples (device_id, metric, ts, value)
			 SELECT ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM plugin_devices WHERE id = ?)`,
			deviceID, smp.Metric, t.UnixMilli(), smp.Value, deviceID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PluginPoint is the average of a plugin metric over one bucket.
type PluginPoint struct {
	T   time.Time `json:"t"`
	Avg float64   `json:"avg"`
	Min float64   `json:"min"`
	Max float64   `json:"max"`
}

// PluginSeries returns one metric of a plugin device in [from, to), averaged
// per step (at least one second).
func (s *Store) PluginSeries(ctx context.Context, deviceID, metric string, from, to time.Time, step time.Duration) ([]PluginPoint, error) {
	stepMs := step.Milliseconds()
	if stepMs < 1000 {
		stepMs = 1000
	}
	f := from.UnixMilli()
	rows, err := s.db.QueryContext(ctx,
		`SELECT (ts - ?) / ? AS b, AVG(value), MIN(value), MAX(value)
		 FROM plugin_samples WHERE device_id = ? AND metric = ? AND ts >= ? AND ts < ?
		 GROUP BY b ORDER BY b`,
		f, stepMs, deviceID, metric, f, to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginPoint{}
	for rows.Next() {
		var b int64
		var p PluginPoint
		if err := rows.Scan(&b, &p.Avg, &p.Min, &p.Max); err != nil {
			return nil, err
		}
		p.T = time.UnixMilli(f + b*stepMs).UTC()
		out = append(out, p)
	}
	return out, rows.Err()
}

// PrunePluginSamples deletes plugin samples older than before.
func (s *Store) PrunePluginSamples(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM plugin_samples WHERE ts < ?", before.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
