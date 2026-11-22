package ista

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// Sim is a stand-in for mijn.ista.nl: the login flow (portal → Keycloak
// form → form_post back → /Home with a CSRF token) and the four JSON calls,
// over made-up readings of four radiators. It serves tests and hwsim; it
// holds no real account data.
type Sim struct {
	Username string
	Password string
	// MoveIn is the first day of the tenancy (DateStart).
	MoveIn time.Time
	// Now is the simulator's clock (time.Now when nil).
	Now func() time.Time
	// Lag is how many days the last real reading is behind today.
	Lag int
	// Stale is how many of the days up to the last reading ista has not
	// measured: like the portal when the radiators' readings are late, those
	// days carry a fraction (ista's estimate) that stops growing after three
	// days, and the building average stays at the last measured day.
	Stale int

	mu       sync.Mutex
	sessions map[string]string // session cookie -> csrf token
	codes    map[string]bool
	shards   map[string]int // session -> MonthValues calls
	// Calls counts the API calls per path, for tests.
	Calls map[string]int
	// Logins counts successful logins.
	Logins int
}

// SimCuid is the address id the simulator answers with.
const SimCuid = "0000sim0-0000-4000-8000-000000000001"

type simRoom struct {
	id        int64
	radNr     int
	position  string
	factor    float64
	reduction float64
	weight    float64 // share of the heating demand
}

var simRooms = []simRoom{
	{9000001, 4, "Woonkamer", 2.25, 25, 1.6},
	{9000002, 3, "Slaapkamer", 1.0, 25, 0.6},
	{9000003, 2, "Badkamer", 0.325, 10, 0.5},
	{9000004, 1, "Keuken", 0.5, 10, 0.2},
}

