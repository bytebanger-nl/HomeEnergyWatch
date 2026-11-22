package mhi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// fakeUnit answers the beaver commands Set uses.
type fakeUnit struct {
	mu         sync.Mutex
	registered bool
	busy       bool
	calls      []string
	frame      string // last setAirconStat frame
	stat       string // getAirconStat answer; sampleStat when empty
}

func (f *fakeUnit) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Contents map[string]any `json:"contents"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	cmd := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, cmd)
	res := map[string]any{"result": 0}
	switch cmd {
	case "getAirconStat":
		stat := f.stat
		if stat == "" {
			stat = sampleStat
		}
		res["contents"] = map[string]any{"airconStat": stat, "timezone": "Europe/Amsterdam"}
	case "updateAccountInfo":
		f.registered = req.Contents["accountId"] == operatorID
	case "setAirconStat":
		switch {
		case !f.registered:
			res["result"] = 2
		case f.busy:
			res["result"] = 11
		default:
			f.frame, _ = req.Contents["airconStat"].(string)
		}
	}
	_ = json.NewEncoder(w).Encode(res)
}

func TestMHISet(t *testing.T) {
	unit := &fakeUnit{}
	srv := httptest.NewTLSServer(unit)
	defer srv.Close()
	in := &instance{cli: newClient(), watch: plugin.NewWatchdog("0"), control: true}
	defer in.Close()
	dev := plugin.Device{ID: "d1", Address: strings.TrimPrefix(srv.URL, "https://"), Config: map[string]string{"airconId": "abc"}}
	ctx := context.Background()

	if err := in.Set(ctx, dev, "preset", 23.0); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(unit.calls, ","); got != "getAirconStat,setAirconStat,updateAccountInfo,setAirconStat" {
		t.Fatalf("calls %s", got)
	}
	st, err := decodeStat(unit.frame)
	if err != nil || st.PresetC != 23 || st.Mode != "HEAT" || !st.Power || st.Fan != "QUIET" {
		t.Fatalf("written state %+v %v", st, err)
	}

	unit.calls = nil
	if err := in.Set(ctx, dev, "mode", "COOL"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(unit.calls, ","); got != "getAirconStat,setAirconStat" {
		t.Fatalf("second write registered again: %s", got)
	}

	unit.busy = true
	if err := in.Set(ctx, dev, "power", false); err == nil || !strings.Contains(err.Error(), "probeer het over een minuut") {
		t.Fatalf("busy unit: %v", err)
	}
}

// A unit answered setAirconStat with JSON that does not parse; the result
// code is still read, and a reply without one shows in the error.
func TestBrokenReply(t *testing.T) {
	body := `{"apiVer":"1.0","result":0,"contents":{"airconId":"abc"}airconStat:"x"}`
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getDeviceInfo") {
			w.Write([]byte(`{"result":11,"x"y}`))
			return
		}
		if strings.HasSuffix(r.URL.Path, "/getAirconStat") {
			w.Write([]byte(`not json at all`))
			return
		}
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c := newClient()
	defer c.close()
	ip := strings.TrimPrefix(srv.URL, "https://")
	ctx := context.Background()
	if err := c.setStat(ctx, ip, "abc", "frame"); err != nil {
		t.Fatalf("broken reply with result 0: %v", err)
	}
	if _, err := c.deviceInfo(ctx, ip); resultCode(err) != 11 {
		t.Fatalf("broken reply with result 11: %v", err)
	}
	if _, err := c.airconStat(ctx, ip, "abc"); err == nil || !strings.Contains(err.Error(), "not json at all") {
		t.Fatalf("reply without result: %v", err)
	}
}

// The vanes: every position is written, a chosen position turns 3D auto
// off, and 3D auto can be switched on again.
func TestMHIVanes(t *testing.T) {
	unit := &fakeUnit{registered: true}
	srv := httptest.NewTLSServer(unit)
	defer srv.Close()
	in := &instance{cli: newClient(), watch: plugin.NewWatchdog("0"), control: true}
	defer in.Close()
	dev := plugin.Device{ID: "d1", Address: strings.TrimPrefix(srv.URL, "https://"), Config: map[string]string{"airconId": "abc"}}
	ctx := context.Background()

	cs := in.Controls(dev)
	if len(cs) != 7 || len(cs[4].Options) != 5 || len(cs[5].Options) != 8 {
		t.Fatalf("controls %+v", cs)
	}
	for i := range 5 {
		if err := in.Set(ctx, dev, "swing_ud", strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		if st, err := decodeStat(unit.frame); err != nil || st.SwingUD != i || st.Entrust || st.Mode != "HEAT" || st.PresetC != 21.5 {
			t.Fatalf("swing_ud %d: written %+v %v", i, st, err)
		}
	}
	for i := range 8 {
		if err := in.Set(ctx, dev, "swing_lr", strconv.Itoa(i)); err != nil {
			t.Fatal(err)
		}
		if st, err := decodeStat(unit.frame); err != nil || st.SwingLR != i || st.Entrust {
			t.Fatalf("swing_lr %d: written %+v %v", i, st, err)
		}
	}
	if err := in.Set(ctx, dev, "entrust", true); err != nil {
		t.Fatal(err)
	}
	if st, _ := decodeStat(unit.frame); !st.Entrust {
		t.Fatal("3D auto not written")
	}
	if err := in.Set(ctx, dev, "swing_ud", "x"); err == nil || err == plugin.ErrNoControl {
		t.Fatalf("bad position: %v", err)
	}
	labels := func(st State) map[string]any {
		out := map[string]any{}
		for _, x := range metrics(st) {
			out[x.Key] = x.Value
		}
		return out
	}
	if l := labels(State{Mode: "HEAT", Fan: "LOW", SwingUD: 2, SwingLR: 6}); l["swing_ud"] != "Hoog" ||
		l["swing_lr"] != "Breed (links en rechts)" || l["entrust"] != false || l["mode"] != "Verwarmen" || l["fan"] != "Laag" {
		t.Fatalf("metrics %v", l)
	}

	// With 3D auto on, swinging keeps it on; a fixed position turns it off.
	unit.stat = patchStat(t, sampleStat, func(c []byte) { c[12] = c[12]&^12 | 4 })
	if st, err := decodeStat(unit.stat); err != nil || !st.Entrust {
		t.Fatalf("patched stat %+v %v", st, err)
	}
	if err := in.Set(ctx, dev, "swing_lr", "0"); err != nil {
		t.Fatal(err)
	}
	if st, _ := decodeStat(unit.frame); !st.Entrust || st.SwingLR != 0 {
		t.Fatalf("swinging turned 3D auto off: %+v", st)
	}

	// A vane position the decoder does not know is not shown, and another
	// change leaves the vanes alone (no set-bit in the command half).
	unit.stat = patchStat(t, sampleStat, func(c []byte) { c[2] &^= 192; c[3] = c[3]&15 | 64; c[12] &^= 3; c[11] = c[11]&^31 | 20 })
	st, err := decodeStat(unit.stat)
	if err != nil || st.SwingUD != -1 || st.SwingLR != -1 {
		t.Fatalf("unknown vanes %+v %v", st, err)
	}
	if l := labels(st); l["swing_ud"] != nil || l["swing_lr"] != nil {
		t.Fatalf("unknown vanes shown: %v", l)
	}
	if err := in.Set(ctx, dev, "preset", 22.0); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(unit.frame)
	if raw[2]&192 != 0 || raw[3]&240 != 0 || raw[12]&3 != 0 || raw[11]&16 != 0 {
		t.Fatalf("unknown vanes written: % x", raw[:18])
	}
}

// patchStat changes the 18 receive-half state bytes of an airconStat blob.
func patchStat(t *testing.T, b64 string, change func(c []byte)) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	start := int(int8(raw[18]))*4 + 21
	change(raw[start : start+18])
	return base64.StdEncoding.EncodeToString(raw)
}
