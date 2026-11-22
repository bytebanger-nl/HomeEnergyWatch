package store

import (
	"context"
	"database/sql"
	"time"
)

// Outage is a time a device did not respond. End is nil while it still does
// not; Cut means the gateway stopped during it, so End is when the gateway
// stopped rather than when the device came back.
type Outage struct {
	DeviceID string     `json:"device_id"`
	Start    time.Time  `json:"start"`
	End      *time.Time `json:"end,omitempty"`
	Cut      bool       `json:"cut,omitempty"`
	Message  string     `json:"message,omitempty"`
	Read     bool       `json:"read"` // confirmed as read by the user
}

// StartOutage records that device id does not respond since start, unless
// an outage of it is already open.
func (s *Store) StartOutage(ctx context.Context, id string, start time.Time, message string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO device_outages (device_id, start_ms, message)
		SELECT ?, ?, ? WHERE NOT EXISTS (SELECT 1 FROM device_outages WHERE device_id = ? AND end_ms IS NULL)`,
		id, start.UnixMilli(), message, id)
	return err
}

// EndOutage ends the open outage of device id, if any.
func (s *Store) EndOutage(ctx context.Context, id string, end time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE device_outages SET end_ms = max(start_ms, ?) WHERE device_id = ? AND end_ms IS NULL`,
		end.UnixMilli(), id)
	return err
}

// AliveKey is the setting with the last moment (Unix ms) the gateway noted
// it was running (package outage, every minute).
const AliveKey = "outage_alive_ms"

// CutOutages ends every open outage at end, marked as cut short by the
// gateway stopping. A zero end uses the last moment the gateway is known to
// have run: the later of its last heartbeat (AliveKey) and its last stored
// sample (any device), or else the outage's start.
func (s *Store) CutOutages(ctx context.Context, end time.Time) error {
	if !end.IsZero() {
		_, err := s.db.ExecContext(ctx, `UPDATE device_outages SET end_ms = max(start_ms, ?), cut = 1 WHERE end_ms IS NULL`,
			end.UnixMilli())
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE device_outages
		SET end_ms = max(start_ms, coalesce((SELECT max(last_ms) FROM minute_samples), 0),
			coalesce((SELECT max(ts) FROM plugin_samples), 0),
			coalesce((SELECT CAST(value AS INTEGER) FROM settings WHERE key = ?), 0)), cut = 1
		WHERE end_ms IS NULL`, AliveKey)
	return err
}

// Outages returns the outages of device id (every device when id is "")
// that overlap [from, to), newest first.
func (s *Store) Outages(ctx context.Context, id string, from, to time.Time) ([]Outage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, start_ms, end_ms, cut, message, ack FROM device_outages
		WHERE (? = '' OR device_id = ?) AND start_ms < ? AND (end_ms IS NULL OR end_ms > ?) ORDER BY start_ms DESC`,
		id, id, to.UnixMilli(), from.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Outage{}
	for rows.Next() {
		var start int64
		var end *int64
		var o Outage
		if err := rows.Scan(&o.DeviceID, &start, &end, &o.Cut, &o.Message, &o.Read); err != nil {
			return nil, err
		}
		o.Start = time.UnixMilli(start)
		if end != nil {
			t := time.UnixMilli(*end)
			o.End = &t
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// OutageState is what is not read yet of one device: its unread outages,
// and whether it is in an outage now (read or not).
type OutageState struct {
	DeviceID string `json:"device_id"`
	Unread   int    `json:"unread"`
	Open     bool   `json:"open"`
}

// UnreadOutages returns, per device with unread or open outages, how many
// are unread and whether one is open.
func (s *Store) UnreadOutages(ctx context.Context) ([]OutageState, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT device_id, sum(ack = 0), max(end_ms IS NULL) FROM device_outages
		WHERE ack = 0 OR end_ms IS NULL GROUP BY device_id ORDER BY device_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []OutageState{}
	for rows.Next() {
		var st OutageState
		if err := rows.Scan(&st.DeviceID, &st.Unread, &st.Open); err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// ReadOutages marks the outages of device id (every device when id is "")
// that started at or before until as read, and returns how many changed.
// Outages that start later (after the user looked) stay unread.
func (s *Store) ReadOutages(ctx context.Context, id string, until time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE device_outages SET ack = 1
		WHERE ack = 0 AND (? = '' OR device_id = ?) AND start_ms <= ?`, id, id, until.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// pruneOutages forgets the outages of devices that no longer exist (a
// HomeWizard device, plugin device or plugin was removed).
func pruneOutages(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}) error {
	_, err := db.ExecContext(ctx, `DELETE FROM device_outages
		WHERE device_id NOT IN (SELECT id FROM devices UNION SELECT id FROM plugin_devices)`)
	return err
}
