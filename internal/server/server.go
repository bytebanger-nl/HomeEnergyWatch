// Package server serves the PWA and the JSON/SSE API.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/auth"
	"github.com/bytebanger-nl/homeenergywatch/internal/backup"
	"github.com/bytebanger-nl/homeenergywatch/internal/control"
	"github.com/bytebanger-nl/homeenergywatch/internal/costs"
	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/importer"
	"github.com/bytebanger-nl/homeenergywatch/internal/ista"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/prices"
	"github.com/bytebanger-nl/homeenergywatch/internal/report"
	"github.com/bytebanger-nl/homeenergywatch/internal/rules"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Info is returned by GET /api/info.
type Info struct {
	Version    string `json:"version"`
	IntervalMS int64  `json:"interval_ms"`
	TLS        string `json:"tls"`
	Discovery  bool   `json:"discovery"`
	CADownload bool   `json:"ca_download"`
	TimeZone   string `json:"time_zone"`
}

// Server holds the HTTP handlers.
type Server struct {
	Manager  *gateway.Manager
	Recorder *history.Recorder
	Hub      *gateway.Hub
	// Plugins is the generic read-only integration manager (optional).
	Plugins *plugin.Manager
	// Control sends every command to a device (checked, tried again).
	Control *control.Service
	ctlOnce sync.Once
	// Ista serves heating data from mijn.ista.nl (optional).
	Ista      *ista.Service
	Prices    *prices.Service // optional, electricity
	GasPrices *prices.Service // optional
	Contracts *costs.Contracts
	Costs     *costs.Calculator
	Scenarios *costs.Scenarios // optional: what-if contracts
	// Forecast: the settings of the forecasts (optional; nil is automatic)
	Forecast *forecast.Service
	Entsoe   TokenSetting // optional: the ENTSO-E API token
	// WaterContracts and WaterCosts: the water timeline (optional), priced
	// on the water meter's quarter hours under /api/water.
	WaterContracts *costs.Contracts
	WaterCosts     *costs.Calculator
	Log            *slog.Logger
	Web            fs.FS
	Info           Info
	// CACert is served at /ca.crt when set (self-signed TLS mode).
	CACert []byte
	// AllowPublic disables the private-network-only check.
	AllowPublic bool
	// AllowTailscale also accepts clients in 100.64.0.0/10, the shared
	// address space Tailscale gives the devices in a tailnet. Tailscale's IPv6
	// addresses are unique local (fd7a:115c:a1e0::/48) and always accepted.
	AllowTailscale bool
	// AllowedHosts are extra host names (besides IP addresses and LAN names)
	// the gateway may be reached by; "*" accepts any.
	AllowedHosts []string
	// Access holds the remote-access settings made in the PWA (optional);
	// they add to AllowTailscale and AllowedHosts.
	Access *AccessSettings
	// Serve publishes the gateway with `tailscale serve` (optional); ServeTarget
	// is the address Tailscale forwards to, e.g. http://127.0.0.1:8080.
	Serve       TailscaleServe
	ServeTarget string
	// TabOrder is the order of the PWA's tab row (optional).
	TabOrder *TabOrder
	// Rules and Push: alert rules and the Web Push messages they send
	// (optional).
	Rules *rules.Engine
	Push  *rules.Push
	// Tiles stores the user's own dashboard tiles (optional; only with
	// Plugins, whose devices they show).
	Tiles *store.Store
	// Outages holds when devices did not respond (optional).
	Outages *store.Store
	// Notices holds the messages about what went wrong (optional).
	Notices *store.Store
	// Auth is the optional password with its sessions and API tokens; once a
	// password is set, /api/ needs a login (requireLogin).
	Auth *auth.Auth
	// Reports makes PDF reports and mails the scheduled ones (optional).
	Reports *report.Scheduler
	// Backups makes the daily backup (optional).
	Backups *backup.Service

	clientErrorBudget clientErrorBudget
}

