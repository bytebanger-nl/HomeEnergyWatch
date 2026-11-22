package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// /api/history/grid returns the per-phase series and the grid events.
func TestGridHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now()
	if err := st.SaveDevice(ctx, store.Device{ID: "p1", Serial: "aabbccddeeff", ProductType: "HWE-P1",
		Host: "127.0.0.1:1", APIVersion: 1, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := history.NewRecorder(st, log, history.Config{Location: time.UTC})
	mgr := gateway.NewManager(st, rec, gateway.NewHub(), log, gateway.Config{})
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	for i, sags := range []float64{2, 3} {
		rec.Add(ctx, "p1", homewizard.KindP1, homewizard.Reading{Time: t0.Add(time.Duration(i) * time.Second), Values: map[string]float64{
			"voltage_l1_v": 231, "current_l1_a": 4, "power_l1_w": 900, "voltage_sag_l1_count": sags}})
	}
	h := (&Server{Manager: mgr, Recorder: rec, Hub: gateway.NewHub(), Log: log, Web: fstest.MapFS{}}).Handler()
	w := do(h, "GET", "/api/history/grid?device=p1&from=2026-11-07T00:00:00Z&to=2026-11-08T00:00:00Z", "127.0.0.1:1", "", nil)
	if w.Code != 200 {
		t.Fatalf("grid: %d %s", w.Code, w.Body)
	}
	var res struct {
		Phases int
		Points []history.GridPoint
		Events []store.GridEvent
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Phases != 1 || len(res.Points) != 1 || *res.Points[0].Phases[0].VAvg != 231 {
		t.Fatalf("series %+v", res)
	}
	if len(res.Events) != 1 || res.Events[0].Kind != store.GridSag || res.Events[0].Phase != 1 {
		t.Fatalf("events %+v", res.Events)
	}
	if w := do(h, "GET", "/api/history/grid?device=nope", "127.0.0.1:1", "", nil); w.Code != 404 {
		t.Fatalf("unknown device: %d", w.Code)
	}
}
