package tlsconfig

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// SelfSigned returns a TLS config with a server certificate signed by a
// local CA stored in dataDir/tls. The CA certificate (returned as PEM) must be
// installed once on every phone or PC that opens the PWA. The server
// certificate is reissued when its names change or it nears expiry.
func SelfSigned(dataDir string, extraNames []string) (*tls.Config, []byte, error) {
	dir := filepath.Join(dataDir, "tls")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, nil, err
	}
	caCert, caKey, caPEM, err := loadOrCreateCA(dir)
	if err != nil {
		return nil, nil, err
	}
	names, ips := serverNames(extraNames)
	cert, err := loadOrCreateServer(dir, caCert, caKey, names, ips)
	if err != nil {
		return nil, nil, err
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"h2", "http/1.1"},
	}, caPEM, nil
}

func loadOrCreateCA(dir string) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	certPath, keyPath := filepath.Join(dir, "ca.crt"), filepath.Join(dir, "ca.key")
	if certPEM, err := os.ReadFile(certPath); err == nil {
		cert, key, err := parsePair(certPEM, keyPath)
		if err == nil && time.Until(cert.NotAfter) > 30*24*time.Hour {
			return cert, key, certPEM, nil
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{CommonName: "HomeWizardLocal CA " + host, Organization: []string{"HomeWizardLocal"}},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := writeKey(keyPath, key); err != nil {
		return nil, nil, nil, err
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return nil, nil, nil, err
	}
	cert, _ := x509.ParseCertificate(der)
	return cert, key, certPEM, nil
}

func loadOrCreateServer(dir string, ca *x509.Certificate, caKey *ecdsa.PrivateKey, names []string, ips []net.IP) (tls.Certificate, error) {
	certPath, keyPath := filepath.Join(dir, "server.crt"), filepath.Join(dir, "server.key")
	if certPEM, err := os.ReadFile(certPath); err == nil {
		if cert, _, err := parsePair(certPEM, keyPath); err == nil &&
			time.Until(cert.NotAfter) > 30*24*time.Hour && coversAll(cert, names, ips) && cert.CheckSignatureFrom(ca) == nil {
			return tls.LoadX509KeyPair(certPath, keyPath)
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		IPAddresses:  ips,
		NotBefore:    time.Now().Add(-time.Hour),
		// Apple devices reject server certificates valid longer than 398 days.
		NotAfter:    time.Now().AddDate(0, 0, 397),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return tls.Certificate{}, err
	}
	if err := writeKey(keyPath, key); err != nil {
		return tls.Certificate{}, err
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return tls.Certificate{}, err
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

// serverNames returns the host names and LAN IP addresses of this machine.
func serverNames(extra []string) ([]string, []net.IP) {
	names := []string{"localhost"}
	if host, err := os.Hostname(); err == nil && host != "" {
		host = strings.ToLower(host)
		names = append(names, host)
		if !strings.Contains(host, ".") {
			names = append(names, host+".local")
		}
	}
	ips := []net.IP{net.IPv4(127, 0, 0, 1)}
	for _, n := range extra {
		n = strings.TrimSpace(strings.ToLower(n))
		if n == "" {
			continue
		}
		if ip := net.ParseIP(n); ip != nil {
			ips = append(ips, ip)
		} else {
			names = append(names, n)
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok {
				if ip := ipnet.IP.To4(); ip != nil && ip.IsPrivate() {
					ips = append(ips, ip)
				}
			}
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	// keep a useful CN first
	if len(names) > 1 && names[0] == "localhost" {
		names[0], names[1] = names[1], names[0]
	}
	slices.SortFunc(ips, func(a, b net.IP) int { return strings.Compare(a.String(), b.String()) })
	ips = slices.CompactFunc(ips, func(a, b net.IP) bool { return a.Equal(b) })
	return names, ips
}

func coversAll(cert *x509.Certificate, names []string, ips []net.IP) bool {
	for _, n := range names {
		if !slices.Contains(cert.DNSNames, n) {
			return false
		}
	}
	for _, ip := range ips {
		if !slices.ContainsFunc(cert.IPAddresses, ip.Equal) {
			return false
		}
	}
	return true
}

func parsePair(certPEM []byte, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil, errors.New("invalid certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, nil, err
	}
	kb, _ := pem.Decode(keyPEM)
	if kb == nil {
		return nil, nil, errors.New("invalid key PEM")
	}
	key, err := x509.ParseECPrivateKey(kb.Bytes)
	if err != nil {
		return nil, nil, err
	}
	return cert, key, nil
}

func writeKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600)
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(fmt.Sprintf("tls: random serial: %v", err))
	}
	return n
}
