package history

import (
	"context"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func f(v float64) *float64 { return &v }

// minutes with a counter rising 1 kWh per hour, last reading at :30 s.
func linearMinutes(from time.Time, n int, skip func(i int) bool) []store.Minute {
	var out []store.Minute
	for i := 0; i < n; i++ {
		if skip != nil && skip(i) {
			continue
		}
		m := from.Add(time.Duration(i) * time.Minute)
		last := m.Add(30 * time.Second)
		kwh := 100 + last.Sub(from).Hours()
		out = append(out, store.Minute{DeviceID: "d", Minute: m, Samples: 30, Last: last, ImportKWh: f(kwh), ExportKWh: f(5)})
	}
	return out
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestComputeQuartersComplete(t *testing.T) {
	from := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	minutes := linearMinutes(from.Add(-5*time.Minute), 70, nil)
	qs := ComputeQuarters("d", minutes, from, from.Add(time.Hour), 5*time.Minute)
	if len(qs) != 4 {
		t.Fatalf("got %d quarters", len(qs))
	}
	for _, q := range qs {
		if q.Quality != QualityComplete || q.Coverage != 1 {
			t.Errorf("%v: quality %s coverage %v", q.Start, q.Quality, q.Coverage)
		}
		if !near(*q.ImportKWh, 0.25) || !near(*q.ExportKWh, 0) {
			t.Errorf("%v: import %v export %v", q.Start, *q.ImportKWh, *q.ExportKWh)
		}
		if q.GasM3 != nil || q.WaterM3 != nil {
			t.Errorf("%v: unexpected gas/water", q.Start)
		}
	}
}

func TestComputeQuartersGapIsPartial(t *testing.T) {
	from := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	// no data between 10:12 and 10:24: the 10:15 boundary cannot be interpolated
	minutes := linearMinutes(from.Add(-5*time.Minute), 40, func(i int) bool { return i >= 17 && i < 29 })
	qs := ComputeQuarters("d", minutes, from, from.Add(30*time.Minute), 5*time.Minute)
	if len(qs) != 2 {
		t.Fatalf("got %d quarters", len(qs))
	}
	for _, q := range qs {
		if q.Quality != QualityPartial {
			t.Errorf("%v: quality %s, want partial", q.Start, q.Quality)
		}
		if *q.ImportKWh >= 0.25 {
			t.Errorf("%v: partial import %v should be below a full quarter", q.Start, *q.ImportKWh)
		}
	}
}

// Minutes missing inside a quarter do not make it partial: both boundaries
// are interpolated between readings at most maxGap apart, so the energy is
// known. Coverage still tells how much is missing.
func TestComputeQuartersGapInsideIsComplete(t *testing.T) {
	from := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	// no readings 10:02-10:12 (ten minutes, but not around a boundary)
	minutes := linearMinutes(from.Add(-5*time.Minute), 25, func(i int) bool { return i >= 7 && i < 17 })
	qs := ComputeQuarters("d", minutes, from, from.Add(15*time.Minute), 5*time.Minute)
	if len(qs) != 1 {
		t.Fatalf("got %d quarters", len(qs))
	}
	q := qs[0]
	if q.Quality != QualityComplete || !near(*q.ImportKWh, 0.25) || q.Coverage >= 0.99 {
		t.Errorf("quality %s import %v coverage %v, want complete 0.25 with lower coverage", q.Quality, *q.ImportKWh, q.Coverage)
	}
}

func TestComputeQuartersCounterReset(t *testing.T) {
	from := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	minutes := linearMinutes(from.Add(-5*time.Minute), 25, nil)
	minutes[12].ImportKWh = f(1) // counter jumps back
	qs := ComputeQuarters("d", minutes, from, from.Add(15*time.Minute), 5*time.Minute)
	if len(qs) != 1 || qs[0].Quality != QualityAnomalous || qs[0].ImportKWh != nil {
		t.Fatalf("got %+v", qs)
	}
}

func TestRecorderMinutesTodayAndEnergy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := NewRecorder(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Location: time.UTC})
	ctx := context.Background()
	start := time.Now().UTC().Truncate(time.Minute).Add(-3 * time.Minute)
	for i := 0; i < 90; i++ { // 3 minutes of readings every 2 s
		ts := start.Add(time.Duration(i) * 2 * time.Second)
		rec.Add(ctx, "p1", homewizard.KindP1, homewizard.Reading{Time: ts, Values: map[string]float64{
			"power_w": 1000, "energy_import_kwh": 50 + float64(i)*0.001,
		}})
	}
	minutes, err := st.Minutes(ctx, "p1", start, start.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(minutes) != 2 { // the third minute is still open
		t.Fatalf("stored %d minutes", len(minutes))
	}
	if minutes[0].Samples != 30 || *minutes[0].RateAvg != 1000 {
		t.Errorf("minute = %+v", minutes[0])
	}
	series, _, err := rec.RateSeries(ctx, "p1", start, start.Add(10*time.Minute), 100)
	if err != nil || len(series) != 3 {
		t.Fatalf("series %d %v", len(series), err)
	}
	cur := homewizard.Reading{Time: start.Add(3 * time.Minute), Values: map[string]float64{"energy_import_kwh": 50.1}}
	today := rec.Today(ctx, "p1", cur)
	if v := today["energy_import_kwh"]; v < 0 || v > 0.1+1e-9 {
		t.Errorf("today = %v", today)
	}
}

func TestEnergyBucketsKeepEmptyPeriods(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := NewRecorder(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Location: time.UTC})
	ctx := context.Background()
	day := time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	var qs []store.Quarter
	for i := 0; i < 4; i++ { // only the first hour has data
		qs = append(qs, store.Quarter{DeviceID: "p1", Start: day.Add(time.Duration(i) * QuarterDuration), ImportKWh: f(0.5), Quality: QualityComplete, Coverage: 1})
	}
	if err := st.SaveQuarters(ctx, qs); err != nil {
		t.Fatal(err)
	}
	buckets, err := rec.Energy(ctx, "p1", day, day.Add(24*time.Hour), BucketHour)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 24 {
		t.Fatalf("got %d buckets", len(buckets))
	}
	if !buckets[0].Complete || !near(*buckets[0].ImportKWh, 2) || buckets[1].ImportKWh != nil || buckets[1].Complete {
		t.Errorf("buckets = %+v %+v", buckets[0], buckets[1])
	}
	days, _ := rec.Energy(ctx, "p1", day, day.Add(48*time.Hour), BucketDay)
	if len(days) != 2 || days[0].Complete {
		t.Errorf("day buckets = %+v", days)
	}
}

