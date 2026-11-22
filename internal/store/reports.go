package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MailConfig returns the e-mail settings (an empty map when there are
// none). They are stored like plugin configs: encrypted once a key is set.
func (s *Store) MailConfig(ctx context.Context) (map[string]string, error) {
	var cfg string
	err := s.db.QueryRowContext(ctx, `SELECT config FROM mail_settings WHERE id = 1`).Scan(&cfg)
	if errors.Is(err, sql.ErrNoRows) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	return s.openConfig(cfg), nil
}

// SaveMailConfig replaces the e-mail settings; an empty map removes them.
func (s *Store) SaveMailConfig(ctx context.Context, cfg map[string]string) error {
	if len(cfg) == 0 {
		_, err := s.db.ExecContext(ctx, `DELETE FROM mail_settings`)
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO mail_settings (id, config) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET config = excluded.config`, s.sealConfig(cfg))
	return err
}

// StoredSchedule is a scheduled report as JSON (see package report) with
// the period it was last sent for.
type StoredSchedule struct {
	ID         int64
	Data       []byte
	LastPeriod string    // e.g. "2026-10" or "2026-11-02"; "" = never
	LastAt     time.Time // zero = never
	LastError  string

	// HeatingPeriod: the period that was sent without heating, whose
	// heating report still has to follow ("" = none); HeatingAt and
	// HeatingError are its last attempt.
	HeatingPeriod string
	HeatingAt     time.Time
	HeatingError  string
}

// ReportSchedules returns all scheduled reports in the order they were made.
func (s *Store) ReportSchedules(ctx context.Context) ([]StoredSchedule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data, last_period, last_at, last_error, heating_period, heating_at, heating_error FROM report_schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredSchedule
	for rows.Next() {
		var r StoredSchedule
		var at, hat int64
		if err := rows.Scan(&r.ID, &r.Data, &r.LastPeriod, &at, &r.LastError, &r.HeatingPeriod, &hat, &r.HeatingError); err != nil {
			return nil, err
		}
		if at > 0 {
			r.LastAt = time.UnixMilli(at)
		}
		if hat > 0 {
			r.HeatingAt = time.UnixMilli(hat)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveReportSchedule inserts a schedule (ID 0) or replaces the JSON of an
// existing one, keeping when it was last sent; it returns the ID. A new
// schedule starts at lastPeriod, so it does not send the period that has
// just ended unless asked.
func (s *Store) SaveReportSchedule(ctx context.Context, id int64, data []byte, lastPeriod string) (int64, error) {
	if id == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO report_schedules (data, last_period) VALUES (?, ?)`, data, lastPeriod)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	// an update keeps the last sent period, unless lastPeriod says where
	// to start (the kind changed: its periods have other keys, and a
	// heating report that was still to follow is dropped)
	q, args := `UPDATE report_schedules SET data = ? WHERE id = ?`, []any{data, id}
	if lastPeriod != "" {
		q, args = `UPDATE report_schedules SET data = ?, last_period = ?, heating_period = '', heating_error = '' WHERE id = ?`, []any{data, lastPeriod, id}
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return id, nil
}

// SetReportScheduleSent records a send: the period when it succeeded, and
// the time and error (empty on success) of the attempt. A successful send
// of a whole period also settles a heating report that was still to follow
// for it.
func (s *Store) SetReportScheduleSent(ctx context.Context, id int64, period string, at time.Time, errText string) error {
	if errText != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE report_schedules SET last_at = ?, last_error = ? WHERE id = ?`, at.UnixMilli(), errText, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE report_schedules SET last_period = ?, last_at = ?, last_error = '',
		heating_period = CASE WHEN heating_period = ? THEN '' ELSE heating_period END,
		heating_error = CASE WHEN heating_period = ? THEN '' ELSE heating_error END
		WHERE id = ?`, period, at.UnixMilli(), period, period, id)
	return err
}

// SetReportScheduleSentWithoutHeating records a successful send of period
// without heating: its heating report follows later.
func (s *Store) SetReportScheduleSentWithoutHeating(ctx context.Context, id int64, period string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE report_schedules SET last_period = ?, last_at = ?, last_error = '',
		heating_period = ?, heating_at = 0, heating_error = '' WHERE id = ?`, period, at.UnixMilli(), period, id)
	return err
}

// SetReportHeatingSent records an attempt to send the heating report that
// followed: on success (errText empty) nothing follows any more.
func (s *Store) SetReportHeatingSent(ctx context.Context, id int64, at time.Time, errText string) error {
	if errText != "" {
		_, err := s.db.ExecContext(ctx, `UPDATE report_schedules SET heating_at = ?, heating_error = ? WHERE id = ?`, at.UnixMilli(), errText, id)
		return err
	}
	_, err := s.db.ExecContext(ctx, `UPDATE report_schedules SET heating_period = '', heating_at = ?, heating_error = '' WHERE id = ?`, at.UnixMilli(), id)
	return err
}

// DeleteReportSchedule removes a schedule.
func (s *Store) DeleteReportSchedule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM report_schedules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
