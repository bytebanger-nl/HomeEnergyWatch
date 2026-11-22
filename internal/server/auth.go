package server

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/auth"
)

// sessionCookie holds the secret of a browser's session.
const sessionCookie = "hwlocal_session"

type sessionKey struct{}

// sessionOf returns the session of a request that logged in with a cookie.
func sessionOf(r *http.Request) (auth.Session, bool) {
	s, ok := r.Context().Value(sessionKey{}).(auth.Session)
	return s, ok
}

// publicAPI are the endpoints that work without logging in: the login itself.
func publicAPI(r *http.Request) bool {
	switch r.URL.Path {
	case "/api/auth":
		return r.Method == http.MethodGet || r.Method == http.MethodHead
	case "/api/auth/login", "/api/auth/logout":
		return r.Method == http.MethodPost
	}
	return false
}

// requireLogin protects /api/ once a password is set: a request needs the
// session cookie of a browser that logged in, or an API token (header
// "Authorization: Bearer hwl_..."). A read token only gets GET requests;
// no token reaches the security settings under /api/auth. The PWA's own
// files stay public, so it can show the login screen.
func (s *Server) requireLogin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/") || !s.Auth.Enabled() || publicAPI(r) {
			next.ServeHTTP(w, r)
			return
		}
		if bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			tok, ok := s.Auth.Token(strings.TrimSpace(bearer))
			switch {
			case !ok:
				writeError(w, http.StatusUnauthorized, "onbekend of ingetrokken token")
			case strings.HasPrefix(r.URL.Path, "/api/auth"):
				writeError(w, http.StatusForbidden, "beveiligingsinstellingen kunnen niet met een token worden gewijzigd")
			case tok.Scope == auth.ScopeRead && r.Method != http.MethodGet && r.Method != http.MethodHead:
				writeError(w, http.StatusForbidden, "dit token mag alleen lezen")
			default:
				secret := strings.TrimSpace(bearer)
				next.ServeHTTP(w, s.endWithLogin(r, func() bool { _, ok := s.Auth.Token(secret); return ok }))
			}
			return
		}
		if c, err := r.Cookie(sessionCookie); err == nil {
			if sess, ok := s.Auth.Session(c.Value); ok {
				r = s.endWithLogin(r, func() bool { _, ok := s.Auth.Session(c.Value); return ok })
				next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), sessionKey{}, sess)))
				return
			}
		}
		writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "log in om verder te gaan", "login": true})
	})
}

// streamCheck is how often a running event stream checks its login.
var streamCheck = 30 * time.Second

// endWithLogin ends the event stream (a request that lasts) once its login
// is no longer valid: logged out elsewhere, new password, token revoked, or
// the password removed and set again. Other requests are short and return r.
func (s *Server) endWithLogin(r *http.Request, valid func() bool) *http.Request {
	if r.URL.Path != "/api/stream" {
		return r
	}
	ctx, cancel := context.WithCancel(r.Context())
	go func() {
		defer cancel()
		t := time.NewTicker(streamCheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if s.Auth.Enabled() && !valid() {
					return
				}
			}
		}
	}()
	return r.WithContext(ctx)
}

func (s *Server) routeAuth(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/auth", s.authStatus)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("PUT /api/auth/password", s.setPassword)
	mux.HandleFunc("DELETE /api/auth/password", s.clearPassword)
	mux.HandleFunc("GET /api/auth/sessions", s.listSessions)
	mux.HandleFunc("DELETE /api/auth/sessions/{id}", s.deleteSession)
	mux.HandleFunc("GET /api/auth/tokens", s.listTokens)
	mux.HandleFunc("POST /api/auth/tokens", s.createToken)
	mux.HandleFunc("DELETE /api/auth/tokens/{id}", s.deleteToken)
}

// AuthStatus is returned by GET /api/auth.
type AuthStatus struct {
	// Available: this gateway can have a password (Server.Auth is set).
	Available bool `json:"available"`
	// Enabled: a password is set.
	Enabled bool `json:"enabled"`
	// Authenticated: this request may use the API (always without password).
	Authenticated bool `json:"authenticated"`
	// Session is the id of this browser's session, if logged in.
	Session string `json:"session,omitempty"`
	// Secure: the request came over https (the password is not sent in the
	// clear); behind tailscale serve the gateway cannot tell.
	Secure bool `json:"secure"`
}

func (s *Server) status(r *http.Request) AuthStatus {
	st := AuthStatus{Available: s.Auth != nil, Enabled: s.Auth.Enabled(), Secure: r.TLS != nil}
	if !st.Enabled {
		st.Authenticated = true
		return st
	}
	if sess, ok := sessionOf(r); ok {
		st.Authenticated, st.Session = true, sess.ID
	} else if c, err := r.Cookie(sessionCookie); err == nil {
		if sess, ok := s.Auth.Session(c.Value); ok {
			st.Authenticated, st.Session = true, sess.ID
		}
	}
	return st
}

func (s *Server) authStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.status(r))
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// limitKeys are the keys a password attempt counts against. Behind a proxy
// on this machine every request comes from loopback: tailscale serve puts
// the real client in X-Forwarded-For (it replaces whatever the client sent),
// so each client gets its own limit, and all of them also count against a
// larger shared one for the proxy, in case a proxy passes on a header the
// client chose.
func limitKeys(r *http.Request) []string {
	host := clientIP(r)
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return []string{host}
	}
	if xff := r.Header.Values("X-Forwarded-For"); len(xff) > 0 {
		parts := strings.Split(xff[len(xff)-1], ",")
		if fwd := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(fwd) != nil {
			return []string{fwd, auth.ProxyPrefix + host}
		}
	}
	return []string{host}
}

