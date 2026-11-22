package server

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/auth"
)

func authServer(t *testing.T) (http.Handler, *auth.Auth) {
	t.Helper()
	s := newServer(t)
	a, err := auth.Open(filepath.Join(t.TempDir(), "auth.json"))
	if err != nil {
		t.Fatal(err)
	}
	s.Auth = a
	return s.Handler(), a
}

const lan = "192.168.1.20:5000"

func sessionCookieOf(t *testing.T, resp http.Header) string {
	t.Helper()
	for _, c := range (&http.Response{Header: resp}).Cookies() {
		if c.Name == sessionCookie && c.Value != "" {
			if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
				t.Errorf("cookie %+v not HttpOnly/Strict", c)
			}
			return sessionCookie + "=" + c.Value
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func status(t *testing.T, h http.Handler, cookie string) AuthStatus {
	t.Helper()
	w := do(h, "GET", "/api/auth", lan, "", map[string]string{"Cookie": cookie})
	var st AuthStatus
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &st) != nil {
		t.Fatalf("status: %d %s", w.Code, w.Body)
	}
	return st
}

func TestLogin(t *testing.T) {
	h, a := authServer(t)
	// without a password everything is open, as before
	if w := do(h, "GET", "/api/devices", lan, "", nil); w.Code != http.StatusOK {
		t.Fatalf("open: %d", w.Code)
	}
	if st := status(t, h, ""); !st.Available || st.Enabled || !st.Authenticated {
		t.Fatalf("status without password %+v", st)
	}

	// setting the first password needs no current one and logs this browser in
	if w := do(h, "PUT", "/api/auth/password", lan, `{"password":"kort"}`, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("short password: %d", w.Code)
	}
	w := do(h, "PUT", "/api/auth/password", lan, `{"password":"geheim wachtwoord"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	mine := sessionCookieOf(t, w.Header())

	// now the API needs a login; the PWA's files and the login do not
	w = do(h, "GET", "/api/devices", lan, "", nil)
	if w.Code != http.StatusUnauthorized || !strings.Contains(w.Body.String(), `"login":true`) {
		t.Fatalf("without login: %d %s", w.Code, w.Body)
	}
	if w := do(h, "GET", "/api/stream", lan, "", nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("stream without login: %d", w.Code)
	}
	if w := do(h, "GET", "/", lan, "", nil); w.Code != http.StatusOK {
		t.Fatalf("PWA: %d", w.Code)
	}
	if st := status(t, h, ""); !st.Enabled || st.Authenticated {
		t.Fatalf("status without cookie %+v", st)
	}
	if w := do(h, "GET", "/api/devices", lan, "", map[string]string{"Cookie": mine}); w.Code != http.StatusOK {
		t.Fatalf("with cookie: %d", w.Code)
	}

	// log in from another browser
	if w := do(h, "POST", "/api/auth/login", lan, `{"password":"fout"}`, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	}
	w = do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	other := sessionCookieOf(t, w.Header())
	if st := status(t, h, other); !st.Authenticated || st.Session == "" {
		t.Fatalf("status after login %+v", st)
	}

	// the session list marks this browser
	w = do(h, "GET", "/api/auth/sessions", lan, "", map[string]string{"Cookie": mine})
	var sessions []struct {
		ID      string
		Current bool
	}
	if json.Unmarshal(w.Body.Bytes(), &sessions) != nil || len(sessions) != 2 {
		t.Fatalf("sessions: %s", w.Body)
	}
	current := 0
	for _, s := range sessions {
		if s.Current {
			current++
		}
	}
	if current != 1 {
		t.Fatalf("current sessions: %d", current)
	}

	// changing the password needs the current one and logs out the others
	if w := do(h, "PUT", "/api/auth/password", lan, `{"current":"fout","password":"nieuw wachtwoord"}`, map[string]string{"Cookie": mine}); w.Code != http.StatusUnauthorized {
		t.Fatalf("change with wrong current: %d", w.Code)
	}
	w = do(h, "PUT", "/api/auth/password", lan, `{"current":"geheim wachtwoord","password":"nieuw wachtwoord"}`, map[string]string{"Cookie": mine})
	if w.Code != http.StatusOK {
		t.Fatalf("change: %d %s", w.Code, w.Body)
	}
	mine = sessionCookieOf(t, w.Header())
	if w := do(h, "GET", "/api/devices", lan, "", map[string]string{"Cookie": other}); w.Code != http.StatusUnauthorized {
		t.Fatalf("other browser after change: %d", w.Code)
	}
	if w := do(h, "GET", "/api/devices", lan, "", map[string]string{"Cookie": mine}); w.Code != http.StatusOK {
		t.Fatalf("this browser after change: %d", w.Code)
	}

	// log out
	if w := do(h, "POST", "/api/auth/logout", lan, "", map[string]string{"Cookie": mine}); w.Code != http.StatusNoContent {
		t.Fatalf("logout: %d", w.Code)
	}
	if w := do(h, "GET", "/api/devices", lan, "", map[string]string{"Cookie": mine}); w.Code != http.StatusUnauthorized {
		t.Fatalf("after logout: %d", w.Code)
	}

	// switching it off needs the password
	w = do(h, "POST", "/api/auth/login", lan, `{"password":"nieuw wachtwoord"}`, nil)
	mine = sessionCookieOf(t, w.Header())
	if w := do(h, "DELETE", "/api/auth/password", lan, `{"current":"fout"}`, map[string]string{"Cookie": mine}); w.Code != http.StatusUnauthorized {
		t.Fatalf("off with wrong password: %d", w.Code)
	}
	if w := do(h, "DELETE", "/api/auth/password", lan, `{"current":"nieuw wachtwoord"}`, map[string]string{"Cookie": mine}); w.Code != http.StatusOK {
		t.Fatalf("off: %d %s", w.Code, w.Body)
	}
	if a.Enabled() {
		t.Fatal("still enabled")
	}
	if w := do(h, "GET", "/api/devices", lan, "", nil); w.Code != http.StatusOK {
		t.Fatalf("open again: %d", w.Code)
	}
}

func TestLoginAttemptsLimited(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		if w := do(h, "POST", "/api/auth/login", lan, `{"password":"fout"}`, nil); w.Code != http.StatusUnauthorized {
			t.Fatalf("try %d: %d", i, w.Code)
		}
	}
	// now even the right password has to wait
	if w := do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("after 5 wrong: %d %s", w.Code, w.Body)
	}
	// another address is not affected
	if w := do(h, "POST", "/api/auth/login", "192.168.1.21:5000", `{"password":"geheim wachtwoord"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("other client: %d", w.Code)
	}
}

// After the wait has passed, the right password logs in again.
func TestLoginAfterWait(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	a.Now = func() time.Time { return now }
	for range 5 {
		do(h, "POST", "/api/auth/login", lan, `{"password":"fout"}`, nil)
	}
	if w := do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, nil); w.Code != http.StatusTooManyRequests {
		t.Fatalf("not locked out: %d", w.Code)
	}
	now = now.Add(2 * time.Minute)
	if w := do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, nil); w.Code != http.StatusOK {
		t.Fatalf("after the wait: %d %s", w.Code, w.Body)
	}
}

