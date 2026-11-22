package webpush

import (
	"context"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mustDecode(t *testing.T, s string) []byte {
	t.Helper()
	b, err := decode(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestEncryptRFC8291 checks the example of RFC 8291 Appendix A.
func TestEncryptRFC8291(t *testing.T) {
	plain := mustDecode(t, "V2hlbiBJIGdyb3cgdXAsIEkgd2FudCB0byBiZSBhIHdhdGVybWVsb24")
	asPriv, err := ecdh.P256().NewPrivateKey(mustDecode(t, "yfWPiYE-n46HLnH0KqZOF1fJJU3MYrct3AELtAQ-oRw"))
	if err != nil {
		t.Fatal(err)
	}
	uaPub := mustDecode(t, "BCVxsr7N_eNgVRqvHtD0zTZsEc6-VV-JvLexhqUzORcxaOzi6-AYWXvTBHm4bjyPjs7Vd8pZGH6SRpkNtoIAiw4")
	auth := mustDecode(t, "BTBZMqHH6r4Tts7J_aSIgg")
	salt := mustDecode(t, "DGv6ra1nlYgDCS1FRnbzlw")
	got, err := encrypt(plain, uaPub, auth, asPriv, salt)
	if err != nil {
		t.Fatal(err)
	}
	want := "DGv6ra1nlYgDCS1FRnbzlwAAEABBBP4z9KsN6nGRTbVYI_c7VJSPQTBtkgcy27ml" +
		"mlMoZIIgDll6e3vCYLocInmYWAmS6TlzAC8wEqKK6PBru3jl7A_yl95bQpu6cVPT" +
		"pK4Mqgkf1CXztLVBSt2Ks3oZwbuwXPXLWyouBWLVWGNWQexSgSxsj_Qulcy4a-fN"
	if b64.EncodeToString(got) != want {
		t.Fatalf("body\n got %s\nwant %s", b64.EncodeToString(got), want)
	}
}

func TestSendSignsAndHandlesGone(t *testing.T) {
	key, err := LoadOrCreateKey(filepath.Join(t.TempDir(), "vapid.pem"))
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := ecdh.P256().GenerateKey(nil)
	var gotAuth, gotEnc string
	status := http.StatusCreated
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotEnc = r.Header.Get("Authorization"), r.Header.Get("Content-Encoding")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	s := &Sender{Key: key, Subject: "mailto:test@example.com", Client: srv.Client()}
	sub := Subscription{Endpoint: srv.URL + "/push/abc", P256DH: b64.EncodeToString(ua.PublicKey().Bytes()), Auth: "BTBZMqHH6r4Tts7J_aSIgg"}
	if err := s.Send(context.Background(), sub, []byte(`{"title":"x"}`), Options{}); err != nil {
		t.Fatal(err)
	}
	if gotEnc != "aes128gcm" {
		t.Fatalf("Content-Encoding %q", gotEnc)
	}
	// Authorization: vapid t=<jwt>, k=<public key>
	parts := strings.SplitN(strings.TrimPrefix(gotAuth, "vapid t="), ", k=", 2)
	if len(parts) != 2 || parts[1] != s.PublicKey() {
		t.Fatalf("Authorization %q", gotAuth)
	}
	seg := strings.Split(parts[0], ".")
	var claims struct {
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(mustDecode(t, seg[1]), &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Aud != srv.URL || claims.Sub != s.Subject || claims.Exp < time.Now().Unix() {
		t.Fatalf("claims %+v", claims)
	}
	sig := mustDecode(t, seg[2])
	digest := sha256.Sum256([]byte(seg[0] + "." + seg[1]))
	if !ecdsa.Verify(&key.PublicKey, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("VAPID signature does not verify")
	}

	status = http.StatusGone
	if err := s.Send(context.Background(), sub, []byte("x"), Options{}); err != ErrGone {
		t.Fatalf("410: err %v, want ErrGone", err)
	}
}

func TestLoadOrCreateKeyKeepsKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "vapid.pem")
	a, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !a.Equal(b) {
		t.Fatal("second load returned another key")
	}
}

// The default client only connects to public addresses (checked after DNS)
// and does not follow redirects.
func TestPublicOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"142.250.1.1:443":     true,
		"[2a00:1450::1]:443":  true,
		"192.168.1.1:443":     false,
		"10.0.0.1:443":        false,
		"127.0.0.1:443":       false,
		"100.64.0.7:443":      false,
		"169.254.1.1:443":     false,
		"[::ffff:10.0.0.1]:1": false,
		"[fd00::1]:443":       false,
		"[::1]:443":           false,
	} {
		if err := publicOnly("tcp", addr, nil); (err == nil) != ok {
			t.Errorf("%s: err = %v", addr, err)
		}
	}
	if defaultClient.CheckRedirect == nil {
		t.Fatal("redirects are followed")
	}
}

// With HTTPS_PROXY the push goes through the proxy (which may be on the
// LAN), but only for an endpoint whose addresses are public.
func TestProxyPublic(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://10.0.0.5:3128")
	req := func(u string) *http.Request {
		r, err := http.NewRequest("POST", u, nil)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	u, err := proxyPublic(req("https://142.250.1.1/push/abc"))
	if err != nil || u == nil || u.Host != "10.0.0.5:3128" {
		t.Fatalf("public endpoint: %v %v", u, err)
	}
	if _, ok := proxies.Load("10.0.0.5:3128"); !ok {
		t.Fatal("proxy address not allowed for dialing")
	}
	if _, err := proxyPublic(req("https://192.168.1.1/push/abc")); err == nil {
		t.Fatal("LAN endpoint sent to the proxy")
	}
	if _, ok := proxies.Load("10.0.0.6:3128"); ok {
		t.Fatal("other LAN address allowed")
	}
}
