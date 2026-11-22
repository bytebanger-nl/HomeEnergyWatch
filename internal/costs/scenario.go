package costs

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Scenario is a what-if contract: the costs of the measured usage if this
// contract had applied instead. It is stored apart from the contracts and
// never counts in the costs or the advance.
type Scenario struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Contract Contract `json:"contract"` // tariffs; dates, advance and chain are ignored
	// Rules2027 applies the rules from 2027 to earlier periods too: no net
	// metering, feed-in at the bare market price (not below 0).
	Rules2027 bool `json:"rules_2027"`
}

// Normalize trims the scenario and strips what does not apply to it.
func (s *Scenario) Normalize() {
	s.Name = strings.TrimSpace(s.Name)
	c := &s.Contract
	c.ID, c.Name, c.Start, c.End = 0, s.Name, "", ""
	c.Advance, c.GasAdvance, c.Estimate, c.Continues = nil, nil, nil, false
	c.Normalize()
}

// Validate checks the scenario's tariffs like a contract's.
func (s *Scenario) Validate() error {
	c := s.Contract
	c.Start = "2000-01-01" // a scenario has no dates
	return c.Validate()
}

// timeline is the scenario over the periods the real contracts cover, so
// that both are compared over exactly the same days; with no contracts at
// all it covers everything.
func (s *Scenario) timeline(real Timeline) Timeline {
	if len(real) == 0 {
		c := s.Contract
		c.Start = "0000-01-01"
		return Timeline{c}
	}
	out := make(Timeline, 0, len(real))
	for i, r := range real {
		c := s.Contract
		c.Start, c.End = r.Start, r.End
		if c.End == "" && i+1 < len(real) {
			c.End = prevDay(real[i+1].Start)
		}
		out = append(out, c)
	}
	return out
}

func prevDay(date string) string {
	d, err := time.Parse(dateLayout, date)
	if err != nil {
		return ""
	}
	return d.AddDate(0, 0, -1).Format(dateLayout)
}

// Comparison is the actual costs next to those of each scenario.
type Comparison struct {
	Actual    *Result          `json:"actual"`
	Scenarios []ScenarioResult `json:"scenarios"`
}

// ScenarioResult is one scenario's costs over the same period.
type ScenarioResult struct {
	Scenario Scenario `json:"scenario"`
	Result   *Result  `json:"result"`
}

// Compare returns the actual costs of a device in [from, to) and those of
// each scenario, in buckets. Scenarios use the same quarter hours and cost
// rules, over the days the real contracts cover; net metering runs per
// calendar year from 1 January as in the actual costs.
func (c *Calculator) Compare(ctx context.Context, deviceID string, scenarios []Scenario, from, to time.Time, bucket string) (*Comparison, error) {
	actual, err := c.Calculate(ctx, deviceID, from, to, bucket)
	if err != nil {
		return nil, err
	}
	real, err := c.Contracts.List(ctx)
	if err != nil {
		return nil, err
	}
	now := c.now()
	out := &Comparison{Actual: actual, Scenarios: []ScenarioResult{}}
	for _, sc := range scenarios {
		tl := sc.timeline(real)
		rulesYear := 0
		if sc.Rules2027 {
			rulesYear = NettingEndsYear
		}
		res, err := c.aggregate(from, to, bucket, now, func(ys, end time.Time) ([]*dayEntry, error) {
			return c.computeWith(ctx, deviceID, tl, ys, yearEnd(ys, end), 0, 0, computeOpts{rulesYear: rulesYear})
		})
		if err != nil {
			return nil, err
		}
		out.Scenarios = append(out.Scenarios, ScenarioResult{Scenario: sc, Result: res})
	}
	return out, nil
}

// Scenarios reads and writes scenarios in the store.
type Scenarios struct {
	Store *store.Store
}

// List returns all scenarios.
func (r *Scenarios) List(ctx context.Context) ([]Scenario, error) {
	rows, err := r.Store.Scenarios(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Scenario, 0, len(rows))
	for _, row := range rows {
		var s Scenario
		if err := json.Unmarshal(row.Data, &s); err != nil {
			return nil, fmt.Errorf("scenario %d: %w", row.ID, err)
		}
		s.ID = row.ID
		out = append(out, s)
	}
	return out, nil
}

// Save validates and stores a scenario (ID 0 creates one) and returns it.
func (r *Scenarios) Save(ctx context.Context, s Scenario) (Scenario, error) {
	s.Normalize()
	if s.Name == "" {
		return s, fmt.Errorf("%w: naam ontbreekt", ErrInvalid)
	}
	if err := s.Validate(); err != nil {
		return s, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return s, err
	}
	id, err := r.Store.SaveScenario(ctx, store.StoredScenario{ID: s.ID, Data: data})
	if err != nil {
		return s, err
	}
	s.ID = id
	return s, nil
}

// Delete removes a scenario.
func (r *Scenarios) Delete(ctx context.Context, id int64) error {
	return r.Store.DeleteScenario(ctx, id)
}
