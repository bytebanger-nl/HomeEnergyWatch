package tuya

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"testing"
)

var testKey = []byte("0123456789abcdef") // 16 bytes

func TestECBRoundtrip(t *testing.T) {
	for _, msg := range []string{"", "hi", `{"dps":{"1":true}}`, "exactly-16-bytes"} {
		enc, err := ecbEncrypt(testKey, []byte(msg))
		if err != nil {
			t.Fatalf("encrypt %q: %v", msg, err)
		}
		if len(enc)%16 != 0 {
			t.Fatalf("ciphertext not block-aligned: %d", len(enc))
		}
		dec, err := ecbDecrypt(testKey, enc)
		if err != nil {
			t.Fatalf("decrypt %q: %v", msg, err)
		}
		if string(dec) != msg {
			t.Fatalf("roundtrip: got %q want %q", dec, msg)
		}
	}
}

func TestECBDecryptRejectsBadLength(t *testing.T) {
	if _, err := ecbDecrypt(testKey, []byte("not16")); err == nil {
		t.Fatal("expected error for non-block-aligned ciphertext")
	}
}

func TestPack55aaCRCStructure(t *testing.T) {
	payload := []byte("payload-bytes!!!")
	pkt := pack55aa(7, cmdDPQuery, payload, nil)
	if got := binary.BigEndian.Uint32(pkt[0:]); got != prefix55aa {
		t.Fatalf("prefix = %#x", got)
	}
	if got := binary.BigEndian.Uint32(pkt[4:]); got != 7 {
		t.Fatalf("seq = %d", got)
	}
	if got := binary.BigEndian.Uint32(pkt[8:]); got != cmdDPQuery {
		t.Fatalf("cmd = %d", got)
	}
	if got := binary.BigEndian.Uint32(pkt[len(pkt)-4:]); got != suffix55aa {
		t.Fatalf("suffix = %#x", got)
	}
	// length field = payload + crc(4) + suffix(4)
	if got := binary.BigEndian.Uint32(pkt[12:]); int(got) != len(payload)+8 {
		t.Fatalf("length = %d want %d", got, len(payload)+8)
	}
	// CRC32 covers everything before the CRC itself.
	wantCRC := crc32.ChecksumIEEE(pkt[:len(pkt)-8])
	if got := binary.BigEndian.Uint32(pkt[len(pkt)-8:]); got != wantCRC {
		t.Fatalf("crc = %#x want %#x", got, wantCRC)
	}
}

// buildResp55aa mimics a device reply: header, 4-byte return code, payload,
// trailer (CRC or HMAC; not checked by unpack), suffix.
func buildResp55aa(cmd uint32, payload, hmacKey []byte) []byte {
	h := make([]byte, 16)
	binary.BigEndian.PutUint32(h[0:], prefix55aa)
	binary.BigEndian.PutUint32(h[4:], 1)
	binary.BigEndian.PutUint32(h[8:], cmd)
	frame := append(h, 0, 0, 0, 0) // return code
	frame = append(frame, payload...)
	binary.BigEndian.PutUint32(frame[12:], uint32(len(frame)-16+4+4))
	if hmacKey != nil {
		frame = append(frame, hmacSHA256(hmacKey, frame)...)
	} else {
		crc := make([]byte, 4)
		binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(frame))
		frame = append(frame, crc...)
	}
	tail := make([]byte, 4)
	binary.BigEndian.PutUint32(tail, suffix55aa)
	return append(frame, tail...)
}

func TestUnpack55aaResponse(t *testing.T) {
	payload := []byte("sixteen-byte-pay")
	for _, hmacOn := range []bool{false, true} {
		var key []byte
		if hmacOn {
			key = testKey
		}
		pkt := buildResp55aa(cmdSessResp, payload, key)
		cmd, got, err := unpack55aa(pkt, hmacOn)
		if err != nil {
			t.Fatalf("hmac=%v: %v", hmacOn, err)
		}
		if cmd != cmdSessResp {
			t.Fatalf("cmd = %d", cmd)
		}
		if !bytes.Equal(got, payload) {
			t.Fatalf("payload = %q want %q", got, payload)
		}
	}
}