func TestRateSeriesFallsBackToQuarters(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	rec := NewRecorder(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Location: time.UTC})
	ctx := context.Background()
	start := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	// an imported quarter of 0.25 kWh is an average of 1000 W
	if _, err := st.ImportQuarters(ctx, []store.Quarter{{DeviceID: "p1", Start: start, ImportKWh: f(0.25), Quality: QualityComplete, Coverage: 1}}, false); err != nil {
		t.Fatal(err)
	}
	series, _, err := rec.RateSeries(ctx, "p1", start, start.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(series) != 15 || !near(series[0].Avg, 1000) {
		t.Fatalf("got %d points, first %+v", len(series), series)
	}
}

// On start, a stored partial quarter whose minutes now make it complete is
// stored as complete, and the cost hook hears about it.
func TestRepairPartial(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	from := time.Now().UTC().Add(-72 * time.Hour).Truncate(QuarterDuration)
	for _, m := range linearMinutes(from.Add(-5*time.Minute), 25, func(i int) bool { return i == 10 || i == 11 }) {
		if err := st.SaveMinute(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	// stored partial by the old rule (every minute needed)
	if err := st.SaveQuarters(ctx, []store.Quarter{{DeviceID: "d", Start: from, ImportKWh: f(0.25), ExportKWh: f(0), Quality: QualityPartial, Coverage: 13.0 / 15}}); err != nil {
		t.Fatal(err)
	}
	rec := NewRecorder(st, slog.New(slog.NewTextHandler(io.Discard, nil)), Config{Location: time.UTC})
	var changed time.Time
	rec.OnQuarters = func(_ string, at time.Time) { changed = at }
	rec.repairPartial(ctx, []string{"d"})
	qs, err := st.Quarters(ctx, "d", from, from.Add(QuarterDuration))
	if err != nil || len(qs) != 1 {
		t.Fatalf("%+v %v", qs, err)
	}
	if qs[0].Quality != QualityComplete || !near(*qs[0].ImportKWh, 0.25) {
		t.Errorf("after repair: %+v", qs[0])
	}
	if !changed.Equal(from) {
		t.Errorf("OnQuarters got %v, want %v", changed, from)
	}
}
