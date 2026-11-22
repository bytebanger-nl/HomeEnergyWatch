package store

import (
	"context"
	"testing"
	"time"
)

// The same kind and subject while unread is one notice, counted; once read,
// it happening again is a new unread notice. Reading stops at until, so what
// came in after the user looked stays unread.
func TestNotices(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	for i := range 3 {
		if err := st.AddNotice(ctx, "report", "Maand", "Rapport niet verstuurd", "smtp down", t0.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.AddNotice(ctx, "backup", "", "Back-up mislukt", "disk full", t0.Add(30*time.Minute)); err != nil {
		t.Fatal(err)
	}
	list, err := st.Notices(ctx, t0.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Kind != "report" || list[0].Count != 3 || !list[0].First.Equal(t0) || !list[0].Last.Equal(t0.Add(2*time.Hour)) {
		t.Fatalf("notices %+v", list)
	}
	// read up to 11:00: the backup (10:30), not the report (last 12:00)
	if n, err := st.ReadNotices(ctx, t0.Add(time.Hour)); err != nil || n != 1 {
		t.Fatalf("read %d %v", n, err)
	}
	if n, _ := st.UnreadNotices(ctx); n != 1 {
		t.Fatalf("unread %d, want 1", n)
	}
	st.ReadNotices(ctx, t0.Add(3*time.Hour))
	if err := st.AddNotice(ctx, "report", "Maand", "Rapport niet verstuurd", "again", t0.Add(4*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if list, _ := st.Notices(ctx, t0); len(list) != 3 || list[0].Read || list[0].Count != 1 {
		t.Fatalf("after reading, a new one: %+v", list)
	}
}

// Outages are read up to until, per device or all; the unread state counts
// them and an open outage shows even when read.
func TestReadOutages(t *testing.T) {
	st, _ := openTest(t)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		if err := st.SaveDevice(ctx, Device{ID: id, Host: "10.0.0.2", Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	st.StartOutage(ctx, "a", t0, "x")
	st.EndOutage(ctx, "a", t0.Add(time.Minute))
	st.StartOutage(ctx, "b", t0.Add(time.Hour), "y") // still open
	if n, err := st.ReadOutages(ctx, "", t0.Add(30*time.Minute)); err != nil || n != 1 {
		t.Fatalf("read %d %v", n, err)
	}
	got, err := st.UnreadOutages(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].DeviceID != "b" || got[0].Unread != 1 || !got[0].Open {
		t.Fatalf("unread %+v", got)
	}
	st.ReadOutages(ctx, "b", t0.Add(2*time.Hour))
	if got, _ := st.UnreadOutages(ctx); len(got) != 1 || got[0].Unread != 0 || !got[0].Open {
		t.Fatalf("read but open: %+v", got)
	}
	list, _ := st.Outages(ctx, "", t0.Add(-time.Hour), t0.Add(3*time.Hour))
	if len(list) != 2 || list[0].DeviceID != "b" || !list[0].Read || !list[1].Read {
		t.Fatalf("all outages %+v", list)
	}
}
