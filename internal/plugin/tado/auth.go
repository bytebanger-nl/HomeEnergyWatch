package tado

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// tado's public client id, as used by the official REST-API guide and other
// local clients (homebridge-tado). No secret is involved in the device-code
// flow.
const (
	clientID = "1bb50063-6b0c-4d11-bd99-387f4a91cc46"
	scope    = "offline_access"
)

// The login endpoints are variables so UseBaseURL can point them at a fake.
var (
	deviceAuthURL = "https://login.tado.com/oauth2/device_authorize"
	tokenURL      = "https://login.tado.com/oauth2/token"
)

// tokenFile holds the rotating refresh token for one instance.
type tokenFile struct {
	RefreshToken string `json:"refresh_token"`
}

func (in *instance) tokenPath() string {
	if in.env.DataDir == "" {
		return ""
	}
	return filepath.Join(in.env.DataDir, "tado-"+in.env.InstanceID+".json")
}

func (in *instance) loadToken() {
	path := in.tokenPath()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var tf tokenFile
	if json.Unmarshal(b, &tf) == nil {
		in.refreshToken = tf.RefreshToken
	}
}

func (in *instance) saveToken(refresh string) {
	path := in.tokenPath()
	if path == "" {
		return
	}
	b, _ := json.Marshal(tokenFile{RefreshToken: refresh})
	// owner-only: it is a credential
	_ = os.WriteFile(path, b, 0o600)
}

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	Error        string `json:"error"`
}

// accessTokenFor returns a valid access token, refreshing if needed.
func (in *instance) accessTokenFor(ctx context.Context) (string, error) {
	in.mu.Lock()
	defer in.mu.Unlock()
	if in.accessToken != "" && time.Until(in.accessExpiry) > 30*time.Second {
		return in.accessToken, nil
	}
	if in.refreshToken == "" {
		return "", fmt.Errorf("tado: niet verbonden; log eerst in")
	}
	form := url.Values{
		"client_id":     {clientID},
		"grant_type":    {"refresh_token"},
		"refresh_token": {in.refreshToken},
	}
	tr, err := postToken(ctx, in.http, form)
	if err != nil {
		return "", err
	}
	if tr.Error != "" {
		// a dead refresh token: drop it so the UI shows "not connected"
		in.refreshToken = ""
		in.saveToken("")
		return "", fmt.Errorf("tado: inloggen verlopen (%s); log opnieuw in", tr.Error)
	}
	in.applyTokenLocked(tr)
	return in.accessToken, nil
}

// applyTokenLocked stores a fresh token set. in.mu must be held.
func (in *instance) applyTokenLocked(tr tokenResp) {
	in.accessToken = tr.AccessToken
	in.accessExpiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second)
	if tr.RefreshToken != "" {
		in.refreshToken = tr.RefreshToken // refresh tokens rotate
		in.saveToken(tr.RefreshToken)
	}
}

func postToken(ctx context.Context, client *http.Client, form url.Values) (tokenResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return tokenResp{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := client.Do(req)
	if err != nil {
		return tokenResp{}, err
	}
	defer resp.Body.Close()
	var tr tokenResp
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return tokenResp{}, fmt.Errorf("tado: ongeldig token-antwoord: %w", err)
	}
	return tr, nil
}

type deviceAuthResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// StartAuth begins the OAuth device-code flow and polls for the token in the
// background until the user approves.
func (in *instance) StartAuth(ctx context.Context) (plugin.AuthChallenge, error) {
	form := url.Values{"client_id": {clientID}, "scope": {scope}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deviceAuthURL, strings.NewReader(form.Encode()))
	if err != nil {
		return plugin.AuthChallenge{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := in.http.Do(req)
	if err != nil {
		return plugin.AuthChallenge{}, err
	}
	defer resp.Body.Close()
	var da deviceAuthResp
	if err := json.NewDecoder(resp.Body).Decode(&da); err != nil || da.DeviceCode == "" {
		return plugin.AuthChallenge{}, fmt.Errorf("tado: kon inlogcode niet starten")
	}

	interval := da.Interval
	if interval < 5 {
		interval = 5
	}
	in.mu.Lock()
	if in.cancelAuth != nil {
		in.cancelAuth() // replace any earlier attempt
	}
	actx, cancel := context.WithTimeout(context.Background(), time.Duration(da.ExpiresIn+5)*time.Second)
	in.cancelAuth = cancel
	in.authPending = true
	in.authMsg = "Wachten op goedkeuring in je browser…"
	in.mu.Unlock()

	go in.pollForToken(actx, da.DeviceCode, time.Duration(interval)*time.Second)

	return plugin.AuthChallenge{
		VerificationURI:         da.VerificationURI,
		VerificationURIComplete: da.VerificationURIComplete,
		UserCode:                da.UserCode,
		ExpiresInSeconds:        da.ExpiresIn,
		Message:                 "Open de link, log in bij tado en keur het verzoek goed.",
	}, nil
}

func (in *instance) pollForToken(ctx context.Context, deviceCode string, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			in.setAuthDone(false, "Inloggen afgebroken of verlopen; probeer opnieuw.")
			return
		case <-t.C:
		}
		form := url.Values{
			"client_id":   {clientID},
			"device_code": {deviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}
		tr, err := postToken(ctx, in.http, form)
		if err != nil {
			continue // transient; keep polling until the deadline
		}
		switch tr.Error {
		case "", "null":
			if tr.AccessToken != "" {
				in.mu.Lock()
				in.applyTokenLocked(tr)
				in.authPending = false
				in.authMsg = "Verbonden met tado."
				in.cancelAuth = nil
				in.mu.Unlock()
				return
			}
		case "authorization_pending":
			// keep waiting
		case "slow_down":
			interval += 5 * time.Second
			t.Reset(interval)
		default:
			in.setAuthDone(false, "Inloggen mislukt: "+tr.Error)
			return
		}
	}
}

func (in *instance) setAuthDone(_ bool, msg string) {
	in.mu.Lock()
	in.authPending = false
	in.authMsg = msg
	in.cancelAuth = nil
	in.mu.Unlock()
}

// AuthStatus reports whether the instance has a usable login.
func (in *instance) AuthStatus(context.Context) plugin.AuthState {
	in.mu.Lock()
	defer in.mu.Unlock()
	msg := in.authMsg
	switch {
	case in.authPending:
		return plugin.AuthState{Pending: true, Message: msg}
	case in.refreshToken != "":
		if msg == "" {
			msg = "Verbonden met tado."
		}
		return plugin.AuthState{Connected: true, Message: msg}
	default:
		return plugin.AuthState{Message: msg}
	}
}
