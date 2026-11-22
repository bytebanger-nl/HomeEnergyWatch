// Package auth holds the optional password of the gateway, the sessions of
// the browsers that logged in with it and the API tokens for scripts.
//
// Everything is kept in one JSON file in the data directory (auth.json).
// The password is stored as a PBKDF2 hash; sessions and tokens only as the
// SHA-256 of their secret, so the file never holds anything a client can
// log in with. The file is re-read when it changes on disk, so "hwlocal
// password" works while the gateway runs.
package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Scope is what an API token may do.
type Scope string

const (
	ScopeRead    Scope = "read"    // GET requests only
	ScopeControl Scope = "control" // everything but the security settings
)

const (
	// SessionTTL is how long a session lasts after it was last used.
	SessionTTL = 90 * 24 * time.Hour
	// MinPassword is the shortest password accepted, in characters.
	MinPassword = 8
	maxPassword = 256
	maxSessions = 100
	maxTokens   = 50
	iterations  = 600_000
	// a session's last use is written at most this often
	touchEvery = time.Hour
)

// ErrNotFound is returned for an unknown session or token id.
var ErrNotFound = errors.New("niet gevonden")

// Session describes a logged-in browser, without its secret.
type Session struct {
	ID       string    `json:"id"`
	Created  time.Time `json:"created"`
	LastSeen time.Time `json:"last_seen"`
	Expires  time.Time `json:"expires"`
	Client   string    `json:"client"` // IP address at login
	Agent    string    `json:"agent"`  // browser, shortened
}

