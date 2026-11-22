package server

import (
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
)

// The default price range ends at the midnight after tomorrow, also when
// tomorrow is a DST day of 23 or 25 hours.
func TestTodayAndTomorrowDST(t *testing.T) {
	for _, c := range []struct {
		day   string
		hours float64
	}{
		{"2026-10-24", 49}, // tomorrow ends summer time
		{"2027-03-27", 47}, // tomorrow starts summer time
		{"2026-11-13", 48},
	} {
		d, _ := time.ParseInLocation("2006-01-02", c.day, prices.Amsterdam)
		from, to := todayAndTomorrow(d.Add(15 * time.Hour))
		if got := to.Sub(from).Hours(); got != c.hours {
			t.Errorf("%s: %v hours, want %v", c.day, got, c.hours)
		}
		if end := to.In(prices.Amsterdam); end.Hour() != 0 || end.Day() != d.AddDate(0, 0, 2).Day() {
			t.Errorf("%s: ends %v", c.day, end)
		}
	}
}

// tailscale serve forwards through loopback; a funnel request (from the
// internet) is refused unless public access is allowed.
func TestFunnelRefused(t *testing.T) {
	s := newServer(t)
	h := s.Handler()
	if w := do(h, "GET", "/api/devices", "127.0.0.1:5000", "", nil); w.Code != http.StatusOK {
		t.Fatalf("tailscale serve: %d", w.Code)
	}
	funnel := map[string]string{"Tailscale-Funnel-Request": "?1"}
	if w := do(h, "GET", "/api/devices", "127.0.0.1:5000", "", funnel); w.Code != http.StatusForbidden {
		t.Fatalf("funnel: %d", w.Code)
	}
	s.AllowPublic = true
	if w := do(s.Handler(), "GET", "/api/devices", "127.0.0.1:5000", "", funnel); w.Code != http.StatusOK {
		t.Fatalf("funnel with public access: %d", w.Code)
	}
}

// Behind a local proxy the attempt limit counts per forwarded client, using
// the entry the proxy appended (the last one), not one the client sent.
func TestLoginLimitPerForwardedClient(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	attacker := map[string]string{"X-Forwarded-For": "100.64.0.9, 100.64.0.7"}
	for range 5 {
		do(h, "POST", "/api/auth/login", "127.0.0.1:5000", `{"password":"fout"}`, attacker)
	}
	if w := do(h, "POST", "/api/auth/login", "127.0.0.1:5000", `{"password":"geheim wachtwoord"}`, attacker); w.Code != http.StatusTooManyRequests {
		t.Fatalf("attacker after 5 wrong: %d", w.Code)
	}
	// a spoofed first entry does not escape the limit
	spoof := map[string]string{"X-Forwarded-For": "1.2.3.4, 100.64.0.7"}
	if w := do(h, "POST", "/api/auth/login", "127.0.0.1:5000", `{"password":"fout"}`, spoof); w.Code != http.StatusTooManyRequests {
		t.Fatalf("spoofed entry: %d", w.Code)
	}
	owner := map[string]string{"X-Forwarded-For": "100.64.0.8"}
	if w := do(h, "POST", "/api/auth/login", "127.0.0.1:5000", `{"password":"geheim wachtwoord"}`, owner); w.Code != http.StatusOK {
		t.Fatalf("owner: %d %s", w.Code, w.Body)
	}
	// from the LAN the header is ignored
	if w := do(h, "POST", "/api/auth/login", lan, `{"password":"geheim wachtwoord"}`, attacker); w.Code != http.StatusOK {
		t.Fatalf("LAN client with header: %d", w.Code)
	}
}

// Parallel wrong passwords cannot all pass the limit before it counts them.
func TestLoginLimitParallel(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	checked := 0
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if w := do(h, "POST", "/api/auth/login", lan, `{"password":"fout"}`, nil); w.Code == http.StatusUnauthorized {
				mu.Lock()
				checked++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if checked != 5 {
		t.Fatalf("%d passwords checked, want 5", checked)
	}
}

// A proxy that passes on an X-Forwarded-For the client chose gives no fresh
// budget per guess: all clients behind it share a larger limit.
func TestLoginLimitRotatingForwardedFor(t *testing.T) {
	h, a := authServer(t)
	if err := a.SetPassword("geheim wachtwoord"); err != nil {
		t.Fatal(err)
	}
	refused := 0
	for i := range 60 {
		hdr := map[string]string{"X-Forwarded-For": fmt.Sprintf("10.9.%d.%d", i/250, i%250+1)}
		if w := do(h, "POST", "/api/auth/login", "127.0.0.1:5000", `{"password":"fout"}`, hdr); w.Code == http.StatusTooManyRequests {
			refused++
		}
	}
	if refused != 10 {
		t.Fatalf("%d of 60 refused, want 10 (after the proxy's 50)", refused)
	}
}
