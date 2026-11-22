package ista

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
	"time"
)

func newSim(t *testing.T) (*Sim, *Client) {
	t.Helper()
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	sim := &Sim{Username: "bewoner@example.org", Password: "geheim", MoveIn: time.Date(2024, 11, 11, 0, 0, 0, 0, time.UTC),
		Now: func() time.Time { return now }, Lag: 4}
	srv := httptest.NewServer(sim.Handler())
	t.Cleanup(srv.Close)
	return sim, &Client{BaseURL: srv.URL, Username: sim.Username, Password: sim.Password, ShardDelay: time.Millisecond, MonthBudget: time.Second}
}

func TestLoginAndUserValues(t *testing.T) {
	sim, c := newSim(t)
	uv, raw, err := c.UserValues(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 || len(uv.Cus) != 1 || uv.Cus[0].Cuid != SimCuid {
		t.Fatalf("user values: %+v", uv)
	}
	cc := uv.Cus[0].CurConsumption
	if len(cc.BillingPeriods) != 3 || cc.BillingPeriods[0].Y != 2027 {
		t.Fatalf("periods: %+v", cc.BillingPeriods)
	}
	if len(cc.ServicesComp) != 1 || len(cc.ServicesComp[0].CurMeters) != 4 {
		t.Fatalf("meters: %+v", cc.ServicesComp)
	}
	if sim.Logins != 1 {
		t.Fatalf("logins %d", sim.Logins)
	}
}

func TestWrongPassword(t *testing.T) {
	_, c := newSim(t)
	c.Password = "fout"
	err := c.Login(context.Background())
	if !errors.Is(err, ErrAuth) {
		t.Fatalf("want ErrAuth, got %v", err)
	}
}

func TestReloginAfterExpiry(t *testing.T) {
	sim, c := newSim(t)
	ctx := context.Background()
	if _, _, err := c.UserValues(ctx); err != nil {
		t.Fatal(err)
	}
	sim.Expire()
	if _, _, err := c.UserValues(ctx); err != nil {
		t.Fatal(err)
	}
	if sim.Logins != 2 {
		t.Fatalf("logins %d, want 2", sim.Logins)
	}
}

func TestConsumptionPerDay(t *testing.T) {
	_, c := newSim(t)
	ctx := context.Background()
	uv, _, err := c.UserValues(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := uv.Cus[0].CurConsumption.BillingPeriods[1] // 2025/26
	day := func(s string) time.Time { d, _ := time.Parse(DayLayout, s); return d }
	a, _, err := c.Consumption(ctx, SimCuid, p, day("2026-01-10"))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := c.Consumption(ctx, SimCuid, p, day("2026-01-11"))
	if err != nil {
		t.Fatal(err)
	}
	if a.CurEnd != "10-01-2026" || b.CurEnd != "11-01-2026" {
		t.Fatalf("ends %s %s", a.CurEnd, b.CurEnd)
	}
	ta, tb := a.ServicesComp[0].TotalNow, b.ServicesComp[0].TotalNow
	if !(tb > ta && ta > 0) {
		t.Fatalf("totals %v %v", ta, tb)
	}
	avg, _, err := c.Averages(ctx, SimCuid, day("2025-07-01"), day("2026-01-11"))
	if err != nil || len(avg.Averages) != 1 || avg.Averages[0].NormalizedValue <= 0 {
		t.Fatalf("averages %+v %v", avg, err)
	}
}

func TestMonthValuesShards(t *testing.T) {
	_, c := newSim(t)
	m, _, complete, err := c.MonthValues(context.Background(), SimCuid)
	if err != nil {
		t.Fatal(err)
	}
	if !complete || len(m.Months) != m.Shards || m.Shards != 25 {
		t.Fatalf("complete %v, %d months of %d", complete, len(m.Months), m.Shards)
	}
}

func TestParseDate(t *testing.T) {
	for _, s := range []string{"2025-07-01T00:00:00", "01-07-2025", "2025-07-01"} {
		d, err := ParseDate(s)
		if err != nil || d.Format(DayLayout) != "2025-07-01" {
			t.Fatalf("%s: %v %v", s, d, err)
		}
	}
}