// Handler returns the root handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/info", s.info)
	mux.HandleFunc("GET /api/devices", s.listDevices)
	mux.HandleFunc("POST /api/devices", s.addDevice)
	mux.HandleFunc("GET /api/devices/{id}", s.getDevice)
	mux.HandleFunc("PATCH /api/devices/{id}", s.updateDevice)
	mux.HandleFunc("DELETE /api/devices/{id}", s.deleteDevice)
	mux.HandleFunc("POST /api/devices/{id}/pair", s.pairDevice)
	mux.HandleFunc("POST /api/devices/{id}/identify", s.identifyDevice)
	mux.HandleFunc("PUT /api/devices/{id}/power", s.powerDevice)
	mux.HandleFunc("GET /api/stream", s.stream)
	mux.HandleFunc("GET /api/outages", s.outages)
	mux.HandleFunc("POST /api/outages/read", s.readOutages)
	mux.HandleFunc("GET /api/notices", s.notices)
	mux.HandleFunc("POST /api/notices/read", s.readNotices)
	mux.HandleFunc("GET /api/inbox", s.inbox)
	mux.HandleFunc("GET /api/history/rate", s.rateHistory)
	mux.HandleFunc("GET /api/history/energy", s.energyHistory)
	mux.HandleFunc("GET /api/history/grid", s.gridHistory)
	mux.HandleFunc("POST /api/import", s.importCSV)
	mux.HandleFunc("GET /api/prices", s.priceRange)
	mux.HandleFunc("POST /api/prices/fetch", s.fetchPrices)
	s.routeCosts(mux, "/api", s.energy)
	s.routeCosts(mux, "/api/water", s.water)
	mux.HandleFunc("GET /api/scenarios", s.listScenarios)
	mux.HandleFunc("POST /api/scenarios", s.saveScenario)
	mux.HandleFunc("PUT /api/scenarios/{id}", s.saveScenario)
	mux.HandleFunc("DELETE /api/scenarios/{id}", s.deleteScenario)
	mux.HandleFunc("GET /api/compare", s.compareCosts)
	s.routeReports(mux)
	mux.HandleFunc("GET /api/settings/entsoe", s.getEntsoe)
	mux.HandleFunc("PUT /api/settings/entsoe", s.putEntsoe)
	mux.HandleFunc("GET /api/settings/forecast", s.getForecastSettings)
	mux.HandleFunc("PUT /api/settings/forecast", s.putForecastSettings)
	mux.HandleFunc("GET /api/rules", s.listRules)
	mux.HandleFunc("GET /api/rules/prices", s.rulePrices)
	mux.HandleFunc("POST /api/rules", s.saveRule)
	mux.HandleFunc("PUT /api/rules/{id}", s.saveRule)
	mux.HandleFunc("DELETE /api/rules/{id}", s.deleteRule)
	mux.HandleFunc("PATCH /api/rules/{id}/regulate", s.tuneRule)
	mux.HandleFunc("POST /api/rules/{id}/start", s.startRule)
	mux.HandleFunc("POST /api/rules/{id}/stop", s.stopRule)
	mux.HandleFunc("GET /api/rules/{id}/periods", s.rulePeriods)
	mux.HandleFunc("GET /api/push", s.pushInfo)
	mux.HandleFunc("POST /api/push/subscriptions", s.subscribePush)
	mux.HandleFunc("DELETE /api/push/subscriptions", s.unsubscribePush)
	mux.HandleFunc("POST /api/push/test", s.testPush)
	mux.HandleFunc("GET /api/settings/access", s.getAccess)
	mux.HandleFunc("PUT /api/settings/access", s.putAccess)
	mux.HandleFunc("GET /api/settings/tailscale-serve", s.getTailscaleServe)
	mux.HandleFunc("PUT /api/settings/tailscale-serve", s.putTailscaleServe)
	mux.HandleFunc("DELETE /api/settings/tailscale-serve", s.deleteTailscaleServe)
	mux.HandleFunc("GET /api/settings/backup", s.getBackups)
	mux.HandleFunc("POST /api/settings/backup", s.makeBackup)
	mux.HandleFunc("GET /api/settings/tabs", s.getTabOrder)
	mux.HandleFunc("PUT /api/settings/tabs", s.putTabOrder)
	mux.HandleFunc("POST /api/client-errors", s.clientErrors)
	s.routePlugins(mux)
	s.routeTiles(mux)
	s.routeIsta(mux)
	s.routeAuth(mux)
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "onbekend endpoint")
	})
	mux.HandleFunc("GET /ca.crt", s.caCert)
	mux.Handle("GET /", s.static())
	// reject cross-site state-changing requests (e.g. a malicious page
	// trying to switch a socket through the visitor's browser)
	csrf := http.NewCrossOriginProtection()
	return s.guard(securityHeaders(csrf.Handler(s.requireLogin(mux))))
}