// checkPassword checks pw for this client under the attempt limit and
// writes the error response when it is refused.
func (s *Server) checkPassword(w http.ResponseWriter, r *http.Request, pw string) bool {
	keys := limitKeys(r)
	if wait := s.Auth.Begin(keys...); wait > 0 {
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("te veel onjuiste pogingen; probeer het over %s opnieuw", waitText(wait)))
		return false
	}
	ok := s.Auth.CheckPassword(pw)
	s.Auth.Finish(ok, keys...)
	if !ok {
		s.Log.Warn("onjuist wachtwoord", "client", r.RemoteAddr, "for", keys[0])
		writeError(w, http.StatusUnauthorized, "onjuist wachtwoord")
		return false
	}
	return true
}

func waitText(d time.Duration) string {
	if m := int(math.Ceil(d.Minutes())); m > 1 {
		return fmt.Sprintf("%d minuten", m)
	}
	return "een minuut"
}

// startSession logs this browser in: a new session and its cookie.
func (s *Server) startSession(w http.ResponseWriter, r *http.Request) (auth.Session, bool) {
	secret, sess, err := s.Auth.NewSession(limitKeys(r)[0], r.UserAgent())
	if err != nil {
		s.Log.Error("sessie", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return auth.Session{}, false
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: secret, Path: "/",
		MaxAge:   int((400 * 24 * time.Hour).Seconds()), // the session itself ends unused after auth.SessionTTL
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
	return sess, true
}

func clearCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func (s *Server) needAuth(w http.ResponseWriter) bool {
	if s.Auth == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return false
	}
	return true
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.Auth.Enabled() {
		writeError(w, http.StatusBadRequest, "er is geen wachtwoord ingesteld")
		return
	}
	if !s.checkPassword(w, r, body.Password) {
		return
	}
	sess, ok := s.startSession(w, r)
	if !ok {
		return
	}
	s.Log.Info("ingelogd", "client", r.RemoteAddr, "browser", sess.Agent)
	writeJSON(w, http.StatusOK, AuthStatus{Available: true, Enabled: true, Authenticated: true, Session: sess.ID, Secure: r.TLS != nil})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	if c, err := r.Cookie(sessionCookie); err == nil {
		if sess, ok := s.Auth.Session(c.Value); ok {
			_ = s.Auth.DeleteSession(sess.ID)
		}
	}
	clearCookie(w, r)
	w.WriteHeader(http.StatusNoContent)
}

// setPassword sets the first password (no current one needed: without a
// password everyone may change everything) or changes it. Either way every
// other browser has to log in again; this one gets a new session.
func (s *Server) setPassword(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	var body struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := auth.ValidPassword(body.Password); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	first := !s.Auth.Enabled()
	if !first && !s.checkPassword(w, r, body.Current) {
		return
	}
	if err := s.Auth.SetPassword(body.Password); err != nil {
		s.Log.Error("wachtwoord", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if first {
		s.Log.Info("wachtwoord ingesteld", "client", r.RemoteAddr)
	} else {
		s.Log.Info("wachtwoord gewijzigd; alle sessies beëindigd", "client", r.RemoteAddr)
	}
	sess, ok := s.startSession(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, AuthStatus{Available: true, Enabled: true, Authenticated: true, Session: sess.ID, Secure: r.TLS != nil})
}

// clearPassword removes the password, after checking it once more.
func (s *Server) clearPassword(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	var body struct {
		Current string `json:"current"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if !s.Auth.Enabled() {
		writeJSON(w, http.StatusOK, s.status(r))
		return
	}
	if !s.checkPassword(w, r, body.Current) {
		return
	}
	if err := s.Auth.ClearPassword(); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Warn("wachtwoord uitgezet: de app is weer open voor iedereen op het netwerk", "client", r.RemoteAddr)
	clearCookie(w, r)
	writeJSON(w, http.StatusOK, AuthStatus{Available: true, Authenticated: true, Secure: r.TLS != nil})
}

func (s *Server) listSessions(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	current, _ := sessionOf(r)
	type item struct {
		auth.Session
		Current bool `json:"current"`
	}
	out := []item{}
	for _, sess := range s.Auth.Sessions() {
		out = append(out, item{sess, sess.ID == current.ID})
	}
	writeJSON(w, http.StatusOK, out)
}

// deleteSession logs out one browser; id "others" logs out all but this one.
func (s *Server) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	id := r.PathValue("id")
	var err error
	if id == "others" {
		current, _ := sessionOf(r)
		err = s.Auth.DeleteOtherSessions(current.ID)
	} else {
		err = s.Auth.DeleteSession(id)
	}
	switch {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, http.StatusNotFound, "sessie niet gevonden")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		s.Log.Info("uitgelogd op afstand", "session", id, "client", r.RemoteAddr)
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.Auth.Tokens())
}

// createToken returns the new token's secret once; it cannot be read later.
func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	var body struct {
		Name  string     `json:"name"`
		Scope auth.Scope `json:"scope"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	secret, tok, err := s.Auth.NewToken(body.Name, body.Scope)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.Log.Info("API-token gemaakt", "name", tok.Name, "scope", tok.Scope, "client", r.RemoteAddr)
	writeJSON(w, http.StatusCreated, struct {
		auth.Token
		Secret string `json:"secret"`
	}{tok, secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	if !s.needAuth(w) {
		return
	}
	switch err := s.Auth.DeleteToken(r.PathValue("id")); {
	case errors.Is(err, auth.ErrNotFound):
		writeError(w, http.StatusNotFound, "token niet gevonden")
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		s.Log.Info("API-token ingetrokken", "id", r.PathValue("id"), "client", r.RemoteAddr)
		w.WriteHeader(http.StatusNoContent)
	}
}
