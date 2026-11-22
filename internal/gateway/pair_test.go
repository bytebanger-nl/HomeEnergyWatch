package gateway

import (
	"context"
	"errors"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// A pairing that completes after the device was removed does not bring it
// back; one that completes after a rename keeps the new name.
func TestSavePairedUsesLatestRow(t *testing.T) {
	host := fakeP1(t, nil)
	m, _, _ := newManager(t)
	ctx := context.Background()
	v, err := m.AddManual(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	name := "Meterkast"
	if _, err := m.Update(ctx, v.ID, DeviceUpdate{Name: &name}); err != nil {
		t.Fatal(err)
	}
	m.savePaired(v.ID, "127.0.0.1:1", "token1", homewizard.Info{}, errors.New("offline"))
	d, err := m.store.Device(ctx, v.ID)
	if err != nil || d.Token != "token1" || d.APIVersion != 2 || d.Name != name {
		t.Fatalf("paired row %+v %v", d, err)
	}

	if err := m.Remove(ctx, v.ID, false); err != nil {
		t.Fatal(err)
	}
	m.savePaired(v.ID, "127.0.0.1:1", "token2", homewizard.Info{}, errors.New("offline"))
	if _, err := m.store.Device(ctx, v.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("removed device came back: %v", err)
	}
	if _, ok := m.View(v.ID); ok {
		t.Fatal("removed device has a worker again")
	}
}

// A pairing that succeeds ends: the restarted worker polls the device and
// can be paired again.
func TestSavePairedEndsPairing(t *testing.T) {
	host := fakeP1(t, nil)
	m, _, _ := newManager(t)
	v, err := m.AddManual(context.Background(), host)
	if err != nil {
		t.Fatal(err)
	}
	if !m.worker(v.ID).beginPairing() {
		t.Fatal("pairing did not start")
	}
	m.savePaired(v.ID, "127.0.0.1:1", "token1", homewizard.Info{}, errors.New("offline"))
	if view, _ := m.View(v.ID); view.Status == StatusPairing {
		t.Fatal("still pairing after it succeeded")
	}
	if !m.worker(v.ID).beginPairing() {
		t.Fatal("pairing carried over to the new worker")
	}
}

// A restart while pairing (discovery found a new address) keeps the pairing
// on the new worker, so its end is shown there.
func TestRestartKeepsPairing(t *testing.T) {
	host := fakeP1(t, nil)
	m, _, _ := newManager(t)
	ctx := context.Background()
	v, err := m.AddManual(ctx, host)
	if err != nil {
		t.Fatal(err)
	}
	if !m.worker(v.ID).beginPairing() {
		t.Fatal("pairing did not start")
	}
	m.restart(m.worker(v.ID).device())
	if m.worker(v.ID).beginPairing() {
		t.Fatal("pairing lost on restart")
	}
	m.endPairing(v.ID, "Koppelen verlopen")
	if !m.worker(v.ID).beginPairing() {
		t.Fatal("pairing not ended on the current worker")
	}
}
