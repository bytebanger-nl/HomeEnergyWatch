package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type fakeToken struct{ token string }

func (f *fakeToken) Status() TokenStatus { return TokenStatus{Configured: f.token != "", Used: true} }

func (f *fakeToken) Set(_ context.Context, token string) (string, error) {
	if token == "bad" {
		return "", errors.New("ENTSO-E weigert dit token")
	}
	f.token = token
	return "", nil
}

// The token can be set and cleared but is never sent back.
func TestEntsoeTokenSetting(t *testing.T) {
	s := newServer(t)
	tok := &fakeToken{}
	s.Entsoe = tok
	h := s.Handler()
	hdr := map[string]string{"Content-Type": "application/json"}

	w := do(h, "PUT", "/api/settings/entsoe", "192.168.1.20:5000", `{"token":"secret-123"}`, hdr)
	if w.Code != http.StatusOK || tok.token != "secret-123" {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	w = do(h, "GET", "/api/settings/entsoe", "192.168.1.20:5000", "", nil)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "secret") || !strings.Contains(w.Body.String(), `"configured":true`) {
		t.Fatalf("get: %d %s", w.Code, w.Body)
	}
	if w := do(h, "PUT", "/api/settings/entsoe", "192.168.1.20:5000", `{"token":"bad"}`, hdr); w.Code != http.StatusBadRequest {
		t.Fatalf("rejected token: %d", w.Code)
	}
	if w := do(h, "PUT", "/api/settings/entsoe", "8.8.8.8:5000", `{"token":"x"}`, hdr); w.Code != http.StatusForbidden {
		t.Fatalf("from outside the LAN: %d", w.Code)
	}
	w = do(h, "PUT", "/api/settings/entsoe", "192.168.1.20:5000", `{"token":""}`, hdr)
	var st TokenStatus
	if err := decode(w.Body.Bytes(), &st); err != nil || w.Code != http.StatusOK || st.Configured {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
}

func decode(b []byte, v any) error { return json.Unmarshal(b, v) }
