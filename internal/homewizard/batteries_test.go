package homewizard

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
)

func TestBatteries(t *testing.T) {
	name := "appliance/p1dongle/aabbccddeeff"
	pool, cert := testCA(t, name)
	var put map[string]any
	host := v2Server(t, cert, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/batteries" || r.Header.Get("Authorization") != "Bearer TOKEN" || r.Header.Get("X-Api-Version") != "2" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		mode := "zero"
		if r.Method == http.MethodPut {
			b, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(b, &put)
			mode = "standby"
		}
		_, _ = w.Write([]byte(`{"mode":"` + mode + `","permissions":["charge_allowed"],"charge_to_full":false,"battery_count":2,"power_w":-404,"target_power_w":-400,"max_consumption_w":1600,"max_production_w":800}`))
	}))
	c := NewClient(host, 2, "TOKEN", name, Options{RootCAs: pool})
	ctx := context.Background()
	b, err := c.Batteries(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if b.Mode != BatteryZero || *b.BatteryCount != 2 || b.PowerW != -404 || b.MaxConsumptionW != 1600 || *b.ChargeToFull {
		t.Errorf("batteries = %+v", b)
	}
	if !b.Permitted(PermitCharge) || b.Permitted(PermitDischarge) {
		t.Errorf("permissions = %v", *b.Permissions)
	}
	none := []string{}
	b, err = c.SetBatteries(ctx, BatteriesUpdate{Mode: BatteryZero, Permissions: &none})
	if err != nil || b.Mode != BatteryStandby {
		t.Fatalf("set: %+v %v", b, err)
	}
	if put["mode"] != "zero" || put["permissions"] == nil || len(put["permissions"].([]any)) != 0 || put["charge_to_full"] != nil {
		t.Errorf("sent %v", put)
	}
	if _, err := NewClient("127.0.0.1:1", 1, "", "", Options{}).Batteries(ctx); !errors.Is(err, ErrUnsupported) {
		t.Errorf("v1: %v", err)
	}
}

func TestPermittedLegacy(t *testing.T) {
	for _, c := range []struct {
		mode              string
		charge, discharge bool
	}{
		{BatteryZero, true, true},
		{BatteryStandby, false, false},
		{BatteryToFull, true, false},
	} {
		b := Batteries{Mode: c.mode}
		if b.Permitted(PermitCharge) != c.charge || b.Permitted(PermitDischarge) != c.discharge {
			t.Errorf("%s: charge %v discharge %v", c.mode, b.Permitted(PermitCharge), b.Permitted(PermitDischarge))
		}
	}
}
