// Package tuya is a plugin for Tuya / Smart Life devices. It reads (and, when
// the user allows control, writes) each device locally over the LAN (no cloud
// for the live data) using the device's local key, and can discover devices,
// their keys and their datapoint model once through a Tuya IoT project. It
// registers itself with the plugin framework.
//
// This file implements the local control protocol (versions 3.3, 3.4 and 3.5).
// A device listens on TCP 6668 and speaks a framed, AES-encrypted protocol:
//
//   - 3.3: AES-128-ECB payloads, a 0x000055aa frame with a CRC32 trailer. No
//     session: DP_QUERY and CONTROL are encrypted directly with the local key.
//   - 3.4: AES-128-ECB payloads, a 0x000055aa frame with a 32-byte HMAC-SHA256
//     trailer. A session key is negotiated first (nonce exchange), then
//     DP_QUERY_NEW and CONTROL_NEW are sent with the session key.
//   - 3.5: AES-128-GCM payloads in a 0x00006699 frame. Same nonce handshake as
//     3.4; the session key derivation differs.
//
// Everything a test needs is a pure function of its inputs (framing, session
// key derivation); only Query and Set touch the network. sim.go is the device
// side of the same protocol, for tests and hwsim.
package tuya

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net"
	"slices"
	"time"
)

// Version is a Tuya local protocol version.
type Version string

const (
	V33 Version = "3.3"
	V34 Version = "3.4"
	V35 Version = "3.5"
)

// known reports whether v is a protocol version we implement.
func (v Version) known() bool { return v == V33 || v == V34 || v == V35 }

// Tuya command codes used here.
const (
	cmdSessStart  = 0x03 // SESS_KEY_NEG_START
	cmdSessResp   = 0x04 // SESS_KEY_NEG_RESP
	cmdSessFinish = 0x05 // SESS_KEY_NEG_FINISH
	cmdControl    = 0x07 // CONTROL (3.3)
	cmdStatus     = 0x08 // STATUS: the device reports its dps (also unasked)
	cmdDPQuery    = 0x0a // DP_QUERY (3.3)
	cmdControlNew = 0x0d // CONTROL_NEW (3.4 / 3.5)
	cmdDPQueryNew = 0x10 // DP_QUERY_NEW (3.4 / 3.5)
)

const (
	prefix55aa = 0x000055aa
	suffix55aa = 0x0000aa55
	prefix6699 = 0x00006699
	suffix6699 = 0x00009966
)

// defaultPort is the Tuya local control port.
const defaultPort = "6668"

// Device identifies one device for a local query.
type Device struct {
	Addr    string  // host or host:port (port defaults to 6668)
	ID      string  // devId / gwId
	Key     []byte  // 16-byte local key
	Version Version // 3.3 / 3.4 / 3.5
}

// --- AES helpers ---------------------------------------------------------

func pkcs7Pad(b []byte) []byte {
	p := 16 - len(b)%16
	return append(b, bytes.Repeat([]byte{byte(p)}, p)...)
}

func pkcs7Unpad(b []byte) []byte {
	if len(b) == 0 {
		return b
	}
	p := int(b[len(b)-1])
	if p < 1 || p > 16 || p > len(b) {
		return b
	}
	return b[:len(b)-p]
}

func ecbEncrypt(key, b []byte) ([]byte, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	b = pkcs7Pad(b)
	out := make([]byte, len(b))
	for i := 0; i < len(b); i += 16 {
		c.Encrypt(out[i:i+16], b[i:i+16])
	}
	return out, nil
}

func ecbDecrypt(key, b []byte) ([]byte, error) {
	if len(b) == 0 || len(b)%16 != 0 {
		return nil, fmt.Errorf("tuya: ciphertext length %d not a multiple of 16", len(b))
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(b))
	for i := 0; i < len(b); i += 16 {
		c.Decrypt(out[i:i+16], b[i:i+16])
	}
	return pkcs7Unpad(out), nil
}