// guard rejects clients outside private networks unless AllowPublic is set.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tailscale, _ := s.Access.get()
		if !s.AllowPublic && (!isPrivateClient(r.RemoteAddr, s.AllowTailscale || tailscale) || r.Header.Get("Tailscale-Funnel-Request") != "") {
			// tailscale serve forwards through 127.0.0.1; with funnel the
			// request comes from the internet, which it marks with
			// "Tailscale-Funnel-Request: ?1" (after removing any copy the
			// client sent)
			http.Error(w, "alleen bereikbaar vanuit het lokale netwerk", http.StatusForbidden)
			return
		}
		if !s.allowedHost(r.Host) {
			// DNS rebinding: another site's domain pointing at this gateway
			http.Error(w, "onbekende hostnaam; voeg hem toe onder Instellingen > Toegang op afstand of aan HWLOCAL_ALLOWED_HOSTS", http.StatusMisdirectedRequest)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// lanSuffixes are domain suffixes that only exist inside a home network.
var lanSuffixes = []string{".local", ".lan", ".home", ".home.arpa", ".internal", ".localdomain", ".fritz.box"}

// allowedHost reports whether the Host header names this gateway: an IP
// address, localhost, a single-label or LAN name, or a configured name. A
// public domain name that is not configured is refused, which stops DNS
// rebinding (a website's own domain resolving to the gateway's address).
func (s *Server) allowedHost(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(strings.Trim(host, "[]")), ".")
	if host == "" || net.ParseIP(host) != nil || host == "localhost" || !strings.Contains(host, ".") {
		return true
	}
	for _, suffix := range lanSuffixes {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	_, extra := s.Access.get()
	for _, allowed := range slices.Concat(s.AllowedHosts, extra) {
		allowed = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(allowed)), ".")
		if allowed == "*" || allowed == host {
			return true
		}
	}
	return false
}

// tailnet is the shared address space (RFC 6598) that Tailscale assigns its
// devices from. ISPs use it for carrier-grade NAT too, so it is only trusted
// when AllowTailscale or the PWA setting (Access) says so.
var tailnet = netip.MustParsePrefix("100.64.0.0/10")

func isPrivateClient(remoteAddr string, allowTailscale bool) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return true
	}
	if addr, ok := netip.AddrFromSlice(ip); ok && allowTailscale {
		return tailnet.Contains(addr.Unmap())
	}
	return false
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self'; script-src 'self'; connect-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) static() http.Handler {
	files := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// the PWA is small; always revalidate so updates show up immediately
		w.Header().Set("Cache-Control", "no-cache")
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Service-Worker-Allowed", "/")
		}
		if strings.HasSuffix(r.URL.Path, ".webmanifest") {
			w.Header().Set("Content-Type", "application/manifest+json")
		}
		files.ServeHTTP(w, r)
	})
}

func (s *Server) caCert(w http.ResponseWriter, r *http.Request) {
	if len(s.CACert) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-x509-ca-cert")
	w.Header().Set("Content-Disposition", `attachment; filename="homewizardlocal-ca.crt"`)
	_, _ = w.Write(s.CACert)
}

