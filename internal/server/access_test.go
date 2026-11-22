package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newAccess(t *testing.T) *AccessSettings {
	t.Helper()
	a, err := LoadAccessSettings(filepath.Join(t.TempDir(), "access.json"))
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Tailscale access can be switched on in the PWA and applies at once.
func TestAccessSettingTailscale(t *testing.T) {
	s := newServer(t)
	s.Access = newAccess(t)
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	const tailnetClient = "100.101.102.103:5000"

	if w := do(h, "GET", "/api/devices", tailnetClient, "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("tailnet client before: %d", w.Code)
	}
	w := do(h, "PUT", "/api/settings/access", "192.168.1.20:5000", `{"tailscale":true,"hosts":[]}`, hdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tailscale":true`) {
		t.Fatalf("switch on: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/devices", tailnetClient, "", nil); w.Code != http.StatusOK {
		t.Fatalf("tailnet client after: %d", w.Code)
	}
	if w := do(h, "GET", "/api/devices", "100.128.0.1:5000", "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("outside tailnet: %d", w.Code)
	}

	// it survives a restart
	again, err := LoadAccessSettings(s.Access.path)
	if on, _ := again.get(); err != nil || !on {
		t.Fatalf("reload: %v %v", on, err)
	}

	// over Tailscale it can be switched off, after which the client is out
	w = do(h, "PUT", "/api/settings/access", tailnetClient, `{"tailscale":false,"hosts":[]}`, hdr)
	if w.Code != http.StatusOK {
		t.Fatalf("switch off over tailscale: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/devices", tailnetClient, "", nil); w.Code != http.StatusForbidden {
		t.Fatalf("tailnet client after switching off: %d", w.Code)
	}
}

// Over Tailscale, remote access can only be narrowed, not widened.
func TestAccessSettingNotWidenedOverTailscale(t *testing.T) {
	s := newServer(t)
	s.Access = newAccess(t)
	s.AllowTailscale = true // option on, so the tailnet client gets in
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	const tailnetClient = "100.101.102.103:5000"

	for _, body := range []string{`{"tailscale":true,"hosts":[]}`, `{"tailscale":false,"hosts":["pc.tail1234.ts.net"]}`} {
		if w := do(h, "PUT", "/api/settings/access", tailnetClient, body, hdr); w.Code != http.StatusForbidden {
			t.Errorf("%s over tailscale: %d", body, w.Code)
		}
	}
	w := do(h, "GET", "/api/settings/access", tailnetClient, "", nil)
	if !strings.Contains(w.Body.String(), `"via_tailscale":true`) || !strings.Contains(w.Body.String(), `"tailscale_from_option":true`) {
		t.Fatalf("status: %s", w.Body)
	}
	// the option cannot be switched off from the PWA
	w = do(h, "PUT", "/api/settings/access", "192.168.1.20:5000", `{"tailscale":false,"hosts":[]}`, hdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"tailscale":true`) {
		t.Fatalf("option stays on: %d %s", w.Code, w.Body)
	}
}

// Host names added in the PWA pass the DNS-rebinding check.
func TestAccessSettingHosts(t *testing.T) {
	s := newServer(t)
	s.Access = newAccess(t)
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}
	get := func(host string) int {
		req, _ := http.NewRequest("GET", "/api/devices", nil)
		req.Host, req.RemoteAddr = host, "192.168.1.20:5000"
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w.Code
	}
	if c := get("pc.tail1234.ts.net"); c != http.StatusMisdirectedRequest {
		t.Fatalf("before: %d", c)
	}
	w := do(h, "PUT", "/api/settings/access", "192.168.1.20:5000", `{"tailscale":false,"hosts":[" PC.tail1234.ts.net. ","pc.tail1234.ts.net"]}`, hdr)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"hosts":["pc.tail1234.ts.net"]`) {
		t.Fatalf("set hosts: %d %s", w.Code, w.Body)
	}
	if c := get("pc.tail1234.ts.net:8080"); c != http.StatusOK {
		t.Fatalf("after: %d", c)
	}
	for _, bad := range []string{`["*"]`, `["evil example"]`, `["1.2.3.4"]`, `["-x.example"]`} {
		if w := do(h, "PUT", "/api/settings/access", "192.168.1.20:5000", `{"tailscale":false,"hosts":`+bad+`}`, hdr); w.Code != http.StatusBadRequest {
			t.Errorf("hosts %s: %d", bad, w.Code)
		}
	}
}

// A damaged settings file leaves remote access off.
func TestAccessSettingsDamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "access.json")
	if err := os.WriteFile(path, []byte(`{"tailscale":tru`), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAccessSettings(path)
	if on, _ := a.get(); err == nil || on {
		t.Fatalf("damaged file: on=%v err=%v", on, err)
	}
}
