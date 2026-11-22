package mhi

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

func init() { plugin.Register(provider{}) }

// provider is the MHI WF-RAC provider type.
type provider struct{}

func (provider) Info() plugin.ProviderInfo {
	return plugin.ProviderInfo{
		Type:        "mhi",
		Name:        "Mitsubishi Heavy (WF-RAC)",
		Description: "Airco's van Mitsubishi Heavy Industries met de WF-RAC wifi-module (Smart M-Air). Lokaal. Bedienen kan als je dat toestaat.",
		InstanceForm: []plugin.Field{
			{Key: "cidr", Label: "Netwerkbereik om te scannen", Type: plugin.FieldText,
				Help: "Bijv. 192.168.1.0/24. Leeg laten om het eigen netwerk te gebruiken."},
			{Key: "max_on_hours", Label: "Automatisch uitzetten na (uur)", Type: plugin.FieldNumber,
				Help: "Een airco die deze app heeft aangezet, gaat na zoveel uur weer uit. Leeg of 0 = nooit. De airco heeft zelf geen timer; dit werkt alleen zolang de gateway draait."},
		},
		DeviceForm: []plugin.Field{
			{Key: "address", Label: "IP-adres", Type: plugin.FieldText, Required: true,
				Help: "Het IP-adres van de airco in je netwerk."},
		},
		CanScan:         true,
		CanAddManual:    true,
		CanLinkMeter:    true,
		CanRegulate:     true,
		IntervalSeconds: 30,
	}
}

func (provider) Instance(config map[string]string, _ plugin.Env) (plugin.Instance, error) {
	return &instance{cidr: config["cidr"], cli: newClient(), watch: plugin.NewWatchdog(config["max_on_hours"]),
		control: config["_control"] == "true"}, nil
}

type instance struct {
	cidr    string
	cli     *client
	watch   *plugin.Watchdog
	control bool // the user allowed control (needed for the watchdog to act)
}

func (in *instance) Close() { in.cli.close() }

// Poll reads one unit, read-only.
func (in *instance) Poll(ctx context.Context, dev plugin.Device) (plugin.Reading, error) {
	ip := dev.Address
	if ip == "" {
		return plugin.Reading{}, fmt.Errorf("geen IP-adres")
	}
	airconID := dev.Config["airconId"]
	if airconID == "" {
		if di, err := in.cli.deviceInfo(ctx, ip); err == nil {
			airconID = di.AirconID
		}
	}
	sc, err := in.cli.airconStat(ctx, ip, airconID)
	if err != nil {
		return plugin.Reading{}, err
	}
	st, err := decodeStat(sc.AirconStat)
	if err != nil {
		return plugin.Reading{}, err
	}
	if in.control && in.watch.Expired(dev.ID, st.Power, time.Now()) {
		if err := in.Set(ctx, dev, "power", false); err == nil {
			st.Power = false
		}
	}
	return plugin.Reading{Online: true, Metrics: metrics(st)}, nil
}