func hmacSHA256(key, b []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return m.Sum(nil)
}

// --- 0x55aa framing (3.3 CRC32, 3.4 HMAC-SHA256) -------------------------

// pack55aa builds a 0x000055aa packet. When hmacKey is non-nil the trailer is
// a 32-byte HMAC-SHA256 over the frame (3.4); otherwise it is a CRC32 (3.3).
func pack55aa(seq, cmd uint32, payload, hmacKey []byte) []byte {
	trailerLen := 4 // CRC32
	if hmacKey != nil {
		trailerLen = 32
	}
	h := make([]byte, 16)
	binary.BigEndian.PutUint32(h[0:], prefix55aa)
	binary.BigEndian.PutUint32(h[4:], seq)
	binary.BigEndian.PutUint32(h[8:], cmd)
	binary.BigEndian.PutUint32(h[12:], uint32(len(payload)+trailerLen+4))
	b := append(h, payload...)
	if hmacKey != nil {
		b = append(b, hmacSHA256(hmacKey, b)...)
	} else {
		crc := make([]byte, 4)
		binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(b))
		b = append(b, crc...)
	}
	tail := make([]byte, 4)
	binary.BigEndian.PutUint32(tail, suffix55aa)
	return append(b, tail...)
}

// unpack55aa returns the command and the raw (still-encrypted) payload of a
// 0x55aa reply, verifying the structure. trailerLen is 4 (CRC) or 32 (HMAC).
func unpack55aa(pkt []byte, hmac bool) (cmd uint32, payload []byte, err error) {
	trailerLen := 4
	if hmac {
		trailerLen = 32
	}
	if len(pkt) < 16+4+trailerLen+4 || binary.BigEndian.Uint32(pkt) != prefix55aa {
		return 0, nil, fmt.Errorf("tuya: not a 0x55aa packet")
	}
	cmd = binary.BigEndian.Uint32(pkt[8:])
	// 16-byte header, 4-byte return code, payload, trailer, 4-byte suffix.
	start, end := 20, len(pkt)-trailerLen-4
	if end < start {
		return cmd, nil, fmt.Errorf("tuya: short 0x55aa payload")
	}
	return cmd, pkt[start:end], nil
}

// --- 0x6699 framing (3.5 GCM) --------------------------------------------

