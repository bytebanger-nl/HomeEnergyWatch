package plugin

import (
	"context"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/outage"
)

// A plugin device reports to the same outage log as HomeWizard devices: an
// outage from its first failed poll until it answers again; the failed polls
// bridged by the grace before it count, a single failure alone does not.
func TestPluginOutageLogged(t *testing.T) {
	m := newTestManager(t)
	ctx := context.Background()
	m.Outages = outage.New(m.store, m.log)
	if err := m.Outages.Start(ctx); err != nil {
		t.Fatal(err)
	}
	inst, err := m.AddInstance(ctx, "test-flaky", "Airco", nil)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := m.AddDevice(ctx, inst.ID, Candidate{Address: "1.2.3.4"}, "Woonkamer")
	if err != nil {
		t.Fatal(err)
	}
	flakyFail.Store(false)
	m.pollDue(ctx, true, "")
	before := time.Now()
	flakyFail.Store(true)
	m.pollDue(ctx, true, "") // one failure: bridged, no outage yet
	list := func() int {
		t.Helper()
		l, err := m.store.Outages(ctx, dev.ID, before.Add(-time.Hour), time.Now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		return len(l)
	}
	if n := list(); n != 0 {
		t.Fatalf("%d outages after one failure", n)
	}
	for range offlineAfter {
		m.pollDue(ctx, true, "")
	}
	flakyFail.Store(false)
	m.pollDue(ctx, true, "")
	got, err := m.store.Outages(ctx, dev.ID, before.Add(-time.Hour), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].End == nil || got[0].Start.Before(before.Truncate(time.Millisecond)) || got[0].Message == "" {
		t.Fatalf("outages %+v, want one closed from the first failure", got)
	}
}
