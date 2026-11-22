package store

import (
	"context"
	"database/sql"
	"errors"
)

// PluginTab is a tab of the user's own for plugin devices.
type PluginTab struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Position int    `json:"position"`
}

// PluginTabs returns the tabs in their order.
func (s *Store) PluginTabs(ctx context.Context) ([]PluginTab, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, name, position FROM plugin_tabs ORDER BY position, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []PluginTab{}
	for rows.Next() {
		var t PluginTab
		if err := rows.Scan(&t.ID, &t.Name, &t.Position); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddPluginTab adds a tab after the others.
func (s *Store) AddPluginTab(ctx context.Context, name string) (PluginTab, error) {
	var pos int
	if err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(position), -1) + 1 FROM plugin_tabs").Scan(&pos); err != nil {
		return PluginTab{}, err
	}
	res, err := s.db.ExecContext(ctx, "INSERT INTO plugin_tabs (name, position) VALUES (?, ?)", name, pos)
	if err != nil {
		return PluginTab{}, err
	}
	id, err := res.LastInsertId()
	return PluginTab{ID: id, Name: name, Position: pos}, err
}

// RenamePluginTab changes a tab's name.
func (s *Store) RenamePluginTab(ctx context.Context, id int64, name string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE plugin_tabs SET name = ? WHERE id = ?", name, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePluginTab removes a tab; its devices go back to the default tab.
func (s *Store) DeletePluginTab(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, "DELETE FROM plugin_tabs WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetPluginDeviceTab puts a plugin device on a tab (0 = the default tab).
func (s *Store) SetPluginDeviceTab(ctx context.Context, deviceID string, tabID int64) error {
	var tab sql.NullInt64
	if tabID != 0 {
		var one int
		err := s.db.QueryRowContext(ctx, "SELECT 1 FROM plugin_tabs WHERE id = ?", tabID).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		tab = sql.NullInt64{Int64: tabID, Valid: true}
	}
	res, err := s.db.ExecContext(ctx, "UPDATE plugin_devices SET tab_id = ? WHERE id = ?", tab, deviceID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
