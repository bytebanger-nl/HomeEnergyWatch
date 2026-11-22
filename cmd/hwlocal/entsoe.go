package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/server"
)

// entsoeSetting is the ENTSO-E API token as the PWA sees it: it can be set
// or cleared, never read back. It lives in a file in the data directory
// (protected on Windows by the service's ACL); ENTSOE_API_TOKEN takes
// precedence and then cannot be changed from the PWA.
type entsoeSetting struct {
	path    string          // token file
	names   string          // configured electricity sources
	fromEnv bool            // token comes from ENTSOE_API_TOKEN
	svc     *prices.Service // gets the rebuilt sources
	log     *slog.Logger
	// check asks ENTSO-E whether it accepts the token (tests replace it).
	check func(ctx context.Context, token string) error

	mu    sync.Mutex
	token string
}

func (e *entsoeSetting) Status() server.TokenStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	used := false
	for _, n := range strings.Split(e.names, ",") {
		used = used || strings.EqualFold(strings.TrimSpace(n), "entsoe")
	}
	return server.TokenStatus{Configured: e.token != "", FromEnv: e.fromEnv, Used: used}
}

func (e *entsoeSetting) Set(ctx context.Context, token string) (string, error) {
	if e.fromEnv {
		return "", errors.New("het token komt uit de omgevingsvariabele ENTSOE_API_TOKEN en kan hier niet worden gewijzigd")
	}
	token = cleanToken(token)
	if len(token) > 256 || strings.IndexFunc(token, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("ongeldig token")
	}
	warning := ""
	if token != "" {
		check := e.check
		if check == nil {
			check = checkEntsoeToken
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := check(ctx, token)
		cancel()
		switch {
		case errors.Is(err, prices.ErrTokenRejected):
			return "", errors.New("ENTSO-E weigert dit token")
		case err != nil:
			// unreachable or no data: keep it, it is checked on the next fetch
			warning = "het token kon nu niet bij ENTSO-E worden gecontroleerd en is wel opgeslagen"
			e.log.Warn("ENTSO-E-token niet gecontroleerd", "err", err)
		}
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if token == "" {
		if err := os.Remove(e.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("token verwijderen: %w", err)
		}
	} else if err := os.WriteFile(e.path, []byte(token+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("token opslaan: %w", err)
	}
	list, err := priceSources(e.names, token)
	if err != nil {
		return "", err
	}
	e.svc.SetSources(list)
	e.token = token
	return warning, nil
}

// checkEntsoeToken requests today's prices; only a rejected token is an
// error that matters (ErrNotPublished means the token was accepted).
func checkEntsoeToken(ctx context.Context, token string) error {
	_, err := (&prices.EntsoE{Token: token}).Day(ctx, time.Now())
	if errors.Is(err, prices.ErrNotPublished) {
		return nil
	}
	return err
}

// readTokenFile returns the token in path, cleaned by cleanToken; a missing
// file is no error.
func readTokenFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return cleanToken(string(b)), nil
}

// cleanToken returns the token as typed or pasted (in the app, the installer
// or the token file) without what commonly comes along with it: a byte order
// mark (Notepad), surrounding white space and quotes, and a variable
// assignment such as "ENTSOE_API_TOKEN=..." or "set ENTSOE_API_TOKEN=...".
func cleanToken(s string) string {
	s = strings.TrimSpace(strings.TrimPrefix(s, "\xef\xbb\xbf"))
	for _, kw := range []string{"export ", "set "} {
		if len(s) > len(kw) && strings.EqualFold(s[:len(kw)], kw) {
			s = strings.TrimSpace(s[len(kw):])
		}
	}
	if name, value, ok := strings.Cut(s, "="); ok && strings.EqualFold(strings.TrimSpace(name), "ENTSOE_API_TOKEN") {
		s = strings.TrimSpace(value)
	}
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		s = strings.TrimSpace(s[1 : len(s)-1])
	}
	return s
}
