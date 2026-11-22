package store

import (
	"context"
	"database/sql"
	"time"
)

// Heating data from mijn.ista.nl (package ista). Days are "YYYY-MM-DD";
// readings are cumulative from the start of their settlement period (y).

// IstaAddress is one address (ista "consumption unit") of an account.
type IstaAddress struct {
	Cuid    string
	Account string // the plugin instance it was fetched with
	Address string
	Zip     string
	City    string
	MoveIn  string
}

// IstaPeriod is one settlement period of an address.
type IstaPeriod struct {
	Cuid  string
	Y     int
	Start string
	End   string
	TA    *float64 // average outdoor temperature
}

// IstaService is a billed service (heating) with its unit.
type IstaService struct {
	Cuid        string
	ServiceID   int
	Description string
	MeterType   string
	Unit        string
}

// IstaMeter is one heat cost allocator (radiator).
type IstaMeter struct {
	MeterID   int64
	Cuid      string
	ServiceID int
	RadNr     int
	Position  string
	MeterNr   int64
	ArtNr     int64
}

// IstaMeterPeriod is a radiator's factors in one settlement period.
type IstaMeterPeriod struct {
	MeterID      int64
	Y            int
	CalcFactor   float64
	Reduction    float64
	Multiply     float64
	TransferLoss float64
	BeginValue   float64
}

// IstaReading is a radiator's reading at the end of a day: ticks (C),
// ticks × factor (CC) and billed units (CCD), all since the period start.
type IstaReading struct {
	MeterID   int64
	Day       string
	Y         int
	C         float64
	CC        float64
	CCD       float64
	End       float64
	Estimated bool
}

// IstaDay is an address's total at the end of a day and the building
// average from the period start to that day.
type IstaDay struct {
	Cuid         string
	Day          string
	Y            int
	TotalNow     float64
	BuildingAvg  *float64
	Estimated    bool
	FetchedAt    time.Time
	AvgFetchedAt *time.Time
}

// IstaMonth is a month's consumption of one service.
type IstaMonth struct {
	Cuid        string
	ServiceID   int
	Year        int
	Month       int
	Total       float64
	BuildingAvg *float64
	Approx      bool
	AvgTemp     *float64
}

// IstaMeta is what UserValues and ConsumptionValues tell about an address.
type IstaMeta struct {
	Address      IstaAddress
	Periods      []IstaPeriod
	Services     []IstaService
	Meters       []IstaMeter
	MeterPeriods []IstaMeterPeriod
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SaveIstaMeta stores (replacing) the descriptive data of an address.
func (s *Store) SaveIstaMeta(ctx context.Context, m IstaMeta) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	a := m.Address
	if a.Cuid != "" {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_addresses (cuid, account, address, zip, city, move_in, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (cuid) DO UPDATE SET account = excluded.account, address = excluded.address, zip = excluded.zip,
				city = excluded.city, move_in = excluded.move_in, updated_at = excluded.updated_at`,
			a.Cuid, a.Account, a.Address, a.Zip, a.City, a.MoveIn, now); err != nil {
			return err
		}
	}
	for _, p := range m.Periods {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_periods (cuid, y, start_day, end_day, ta) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (cuid, y) DO UPDATE SET start_day = excluded.start_day, end_day = excluded.end_day, ta = excluded.ta`,
			p.Cuid, p.Y, p.Start, p.End, p.TA); err != nil {
			return err
		}
	}
	for _, v := range m.Services {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_services (cuid, service_id, description, meter_type, unit) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (cuid, service_id) DO UPDATE SET description = excluded.description, meter_type = excluded.meter_type, unit = excluded.unit`,
			v.Cuid, v.ServiceID, v.Description, v.MeterType, v.Unit); err != nil {
			return err
		}
	}
	for _, v := range m.Meters {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_meters (meter_id, cuid, service_id, rad_nr, position, meter_nr, art_nr, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (meter_id) DO UPDATE SET cuid = excluded.cuid, service_id = excluded.service_id, rad_nr = excluded.rad_nr,
				position = excluded.position, meter_nr = excluded.meter_nr, art_nr = excluded.art_nr, updated_at = excluded.updated_at`,
			v.MeterID, v.Cuid, v.ServiceID, v.RadNr, v.Position, v.MeterNr, v.ArtNr, now); err != nil {
			return err
		}
	}
	if err := saveMeterPeriods(ctx, tx, m.MeterPeriods); err != nil {
		return err
	}
	return tx.Commit()
}

