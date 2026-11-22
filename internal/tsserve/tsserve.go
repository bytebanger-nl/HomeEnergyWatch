// Package tsserve drives `tailscale serve` through the Tailscale CLI, so the
// PWA can publish the gateway on an HTTPS port of the machine's tailnet name
// without the user typing commands. It only reads the node's status and serve
// configuration and adds or removes one HTTPS port; it never touches Funnel.
package tsserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrNoCLI means the tailscale command was not found on this machine.
var ErrNoCLI = errors.New("Tailscale is niet gevonden op deze computer")

// CLI runs the tailscale command. Run is replaceable for tests; nil runs the
// real binary, looked up on every call so installing Tailscale later works
// without restarting the gateway.
type CLI struct {
	Run func(ctx context.Context, args ...string) ([]byte, error)
}

// Status is what the PWA needs to know about this node.
type Status struct {
	// BackendState is Tailscale's own state ("Running", "NeedsLogin",
	// "Stopped", ...).
	BackendState string
	// DNSName is the node's MagicDNS name without the trailing dot.
	DNSName string
	// HTTPS: the tailnet hands out HTTPS certificates for this node.
	HTTPS bool
	// Ports are the ports `tailscale serve` (or funnel) already uses.
	Ports []Port
}

// Port is one port in the serve configuration.
type Port struct {
	Port int
	// Proto is "https", "http" or "tcp".
	Proto string
	// Targets are the backends: for http(s) "path → proxy" per handler, for
	// tcp the forward address.
	Targets []string
	// Proxy is set when the port has exactly one handler, for "/", proxying
	// to this address.
	Proxy      string
	Funnel     bool
	Foreground bool
}

// Find returns the port entry, if the port is in use.
func (s Status) Find(port int) (Port, bool) {
	i := slices.IndexFunc(s.Ports, func(p Port) bool { return p.Port == port })
	if i < 0 {
		return Port{}, false
	}
	return s.Ports[i], true
}

