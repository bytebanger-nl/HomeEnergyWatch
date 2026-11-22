package plugin

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// flakyProvider's Poll fails while flakyFail is set.
type flakyProvider struct{}

var flakyFail atomic.Bool

func (flakyProvider) Info() ProviderInfo {
	return ProviderInfo{Type: "test-flaky", Name: "Test flaky", CanAddManual: true, IntervalSeconds: 30}
}
func (flakyProvider) Instance(map[string]string, Env) (Instance, error) { return flakyInstance{}, nil }

type flakyInstance struct{}

func (flakyInstance) Scan(context.Context) ([]Candidate, error) { return nil, nil }
func (flakyInstance) Poll(context.Context, Device) (Reading, error) {
	if flakyFail.Load() {
		return Reading{}, errors.New("net/http: TLS handshake timeout")
	}
	return Reading{Online: true, Metrics: []Metric{{Key: "indoor", Label: "Kamer", Kind: MetricNumber, Value: 21.0}}}, nil
}
func (flakyInstance) Close() {}

func init() { Register(flakyProvider{}) }

// A device stays online with its last reading through offlineAfter-1 failed
// polls, goes offline on the next, and both changes are logged once.
func TestPluginOfflineAfterConsecutiveFailures(t *testing.T) {
	m := newTestManager(t)
	var logs bytes.Buffer
	m.log = slog.New(slog.NewTextHandler(&logs, nil))
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "test-flaky", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	view := func() DeviceView {
		t.Helper()
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.latest[dev.ID]
	}

	flakyFail.Store(false)
	m.pollDue(ctx, true, "")
	good := view()
	if good.Reading == nil || !good.Reading.Online || good.LastSeen == nil {
		t.Fatalf("first poll: %+v", good)
	}
	time.Sleep(5 * time.Millisecond)

	flakyFail.Store(true)
	for i := 1; i < offlineAfter; i++ {
		m.pollDue(ctx, true, "")
		v := view()
		if v.Reading == nil || !v.Reading.Online || len(v.Reading.Metrics) != 1 {
			t.Fatalf("failure %d: reading %+v, want the last good one", i, v.Reading)
		}
		if !v.LastSeen.Equal(*good.LastSeen) {
			t.Fatalf("failure %d: last seen moved to %v", i, v.LastSeen)
		}
	}
	m.pollDue(ctx, true, "")
	if v := view(); v.Reading == nil || v.Reading.Online || !strings.Contains(v.Reading.Err, "TLS handshake timeout") {
		t.Fatalf("failure %d: reading %+v, want offline with the error", offlineAfter, v.Reading)
	}
	m.pollDue(ctx, true, "") // still offline: logged once

	flakyFail.Store(false)
	m.pollDue(ctx, true, "")
	if v := view(); v.Reading == nil || !v.Reading.Online {
		t.Fatalf("after recovery: %+v", v.Reading)
	}
	out := logs.String()
	if n := strings.Count(out, `msg="plugin-apparaat offline"`); n != 1 {
		t.Errorf("logged offline %d times, want 1:\n%s", n, out)
	}
	if n := strings.Count(out, `msg="plugin-apparaat online"`); n != 2 {
		t.Errorf("logged online %d times, want 2 (start and recovery):\n%s", n, out)
	}

	// one failure after the recovery is again bridged
	flakyFail.Store(true)
	m.pollDue(ctx, true, "")
	if v := view(); v.Reading == nil || !v.Reading.Online {
		t.Fatalf("single failure after recovery: %+v", v.Reading)
	}
}

// Without an earlier good reading there is nothing to keep: a failing new
// device shows offline at once.
func TestPluginFirstPollFailureIsOffline(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	inst, err := m.AddInstance(ctx, "test-flaky", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	flakyFail.Store(true)
	defer flakyFail.Store(false)
	m.pollDue(ctx, true, "")
	m.mu.Lock()
	v := m.latest[dev.ID]
	m.mu.Unlock()
	if v.Reading == nil || v.Reading.Online {
		t.Fatalf("reading %+v, want offline", v.Reading)
	}
}
