package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// GridPhase is the aggregate of one phase in one minute: voltage (V)
// min/avg/max, the largest absolute current (A) and the average power (W,
// negative when exporting).
type GridPhase struct {
	VMin *float64 `json:"v_min,omitempty"`
	VAvg *float64 `json:"v_avg,omitempty"`
	VMax *float64 `json:"v_max,omitempty"`
	IMax *float64 `json:"i_max,omitempty"`
	PAvg *float64 `json:"p_avg,omitempty"`
}

// Empty reports whether the phase has no values.
func (p GridPhase) Empty() bool {
	return p.VMin == nil && p.VAvg == nil && p.VMax == nil && p.IMax == nil && p.PAvg == nil
}

// GridCounters are the meter's cumulative event counters (DSMR): voltage
// sags and swells per phase and power failures (any and long).
type GridCounters struct {
	Sag      [3]*float64
	Swell    [3]*float64
	AnyFail  *float64
	LongFail *float64
}

// Empty reports whether no counter is set.
func (c GridCounters) Empty() bool {
	for i := range 3 {
		if c.Sag[i] != nil || c.Swell[i] != nil {
			return false
		}
	}
	return c.AnyFail == nil && c.LongFail == nil
}

// GridMinute is the grid quality of one device in one minute. Counters hold
// the last values seen in that minute, observed at Last.
type GridMinute struct {
	DeviceID string
	Minute   time.Time
	Last     time.Time
	Phases   [3]GridPhase
	Counters GridCounters
}

const gridColumns = `minute, last_ms,
	v1_min, v1_avg, v1_max, i1_max, p1_avg,
	v2_min, v2_avg, v2_max, i2_max, p2_avg,
	v3_min, v3_avg, v3_max, i3_max, p3_avg,
	sag_l1, sag_l2, sag_l3, swell_l1, swell_l2, swell_l3, fail_any, fail_long`

// SaveGridMinute upserts one grid minute.
func (s *Store) SaveGridMinute(ctx context.Context, m GridMinute) error {
	args := []any{m.DeviceID, m.Minute.Unix(), m.Last.UnixMilli()}
	for _, p := range m.Phases {
		args = append(args, p.VMin, p.VAvg, p.VMax, p.IMax, p.PAvg)
	}
	c := m.Counters
	args = append(args, c.Sag[0], c.Sag[1], c.Sag[2], c.Swell[0], c.Swell[1], c.Swell[2], c.AnyFail, c.LongFail)
	_, err := s.db.ExecContext(ctx, `INSERT OR REPLACE INTO grid_minutes (device_id, `+gridColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, args...)
	return err
}

func scanGridMinute(row interface{ Scan(...any) error }, deviceID string) (GridMinute, error) {
	var minute, last int64
	var v [23]sql.NullFloat64
	dest := []any{&minute, &last}
	for i := range v {
		dest = append(dest, &v[i])
	}
	if err := row.Scan(dest...); err != nil {
		return GridMinute{}, err
	}
	m := GridMinute{DeviceID: deviceID, Minute: time.Unix(minute, 0).UTC(), Last: time.UnixMilli(last).UTC()}
	for i := range 3 {
		b := i * 5
		m.Phases[i] = GridPhase{VMin: ptr(v[b]), VAvg: ptr(v[b+1]), VMax: ptr(v[b+2]), IMax: ptr(v[b+3]), PAvg: ptr(v[b+4])}
		m.Counters.Sag[i] = ptr(v[15+i])
		m.Counters.Swell[i] = ptr(v[18+i])
	}
	m.Counters.AnyFail, m.Counters.LongFail = ptr(v[21]), ptr(v[22])
	return m, nil
}

// GridMinutes returns grid minutes with from <= minute < to, oldest first.
func (s *Store) GridMinutes(ctx context.Context, deviceID string, from, to time.Time) ([]GridMinute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+gridColumns+` FROM grid_minutes
		WHERE device_id = ? AND minute >= ? AND minute < ? ORDER BY minute`, deviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GridMinute
	for rows.Next() {
		m, err := scanGridMinute(rows, deviceID)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// LastGridCounters returns the most recent stored grid minute that has
// event counters, or ErrNotFound.
func (s *Store) LastGridCounters(ctx context.Context, deviceID string) (GridMinute, error) {
	m, err := scanGridMinute(s.db.QueryRowContext(ctx, `SELECT `+gridColumns+` FROM grid_minutes
		WHERE device_id = ? AND COALESCE(sag_l1, sag_l2, sag_l3, swell_l1, swell_l2, swell_l3, fail_any, fail_long) IS NOT NULL
		ORDER BY minute DESC LIMIT 1`, deviceID), deviceID)
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// Grid event kinds.
const (
	GridSag      = "sag"
	GridSwell    = "swell"
	GridFail     = "power_fail"
	GridLongFail = "long_power_fail"
)

// GridEvent is an increase of one of the meter's event counters. The meter
// does not say when it happened: it happened after Since (the previous
// reading) and at or before At (the reading that showed the increase). For a
// power failure the dongle loses power as well, so Since..At roughly spans
// the outage.
type GridEvent struct {
	ID       int64     `json:"id"`
	DeviceID string    `json:"device_id"`
	Kind     string    `json:"kind"`
	Phase    int       `json:"phase,omitempty"` // 1-3 for sags and swells
	Count    int       `json:"count"`
	Since    time.Time `json:"since"`
	At       time.Time `json:"at"`
}

// AddGridEvents stores events.
func (s *Store) AddGridEvents(ctx context.Context, events []GridEvent) error {
	if len(events) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if _, err := tx.ExecContext(ctx, `INSERT INTO grid_events (device_id, kind, phase, count, since_ms, at_ms)
			VALUES (?, ?, ?, ?, ?, ?)`, e.DeviceID, e.Kind, e.Phase, e.Count, e.Since.UnixMilli(), e.At.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GridEvents returns the events of a device with from <= at < to, oldest
// first.
func (s *Store) GridEvents(ctx context.Context, deviceID string, from, to time.Time) ([]GridEvent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, phase, count, since_ms, at_ms FROM grid_events
		WHERE device_id = ? AND at_ms >= ? AND at_ms < ? ORDER BY at_ms, id`, deviceID, from.UnixMilli(), to.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GridEvent
	for rows.Next() {
		e := GridEvent{DeviceID: deviceID}
		var since, at int64
		if err := rows.Scan(&e.ID, &e.Kind, &e.Phase, &e.Count, &since, &at); err != nil {
			return nil, err
		}
		e.Since, e.At = time.UnixMilli(since).UTC(), time.UnixMilli(at).UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}
