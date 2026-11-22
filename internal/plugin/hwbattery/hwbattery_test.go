package hwbattery

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// fakeMeters is a P1 meter with a battery group that applies updates like
// the API describes.
type fakeMeters struct {
	views []gateway.DeviceView
	b     homewizard.Batteries
	sent  []homewizard.BatteriesUpdate
}

func (f *fakeMeters) Views() []gateway.DeviceView { return f.views }

func (f *fakeMeters) Batteries(_ context.Context, id string) (homewizard.Batteries, error) {
	if id != "p1" {
		return homewizard.Batteries{}, gateway.ErrNotFound
	}
	return f.b, nil
}

func (f *fakeMeters) SetBatteries(_ context.Context, id string, u homewizard.BatteriesUpdate) (homewizard.Batteries, error) {
	f.sent = append(f.sent, u)
	if u.Mode != "" {
		f.b.Mode = u.Mode
	}
	if u.Permissions != nil {
		p := append([]string{}, *u.Permissions...)
		f.b.Permissions = &p
		if len(p) == 0 {
			f.b.Mode = homewizard.BatteryStandby
		}
	}
	if u.ChargeToFull != nil {
		v := *u.ChargeToFull
		f.b.ChargeToFull = &v
	}
	return f.b, nil
}

func newFirmware() homewizard.Batteries {
	off, n := false, 1
	return homewizard.Batteries{Mode: homewizard.BatteryZero, Permissions: permissions(homewizard.PermitCharge, homewizard.PermitDischarge),
		ChargeToFull: &off, BatteryCount: &n, PowerW: 300, MaxConsumptionW: 800, MaxProductionW: 800}
}

func meters(b homewizard.Batteries) *fakeMeters {
	return &fakeMeters{b: b, views: []gateway.DeviceView{
		{ID: "p1", Name: "P1", Kind: homewizard.KindP1, Paired: true, Enabled: true, Status: gateway.StatusOnline},
		{ID: "bat", Kind: homewizard.KindBattery, Status: gateway.StatusOnline, Reading: &homewizard.Reading{Values: map[string]float64{"state_of_charge_pct": 60}}},
		{ID: "bat2", Kind: homewizard.KindBattery, Status: gateway.StatusOnline, Reading: &homewizard.Reading{Values: map[string]float64{"state_of_charge_pct": 80}}},
	}}
}

func newInstance(t *testing.T, m Meters) *instance {
	t.Helper()
	inst, err := Provider{Meters: m}.Instance(nil, plugin.Env{})
	if err != nil {
		t.Fatal(err)
	}
	return inst.(*instance)
}

func metric(r plugin.Reading, key string) any {
	for _, m := range r.Metrics {
		if m.Key == key {
			return m.Value
		}
	}
	return nil
}

func TestScan(t *testing.T) {
	m := meters(newFirmware())
	got, err := newInstance(t, m).Scan(context.Background())
	if err != nil || len(got) != 1 || got[0].Address != "p1" || got[0].Name != "Thuisbatterij" {
		t.Fatalf("scan = %+v, %v", got, err)
	}
	// a P1 on API v1: nothing found, and why
	m.views[0].Paired = false
	if _, err := newInstance(t, m).Scan(context.Background()); err == nil {
		t.Fatal("no hint about pairing")
	}
}

func TestPollAndControls(t *testing.T) {
	m := meters(newFirmware())
	i := newInstance(t, m)
	dev := plugin.Device{Address: "p1"}
	// before the first poll every stand is offered
	if n := len(i.Controls(dev)[0].Options); n != len(stands) {
		t.Errorf("options before poll = %d", n)
	}
	r, err := i.Poll(context.Background(), dev)
	if err != nil || !r.Online {
		t.Fatalf("poll = %+v, %v", r, err)
	}
	if metric(r, "stand") != "Nul op de meter" || metric(r, "power_w") != 300.0 || metric(r, "state_of_charge_pct") != 70.0 || metric(r, "charge_allowed") != true {
		t.Errorf("metrics = %+v", r.Metrics)
	}
	if r, _ := i.Poll(context.Background(), plugin.Device{Address: "gone"}); r.Online || r.Err == "" {
		t.Errorf("unknown meter = %+v", r)
	}

	// an older firmware (API 2.1): no permissions, no charge_to_full
	m.b = homewizard.Batteries{Mode: homewizard.BatteryZero}
	if _, err := i.Poll(context.Background(), dev); err != nil {
		t.Fatal(err)
	}
	var values []string
	for _, o := range i.Controls(dev)[0].Options {
		values = append(values, o.Value)
	}
	if want := []string{standZero, standStandby, standFull}; !reflect.DeepEqual(values, want) {
		t.Errorf("options on API 2.1 = %v, want %v", values, want)
	}
}

