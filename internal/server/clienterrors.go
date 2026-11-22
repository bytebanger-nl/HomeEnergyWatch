package server

import (
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// clientErrorLimit caps how many PWA errors are logged per minute, so a
// page stuck in an error loop (or a client on the LAN) cannot flood the log.
const clientErrorLimit = 30

// clientError is what the PWA sends to POST /api/client-errors when a script
// error or an unhandled promise rejection reaches the window.
type clientError struct {
	Kind    string `json:"kind"` // "error" or "rejection"
	Message string `json:"message"`
	Source  string `json:"source"`
	Line    int    `json:"line"`
	Column  int    `json:"column"`
	Stack   string `json:"stack"`
	Page    string `json:"page"`
}

// clientErrorBudget is a fixed one-minute window shared by all clients.
type clientErrorBudget struct {
	mu      sync.Mutex
	start   time.Time
	n       int
	dropped int
}

// take reports whether one more error may be logged now, and how many were
// dropped in the previous window (to log once when a new window opens).
func (b *clientErrorBudget) take(now time.Time) (ok bool, dropped int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.start) >= time.Minute {
		dropped, b.dropped = b.dropped, 0
		b.start, b.n = now, 0
	}
	if b.n >= clientErrorLimit {
		b.dropped++
		return false, dropped
	}
	b.n++
	return true, dropped
}

// clientErrors logs an error reported by the PWA, so errors in a browser
// (on a phone, without developer tools) end up in the gateway's log.
func (s *Server) clientErrors(w http.ResponseWriter, r *http.Request) {
	var e clientError
	r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
	if !readJSON(w, r, &e) {
		return
	}
	ok, dropped := s.clientErrorBudget.take(time.Now())
	if dropped > 0 {
		s.Log.Warn("PWA-fouten niet gelogd (te veel per minuut)", "aantal", dropped)
	}
	if ok {
		kind := "error"
		if e.Kind == "rejection" {
			kind = "rejection"
		}
		s.Log.Warn("fout in PWA",
			"soort", kind,
			"melding", clip(e.Message, 500),
			"bron", clip(e.Source, 300),
			"regel", e.Line, "kolom", e.Column,
			"pagina", clip(e.Page, 300),
			"client", r.RemoteAddr,
			"browser", clip(r.UserAgent(), 200),
			"stack", clip(e.Stack, 4000))
	}
	w.WriteHeader(http.StatusNoContent)
}

// clip shortens s to at most n bytes without splitting a UTF-8 character.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}
