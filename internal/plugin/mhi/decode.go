// Package mhi is a plugin for Mitsubishi Heavy Industries air conditioners
// with a factory WF-RAC Wi-Fi module (the Smart M-Air app). It talks the
// module's local "beaver" API over HTTPS on port 51443. Reading needs no
// account (getDeviceInfo, getAirconStat); changing a unit (setAirconStat)
// only happens when the user allowed control, and then registers this
// gateway as one of the module's accounts first (updateAccountInfo). The
// state is a base64 blob; decoding and encoding it follow the
// manufacturer's own format as used by the pywfrac project (MIT).
package mhi

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"syscall"
	"time"
)

const port = "51443"

// client sends beaver commands. The module serves a self-signed
// certificate, so verification is disabled; nothing secret is sent.
type client struct {
	http *http.Client
}

func newClient() *client {
	// The WF-RAC module has an old embedded TLS stack and a self-signed
	// certificate: allow legacy versions and ciphers, and skip verification.
	// Nothing secret is sent.
	ciphers := make([]uint16, 0, 32)
	for _, c := range tls.CipherSuites() {
		ciphers = append(ciphers, c.ID)
	}
	for _, c := range tls.InsecureCipherSuites() {
		ciphers = append(ciphers, c.ID)
	}
	return &client{http: &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{ //nolint:gosec // device uses a self-signed cert and legacy TLS; nothing secret is sent
				InsecureSkipVerify: true,
				MinVersion:         tls.VersionTLS10,
				CipherSuites:       ciphers,
			},
			DialContext:         (&net.Dialer{Timeout: 4 * time.Second}).DialContext,
			TLSHandshakeTimeout: 4 * time.Second,
			// The module's slow TLS stack copes badly with parallel
			// handshakes (a poll, a control and a scan at once): one
			// connection per unit, kept for reuse but not for long, since
			// the module drops idle connections itself.
			MaxConnsPerHost: 1,
			IdleConnTimeout: 20 * time.Second,
		},
	}}
}

// retryBudget is the time a retry needs at least: a new connection with its
// TLS handshake and the request.
const retryBudget = 5 * time.Second

// transient reports whether err is a connection failure that a second try
// on a new connection may not have (a TLS handshake that timed out, a
// connection the module reset or closed), rather than an answer.
func transient(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return false
	}
	var re *resultError
	if errors.As(err, &re) {
		return false
	}
	var ne net.Error
	return errors.As(err, &ne) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNREFUSED)
}

func (c *client) close() { c.http.CloseIdleConnections() }

type envelope struct {
	Result   int             `json:"result"`
	Contents json.RawMessage `json:"contents"`
}

// command sends one beaver command to ip and returns its contents. A
// connection failure is tried once more on a fresh connection when the
// context leaves time for it; the commands set a whole state, so sending one
// twice is harmless.
func (c *client) command(ctx context.Context, ip, command string, contents map[string]any) (json.RawMessage, error) {
	out, err := c.send(ctx, ip, command, contents)
	if err == nil || !transient(ctx, err) {
		return out, err
	}
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < retryBudget {
		return out, err
	}
	c.http.CloseIdleConnections()
	return c.send(ctx, ip, command, contents)
}

// send sends one beaver command once.
func (c *client) send(ctx context.Context, ip, command string, contents map[string]any) (json.RawMessage, error) {
	body := map[string]any{
		"apiVer":     "1.0",
		"command":    command,
		"deviceId":   "hwlocal",
		"operatorId": "hwlocal",
		"timestamp":  time.Now().Unix(),
	}
	if contents != nil {
		body["contents"] = contents
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	url := "https://" + hostPort(ip) + "/beaver/command/" + command
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", command, resp.StatusCode)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		// A real unit answered setAirconStat with JSON that does not parse
		// ("invalid character 'a' after object key:value pair"). The result
		// code is what matters for a write: take it from the text, and
		// leave the contents out.
		code, ok := resultOf(raw)
		if !ok {
			return nil, fmt.Errorf("%s: ongeldig antwoord: %w (antwoord: %q)", command, err, snippet(raw))
		}
		env = envelope{Result: code}
	}
	if env.Result != 0 {
		return nil, &resultError{command: command, code: env.Result}
	}
	return env.Contents, nil
}

var resultRe = regexp.MustCompile(`"result"\s*:\s*(-?\d+)`)

// resultOf finds the result code in a reply that is not valid JSON.
func resultOf(raw []byte) (int, bool) {
	m := resultRe.FindSubmatch(raw)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(string(m[1]))
	return n, err == nil
}

// snippet shortens a reply for an error message.
func snippet(raw []byte) string {
	const max = 300
	if len(raw) > max {
		return string(raw[:max]) + "..."
	}
	return string(raw)
}

// hostPort adds the module's port to an address without one; an address
// with a port (a simulator, a port forward) is used as is.
func hostPort(addr string) string {
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr
	}
	return net.JoinHostPort(addr, port)
}

// DeviceInfo is the identity of a unit (getDeviceInfo).
type DeviceInfo struct {
	AirconID   string `json:"airconId"`
	MacAddress string `json:"macAddress"`
	APMode     int    `json:"apMode"`
}

func (c *client) deviceInfo(ctx context.Context, ip string) (DeviceInfo, error) {
	raw, err := c.command(ctx, ip, "getDeviceInfo", nil)
	if err != nil {
		return DeviceInfo{}, err
	}
	var di DeviceInfo
	if err := json.Unmarshal(raw, &di); err != nil {
		return DeviceInfo{}, err
	}
	return di, nil
}