func (s *Sim) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func day0(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// ticksOn is a room's ticks on one day: heating degree days with a seasonal
// curve, a weekly wobble and nothing in summer.
func ticksOn(r simRoom, d time.Time) float64 {
	doy := float64(d.YearDay())
	temp := 11 - 8*math.Cos(2*math.Pi*(doy-20)/365.25) + 2*math.Sin(doy*0.9)
	hdd := 15.5 - temp
	if hdd <= 0 {
		return 0
	}
	return hdd * 0.35 * r.weight
}

// cumTicks is a room's ticks from from up to and including to.
func cumTicks(r simRoom, from, to time.Time) float64 {
	if to.Before(from) {
		return 0
	}
	return prefixTicks(r, to) - prefixTicks(r, from.AddDate(0, 0, -1))
}

// prefixTicks is a room's ticks from simEpoch up to and including d,
// memoised: the simulator answers hundreds of calls over the same days.
var (
	simEpoch  = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	prefixMu  sync.Mutex
	prefixSum = map[int64][]float64{} // room id -> sums by day since simEpoch
)

func prefixTicks(r simRoom, d time.Time) float64 {
	n := int(d.Sub(simEpoch).Hours() / 24)
	if n < 0 {
		return 0
	}
	prefixMu.Lock()
	defer prefixMu.Unlock()
	sums := prefixSum[r.id]
	for len(sums) <= n {
		prev := 0.0
		if len(sums) > 0 {
			prev = sums[len(sums)-1]
		}
		sums = append(sums, prev+ticksOn(r, simEpoch.AddDate(0, 0, len(sums))))
	}
	prefixSum[r.id] = sums
	return sums[n]
}

// simPeriod is the settlement year (1 July – 30 June) a day falls in.
func simPeriod(d time.Time) (start, end time.Time, y int) {
	y = d.Year()
	if d.Month() >= time.July {
		y++
	}
	start = time.Date(y-1, time.July, 1, 0, 0, 0, 0, time.UTC)
	return start, start.AddDate(1, 0, -1), y
}

func (s *Sim) moveIn() time.Time {
	if s.MoveIn.IsZero() {
		return day0(s.now()).AddDate(-1, -4, 0)
	}
	return day0(s.MoveIn)
}

// lastReading is the last day with a real reading.
func (s *Sim) lastReading() time.Time { return day0(s.now()).AddDate(0, 0, -s.Lag) }

// lastMeasured is the last day ista measured (see Stale).
func (s *Sim) lastMeasured() time.Time { return s.lastReading().AddDate(0, 0, -s.Stale) }

// meters returns the readings of every room from max(start, move-in) up to
// and including end. The end day itself, when it is today, carries a
// fraction (a reading in progress), like the portal.
func (s *Sim) meters(y int, start, end time.Time) []Meter {
	from := start
	if mi := s.moveIn(); mi.After(from) {
		from = mi
	}
	today := day0(s.now())
	var out []Meter
	for _, r := range simRooms {
		c := 0.0
		if !end.Before(from) {
			c = cumTicks(r, from, end)
		}
		if m := s.lastMeasured(); s.Stale > 0 && end.After(m) && !m.Before(from) {
			// ista's estimate: the measured reading plus a growing fraction
			c = math.Floor(cumTicks(r, from, m)) + 0.25*math.Min(end.Sub(m).Hours()/24, 3)
		} else if !end.Equal(today) {
			c = math.Floor(c)
		} else {
			c = math.Round(c*1000) / 1000
		}
		cc := math.Round(c * r.factor)
		evalR := 92.0
		m := Meter{
			MeterID: r.id, ServiceID: 1, BillingPeriodID: y - 1, RadNr: r.radNr, Order: 1,
			Position: r.position, ArtNr: 11490, MeterNr: 200000000 + r.id%1000, Multiply: 1,
			Reduction: r.reduction, CalcFactor: r.factor,
			BsDate: from.Format(dmyLayout), EsDate: end.Format(dmyLayout),
			EndValue: c, CValue: c, CCValue: cc, CCDValue: math.Round(cc * (1 - r.reduction/100)),
		}
		if end.Equal(today) {
			m.EvalR = &evalR
		}
		out = append(out, m)
	}
	return out
}

func total(ms []Meter) float64 {
	t := 0.0
	for _, m := range ms {
		t += m.CCDValue
	}
	return t
}

// buildingAvg is the building's normalised average from from to to.
func (s *Sim) buildingAvg(from, to time.Time) float64 {
	sum := 0.0
	for _, r := range simRooms {
		sum += cumTicks(r, from, to) * r.factor * 1.15
	}
	return math.Round(sum)
}

// measuredUpTo returns to, or the last measured day when to is after it.
func (s *Sim) measuredUpTo(to time.Time) time.Time {
	if m := s.lastMeasured(); s.Stale > 0 && to.After(m) {
		return m
	}
	return to
}

// Handler serves the portal.
func (s *Sim) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "affinity", Value: "sim", Path: "/"})
		fmt.Fprint(w, "<html><title>Mijn ista</title><a href=\"/home/login\">Inloggen</a></html>")
	})
	mux.HandleFunc("GET /home/login", func(w http.ResponseWriter, r *http.Request) {
		if s.session(r) != "" {
			http.Redirect(w, r, "/Home", http.StatusFound)
			return
		}
		if r.Header.Get("Sec-Fetch-Mode") != "navigate" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		http.Redirect(w, r, "/realms/portal_nl/protocol/openid-connect/auth?client_id=sim&state=xyz", http.StatusFound)
	})
	mux.HandleFunc("GET /realms/portal_nl/protocol/openid-connect/auth", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, loginPage(""))
	})
	mux.HandleFunc("POST /realms/portal_nl/login-actions/authenticate", func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("username") != s.Username || r.FormValue("password") != s.Password {
			fmt.Fprint(w, loginPage("Ongeldige gebruikersnaam of wachtwoord."))
			return
		}
		code := token()
		s.mu.Lock()
		if s.codes == nil {
			s.codes = map[string]bool{}
		}
		s.codes[code] = true
		s.mu.Unlock()
		fmt.Fprintf(w, `<HTML><HEAD><TITLE>Submit This Form</TITLE></HEAD><BODY Onload="javascript:document.forms[0].submit()">
<FORM METHOD="POST" ACTION="/signin-oidc"><INPUT TYPE="HIDDEN" NAME="code" VALUE="%s"/><INPUT TYPE="HIDDEN" NAME="state" VALUE="xyz"/></FORM></BODY></HTML>`, code)
	})
	mux.HandleFunc("POST /signin-oidc", func(w http.ResponseWriter, r *http.Request) {
		code := r.FormValue("code")
		s.mu.Lock()
		ok := s.codes[code]
		delete(s.codes, code)
		var sess string
		if ok {
			sess = token()
			if s.sessions == nil {
				s.sessions = map[string]string{}
			}
			s.sessions[sess] = token()
			s.Logins++
		}
		s.mu.Unlock()
		if !ok {
			http.Error(w, "invalid code", http.StatusBadRequest)
			return
		}
		http.SetCookie(w, &http.Cookie{Name: ".AspNetCore.Cookies", Value: sess, Path: "/", HttpOnly: true})
		http.Redirect(w, r, "/home/login", http.StatusFound)
	})
	mux.HandleFunc("GET /Home", func(w http.ResponseWriter, r *http.Request) {
		csrf := s.session(r)
		if csrf == "" {
			http.Redirect(w, r, "/home/login", http.StatusFound)
			return
		}
		fmt.Fprintf(w, `<html><head><title>Mijn ista</title><meta name="csrf-token" content="%s"></head><body>Dashboard</body></html>`, csrf)
	})
	api := func(path string, fn func(w http.ResponseWriter, body map[string]json.RawMessage, sess string)) {
		mux.HandleFunc("POST "+path, func(w http.ResponseWriter, r *http.Request) {
			s.mu.Lock()
			if s.Calls == nil {
				s.Calls = map[string]int{}
			}
			s.Calls[path]++
			s.mu.Unlock()
			cookie, _ := r.Cookie(".AspNetCore.Cookies")
			csrf := s.session(r)
			if csrf == "" || r.Header.Get(csrfHeader) != csrf || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "bad body", http.StatusBadRequest)
				return
			}
			fn(w, body, cookie.Value)
		})
	}
	api("/api/Values/UserValues", s.userValues)
	api("/api/Values/ConsumptionValues", s.consumption)
	api("/api/Values/ConsumptionAverages", s.averages)
	api("/api/Consumption/MonthValues", s.months)
	return mux
}

