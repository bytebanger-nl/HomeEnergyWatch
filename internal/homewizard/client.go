package homewizard

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client reads one device. It is safe for concurrent use.
type Client struct {
	// Host is an IP address or hostname, optionally with a port.
	Host string
	// APIVersion selects HTTP (1) or HTTPS (2).
	APIVersion int
	// Token is the bearer token for API v2.
	Token string
	// ServerName is the expected certificate identity for API v2
	// (appliance/<type>/<serial>). When empty only the chain is verified.
	ServerName string

	httpClient *http.Client
}

// Options configures the transport of clients.
type Options struct {
	// Timeout per request. Defaults to 5 seconds.
	Timeout time.Duration
	// RootCAs used to verify API v2 devices. Defaults to the HomeWizard
	// Appliance Access CA.
	RootCAs *x509.CertPool
}

// NewClient creates a client for one device.
func NewClient(host string, apiVersion int, token, serverName string, opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 5 * time.Second
	}
	c := &Client{Host: host, APIVersion: apiVersion, Token: token, ServerName: serverName}
	transport := &http.Transport{
		Proxy:               nil, // devices are always on the LAN
		DialContext:         (&net.Dialer{Timeout: opts.Timeout, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 2,
		IdleConnTimeout:     90 * time.Second,
	}
	if apiVersion >= 2 {
		transport.TLSClientConfig = deviceTLSConfig(opts.RootCAs, serverName)
	}
	c.httpClient = &http.Client{Timeout: opts.Timeout, Transport: transport}
	return c
}

// Close releases idle connections.
func (c *Client) Close() {
	c.httpClient.CloseIdleConnections()
}

// deviceTLSConfig verifies the device certificate against the HomeWizard CA.
// Devices put their identity in the certificate's common name, which Go's
// standard hostname verification ignores, so it is checked here.
func deviceTLSConfig(roots *x509.CertPool, serverName string) *tls.Config {
	if roots == nil {
		roots = HomeWizardCA()
	}
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // verification is done in VerifyConnection
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return errors.New("homewizard: device presented no certificate")
			}
			leaf := cs.PeerCertificates[0]
			inter := x509.NewCertPool()
			for _, cert := range cs.PeerCertificates[1:] {
				inter.AddCert(cert)
			}
			if _, err := leaf.Verify(x509.VerifyOptions{
				Roots:         roots,
				Intermediates: inter,
				KeyUsages:     []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
			}); err != nil {
				return fmt.Errorf("homewizard: untrusted device certificate: %w", err)
			}
			if serverName == "" {
				return nil
			}
			if strings.EqualFold(leaf.Subject.CommonName, serverName) {
				return nil
			}
			for _, name := range leaf.DNSNames {
				if strings.EqualFold(name, serverName) {
					return nil
				}
			}
			return fmt.Errorf("homewizard: certificate is for %q, expected %q", leaf.Subject.CommonName, serverName)
		},
	}
}

func (c *Client) url(path string) string {
	if c.APIVersion >= 2 {
		return "https://" + c.Host + path
	}
	return "http://" + c.Host + path
}

func (c *Client) do(ctx context.Context, method, path string, body any) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.url(path), reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIVersion >= 2 {
		req.Header.Set("X-Api-Version", "2")
		if c.Token != "" {
			req.Header.Set("Authorization", "Bearer "+c.Token)
		}
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func (c *Client) get(ctx context.Context, path string) ([]byte, error) {
	data, status, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	return data, statusError(status, data)
}

func statusError(status int, body []byte) error {
	switch {
	case status >= 200 && status < 300:
		return nil
	case status == http.StatusUnauthorized:
		return ErrUnauthorized
	case status == http.StatusForbidden:
		return ErrAPIDisabled
	case status == http.StatusNotFound:
		return ErrUnsupported
	default:
		msg := strings.TrimSpace(string(body))
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return fmt.Errorf("homewizard: HTTP %d: %s", status, msg)
	}
}

// Info returns GET /api.
func (c *Client) Info(ctx context.Context) (Info, error) {
	data, err := c.get(ctx, "/api")
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("homewizard: invalid /api response: %w", err)
	}
	if info.ProductType == "" || info.Serial == "" {
		return Info{}, errors.New("homewizard: /api response is not from a HomeWizard device")
	}
	return info, nil
}

// Measurement returns the current normalised measurement.
func (c *Client) Measurement(ctx context.Context) (Reading, error) {
	path := "/api/v1/data"
	if c.APIVersion >= 2 {
		path = "/api/measurement"
	}
	data, err := c.get(ctx, path)
	if err != nil {
		return Reading{}, err
	}
	return parseMeasurement(data, time.Now())
}

// SocketState is the switch state of an Energy Socket.
type SocketState struct {
	PowerOn    *bool `json:"power_on,omitempty"`
	SwitchLock *bool `json:"switch_lock,omitempty"`
	Brightness *int  `json:"brightness,omitempty"`
}

// State returns the switch state (Energy Socket, API v1).
func (c *Client) State(ctx context.Context) (SocketState, error) {
	if c.APIVersion >= 2 {
		return SocketState{}, ErrUnsupported
	}
	data, err := c.get(ctx, "/api/v1/state")
	if err != nil {
		return SocketState{}, err
	}
	var s SocketState
	err = json.Unmarshal(data, &s)
	return s, err
}

// SetState changes the switch state (Energy Socket, API v1).
func (c *Client) SetState(ctx context.Context, update SocketState) (SocketState, error) {
	if c.APIVersion >= 2 {
		return SocketState{}, ErrUnsupported
	}
	data, status, err := c.do(ctx, http.MethodPut, "/api/v1/state", update)
	if err != nil {
		return SocketState{}, err
	}
	if err := statusError(status, data); err != nil {
		return SocketState{}, err
	}
	var s SocketState
	err = json.Unmarshal(data, &s)
	return s, err
}

// Identify makes the device blink its LED.
func (c *Client) Identify(ctx context.Context) error {
	path := "/api/v1/identify"
	if c.APIVersion >= 2 {
		path = "/api/system/identify"
	}
	data, status, err := c.do(ctx, http.MethodPut, path, nil)
	if err != nil {
		return err
	}
	return statusError(status, data)
}

// RequestToken asks an API v2 device for a token. It returns
// ErrButtonPending until the button on the device has been pressed.
func (c *Client) RequestToken(ctx context.Context, name string) (string, error) {
	if c.APIVersion < 2 {
		return "", ErrUnsupported
	}
	data, status, err := c.do(ctx, http.MethodPost, "/api/user", map[string]string{"name": "local/" + name})
	if err != nil {
		return "", err
	}
	if status == http.StatusForbidden {
		return "", ErrButtonPending
	}
	if err := statusError(status, data); err != nil {
		return "", err
	}
	var resp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || resp.Token == "" {
		return "", errors.New("homewizard: device returned no token")
	}
	return resp.Token, nil
}
