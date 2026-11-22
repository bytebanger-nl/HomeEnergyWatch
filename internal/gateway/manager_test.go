package gateway

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func fakeP1(t *testing.T, failing *atomic.Bool) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing != nil && failing.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/api":
			_, _ = w.Write([]byte(`{"product_type":"HWE-P1","serial":"5C2FAF000001","product_name":"P1 Meter","firmware_version":"5.0","api_version":"v1"}`))
		case "/api/v1/data":
			_ = json.NewEncoder(w).Encode(map[string]any{"wifi_ssid": "x", "active_power_w": 321, "total_power_import_kwh": 100.5})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func newManager(t *testing.T) (*Manager, *Hub, context.CancelFunc) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := history.NewRecorder(st, log, history.Config{Location: time.UTC})
	hub := NewHub()
	m := NewManager(st, rec, hub, log, Config{Interval: 50 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return m, hub, cancel
}

func waitStatus(t *testing.T, m *Manager, id, status string) DeviceView {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if v, ok := m.View(id); ok && v.Status == status {
			return v
		}
		time.Sleep(20 * time.Millisecond)
	}
	v, _ := m.View(id)
	t.Fatalf("device %s status %q, want %q (%s)", id, v.Status, status, v.Message)
	return v
}

func TestManagerAddPollRemove(t *testing.T) {
	var failing atomic.Bool
	host := fakeP1(t, &failing)
	m, hub, _ := newManager(t)
	events, cancel := hub.Subscribe()
	defer cancel()

	v, err := m.AddManual(context.Background(), "http://"+host+"/")
	if err != nil {
		t.Fatal(err)
	}
	if v.ID != "5c2faf000001" || v.Kind != "p1" || v.Name != "P1-meter" {
		t.Fatalf("view = %+v", v)
	}
	online := waitStatus(t, m, v.ID, StatusOnline)
	if online.Reading == nil || online.Reading.Values["power_w"] != 321 {
		t.Fatalf("reading = %+v", online.Reading)
	}
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("no live event published")
	}

	// adding the same device again (e.g. via discovery) must not duplicate it
	m.AddDiscovered(context.Background(), Candidate{Host: host, APIVersion: 1, Serial: "5C2FAF000001", ProductType: "HWE-P1", APIEnabled: true})
	if n := len(m.Views()); n != 1 {
		t.Fatalf("%d devices after rediscovery", n)
	}

	failing.Store(true)
	waitStatus(t, m, v.ID, StatusOffline)
	failing.Store(false)

	name := "Meterkast"
	if v, err = m.Update(context.Background(), v.ID, DeviceUpdate{Name: &name}); err != nil || v.Name != name {
		t.Fatalf("rename: %+v %v", v, err)
	}
	on := true
	if v, err = m.Update(context.Background(), v.ID, DeviceUpdate{ShowCounters: &on}); err != nil || !v.ShowCounters || v.Name != name {
		t.Fatalf("show counters: %+v %v", v, err)
	}
	if d, err := m.store.Device(context.Background(), v.ID); err != nil || !d.ShowCounters {
		t.Fatalf("stored show_counters %+v %v", d, err)
	}
	if err := m.Remove(context.Background(), v.ID, true); err != nil {
		t.Fatal(err)
	}
	if len(m.Views()) != 0 {
		t.Fatal("device not removed")
	}
}

func TestAddManualUnreachable(t *testing.T) {
	m, _, _ := newManager(t)
	if _, err := m.AddManual(context.Background(), "127.0.0.1:1"); err == nil {
		t.Fatal("expected an error")
	}
}

func TestNormalizeHost(t *testing.T) {
	for in, want := range map[string]string{
		"192.168.1.5":           "192.168.1.5",
		" http://192.168.1.5/ ": "192.168.1.5",
		"https://p1.local:443":  "p1.local",
		"10.0.0.2:8080/api":     "10.0.0.2:8080",
	} {
		if got, err := NormalizeHost(in); err != nil || got != want {
			t.Errorf("NormalizeHost(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}