// pack6699 builds a 0x00006699 (GCM) packet encrypted with key.
func pack6699(key []byte, seq, cmd uint32, plain []byte) ([]byte, error) {
	iv := make([]byte, 12)
	if _, err := rand.Read(iv); err != nil {
		return nil, err
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(c)
	if err != nil {
		return nil, err
	}
	length := uint32(len(iv) + len(plain) + 16) // iv + ciphertext + tag
	hdr := make([]byte, 18)
	binary.BigEndian.PutUint32(hdr[0:], prefix6699)
	binary.BigEndian.PutUint16(hdr[4:], 0)
	binary.BigEndian.PutUint32(hdr[6:], seq)
	binary.BigEndian.PutUint32(hdr[10:], cmd)
	binary.BigEndian.PutUint32(hdr[14:], length)
	sealed := g.Seal(nil, iv, plain, hdr[4:18]) // AAD = header without prefix
	out := append(hdr, iv...)
	out = append(out, sealed...)
	tail := make([]byte, 4)
	binary.BigEndian.PutUint32(tail, suffix6699)
	return append(out, tail...), nil
}

// unpack6699 decrypts a 0x6699 reply and returns its command and plaintext. A
// 4-byte return-code prefix on responses is stripped.
func unpack6699(key, pkt []byte) (cmd uint32, plain []byte, err error) {
	cmd, out, err := open6699(key, pkt)
	if err != nil {
		return cmd, nil, err
	}
	// Responses carry a 4-byte return code before the JSON body.
	if len(out) >= 4 && out[0] != '{' {
		out = out[4:]
	}
	return cmd, out, nil
}

// open6699 decrypts a 0x6699 packet and returns its command and the whole
// plaintext.
func open6699(key, pkt []byte) (cmd uint32, plain []byte, err error) {
	if len(pkt) < 18+12+16+4 || binary.BigEndian.Uint32(pkt) != prefix6699 {
		return 0, nil, fmt.Errorf("tuya: not a 0x6699 packet")
	}
	cmd = binary.BigEndian.Uint32(pkt[10:])
	length := int(binary.BigEndian.Uint32(pkt[14:]))
	iv := pkt[18:30]
	ctEnd := 18 + length
	if ctEnd > len(pkt)-4 || ctEnd < 30 {
		return 0, nil, fmt.Errorf("tuya: bad 0x6699 length")
	}
	c, err := aes.NewCipher(key)
	if err != nil {
		return 0, nil, err
	}
	g, err := cipher.NewGCM(c)
	if err != nil {
		return 0, nil, err
	}
	out, err := g.Open(nil, iv, pkt[30:ctEnd], pkt[4:18])
	if err != nil {
		return cmd, nil, fmt.Errorf("tuya: GCM open: %w", err)
	}
	return cmd, out, nil
}

// --- session key derivation ----------------------------------------------

// localNonce is the 16-byte nonce the client offers (as in the reference
// implementations). The device replies with its own nonce.
var localNonce = []byte("0123456789abcdef")

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// sessionKey34 = AES-ECB(localKey, localNonce XOR remoteNonce).
func sessionKey34(localKey, remoteNonce []byte) ([]byte, error) {
	c, err := aes.NewCipher(localKey)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 16)
	c.Encrypt(out, xorBytes(localNonce, remoteNonce))
	return out, nil
}

// sessionKey35 = first 16 bytes of AES-GCM(localKey, iv=localNonce[:12],
// localNonce XOR remoteNonce) ciphertext.
func sessionKey35(localKey, remoteNonce []byte) ([]byte, error) {
	c, err := aes.NewCipher(localKey)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(c)
	if err != nil {
		return nil, err
	}
	sealed := g.Seal(nil, localNonce[:12], xorBytes(localNonce, remoteNonce), nil)
	return sealed[:16], nil
}

// --- connection ----------------------------------------------------------

// session is one open connection to a device, after the handshake for its
// protocol version. key is the key for the payloads: the local key (3.3) or
// the negotiated session key (3.4 / 3.5).
type session struct {
	d    Device
	conn net.Conn
	key  []byte
	seq  uint32
}

// open dials the device and runs the handshake for its protocol version.
func (d Device) open(ctx context.Context) (*session, error) {
	if !d.Version.known() {
		return nil, fmt.Errorf("tuya: unsupported protocol version %q", d.Version)
	}
	if len(d.Key) != 16 {
		return nil, fmt.Errorf("tuya: local key must be 16 bytes, got %d", len(d.Key))
	}
	addr := d.Addr
	if _, _, err := net.SplitHostPort(addr); err != nil {
		addr = net.JoinHostPort(addr, defaultPort)
	}
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	} else {
		conn.SetDeadline(time.Now().Add(6 * time.Second))
	}
	s := &session{d: d, conn: conn, key: d.Key}
	if d.Version != V33 {
		if err := s.handshake(); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return s, nil
}

func (s *session) Close() { s.conn.Close() }

func (s *session) next() uint32 { s.seq++; return s.seq }