func metrics(st State) []plugin.Metric {
	m := []plugin.Metric{
		{Key: "power", Label: "Aan", Kind: plugin.MetricBool, Value: st.Power},
		{Key: "mode", Label: "Modus", Kind: plugin.MetricText, Value: optionLabel(modeOptions, st.Mode)},
		{Key: "preset", Label: "Ingesteld", Kind: plugin.MetricNumber, Value: st.PresetC, Unit: "°C"},
	}
	if st.IndoorC != nil {
		m = append(m, plugin.Metric{Key: "indoor", Label: "Kamer", Kind: plugin.MetricNumber, Value: *st.IndoorC, Unit: "°C"})
	}
	if st.OutdoorC != nil {
		m = append(m, plugin.Metric{Key: "outdoor", Label: "Buiten", Kind: plugin.MetricNumber, Value: *st.OutdoorC, Unit: "°C"})
	}
	if st.Fan != "" {
		m = append(m, plugin.Metric{Key: "fan", Label: "Ventilator", Kind: plugin.MetricText, Value: optionLabel(fanOptions, st.Fan)})
	}
	if st.SwingUD >= 0 {
		m = append(m, plugin.Metric{Key: "swing_ud", Label: "Flap boven/onder", Kind: plugin.MetricText,
			Value: optionLabel(swingUDOptions, strconv.Itoa(st.SwingUD))})
	}
	if st.SwingLR >= 0 {
		m = append(m, plugin.Metric{Key: "swing_lr", Label: "Flap links/rechts", Kind: plugin.MetricText,
			Value: optionLabel(swingLROptions, strconv.Itoa(st.SwingLR))})
	}
	m = append(m, plugin.Metric{Key: "entrust", Label: "3D auto", Kind: plugin.MetricBool, Value: st.Entrust})
	m = append(m, plugin.Metric{Key: "compressor", Label: "Compressor", Kind: plugin.MetricBool, Value: st.CompressorRunning})
	if st.ErrorCode != "" && st.ErrorCode != "00" {
		m = append(m, plugin.Metric{Key: "error", Label: "Storing", Kind: plugin.MetricText, Value: st.ErrorCode})
	}
	return m
}

// Scan probes the configured (or local) /24 networks for units answering on
// port 51443 and confirms each with getDeviceInfo.
func (in *instance) Scan(ctx context.Context) ([]plugin.Candidate, error) {
	hosts := in.hostsToScan()
	if len(hosts) == 0 {
		return nil, fmt.Errorf("geen netwerkbereik om te scannen")
	}
	sem := make(chan struct{}, 64)
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		out []plugin.Candidate
	)
	for _, ip := range hosts {
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			if !portOpen(ctx, ip, 400*time.Millisecond) {
				return
			}
			dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			di, err := in.cli.deviceInfo(dctx, ip)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, plugin.Candidate{
				Address: ip,
				Name:    "Airco " + lastOctet(ip),
				Config:  map[string]string{"airconId": di.AirconID, "mac": di.MacAddress},
			})
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	return out, ctx.Err()
}

func portOpen(ctx context.Context, ip string, timeout time.Duration) bool {
	dctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, "tcp", net.JoinHostPort(ip, port))
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// hostsToScan returns the IPv4 hosts of the configured CIDR, or of every
// private /24 the machine is on. Capped so an over-wide CIDR can't enumerate
// a huge range.
func (in *instance) hostsToScan() []string {
	const maxHosts = 1024
	var nets []*net.IPNet
	if in.cidr != "" {
		_, n, err := net.ParseCIDR(in.cidr)
		if err == nil {
			nets = append(nets, n)
		}
	} else {
		addrs, _ := net.InterfaceAddrs()
		for _, a := range addrs {
			n, ok := a.(*net.IPNet)
			if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
				continue
			}
			// scan the /24 around this address
			ip := n.IP.To4()
			base := net.IPv4(ip[0], ip[1], ip[2], 0).To4()
			nets = append(nets, &net.IPNet{IP: base, Mask: net.CIDRMask(24, 32)})
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, n := range nets {
		ip := n.IP.Mask(n.Mask).To4()
		if ip == nil {
			continue
		}
		for h := cloneIP(ip); n.Contains(h) && len(out) < maxHosts; incIP(h) {
			if h[3] == 0 || h[3] == 255 {
				continue
			}
			s := h.String()
			if !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out
}

func cloneIP(ip net.IP) net.IP {
	c := make(net.IP, len(ip))
	copy(c, ip)
	return c
}

func incIP(ip net.IP) {
	for i := len(ip) - 1; i >= 0; i-- {
		ip[i]++
		if ip[i] != 0 {
			break
		}
	}
}

func lastOctet(ip string) string {
	if i := net.ParseIP(ip).To4(); i != nil {
		return fmt.Sprintf("%d", i[3])
	}
	return ip
}
