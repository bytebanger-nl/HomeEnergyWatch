package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

func newServer(t *testing.T) *Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := history.NewRecorder(st, log, history.Config{Location: time.UTC})
	hub := gateway.NewHub()
	mgr := gateway.NewManager(st, rec, hub, log, gateway.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return &Server{Manager: mgr, Recorder: rec, Hub: hub, Log: log,
		Web: fstest.MapFS{"index.html": {Data: []byte("<!doctype html>hi")}}}
}

func do(h http.Handler, method, target, remote string, body string, headers map[string]string) *httptest.ResponseRecorder {
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	r.RemoteAddr = remote
	r.Host = "hwlocal.local:8080" // httptest's default example.com is a public name
	for k, v := range headers {
		r.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestOnlyLocalClients(t *testing.T) {
	h := newServer(t).Handler()
	if w := do(h, "GET", "/api/devices", "192.168.1.20:5000", "", nil); w.Code != 200 {
		t.Fatalf("LAN client: %d", w.Code)
	}
	if w := do(h, "GET", "/api/devices", "127.0.0.1:5000", "", nil); w.Code != 200 {
		t.Fatalf("loopback client: %d", w.Code)
	}
	if w := do(h, "GET", "/api/devices", "8.8.8.8:5000", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("public client: %d", w.Code)
	}
}

// Tailscale addresses (100.64.0.0/10) are shared with carrier-grade NAT, so
// they are only accepted with AllowTailscale.
func TestTailscaleClients(t *testing.T) {
	s := newServer(t)
	if w := do(s.Handler(), "GET", "/api/devices", "100.101.102.103:5000", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("tailnet client without option: %d", w.Code)
	}
	s.AllowTailscale = true
	h := s.Handler()
	for _, remote := range []string{"100.101.102.103:5000", "[::ffff:100.64.0.1]:5000", "[fd7a:115c:a1e0::1]:5000"} {
		if w := do(h, "GET", "/api/devices", remote, "", nil); w.Code != 200 {
			t.Fatalf("tailnet client %s: %d", remote, w.Code)
		}
	}
	for _, remote := range []string{"100.128.0.1:5000", "8.8.8.8:5000"} {
		if w := do(h, "GET", "/api/devices", remote, "", nil); w.Code != http.StatusForbidden {
			t.Fatalf("outside tailnet %s: %d", remote, w.Code)
		}
	}
}

func TestCrossSiteWriteRejected(t *testing.T) {
	h := newServer(t).Handler()
	w := do(h, "POST", "/api/devices", "192.168.1.20:5000", `{"host":"127.0.0.1:1"}`, map[string]string{
		"Content-Type": "application/json", "Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST: %d", w.Code)
	}
}

// DNS rebinding: a website whose domain resolves to the gateway's LAN address
// makes same-origin requests from the user's browser. Only host names the
// gateway can legitimately be reached by are accepted.
func TestUnknownHostRejected(t *testing.T) {
	s := newServer(t)
	s.AllowedHosts = []string{"energie.example.nl"}
	h := s.Handler()
	for _, host := range []string{"evil.example", "energie.example.nl.evil.example", "192.168.1.20.nip.io"} {
		r := httptest.NewRequest("PUT", "/api/devices/x/power", strings.NewReader(`{"on":true}`))
		r.Host, r.RemoteAddr = host, "192.168.1.20:5000"
		r.Header.Set("Sec-Fetch-Site", "same-origin")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusMisdirectedRequest {
			t.Errorf("host %q: %d, want 421", host, w.Code)
		}
	}
	for _, host := range []string{"192.168.1.20:8080", "[fe80::1]:8080", "localhost:8080", "raspberrypi", "hwlocal.local",
		"gateway.fritz.box", "pi.home.arpa", "energie.example.nl", "ENERGIE.example.nl:8443"} {
		r := httptest.NewRequest("GET", "/api/devices", nil)
		r.Host, r.RemoteAddr = host, "192.168.1.20:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Errorf("host %q: %d, want 200", host, w.Code)
		}
	}
}

// A command to an unknown device is refused before anything is sent.
func TestPowerUnknownDevice(t *testing.T) {
	h := newServer(t).Handler()
	w := do(h, "PUT", "/api/devices/nope/power", "192.168.1.20:5000", `{"on":true}`, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown device: %d %s", w.Code, w.Body.String())
	}
}

// Management actions from outside the LAN are refused, also for contracts.
func TestManagementRequiresLocalSameOrigin(t *testing.T) {
	h := newServer(t).Handler()
	body := `{"name":"x","type":"dynamic","start":"2026-01-01"}`
	if w := do(h, "POST", "/api/contracts", "8.8.8.8:5000", body, map[string]string{"Content-Type": "application/json"}); w.Code != http.StatusForbidden {
		t.Fatalf("public client: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/contracts/1", "192.168.1.20:5000", "", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}); w.Code != http.StatusForbidden {
		t.Fatalf("cross-site delete: %d", w.Code)
	}
}

// API v2 tokens stay in the database: the device list never contains them.
func TestDeviceListHasNoToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const secret = "SECRETTOKEN0123456789ABCDEF0123"
	now := time.Now()
	if err := st.SaveDevice(context.Background(), store.Device{ID: "bat", Serial: "aabbccddeeff", ProductType: "HWE-BAT",
		Host: "127.0.0.1:1", V2Host: "127.0.0.1:1", APIVersion: 2, Token: secret, V2Available: true, Enabled: true,
		CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	rec := history.NewRecorder(st, log, history.Config{Location: time.UTC})
	mgr := gateway.NewManager(st, rec, gateway.NewHub(), log, gateway.Config{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := mgr.Start(ctx); err != nil {
		t.Fatal(err)
	}
	h := (&Server{Manager: mgr, Recorder: rec, Hub: gateway.NewHub(), Log: log, Web: fstest.MapFS{}}).Handler()
	w := do(h, "GET", "/api/devices", "127.0.0.1:1", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"paired":true`) {
		t.Fatalf("devices: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Fatal("token in device list")
	}
}

func TestHistoryValidation(t *testing.T) {
	h := newServer(t).Handler()
	if w := do(h, "GET", "/api/history/energy?device=nope", "127.0.0.1:1", "", nil); w.Code != 404 {
		t.Fatalf("unknown device: %d", w.Code)
	}
}

func TestStaticAndHeaders(t *testing.T) {
	h := newServer(t).Handler()
	w := do(h, "GET", "/", "127.0.0.1:1", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "hi") {
		t.Fatalf("index: %d %q", w.Code, w.Body.String())
	}
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("missing CSP")
	}
	if w := do(h, "GET", "/ca.crt", "127.0.0.1:1", "", nil); w.Code != 404 {
		t.Fatalf("ca.crt without selfsigned mode: %d", w.Code)
	}
}
