package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/tsserve"
)

// TailscaleServe publishes the gateway with `tailscale serve` on an HTTPS
// port chosen in the PWA (optional; Server.ServeTarget must be set too).
type TailscaleServe interface {
	Status(ctx context.Context) (tsserve.Status, error)
	Serve(ctx context.Context, port int, target string) error
	Off(ctx context.Context, port int) error
}

// ServeStatus is returned by GET, PUT and DELETE /api/settings/tailscale-serve.
type ServeStatus struct {
	// Available: Tailscale is installed and running; Problem says why not.
	Available bool   `json:"available"`
	Problem   string `json:"problem,omitempty"`
	DNSName   string `json:"dns_name,omitempty"`
	HTTPS     bool   `json:"https"`
	// Target is where Tailscale sends the requests: this gateway.
	Target string `json:"target"`
	// Ports are the HTTPS ports that serve this gateway, URLs their addresses.
	Ports []int    `json:"ports"`
	URLs  []string `json:"urls"`
	// Used are the ports other apps already have in the serve configuration.
	Used []ServePort `json:"used"`
	// ViaTailscale: this request came over the tailnet (only removing is
	// allowed then).
	ViaTailscale bool `json:"via_tailscale"`
}

// ServePort is a port in use by another app.
type ServePort struct {
	Port    int      `json:"port"`
	Proto   string   `json:"proto"`
	Targets []string `json:"targets"`
	Funnel  bool     `json:"funnel,omitempty"`
}

// portInUse reports whether something on this machine accepts connections
// on port, on loopback or on one of its tailnet addresses; a port that
// `tailscale serve` takes would hide it from the tailnet. Replaceable for
// tests.
var portInUse = func(port int) bool {
	for _, host := range append([]string{"127.0.0.1"}, tailnetAddresses()...) {
		c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 300*time.Millisecond)
		if err == nil {
			c.Close()
			return true
		}
	}
	return false
}

func (s *Server) ours(p tsserve.Port) bool {
	return p.Proto == "https" && !p.Funnel && !p.Foreground && p.Proxy == s.ServeTarget
}

func (s *Server) serveStatus(ctx context.Context, r *http.Request) ServeStatus {
	out := ServeStatus{Target: s.ServeTarget, Ports: []int{}, URLs: []string{}, Used: []ServePort{}, ViaTailscale: isTailnetClient(r.RemoteAddr)}
	st, err := s.Serve.Status(ctx)
	switch {
	case errors.Is(err, tsserve.ErrNoCLI):
		out.Problem = "Tailscale is niet geïnstalleerd op deze computer."
		return out
	case err != nil:
		out.Problem = s.serveError(err)
		return out
	case st.BackendState != "Running":
		out.Problem = fmt.Sprintf("Tailscale draait niet of is niet ingelogd (status %s).", st.BackendState)
		return out
	}
	out.Available, out.DNSName, out.HTTPS = true, st.DNSName, st.HTTPS
	for _, p := range st.Ports {
		if s.ours(p) {
			out.Ports = append(out.Ports, p.Port)
			out.URLs = append(out.URLs, serveURL(st.DNSName, p.Port))
		} else {
			out.Used = append(out.Used, ServePort{Port: p.Port, Proto: p.Proto, Targets: append([]string{}, p.Targets...), Funnel: p.Funnel})
		}
	}
	return out
}

func serveURL(name string, port int) string {
	if port == 443 {
		return "https://" + name
	}
	return "https://" + name + ":" + strconv.Itoa(port)
}

func (s *Server) serveAvailable(w http.ResponseWriter) bool {
	if s.Serve == nil || s.ServeTarget == "" {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return false
	}
	return true
}

func (s *Server) getTailscaleServe(w http.ResponseWriter, r *http.Request) {
	if !s.serveAvailable(w) {
		return
	}
	writeJSON(w, http.StatusOK, s.serveStatus(r.Context(), r))
}

