package homewizard

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// testCA creates a CA and a device certificate whose identity is only in the
// common name, like HomeWizard devices.
func testCA(t *testing.T, cn string) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pool, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func v2Server(t *testing.T, cert tls.Certificate, handler http.Handler) string {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "https://")
}

func TestV2CertificateVerification(t *testing.T) {
	const name = "appliance/p1dongle/aabbccddeeff"
	pool, cert := testCA(t, name)
	host := v2Server(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOKEN" || r.Header.Get("X-Api-Version") != "2" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"power_w": 12}`))
	}))
	ctx := context.Background()

	ok := NewClient(host, 2, "TOKEN", name, Options{RootCAs: pool})
	if r, err := ok.Measurement(ctx); err != nil || r.Values["power_w"] != 12 {
		t.Fatalf("valid device: %v %v", r, err)
	}
	if _, err := NewClient(host, 2, "TOKEN", "", Options{RootCAs: pool}).Measurement(ctx); err != nil {
		t.Fatalf("chain-only verification should pass: %v", err)
	}
	if _, err := NewClient(host, 2, "TOKEN", "appliance/p1dongle/000000000000", Options{RootCAs: pool}).Measurement(ctx); err == nil {
		t.Fatal("wrong identity must be rejected")
	}
	if _, err := NewClient(host, 2, "TOKEN", name, Options{}).Measurement(ctx); err == nil {
		t.Fatal("certificate from an unknown CA must be rejected")
	}
	if _, err := NewClient(host, 2, "WRONG", name, Options{RootCAs: pool}).Measurement(ctx); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("bad token: %v", err)
	}
}

// An expired (or not yet valid) device certificate is refused, even when it
// was signed by the trusted CA and carries the expected identity.
func TestV2ExpiredCertificateRejected(t *testing.T) {
	const name = "appliance/p1dongle/aabbccddeeff"
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(48 * time.Hour),
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	for label, window := range map[string][2]time.Duration{
		"expired":       {-48 * time.Hour, -24 * time.Hour},
		"not yet valid": {24 * time.Hour, 48 * time.Hour},
	} {
		key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: name},
			NotBefore: time.Now().Add(window[0]), NotAfter: time.Now().Add(window[1]),
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		var sawToken atomic.Bool
		host := v2Server(t, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sawToken.Store(r.Header.Get("Authorization") != "")
			_, _ = w.Write([]byte(`{"power_w": 1}`))
		}))
		if _, err := NewClient(host, 2, "TOKEN", name, Options{RootCAs: pool}).Measurement(context.Background()); err == nil {
			t.Errorf("%s certificate accepted", label)
		}
		if sawToken.Load() {
			t.Errorf("%s: token sent to an untrusted device", label)
		}
	}
}

func TestRequestToken(t *testing.T) {
	pool, cert := testCA(t, "appliance/battery/aabbccddeeff")
	var calls atomic.Int32
	host := v2Server(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/user" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if calls.Add(1) < 2 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"user:creation-not-enabled"}`))
			return
		}
		_, _ = w.Write([]byte(`{"token":"ABCDEF0123456789ABCDEF0123456789","name":"local/x"}`))
	}))
	c := NewClient(host, 2, "", "appliance/battery/aabbccddeeff", Options{RootCAs: pool})
	if _, err := c.RequestToken(context.Background(), "x"); !errors.Is(err, ErrButtonPending) {
		t.Fatalf("first attempt: %v", err)
	}
	token, err := c.RequestToken(context.Background(), "x")
	if err != nil || token != "ABCDEF0123456789ABCDEF0123456789" {
		t.Fatalf("second attempt: %q %v", token, err)
	}
}

func TestV1Socket(t *testing.T) {
	on := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api":
			_, _ = w.Write([]byte(`{"product_type":"HWE-SKT","serial":"3c39e7aabbcc","product_name":"Energy Socket","firmware_version":"4.0","api_version":"v1"}`))
		case r.URL.Path == "/api/v1/state" && r.Method == http.MethodPut:
			on = false
			_, _ = w.Write([]byte(`{"power_on":false,"switch_lock":false}`))
		case r.URL.Path == "/api/v1/state":
			_, _ = w.Write([]byte(`{"power_on":true,"switch_lock":false}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	c := NewClient(strings.TrimPrefix(srv.URL, "http://"), 1, "", "", Options{})
	info, err := c.Info(context.Background())
	if err != nil || info.ProductType != ProductSocket {
		t.Fatalf("info: %+v %v", info, err)
	}
	off := false
	s, err := c.SetState(context.Background(), SocketState{PowerOn: &off})
	if err != nil || s.PowerOn == nil || *s.PowerOn || on {
		t.Fatalf("set state: %+v %v", s, err)
	}
}
