package store

import (
	"context"
	"database/sql"
	"math"
	"time"
)

// Minute is the aggregate of all samples of one device in one minute.
// Rate is the primary live value: power in W, or flow in L/min for a
// watermeter.
// Counter fields hold the last value seen in that minute, observed at Last.
type Minute struct {
	DeviceID  string
	Minute    time.Time
	Samples   int
	RateAvg   *float64
	RateMin   *float64
	RateMax   *float64
	Last      time.Time
	ImportKWh *float64
	ExportKWh *float64
	GasM3     *float64
	WaterM3   *float64
	SocPct    *float64
	// ImportLowKWh and ExportLowKWh are the meter's low-tariff (T1)
	// counters; only set for a P1 meter that registers two tariffs.
	ImportLowKWh *float64
	ExportLowKWh *float64
}

// SaveMinute upserts one minute aggregate.
func (s *Store) SaveMinute(ctx context.Context, m Minute) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO minute_samples
		(device_id, minute, samples, rate_avg, rate_min, rate_max, last_ms,
		 import_kwh, export_kwh, gas_m3, water_m3, soc_pct, import_low_kwh, export_low_kwh)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(device_id, minute) DO UPDATE SET samples=excluded.samples,
			rate_avg=excluded.rate_avg, rate_min=excluded.rate_min,
			rate_max=excluded.rate_max, last_ms=excluded.last_ms,
			import_kwh=excluded.import_kwh, export_kwh=excluded.export_kwh,
			gas_m3=excluded.gas_m3, water_m3=excluded.water_m3, soc_pct=excluded.soc_pct,
			import_low_kwh=excluded.import_low_kwh, export_low_kwh=excluded.export_low_kwh`,
		m.DeviceID, m.Minute.Unix(), m.Samples, m.RateAvg, m.RateMin, m.RateMax,
		m.Last.UnixMilli(), m.ImportKWh, m.ExportKWh, m.GasM3, m.WaterM3, m.SocPct, m.ImportLowKWh, m.ExportLowKWh)
	return err
}

// Minutes returns minute aggregates with from <= minute < to, oldest first.
func (s *Store) Minutes(ctx context.Context, deviceID string, from, to time.Time) ([]Minute, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT minute, samples, rate_avg, rate_min,
		rate_max, last_ms, import_kwh, export_kwh, gas_m3, water_m3, soc_pct, import_low_kwh, export_low_kwh
		FROM minute_samples WHERE device_id = ? AND minute >= ? AND minute < ? ORDER BY minute`,
		deviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Minute
	for rows.Next() {
		var m Minute
		var minute, last int64
		var avg, mn, mx, imp, exp, gas, water, soc, impLow, expLow sql.NullFloat64
		if err := rows.Scan(&minute, &m.Samples, &avg, &mn, &mx, &last, &imp, &exp, &gas, &water, &soc, &impLow, &expLow); err != nil {
			return nil, err
		}
		m.DeviceID = deviceID
		m.Minute = time.Unix(minute, 0).UTC()
		m.Last = time.UnixMilli(last).UTC()
		m.RateAvg, m.RateMin, m.RateMax = ptr(avg), ptr(mn), ptr(mx)
		m.ImportKWh, m.ExportKWh, m.GasM3, m.WaterM3, m.SocPct = ptr(imp), ptr(exp), ptr(gas), ptr(water), ptr(soc)
		m.ImportLowKWh, m.ExportLowKWh = ptr(impLow), ptr(expLow)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Quarter is the energy of one device in one quarter hour.
type Quarter struct {
	DeviceID  string
	Start     time.Time
	ImportKWh *float64
	ExportKWh *float64
	GasM3     *float64
	WaterM3   *float64
	// ImportLowKWh and ExportLowKWh are the part of ImportKWh and ExportKWh
	// that the meter registered at its low tariff (T1, "dal"). nil when
	// unknown (old data, a meter without tariffs): costs then fall back to
	// the clock.
	ImportLowKWh *float64
	ExportLowKWh *float64
	// Quality is "complete", "partial" or "anomalous".
	Quality  string
	Coverage float64
	// Source is SourceMeasured or SourceImport.
	Source string
}

// Quarter sources.
const (
	SourceMeasured = "measured"
	SourceImport   = "import"
)

// SaveQuarters upserts quarter-hour records in one transaction.
func (s *Store) SaveQuarters(ctx context.Context, quarters []Quarter) error {
	if len(quarters) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	// Measured values replace what is stored; a counter without a measured
	// value keeps an earlier (e.g. imported) value. An imported quarter is
	// only replaced by a complete measurement: a partial one (e.g. the
	// gateway started halfway the quarter) or an anomalous one would
	// overwrite valid history with less.
	for _, q := range quarters {
		if _, err := tx.ExecContext(ctx, `INSERT INTO quarter_hours
			(device_id, start, import_kwh, export_kwh, gas_m3, water_m3, quality, coverage, updated_at, source,
			 import_low_kwh, export_low_kwh)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'measured', ?, ?)
			ON CONFLICT(device_id, start) DO UPDATE SET
				import_kwh=COALESCE(excluded.import_kwh, quarter_hours.import_kwh),
				export_kwh=COALESCE(excluded.export_kwh, quarter_hours.export_kwh),
				gas_m3=COALESCE(excluded.gas_m3, quarter_hours.gas_m3),
				water_m3=COALESCE(excluded.water_m3, quarter_hours.water_m3),
				import_low_kwh=COALESCE(excluded.import_low_kwh, quarter_hours.import_low_kwh),
				export_low_kwh=COALESCE(excluded.export_low_kwh, quarter_hours.export_low_kwh),
				quality=excluded.quality, coverage=excluded.coverage, updated_at=excluded.updated_at,
				source='measured'
			WHERE quarter_hours.source <> 'import' OR excluded.quality = 'complete'`,
			q.DeviceID, q.Start.Unix(), q.ImportKWh, q.ExportKWh, q.GasM3, q.WaterM3,
			q.Quality, q.Coverage, now, q.ImportLowKWh, q.ExportLowKWh); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ImportStats counts quarters by outcome of ImportQuarters.
type ImportStats struct {
	Added    int `json:"added"`    // quarters that got at least one new value
	Replaced int `json:"replaced"` // partly measured or anomalous quarters replaced by complete imported ones
	Skipped  int `json:"skipped"`  // quarters whose values were all present already (never overwritten)
}

// ImportQuarters stores imported quarters without changing what already
// exists: each counter is only filled where it is still empty. The one
// exception is a measured quarter that is not complete (partial or
// anomalous): a complete imported quarter replaces its counters. With dryRun
// nothing is written. Importing the same data twice adds nothing.
func (s *Store) ImportQuarters(ctx context.Context, quarters []Quarter, dryRun bool) (ImportStats, error) {
	var st ImportStats
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, q := range quarters {
		var imp, exp, gas, water, impLow, expLow sql.NullFloat64
		var quality, source string
		err := tx.QueryRowContext(ctx, `SELECT import_kwh, export_kwh, gas_m3, water_m3, import_low_kwh, export_low_kwh,
			quality, source FROM quarter_hours WHERE device_id = ? AND start = ?`, q.DeviceID, q.Start.Unix()).
			Scan(&imp, &exp, &gas, &water, &impLow, &expLow, &quality, &source)
		exists := err == nil
		if err != nil && err != sql.ErrNoRows {
			return st, err
		}
		// a quarter the gateway measured only partly (it was not running the
		// whole quarter) or with a counter going backwards is an estimate: a
		// complete imported quarter replaces it
		replace := exists && source == SourceMeasured && quality != "complete" && q.Quality == "complete"
		if !replace {
			// a low-tariff part only belongs next to the total it is part of
			if !sameTotal(imp, q.ImportKWh) {
				q.ImportLowKWh = nil
			}
			if !sameTotal(exp, q.ExportKWh) {
				q.ExportLowKWh = nil
			}
		}
		// the tariff split counts as a value: importing an export again after
		// an update fills in the meter's tariffs of older history
		existing := []sql.NullFloat64{imp, exp, gas, water, impLow, expLow}
		adds, provided := false, false
		for i, v := range []*float64{q.ImportKWh, q.ExportKWh, q.GasM3, q.WaterM3, q.ImportLowKWh, q.ExportLowKWh} {
			if v != nil {
				provided = true
				adds = adds || replace || !existing[i].Valid
			}
		}
		switch {
		case adds && replace:
			st.Replaced++
		case adds:
			st.Added++
		case provided:
			st.Skipped++
			continue
		}
		if dryRun {
			continue
		}
		switch {
		case !exists:
			_, err = tx.ExecContext(ctx, `INSERT INTO quarter_hours
				(device_id, start, import_kwh, export_kwh, gas_m3, water_m3, quality, coverage, updated_at, source,
				 import_low_kwh, export_low_kwh)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'import', ?, ?)`,
				q.DeviceID, q.Start.Unix(), q.ImportKWh, q.ExportKWh, q.GasM3, q.WaterM3, q.Quality, q.Coverage, now,
				q.ImportLowKWh, q.ExportLowKWh)
		case replace && adds:
			// counters the file does not hold (e.g. gas in an electricity
			// export) keep their measured value
			_, err = tx.ExecContext(ctx, `UPDATE quarter_hours SET
				import_kwh=COALESCE(?, import_kwh), export_kwh=COALESCE(?, export_kwh),
				gas_m3=COALESCE(?, gas_m3), water_m3=COALESCE(?, water_m3),
				import_low_kwh=COALESCE(?, import_low_kwh), export_low_kwh=COALESCE(?, export_low_kwh),
				quality=?, coverage=?, updated_at=?, source='import'
				WHERE device_id = ? AND start = ?`,
				q.ImportKWh, q.ExportKWh, q.GasM3, q.WaterM3, q.ImportLowKWh, q.ExportLowKWh,
				q.Quality, q.Coverage, now, q.DeviceID, q.Start.Unix())
		default:
			_, err = tx.ExecContext(ctx, `UPDATE quarter_hours SET
				import_kwh=COALESCE(import_kwh, ?), export_kwh=COALESCE(export_kwh, ?),
				gas_m3=COALESCE(gas_m3, ?), water_m3=COALESCE(water_m3, ?),
				import_low_kwh=COALESCE(import_low_kwh, ?), export_low_kwh=COALESCE(export_low_kwh, ?), updated_at=?
				WHERE device_id = ? AND start = ?`,
				q.ImportKWh, q.ExportKWh, q.GasM3, q.WaterM3, q.ImportLowKWh, q.ExportLowKWh, now, q.DeviceID, q.Start.Unix())
		}
		if err != nil {
			return st, err
		}
	}
	if dryRun {
		return st, nil
	}
	return st, tx.Commit()
}

// PartialQuarters returns the starts of the measured quarters of a device
// that are partial, with start >= from, oldest first.
func (s *Store) PartialQuarters(ctx context.Context, deviceID string, from time.Time) ([]time.Time, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start FROM quarter_hours
		WHERE device_id = ? AND start >= ? AND quality = 'partial' AND source = 'measured'
		ORDER BY start`, deviceID, from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []time.Time
	for rows.Next() {
		var start int64
		if err := rows.Scan(&start); err != nil {
			return nil, err
		}
		out = append(out, time.Unix(start, 0).UTC())
	}
	return out, rows.Err()
}

// MeasuredFrom returns when the history of a device starts: the first
// complete quarter hour (measured or imported), or else its first quarter
// hour at all. ok is false when the device has no quarter hours.
func (s *Store) MeasuredFrom(ctx context.Context, deviceID string) (from time.Time, ok bool, err error) {
	var start sql.NullInt64
	err = s.db.QueryRowContext(ctx, `SELECT coalesce(
		(SELECT min(start) FROM quarter_hours WHERE device_id = ? AND quality = 'complete'),
		(SELECT min(start) FROM quarter_hours WHERE device_id = ?))`, deviceID, deviceID).Scan(&start)
	if err != nil || !start.Valid {
		return time.Time{}, false, err
	}
	return time.Unix(start.Int64, 0).UTC(), true, nil
}

// Quarters returns quarter-hour records with from <= start < to, oldest first.
func (s *Store) Quarters(ctx context.Context, deviceID string, from, to time.Time) ([]Quarter, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start, import_kwh, export_kwh, gas_m3, water_m3,
		import_low_kwh, export_low_kwh, quality, coverage, source FROM quarter_hours WHERE device_id = ? AND start >= ? AND start < ?
		ORDER BY start`, deviceID, from.Unix(), to.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Quarter
	for rows.Next() {
		var q Quarter
		var start int64
		var imp, exp, gas, water, impLow, expLow sql.NullFloat64
		if err := rows.Scan(&start, &imp, &exp, &gas, &water, &impLow, &expLow, &q.Quality, &q.Coverage, &q.Source); err != nil {
			return nil, err
		}
		q.DeviceID = deviceID
		q.Start = time.Unix(start, 0).UTC()
		q.ImportKWh, q.ExportKWh, q.GasM3, q.WaterM3 = ptr(imp), ptr(exp), ptr(gas), ptr(water)
		q.ImportLowKWh, q.ExportLowKWh = ptr(impLow), ptr(expLow)
		out = append(out, q)
	}
	return out, rows.Err()
}

// PruneMinutes removes minute aggregates older than before.
func (s *Store) PruneMinutes(ctx context.Context, before time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, "DELETE FROM minute_samples WHERE minute < ?", before.Unix())
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	res, err = s.db.ExecContext(ctx, "DELETE FROM grid_minutes WHERE minute < ?", before.Unix())
	if err != nil {
		return n, err
	}
	g, err := res.RowsAffected()
	return n + g, err
}

func ptr(v sql.NullFloat64) *float64 {
	if !v.Valid {
		return nil
	}
	f := v.Float64
	return &f
}

// sameTotal reports whether a stored total is empty (the import fills it)
// or the same as the imported one v, up to rounding and interpolation.
func sameTotal(stored sql.NullFloat64, v *float64) bool {
	if !stored.Valid {
		return true
	}
	return v != nil && math.Abs(stored.Float64-*v) <= max(0.002, 0.02*math.Abs(*v))
}
