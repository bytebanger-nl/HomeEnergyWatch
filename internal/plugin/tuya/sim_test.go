package tuya

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// startSim serves a simulated device on a loopback port and returns the
// client side for it.
func startSim(t *testing.T, sim *SimDevice) Device {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go sim.Serve(ln)
	return Device{Addr: ln.Addr().String(), ID: sim.ID, Key: sim.Key, Version: sim.Version}
}

func TestQueryAndSetEveryVersion(t *testing.T) {
	for _, v := range []Version{V33, V34, V35} {
		t.Run(string(v), func(t *testing.T) {
			sim := NewSimDevice("bf0123456789abcdef", testKey, v, map[string]any{"1": false, "2": float64(180), "4": "eco"})
			var written map[string]any
			sim.OnSet = func(dps map[string]any) { written = dps }
			d := startSim(t, sim)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			dps, err := d.Query(ctx)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if dps["1"] != false || dps["4"] != "eco" {
				t.Fatalf("dps = %v", dps)
			}
			if err := d.Set(ctx, map[string]any{"1": true, "2": 215}); err != nil {
				t.Fatalf("Set: %v", err)
			}
			if written["1"] != true || written["2"] != float64(215) {
				t.Fatalf("device got %v", written)
			}
			dps, err = d.Query(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if dps["1"] != true || dps["2"] != float64(215) || dps["4"] != "eco" {
				t.Fatalf("after set dps = %v", dps)
			}
		})
	}
}

func TestSetRejectedAndWrongKey(t *testing.T) {
	for _, v := range []Version{V33, V34, V35} {
		sim := NewSimDevice("dev", testKey, v, map[string]any{"1": false})
		sim.Reject = 2
		d := startSim(t, sim)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := d.Set(ctx, map[string]any{"1": true})
		if err == nil || !strings.Contains(err.Error(), "code 2") {
			t.Fatalf("%s: rejected set: err = %v", v, err)
		}
		if sim.DPS()["1"] != false {
			t.Fatalf("%s: rejected set changed the device", v)
		}
		d.Key = []byte("fedcba9876543210")
		if err := d.Set(ctx, map[string]any{"1": true}); err == nil {
			t.Fatalf("%s: set with the wrong key succeeded", v)
		}
		cancel()
	}
}

func TestFrameLen(t *testing.T) {
	a := pack55aa(1, cmdControl, []byte("0123456789abcdef"), nil)
	b, _ := pack6699(testKey, 2, cmdControlNew, []byte("x"))
	if frameLen(a) != len(a) || frameLen(b) != len(b) {
		t.Fatalf("frameLen = %d/%d, want %d/%d", frameLen(a), frameLen(b), len(a), len(b))
	}
	if frameLen(a[:10]) != 0 || frameLen([]byte("junkjunk")) != -1 {
		t.Fatal("partial or junk frame")
	}
}
