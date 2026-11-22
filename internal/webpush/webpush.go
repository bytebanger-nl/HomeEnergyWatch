// Package webpush sends Web Push messages (RFC 8030) to browsers: payloads
// are encrypted with aes128gcm (RFC 8291) and requests carry a VAPID
// signature (RFC 8292). Only the standard library is used.
//
// The push service is the browser vendor's (Google, Mozilla, Apple), so a
// message leaves the LAN; its content is end-to-end encrypted.
package webpush

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// recordSize is the aes128gcm record size; a message is one record.
const recordSize = 4096

// MaxPayload is the largest payload that fits one record.
const MaxPayload = recordSize - 16 - 1 - 86

// ErrGone means the subscription no longer exists (HTTP 404 or 410) and
// should be removed.
var ErrGone = errors.New("webpush: subscription expired or removed")

// Subscription is what PushSubscription.toJSON() gives in the browser.
type Subscription struct {
	Endpoint string
	P256DH   string // base64url, the browser's ECDH public key
	Auth     string // base64url, 16 byte authentication secret
}

// Sender signs and sends messages.
type Sender struct {
	Key *ecdsa.PrivateKey // VAPID key (P-256)
	// Subject is the contact for the push service: a mailto: or https: URL.
	Subject string
	Client  *http.Client // defaults to a client with a 15 s timeout
}

// Options for one message.
type Options struct {
	TTL     time.Duration // how long the push service keeps it; default 12 h
	Urgency string        // very-low, low, normal or high; default high
	Topic   string        // replaces an undelivered message with the same topic
}

var b64 = base64.RawURLEncoding

// decode accepts base64url with or without padding (browsers differ).
func decode(s string) ([]byte, error) {
	return b64.DecodeString(strings.TrimRight(s, "="))
}

// PublicKey returns the VAPID public key as the browser wants it for
// applicationServerKey: the uncompressed point, base64url.
func (s *Sender) PublicKey() string {
	pub, err := s.Key.PublicKey.ECDH()
	if err != nil {
		return ""
	}
	return b64.EncodeToString(pub.Bytes())
}