func TestUnframeSession34(t *testing.T) {
	plain := []byte("some-16byte-text")
	enc, err := ecbEncrypt(testKey, plain)
	if err != nil {
		t.Fatal(err)
	}
	pkt := buildResp55aa(cmdSessResp, enc, testKey)
	cmd, code, got, err := unframeSession(pkt, testKey, false)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != cmdSessResp || code != 0 || string(got) != string(plain) {
		t.Fatalf("got cmd=%d code=%d plain=%q", cmd, code, got)
	}
}

func TestPack6699Roundtrip(t *testing.T) {
	body := []byte(`{"dps":{"1":true}}`)
	pkt, err := pack6699(testKey, 3, cmdDPQueryNew, body)
	if err != nil {
		t.Fatal(err)
	}
	if got := binary.BigEndian.Uint32(pkt[0:]); got != prefix6699 {
		t.Fatalf("prefix = %#x", got)
	}
	if got := binary.BigEndian.Uint32(pkt[len(pkt)-4:]); got != suffix6699 {
		t.Fatalf("suffix = %#x", got)
	}
	cmd, got, err := unpack6699(testKey, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if cmd != cmdDPQueryNew {
		t.Fatalf("cmd = %d", cmd)
	}
	if string(got) != string(body) { // starts with '{', no strip
		t.Fatalf("body = %q want %q", got, body)
	}
}

func TestUnpack6699StripsReturnCode(t *testing.T) {
	// A device response prefixes a 4-byte return code before binary payload.
	payload := append([]byte{0, 0, 0, 0}, []byte("remote-nonce-data")...)
	pkt, err := pack6699(testKey, 1, cmdSessResp, payload)
	if err != nil {
		t.Fatal(err)
	}
	_, got, err := unpack6699(testKey, pkt)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "remote-nonce-data" {
		t.Fatalf("got %q, return code not stripped", got)
	}
}

func TestUnpack6699BadKey(t *testing.T) {
	pkt, _ := pack6699(testKey, 1, cmdDPQueryNew, []byte(`{"dps":{}}`))
	other := []byte("fedcba9876543210")
	if _, _, err := unpack6699(other, pkt); err == nil {
		t.Fatal("expected GCM auth failure with wrong key")
	}
}

func TestSessionKeyDerivation(t *testing.T) {
	remote := []byte("fedcba9876543210")
	for _, fn := range []func([]byte, []byte) ([]byte, error){sessionKey34, sessionKey35} {
		k1, err := fn(testKey, remote)
		if err != nil {
			t.Fatal(err)
		}
		if len(k1) != 16 {
			t.Fatalf("session key length %d", len(k1))
		}
		k2, _ := fn(testKey, remote)
		if !bytes.Equal(k1, k2) {
			t.Fatal("session key not deterministic")
		}
		k3, _ := fn(testKey, []byte("0000000000000000"))
		if bytes.Equal(k1, k3) {
			t.Fatal("different nonce produced same session key")
		}
	}
	// The two derivations must differ (ECB vs GCM).
	a, _ := sessionKey34(testKey, remote)
	b, _ := sessionKey35(testKey, remote)
	if bytes.Equal(a, b) {
		t.Fatal("3.4 and 3.5 session keys should differ")
	}
}

func TestParseDPS(t *testing.T) {
	flat, err := parseDPS([]byte(`{"dps":{"1":true,"24":180}}`))
	if err != nil {
		t.Fatal(err)
	}
	if flat["1"] != true {
		t.Fatalf("flat dps 1 = %v", flat["1"])
	}
	nested, err := parseDPS([]byte(`{"protocol":5,"t":1,"data":{"dps":{"2":"hot"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if nested["2"] != "hot" {
		t.Fatalf("nested dps 2 = %v", nested["2"])
	}
	if _, err := parseDPS([]byte(`{"t":1}`)); err == nil {
		t.Fatal("expected error for body without dps")
	}
}

func TestDecodeDPSStripsVersionHeader(t *testing.T) {
	body := []byte(`{"dps":{"1":false}}`)
	enc, err := ecbEncrypt(testKey, body)
	if err != nil {
		t.Fatal(err)
	}
	// Real devices prepend a 15-byte "3.3"+padding header outside the ciphertext.
	withHeader := append([]byte("3.3\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00\x00"), enc...)
	dps, err := decodeDPS(testKey, withHeader)
	if err != nil {
		t.Fatal(err)
	}
	if dps["1"] != false {
		t.Fatalf("dps 1 = %v", dps["1"])
	}
}
