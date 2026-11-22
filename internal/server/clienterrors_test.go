package server

import (
	"bytes"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestClientErrorsAreLogged(t *testing.T) {
	s := newServer(t)
	var logs bytes.Buffer
	s.Log = slog.New(slog.NewTextHandler(&logs, nil))
	h := s.Handler()
	body := `{"kind":"error","message":"x is not defined\nsecond line","source":"/app.js","line":12,"column":3,"stack":"at f (/app.js:12:3)","page":"/#/dashboard"}`
	w := do(h, "POST", "/api/client-errors", "192.168.1.20:5000", body, map[string]string{"Content-Type": "application/json"})
	if w.Code != http.StatusNoContent {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", `msg="fout in PWA"`, `melding="x is not defined\nsecond line"`, "regel=12", "bron=/app.js"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	if strings.Count(out, "\n") != 1 {
		t.Errorf("one error should be one log line:\n%s", out)
	}

	if w := do(h, "POST", "/api/client-errors", "192.168.1.20:5000", `{"nope":1}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field: status %d", w.Code)
	}
}

func TestClientErrorBudget(t *testing.T) {
	var b clientErrorBudget
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	for i := range clientErrorLimit {
		if ok, _ := b.take(now); !ok {
			t.Fatalf("error %d refused", i)
		}
	}
	for range 5 {
		if ok, _ := b.take(now.Add(30 * time.Second)); ok {
			t.Fatal("over the limit accepted")
		}
	}
	ok, dropped := b.take(now.Add(time.Minute))
	if !ok || dropped != 5 {
		t.Fatalf("next window: ok %v, dropped %d", ok, dropped)
	}
	if _, dropped := b.take(now.Add(time.Minute)); dropped != 0 {
		t.Fatalf("dropped reported twice: %d", dropped)
	}
}

func TestClip(t *testing.T) {
	if got := clip("  héllo  ", 10); got != "héllo" {
		t.Fatalf("got %q", got)
	}
	if got := clip("ééé", 3); got != "é…" {
		t.Fatalf("got %q", got)
	}
}
