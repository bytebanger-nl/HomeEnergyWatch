package store

import (
	"context"
	"time"
)

// StoredScenario is a what-if contract as JSON (see package costs).
type StoredScenario struct {
	ID   int64
	Data []byte
}

// Scenarios returns all scenarios in the order they were made.
func (s *Store) Scenarios(ctx context.Context) ([]StoredScenario, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data FROM scenarios ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredScenario
	for rows.Next() {
		var sc StoredScenario
		if err := rows.Scan(&sc.ID, &sc.Data); err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, rows.Err()
}

// SaveScenario inserts a scenario (ID 0) or replaces an existing one and
// returns its ID.
func (s *Store) SaveScenario(ctx context.Context, sc StoredScenario) (int64, error) {
	now := time.Now().UnixMilli()
	if sc.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO scenarios (data, updated_at) VALUES (?, ?)`, sc.Data, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE scenarios SET data = ?, updated_at = ? WHERE id = ?`, sc.Data, now, sc.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return sc.ID, nil
}

// DeleteScenario removes a scenario.
func (s *Store) DeleteScenario(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM scenarios WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