func TestTokens(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	w := do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, nil)
	cookie := map[string]string{"Cookie": sessionCookieOf(t, w.Header())}
	token := func(scope string) string {
		t.Helper()
		w := do(h, "POST", "/api/auth/tokens", lan, `{"name":"script","scope":"`+scope+`"}`, cookie)
		var tok struct{ ID, Secret string }
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &tok) != nil || tok.Secret == "" {
			t.Fatalf("token: %d %s", w.Code, w.Body)
		}
		return tok.Secret
	}
	read, control := token("read"), token("control")
	bearer := func(s string) map[string]string { return map[string]string{"Authorization": "Bearer " + s} }

	if w := do(h, "GET", "/api/devices", lan, "", bearer(read)); w.Code != http.StatusOK {
		t.Fatalf("read token GET: %d", w.Code)
	}
	if w := do(h, "PUT", "/api/devices/x/power", lan, `{"on":true}`, bearer(read)); w.Code != http.StatusForbidden {
		t.Fatalf("read token PUT: %d", w.Code)
	}
	// a control token passes the login; the handler answers (unknown device)
	if w := do(h, "PUT", "/api/devices/x/power", lan, `{"on":true}`, bearer(control)); w.Code == http.StatusUnauthorized || w.Code == http.StatusForbidden {
		t.Fatalf("control token PUT: %d %s", w.Code, w.Body)
	}
	// no token reaches the security settings
	for _, tok := range []string{read, control} {
		if w := do(h, "GET", "/api/auth/tokens", lan, "", bearer(tok)); w.Code != http.StatusForbidden {
			t.Fatalf("token on /api/auth/tokens: %d", w.Code)
		}
		if w := do(h, "PUT", "/api/auth/password", lan, `{"current":"x","password":"yyyyyyyyy"}`, bearer(tok)); w.Code != http.StatusForbidden {
			t.Fatalf("token on /api/auth/password: %d", w.Code)
		}
	}
	if w := do(h, "GET", "/api/devices", lan, "", bearer("hwl_wrong")); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: %d", w.Code)
	}

	// revoke
	w = do(h, "GET", "/api/auth/tokens", lan, "", cookie)
	var list []auth.Token
	if json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list) != 2 {
		t.Fatalf("tokens: %s", w.Body)
	}
	if w := do(h, "DELETE", "/api/auth/tokens/"+list[0].ID, lan, "", cookie); w.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := do(h, "GET", "/api/devices", lan, "", bearer(read)); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", w.Code)
	}
}

// A running event stream ends once its session does.
func TestStreamEndsWithSession(t *testing.T) {
	old := streamCheck
	streamCheck = 20 * time.Millisecond
	t.Cleanup(func() { streamCheck = old })
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	w := do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, nil)
	cookie := sessionCookieOf(t, w.Header())
	done := make(chan int)
	go func() {
		w := do(h, "GET", "/api/stream", lan, "", map[string]string{"Cookie": cookie})
		done <- w.Code
	}()
	select {
	case <-done:
		t.Fatal("stream ended while logged in")
	case <-time.After(100 * time.Millisecond):
	}
	if err := a.SetPassword("nieuw wachtwoord"); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != http.StatusOK {
			t.Fatalf("stream: %d", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream kept running after the session ended")
	}
}

// A request body is one JSON value: anything after it is refused.
func TestTrailingJSONRefused(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{
		`{"password":"geheim wachtwoord"}{"password":"fout"}`,
		`{"password":"geheim wachtwoord"} rest`,
	} {
		if w := do(h, "POST", "/api/auth/login", lan, body, nil); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	if w := do(h, "POST", "/api/auth/login", lan, "{\"password\":\"geheim wachtwoord\"}\n", nil); w.Code != http.StatusOK {
		t.Fatalf("trailing newline: %d %s", w.Code, w.Body)
	}
}
