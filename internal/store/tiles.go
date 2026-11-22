package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// DashboardTile is a tile of the user's own on the dashboard. Items is a
// JSON array whose shape the server checks; the store keeps it as is.
type DashboardTile struct {
	ID       int64           `json:"id"`
	Name     string          `json:"name"`
	Position int             `json:"position"`
	Size     string          `json:"size"`
	Style    string          `json:"style"` // the form: value, list, grid, big or spark
	Items    json.RawMessage `json:"items"`
}

// DashboardTiles returns the tiles in their order.
func (s *Store) DashboardTiles(ctx context.Context) ([]DashboardTile, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, position, size, style, items FROM dashboard_tiles ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DashboardTile{}
	for rows.Next() {
		var t DashboardTile
		var items string
		if err := rows.Scan(&t.ID, &t.Name, &t.Position, &t.Size, &t.Style, &items); err != nil {
			return nil, err
		}
		t.Items = json.RawMessage(items)
		out = append(out, t)
	}
	return out, rows.Err()
}

// DashboardTile returns one tile.
func (s *Store) DashboardTile(ctx context.Context, id int64) (DashboardTile, error) {
	t := DashboardTile{ID: id}
	var items string
	err := s.db.QueryRowContext(ctx, "SELECT name, position, size, style, items FROM dashboard_tiles WHERE id = ?", id).
		Scan(&t.Name, &t.Position, &t.Size, &t.Style, &items)
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	t.Items = json.RawMessage(items)
	return t, err
}

// AddDashboardTile adds a tile after the others and returns it with its id
// and position.
func (s *Store) AddDashboardTile(ctx context.Context, t DashboardTile) (DashboardTile, error) {
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), -1) + 1 FROM dashboard_tiles").Scan(&t.Position); err != nil {
		return t, err
	}
	if t.Style == "" {
		t.Style = "value"
	}
	res, err := s.db.ExecContext(ctx, "INSERT INTO dashboard_tiles (name, position, size, style, items) VALUES (?, ?, ?, ?, ?)",
		t.Name, t.Position, t.Size, t.Style, string(t.Items))
	if err != nil {
		return t, err
	}
	t.ID, err = res.LastInsertId()
	return t, err
}

// UpdateDashboardTile changes a tile's name, size, form and items (not its
// position).
func (s *Store) UpdateDashboardTile(ctx context.Context, t DashboardTile) error {
	if t.Style == "" {
		t.Style = "value"
	}
	res, err := s.db.ExecContext(ctx, "UPDATE dashboard_tiles SET name = ?, size = ?, style = ?, items = ? WHERE id = ?",
		t.Name, t.Size, t.Style, string(t.Items), t.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteDashboardTile removes a tile.
func (s *Store) DeleteDashboardTile(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM dashboard_tiles WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// OrderDashboardTiles puts the tiles in the order of ids. Tiles not in ids
// keep their place after the listed ones.
func (s *Store) OrderDashboardTiles(ctx context.Context, ids []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "UPDATE dashboard_tiles SET position = position + ?", len(ids)); err != nil {
		return err
	}
	for i, id := range ids {
		if _, err := tx.ExecContext(ctx, "UPDATE dashboard_tiles SET position = ? WHERE id = ?", i, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}
