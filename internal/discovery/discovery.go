// Package discovery finds HomeWizard devices on the LAN via mDNS.
package discovery

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/grandcat/zeroconf"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
)

// Services announced by HomeWizard devices.
const (
	ServiceV1 = "_hwenergy._tcp"   // API v1, HTTP port 80
	ServiceV2 = "_homewizard._tcp" // API v2, HTTPS port 443
)

// Run browses for devices every interval until ctx is done and reports each
// device found to sink.
func Run(ctx context.Context, log *slog.Logger, interval time.Duration, sink func(context.Context, gateway.Candidate)) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	for {
		scan(ctx, log, 10*time.Second, sink)
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

func scan(ctx context.Context, log *slog.Logger, window time.Duration, sink func(context.Context, gateway.Candidate)) {
	for _, svc := range []string{ServiceV1, ServiceV2} {
		resolver, err := zeroconf.NewResolver(zeroconf.SelectIPTraffic(zeroconf.IPv4))
		if err != nil {
			log.Warn("mdns unavailable", "err", err)
			return
		}
		entries := make(chan *zeroconf.ServiceEntry, 32)
		sctx, cancel := context.WithTimeout(ctx, window)
		if err := resolver.Browse(sctx, svc, "local.", entries); err != nil {
			cancel()
			log.Warn("mdns browse failed", "service", svc, "err", err)
			continue
		}
		found := 0
		for done := false; !done; {
			select {
			case e, ok := <-entries:
				if !ok {
					done = true
					break
				}
				if c, ok := candidate(svc, e); ok {
					found++
					sink(ctx, c)
				}
			case <-sctx.Done():
				done = true
			}
		}
		cancel()
		log.Debug("mdns scan done", "service", svc, "found", found)
	}
}

func candidate(service string, e *zeroconf.ServiceEntry) (gateway.Candidate, bool) {
	txt := map[string]string{}
	for _, t := range e.Text {
		if k, v, ok := strings.Cut(t, "="); ok {
			txt[strings.ToLower(k)] = v
		}
	}
	var ip net.IP
	for _, a := range e.AddrIPv4 {
		if a.IsPrivate() || a.IsLinkLocalUnicast() {
			ip = a
			break
		}
	}
	if ip == nil && len(e.AddrIPv4) > 0 {
		ip = e.AddrIPv4[0]
	}
	if ip == nil || txt["serial"] == "" || txt["product_type"] == "" {
		return gateway.Candidate{}, false
	}
	c := gateway.Candidate{
		Serial:      txt["serial"],
		ProductType: txt["product_type"],
		ProductName: txt["product_name"],
	}
	host := ip.String()
	if service == ServiceV2 {
		c.APIVersion = 2
		c.APIEnabled = true
		if e.Port != 0 && e.Port != 443 {
			host = net.JoinHostPort(host, strconv.Itoa(e.Port))
		}
	} else {
		c.APIVersion = 1
		c.APIEnabled = txt["api_enabled"] == "1"
		if !c.APIEnabled {
			return gateway.Candidate{}, false // v1 API switched off in the app
		}
		if e.Port != 0 && e.Port != 80 {
			host = net.JoinHostPort(host, strconv.Itoa(e.Port))
		}
	}
	c.Host = host
	return c, true
}
