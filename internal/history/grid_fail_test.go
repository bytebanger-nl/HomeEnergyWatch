package history

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// A grid event whose write fails must be found again by the next reading
// (regression: the baseline moved before the write, so the event was lost).
func TestGridEventSurvivesFailedWrite(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "t.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	raw, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)") // driver registered by store
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	exec := func(q string) {
		t.Helper()
		if _, err := raw.Exec(q); err != nil {
			t.Fatal(err)
		}
	}

	rec := newTestRecorder(st)
	t0 := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	fails := func(n float64) map[string]float64 {
		return map[string]float64{"voltage_l1_v": 230, "voltage_sag_l1_count": 0, "any_power_fail_count": n}
	}
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0, fails(5)))
	exec(`CREATE TRIGGER fail_grid_events BEFORE INSERT ON grid_events BEGIN SELECT RAISE(ABORT, 'test'); END`)
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(time.Second), fails(6)))
	if events, _ := st.GridEvents(ctx, "p1", t0, t0.Add(time.Hour)); len(events) != 0 {
		t.Fatalf("stored despite the failing write: %+v", events)
	}
	exec(`DROP TRIGGER fail_grid_events`)
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(2*time.Second), fails(6)))
	rec.Add(ctx, "p1", homewizard.KindP1, reading(t0.Add(3*time.Second), fails(6)))
	events, err := st.GridEvents(ctx, "p1", t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != store.GridFail || events[0].Count != 1 ||
		!events[0].Since.Equal(t0) || !events[0].At.Equal(t0.Add(2*time.Second)) {
		t.Fatalf("events %+v", events)
	}
}
