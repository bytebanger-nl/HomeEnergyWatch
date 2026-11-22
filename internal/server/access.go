package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// AccessSettings holds the remote-access choices made in the PWA: whether
// Tailscale clients are accepted and which extra host names are allowed.
// They are stored as JSON in a file in the data directory and apply at once.
// The startup options (Server.AllowTailscale, Server.AllowedHosts) come on
// top and cannot be undone here.
type AccessSettings struct {
	path string

	mu        sync.RWMutex
	tailscale bool
	hosts     []string
}

type accessFile struct {
	Tailscale bool     `json:"tailscale"`
	Hosts     []string `json:"hosts"`
}

// LoadAccessSettings reads path; a missing file means everything off. On
// a damaged file the settings are off and the error is returned, so the
// caller can warn and carry on.
func LoadAccessSettings(path string) (*AccessSettings, error) {
	a := &AccessSettings{path: path}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return a, err
	}
	var f accessFile
	if err := json.Unmarshal(b, &f); err != nil {
		return a, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	hosts, err := normalizeHosts(f.Hosts)
	if err != nil {
		return a, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	a.tailscale, a.hosts = f.Tailscale, hosts
	return a, nil
}

func (a *AccessSettings) get() (bool, []string) {
	if a == nil {
		return false, nil
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.tailscale, a.hosts
}

func (a *AccessSettings) set(tailscale bool, hosts []string) error {
	b, err := json.MarshalIndent(accessFile{Tailscale: tailscale, Hosts: hosts}, "", "  ")
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	tmp := a.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("instelling opslaan: %w", err)
	}
	if err := os.Rename(tmp, a.path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("instelling opslaan: %w", err)
	}
	a.tailscale, a.hosts = tailscale, hosts
	return nil
}

const maxAccessHosts = 20

// normalizeHosts checks host names entered in the PWA: DNS names with at
// least one dot (names without a dot and IP addresses are always allowed),
// no wildcard. It lowercases, trims and drops duplicates.
func normalizeHosts(in []string) ([]string, error) {
	out := []string{}
	for _, h := range in {
		h = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(h)), ".")
		if h == "" || slices.Contains(out, h) {
			continue
		}
		if !validHostName(h) {
			return nil, fmt.Errorf("ongeldige hostnaam %q", h)
		}
		out = append(out, h)
	}
	if len(out) > maxAccessHosts {
		return nil, fmt.Errorf("maximaal %d hostnamen", maxAccessHosts)
	}
	return out, nil
}

func validHostName(h string) bool {
	if len(h) > 253 || !strings.Contains(h, ".") || net.ParseIP(h) != nil {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
				return false
			}
		}
	}
	return true
}

// AccessStatus is returned by GET and PUT /api/settings/access.
type AccessStatus struct {
	// Tailscale: Tailscale clients are accepted (option or setting).
	Tailscale bool `json:"tailscale"`
	// Setting is the PWA setting itself, without the startup option.
	Setting bool `json:"setting"`
	// TailscaleFromOption: the startup option forces it on.
	TailscaleFromOption bool `json:"tailscale_from_option"`
	// Hosts are the names set in the PWA; OptionHosts come from the startup
	// options and this machine's name.
	Hosts       []string `json:"hosts"`
	OptionHosts []string `json:"option_hosts"`
	// Addresses are this machine's Tailscale IPv4 addresses, if any.
	Addresses []string `json:"addresses"`
	// ViaTailscale: this request came over Tailscale; only switching off is
	// allowed then.
	ViaTailscale bool `json:"via_tailscale"`
}

func (s *Server) accessStatus(r *http.Request) AccessStatus {
	on, hosts := s.Access.get()
	st := AccessStatus{
		Tailscale: on || s.AllowTailscale, Setting: on, TailscaleFromOption: s.AllowTailscale,
		Hosts: append([]string{}, hosts...), OptionHosts: append([]string{}, s.AllowedHosts...),
		Addresses: tailnetAddresses(), ViaTailscale: isTailnetClient(r.RemoteAddr),
	}
	return st
}

func (s *Server) getAccess(w http.ResponseWriter, r *http.Request) {
	if s.Access == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return
	}
	writeJSON(w, http.StatusOK, s.accessStatus(r))
}

func (s *Server) putAccess(w http.ResponseWriter, r *http.Request) {
	if s.Access == nil {
		writeError(w, http.StatusNotFound, "niet beschikbaar")
		return
	}
	var body struct {
		Tailscale bool     `json:"tailscale"`
		Hosts     []string `json:"hosts"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	hosts, err := normalizeHosts(body.Hosts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	on, current := s.Access.get()
	// over Tailscale, remote access may be narrowed but never widened: it can
	// only be switched on from the LAN or this machine
	if isTailnetClient(r.RemoteAddr) && (body.Tailscale && !on || slices.ContainsFunc(hosts, func(h string) bool { return !slices.Contains(current, h) })) {
		writeError(w, http.StatusForbidden, "toegang op afstand kun je alleen vanaf het lokale netwerk aanzetten")
		return
	}
	if err := s.Access.set(body.Tailscale, hosts); err != nil {
		s.Log.Error("access settings", "err", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.Log.Info("toegang op afstand gewijzigd", "tailscale", body.Tailscale, "hosts", hosts, "client", r.RemoteAddr)
	writeJSON(w, http.StatusOK, s.accessStatus(r))
}

// isTailnetClient reports whether the client address is in 100.64.0.0/10.
func isTailnetClient(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && tailnet.Contains(addr.Unmap())
}

// tailnetAddresses lists this machine's addresses in 100.64.0.0/10, so the
// PWA can show where the gateway is reachable over Tailscale.
var tailnetAddresses = func() []string {
	out := []string{}
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if addr, ok := netip.AddrFromSlice(n.IP); ok && tailnet.Contains(addr.Unmap()) {
				out = append(out, addr.Unmap().String())
			}
		}
	}
	return out
}