// handshake runs the 3.4/3.5 nonce exchange and switches to the session key.
func (s *session) handshake() error {
	d := s.d
	is35 := d.Version == V35

	// Step 1: SESS_KEY_NEG_START with our nonce.
	start, err := frameSession(s.next(), cmdSessStart, localNonce, d.Key, is35)
	if err != nil {
		return err
	}
	reply, err := roundtrip(s.conn, start, cmdSessResp)
	if err != nil {
		return fmt.Errorf("tuya: handshake start: %w", err)
	}
	cmd, _, dec, err := unframeSession(reply, d.Key, is35)
	if err != nil {
		return fmt.Errorf("tuya: handshake reply: %w", err)
	}
	if cmd != cmdSessResp || len(dec) < 48 {
		return fmt.Errorf("tuya: unexpected handshake reply (cmd 0x%02x, %d bytes)", cmd, len(dec))
	}
	remoteNonce := dec[:16]
	if !hmac.Equal(dec[16:48], hmacSHA256(d.Key, localNonce)) {
		return fmt.Errorf("tuya: handshake HMAC mismatch (wrong local key?)")
	}

	// Step 2: SESS_KEY_NEG_FINISH with HMAC of the device nonce.
	finish, err := frameSession(s.next(), cmdSessFinish, hmacSHA256(d.Key, remoteNonce), d.Key, is35)
	if err != nil {
		return err
	}
	if _, err := s.conn.Write(finish); err != nil {
		return fmt.Errorf("tuya: handshake finish: %w", err)
	}

	if is35 {
		s.key, err = sessionKey35(d.Key, remoteNonce)
	} else {
		s.key, err = sessionKey34(d.Key, remoteNonce)
	}
	return err
}

// --- Query and Set -------------------------------------------------------

// Query opens a connection, runs the handshake for the device's protocol
// version, sends a status query and returns the decoded dps map.
func (d Device) Query(ctx context.Context) (map[string]any, error) {
	s, err := d.open(ctx)
	if err != nil {
		return nil, err
	}
	defer s.Close()
	if d.Version == V33 {
		// DP_QUERY, ECB-encrypted with the local key, no version header.
		payload, err := ecbEncrypt(d.Key, d.queryJSON())
		if err != nil {
			return nil, err
		}
		if _, err := s.conn.Write(pack55aa(s.next(), cmdDPQuery, payload, nil)); err != nil {
			return nil, err
		}
		return queryReply(s.conn, []uint32{cmdDPQuery, cmdStatus}, func(f []byte) (map[string]any, bool, error) {
			_, enc, err := unpack55aa(f, false)
			if err != nil || len(enc) == 0 {
				return nil, false, err
			}
			dps, err := decodeDPS(d.Key, enc)
			return dps, true, err
		})
	}
	q, err := frameSession(s.next(), cmdDPQueryNew, d.queryJSON(), s.key, d.Version == V35)
	if err != nil {
		return nil, err
	}
	if _, err := s.conn.Write(q); err != nil {
		return nil, fmt.Errorf("tuya: query: %w", err)
	}
	return queryReply(s.conn, []uint32{cmdDPQueryNew, cmdStatus}, func(f []byte) (map[string]any, bool, error) {
		_, _, body, err := unframeSession(f, s.key, d.Version == V35)
		if err != nil {
			return nil, false, fmt.Errorf("tuya: query reply: %w", err)
		}
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, false, nil
		}
		dps, err := parseDPS(body)
		return dps, true, err
	})
}

// queryReply reads the answer to a query: frames of another command, and
// empty acknowledgements, which some devices send before the data, are
// skipped. decode returns done=false for an empty frame.
func queryReply(conn io.Reader, want []uint32, decode func([]byte) (map[string]any, bool, error)) (map[string]any, error) {
	for range maxSkip {
		f, err := readReply(conn, want...)
		if err != nil {
			return nil, err
		}
		dps, done, err := decode(f)
		if err != nil || done {
			return dps, err
		}
	}
	return nil, fmt.Errorf("tuya: no status in the reply")
}