// statContents is the part of getAirconStat we use.
type statContents struct {
	AirconStat string `json:"airconStat"`
	FirmType   string `json:"firmType"`
	Timezone   string `json:"timezone"`
	Wireless   struct {
		FirmVer string `json:"firmVer"`
	} `json:"wireless"`
	MCU struct {
		FirmVer string `json:"firmVer"`
	} `json:"mcu"`
	NumOfAccount int `json:"numOfAccount"`
}

func (c *client) airconStat(ctx context.Context, ip, airconID string) (statContents, error) {
	var contents map[string]any
	if airconID != "" {
		contents = map[string]any{"airconId": airconID}
	}
	raw, err := c.command(ctx, ip, "getAirconStat", contents)
	if err != nil {
		return statContents{}, err
	}
	var sc statContents
	if err := json.Unmarshal(raw, &sc); err != nil {
		return statContents{}, err
	}
	return sc, nil
}

// State is the decoded operating state of a unit.
type State struct {
	Power             bool
	Mode              string  // AUTO, COOL, HEAT, FAN, DRY
	PresetC           float64 // setpoint
	Fan               string  // AUTO, QUIET, LOW, MEDIUM, HIGH or ""
	SwingUD           int     // 0 = swinging, 1..4 = fixed position, -1 = unknown (left unchanged on write)
	IndoorC           *float64
	OutdoorC          *float64
	CompressorRunning bool
	ErrorCode         string // "00" = no fault
	ModelRaw          int
	SwingLR           int  // 0 = swinging, 1..7 = fixed position (6 wide, 7 narrow), -1 = unknown
	Entrust           bool // 3D auto

	// Fields that are not shown but must be sent back unchanged when the
	// state is written (setAirconStat takes a complete state).
	CoolHotJudge bool
	Vacant       bool
	SelfClean    bool
	ModelNr      int // ModelRaw grouped: 0, 1, 2 or -1
}

var (
	modeNames = []string{"AUTO", "COOL", "HEAT", "FAN", "DRY"}
	fanNames  = []string{"AUTO", "QUIET", "LOW", "MEDIUM", "HIGH"}
)

// findMatch returns the index of want in list, or -1.
func findMatch(want int, list ...int) int {
	for i, v := range list {
		if v == want {
			return i
		}
	}
	return -1
}

// vanePosition turns a findMatch index into a fixed vane position (1..),
// keeping -1 for a value the decoder does not know: reading that as 0 would
// show "swinging" and write swinging back with the next change.
func vanePosition(i int) int {
	if i < 0 {
		return -1
	}
	return i + 1
}

// decodeStat decodes a base64 airconStat blob into a State. The byte layout
// mirrors the manufacturer's AirconStatCoder (via pywfrac): the receive-half
// settings start at signed[18]*4+21, followed by 4-byte temperature segments.
func decodeStat(b64 string) (State, error) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return State{}, fmt.Errorf("airconStat: %w", err)
	}
	if len(raw) < 19 {
		return State{}, fmt.Errorf("airconStat: te kort (%d bytes)", len(raw))
	}
	signed := make([]int, len(raw))
	for i, a := range raw {
		if a > 127 {
			signed[i] = int(a) - 256
		} else {
			signed[i] = int(a)
		}
	}
	start := signed[18]*4 + 21
	if start < 0 || start+18 > len(signed) {
		return State{}, fmt.Errorf("airconStat: te kort (%d bytes)", len(raw))
	}
	content := signed[start : start+18]

	var st State
	st.Power = 1 == (3 & content[2])
	st.PresetC = float64(content[4]) / 2
	if m := findMatch(60&content[2], 8, 16, 12, 4) + 1; m >= 0 && m < len(modeNames) {
		st.Mode = modeNames[m]
	}
	if f := findMatch(15&content[3], 7, 0, 1, 2, 6); f >= 0 && f < len(fanNames) {
		st.Fan = fanNames[f]
	}
	if content[2]&192 == 64 {
		st.SwingUD = 0
	} else {
		st.SwingUD = vanePosition(findMatch(240&content[3], 0, 16, 32, 48))
	}
	st.CompressorRunning = content[9]&2 != 0
	st.ModelRaw = content[0] & 127
	if content[12]&3 == 1 {
		st.SwingLR = 0
	} else {
		st.SwingLR = vanePosition(findMatch(31&content[11], 0, 1, 2, 3, 4, 5, 6))
	}
	st.Entrust = 12&content[12] == 4
	st.CoolHotJudge = content[8]&8 <= 0
	st.Vacant = content[10]&1 != 0
	if st.ModelRaw == 3 {
		st.ModelNr = 2 // ZT series: the byte layout of model 2
	} else {
		st.ModelNr = findMatch(st.ModelRaw, 0, 1, 2)
	}
	if st.ModelNr == 1 || st.ModelNr == 2 {
		st.SelfClean = content[15]&1 != 0
	}
	code := content[6] & 127
	switch {
	case content[6] < 0:
		st.ErrorCode = fmt.Sprintf("M%02d", code)
	case code == 0:
		st.ErrorCode = "00"
	default:
		st.ErrorCode = "E" + strconv.Itoa(code)
	}

	// temperature segments: 4-byte (tag, sub, value, _) records after the
	// basic block, before the trailing CRC.
	vals := signed[start+19:]
	if len(vals) >= 2 {
		vals = vals[:len(vals)-2]
	}
	for i := 0; i+3 < len(vals); i += 4 {
		if vals[i] != -128 {
			continue
		}
		b := vals[i+2] & 0xFF
		switch vals[i+1] {
		case 16:
			v := outdoorTempList[b]
			st.OutdoorC = &v
		case 32:
			v := indoorTempList[b]
			st.IndoorC = &v
		}
	}
	return st, nil
}