func TestSet(t *testing.T) {
	ctx := context.Background()
	dev := plugin.Device{Address: "p1"}
	for _, c := range []struct {
		name  string
		start homewizard.Batteries
		stand string
		want  string // stand afterwards
		last  homewizard.BatteriesUpdate
	}{
		{"charge only", newFirmware(), standCharge, standCharge,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero, Permissions: permissions(homewizard.PermitCharge)}},
		{"discharge only", newFirmware(), standDischarge, standDischarge,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero, Permissions: permissions(homewizard.PermitDischarge)}},
		{"standby", newFirmware(), standStandby, standStandby,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero, Permissions: permissions()}},
		{"to full", newFirmware(), standFull, standFull,
			homewizard.BatteriesUpdate{ChargeToFull: ptr(true)}},
		{"predictive", newFirmware(), standPredictive, standPredictive,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryPredictive}},
		{"standby on 2.1", homewizard.Batteries{Mode: homewizard.BatteryZero}, standStandby, standStandby,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryStandby}},
		{"to full on 2.1", homewizard.Batteries{Mode: homewizard.BatteryZero}, standFull, standFull,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryToFull}},
		{"zero on 2.1", homewizard.Batteries{Mode: homewizard.BatteryStandby}, standZero, standZero,
			homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := meters(c.start)
			if err := newInstance(t, m).Set(ctx, dev, keyMode, c.stand); err != nil {
				t.Fatal(err)
			}
			if got := standOf(m.b); got != c.want {
				t.Errorf("stand = %s, want %s", got, c.want)
			}
			if last := m.sent[len(m.sent)-1]; !reflect.DeepEqual(last, c.last) {
				t.Errorf("sent %+v, want %+v", last, c.last)
			}
		})
	}
}

func TestSetEndsToFullFirst(t *testing.T) {
	b := newFirmware()
	b.ChargeToFull = ptr(true)
	m := meters(b)
	if err := newInstance(t, m).Set(context.Background(), plugin.Device{Address: "p1"}, keyMode, standDischarge); err != nil {
		t.Fatal(err)
	}
	if len(m.sent) != 2 || m.sent[0].ChargeToFull == nil || *m.sent[0].ChargeToFull {
		t.Errorf("sent %+v", m.sent)
	}
	if standOf(m.b) != standDischarge {
		t.Errorf("stand = %s", standOf(m.b))
	}
}

func TestSetRefuses(t *testing.T) {
	ctx := context.Background()
	m := meters(homewizard.Batteries{Mode: homewizard.BatteryZero}) // API 2.1
	i := newInstance(t, m)
	if err := i.Set(ctx, plugin.Device{Address: "p1"}, keyMode, standCharge); !errors.Is(err, plugin.ErrValue) {
		t.Errorf("charge only on 2.1: %v", err)
	}
	if err := i.Set(ctx, plugin.Device{Address: "p1"}, keyMode, "boost"); !errors.Is(err, plugin.ErrValue) {
		t.Errorf("unknown stand: %v", err)
	}
	if err := i.Set(ctx, plugin.Device{Address: "p1"}, "other", standZero); !errors.Is(err, plugin.ErrNoControl) {
		t.Errorf("unknown control: %v", err)
	}
	if len(m.sent) != 0 {
		t.Errorf("sent %+v", m.sent)
	}
}

func TestStandOf(t *testing.T) {
	for want, b := range map[string]homewizard.Batteries{
		standZero:       {Mode: homewizard.BatteryZero},
		standStandby:    {Mode: homewizard.BatteryZero, Permissions: permissions()},
		standFull:       {Mode: homewizard.BatteryToFull},
		standPredictive: {Mode: homewizard.BatteryPredictive, Permissions: permissions(homewizard.PermitCharge)},
		standCharge:     {Mode: homewizard.BatteryZero, Permissions: permissions(homewizard.PermitCharge)},
	} {
		if got := standOf(b); got != want {
			t.Errorf("standOf(%+v) = %s, want %s", b, got, want)
		}
	}
}

func ptr[T any](v T) *T { return &v }
