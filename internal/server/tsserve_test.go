package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/tsserve"
)

// fakeServe keeps a serve configuration in memory.
type fakeServe struct {
	st    tsserve.Status
	err   error
	calls []string
}

func (f *fakeServe) Status(context.Context) (tsserve.Status, error) {
	st := f.st
	st.Ports = slices.Clone(f.st.Ports)
	return st, f.err
}

func (f *fakeServe) Serve(_ context.Context, port int, target string) error {
	f.calls = append(f.calls, "serve "+strconv.Itoa(port))
	f.st.Ports = slices.DeleteFunc(f.st.Ports, func(p tsserve.Port) bool { return p.Port == port })
	f.st.Ports = append(f.st.Ports, tsserve.Port{Port: port, Proto: "https", Proxy: target, Targets: []string{"/ → " + target}})
	return nil
}

func (f *fakeServe) Off(_ context.Context, port int) error {
	f.calls = append(f.calls, "off "+strconv.Itoa(port))
	f.st.Ports = slices.DeleteFunc(f.st.Ports, func(p tsserve.Port) bool { return p.Port == port })
	return nil
}

const testTarget = "http://127.0.0.1:8080"

func newServeServer(t *testing.T, f *fakeServe) (*Server, http.Handler) {
	t.Helper()
	s := newServer(t)
	s.Access = newAccess(t)
	s.Serve, s.ServeTarget = f, testTarget
	inUse := map[int]bool{8080: true}
	old := portInUse
	portInUse = func(port int) bool { return inUse[port] }
	t.Cleanup(func() { portInUse = old })
	return s, s.Handler()
}

func runningStatus(ports ...tsserve.Port) tsserve.Status {
	return tsserve.Status{BackendState: "Running", DNSName: "gateway-pc.tail1234.ts.net", HTTPS: true, Ports: ports}
}

