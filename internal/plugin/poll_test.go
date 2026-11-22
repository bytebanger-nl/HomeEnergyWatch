package plugin

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// blockProvider's Poll waits until the test releases it, so the test can
// change the config while a poll is in flight.
type blockProvider struct{}

var (
	pollEntered chan struct{}
	pollRelease chan struct{}
)

func (blockProvider) Info() ProviderInfo {
	return ProviderInfo{Type: "test-block", Name: "Test block", CanAddManual: true, IntervalSeconds: 30}
}
func (blockProvider) Instance(map[string]string, Env) (Instance, error) { return &blockInstance{}, nil }

type blockInstance struct{}

func (*blockInstance) Scan(context.Context) ([]Candidate, error) { return nil, nil }
func (*blockInstance) Poll(context.Context, Device) (Reading, error) {
	pollEntered <- struct{}{}
	<-pollRelease
	return Reading{Online: true, Metrics: []Metric{{Key: "t", Label: "T", Kind: MetricNumber, Value: 21.5}}}, nil
}
func (*blockInstance) Close() {}

func init() { Register(blockProvider{}) }

// pollWhile polls everything, runs change while the poll is in flight, then
// lets the poll finish. It returns the types of the events published from
// the moment change starts.
func pollWhile(t *testing.T, m *Manager, change func()) []string {
	t.Helper()
	pollEntered, pollRelease = make(chan struct{}, 1), make(chan struct{})
	events, cancel := m.hub.Subscribe()
	defer cancel()
	done := make(chan struct{})
	go func() { m.pollDue(context.Background(), true, ""); close(done) }()
	select {
	case <-pollEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("poll did not start")
	}
	change()
	close(pollRelease)
	<-done
	var types []string
	for len(events) > 0 {
		var e struct{ Type string }
		_ = json.Unmarshal(<-events, &e)
		types = append(types, e.Type)
	}
	return types
}

// A poll that finishes after its device was removed must not bring the
// device back (regression: the reading was cached and published as a
// "plugin" event, so the PWA showed the deleted device again).
func TestPollAfterDeleteIsDropped(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "test-block", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	types := pollWhile(t, m, func() {
		if err := m.DeleteDevice(ctx, dev.ID); err != nil {
			t.Fatal(err)
		}
	})
	if len(types) != 1 || types[0] != "plugins" {
		t.Errorf("events %v, want only the delete broadcast", types)
	}
	m.mu.Lock()
	_, cached := m.latest[dev.ID]
	m.mu.Unlock()
	if cached {
		t.Error("reading of the deleted device was cached")
	}
}

// A disabled device is no longer polled, so it must not keep showing its
// last reading as if it were current; nor may a poll in flight while it was
// disabled put that reading back.
func TestDisabledDeviceHasNoReading(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "test-block", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	reading := func() *Reading {
		t.Helper()
		views := m.Views()
		if len(views) != 1 || len(views[0].Devices) != 1 {
			t.Fatalf("views %+v", views)
		}
		return views[0].Devices[0].Reading
	}

	pollWhile(t, m, func() {}) // a normal poll
	if reading() == nil {
		t.Fatal("no reading after a poll")
	}
	off := false
	if err := m.UpdateDevice(ctx, dev.ID, nil, &off, nil); err != nil {
		t.Fatal(err)
	}
	if r := reading(); r != nil {
		t.Errorf("disabled device still shows a reading: %+v", r)
	}

	on := true
	if err := m.UpdateDevice(ctx, dev.ID, nil, &on, nil); err != nil {
		t.Fatal(err)
	}
	types := pollWhile(t, m, func() {
		if err := m.UpdateDevice(ctx, dev.ID, nil, &off, nil); err != nil {
			t.Fatal(err)
		}
	})
	if r := reading(); r != nil || len(types) != 1 {
		t.Errorf("poll in flight while disabling: reading %+v, events %v", r, types)
	}

	// disabling the whole instance drops the readings too
	if err := m.UpdateDevice(ctx, dev.ID, nil, &on, nil); err != nil {
		t.Fatal(err)
	}
	pollWhile(t, m, func() {})
	if _, err := m.UpdateInstance(ctx, inst.ID, nil, &off, nil, nil); err != nil {
		t.Fatal(err)
	}
	if r := reading(); r != nil {
		t.Errorf("device of a disabled instance still shows a reading: %+v", r)
	}
}
