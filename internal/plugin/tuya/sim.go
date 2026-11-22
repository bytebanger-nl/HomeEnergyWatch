package tuya

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"maps"
	"net"
	"sync"
	"time"
)

// SimDevice is the device side of the local protocol: a simulated Tuya
// device that answers DP_QUERY / DP_QUERY_NEW and takes CONTROL /
// CONTROL_NEW, for the tests and for hwsim. It checks what a real device
// checks (the local key, the handshake, the version header on control
// commands), so a client bug shows as an error here too.
type SimDevice struct {
	ID      string
	Key     []byte // 16-byte local key
	Version Version

	mu  sync.Mutex
	dps map[string]any
	// Reject, when set, makes the device refuse control commands with this
	// return code.
	Reject uint32
	// OnSet, when set, is called with every set of written dps (under the
	// lock, after they were applied).
	OnSet func(map[string]any)
}

// NewSimDevice returns a simulated device with the given initial dps.
func NewSimDevice(id string, key []byte, v Version, dps map[string]any) *SimDevice {
	return &SimDevice{ID: id, Key: key, Version: v, dps: maps.Clone(dps)}
}

// DPS returns a copy of the current datapoints.
func (s *SimDevice) DPS() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.dps)
}

// Update changes datapoints from the simulation itself (a room warming up),
// not from a client.
func (s *SimDevice) Update(fn func(dps map[string]any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(s.dps)
}

// Serve accepts connections until ln is closed.
func (s *SimDevice) Serve(ln net.Listener) error {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handle(c)
	}
}

// simConn is one client connection: the key in use (local key, then the
// session key after a 3.4/3.5 handshake) and the bytes read but not yet
// framed.
type simConn struct {
	c           net.Conn
	key         []byte
	remoteNonce []byte
	buf         []byte
}

func (s *SimDevice) handle(c net.Conn) {
	defer c.Close()
	sc := &simConn{c: c, key: s.Key}
	for {
		c.SetDeadline(time.Now().Add(10 * time.Second))
		pkt, err := sc.readFrame()
		if err != nil {
			return
		}
		if err := s.answer(sc, pkt); err != nil {
			return
		}
	}
}

// readFrame returns the next whole frame (0x55aa or 0x6699).
func (sc *simConn) readFrame() ([]byte, error) {
	tmp := make([]byte, 4096)
	for {
		if n := frameLen(sc.buf); n > 0 && len(sc.buf) >= n {
			pkt := sc.buf[:n:n]
			sc.buf = sc.buf[n:]
			return pkt, nil
		} else if n < 0 {
			return nil, fmt.Errorf("tuya sim: bad frame")
		}
		n, err := sc.c.Read(tmp)
		if err != nil {
			return nil, err
		}
		sc.buf = append(sc.buf, tmp[:n]...)
	}
}

// frameLen is the total length of the frame at the start of b: 0 when the
// header is not complete yet, -1 when b does not start with a frame.
func frameLen(b []byte) int {
	if len(b) < 4 {
		return 0
	}
	switch binary.BigEndian.Uint32(b) {
	case prefix55aa:
		if len(b) < 16 {
			return 0
		}
		return 16 + int(binary.BigEndian.Uint32(b[12:]))
	case prefix6699:
		if len(b) < 18 {
			return 0
		}
		return 18 + int(binary.BigEndian.Uint32(b[14:])) + 4
	}
	return -1
}

func (s *SimDevice) answer(sc *simConn, pkt []byte) error {
	cmd, seq, plain, err := s.unframe(sc, pkt)
	if err != nil {
		return err
	}
	switch cmd {
	case cmdSessStart:
		if len(plain) < 16 {
			return fmt.Errorf("tuya sim: short nonce")
		}
		sc.remoteNonce = make([]byte, 16)
		rand.Read(sc.remoteNonce)
		body := append(append([]byte{}, sc.remoteNonce...), hmacSHA256(s.Key, plain[:16])...)
		return s.reply(sc, seq, cmdSessResp, 0, body)
	case cmdSessFinish:
		if sc.remoteNonce == nil || !bytes.Equal(plain, hmacSHA256(s.Key, sc.remoteNonce)) {
			return fmt.Errorf("tuya sim: bad handshake finish")
		}
		if s.Version == V35 {
			sc.key, err = sessionKey35(s.Key, sc.remoteNonce)
		} else {
			sc.key, err = sessionKey34(s.Key, sc.remoteNonce)
		}
		return err // no reply
	case cmdDPQuery, cmdDPQueryNew:
		if (cmd == cmdDPQuery) != (s.Version == V33) {
			return fmt.Errorf("tuya sim: query 0x%02x for version %s", cmd, s.Version)
		}
		body, _ := json.Marshal(map[string]any{"devId": s.ID, "dps": s.DPS()})
		return s.reply(sc, seq, cmd, 0, body)
	case cmdControl, cmdControlNew:
		if (cmd == cmdControl) != (s.Version == V33) {
			return fmt.Errorf("tuya sim: control 0x%02x for version %s", cmd, s.Version)
		}
		if s.Version != V33 { // 3.4/3.5: the header is inside the plaintext
			if !bytes.HasPrefix(plain, versionHeader(s.Version)) {
				return fmt.Errorf("tuya sim: control without version header")
			}
			plain = plain[15:]
		}
		var req struct {
			DPS  map[string]any `json:"dps"`
			Data struct {
				DPS map[string]any `json:"dps"`
			} `json:"data"`
		}
		if err := json.Unmarshal(plain, &req); err != nil {
			return fmt.Errorf("tuya sim: control body: %w", err)
		}
		dps := req.DPS
		if s.Version != V33 {
			dps = req.Data.DPS
		}
		if s.Reject != 0 {
			return s.reply(sc, seq, cmd, s.Reject, nil)
		}
		s.mu.Lock()
		for k, v := range dps {
			if _, ok := s.dps[k]; ok {
				s.dps[k] = v
			}
		}
		if s.OnSet != nil {
			s.OnSet(maps.Clone(dps))
		}
		s.mu.Unlock()
		return s.reply(sc, seq, cmd, 0, nil)
	}
	return s.reply(sc, seq, cmd, 1, nil)
}