func TestTailscaleServePort(t *testing.T) {
	other := tsserve.Port{Port: 443, Proto: "https", Proxy: "http://127.0.0.1:3000", Targets: []string{"/ → http://127.0.0.1:3000"}}
	f := &fakeServe{st: runningStatus(other)}
	s, h := newServeServer(t, f)
	hdr := map[string]string{"Content-Type": "application/json"}
	const lan = "192.168.1.20:5000"

	var st ServeStatus
	w := do(h, "GET", "/api/settings/tailscale-serve", lan, "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusOK {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if !st.Available || len(st.Ports) != 0 || len(st.Used) != 1 || st.Used[0].Port != 443 || st.Target != testTarget {
		t.Fatalf("get: %+v", st)
	}

	// 443 belongs to another app, 8080 to a program on this machine
	for port, want := range map[string]string{"443": "in gebruik door Tailscale Serve: https / → http://127.0.0.1:3000", "8080": "ander programma", "0": "1 tot en met 65535", "70000": "1 tot en met 65535"} {
		w := do(h, "PUT", "/api/settings/tailscale-serve", lan, `{"port":`+port+`}`, hdr)
		if w.Code == http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("port %s: %d %s", port, w.Code, w.Body)
		}
	}
	if len(f.calls) != 0 {
		t.Fatalf("calls after refusals: %q", f.calls)
	}

	// a free port: published, and the MagicDNS name is allowed as host
	w = do(h, "PUT", "/api/settings/tailscale-serve", lan, `{"port":8443}`, hdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"urls":["https://gateway-pc.tail1234.ts.net:8443"]`) {
		t.Fatalf("8443: %d %s", w.Code, w.Body)
	}
	if !s.allowedHost("gateway-pc.tail1234.ts.net:8443") {
		t.Fatal("MagicDNS name not allowed")
	}

	// moving to another port removes the old registration, not the other app
	w = do(h, "PUT", "/api/settings/tailscale-serve", lan, `{"port":9443}`, hdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ports":[9443]`) {
		t.Fatalf("9443: %d %s", w.Code, w.Body)
	}
	if want := []string{"serve 8443", "serve 9443", "off 8443"}; !slices.Equal(f.calls, want) {
		t.Fatalf("calls: %q", f.calls)
	}
	// the same port again is fine (no check against itself)
	if w := do(h, "PUT", "/api/settings/tailscale-serve", lan, `{"port":9443}`, hdr); w.Code != http.StatusOK {
		t.Fatalf("again: %d %s", w.Code, w.Body)
	}

	// over Tailscale it cannot be set, only removed
	const tailnetClient = "100.101.102.103:5000"
	s.AllowTailscale = true
	if w := do(h, "PUT", "/api/settings/tailscale-serve", tailnetClient, `{"port":7443}`, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("over tailscale: %d", w.Code)
	}
	w = do(h, "DELETE", "/api/settings/tailscale-serve", tailnetClient, "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ports":[]`) {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, ok := f.st.Find(443); !ok {
		t.Fatal("other app's port removed")
	}
}

func TestTailscaleServeUnavailable(t *testing.T) {
	hdr := map[string]string{"Content-Type": "application/json"}
	for name, c := range map[string]struct {
		f    *fakeServe
		get  string
		code int
		put  string
	}{
		"no cli":     {&fakeServe{err: tsserve.ErrNoCLI}, "niet geïnstalleerd", http.StatusConflict, "niet geïnstalleerd"},
		"logged out": {&fakeServe{st: tsserve.Status{BackendState: "NeedsLogin"}}, "NeedsLogin", http.StatusConflict, "niet ingelogd"},
		"no https":   {&fakeServe{st: tsserve.Status{BackendState: "Running", DNSName: "pc.ts.net"}}, `"available":true`, http.StatusConflict, "HTTPS Certificates"},
	} {
		_, h := newServeServer(t, c.f)
		if w := do(h, "GET", "/api/settings/tailscale-serve", "192.168.1.20:5000", "", nil); !strings.Contains(w.Body.String(), c.get) {
			t.Errorf("%s get: %s", name, w.Body)
		}
		if w := do(h, "PUT", "/api/settings/tailscale-serve", "192.168.1.20:5000", `{"port":8443}`, hdr); w.Code != c.code || !strings.Contains(w.Body.String(), c.put) {
			t.Errorf("%s put: %d %s", name, w.Code, w.Body)
		}
	}
	// without a target (no listener) the endpoint does not exist
	s := newServer(t)
	if w := do(s.Handler(), "GET", "/api/settings/tailscale-serve", "192.168.1.20:5000", "", nil); w.Code != http.StatusNotFound {
		t.Fatalf("no serve: %d", w.Code)
	}
}

// Errors the user can fix get a hint instead of Tailscale's own text.
func TestTailscaleServeErrorHints(t *testing.T) {
	const windows = `failed to connect to local tailscaled (which appears to be running as tailscaled.exe, pid 7156). Got error: 401 Unauthorized: Tailscale already in use by DESKTOP-EXAMPLE\user, pid 8104`
	f := &fakeServe{err: errors.New(windows)}
	s, h := newServeServer(t, f)
	w := do(h, "GET", "/api/settings/tailscale-serve", "192.168.1.20:5000", "", nil)
	var st ServeStatus
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`(DESKTOP-EXAMPLE\user)`, "tailscale serve --bg --https=<poort> 8080"} {
		if !strings.Contains(st.Problem, want) {
			t.Errorf("windows hint lacks %q: %s", want, st.Problem)
		}
	}
	if w := do(h, "PUT", "/api/settings/tailscale-serve", "192.168.1.20:5000", `{"port":8443}`, map[string]string{"Content-Type": "application/json"}); !strings.Contains(w.Body.String(), "met de hand") {
		t.Errorf("put: %d %s", w.Code, w.Body)
	}

	if got := s.serveError(errors.New("sending serve config: Access denied: serve config denied")); !strings.Contains(got, "--operator=") {
		t.Errorf("linux: %s", got)
	}
	s.ServeTarget = "https+insecure://127.0.0.1:8443"
	if got := s.serveError(errors.New(`Tailscale already in use by PC\x`)); !strings.Contains(got, "--https=<poort> https+insecure://127.0.0.1:8443") {
		t.Errorf("tls target: %s", got)
	}
	if got := s.serveError(errors.New("something else")); got != "something else" {
		t.Errorf("other: %s", got)
	}
}
