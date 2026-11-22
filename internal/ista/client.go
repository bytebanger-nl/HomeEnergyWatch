// Package ista reads heating consumption from mijn.ista.nl, the resident
// portal of ista Nederland (heat cost allocators on the radiators), stores
// it per radiator per day and works out usage and costs per settlement
// period. There is no official API: the client logs in like a browser
// (OpenID Connect through ista's Keycloak) and calls the JSON endpoints the
// portal's own pages use. See docs/ISTA.md.
package ista

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// DefaultURL is the resident portal.
const DefaultURL = "https://mijn.ista.nl"

const (
	// The portal's own frontend sends this fixed bearer ("i want a tasty
	// cookie"); the server only checks that it is there. Access comes from
	// the session cookies and the CSRF header.
	placeholderBearer = "Bearer aSB3YW50IGEgdGFzdHkgY29va2ll"
	csrfHeader        = "x-csrf-token-ista-nl_tp"
	userAgent         = "Mozilla/5.0 (compatible; hwlocal)"
	maxBody           = 8 << 20
)

// ErrAuth means the portal refused the username or password. Retrying with
// the same credentials only risks a locked account.
var ErrAuth = errors.New("ista: gebruikersnaam of wachtwoord geweigerd")

var (
	reLoginForm = regexp.MustCompile(`(?is)<form[^>]+id="kc-form-login"[^>]*>`)
	reAction    = regexp.MustCompile(`(?is)action="([^"]+)"`)
	reForm      = regexp.MustCompile(`(?is)<form[^>]*>`)
	reInput     = regexp.MustCompile(`(?is)<input[^>]*>`)
	reName      = regexp.MustCompile(`(?is)\bname="([^"]*)"`)
	reValue     = regexp.MustCompile(`(?is)\bvalue="([^"]*)"`)
	reCSRF      = regexp.MustCompile(`(?is)<meta\s+name="csrf-token"\s+content="([^"]+)"`)
	reTitle     = regexp.MustCompile(`(?is)<title>\s*(.*?)\s*</title>`)
	reKCError   = regexp.MustCompile(`(?is)class="[^"]*(?:kc-feedback-text|alert-error|input-error)[^"]*"[^>]*>\s*([^<]+)`)
)

// Client talks to the portal for one account. It is safe for concurrent use;
// calls are serialised (the portal session is one browser session).
type Client struct {
	// BaseURL is the portal, DefaultURL when empty (a simulator in tests).
	BaseURL  string
	Username string
	Password string
	// ShardDelay is the pause between MonthValues polls (2 s when zero).
	ShardDelay time.Duration
	// MonthBudget is how long MonthValues may keep polling (60 s when zero).
	MonthBudget time.Duration

	mu   sync.Mutex
	hc   *http.Client
	csrf string
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) http() *http.Client {
	if c.hc == nil {
		jar, _ := cookiejar.New(nil)
		c.hc = &http.Client{Jar: jar, Timeout: 30 * time.Second}
	}
	return c.hc
}

// Login starts a new portal session. It returns ErrAuth when the
// credentials are refused.
func (c *Client) Login(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.login(ctx)
}

func (c *Client) login(ctx context.Context) error {
	c.csrf = ""
	jar, _ := cookiejar.New(nil) // a fresh session: no stale cookies
	c.http().Jar = jar
	if _, _, err := c.page(ctx, "GET", c.base()+"/", nil); err != nil {
		return err
	}
	body, at, err := c.page(ctx, "GET", c.base()+"/home/login", nil)
	if err != nil {
		return err
	}
	form := reLoginForm.FindString(body)
	if form == "" {
		return fmt.Errorf("ista: geen inlogformulier gevonden op %s (%q)", hostPath(at), title(body))
	}
	action, err := resolve(at, attr(reAction, form))
	if err != nil {
		return err
	}
	body, at, err = c.page(ctx, "POST", action, url.Values{
		"username": {c.Username}, "password": {c.Password}, "credentialId": {""},
	})
	if err != nil {
		return err
	}
	if reLoginForm.MatchString(body) {
		if msg := strings.TrimSpace(html.UnescapeString(attr(reKCError, body))); msg != "" {
			return fmt.Errorf("%w: %s", ErrAuth, msg)
		}
		return ErrAuth
	}
	// Keycloak answers with a form that a browser submits by script
	// (response_mode=form_post), unless it already sent us back.
	fields := url.Values{}
	for _, in := range reInput.FindAllString(body, -1) {
		if name := attr(reName, in); name != "" {
			fields.Set(name, html.UnescapeString(attr(reValue, in)))
		}
	}
	if form := reForm.FindString(body); form != "" && fields.Get("code") != "" {
		action, err := resolve(at, attr(reAction, form))
		if err != nil {
			return err
		}
		if _, _, err := c.page(ctx, "POST", action, fields); err != nil {
			return err
		}
	} else if !sameHost(at, c.base()) {
		return fmt.Errorf("ista: onverwacht antwoord van de inlogserver op %s (%q)", hostPath(at), title(body))
	}
	body, _, err = c.page(ctx, "GET", c.base()+"/Home", nil)
	if err != nil {
		return err
	}
	c.csrf = html.UnescapeString(attr(reCSRF, body))
	if c.csrf == "" {
		return fmt.Errorf("ista: na inloggen geen sessie (%q)", title(body))
	}
	return nil
}