// Token describes an API token, without its secret.
type Token struct {
	ID       string     `json:"id"`
	Name     string     `json:"name"`
	Scope    Scope      `json:"scope"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

type session struct {
	Session
	Hash string `json:"hash"`
}

type token struct {
	Token
	Hash string `json:"hash"`
}

type file struct {
	Password string    `json:"password,omitempty"`
	Sessions []session `json:"sessions"`
	Tokens   []token   `json:"tokens"`
}

// Auth is safe for concurrent use.
type Auth struct {
	path string
	// Now is the clock (tests); nil is time.Now.
	Now func() time.Time

	mu    sync.Mutex
	f     file
	mod   time.Time // modification time of the file as last read or written
	fails map[string]*failure
}

// Open reads path; a missing file means no password.
func Open(path string) (*Auth, error) {
	a := &Auth{path: path, fails: map[string]*failure{}}
	if err := a.read(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Auth) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Auth) read() error {
	st, err := os.Stat(a.path)
	if errors.Is(err, os.ErrNotExist) {
		a.f, a.mod = file{}, time.Time{}
		return nil
	}
	if err != nil {
		return err
	}
	b, err := os.ReadFile(a.path)
	if err != nil {
		return err
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return fmt.Errorf("%s: %w", a.path, err)
	}
	if f.Password != "" {
		if _, _, _, err := parseHash(f.Password); err != nil {
			return fmt.Errorf("%s: %w", a.path, err)
		}
	}
	a.f, a.mod = f, st.ModTime()
	return nil
}

// reload re-reads the file when it changed on disk (e.g. by "hwlocal
// password"). It runs before every use: a stat is cheap next to a request,
// and a change made elsewhere is never overwritten by a stale copy. On an
// unreadable file the last good state is kept.
func (a *Auth) reload() {
	st, err := os.Stat(a.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if !a.mod.IsZero() {
			a.f, a.mod = file{}, time.Time{}
		}
	case err == nil && !st.ModTime().Equal(a.mod):
		_ = a.read()
	}
}

func (a *Auth) write() error {
	b, err := json.MarshalIndent(a.f, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("beveiliging opslaan: %w", err)
	}
	if err := os.Rename(tmp, a.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("beveiliging opslaan: %w", err)
	}
	if st, err := os.Stat(a.path); err == nil {
		a.mod = st.ModTime()
	}
	return nil
}

// Enabled reports whether a password is set; without one everything is open.
func (a *Auth) Enabled() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	return a.f.Password != ""
}

// CheckPassword reports whether pw is the password (false without one).
func (a *Auth) CheckPassword(pw string) bool {
	a.mu.Lock()
	a.reload()
	stored := a.f.Password
	a.mu.Unlock()
	if stored == "" {
		return false
	}
	return verify(stored, pw)
}

// ValidPassword checks a new password.
func ValidPassword(pw string) error {
	n := utf8.RuneCountInString(pw)
	switch {
	case n < MinPassword:
		return fmt.Errorf("het wachtwoord moet minstens %d tekens hebben", MinPassword)
	case len(pw) > maxPassword:
		return fmt.Errorf("het wachtwoord mag hoogstens %d tekens hebben", maxPassword)
	case strings.TrimSpace(pw) == "":
		return errors.New("het wachtwoord mag niet alleen uit spaties bestaan")
	}
	return nil
}

// SetPassword sets a new password and ends every session. Tokens stay.
func (a *Auth) SetPassword(pw string) error {
	if err := ValidPassword(pw); err != nil {
		return err
	}
	hash, err := hashPassword(pw)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	a.f.Password, a.f.Sessions = hash, nil
	return a.write()
}

// ClearPassword removes the password and ends every session, which opens the
// gateway to everyone on the network again. Tokens are kept for when a
// password is set again.
func (a *Auth) ClearPassword() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	a.f.Password, a.f.Sessions = "", nil
	return a.write()
}

// NewSession starts a session and returns its secret (for the cookie).
func (a *Auth) NewSession(client, agent string) (string, Session, error) {
	secret, err := randomString(32)
	if err != nil {
		return "", Session{}, err
	}
	id, err := randomString(9)
	if err != nil {
		return "", Session{}, err
	}
	now := a.now().UTC().Truncate(time.Second)
	s := session{Session: Session{ID: id, Created: now, LastSeen: now, Expires: now.Add(SessionTTL),
		Client: client, Agent: shortAgent(agent)}, Hash: digest(secret)}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	a.pruneSessions(now)
	// at most maxSessions: the one used longest ago goes
	for len(a.f.Sessions) >= maxSessions {
		oldest := 0
		for i, x := range a.f.Sessions {
			if x.LastSeen.Before(a.f.Sessions[oldest].LastSeen) {
				oldest = i
			}
		}
		a.f.Sessions = slices.Delete(a.f.Sessions, oldest, oldest+1)
	}
	a.f.Sessions = append(a.f.Sessions, s)
	if err := a.write(); err != nil {
		return "", Session{}, err
	}
	return secret, s.Session, nil
}

func (a *Auth) pruneSessions(now time.Time) {
	a.f.Sessions = slices.DeleteFunc(a.f.Sessions, func(s session) bool { return !now.Before(s.Expires) })
}

// Session returns the live session with this secret and extends it.
func (a *Auth) Session(secret string) (Session, bool) {
	if secret == "" {
		return Session{}, false
	}
	h := digest(secret)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	if a.f.Password == "" {
		return Session{}, false
	}
	now := a.now().UTC()
	for i := range a.f.Sessions {
		s := &a.f.Sessions[i]
		if subtle.ConstantTimeCompare([]byte(s.Hash), []byte(h)) != 1 {
			continue
		}
		if !now.Before(s.Expires) {
			return Session{}, false
		}
		if now.Sub(s.LastSeen) >= touchEvery {
			s.LastSeen = now.Truncate(time.Second)
			s.Expires = s.LastSeen.Add(SessionTTL)
			_ = a.write() // a failed write only shortens the session
		}
		return s.Session, true
	}
	return Session{}, false
}

// Sessions lists the live sessions, most recently used first.
func (a *Auth) Sessions() []Session {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	now := a.now()
	out := []Session{}
	for _, s := range a.f.Sessions {
		if now.Before(s.Expires) {
			out = append(out, s.Session)
		}
	}
	slices.SortFunc(out, func(x, y Session) int { return y.LastSeen.Compare(x.LastSeen) })
	return out
}

// DeleteSession ends a session by id.
func (a *Auth) DeleteSession(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	n := len(a.f.Sessions)
	a.f.Sessions = slices.DeleteFunc(a.f.Sessions, func(s session) bool { return s.ID == id })
	if len(a.f.Sessions) == n {
		return ErrNotFound
	}
	return a.write()
}

// DeleteOtherSessions ends every session but keep (an id).
func (a *Auth) DeleteOtherSessions(keep string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	a.f.Sessions = slices.DeleteFunc(a.f.Sessions, func(s session) bool { return s.ID != keep })
	return a.write()
}

// NewToken makes an API token and returns its secret; it is shown once.
func (a *Auth) NewToken(name string, scope Scope) (string, Token, error) {
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", Token{}, errors.New("geef het token een naam")
	case utf8.RuneCountInString(name) > 60:
		return "", Token{}, errors.New("de naam mag hoogstens 60 tekens hebben")
	case scope != ScopeRead && scope != ScopeControl:
		return "", Token{}, fmt.Errorf("onbekende rechten %q", scope)
	}
	raw, err := randomString(32)
	if err != nil {
		return "", Token{}, err
	}
	secret := "hwl_" + raw
	id, err := randomString(9)
	if err != nil {
		return "", Token{}, err
	}
	t := token{Token: Token{ID: id, Name: name, Scope: scope, Created: a.now().UTC().Truncate(time.Second)}, Hash: digest(secret)}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	if len(a.f.Tokens) >= maxTokens {
		return "", Token{}, fmt.Errorf("maximaal %d tokens", maxTokens)
	}
	a.f.Tokens = append(a.f.Tokens, t)
	if err := a.write(); err != nil {
		return "", Token{}, err
	}
	return secret, t.Token, nil
}

// Token returns the token with this secret and notes its use.
func (a *Auth) Token(secret string) (Token, bool) {
	if !strings.HasPrefix(secret, "hwl_") {
		return Token{}, false
	}
	h := digest(secret)
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	for i := range a.f.Tokens {
		t := &a.f.Tokens[i]
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(h)) != 1 {
			continue
		}
		now := a.now().UTC().Truncate(time.Second)
		if t.LastUsed == nil || now.Sub(*t.LastUsed) >= touchEvery {
			t.LastUsed = &now
			_ = a.write()
		}
		return t.Token, true
	}
	return Token{}, false
}

// Tokens lists the API tokens, oldest first.
func (a *Auth) Tokens() []Token {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	out := make([]Token, 0, len(a.f.Tokens))
	for _, t := range a.f.Tokens {
		out = append(out, t.Token)
	}
	return out
}

// DeleteToken revokes a token by id.
func (a *Auth) DeleteToken(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.reload()
	n := len(a.f.Tokens)
	a.f.Tokens = slices.DeleteFunc(a.f.Tokens, func(t token) bool { return t.ID == id })
	if len(a.f.Tokens) == n {
		return ErrNotFound
	}
	return a.write()
}

// ---------- password hashing ----------

// hashPassword returns "pbkdf2-sha256$<iterations>$<salt>$<key>" (base64).
func hashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, pw, salt, iterations, 32)
	if err != nil {
		return "", err
	}
	enc := base64.RawStdEncoding
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", iterations, enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

func parseHash(s string) (iter int, salt, key []byte, err error) {
	parts := strings.Split(s, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" {
		return 0, nil, nil, errors.New("onbekend wachtwoordformaat")
	}
	iter, err = strconv.Atoi(parts[1])
	if err != nil || iter < 1 || iter > 10_000_000 {
		return 0, nil, nil, errors.New("onbekend wachtwoordformaat")
	}
	enc := base64.RawStdEncoding
	if salt, err = enc.DecodeString(parts[2]); err != nil {
		return 0, nil, nil, errors.New("onbekend wachtwoordformaat")
	}
	if key, err = enc.DecodeString(parts[3]); err != nil || len(key) == 0 {
		return 0, nil, nil, errors.New("onbekend wachtwoordformaat")
	}
	return iter, salt, key, nil
}

func verify(stored, pw string) bool {
	iter, salt, key, err := parseHash(stored)
	if err != nil || len(pw) > maxPassword {
		return false
	}
	got, err := pbkdf2.Key(sha256.New, pw, salt, iter, len(key))
	return err == nil && subtle.ConstantTimeCompare(got, key) == 1
}

func digest(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func randomString(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// shortAgent turns a User-Agent into "Chrome op Android" and the like.
func shortAgent(ua string) string {
	browser := ""
	for _, b := range []struct{ key, name string }{
		{"Edg/", "Edge"}, {"OPR/", "Opera"}, {"Firefox/", "Firefox"}, {"FxiOS/", "Firefox"},
		{"CriOS/", "Chrome"}, {"Chrome/", "Chrome"}, {"Safari/", "Safari"},
	} {
		if strings.Contains(ua, b.key) {
			browser = b.name
			break
		}
	}
	system := ""
	for _, s := range []struct{ key, name string }{
		{"Android", "Android"}, {"iPhone", "iPhone"}, {"iPad", "iPad"}, {"Windows", "Windows"},
		{"Mac OS X", "macOS"}, {"CrOS", "ChromeOS"}, {"Linux", "Linux"},
	} {
		if strings.Contains(ua, s.key) {
			system = s.name
			break
		}
	}
	switch {
	case browser != "" && system != "":
		return browser + " op " + system
	case browser != "":
		return browser
	case system != "":
		return system
	}
	if len(ua) > 40 {
		ua = ua[:40]
	}
	return strings.ToValidUTF8(ua, "")
}
