package main

import (
	"fmt"
	"net"
)

// checkPorts reports whether the HTTP and (with TLS on) HTTPS addresses of
// cfg can be listened on.
func checkPorts(cfg config) error {
	if cfg.httpAddr != "" {
		if err := checkPort(cfg.httpAddr); err != nil {
			return err
		}
	}
	if cfg.tlsMode != "off" {
		return checkPort(cfg.httpsAddr)
	}
	return nil
}

// checkPort tries to listen on addr the way the web server does and
// explains why that fails. For a wildcard address IPv4 is also tried on its
// own: on Windows the server's dual-stack socket can be opened next to
// another program's IPv4-only socket, and IPv4 clients would then reach
// that program instead.
func checkPort(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("ongeldig adres %q: %w", addr, err)
	}
	err = tryListen("tcp", addr)
	if err == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		err = tryListen("tcp4", net.JoinHostPort("0.0.0.0", port))
	}
	if err == nil {
		return nil
	}
	switch {
	case portInUse(err):
		return fmt.Errorf("poort %s is al in gebruik door een ander programma; kies een andere poort of stop dat programma", port)
	case portReserved(err):
		return fmt.Errorf("poort %s is door Windows gereserveerd (zie 'netsh interface ipv4 show excludedportrange protocol=tcp'); kies een andere poort", port)
	}
	return fmt.Errorf("poort %s kan niet worden geopend: %w", port, err)
}

func tryListen(network, addr string) error {
	l, err := net.Listen(network, addr)
	if err != nil {
		return err
	}
	return l.Close()
}
