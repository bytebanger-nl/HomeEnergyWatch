package tuya

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// cloudClient talks to the official Tuya OpenAPI of a user's IoT project. It is
// used only to list the account's devices and their local keys (the key
// source); the live data is read locally over the LAN.
type cloudClient struct {
	accessID string
	secret   string
	base     string
	http     *http.Client

	token  string
	expiry time.Time
}

// regionBase maps a region code to the OpenAPI data-center base URL. A
// region that is itself a URL is used as is (hwsim's fake cloud).
func regionBase(region string) string {
	region = strings.TrimSpace(region)
	if strings.HasPrefix(region, "http://") || strings.HasPrefix(region, "https://") {
		return strings.TrimRight(region, "/")
	}
	switch strings.ToUpper(region) {
	case "US":
		return "https://openapi.tuyaus.com"
	case "CN":
		return "https://openapi.tuyacn.com"
	case "IN":
		return "https://openapi.tuyain.com"
	default: // EU and anything unset
		return "https://openapi.tuyaeu.com"
	}
}

func newCloudClient(accessID, secret, region string) *cloudClient {
	return &cloudClient{
		accessID: accessID,
		secret:   secret,
		base:     regionBase(region),
		http:     &http.Client{Timeout: 15 * time.Second},
	}
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func (c *cloudClient) sign(str string) string {
	m := hmac.New(sha256.New, []byte(c.secret))
	m.Write([]byte(str))
	return strings.ToUpper(hex.EncodeToString(m.Sum(nil)))
}

// get performs a signed GET against the OpenAPI. withToken adds the access
// token to the signature and headers (every call except fetching the token).
func (c *cloudClient) get(ctx context.Context, path string, withToken bool) (json.RawMessage, error) {
	t := strconv.FormatInt(time.Now().UnixMilli(), 10)
	stringToSign := "GET\n" + sha256Hex(nil) + "\n\n" + path
	var signStr string
	if withToken {
		signStr = c.accessID + c.token + t + stringToSign
	} else {
		signStr = c.accessID + t + stringToSign
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("client_id", c.accessID)
	req.Header.Set("sign", c.sign(signStr))
	req.Header.Set("t", t)
	req.Header.Set("sign_method", "HMAC-SHA256")
	if withToken {
		req.Header.Set("access_token", c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var env struct {
		Success bool            `json:"success"`
		Code    int             `json:"code"`
		Msg     string          `json:"msg"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, fmt.Errorf("tuya cloud: kan antwoord niet lezen: %s", trim(body))
	}
	if !env.Success {
		return nil, fmt.Errorf("tuya cloud: %s (code %d)", env.Msg, env.Code)
	}
	return env.Result, nil
}

// ensureToken fetches or refreshes the access token.
func (c *cloudClient) ensureToken(ctx context.Context) error {
	if c.token != "" && time.Now().Before(c.expiry) {
		return nil
	}
	res, err := c.get(ctx, "/v1.0/token?grant_type=1", false)
	if err != nil {
		return err
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpireTime  int    `json:"expire_time"`
	}
	if err := json.Unmarshal(res, &tok); err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("tuya cloud: lege token ontvangen")
	}
	c.token = tok.AccessToken
	exp := tok.ExpireTime
	if exp <= 0 {
		exp = 7200
	}
	c.expiry = time.Now().Add(time.Duration(exp-60) * time.Second)
	return nil
}

// cloudDevice is one device as the IoT project reports it.
type cloudDevice struct {
	ID          string
	Name        string
	LocalKey    string
	Category    string
	ProductName string
}

// listDevices returns every device linked to the project's app account, with
// its local key.
func (c *cloudClient) listDevices(ctx context.Context) ([]cloudDevice, error) {
	if err := c.ensureToken(ctx); err != nil {
		return nil, err
	}
	res, err := c.get(ctx, "/v1.0/iot-01/associated-users/devices?size=100", true)
	if err != nil {
		return nil, err
	}
	var list struct {
		Devices []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			LocalKey    string `json:"local_key"`
			Category    string `json:"category"`
			ProductName string `json:"product_name"`
		} `json:"devices"`
	}
	if err := json.Unmarshal(res, &list); err != nil {
		return nil, err
	}
	out := make([]cloudDevice, 0, len(list.Devices))
	for _, d := range list.Devices {
		cd := cloudDevice{
			ID: d.ID, Name: d.Name, LocalKey: d.LocalKey,
			Category: d.Category, ProductName: d.ProductName,
		}
		// The list endpoint does not always include the local key; fetch the
		// device detail when it is missing.
		if cd.LocalKey == "" {
			if lk, err := c.deviceLocalKey(ctx, d.ID); err == nil {
				cd.LocalKey = lk
			}
		}
		out = append(out, cd)
	}
	return out, nil
}

// deviceSchema fetches the device model and returns the dp-id -> {code, unit,
// scale, type, access, range} mapping as a JSON string ready to store in the
// device config, so Poll can label and scale local datapoints and Controls
// can offer the writable ones. Returns "" when unavailable.
func (c *cloudClient) deviceSchema(ctx context.Context, id string) string {
	res, err := c.get(ctx, "/v2.0/cloud/thing/"+id+"/model", true)
	if err != nil {
		return ""
	}
	var outer struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(res, &outer); err != nil || outer.Model == "" {
		return ""
	}
	schema := parseModel(outer.Model)
	if len(schema) == 0 {
		return ""
	}
	b, err := json.Marshal(schema)
	if err != nil {
		return ""
	}
	return string(b)
}

// parseModel turns a thing model (the JSON string in the model response)
// into a dp schema.
func parseModel(modelJSON string) dpSchema {
	var model struct {
		Services []struct {
			Properties []struct {
				AbilityID  int    `json:"abilityId"`
				Code       string `json:"code"`
				AccessMode string `json:"accessMode"`
				TypeSpec   struct {
					Type  string   `json:"type"`
					Scale int      `json:"scale"`
					Unit  string   `json:"unit"`
					Min   float64  `json:"min"`
					Max   float64  `json:"max"`
					Step  float64  `json:"step"`
					Range []string `json:"range"`
				} `json:"typeSpec"`
			} `json:"properties"`
		} `json:"services"`
	}
	if err := json.Unmarshal([]byte(modelJSON), &model); err != nil {
		return nil
	}
	schema := dpSchema{}
	for _, s := range model.Services {
		for _, p := range s.Properties {
			if p.Code == "" {
				continue
			}
			ts := p.TypeSpec
			e := dpEntry{
				Code:   p.Code,
				Unit:   normalizeUnit(ts.Unit),
				Scale:  ts.Scale,
				Type:   ts.Type,
				Access: p.AccessMode,
			}
			switch ts.Type {
			case "value":
				e.Min, e.Max, e.Step = ts.Min, ts.Max, ts.Step
			case "enum":
				e.Range = ts.Range
			}
			schema[strconv.Itoa(p.AbilityID)] = e
		}
	}
	return schema
}

// normalizeUnit tidies the units Tuya reports (e.g. the ℃ sign) for display.
func normalizeUnit(u string) string {
	switch u {
	case "℃":
		return "°C"
	case "℉":
		return "°F"
	}
	return u
}

// deviceLocalKey fetches one device's local key from its detail.
func (c *cloudClient) deviceLocalKey(ctx context.Context, id string) (string, error) {
	res, err := c.get(ctx, "/v1.0/devices/"+id, true)
	if err != nil {
		return "", err
	}
	var d struct {
		LocalKey string `json:"local_key"`
	}
	if err := json.Unmarshal(res, &d); err != nil {
		return "", err
	}
	return d.LocalKey, nil
}
