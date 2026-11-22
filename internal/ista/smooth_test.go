package ista

import (
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func TestSmoothBuilding(t *testing.T) {
	day := func(d string, y int, v float64) store.IstaDay {
		return store.IstaDay{Day: d, Y: y, BuildingAvg: &v}
	}
	days := []store.IstaDay{
		day("2026-06-28", 2026, 100),
		day("2026-06-29", 2026, 250), // a peak ista takes back
		day("2026-06-30", 2026, 120),
		day("2026-07-01", 2027, 5), // a new period starts from zero
		day("2026-07-02", 2027, 3),
		day("2026-07-03", 2027, 9),
		{Day: "2026-07-04", Y: 2027}, // no average
	}
	got := smoothBuilding(days)
	want := map[string]float64{
		"2026-06-28": 100, "2026-06-29": 120, "2026-06-30": 120,
		"2026-07-01": 3, "2026-07-02": 3, "2026-07-03": 9,
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for d, v := range want {
		if got[d] != v {
			t.Errorf("%s: got %v, want %v", d, got[d], v)
		}
	}
}
