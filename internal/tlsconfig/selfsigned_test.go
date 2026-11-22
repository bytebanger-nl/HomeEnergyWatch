package tlsconfig

import (
	"bytes"
	"crypto/x509"
	"encoding/pem"
	"testing"
)

func TestSelfSignedIsStableAndValid(t *testing.T) {
	dir := t.TempDir()
	cfg, caPEM, err := SelfSigned(dir, []string{"energie.thuis", "192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(caPEM)
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for _, name := range []string{"energie.thuis", "192.168.1.20", "localhost"} {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: pool}); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// a restart reuses the CA and the server certificate
	cfg2, caPEM2, err := SelfSigned(dir, []string{"energie.thuis", "192.168.1.20"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(caPEM, caPEM2) || !bytes.Equal(cfg.Certificates[0].Certificate[0], cfg2.Certificates[0].Certificate[0]) {
		t.Error("certificates changed on restart")
	}
	// a new name reissues the server certificate but keeps the CA
	cfg3, caPEM3, err := SelfSigned(dir, []string{"energie.thuis", "192.168.1.20", "nieuw.thuis"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(caPEM, caPEM3) || bytes.Equal(cfg.Certificates[0].Certificate[0], cfg3.Certificates[0].Certificate[0]) {
		t.Error("expected a new server certificate under the same CA")
	}
}