// Expire ends every session, as when the portal's session times out.
func (s *Sim) Expire() {
	s.mu.Lock()
	s.sessions = nil
	s.mu.Unlock()
}

func (s *Sim) session(r *http.Request) string {
	c, err := r.Cookie(".AspNetCore.Cookies")
	if err != nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[c.Value]
}

func loginPage(errMsg string) string {
	msg := ""
	if errMsg != "" {
		msg = `<span id="input-error" class="kc-feedback-text" aria-live="polite">` + errMsg + `</span>`
	}
	return `<html><head><title>Log in met ista NL</title></head><body>` + msg +
		`<form id="kc-form-login" onsubmit="return true;" action="/realms/portal_nl/login-actions/authenticate?session_code=abc&amp;execution=e1&amp;client_id=sim" method="post">` +
		`<input id="username" name="username" value="" type="text"><input id="password" name="password" type="password">` +
		`<input type="hidden" id="id-hidden-input" name="credentialId"></form></body></html>`
}

func token() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func writeSimJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// periods returns the settlement years from the move-in to today, newest
// first.
func (s *Sim) periods() []BillingPeriod {
	var out []BillingPeriod
	today := day0(s.now())
	for d := s.moveIn(); ; {
		start, end, y := simPeriod(d)
		ta := 10.5 + float64(y%3)/2
		out = append(out, BillingPeriod{Y: y, S: start.Format(isoLayout), E: end.Format(isoLayout), TA: &ta})
		d = end.AddDate(0, 0, 1)
		if d.After(today) {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Y > out[j].Y })
	return out
}

func (s *Sim) userValues(w http.ResponseWriter, _ map[string]json.RawMessage, _ string) {
	last := s.lastReading()
	start, _, y := simPeriod(last)
	compStart, compEnd := start.AddDate(-1, 0, 0), last.AddDate(-1, 0, 0)
	cur := s.meters(y, start, last)
	var comp []Meter
	if !compEnd.Before(s.moveIn()) {
		comp = s.meters(y-1, compStart, compEnd)
	}
	prevStart, prevEnd, _ := simPeriod(compStart)
	whole := 0.0
	if !prevEnd.Before(s.moveIn()) {
		whole = total(s.meters(y-1, prevStart, prevEnd))
	}
	tn, tp := total(cur), total(comp)
	diff := 0.0
	if tp > 0 {
		diff = math.Round((tn - tp) / tp * 100)
	}
	writeSimJSON(w, 200, map[string]any{
		"DisplayName": "Sim Bewoner",
		"UserKey":     "sim-user-key",
		"Cus": []map[string]any{{
			"Cuid": SimCuid, "Adress": "Simulatiestraat 1", "Zip": "1234 AB", "City": "TESTSTAD",
			"DateStart": s.moveIn().Format(isoLayout),
			"curConsumption": map[string]any{
				"BillingPeriods":  s.periods(),
				"Billingservices": []BillingService{{ID: 1, Description: "Verwarming", MeterType: "doprimo 3 radio", Unit: "eenheden"}},
				"CurStart":        start.Format(dmyLayout), "CurEnd": last.Format(dmyLayout),
				"CompStart": compStart.Format(dmyLayout), "CompEnd": compEnd.Format(dmyLayout),
				"CurAverageTemp": 9.5, "CompAverageTemp": 9.1,
				"ServicesComp": []ServiceComp{{ID: 1, TotalNow: tn, TotalPrevious: tp, TotalDiffperc: diff,
					TotalWholePrevious: whole, CurMeters: cur, CompMeters: comp}},
			},
		}},
	})
}