// page fetches an HTML page like a browser navigation and follows
// redirects. It returns the body and the final URL.
func (c *Client) page(ctx context.Context, method, u string, form url.Values) (string, string, error) {
	var rd io.Reader
	if form != nil {
		rd = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,*/*;q=0.8")
	// /home/login answers 401 instead of redirecting to the login server
	// without the headers of a navigation.
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("Referer", c.base()+"/")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return "", "", fmt.Errorf("ista: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return "", "", fmt.Errorf("ista: %w", err)
	}
	final := resp.Request.URL.String()
	if resp.StatusCode >= 400 {
		return "", final, fmt.Errorf("ista: %s %s: HTTP %d", method, hostPath(final), resp.StatusCode)
	}
	return string(b), final, nil
}

// errStatus is a JSON call's HTTP status other than 200.
type errStatus struct {
	code int
	body string
}

func (e errStatus) Error() string {
	return fmt.Sprintf("ista: HTTP %d: %.200s", e.code, e.body)
}

// call posts a JSON API request (logging in first when needed, and once
// more when the session expired). It returns the raw answer and its status;
// 425 ("still loading") counts as an answer.
func (c *Client) call(ctx context.Context, path string, extra map[string]any) ([]byte, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.csrf == "" {
		if err := c.login(ctx); err != nil {
			return nil, 0, err
		}
	}
	b, code, err := c.post(ctx, path, extra)
	if err == nil && code == http.StatusUnauthorized {
		if err := c.login(ctx); err != nil {
			return nil, 0, err
		}
		b, code, err = c.post(ctx, path, extra)
	}
	if err != nil {
		return nil, 0, err
	}
	if code != http.StatusOK && code != http.StatusTooEarly {
		return nil, code, errStatus{code, string(b)}
	}
	return b, code, nil
}

func (c *Client) post(ctx context.Context, path string, extra map[string]any) ([]byte, int, error) {
	body := map[string]any{"LANG": "nl-NL"}
	for k, v := range extra {
		body[k] = v
	}
	j, err := json.Marshal(body)
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base()+path, bytes.NewReader(j))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", placeholderBearer)
	req.Header.Set(csrfHeader, c.csrf)
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("ista: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return nil, 0, fmt.Errorf("ista: %w", err)
	}
	return b, resp.StatusCode, nil
}

func decodeInto(b []byte, v any) error {
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("ista: onverwacht antwoord: %w", err)
	}
	return nil
}

// UserValues returns the account's addresses, billing periods and services.
// raw is the answer as received.
func (c *Client) UserValues(ctx context.Context) (v UserValues, raw []byte, err error) {
	raw, _, err = c.call(ctx, "/api/Values/UserValues", nil)
	if err == nil {
		err = decodeInto(raw, &v)
	}
	return v, raw, err
}

// Consumption returns the readings of every meter of an address from the
// start of the billing period up to and including day (the period's own
// end when day is zero). ista answers for any day inside the period.
func (c *Client) Consumption(ctx context.Context, cuid string, p BillingPeriod, day time.Time) (v Consumption, raw []byte, err error) {
	if !day.IsZero() {
		p.E = day.Format(DayLayout) + "T00:00:00"
	}
	raw, _, err = c.call(ctx, "/api/Values/ConsumptionValues", map[string]any{"Cuid": cuid, "Billingperiod": p})
	if err == nil {
		err = decodeInto(raw, &v)
	}
	return v, raw, err
}

// Averages returns the building average per service from start to end
// (both calendar days).
func (c *Client) Averages(ctx context.Context, cuid string, start, end time.Time) (v Averages, raw []byte, err error) {
	raw, _, err = c.call(ctx, "/api/Values/ConsumptionAverages", map[string]any{
		"Cuid": cuid,
		"PAR":  map[string]any{"start": start.Format(DayLayout), "end": end.Format(DayLayout), "cuid": cuid},
	})
	if err == nil {
		err = decodeInto(raw, &v)
	}
	return v, raw, err
}

// MonthValues returns the consumption per month. The portal loads these in
// shards: the call is repeated until every shard is there or the budget is
// spent; a partial answer is returned with complete false.
func (c *Client) MonthValues(ctx context.Context, cuid string) (v Months, raw []byte, complete bool, err error) {
	delay, budget := c.ShardDelay, c.MonthBudget
	if delay == 0 {
		delay = 2 * time.Second
	}
	if budget == 0 {
		budget = 60 * time.Second
	}
	deadline := time.Now().Add(budget)
	for {
		b, _, err := c.call(ctx, "/api/Consumption/MonthValues", map[string]any{"Cuid": cuid})
		if err != nil {
			return v, raw, false, err
		}
		var m Months
		if json.Unmarshal(b, &m) == nil {
			v, raw = m, b
			if m.Shards > 0 && m.Loaded >= m.Shards {
				return v, raw, true, nil
			}
		}
		if time.Now().Add(delay).After(deadline) {
			if raw == nil {
				return v, nil, false, errors.New("ista: maandwaarden niet geladen")
			}
			return v, raw, false, nil
		}
		select {
		case <-ctx.Done():
			return v, raw, false, ctx.Err()
		case <-time.After(delay):
		}
	}
}

func attr(re *regexp.Regexp, s string) string {
	if m := re.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return ""
}

func title(body string) string { return strings.TrimSpace(attr(reTitle, body)) }

func resolve(base, ref string) (string, error) {
	b, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	r, err := url.Parse(html.UnescapeString(ref))
	if err != nil {
		return "", err
	}
	return b.ResolveReference(r).String(), nil
}

func sameHost(a, b string) bool {
	x, err1 := url.Parse(a)
	y, err2 := url.Parse(b)
	return err1 == nil && err2 == nil && x.Host == y.Host
}

// hostPath drops the query (it carries login state) for messages.
func hostPath(u string) string {
	if x, err := url.Parse(u); err == nil {
		return x.Host + x.Path
	}
	return u
}