// putTailscaleServe publishes the gateway on the chosen HTTPS port after
// checking it is free, moves an earlier registration of this gateway to it
// and allows the node's MagicDNS name as host.
func (s *Server) putTailscaleServe(w http.ResponseWriter, r *http.Request) {
	if !s.serveAvailable(w) {
		return
	}
	if isTailnetClient(r.RemoteAddr) {
		writeError(w, http.StatusForbidden, "Tailscale Serve kun je alleen vanaf het lokale netwerk instellen")
		return
	}
	var body struct {
		Port int `json:"port"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Port < 1 || body.Port > 65535 {
		writeError(w, http.StatusBadRequest, "kies een poort van 1 tot en met 65535")
		return
	}
	ctx := r.Context()
	st, err := s.Serve.Status(ctx)
	if errors.Is(err, tsserve.ErrNoCLI) {
		writeError(w, http.StatusConflict, "Tailscale is niet geïnstalleerd op deze computer")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, s.serveError(err))
		return
	}
	if st.BackendState != "Running" || st.DNSName == "" {
		writeError(w, http.StatusConflict, "Tailscale draait niet of is niet ingelogd")
		return
	}
	if !st.HTTPS {
		writeError(w, http.StatusConflict, "HTTPS-certificaten staan uit in je tailnet: zet in de Tailscale-beheerconsole onder DNS MagicDNS en HTTPS Certificates aan")
		return
	}
	if p, used := st.Find(body.Port); used && !s.ours(p) {
		what := p.Proto
		if len(p.Targets) > 0 {
			what += " " + strings.Join(p.Targets, ", ")
		}
		if p.Funnel {
			what += " (Funnel)"
		}
		writeError(w, http.StatusConflict, fmt.Sprintf("poort %d is al in gebruik door Tailscale Serve: %s", body.Port, what))
		return
	} else if !used && portInUse(body.Port) {
		writeError(w, http.StatusConflict, fmt.Sprintf("poort %d is al in gebruik door een ander programma op deze computer", body.Port))
		return
	}
	// the name has to be allowed before Tailscale starts sending it
	if err := s.allowServeName(st.DNSName); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Serve.Serve(ctx, body.Port, s.ServeTarget); err != nil {
		s.Log.Warn("tailscale serve", "port", body.Port, "err", err)
		writeError(w, http.StatusBadGateway, s.serveError(err))
		return
	}
	for _, p := range st.Ports {
		if p.Port != body.Port && s.ours(p) {
			if err := s.Serve.Off(ctx, p.Port); err != nil {
				s.Log.Warn("tailscale serve off", "port", p.Port, "err", err)
			}
		}
	}
	s.Log.Info("Tailscale Serve ingesteld", "port", body.Port, "target", s.ServeTarget, "client", r.RemoteAddr)
	writeJSON(w, http.StatusOK, s.serveStatus(ctx, r))
}

// deleteTailscaleServe removes every serve port of this gateway; it leaves
// the ports of other apps alone.
func (s *Server) deleteTailscaleServe(w http.ResponseWriter, r *http.Request) {
	if !s.serveAvailable(w) {
		return
	}
	ctx := r.Context()
	st, err := s.Serve.Status(ctx)
	if err != nil {
		writeError(w, http.StatusBadGateway, s.serveError(err))
		return
	}
	for _, p := range st.Ports {
		if s.ours(p) {
			if err := s.Serve.Off(ctx, p.Port); err != nil {
				writeError(w, http.StatusBadGateway, s.serveError(err))
				return
			}
		}
	}
	s.Log.Info("Tailscale Serve uitgezet", "client", r.RemoteAddr)
	writeJSON(w, http.StatusOK, s.serveStatus(ctx, r))
}

// allowServeName adds the MagicDNS name to the PWA's host names, unless a
// startup option or the setting already allows it.
func (s *Server) allowServeName(name string) error {
	if s.allowedHost(name) || s.Access == nil {
		return nil
	}
	on, hosts := s.Access.get()
	if slices.Contains(hosts, name) {
		return nil
	}
	if len(hosts) >= maxAccessHosts {
		return fmt.Errorf("maximaal %d hostnamen; verwijder er een onder Toegang op afstand", maxAccessHosts)
	}
	return s.Access.set(on, append(slices.Clone(hosts), name))
}

// serveError turns the Tailscale errors a user can fix into what to do.
// Tailscale for Windows only takes commands from the Windows user it belongs
// to ("Tailscale already in use by PC\user"), and on Linux only root and
// the operator may change the serve configuration ("Access denied").
func (s *Server) serveError(err error) string {
	msg := err.Error()
	manual := "tailscale serve --bg --https=<poort> " + s.ServeTarget
	if u, perr := url.Parse(s.ServeTarget); perr == nil && u.Scheme == "http" && u.Hostname() == "127.0.0.1" {
		manual = "tailscale serve --bg --https=<poort> " + u.Port()
	}
	switch {
	case strings.Contains(msg, "already in use by"):
		owner := ""
		if _, after, ok := strings.Cut(msg, "already in use by "); ok {
			owner, _, _ = strings.Cut(after, ",")
			owner = " (" + strings.TrimSpace(owner) + ")"
		}
		return "Tailscale hoort bij een Windows-gebruiker" + owner + " en neemt geen opdrachten aan van de gateway, die als service draait. " +
			"Ook met Run unattended blijft dat zo. Stel Serve met de hand in vanuit dat Windows-account: " + manual + ", en zet de Tailscale-naam bij de extra hostnamen hierboven."
	case strings.Contains(msg, "Access denied") || strings.Contains(msg, "access denied"):
		return "Tailscale staat de gateway niet toe Serve in te stellen. Maak de gebruiker waaronder de gateway draait operator: " +
			"sudo tailscale set --operator=<gebruiker>, of stel Serve met de hand in: " + manual + "."
	}
	return msg
}