func saveMeterPeriods(ctx context.Context, tx *sql.Tx, mps []IstaMeterPeriod) error {
	for _, v := range mps {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_meter_periods (meter_id, y, calc_factor, reduction, multiply, transfer_loss, begin_value)
			VALUES (?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (meter_id, y) DO UPDATE SET calc_factor = excluded.calc_factor, reduction = excluded.reduction,
				multiply = excluded.multiply, transfer_loss = excluded.transfer_loss, begin_value = excluded.begin_value`,
			v.MeterID, v.Y, v.CalcFactor, v.Reduction, v.Multiply, v.TransferLoss, v.BeginValue); err != nil {
			return err
		}
	}
	return nil
}

// SaveIstaDay stores the readings of one day (replacing earlier ones) with
// the factors they were computed with. The building average is kept unless
// d carries one.
func (s *Store) SaveIstaDay(ctx context.Context, d IstaDay, readings []IstaReading, mps []IstaMeterPeriod) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	var avgAt *int64
	if d.BuildingAvg != nil {
		avgAt = &now
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ista_days (cuid, day, y, total_now, building_avg, estimated, fetched_at, avg_fetched_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (cuid, day) DO UPDATE SET y = excluded.y, total_now = excluded.total_now,
			building_avg = COALESCE(excluded.building_avg, ista_days.building_avg),
			avg_fetched_at = COALESCE(excluded.avg_fetched_at, ista_days.avg_fetched_at),
			estimated = excluded.estimated, fetched_at = excluded.fetched_at`,
		d.Cuid, d.Day, d.Y, d.TotalNow, d.BuildingAvg, boolInt(d.Estimated), now, avgAt); err != nil {
		return err
	}
	for _, r := range readings {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_readings (meter_id, day, y, c_value, cc_value, ccd_value, end_value, estimated, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (meter_id, day) DO UPDATE SET y = excluded.y, c_value = excluded.c_value, cc_value = excluded.cc_value,
				ccd_value = excluded.ccd_value, end_value = excluded.end_value, estimated = excluded.estimated, fetched_at = excluded.fetched_at`,
			r.MeterID, r.Day, r.Y, r.C, r.CC, r.CCD, r.End, boolInt(r.Estimated), now); err != nil {
			return err
		}
	}
	if err := saveMeterPeriods(ctx, tx, mps); err != nil {
		return err
	}
	return tx.Commit()
}

// SetIstaBuildingAvg updates the building average of a stored day.
func (s *Store) SetIstaBuildingAvg(ctx context.Context, cuid, day string, v float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ista_days SET building_avg = ?, avg_fetched_at = ? WHERE cuid = ? AND day = ?`,
		v, time.Now().UnixMilli(), cuid, day)
	return err
}

// IstaAddresses returns every stored address.
func (s *Store) IstaAddresses(ctx context.Context) ([]IstaAddress, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cuid, account, address, zip, city, move_in FROM ista_addresses ORDER BY address, cuid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaAddress
	for rows.Next() {
		var a IstaAddress
		if err := rows.Scan(&a.Cuid, &a.Account, &a.Address, &a.Zip, &a.City, &a.MoveIn); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// IstaPeriods returns an address's settlement periods, oldest first.
func (s *Store) IstaPeriods(ctx context.Context, cuid string) ([]IstaPeriod, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cuid, y, start_day, end_day, ta FROM ista_periods WHERE cuid = ? ORDER BY y`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaPeriod
	for rows.Next() {
		var p IstaPeriod
		if err := rows.Scan(&p.Cuid, &p.Y, &p.Start, &p.End, &p.TA); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// IstaServices returns an address's services.
func (s *Store) IstaServices(ctx context.Context, cuid string) ([]IstaService, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cuid, service_id, description, meter_type, unit FROM ista_services WHERE cuid = ? ORDER BY service_id`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaService
	for rows.Next() {
		var v IstaService
		if err := rows.Scan(&v.Cuid, &v.ServiceID, &v.Description, &v.MeterType, &v.Unit); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// IstaMeters returns an address's radiators, highest radiator number first
// (the order of the portal and the settlement).
func (s *Store) IstaMeters(ctx context.Context, cuid string) ([]IstaMeter, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT meter_id, cuid, service_id, rad_nr, position, meter_nr, art_nr
		FROM ista_meters WHERE cuid = ? ORDER BY rad_nr DESC, meter_id`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaMeter
	for rows.Next() {
		var v IstaMeter
		if err := rows.Scan(&v.MeterID, &v.Cuid, &v.ServiceID, &v.RadNr, &v.Position, &v.MeterNr, &v.ArtNr); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// IstaMeterPeriods returns the factors of an address's radiators.
func (s *Store) IstaMeterPeriods(ctx context.Context, cuid string) ([]IstaMeterPeriod, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT mp.meter_id, mp.y, mp.calc_factor, mp.reduction, mp.multiply, mp.transfer_loss, mp.begin_value
		FROM ista_meter_periods mp JOIN ista_meters m ON m.meter_id = mp.meter_id WHERE m.cuid = ? ORDER BY mp.y, mp.meter_id`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaMeterPeriod
	for rows.Next() {
		var v IstaMeterPeriod
		if err := rows.Scan(&v.MeterID, &v.Y, &v.CalcFactor, &v.Reduction, &v.Multiply, &v.TransferLoss, &v.BeginValue); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// IstaDays returns an address's stored days from from to to (inclusive),
// oldest first.
func (s *Store) IstaDays(ctx context.Context, cuid, from, to string) ([]IstaDay, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cuid, day, y, total_now, building_avg, estimated, fetched_at, avg_fetched_at
		FROM ista_days WHERE cuid = ? AND day >= ? AND day <= ? ORDER BY day`, cuid, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaDay
	for rows.Next() {
		var d IstaDay
		var est int
		var at int64
		var avgAt sql.NullInt64
		if err := rows.Scan(&d.Cuid, &d.Day, &d.Y, &d.TotalNow, &d.BuildingAvg, &est, &at, &avgAt); err != nil {
			return nil, err
		}
		d.Estimated = est != 0
		d.FetchedAt = time.UnixMilli(at)
		if avgAt.Valid {
			t := time.UnixMilli(avgAt.Int64)
			d.AvgFetchedAt = &t
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// IstaReadings returns the readings of an address's radiators from from to
// to (inclusive), by day then radiator.
func (s *Store) IstaReadings(ctx context.Context, cuid, from, to string) ([]IstaReading, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT r.meter_id, r.day, r.y, r.c_value, r.cc_value, r.ccd_value, r.end_value, r.estimated
		FROM ista_readings r JOIN ista_meters m ON m.meter_id = r.meter_id
		WHERE m.cuid = ? AND r.day >= ? AND r.day <= ? ORDER BY r.day, r.meter_id`, cuid, from, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaReading
	for rows.Next() {
		var r IstaReading
		var est int
		if err := rows.Scan(&r.MeterID, &r.Day, &r.Y, &r.C, &r.CC, &r.CCD, &r.End, &est); err != nil {
			return nil, err
		}
		r.Estimated = est != 0
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveIstaMonths stores (replacing) months.
func (s *Store) SaveIstaMonths(ctx context.Context, months []IstaMonth) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, m := range months {
		if _, err := tx.ExecContext(ctx, `INSERT INTO ista_months (cuid, service_id, year, month, total, building_avg, approx, avg_temp, fetched_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (cuid, service_id, year, month) DO UPDATE SET total = excluded.total, building_avg = excluded.building_avg,
				approx = excluded.approx, avg_temp = excluded.avg_temp, fetched_at = excluded.fetched_at`,
			m.Cuid, m.ServiceID, m.Year, m.Month, m.Total, m.BuildingAvg, boolInt(m.Approx), m.AvgTemp, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// IstaMonths returns an address's months, oldest first.
func (s *Store) IstaMonths(ctx context.Context, cuid string) ([]IstaMonth, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cuid, service_id, year, month, total, building_avg, approx, avg_temp
		FROM ista_months WHERE cuid = ? ORDER BY year, month, service_id`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaMonth
	for rows.Next() {
		var m IstaMonth
		var approx int
		if err := rows.Scan(&m.Cuid, &m.ServiceID, &m.Year, &m.Month, &m.Total, &m.BuildingAvg, &approx, &m.AvgTemp); err != nil {
			return nil, err
		}
		m.Approx = approx != 0
		out = append(out, m)
	}
	return out, rows.Err()
}

// SaveIstaRaw keeps the latest raw answer of a call.
func (s *Store) SaveIstaRaw(ctx context.Context, endpoint, key string, body []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ista_raw (endpoint, key, fetched_at, body) VALUES (?, ?, ?, ?)
		ON CONFLICT (endpoint, key) DO UPDATE SET fetched_at = excluded.fetched_at, body = excluded.body`,
		endpoint, key, time.Now().UnixMilli(), string(body))
	return err
}

// IstaSyncState returns the stored sync state of an account ("" if none).
func (s *Store) IstaSyncState(ctx context.Context, account string) (string, error) {
	var data string
	err := s.db.QueryRowContext(ctx, `SELECT data FROM ista_sync WHERE account = ?`, account).Scan(&data)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return data, err
}

// SaveIstaSyncState stores the sync state of an account.
func (s *Store) SaveIstaSyncState(ctx context.Context, account, data string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ista_sync (account, data) VALUES (?, ?)
		ON CONFLICT (account) DO UPDATE SET data = excluded.data`, account, data)
	return err
}

// IstaContract is the stored contract of one settlement period (JSON, see
// package ista).
type IstaContract struct {
	Cuid string
	Y    int
	Data string
}

// IstaContracts returns an address's contracts, oldest first.
func (s *Store) IstaContracts(ctx context.Context, cuid string) ([]IstaContract, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT cuid, y, data FROM ista_contracts WHERE cuid = ? ORDER BY y`, cuid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IstaContract
	for rows.Next() {
		var c IstaContract
		if err := rows.Scan(&c.Cuid, &c.Y, &c.Data); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// SaveIstaContract stores (replacing) the contract of a period.
func (s *Store) SaveIstaContract(ctx context.Context, c IstaContract) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO ista_contracts (cuid, y, data, updated_at) VALUES (?, ?, ?, ?)
		ON CONFLICT (cuid, y) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		c.Cuid, c.Y, c.Data, time.Now().UnixMilli())
	return err
}

// DeleteIstaContract removes the contract of a period.
func (s *Store) DeleteIstaContract(ctx context.Context, cuid string, y int) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM ista_contracts WHERE cuid = ? AND y = ?`, cuid, y)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
