package tuya

import (
	"bytes"
	"io"
	"testing"
	"testing/iotest"
)

// readFrame reads one frame by its length: also when TCP splits it into
// single bytes, and it leaves a second frame in the same read alone.
func TestReadFrame(t *testing.T) {
	key := []byte("0123456789abcdef")
	a := pack55aa(1, cmdDPQuery, []byte("first payload"), nil)
	b := pack55aa(2, cmdDPQuery, []byte("second"), key)
	g, err := pack6699(key, 3, cmdDPQueryNew, []byte(`{"dps":{"1":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	stream := bytes.Join([][]byte{a, b, g}, nil)

	for name, r := range map[string]io.Reader{
		"merged":   bytes.NewReader(stream),
		"one byte": iotest.OneByteReader(bytes.NewReader(stream)),
	} {
		for i, want := range [][]byte{a, b, g} {
			got, err := readFrame(r)
			if err != nil {
				t.Fatalf("%s frame %d: %v", name, i, err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("%s frame %d: %d bytes, want %d", name, i, len(got), len(want))
			}
		}
	}
	if _, _, err := unpack55aa(a, false); err != nil {
		t.Fatalf("frame does not unpack: %v", err)
	}
	if _, plain, err := open6699(key, g); err != nil || string(plain) != `{"dps":{"1":true}}` {
		t.Fatalf("6699: %q %v", plain, err)
	}

	bad := append([]byte(nil), a...)
	bad[12], bad[13] = 0x7f, 0xff // announces far more than maxFrame
	if _, err := readFrame(bytes.NewReader(bad)); err == nil {
		t.Fatal("oversized frame accepted")
	}
	if _, err := readFrame(bytes.NewReader([]byte("HTTP/1.1 400 Bad Request\r\n"))); err == nil {
		t.Fatal("not a frame accepted")
	}
}

// A query reply may come after a status the device sent by itself and an
// empty acknowledgement; both are skipped.
func TestQueryReplySkips(t *testing.T) {
	key := []byte("0123456789abcdef")
	status, _ := pack6699(key, 9, cmdControlNew, []byte("\x00\x00\x00\x00"))
	ack, _ := pack6699(key, 10, cmdDPQueryNew, []byte("\x00\x00\x00\x00"))
	data, _ := pack6699(key, 11, cmdDPQueryNew, append([]byte("\x00\x00\x00\x00"), `{"dps":{"1":true,"2":215}}`...))
	r := bytes.NewReader(bytes.Join([][]byte{status, ack, data}, nil))
	dps, err := queryReply(r, []uint32{cmdDPQueryNew, cmdStatus}, func(f []byte) (map[string]any, bool, error) {
		_, _, body, err := unframeSession(f, key, true)
		if err != nil || len(body) == 0 {
			return nil, false, err
		}
		d, err := parseDPS(body)
		return d, true, err
	})
	if err != nil || dps["1"] != true || dps["2"] != float64(215) {
		t.Fatalf("dps %v, err %v", dps, err)
	}
	// nothing but other commands: an error, not a hang
	many := bytes.Repeat(status, maxSkip)
	if _, err := readReply(bytes.NewReader(many), cmdDPQueryNew); err == nil {
		t.Fatal("no error without a reply")
	}
}