// Set writes datapoints: CONTROL (3.3) or CONTROL_NEW (3.4 / 3.5), with the
// version header these commands carry. Values are raw dp values (bool,
// integer in the dp's own scale, enum string). The device acknowledges with
// a return code; anything but 0 is an error.
func (d Device) Set(ctx context.Context, dps map[string]any) error {
	if len(dps) == 0 {
		return fmt.Errorf("tuya: nothing to set")
	}
	s, err := d.open(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	var pkt []byte
	if d.Version == V33 {
		enc, err := ecbEncrypt(d.Key, d.controlJSON(dps))
		if err != nil {
			return err
		}
		pkt = pack55aa(s.next(), cmdControl, append(versionHeader(V33), enc...), nil)
	} else {
		plain := append(versionHeader(d.Version), d.controlJSON(dps)...)
		if pkt, err = frameSession(s.next(), cmdControlNew, plain, s.key, d.Version == V35); err != nil {
			return err
		}
	}
	want := uint32(cmdControlNew)
	if d.Version == V33 {
		want = cmdControl
	}
	reply, err := roundtrip(s.conn, pkt, want)
	if err != nil {
		return fmt.Errorf("tuya: set: %w", err)
	}
	var code uint32
	if d.Version == V33 {
		if _, _, err := unpack55aa(reply, false); err != nil {
			return err
		}
		code = binary.BigEndian.Uint32(reply[16:20])
	} else if _, code, _, err = unframeSession(reply, s.key, d.Version == V35); err != nil {
		return fmt.Errorf("tuya: set reply: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("tuya: apparaat weigerde de opdracht (code %d)", code)
	}
	return nil
}

// versionHeader is the 15-byte header ("3.x" and 12 zero bytes) that CONTROL
// and CONTROL_NEW carry: outside the ciphertext for 3.3, inside for 3.4/3.5.
func versionHeader(v Version) []byte {
	return append([]byte(v), make([]byte, 12)...)
}

// frameSession builds a packet for the session protocols.
func frameSession(seq, cmd uint32, plain, key []byte, is35 bool) ([]byte, error) {
	if is35 {
		return pack6699(key, seq, cmd, plain)
	}
	enc, err := ecbEncrypt(key, plain)
	if err != nil {
		return nil, err
	}
	return pack55aa(seq, cmd, enc, key), nil
}

// unframeSession decodes a reply for the session protocols, returning its
// command, return code and plaintext.
func unframeSession(pkt, key []byte, is35 bool) (cmd, code uint32, plain []byte, err error) {
	if is35 {
		cmd, plain, err := open6699(key, pkt)
		if err != nil {
			return cmd, 0, nil, err
		}
		// Replies carry a 4-byte return code before the body.
		if len(plain) >= 4 && plain[0] != '{' {
			code, plain = binary.BigEndian.Uint32(plain), plain[4:]
		}
		return cmd, code, plain, nil
	}
	cmd, enc, err := unpack55aa(pkt, true)
	if err != nil {
		return 0, 0, nil, err
	}
	code = binary.BigEndian.Uint32(pkt[16:20])
	if len(enc) == 0 {
		return cmd, code, nil, nil // a bare acknowledgement
	}
	dec, err := ecbDecrypt(key, enc)
	if err != nil {
		return cmd, code, nil, err
	}
	return cmd, code, dec, nil
}

// queryJSON is the DP_QUERY request body.
func (d Device) queryJSON() []byte {
	b, _ := json.Marshal(map[string]any{
		"gwId":  d.ID,
		"devId": d.ID,
		"uid":   d.ID,
		"t":     fmt.Sprintf("%d", time.Now().Unix()),
	})
	return b
}

// controlJSON is the CONTROL (3.3) or CONTROL_NEW (3.4 / 3.5) body.
func (d Device) controlJSON(dps map[string]any) []byte {
	if d.Version == V33 {
		b, _ := json.Marshal(map[string]any{
			"devId": d.ID,
			"uid":   d.ID,
			"t":     fmt.Sprintf("%d", time.Now().Unix()),
			"dps":   dps,
		})
		return b
	}
	b, _ := json.Marshal(map[string]any{
		"protocol": 5,
		"t":        time.Now().Unix(),
		"data":     map[string]any{"dps": dps},
	})
	return b
}

// decodeDPS decrypts an ECB payload (3.3) and extracts the dps map, stripping
// an optional 15-byte "3.3" version header.
func decodeDPS(key, enc []byte) (map[string]any, error) {
	if len(enc) > 15 && string(enc[:3]) == "3.3" {
		enc = enc[15:]
	}
	plain, err := ecbDecrypt(key, enc)
	if err != nil {
		return nil, err
	}
	return parseDPS(plain)
}

// parseDPS pulls the dps object out of a status JSON body, accepting both the
// flat {"dps":{...}} shape and the nested {"data":{"dps":{...}}} shape.
func parseDPS(body []byte) (map[string]any, error) {
	var top struct {
		DPS  map[string]any `json:"dps"`
		Data struct {
			DPS map[string]any `json:"dps"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &top); err != nil {
		return nil, fmt.Errorf("tuya: decode status: %w (body %q)", err, trim(body))
	}
	if top.DPS != nil {
		return top.DPS, nil
	}
	if top.Data.DPS != nil {
		return top.Data.DPS, nil
	}
	return nil, fmt.Errorf("tuya: no dps in status (body %q)", trim(body))
}

func trim(b []byte) string {
	const max = 120
	if len(b) > max {
		return string(b[:max]) + "..."
	}
	return string(b)
}

// roundtrip writes a packet and reads the reply frame with one of the
// commands in want.
func roundtrip(conn net.Conn, pkt []byte, want ...uint32) ([]byte, error) {
	if _, err := conn.Write(pkt); err != nil {
		return nil, err
	}
	return readReply(conn, want...)
}

// maxSkip bounds the frames read while looking for a reply.
const maxSkip = 4

// readReply reads frames until one has a command in want, skipping others
// (a status the device sends by itself, a heartbeat), at most maxSkip.
func readReply(r io.Reader, want ...uint32) ([]byte, error) {
	for range maxSkip {
		f, err := readFrame(r)
		if err != nil {
			return nil, err
		}
		if slices.Contains(want, frameCmd(f)) {
			return f, nil
		}
	}
	return nil, fmt.Errorf("tuya: no reply with command %#x", want)
}

// frameCmd is the command of a frame readFrame returned.
func frameCmd(f []byte) uint32 {
	if binary.BigEndian.Uint32(f) == prefix6699 {
		return binary.BigEndian.Uint32(f[10:])
	}
	return binary.BigEndian.Uint32(f[8:])
}

// maxFrame bounds the length a frame header may announce.
const maxFrame = 64 << 10

// readFrame reads exactly one 0x55aa or 0x6699 frame, by the length in its
// header: TCP may split a frame over several reads or put two in one.
func readFrame(r io.Reader) ([]byte, error) {
	hdr := make([]byte, 16, 18)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	var size int
	switch binary.BigEndian.Uint32(hdr) {
	case prefix55aa:
		// prefix, seq, cmd, length (of everything after the header)
		size = 16 + int(binary.BigEndian.Uint32(hdr[12:]))
	case prefix6699:
		// prefix, 2 bytes, seq, cmd, length (iv + ciphertext + tag), suffix
		hdr = hdr[:18]
		if _, err := io.ReadFull(r, hdr[16:]); err != nil {
			return nil, err
		}
		size = 18 + int(binary.BigEndian.Uint32(hdr[14:])) + 4
	default:
		return nil, fmt.Errorf("tuya: unknown frame prefix %x", hdr[:4])
	}
	if size < len(hdr) || size > maxFrame {
		return nil, fmt.Errorf("tuya: bad frame length %d", size)
	}
	pkt := make([]byte, size)
	copy(pkt, hdr)
	if _, err := io.ReadFull(r, pkt[len(hdr):]); err != nil {
		return nil, err
	}
	return pkt, nil
}
