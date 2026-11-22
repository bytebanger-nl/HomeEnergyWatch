package store

import (
	"context"
	"fmt"
	"time"
)

// ContractTable selects a timeline of contracts.
type ContractTable string

// Contract timelines: energy (electricity and gas) and water have their own
// suppliers and dates, so they never overlap each other.
const (
	EnergyContracts ContractTable = "contracts"
	WaterContracts  ContractTable = "water_contracts"
)

func (t ContractTable) valid() bool { return t == EnergyContracts || t == WaterContracts }

// StoredContract is an energy or water contract as kept in the store: its first day
// (YYYY-MM-DD) plus the contract itself as JSON (see package costs).
type StoredContract struct {
	ID    int64
	Start string
	Data  []byte
}

// Contracts returns all contracts ordered by start date.
func (s *Store) Contracts(ctx context.Context, table ContractTable) ([]StoredContract, error) {
	if !table.valid() {
		return nil, fmt.Errorf("unknown contract table %q", table)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, start_date, data FROM `+string(table)+` ORDER BY start_date`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredContract
	for rows.Next() {
		var c StoredContract
		if err := rows.Scan(&c.ID, &c.Start, &c.Data); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveContract inserts a contract (ID 0) or replaces an existing one and
// returns its ID.
func (s *Store) SaveContract(ctx context.Context, table ContractTable, c StoredContract) (int64, error) {
	if !table.valid() {
		return 0, fmt.Errorf("unknown contract table %q", table)
	}
	now := time.Now().UnixMilli()
	if c.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO `+string(table)+` (start_date, data, updated_at) VALUES (?, ?, ?)`, c.Start, c.Data, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE `+string(table)+` SET start_date = ?, data = ?, updated_at = ? WHERE id = ?`, c.Start, c.Data, now, c.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return c.ID, nil
}

// DeleteContract removes a contract.
func (s *Store) DeleteContract(ctx context.Context, table ContractTable, id int64) error {
	if !table.valid() {
		return fmt.Errorf("unknown contract table %q", table)
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM `+string(table)+` WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