func parseBody[T any](body map[string]json.RawMessage, key string) (T, bool) {
	var v T
	raw, ok := body[key]
	if !ok {
		return v, false
	}
	return v, json.Unmarshal(raw, &v) == nil
}

func (s *Sim) consumption(w http.ResponseWriter, body map[string]json.RawMessage, _ string) {
	bp, ok := parseBody[BillingPeriod](body, "Billingperiod")
	cuid, _ := parseBody[string](body, "Cuid")
	if !ok || cuid != SimCuid {
		writeSimJSON(w, 400, map[string]string{"error": "bad request"})
		return
	}
	start, err1 := bp.Start()
	end, err2 := bp.End()
	if err1 != nil || err2 != nil {
		writeSimJSON(w, 400, map[string]string{"error": "bad period"})
		return
	}
	if today := day0(s.now()); end.After(today) {
		end = today
	}
	from := start
	if mi := s.moveIn(); mi.After(from) {
		from = mi
	}
	ms := s.meters(bp.Y, start, end)
	writeSimJSON(w, 200, map[string]any{
		"CurStart": from.Format(dmyLayout), "CurEnd": end.Format(dmyLayout),
		"DCurStart": from.Format(isoLayout), "DCurEnd": end.Format(isoLayout),
		"ServicesComp": []map[string]any{{"Id": 1, "DecPos": 0, "TotalNow": total(ms), "CurMeters": ms}},
	})
}

func (s *Sim) averages(w http.ResponseWriter, body map[string]json.RawMessage, _ string) {
	par, ok := parseBody[struct{ Start, End string }](body, "PAR")
	from, err1 := time.Parse(DayLayout, par.Start)
	to, err2 := time.Parse(DayLayout, par.End)
	if !ok || err1 != nil || err2 != nil {
		writeSimJSON(w, 400, map[string]string{"error": "bad window"})
		return
	}
	writeSimJSON(w, 200, map[string]any{
		"Averages": []map[string]any{{"BillingServiceId": 1, "DecPos": 0, "NormalizedValue": s.buildingAvg(from, s.measuredUpTo(to))}},
		"CurStart": from.Format(dmyLayout), "CurEnd": to.Format(dmyLayout),
	})
}

// months answers like the portal: the first call 425 without data, then
// four more months per call until all are there.
func (s *Sim) months(w http.ResponseWriter, _ map[string]json.RawMessage, sess string) {
	s.mu.Lock()
	if s.shards == nil {
		s.shards = map[string]int{}
	}
	n := s.shards[sess]
	s.shards[sess]++
	s.mu.Unlock()
	today := day0(s.now())
	mi := s.moveIn()
	var all []map[string]any
	for m := time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, time.UTC); !m.Before(time.Date(mi.Year(), mi.Month(), 1, 0, 0, 0, 0, time.UTC)); m = m.AddDate(0, -1, 0) {
		from, to := m, m.AddDate(0, 1, -1)
		if from.Before(mi) {
			from = mi
		}
		if to.After(today) {
			to = today
		}
		own := 0.0
		for _, r := range simRooms {
			own += math.Round(cumTicks(r, from, to) * r.factor * (1 - r.reduction/100))
		}
		at := math.Round((11-8*math.Cos(2*math.Pi*(float64(m.YearDay())+15-20)/365.25))*10) / 10
		all = append(all, map[string]any{
			"y": m.Year(), "m": int(m.Month()), "at": at, "CuToken": "eyJJdGVtMSI6MX0=",
			"ServiceConsumptions": []map[string]any{{"ServiceId": 1, "TotalConsumption": own,
				"BuldingAverage": s.buildingAvg(from, to), "HasApproximation": to.Equal(today)}},
		})
	}
	loaded := min(len(all), 4*n)
	status := 200
	if n == 0 {
		status = http.StatusTooEarly
	}
	years := []int{}
	for y := today.Year(); y >= mi.Year(); y-- {
		years = append(years, y)
	}
	writeSimJSON(w, status, map[string]any{
		"cuid": SimCuid, "hs": loaded, "sh": len(all), "sd": mi.Format(isoLayout), "ys": years, "mc": all[:loaded],
	})
}
