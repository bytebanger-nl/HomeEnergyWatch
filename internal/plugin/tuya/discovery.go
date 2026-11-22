package tuya

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/binary"
	"encoding/json"
	"net"
	"sync"
	"time"
)

// udpKey is the well-known key Tuya uses for its UDP discovery broadcasts
// (md5 of a fixed string). Not a secret; it is the same for every device.
var udpKey = func() []byte { s := md5.Sum([]byte("yGAdlopoPVldABfn")); return s[:] }()

// lanInfo is what LAN discovery learns about a device: where it is and which
// protocol version it speaks.
type lanInfo struct {
	IP      string
	Version Version
}

// broadcast is the JSON a device sends in its discovery packet.
type broadcast struct {
	IP      string `json:"ip"`
	GwID    string `json:"gwId"`
	Version string `json:"version"`
}

// decodeBroadcast decodes one UDP discovery packet. Port 6666 is plaintext;
// 6667/7000 are AES-ECB (older) or AES-GCM/0x6699 (newer) with the UDP key.
func decodeBroadcast(data []byte) (broadcast, bool) {
	var payload []byte
	switch {
	case len(data) >= 8 && binary.BigEndian.Uint32(data) == prefix6699:
		if len(data) < 18+12+16+4 {
			return broadcast{}, false
		}
		c, _ := aes.NewCipher(udpKey)
		g, err := cipher.NewGCM(c)
		if err != nil {
			return broadcast{}, false
		}
		length := int(binary.BigEndian.Uint32(data[14:]))
		end := 18 + length
		if end > len(data)-4 || end < 30 {
			return broadcast{}, false
		}
		p, err := g.Open(nil, data[18:30], data[30:end], data[4:18])
		if err != nil {
			return broadcast{}, false
		}
		if len(p) >= 4 && p[0] != '{' {
			p = p[4:]
		}
		payload = p
	case len(data) >= 24 && binary.BigEndian.Uint32(data) == prefix55aa:
		enc := data[20 : len(data)-8]
		if p, err := ecbDecrypt(udpKey, enc); err == nil {
			payload = p
		} else if len(enc) > 0 && enc[0] == '{' {
			payload = enc
		} else {
			return broadcast{}, false
		}
	default:
		if len(data) > 0 && data[0] == '{' {
			payload = data
		} else {
			return broadcast{}, false
		}
	}
	var b broadcast
	if err := json.Unmarshal(payload, &b); err != nil || b.GwID == "" {
		return broadcast{}, false
	}
	return b, true
}

// discoverLAN listens for Tuya discovery broadcasts for the given duration and
// returns what it heard, keyed by device id. Blocked inbound UDP (e.g. a
// Windows firewall) simply yields nothing; matchByScan is the fallback.
func discoverLAN(ctx context.Context, d time.Duration) map[string]lanInfo {
	out := map[string]lanInfo{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	deadline := time.Now().Add(d)
	for _, port := range []int{6666, 6667, 7000} {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: port})
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(conn *net.UDPConn) {
			defer wg.Done()
			defer conn.Close()
			conn.SetReadDeadline(deadline)
			buf := make([]byte, 4096)
			for {
				if ctx.Err() != nil {
					return
				}
				n, _, err := conn.ReadFromUDP(buf)
				if err != nil {
					return
				}
				b, ok := decodeBroadcast(buf[:n])
				if !ok || b.IP == "" {
					continue
				}
				mu.Lock()
				out[b.GwID] = lanInfo{IP: b.IP, Version: Version(b.Version)}
				mu.Unlock()
			}
		}(conn)
	}
	wg.Wait()
	return out
}

// matchByScan scans the local /24 networks for open port 6668 and identifies
// each host by trying the handshake with every device's key and version. It is
// the firewall-proof fallback when no broadcast is heard, and also fills in the
// protocol version for devices that broadcast without one.
func matchByScan(ctx context.Context, devices []cloudDevice, already map[string]lanInfo) map[string]lanInfo {
	// Which devices still need locating?
	need := map[string]cloudDevice{}
	for _, d := range devices {
		if len(d.LocalKey) != 16 {
			continue
		}
		if info, ok := already[d.ID]; ok && info.IP != "" && info.Version.known() {
			continue
		}
		need[d.ID] = d
	}
	if len(need) == 0 {
		return already
	}

	hosts := localHosts()
	taken := map[string]bool{} // ip already matched
	for ip := range usedIPs(already) {
		taken[ip] = true
	}

	out := map[string]lanInfo{}
	for k, v := range already {
		out[k] = v
	}
	var mu sync.Mutex
	sem := make(chan struct{}, 48)
	var wg sync.WaitGroup
	for _, ip := range hosts {
		mu.Lock()
		done := len(need) == 0
		skip := taken[ip]
		mu.Unlock()
		if done {
			break
		}
		if skip || !tcpOpen(ctx, ip, defaultPort, 400*time.Millisecond) {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			mu.Lock()
			cands := make([]cloudDevice, 0, len(need))
			for _, d := range need {
				cands = append(cands, d)
			}
			mu.Unlock()
			for _, d := range cands {
				if ver, ok := identify(ctx, ip, d); ok {
					mu.Lock()
					out[d.ID] = lanInfo{IP: ip, Version: ver}
					delete(need, d.ID)
					taken[ip] = true
					mu.Unlock()
					return
				}
			}
		}(ip)
	}
	wg.Wait()
	return out
}

// identify tries the handshake with a device's key at each protocol version,
// returning the first that yields a status. A match proves this host is that
// device.
func identify(ctx context.Context, ip string, d cloudDevice) (Version, bool) {
	for _, v := range []Version{V35, V34, V33} {
		qctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		dev := Device{Addr: ip, ID: d.ID, Key: []byte(d.LocalKey), Version: v}
		_, err := dev.Query(qctx)
		cancel()
		if err == nil {
			return v, true
		}
	}
	return "", false
}

func usedIPs(m map[string]lanInfo) map[string]struct{} {
	out := map[string]struct{}{}
	for _, v := range m {
		if v.IP != "" {
			out[v.IP] = struct{}{}
		}
	}
	return out
}

// tcpOpen reports whether a TCP port accepts a connection.
func tcpOpen(ctx context.Context, ip, port string, timeout time.Duration) bool {
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

// localHosts returns the IPv4 hosts of every private /24 the machine is on,
// capped so discovery cannot enumerate a huge range.
func localHosts() []string {
	const maxHosts = 1024
	addrs, _ := net.InterfaceAddrs()
	seen := map[string]bool{}
	var out []string
	for _, a := range addrs {
		n, ok := a.(*net.IPNet)
		if !ok || n.IP.To4() == nil || !n.IP.IsPrivate() {
			continue
		}
		ip := n.IP.To4()
		base := net.IPv4(ip[0], ip[1], ip[2], 0).To4()
		for last := 1; last < 255 && len(out) < maxHosts; last++ {
			h := net.IPv4(base[0], base[1], base[2], byte(last)).String()
			if !seen[h] {
				seen[h] = true
				out = append(out, h)
			}
		}
	}
	return out
}
