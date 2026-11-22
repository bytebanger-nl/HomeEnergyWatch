package outage

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

type fixture struct {
	st  *store.Store
	log *Log
	now time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "hwlocal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &fixture{st: st, now: time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)}
	f.log = New(st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	f.log.now = func() time.Time { return f.now }
	if err := f.log.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) outages(t *testing.T, id string) []store.Outage {
	t.Helper()
	list, err := f.st.Outages(context.Background(), id, f.now.Add(-48*time.Hour), f.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return list
}

// A device that does not answer right after a start (network still coming
// up) is no outage; one that stays away past the grace is, from the end of
// the grace.
func TestGraceAfterStart(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	start := f.now
	f.now = start.Add(30 * time.Second)
	f.log.Offline(ctx, "a", start.Add(5*time.Second), "timeout")
	f.now = start.Add(time.Minute)
	f.log.Online(ctx, "a", f.now)
	if got := f.outages(t, "a"); len(got) != 0 {
		t.Fatalf("outage during the grace: %+v", got)
	}

	f.now = start.Add(3 * time.Minute)
	f.log.Offline(ctx, "b", start.Add(5*time.Second), "timeout")
	got := f.outages(t, "b")
	if len(got) != 1 || !got[0].Start.Equal(start.Add(DefaultGrace)) || got[0].End != nil {
		t.Fatalf("outages %+v, want one open from the end of the grace", got)
	}
}

// A device seen since the start counts from its first failed poll, even
// within the grace; it ends when the device answers again.
func TestSeenDeviceOutage(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.log.Online(ctx, "a", f.now)
	failed := f.now.Add(10 * time.Second)
	f.now = f.now.Add(20 * time.Second)
	f.log.Offline(ctx, "a", failed, "timeout")
	f.log.Offline(ctx, "a", failed, "timeout") // still offline: one outage
	back := f.now.Add(5 * time.Minute)
	f.log.Online(ctx, "a", back)
	got := f.outages(t, "a")
	if len(got) != 1 || !got[0].Start.Equal(failed) || got[0].End == nil || !got[0].End.Equal(back) || got[0].Cut {
		t.Fatalf("outages %+v, want %v-%v", got, failed, back)
	}
	if got[0].Message != "timeout" {
		t.Errorf("message %q", got[0].Message)
	}
}

// Stopping the gateway ends an open outage then, marked cut; the time the
// gateway was stopped is not counted, and after a start without Stop (a
// crash) an open outage ends at the last moment the gateway ran.
func TestStopAndCrash(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.log.Online(ctx, "a", f.now)
	failed := f.now.Add(time.Minute)
	f.log.Offline(ctx, "a", failed, "timeout")
	stop := f.now.Add(10 * time.Minute)
	f.now = stop
	f.log.Stop(ctx)
	got := f.outages(t, "a")
	if len(got) != 1 || got[0].End == nil || !got[0].End.Equal(stop) || !got[0].Cut {
		t.Fatalf("after stop: %+v", got)
	}

	// crash: open outage, a minute stored at 10:20, restart at 12:00
	f.log.Online(ctx, "b", f.now)
	f.log.Offline(ctx, "b", f.now, "timeout")
	last := time.Date(2026, 11, 17, 10, 20, 30, 0, time.UTC)
	if err := f.st.SaveMinute(ctx, store.Minute{DeviceID: "c", Minute: last.Truncate(time.Minute), Samples: 1, Last: last}); err != nil {
		t.Fatal(err)
	}
	f.now = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	restarted := New(f.st, f.log.log)
	restarted.now = func() time.Time { return f.now }
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got = f.outages(t, "b")
	if len(got) != 1 || got[0].End == nil || !got[0].End.Equal(last) || !got[0].Cut {
		t.Fatalf("after crash: %+v, want cut at %v", got, last)
	}
}

// End closes an outage of a device that is no longer polled.
func TestEnd(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.log.Online(ctx, "a", f.now)
	f.log.Offline(ctx, "a", f.now, "timeout")
	at := f.now.Add(time.Hour)
	f.log.End(ctx, "a", at)
	if got := f.outages(t, "a"); len(got) != 1 || got[0].End == nil || !got[0].End.Equal(at) || got[0].Cut {
		t.Fatalf("outages %+v", got)
	}
	var nilLog *Log
	nilLog.Offline(ctx, "a", at, "x") // a nil log records nothing
}

// After a crash an open outage ends at the gateway's last heartbeat when
// that is later than its last stored sample (every device was away).
func TestCrashEndsAtHeartbeat(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	f.log.Online(ctx, "a", f.now)
	f.log.Offline(ctx, "a", f.now, "timeout")
	alive := f.now.Add(7 * time.Minute)
	if err := f.st.SaveSetting(ctx, store.AliveKey, strconv.FormatInt(alive.UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	restarted := New(f.st, f.log.log)
	if err := restarted.Start(ctx); err != nil {
		t.Fatal(err)
	}
	got := f.outages(t, "a")
	if len(got) != 1 || got[0].End == nil || !got[0].End.Equal(alive) || !got[0].Cut {
		t.Fatalf("outages %+v, want cut at the heartbeat %v", got, alive)
	}
}
