package store

import (
	"context"
	"time"
)

// StoredRule is an alert rule as JSON (see package rules). Armed records
// whether the rule may fire: it is cleared when the rule fires and set again
// once its conditions are seen to be false, so a restart neither repeats an
// alert nor forgets one that is due. Active records whether a manual action
// is switched on; saving the rule keeps it.
type StoredRule struct {
	ID     int64
	Data   []byte
	Armed  bool
	Active bool
}

// Rules returns all rules in the order they were made.
func (s *Store) Rules(ctx context.Context) ([]StoredRule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, data, armed, active FROM rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StoredRule
	for rows.Next() {
		var r StoredRule
		if err := rows.Scan(&r.ID, &r.Data, &r.Armed, &r.Active); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SaveRule inserts a rule (ID 0) or replaces an existing one and returns its
// ID. A saved rule is not armed.
func (s *Store) SaveRule(ctx context.Context, r StoredRule) (int64, error) {
	now := time.Now().UnixMilli()
	if r.ID == 0 {
		res, err := s.db.ExecContext(ctx, `INSERT INTO rules (data, armed, updated_at) VALUES (?, 0, ?)`, r.Data, now)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	res, err := s.db.ExecContext(ctx, `UPDATE rules SET data = ?, armed = 0, updated_at = ? WHERE id = ?`, r.Data, now, r.ID)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, ErrNotFound
	}
	return r.ID, nil
}

// UpdateRuleData replaces the JSON of a rule and keeps whether it may fire:
// a small change (the target of regulating) that is not a new rule.
func (s *Store) UpdateRuleData(ctx context.Context, id int64, data []byte) error {
	res, err := s.db.ExecContext(ctx, `UPDATE rules SET data = ?, updated_at = ? WHERE id = ?`, data, time.Now().UnixMilli(), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetRuleState stores whether a rule may fire and whether a manual action
// is switched on.
func (s *Store) SetRuleState(ctx context.Context, id int64, armed, active bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rules SET armed = ?, active = ? WHERE id = ?`, armed, active, id)
	return err
}

// DeleteRule removes a rule and where it was in its actions.
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if err := s.DeleteRuleRun(ctx, id); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM rule_regulate_periods WHERE rule_id = ?`, id)
	return err
}

// RuleRuns returns where automations are in their wait and regulate
// actions (JSON, see package rules), by rule id.
func (s *Store) RuleRuns(ctx context.Context) (map[int64][]byte, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT rule_id, data FROM rule_runs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]byte{}
	for rows.Next() {
		var id int64
		var data []byte
		if err := rows.Scan(&id, &data); err != nil {
			return nil, err
		}
		out[id] = data
	}
	return out, rows.Err()
}

// SaveRuleRun stores where an automation is in its actions.
func (s *Store) SaveRuleRun(ctx context.Context, id int64, data []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO rule_runs (rule_id, data, updated_at) VALUES (?, ?, ?)
		ON CONFLICT (rule_id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		id, data, time.Now().UnixMilli())
	return err
}

// DeleteRuleRun forgets where an automation was (it ended or changed); a
// regulating period it was in ends now.
func (s *Store) DeleteRuleRun(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM rule_runs WHERE rule_id = ?`, id); err != nil {
		return err
	}
	return s.EndRegulatePeriod(ctx, id, time.Now())
}

// Period is a time an automation regulated; End is nil while it still does.
type Period struct {
	Start time.Time  `json:"start"`
	End   *time.Time `json:"end,omitempty"`
}

// StartRegulatePeriod records that automation id regulates since start,
// unless a period of it is already open.
func (s *Store) StartRegulatePeriod(ctx context.Context, id int64, start time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO rule_regulate_periods (rule_id, start_ms)
		SELECT ?, ? WHERE NOT EXISTS (SELECT 1 FROM rule_regulate_periods WHERE rule_id = ? AND end_ms IS NULL)`,
		id, start.UnixMilli(), id)
	return err
}

// EndRegulatePeriod ends the open period of automation id, if any.
func (s *Store) EndRegulatePeriod(ctx context.Context, id int64, end time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE rule_regulate_periods SET end_ms = max(start_ms, ?) WHERE rule_id = ? AND end_ms IS NULL`,
		end.UnixMilli(), id)
	return err
}

// RegulatePeriods returns the periods of automation id that overlap
// [from, to), oldest first.
func (s *Store) RegulatePeriods(ctx context.Context, id int64, from, to time.Time) ([]Period, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT start_ms, end_ms FROM rule_regulate_periods
		WHERE rule_id = ? AND start_ms < ? AND (end_ms IS NULL OR end_ms > ?) ORDER BY start_ms`,
		id, to.UnixMilli(), from.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Period{}
	for rows.Next() {
		var start int64
		var end *int64
		if err := rows.Scan(&start, &end); err != nil {
			return nil, err
		}
		p := Period{Start: time.UnixMilli(start)}
		if end != nil {
			t := time.UnixMilli(*end)
			p.End = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LastRegulated returns, per automation that ever regulated, when it last
// did (now for one that still does).
func (s *Store) LastRegulated(ctx context.Context) (map[int64]time.Time, error) {
	now := time.Now().UnixMilli()
	rows, err := s.db.QueryContext(ctx, `SELECT rule_id, max(coalesce(end_ms, ?)) FROM rule_regulate_periods GROUP BY rule_id`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id, ms int64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		out[id] = time.UnixMilli(ms)
	}
	return out, rows.Err()
}

// PushSubscription is a Web Push subscription of one browser (one install
// of the PWA). P256DH and Auth are base64url as the browser gives them.
type PushSubscription struct {
	Endpoint string    `json:"endpoint"`
	P256DH   string    `json:"-"`
	Auth     string    `json:"-"`
	Label    string    `json:"label"`
	Created  time.Time `json:"created"`
}

// PushSubscriptions returns all subscriptions, oldest first.
func (s *Store) PushSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT endpoint, p256dh, auth, label, created_at FROM push_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var p PushSubscription
		var created int64
		if err := rows.Scan(&p.Endpoint, &p.P256DH, &p.Auth, &p.Label, &created); err != nil {
			return nil, err
		}
		p.Created = time.UnixMilli(created)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SavePushSubscription adds a subscription or replaces the keys and label of
// the one with the same endpoint.
func (s *Store) SavePushSubscription(ctx context.Context, p PushSubscription) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO push_subscriptions (endpoint, p256dh, auth, label, created_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT (endpoint) DO UPDATE SET p256dh = excluded.p256dh, auth = excluded.auth, label = excluded.label`,
		p.Endpoint, p.P256DH, p.Auth, p.Label, time.Now().UnixMilli())
	return err
}

// DeletePushSubscription removes a subscription; a missing one is no error.
func (s *Store) DeletePushSubscription(ctx context.Context, endpoint string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM push_subscriptions WHERE endpoint = ?`, endpoint)
	return err
}
