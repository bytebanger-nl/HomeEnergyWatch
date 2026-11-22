package store

import (
	"context"
	"time"
)

// MarketPrice is the price of one delivery period. The unit of Value follows
// the market (EUR/MWh for electricity, EUR/m³ for gas).
type MarketPrice struct {
	Start    time.Time
	Duration time.Duration
	Value    float64
	Source   string
}

// SavePrices upserts prices of a market. A finer resolution replaces
// overlapping coarser periods, never the other way round; a period with the
// same start and resolution is updated.
func (s *Store) SavePrices(ctx context.Context, market string, prices []MarketPrice) error {
	if len(prices) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	for _, p := range prices {
		end := p.Start.Add(p.Duration)
		// overlapping periods start less than maxPeriod before this one; the
		// bound lets SQLite seek on the primary key instead of scanning
		lo := p.Start.Add(-maxPeriod).Unix()
		// never let a coarser period (e.g. hourly) replace finer ones
		var finer int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM prices WHERE market = ? AND start > ? AND start < ? AND start + duration_s > ? AND duration_s < ?`,
			market, lo, end.Unix(), p.Start.Unix(), int64(p.Duration.Seconds())).Scan(&finer); err != nil {
			return err
		}
		if finer > 0 {
			continue
		}
		// remove coarser periods that overlap this one (hourly -> quarter hour)
		if _, err := tx.ExecContext(ctx, `DELETE FROM prices WHERE market = ? AND start > ? AND start < ? AND start + duration_s > ? AND duration_s > ?`,
			market, lo, end.Unix(), p.Start.Unix(), int64(p.Duration.Seconds())); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO prices (market, start, duration_s, price, source, updated_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT(market, start) DO UPDATE SET duration_s=excluded.duration_s, price=excluded.price,
				source=excluded.source, updated_at=excluded.updated_at`,
			market, p.Start.Unix(), int64(p.Duration.Seconds()), p.Value, p.Source, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// maxPeriod is longer than any delivery period (a gas day lasts up to 25 h).
const maxPeriod = 48 * time.Hour

// Prices returns prices of periods that overlap [from, to), oldest first.
func (s *Store) Prices(ctx context.Context, market string, from, to time.Time) ([]MarketPrice, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start, duration_s, price, source FROM prices
		WHERE market = ? AND start > ? AND start < ? AND start + duration_s > ? ORDER BY start`,
		market, from.Add(-maxPeriod).Unix(), to.Unix(), from.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MarketPrice
	for rows.Next() {
		var start, dur int64
		var p MarketPrice
		if err := rows.Scan(&start, &dur, &p.Value, &p.Source); err != nil {
			return nil, err
		}
		p.Start = time.Unix(start, 0).UTC()
		p.Duration = time.Duration(dur) * time.Second
		out = append(out, p)
	}
	return out, rows.Err()
}