// unframe checks and decrypts a client frame.
func (s *SimDevice) unframe(sc *simConn, pkt []byte) (cmd, seq uint32, plain []byte, err error) {
	if s.Version == V35 {
		if len(pkt) < 18 {
			return 0, 0, nil, fmt.Errorf("tuya sim: short frame")
		}
		cmd, plain, err := open6699(sc.key, pkt)
		return cmd, binary.BigEndian.Uint32(pkt[6:]), plain, err
	}
	hmacOn := s.Version == V34
	trailer := 4
	if hmacOn {
		trailer = 32
	}
	if len(pkt) < 16+trailer+4 || binary.BigEndian.Uint32(pkt) != prefix55aa {
		return 0, 0, nil, fmt.Errorf("tuya sim: not a 0x55aa frame")
	}
	seq, cmd = binary.BigEndian.Uint32(pkt[4:]), binary.BigEndian.Uint32(pkt[8:])
	end := len(pkt) - trailer - 4
	if hmacOn {
		if !bytes.Equal(pkt[end:end+32], hmacSHA256(sc.key, pkt[:end])) {
			return 0, 0, nil, fmt.Errorf("tuya sim: bad HMAC")
		}
	} else if binary.BigEndian.Uint32(pkt[end:]) != crc32.ChecksumIEEE(pkt[:end]) {
		return 0, 0, nil, fmt.Errorf("tuya sim: bad CRC")
	}
	payload := pkt[16:end] // requests have no return code
	if s.Version == V33 && cmd == cmdControl {
		if !bytes.HasPrefix(payload, versionHeader(V33)) {
			return 0, 0, nil, fmt.Errorf("tuya sim: control without version header")
		}
		payload = payload[15:]
	}
	plain, err = ecbDecrypt(sc.key, payload)
	return cmd, seq, plain, err
}

// reply sends a frame with a return code and an optional body.
func (s *SimDevice) reply(sc *simConn, seq, cmd, code uint32, body []byte) error {
	rc := make([]byte, 4)
	binary.BigEndian.PutUint32(rc, code)
	var pkt []byte
	if s.Version == V35 {
		var err error
		if pkt, err = sealReply6699(sc.key, seq, cmd, append(rc, body...)); err != nil {
			return err
		}
	} else {
		var enc []byte
		if body != nil {
			var err error
			if enc, err = ecbEncrypt(sc.key, body); err != nil {
				return err
			}
		}
		var hmacKey []byte
		if s.Version == V34 {
			hmacKey = sc.key
		}
		pkt = pack55aa(seq, cmd, append(rc, enc...), hmacKey)
	}
	_, err := sc.c.Write(pkt)
	return err
}

// sealReply6699 is pack6699 for a reply (same layout; kept apart so the
// client builder stays the one under test).
func sealReply6699(key []byte, seq, cmd uint32, plain []byte) ([]byte, error) {
	c, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	g, err := cipher.NewGCM(c)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 12)
	rand.Read(iv)
	hdr := make([]byte, 18)
	binary.BigEndian.PutUint32(hdr[0:], prefix6699)
	binary.BigEndian.PutUint32(hdr[6:], seq)
	binary.BigEndian.PutUint32(hdr[10:], cmd)
	binary.BigEndian.PutUint32(hdr[14:], uint32(len(iv)+len(plain)+16))
	out := append(hdr, iv...)
	out = append(out, g.Seal(nil, iv, plain, hdr[4:18])...)
	return binary.BigEndian.AppendUint32(out, suffix6699), nil
}
