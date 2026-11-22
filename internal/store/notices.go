package store

import (
	"context"
	"time"
)

// Notice is a message for the user about something that went wrong. While
// unread, the same kind and subject happening again updates it (Last, Text,
// Count) instead of adding another.
type Notice struct {
	ID      int64     `json:"id"`
	Kind    string    `json:"kind"`
	Subject string    `json:"subject,omitempty"`
	Title   string    `json:"title"`
	Text    string    `json:"text,omitempty"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
	Count   int       `json:"count"`
	Read    bool      `json:"read"`
}

// AddNotice records a notice at at, or counts it on the unread one of the
// same kind and subject.
func (s *Store) AddNotice(ctx context.Context, kind, subject, title, text string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `UPDATE notices SET title = ?, text = ?, last_ms = max(last_ms, ?), count = count + 1
		WHERE kind = ? AND subject = ? AND ack = 0`, title, text, at.UnixMilli(), kind, subject)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.ExecContext(ctx, `INSERT INTO notices (kind, subject, title, text, first_ms, last_ms)
			VALUES (?, ?, ?, ?, ?, ?)`, kind, subject, title, text, at.UnixMilli(), at.UnixMilli()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Notices returns the notices last seen at or after from, newest first.
func (s *Store) Notices(ctx context.Context, from time.Time) ([]Notice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, kind, subject, title, text, first_ms, last_ms, count, ack
		FROM notices WHERE last_ms >= ? ORDER BY last_ms DESC`, from.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notice{}
	for rows.Next() {
		var n Notice
		var first, last int64
		if err := rows.Scan(&n.ID, &n.Kind, &n.Subject, &n.Title, &n.Text, &first, &last, &n.Count, &n.Read); err != nil {
			return nil, err
		}
		n.First, n.Last = time.UnixMilli(first), time.UnixMilli(last)
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadNotices returns how many notices are unread.
func (s *Store) UnreadNotices(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM notices WHERE ack = 0`).Scan(&n)
	return n, err
}

// ReadNotices marks the notices last seen at or before until as read and
// returns how many changed; one that happened again after it stays unread.
func (s *Store) ReadNotices(ctx context.Context, until time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE notices SET ack = 1 WHERE ack = 0 AND last_ms <= ?`, until.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