func (c *CLI) run(ctx context.Context, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if c != nil && c.Run != nil {
		return c.Run(ctx, args...)
	}
	path, err := findCLI()
	if err != nil {
		return nil, err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.WaitDelay = 2 * time.Second
	err = cmd.Run()
	if err != nil {
		out := strings.TrimSpace(stderr.String() + "\n" + stdout.String())
		if ctx.Err() != nil {
			// `tailscale serve` waits when Serve still has to be enabled for
			// the tailnet; what it printed tells the user where
			return nil, fmt.Errorf("Tailscale gaf geen antwoord: %s", limit(out))
		}
		if out == "" {
			return nil, fmt.Errorf("tailscale %s: %w", args[0], err)
		}
		return nil, errors.New(limit(out))
	}
	return stdout.Bytes(), nil
}

func limit(s string) string {
	if s == "" {
		return "geen uitvoer"
	}
	if len(s) > 600 {
		return s[:600] + "…"
	}
	return s
}

// findCLI looks in PATH, then where the installers put it.
func findCLI() (string, error) {
	if p, err := exec.LookPath("tailscale"); err == nil {
		return p, nil
	}
	var candidates []string
	switch runtime.GOOS {
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramW6432"} {
			if dir := os.Getenv(env); dir != "" {
				candidates = append(candidates, filepath.Join(dir, "Tailscale", "tailscale.exe"))
			}
		}
	case "darwin":
		candidates = append(candidates, "/Applications/Tailscale.app/Contents/MacOS/Tailscale", "/usr/local/bin/tailscale", "/opt/homebrew/bin/tailscale")
	default:
		candidates = append(candidates, "/usr/bin/tailscale", "/usr/local/bin/tailscale", "/usr/sbin/tailscale")
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	return "", ErrNoCLI
}

// Status reads `tailscale status --json` and `tailscale serve status --json`.
func (c *CLI) Status(ctx context.Context) (Status, error) {
	out, err := c.run(ctx, 10*time.Second, "status", "--json", "--peers=false")
	if err != nil {
		return Status{}, err
	}
	st, err := parseStatus(out)
	if err != nil {
		return Status{}, err
	}
	if st.BackendState != "Running" {
		return st, nil
	}
	out, err = c.run(ctx, 10*time.Second, "serve", "status", "--json")
	if err != nil {
		return st, err
	}
	st.Ports, err = parseServe(out)
	return st, err
}

// Serve publishes target (e.g. http://127.0.0.1:8080) on HTTPS port port,
// in the background so it stays after a restart.
func (c *CLI) Serve(ctx context.Context, port int, target string) error {
	_, err := c.run(ctx, 20*time.Second, "serve", "--bg", "--https="+strconv.Itoa(port), target)
	return err
}

// Off removes the serve configuration of HTTPS port port.
func (c *CLI) Off(ctx context.Context, port int) error {
	_, err := c.run(ctx, 20*time.Second, "serve", "--https="+strconv.Itoa(port), "off")
	return err
}

func parseStatus(b []byte) (Status, error) {
	var raw struct {
		BackendState string
		Self         *struct{ DNSName string }
		CertDomains  []string
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Status{}, fmt.Errorf("tailscale status: %w", err)
	}
	st := Status{BackendState: raw.BackendState, HTTPS: len(raw.CertDomains) > 0}
	if raw.Self != nil {
		st.DNSName = strings.TrimSuffix(raw.Self.DNSName, ".")
	}
	return st, nil
}

// serveConfig is the part of Tailscale's ipn.ServeConfig read here.
type serveConfig struct {
	TCP map[string]*struct {
		HTTPS      bool
		HTTP       bool
		TCPForward string
	}
	Web map[string]*struct {
		Handlers map[string]*struct {
			Path  string
			Proxy string
			Text  string
		}
	}
	AllowFunnel map[string]bool
	Foreground  map[string]*serveConfig
}

func parseServe(b []byte) ([]Port, error) {
	var cfg serveConfig
	if len(bytes.TrimSpace(b)) > 0 {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return nil, fmt.Errorf("tailscale serve status: %w", err)
		}
	}
	ports := cfg.ports(false)
	for _, fg := range cfg.Foreground {
		if fg != nil {
			ports = append(ports, fg.ports(true)...)
		}
	}
	slices.SortFunc(ports, func(a, b Port) int { return a.Port - b.Port })
	return ports, nil
}

func (cfg *serveConfig) ports(foreground bool) []Port {
	var out []Port
	for key, h := range cfg.TCP {
		n, err := strconv.Atoi(key)
		if err != nil || h == nil {
			continue
		}
		p := Port{Port: n, Proto: "tcp", Foreground: foreground}
		switch {
		case h.HTTPS:
			p.Proto = "https"
		case h.HTTP:
			p.Proto = "http"
		default:
			if h.TCPForward != "" {
				p.Targets = []string{h.TCPForward}
			}
		}
		for hostPort, web := range cfg.Web {
			if !strings.HasSuffix(hostPort, ":"+key) || web == nil {
				continue
			}
			if cfg.AllowFunnel[hostPort] {
				p.Funnel = true
			}
			paths := make([]string, 0, len(web.Handlers))
			for path := range web.Handlers {
				paths = append(paths, path)
			}
			slices.Sort(paths)
			for _, path := range paths {
				hd := web.Handlers[path]
				if hd == nil {
					continue
				}
				switch {
				case hd.Proxy != "":
					p.Targets = append(p.Targets, path+" → "+hd.Proxy)
				case hd.Path != "":
					p.Targets = append(p.Targets, path+" → "+hd.Path)
				default:
					p.Targets = append(p.Targets, path+" → tekst")
				}
			}
			if len(web.Handlers) == 1 && web.Handlers["/"] != nil {
				p.Proxy = web.Handlers["/"].Proxy
			}
		}
		out = append(out, p)
	}
	return out
}
