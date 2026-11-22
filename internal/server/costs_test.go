package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// scope=usage prices a socket's import and refuses the P1 meter (whose
// costs would then lack fixed costs and export) and other devices.
func TestCostsUsageScope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	for i, d := range []store.Device{
		{ID: "p1", ProductType: homewizard.ProductP1},
		{ID: "skt", ProductType: homewizard.ProductSocket},
		{ID: "wtr", ProductType: homewizard.ProductWater},
	} {
		d.Serial, d.Host, d.APIVersion, d.Enabled, d.CreatedAt, d.UpdatedAt = "aabbccddee0"+itoa(int64(i)), "127.0.0.1:1", 1, true, now, now
		if err := st.SaveDevice(ctx, d); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := history.NewRecorder(st, log, history.Config{Location: time.UTC})
	mgr := gateway.NewManager(st, rec, gateway.NewHub(), log, gateway.Config{})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	contracts := &costs.Contracts{Store: st}
	if _, err := contracts.Save(ctx, costs.Contract{Name: "Vast", Start: "2026-01-01", Type: costs.TypeFixed,
		Electricity: costs.Electricity{RateCt: 30, TaxCt: 10}, Monthly: []costs.MonthlyCost{{Name: "Vast", EUR: 30}}}); err != nil {
		t.Fatal(err)
	}
	two := 2.0
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	if err := st.SaveQuarters(ctx, []store.Quarter{{DeviceID: "skt", Start: day.Add(12 * time.Hour), ImportKWh: &two, Quality: history.QualityComplete, Coverage: 1}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{Manager: mgr, Recorder: rec, Hub: gateway.NewHub(), Log: log, Web: fstest.MapFS{},
		Contracts: contracts, Costs: &costs.Calculator{Store: st, Contracts: contracts, Loc: time.UTC}}
	h := s.Handler()
	period := "&from=2026-09-01T00:00:00Z&to=2026-09-02T00:00:00Z"

	w := do(h, "GET", "/api/costs?device=skt&scope=usage"+period, "127.0.0.1:1", "", nil)
	if w.Code != http.StatusOK {
		t.Fatalf("socket: %d %s", w.Code, w.Body)
	}
	var res struct{ Result costs.Result }
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if b := res.Result.Total; b.ImportKWh != 2 || b.FixedEUR != 0 || b.TotalEUR != 0.8 {
		t.Fatalf("socket usage %+v, want 2 kWh at 0.40 without fixed costs", b)
	}
	for _, dev := range []string{"p1", "wtr", ""} {
		if w := do(h, "GET", "/api/costs?scope=usage&device="+dev+period, "127.0.0.1:1", "", nil); w.Code != http.StatusBadRequest {
			t.Errorf("usage of %q: %d %s, want 400", dev, w.Code, w.Body)
		}
	}
	if w := do(h, "GET", "/api/costs?scope=usage&device=nope"+period, "127.0.0.1:1", "", nil); w.Code != http.StatusNotFound {
		t.Errorf("unknown device: %d, want 404", w.Code)
	}

	// gaps: the socket's history starts with its quarter at 12:00, so only
	// the 47 quarters after it are missing, not the 48 before it
	w = do(h, "GET", "/api/costs/gaps?device=skt&scope=usage"+period, "127.0.0.1:1", "", nil)
	var gaps struct{ Result costs.Gaps }
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &gaps) != nil {
		t.Fatalf("gaps: %d %s", w.Code, w.Body)
	}
	if g := gaps.Result; g.Missing.Quarters != 47 || len(g.Gaps) != 1 || g.Gaps[0].Count != 47 ||
		g.MeasuredFrom == nil || !g.MeasuredFrom.Equal(time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("gaps %+v", g)
	}
	if w := do(h, "GET", "/api/costs/gaps?scope=usage&device=p1"+period, "127.0.0.1:1", "", nil); w.Code != http.StatusBadRequest {
		t.Errorf("gaps usage of the P1 meter: %d, want 400", w.Code)
	}

	// advance: none on the fixed contract, one status per part on a split one
	type advances struct {
		Advance  *costs.AdvanceStatus
		Advances []*costs.AdvanceStatus
	}
	get := func() advances {
		t.Helper()
		w := do(h, "GET", "/api/advance?device=p1", "127.0.0.1:1", "", nil)
		var a advances
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &a) != nil {
			t.Fatalf("advance: %d %s", w.Code, w.Body)
		}
		return a
	}
	if a := get(); a.Advance != nil || a.Advances == nil || len(a.Advances) != 0 {
		t.Fatalf("without advance: %+v", a)
	}
	start := now.AddDate(0, -1, 0).Format("2006-01-02")
	if _, err := contracts.Save(ctx, costs.Contract{Name: "Tibber / Vattenfall", Start: start, Type: costs.TypeDynamic, Split: true, GasType: costs.TypeFixed,
		Advance:    &costs.Advance{Amounts: []costs.AdvanceAmount{{From: start, EUR: 80}}},
		GasAdvance: &costs.Advance{Amounts: []costs.AdvanceAmount{{From: start, EUR: 60}}}}); err != nil {
		t.Fatal(err)
	}
	a := get()
	if len(a.Advances) != 2 || a.Advances[0].Part != costs.PartElectricity || a.Advances[1].Part != costs.PartGas ||
		a.Advance == nil || a.Advance.Part != costs.PartElectricity || a.Advances[1].MonthlyEUR != 60 {
		t.Fatalf("split: %+v", a)
	}

	// the forecast settings: saved, refused when out of range, and with the
	// use of the running forecast (none here, so its reason)
	s.Forecast = &forecast.Service{Store: st}
	s.Costs.Forecast = s.Forecast // as in main: one service for all forecasts
	w = do(h, "PUT", "/api/settings/forecast", "127.0.0.1:1", `{"period_min_pct": 85, "trend": false}`, nil)
	var fs struct {
		Settings forecast.Settings
		Defaults forecast.Values
		Used     map[string]struct {
			Percentages []forecast.Used
			Error       string
		}
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &fs) != nil {
		t.Fatalf("save: %d %s", w.Code, w.Body)
	}
	if fs.Settings.PeriodMinPct == nil || *fs.Settings.PeriodMinPct != 85 || fs.Settings.TrendOn() || fs.Defaults.PeriodMinPct != 90 {
		t.Fatalf("settings %+v", fs)
	}
	if u := fs.Used["electricity"]; u.Error == "" || !strings.Contains(u.Error, "85%") {
		t.Fatalf("use %+v", fs.Used)
	}
	if w := do(h, "PUT", "/api/settings/forecast", "127.0.0.1:1", `{"price_pct": 150}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("150%% accepted: %d", w.Code)
	}
}