func (s *Server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Info)
}

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Manager.Views())
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	v, ok := s.Manager.View(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "apparaat niet gevonden")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) addDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Host string `json:"host"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Manager.AddManual(r.Context(), body.Host)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) updateDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name         *string `json:"name"`
		Enabled      *bool   `json:"enabled"`
		ShowCounters *bool   `json:"show_counters"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.Manager.Update(r.Context(), r.PathValue("id"), gateway.DeviceUpdate{Name: body.Name, Enabled: body.Enabled, ShowCounters: body.ShowCounters})
	if err != nil {
		s.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) deleteDevice(w http.ResponseWriter, r *http.Request) {
	purge := r.URL.Query().Get("purge") == "true"
	if err := s.Manager.Remove(r.Context(), r.PathValue("id"), purge); err != nil {
		s.writeManagerError(w, err)
		return
	}
	if s.Plugins != nil {
		// the store dropped the device's links to plugin devices
		s.Plugins.Reload(r.Context())
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pairDevice(w http.ResponseWriter, r *http.Request) {
	v, err := s.Manager.Pair(r.PathValue("id"))
	if err != nil {
		s.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, v)
}

func (s *Server) identifyDevice(w http.ResponseWriter, r *http.Request) {
	if err := s.Manager.Identify(r.Context(), r.PathValue("id")); err != nil {
		s.writeManagerError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) powerDevice(w http.ResponseWriter, r *http.Request) {
	var body struct {
		On *bool `json:"on"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.On == nil {
		writeError(w, http.StatusBadRequest, "veld 'on' ontbreekt")
		return
	}
	// sent in the background: checked and tried again, progress as
	// "control" events
	st, err := s.control().Start(control.Command{Device: r.PathValue("id"), Key: control.PowerKey, Value: *body.On, Source: "app"})
	if err != nil {
		s.writeManagerError(w, err)
		return
	}
	writeJSON(w, http.StatusAccepted, st)
}

// control returns the service that sends commands: Control, or one of its
// own over Manager and Plugins when that is not set (tests).
func (s *Server) control() *control.Service {
	s.ctlOnce.Do(func() {
		if s.Control == nil {
			c := &control.Service{Devices: s.Manager, Log: s.Log}
			if s.Plugins != nil {
				c.Plugins = s.Plugins
			}
			s.Control = c
		}
	})
	return s.Control
}

func (s *Server) writeManagerError(w http.ResponseWriter, err error) {
	if errors.Is(err, gateway.ErrNotFound) {
		writeError(w, http.StatusNotFound, "apparaat niet gevonden")
		return
	}
	writeError(w, http.StatusBadRequest, err.Error())
}

// stream sends server-sent events: first a snapshot of all devices, then an
// event for every device update.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming niet ondersteund")
		return
	}
	events, cancel := s.Hub.Subscribe()
	defer cancel()

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	snap := map[string]any{"devices": s.Manager.Views(), "info": s.Info, "tab_order": s.TabOrder.Order(), "controls": s.control().List()}
	if s.Plugins != nil {
		snap["plugins"] = s.Plugins.DeviceViews()
		if tabs, err := s.Plugins.Tabs(r.Context()); err == nil {
			snap["plugin_tabs"] = tabs
		}
	}
	snapshot, _ := json.Marshal(gateway.Event{Type: "snapshot", Data: snap})
	fmt.Fprintf(w, "retry: 3000\nevent: message\ndata: %s\n\n", snapshot)
	flusher.Flush()

	rc := http.NewResponseController(w)
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case data, ok := <-events:
			if !ok {
				return
			}
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprintf(w, "event: message\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (s *Server) historyRange(w http.ResponseWriter, r *http.Request, maxSpan time.Duration) (id string, from, to time.Time, ok bool) {
	q := r.URL.Query()
	id = q.Get("device")
	if _, exists := s.Manager.View(id); !exists {
		writeError(w, http.StatusNotFound, "apparaat niet gevonden")
		return "", time.Time{}, time.Time{}, false
	}
	var err error
	to = time.Now().UTC()
	if v := q.Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'to' (RFC 3339 verwacht)")
			return "", time.Time{}, time.Time{}, false
		}
	}
	from = to.Add(-24 * time.Hour)
	if v := q.Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'from' (RFC 3339 verwacht)")
			return "", time.Time{}, time.Time{}, false
		}
	}
	if !to.After(from) || to.Sub(from) > maxSpan {
		writeError(w, http.StatusBadRequest, "ongeldige periode")
		return "", time.Time{}, time.Time{}, false
	}
	return id, from.UTC(), to.UTC(), true
}

func (s *Server) rateHistory(w http.ResponseWriter, r *http.Request) {
	id, from, to, ok := s.historyRange(w, r, 400*24*time.Hour)
	if !ok {
		return
	}
	points, _ := strconv.Atoi(r.URL.Query().Get("points"))
	if points <= 0 || points > 5000 {
		points = 720
	}
	series, step, err := s.Recorder.RateSeries(r.Context(), id, from, to, points)
	if err != nil {
		s.Log.Error("rate history", "err", err)
		writeError(w, http.StatusInternalServerError, "historie niet beschikbaar")
		return
	}
	if series == nil {
		series = []history.RatePoint{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "step_s": int(step.Seconds()), "points": series})
}

// gridHistory serves per-phase voltage, current and power (from minute
// data, so only within the retention period) and the grid events of the
// period (kept forever). The period may be long (a contract period, all
// years) so the events of all of it can be listed; the minute data only
// covers the retention period anyway.
func (s *Server) gridHistory(w http.ResponseWriter, r *http.Request) {
	id, from, to, ok := s.historyRange(w, r, 20*366*24*time.Hour)
	if !ok {
		return
	}
	points, _ := strconv.Atoi(r.URL.Query().Get("points"))
	if points <= 0 || points > 5000 {
		points = 720
	}
	series, step, phases, err := s.Recorder.GridSeries(r.Context(), id, from, to, points)
	if err != nil {
		s.Log.Error("grid history", "err", err)
		writeError(w, http.StatusInternalServerError, "historie niet beschikbaar")
		return
	}
	events, err := s.Recorder.GridEvents(r.Context(), id, from, to)
	if err != nil {
		s.Log.Error("grid events", "err", err)
		writeError(w, http.StatusInternalServerError, "historie niet beschikbaar")
		return
	}
	if series == nil {
		series = []history.GridPoint{}
	}
	if events == nil {
		events = []store.GridEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "step_s": int(step.Seconds()),
		"phases": phases, "points": series, "events": events})
}

func (s *Server) energyHistory(w http.ResponseWriter, r *http.Request) {
	id, from, to, ok := s.historyRange(w, r, 20*366*24*time.Hour)
	if !ok {
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = history.BucketHour
	}
	limits := map[string]time.Duration{
		history.BucketQuarter: 32 * 24 * time.Hour,
		history.BucketHour:    93 * 24 * time.Hour,
		history.BucketDay:     3 * 366 * 24 * time.Hour,
		history.BucketMonth:   20 * 366 * 24 * time.Hour,
	}
	limit, known := limits[bucket]
	if !known {
		writeError(w, http.StatusBadRequest, "bucket moet quarter, hour, day of month zijn")
		return
	}
	if to.Sub(from) > limit {
		writeError(w, http.StatusBadRequest, "periode te lang voor deze bucket")
		return
	}
	buckets, err := s.Recorder.Energy(r.Context(), id, from, to, bucket)
	if err != nil {
		s.Log.Error("energy history", "err", err)
		writeError(w, http.StatusInternalServerError, "historie niet beschikbaar")
		return
	}
	if buckets == nil {
		buckets = []history.EnergyBucket{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "bucket": bucket, "buckets": buckets})
}

// importCSV imports a HomeWizard Energy app export (CSV in the request body).
// Query: device (optional; default the first device of the matching kind),
// dry_run=true for a preview, force=true to import into a device of another
// kind.
func (s *Server) importCSV(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	res, err := importer.Parse(r.Body, "", s.Recorder.Location())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cands := importCandidates(s.Manager.Views(), res)
	label := map[importer.Kind]string{
		importer.KindElectricity: "de P1-meter", importer.KindGas: "de P1-meter",
		importer.KindWater: "de watermeter", importer.KindMeter: "een Energy Socket of kWh-meter",
	}[res.Kind]
	var device gateway.DeviceView
	match := "chosen"
	if id := q.Get("device"); id != "" {
		v, ok := s.Manager.View(id)
		if !ok {
			writeError(w, http.StatusNotFound, "apparaat niet gevonden")
			return
		}
		if !slices.ContainsFunc(cands, func(c gateway.DeviceView) bool { return c.ID == id }) && q.Get("force") != "true" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("dit bestand komt van %s, maar %s is een %s", label, v.Name, v.Model))
			return
		}
		device = v
	} else {
		if len(cands) == 0 {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("Dit is een export van %s. Voeg dat apparaat eerst toe aan de gateway.", label))
			return
		}
		v, ok := matchByCounter(cands, res)
		if !ok {
			// several fit and the counters do not tell which: let the user choose
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":      "Kies bij welk apparaat dit bestand hoort.",
				"file":       res,
				"candidates": candidateList(cands),
			})
			return
		}
		device = v
		match = "only"
		if len(cands) > 1 {
			match = "counter"
		}
	}
	for i := range res.Quarters {
		res.Quarters[i].DeviceID = device.ID
	}
	dryRun := q.Get("dry_run") == "true"
	stats, err := s.Recorder.Import(r.Context(), res.Quarters, dryRun)
	if err != nil {
		s.Log.Error("import", "err", err)
		writeError(w, http.StatusInternalServerError, "importeren mislukt")
		return
	}
	if !dryRun {
		s.Log.Info("history imported", "device", device.ID, "kind", res.Kind, "added", stats.Added, "replaced", stats.Replaced, "skipped", stats.Skipped)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dry_run": dryRun, "device": map[string]string{"id": device.ID, "name": device.Name},
		"file": res, "added": stats.Added, "replaced": stats.Replaced, "skipped": stats.Skipped,
		"match": match, "candidates": candidateList(cands),
	})
}

// todayAndTomorrow is the default range of /api/prices: from today's to
// the day after tomorrow's midnight (Dutch time), so 47 or 49 hours around
// a DST change.
func todayAndTomorrow(now time.Time) (from, to time.Time) {
	from, todayEnd := prices.DayBounds(now)
	_, to = prices.DayBounds(todayEnd)
	return from, to
}

// priceRange returns market prices of electricity (EUR/MWh) and gas
// (EUR/m³); default: today and tomorrow (NL time).
func (s *Server) priceRange(w http.ResponseWriter, r *http.Request) {
	from, to := todayAndTomorrow(time.Now())
	var err error
	if v := r.URL.Query().Get("from"); v != "" {
		if from, err = time.Parse(time.RFC3339, v); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'from'")
			return
		}
	}
	if v := r.URL.Query().Get("to"); v != "" {
		if to, err = time.Parse(time.RFC3339, v); err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige 'to'")
			return
		}
	}
	if !to.After(from) || to.Sub(from) > 400*24*time.Hour {
		writeError(w, http.StatusBadRequest, "ongeldige periode")
		return
	}
	// with a contract, each period also gets the price you actually pay:
	// incl. VAT, markup and energy tax
	type power struct {
		Start  time.Time `json:"start"`
		End    time.Time `json:"end"`
		EURMWh float64   `json:"eur_mwh"`
		AllIn  *float64  `json:"all_in_ct,omitempty"` // ct/kWh
		// Contract is the type of the contract of that period (dynamic or
		// fixed), omitted without contract
		Contract string `json:"contract,omitempty"`
		// Hourly: the contract bills the hour's average market price, which
		// AllIn then holds (EURMWh stays the quarter's own price)
		Hourly bool   `json:"hourly,omitempty"`
		Source string `json:"source"`
	}
	type gas struct {
		Start    time.Time `json:"start"`
		End      time.Time `json:"end"`
		EURM3    float64   `json:"eur_m3"`
		AllIn    *float64  `json:"all_in_eur_m3,omitempty"`
		Contract string    `json:"contract,omitempty"`
		Source   string    `json:"source"`
	}
	var timeline costs.Timeline
	if s.Contracts != nil {
		if timeline, err = s.Contracts.List(r.Context()); err != nil {
			s.Log.Error("contracts", "err", err)
		}
	}
	loc := prices.Amsterdam
	if s.Costs != nil && s.Costs.Loc != nil {
		loc = s.Costs.Loc
	}
	optional := func(v float64, ok bool) *float64 {
		if !ok {
			return nil
		}
		return &v
	}
	powerRows, gasRows := []power{}, []gas{}
	var powerStatus, gasStatus prices.Status
	if s.Prices != nil {
		list, err := s.Prices.Range(r.Context(), from, to)
		if err != nil {
			s.Log.Error("prices", "err", err)
			writeError(w, http.StatusInternalServerError, "prijzen niet beschikbaar")
			return
		}
		market := make([]store.MarketPrice, len(list))
		for i, p := range list {
			market[i] = store.MarketPrice{Start: p.Start, Duration: p.Duration, Value: p.Value}
		}
		for _, p := range list {
			value, hourly := p.Value, timeline.HourlyAt(p.Start, loc)
			var allIn *float64
			if hourly {
				// the hour's average, unknown while the hour is incomplete
				if avg, ok := costs.HourAverage(market, p.Start); ok {
					allIn = optional(timeline.ElectricityPriceCt(p.Start, loc, avg))
				}
			} else {
				allIn = optional(timeline.ElectricityPriceCt(p.Start, loc, value))
			}
			powerRows = append(powerRows, power{p.Start, p.End(), value, allIn, timeline.ElectricityType(p.Start, loc), hourly, p.Source})
		}
		powerStatus = s.Prices.Status()
	}
	if s.GasPrices != nil {
		list, err := s.GasPrices.Range(r.Context(), from, to)
		if err != nil {
			s.Log.Error("gas prices", "err", err)
			writeError(w, http.StatusInternalServerError, "prijzen niet beschikbaar")
			return
		}
		for _, p := range list {
			gasRows = append(gasRows, gas{p.Start, p.End(), p.Value, optional(timeline.GasPriceEUR(p.Start, loc, p.Value)), timeline.GasType(p.Start, loc), p.Source})
		}
		gasStatus = s.GasPrices.Status()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"from": from.UTC(), "to": to.UTC(),
		"prices": powerRows, "status": powerStatus,
		"gas": gasRows, "gas_status": gasStatus,
	})
}

// fetchPrices fetches one day now (?date=YYYY-MM-DD, default today;
// ?energy=gas for the gas day starting on that date).
func (s *Server) fetchPrices(w http.ResponseWriter, r *http.Request) {
	svc := s.Prices
	if r.URL.Query().Get("energy") == "gas" {
		svc = s.GasPrices
	}
	if svc == nil || len(svc.Status().Sources) == 0 {
		writeError(w, http.StatusBadRequest, "geen prijsbronnen ingesteld")
		return
	}
	day := time.Now()
	if v := r.URL.Query().Get("date"); v != "" {
		d, err := time.ParseInLocation("2006-01-02", v, prices.Amsterdam)
		if err != nil {
			writeError(w, http.StatusBadRequest, "ongeldige datum (JJJJ-MM-DD)")
			return
		}
		day = d
	}
	src, n, err := svc.Fetch(r.Context(), day)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"source": src, "periods": n})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := decodeOne(dec, v); err != nil {
		writeError(w, http.StatusBadRequest, "ongeldige JSON")
		return false
	}
	return true
}

// decodeOne decodes exactly one JSON value from dec: anything but white
// space after it is an error, so a body is never read as only its start.
func decodeOne(dec *json.Decoder, v any) error {
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("na de JSON volgt nog meer")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
