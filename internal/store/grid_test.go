package store

import (
	"context"
	"testing"
	"time"
)

// A meter that only reports L2/L3 sag and swell counters must still give a
// baseline after a restart (regression: the query only looked at L1 and the
// power failure counters).
func TestLastGridCountersOnlyL2L3(t *testing.T) {
	ctx := context.Background()
	st, _ := openTest(t)
	minute := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	m := GridMinute{DeviceID: "p1", Minute: minute, Last: minute.Add(30 * time.Second)}
	m.Counters.Sag[1], m.Counters.Swell[2] = f(3), f(1)
	if err := st.SaveGridMinute(ctx, m); err != nil {
		t.Fatal(err)
	}
	got, err := st.LastGridCounters(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Counters.Sag[1] == nil || *got.Counters.Sag[1] != 3 || got.Counters.Swell[2] == nil || *got.Counters.Swell[2] != 1 {
		t.Fatalf("counters %+v", got.Counters)
	}
}