// Send encrypts payload for sub and posts it to the push service.
func (s *Sender) Send(ctx context.Context, sub Subscription, payload []byte, opts Options) error {
	if len(payload) > MaxPayload {
		return fmt.Errorf("webpush: payload of %d bytes is too large", len(payload))
	}
	u, err := url.Parse(sub.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return errors.New("webpush: endpoint is not an https URL")
	}
	uaPub, err := decode(sub.P256DH)
	if err != nil {
		return fmt.Errorf("webpush: p256dh: %w", err)
	}
	auth, err := decode(sub.Auth)
	if err != nil {
		return fmt.Errorf("webpush: auth: %w", err)
	}
	asPriv, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	body, err := encrypt(payload, uaPub, auth, asPriv, salt)
	if err != nil {
		return err
	}
	jwt, err := s.vapidJWT(u.Scheme+"://"+u.Host, time.Now())
	if err != nil {
		return err
	}

	if opts.TTL <= 0 {
		opts.TTL = 12 * time.Hour
	}
	if opts.Urgency == "" {
		opts.Urgency = "high"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, sub.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Content-Encoding", "aes128gcm")
	req.Header.Set("TTL", strconv.Itoa(int(opts.TTL.Seconds())))
	req.Header.Set("Urgency", opts.Urgency)
	if opts.Topic != "" {
		req.Header.Set("Topic", opts.Topic)
	}
	req.Header.Set("Authorization", "vapid t="+jwt+", k="+s.PublicKey())
	client := s.Client
	if client == nil {
		client = defaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	default:
		return fmt.Errorf("webpush: push service answered HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
}

// encrypt builds an aes128gcm body with a single record (RFC 8291 §3.4).
func encrypt(payload, uaPub, auth []byte, asPriv *ecdh.PrivateKey, salt []byte) ([]byte, error) {
	if len(auth) != 16 {
		return nil, errors.New("webpush: auth secret must be 16 bytes")
	}
	ua, err := ecdh.P256().NewPublicKey(uaPub)
	if err != nil {
		return nil, fmt.Errorf("webpush: p256dh: %w", err)
	}
	secret, err := asPriv.ECDH(ua)
	if err != nil {
		return nil, err
	}
	asPub := asPriv.PublicKey().Bytes()
	keyInfo := "WebPush: info\x00" + string(uaPub) + string(asPub)
	ikm, err := hkdf.Key(sha256.New, secret, auth, keyInfo, 32)
	if err != nil {
		return nil, err
	}
	prk, err := hkdf.Extract(sha256.New, ikm, salt)
	if err != nil {
		return nil, err
	}
	cek, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	if err != nil {
		return nil, err
	}
	nonce, err := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(cek)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	// header: salt (16) | record size (4) | key id length (1) | key id
	out := make([]byte, 0, 86+len(payload)+1+gcm.Overhead())
	out = append(out, salt...)
	out = binary.BigEndian.AppendUint32(out, recordSize)
	out = append(out, byte(len(asPub)))
	out = append(out, asPub...)
	plain := append(append([]byte{}, payload...), 0x02) // last record, no padding
	return gcm.Seal(out, nonce, plain, nil), nil
}

// vapidJWT returns the ES256 token for the push service at audience.
func (s *Sender) vapidJWT(audience string, now time.Time) (string, error) {
	header := b64.EncodeToString([]byte(`{"typ":"JWT","alg":"ES256"}`))
	claims, err := json.Marshal(map[string]any{
		"aud": audience,
		"exp": now.Add(12 * time.Hour).Unix(),
		"sub": s.Subject,
	})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + b64.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	r, sig, err := ecdsa.Sign(rand.Reader, s.Key, digest[:])
	if err != nil {
		return "", err
	}
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sig.FillBytes(raw[32:])
	return unsigned + "." + b64.EncodeToString(raw), nil
}

// LoadOrCreateKey reads the VAPID key from path (PEM) or creates it. The key
// must stay the same: browsers subscribed with its public key.
func LoadOrCreateKey(path string) (*ecdsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		block, _ := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("webpush: no PEM key in %s", path)
		}
		key, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("webpush: %s: %w", path, err)
		}
		if key.Curve != elliptic.P256() {
			return nil, fmt.Errorf("webpush: %s is not a P-256 key", path)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	out := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, out, 0o600); err != nil {
		return nil, err
	}
	return key, nil
}

// defaultClient only reaches push services on the internet: the endpoint
// comes from a browser, so its name could point into the LAN. The address
// is checked when connecting (after DNS), and redirects are not followed.
// With a proxy (HTTPS_PROXY) the connection goes to the proxy, so the
// endpoint's addresses are checked before the request is handed to it.
var defaultClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		Proxy:               proxyPublic,
		DialContext:         dial,
		TLSHandshakeTimeout: 10 * time.Second,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

var (
	plainDialer  = &net.Dialer{Timeout: 10 * time.Second}
	publicDialer = &net.Dialer{Timeout: 10 * time.Second, Control: publicOnly}
	// proxies are the addresses (host:port) proxyPublic handed out; only
	// those may be on the LAN
	proxies sync.Map
)

// proxyPublic is http.ProxyFromEnvironment for an endpoint whose addresses
// are all public.
func proxyPublic(req *http.Request) (*url.URL, error) {
	u, err := http.ProxyFromEnvironment(req)
	if err != nil || u == nil {
		return u, err
	}
	host := req.URL.Hostname()
	addrs, err := net.DefaultResolver.LookupNetIP(req.Context(), "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range addrs {
		if !PublicAddr(ip) {
			return nil, fmt.Errorf("webpush: %s is not a public address", ip)
		}
	}
	port := u.Port()
	if port == "" {
		port = map[string]string{"https": "443", "socks5": "1080"}[u.Scheme]
		if port == "" {
			port = "80"
		}
	}
	proxies.Store(net.JoinHostPort(u.Hostname(), port), true)
	return u, nil
}

// dial connects to a proxy from proxyPublic as it is, and anywhere else
// only to a public address.
func dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if _, ok := proxies.Load(addr); ok {
		return plainDialer.DialContext(ctx, network, addr)
	}
	return publicDialer.DialContext(ctx, network, addr)
}

// publicOnly refuses connections to addresses that are not public.
func publicOnly(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return err
	}
	if ip = ip.Unmap(); !PublicAddr(ip) {
		return fmt.Errorf("webpush: %s is not a public address", ip)
	}
	return nil
}

// cgnat is the shared address space (RFC 6598), also used by Tailscale.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// PublicAddr reports whether ip is a public unicast address.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	return ip.IsGlobalUnicast() && !ip.IsPrivate() && !cgnat.Contains(ip)
}
