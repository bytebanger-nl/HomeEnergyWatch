package plugin

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// a minimal provider so the manager has a type to instantiate in tests
type fakeProvider struct{}

func (fakeProvider) Info() ProviderInfo {
	return ProviderInfo{Type: "test", Name: "Test", CanAddManual: true, CanLinkMeter: true, IntervalSeconds: 30}
}
func (fakeProvider) Instance(map[string]string, Env) (Instance, error) { return fakeInstance{}, nil }

type fakeInstance struct{}

func (fakeInstance) Scan(context.Context) ([]Candidate, error) { return nil, nil }
func (fakeInstance) Poll(context.Context, Device) (Reading, error) {
	return Reading{Online: true}, nil
}
func (fakeInstance) Close() {}

func init() { Register(fakeProvider{}) }

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hwlocal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewManager(st, gateway.NewHub(), slog.New(slog.NewTextHandler(io.Discard, nil)), t.TempDir())
}

// A rename must show in Views() at once, even when a reading from an earlier
// poll is cached under the old name (regression: Views() used to return the
// cached view verbatim, so the new name appeared only after the next poll).
func TestViewsReflectsRenameOverCachedReading(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()

	inst, err := m.AddInstance(ctx, "test", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Oude naam")
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a completed poll cached under the old name.
	seen := time.Now()
	m.mu.Lock()
	m.latest[dev.ID] = DeviceView{ID: dev.ID, Name: "Oude naam", Address: "1.2.3.4",
		LastSeen: &seen, Reading: &Reading{Online: true}}
	m.mu.Unlock()

	newName := "Airco woonkamer"
	if err := m.UpdateDevice(ctx, dev.ID, &newName, nil, nil); err != nil {
		t.Fatal(err)
	}

	views := m.Views()
	if len(views) != 1 || len(views[0].Devices) != 1 {
		t.Fatalf("unexpected views: %+v", views)
	}
	got := views[0].Devices[0]
	if got.Name != newName {
		t.Errorf("Name = %q, want %q (rename not reflected)", got.Name, newName)
	}
	if got.Reading == nil || !got.Reading.Online {
		t.Error("cached reading should be preserved through the overlay")
	}
}

// a provider whose scan finds two devices
type scanProvider struct{}

func (scanProvider) Info() ProviderInfo {
	return ProviderInfo{Type: "test-scan", Name: "Scan", IntervalSeconds: 30}
}
func (scanProvider) Instance(map[string]string, Env) (Instance, error) { return scanInstance{}, nil }

type scanInstance struct{ fakeInstance }

func (scanInstance) Scan(context.Context) ([]Candidate, error) {
	return []Candidate{{Address: "zone-1", Name: "Woonkamer"},
		{Address: "zone-2", Name: "Slaapkamer", Config: map[string]string{"schema": "new", "key": "k2"}}}, nil
}

func init() { Register(scanProvider{}) }

// Adding a device whose address the instance already has is refused, also
// with other case or surrounding spaces; another instance may add it.
func TestAddDeviceRejectsDuplicate(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	a, err := m.AddInstance(ctx, "test", "A", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := m.AddInstance(ctx, "test", "B", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddDevice(ctx, a.ID, Candidate{Address: "zone-1"}, "Woonkamer"); err != nil {
		t.Fatal(err)
	}
	for _, addr := range []string{"zone-1", " ZONE-1 "} {
		if _, err := m.AddDevice(ctx, a.ID, Candidate{Address: addr}, "Nog een keer"); !errors.Is(err, ErrDuplicate) {
			t.Errorf("add %q again: err = %v, want ErrDuplicate", addr, err)
		}
	}
	if _, err := m.AddDevice(ctx, b.ID, Candidate{Address: "zone-1"}, "Woonkamer"); err != nil {
		t.Errorf("same address on another instance: %v", err)
	}
	devs, err := m.store.PluginDevices(ctx, a.ID)
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices of A: %d, %v; want 1", len(devs), err)
	}
}

// Scan marks the candidates the instance already has.
func TestScanMarksAddedCandidates(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "test-scan", "tado", nil)
	if err != nil {
		t.Fatal(err)
	}
	added, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "zone-2",
		Config: map[string]string{"schema": "old", "own": "kept"}}, "Mijn kamer")
	if err != nil {
		t.Fatal(err)
	}
	cands, err := m.Scan(ctx, inst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 2 || cands[0].Added || !cands[1].Added {
		t.Fatalf("candidates = %+v, want only zone-2 added", cands)
	}
	// the added device takes over what the scan found, keeps the rest
	devs, err := m.store.PluginDevices(ctx, inst.ID)
	if err != nil || len(devs) != 1 {
		t.Fatalf("devices: %d, %v", len(devs), err)
	}
	d := devs[0]
	if d.ID != added.ID || d.Name != "Mijn kamer" || d.Config["schema"] != "new" || d.Config["key"] != "k2" || d.Config["own"] != "kept" {
		t.Fatalf("device after scan = %+v", d)
	}
}
