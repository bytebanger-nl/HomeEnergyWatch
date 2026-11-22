package tuya

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestLiveQuery hits a real device. It is skipped unless TUYA_ADDR, TUYA_ID,
// TUYA_KEY and TUYA_VERSION are set, so no secrets live in the repo and CI
// never needs a device.
func TestLiveQuery(t *testing.T) {
	addr := os.Getenv("TUYA_ADDR")
	id := os.Getenv("TUYA_ID")
	key := os.Getenv("TUYA_KEY")
	ver := os.Getenv("TUYA_VERSION")
	if addr == "" || id == "" || key == "" || ver == "" {
		t.Skip("set TUYA_ADDR/TUYA_ID/TUYA_KEY/TUYA_VERSION to run the live query")
	}
	d := Device{Addr: addr, ID: id, Key: []byte(key), Version: Version(ver)}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	dps, err := d.Query(ctx)
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(dps) == 0 {
		t.Fatal("no dps returned")
	}
	t.Logf("dps: %v", dps)
}
