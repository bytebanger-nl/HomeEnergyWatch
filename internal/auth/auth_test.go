package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) (*Auth, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	a, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	return a, path
}

func TestPasswordAndSessions(t *testing.T) {
	a, path := open(t)
	if a.Enabled() || a.CheckPassword("") {
		t.Fatal("enabled without password")
	}
	if err := a.SetPassword("kort"); err == nil {
		t.Fatal("short password accepted")
	}
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	if !a.Enabled() || !a.CheckPassword("geheim wachtwoord") || a.CheckPassword("geheim") {
		t.Fatal("password check")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), "geheim") {
		t.Fatal("password stored in clear")
	}

	secret, s, err := a.NewSession("192.168.1.5", "Mozilla/5.0 (Linux; Android 14) Chrome/130.0 Mobile Safari/537.36")
	if err != nil {
		t.Fatal(err)
	}
	if s.Agent != "Chrome op Android" {
		t.Errorf("agent %q", s.Agent)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), secret) {
		t.Fatal("session secret stored in clear")
	}
	if got, ok := a.Session(secret); !ok || got.ID != s.ID {
		t.Fatal("session not found")
	}
	if _, ok := a.Session(secret + "x"); ok {
		t.Fatal("wrong secret accepted")
	}

	// a session extends while used and ends unused after SessionTTL
	now := time.Now()
	a.Now = func() time.Time { return now.Add(SessionTTL - time.Hour) }
	if _, ok := a.Session(secret); !ok {
		t.Fatal("session ended early")
	}
	a.Now = func() time.Time { return now.Add(2*SessionTTL - 3*time.Hour) }
	if _, ok := a.Session(secret); !ok {
		t.Fatal("used session not extended")
	}
	a.Now = func() time.Time { return now.Add(4 * SessionTTL) }
	if _, ok := a.Session(secret); ok {
		t.Fatal("expired session accepted")
	}
	a.Now = nil

	// a new password ends every session; so does clearing it
	secret, _, _ = a.NewSession("1", "")
	if err := a.SetPassword("nog een wachtwoord"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Session(secret); ok {
		t.Fatal("session survived a new password")
	}
	secret, _, _ = a.NewSession("1", "")
	if err := a.ClearPassword(); err != nil {
		t.Fatal(err)
	}
	if a.Enabled() {
		t.Fatal("still enabled")
	}
	if _, ok := a.Session(secret); ok {
		t.Fatal("session without password")
	}
}

func TestDeleteSessions(t *testing.T) {
	a, _ := open(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	s1, one, _ := a.NewSession("1", "")
	s2, _, _ := a.NewSession("2", "")
	s3, three, _ := a.NewSession("3", "")
	if n := len(a.Sessions()); n != 3 {
		t.Fatalf("%d sessions", n)
	}
	if err := a.DeleteSession(three.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteSession(three.ID); err != ErrNotFound {
		t.Fatalf("delete twice: %v", err)
	}
	if err := a.DeleteOtherSessions(one.ID); err != nil {
		t.Fatal(err)
	}
	_, ok1 := a.Session(s1)
	_, ok2 := a.Session(s2)
	_, ok3 := a.Session(s3)
	if !ok1 || ok2 || ok3 {
		t.Fatalf("sessions left: %v %v %v", ok1, ok2, ok3)
	}
}

func TestTokens(t *testing.T) {
	a, path := open(t)
	if _, _, err := a.NewToken(" ", ScopeRead); err == nil {
		t.Fatal("token without name")
	}
	if _, _, err := a.NewToken("x", "admin"); err == nil {
		t.Fatal("unknown scope")
	}
	secret, tok, err := a.NewToken("Home Assistant", ScopeRead)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, "hwl_") {
		t.Fatalf("secret %q", secret)
	}
	if b, _ := os.ReadFile(path); strings.Contains(string(b), secret) {
		t.Fatal("token stored in clear")
	}
	got, ok := a.Token(secret)
	if !ok || got.ID != tok.ID || got.Scope != ScopeRead || got.LastUsed == nil {
		t.Fatalf("token %+v %v", got, ok)
	}
	if _, ok := a.Token("hwl_wrong"); ok {
		t.Fatal("wrong token accepted")
	}
	// tokens survive a password change
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Token(secret); !ok {
		t.Fatal("token gone after password change")
	}
	if err := a.DeleteToken(tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Token(secret); ok || len(a.Tokens()) != 0 {
		t.Fatal("token not deleted")
	}
}

// The file is re-read when another process (hwlocal password) changes it.
func TestReloadFromDisk(t *testing.T) {
	a, path := open(t)
	other, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := other.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	if !a.Enabled() {
		t.Fatal("change on disk not seen")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if a.Enabled() {
		t.Fatal("removed file not seen")
	}
}

func TestDamagedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auth.json")
	os.WriteFile(path, []byte(`{"password":"plain"}`), 0o600)
	if _, err := Open(path); err == nil {
		t.Fatal("damaged file accepted")
	}
}

func TestLimit(t *testing.T) {
	a, _ := open(t)
	now := time.Now()
	a.Now = func() time.Time { return now }
	for range freeFails - 1 {
		a.Failed("c")
	}
	if a.Wait("c") != 0 {
		t.Fatal("waiting too early")
	}
	a.Failed("c")
	if w := a.Wait("c"); w != firstWait {
		t.Fatalf("wait %v", w)
	}
	a.Failed("c")
	if w := a.Wait("c"); w != 2*firstWait {
		t.Fatalf("wait %v", w)
	}
	if a.Wait("other") != 0 {
		t.Fatal("other client waits")
	}
	for range 20 {
		a.Failed("c")
	}
	if w := a.Wait("c"); w != maxWait {
		t.Fatalf("wait %v, want max", w)
	}
	a.Succeeded("c")
	if a.Wait("c") != 0 {
		t.Fatal("not reset")
	}
}
