package store

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data", "hwlocal.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

func f(v float64) *float64 { return &v }

func quarter(t *testing.T, st *Store, start time.Time) Quarter {
	t.Helper()
	qs, err := st.Quarters(context.Background(), "p1", start, start.Add(15*time.Minute))
	if err != nil || len(qs) != 1 {
		t.Fatalf("quarter %s: %v %v", start, qs, err)
	}
	return qs[0]
}

// A quarter hour the gateway measured only partly (e.g. it started halfway)
// must not replace a complete imported value; a complete measurement may.
func TestPartialMeasurementKeepsImportedQuarter(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	imported := Quarter{DeviceID: "p1", Start: start, ImportKWh: f(1.0), GasM3: f(0.2), Quality: "complete", Coverage: 1, Source: SourceImport}
	if _, err := st.ImportQuarters(ctx, []Quarter{imported}, false); err != nil {
		t.Fatal(err)
	}
	partial := Quarter{DeviceID: "p1", Start: start, ImportKWh: f(0.4), Quality: "partial", Coverage: 0.5}
	if err := st.SaveQuarters(ctx, []Quarter{partial}); err != nil {
		t.Fatal(err)
	}
	if q := quarter(t, st, start); *q.ImportKWh != 1.0 || q.Source != SourceImport || q.Quality != "complete" {
		t.Fatalf("partial measurement replaced imported data: %+v import=%v", q, *q.ImportKWh)
	}
	anomalous := Quarter{DeviceID: "p1", Start: start, Quality: "anomalous", Coverage: 1}
	if err := st.SaveQuarters(ctx, []Quarter{anomalous}); err != nil {
		t.Fatal(err)
	}
	if q := quarter(t, st, start); q.Source != SourceImport || q.Quality != "complete" {
		t.Fatalf("anomalous measurement replaced imported data: %+v", q)
	}
	complete := Quarter{DeviceID: "p1", Start: start, ImportKWh: f(0.9), Quality: "complete", Coverage: 1}
	if err := st.SaveQuarters(ctx, []Quarter{complete}); err != nil {
		t.Fatal(err)
	}
	q := quarter(t, st, start)
	if *q.ImportKWh != 0.9 || q.Source != SourceMeasured || q.GasM3 == nil || *q.GasM3 != 0.2 {
		t.Fatalf("complete measurement: %+v", q)
	}
	// a measured partial quarter is still updated as more data arrives
	next := start.Add(15 * time.Minute)
	_ = st.SaveQuarters(ctx, []Quarter{{DeviceID: "p1", Start: next, ImportKWh: f(0.1), Quality: "partial", Coverage: 0.2}})
	_ = st.SaveQuarters(ctx, []Quarter{{DeviceID: "p1", Start: next, ImportKWh: f(0.3), Quality: "partial", Coverage: 0.6}})
	if q := quarter(t, st, next); *q.ImportKWh != 0.3 || q.Coverage != 0.6 {
		t.Fatalf("measured partial not updated: %+v", q)
	}
}

// A quarter the gateway measured only partly, or with a counter going
// backwards, is replaced by a complete imported quarter; counters the file
// does not hold keep their measured value. A complete measurement stays.
func TestImportReplacesPartialMeasurement(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	start := time.Date(2026, 11, 6, 5, 30, 0, 0, time.UTC)
	anom, full := start.Add(15*time.Minute), start.Add(30*time.Minute)
	if err := st.SaveQuarters(ctx, []Quarter{
		{DeviceID: "p1", Start: start, ImportKWh: f(0.048), GasM3: f(0.01), Quality: "partial", Coverage: 0.6},
		{DeviceID: "p1", Start: anom, GasM3: f(0.02), Quality: "anomalous", Coverage: 1},
		{DeviceID: "p1", Start: full, ImportKWh: f(0.07), Quality: "complete", Coverage: 1},
	}); err != nil {
		t.Fatal(err)
	}
	imp := func(at time.Time, v float64) Quarter {
		return Quarter{DeviceID: "p1", Start: at, ImportKWh: f(v), ExportKWh: f(0), Quality: "complete", Coverage: 1, Source: SourceImport}
	}
	qs := []Quarter{imp(start, 0.072), imp(anom, 0.05), imp(full, 0.08)}
	dry, err := st.ImportQuarters(ctx, qs, true)
	if err != nil || dry.Replaced != 2 || dry.Added != 1 || dry.Skipped != 0 {
		t.Fatalf("dry run %+v %v", dry, err)
	}
	if q := quarter(t, st, start); *q.ImportKWh != 0.048 {
		t.Fatalf("dry run wrote: %+v", q)
	}
	if _, err := st.ImportQuarters(ctx, qs, false); err != nil {
		t.Fatal(err)
	}
	q := quarter(t, st, start)
	if *q.ImportKWh != 0.072 || q.Quality != "complete" || q.Source != SourceImport || q.Coverage != 1 || *q.GasM3 != 0.01 {
		t.Fatalf("partial not replaced: %+v", q)
	}
	if q := quarter(t, st, anom); *q.ImportKWh != 0.05 || q.Quality != "complete" || *q.GasM3 != 0.02 {
		t.Fatalf("anomalous not replaced: %+v", q)
	}
	// complete measurement: only the empty export counter is filled
	if q := quarter(t, st, full); *q.ImportKWh != 0.07 || q.Source != SourceMeasured || *q.ExportKWh != 0 {
		t.Fatalf("complete measurement changed: %+v", q)
	}
	again, err := st.ImportQuarters(ctx, qs, false)
	if err != nil || again.Added != 0 || again.Replaced != 0 || again.Skipped != 3 {
		t.Fatalf("second import %+v %v", again, err)
	}
}

// Re-importing the same data adds nothing and changes nothing.
func TestImportIsIdempotent(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	qs := []Quarter{{DeviceID: "p1", Start: start, ImportKWh: f(1), Quality: "complete", Coverage: 1, Source: SourceImport}}
	first, err := st.ImportQuarters(ctx, qs, false)
	if err != nil || first.Added != 1 {
		t.Fatalf("first import %+v %v", first, err)
	}
	second, err := st.ImportQuarters(ctx, qs, false)
	if err != nil || second.Added != 0 || second.Skipped != 1 {
		t.Fatalf("second import %+v %v", second, err)
	}
	if q := quarter(t, st, start); *q.ImportKWh != 1 {
		t.Fatalf("value changed: %v", *q.ImportKWh)
	}
}

// The database and its WAL files hold device tokens: only the owner may
// read them, also when SQLite creates the WAL files during the first write.
func TestDatabaseFilesArePrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix file modes")
	}
	_, path := openTest(t)
	for _, p := range []string{path, path + "-wal", path + "-shm", filepath.Dir(path)} {
		fi, err := os.Stat(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		if perm := fi.Mode().Perm(); perm&0o077 != 0 {
			t.Errorf("%s has mode %v, want no access for group/others", filepath.Base(p), perm)
		}
	}
}

// Opening an existing database again runs no migration twice and keeps data.
func TestReopenKeepsData(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hwlocal.db")
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if err := st.SaveQuarters(context.Background(), []Quarter{{DeviceID: "p1", Start: start, ImportKWh: f(2), Quality: "complete", Coverage: 1}}); err != nil {
		t.Fatal(err)
	}
	st.Close()
	st, err = Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer st.Close()
	var version int
	if err := st.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil || version != len(migrations) {
		t.Fatalf("user_version %d, want %d (%v)", version, len(migrations), err)
	}
	if q := quarter(t, st, start); *q.ImportKWh != 2 {
		t.Fatalf("data lost: %+v", q)
	}
}
