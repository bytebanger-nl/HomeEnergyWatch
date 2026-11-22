// Package hwbattery is the "homewizard_battery" plugin type: the Plug-In
// Batteries a HomeWizard P1 meter (or kWh meter) controls, through that
// meter's /api/batteries (API v2). It uses the meters the gateway already
// polls and has paired, so it needs no configuration of its own; as a
// plugin, its control goes through the same path as every other device
// (package control, the rules' plugin_set action, tiles) and only works once
// the user allowed control for the instance.
package hwbattery

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// PluginType is the type name.
const PluginType = "homewizard_battery"

// Meters is what the type needs from the gateway: the devices, and the
// battery group of a meter.
type Meters interface {
	Views() []gateway.DeviceView
	Batteries(ctx context.Context, id string) (homewizard.Batteries, error)
	SetBatteries(ctx context.Context, id string, u homewizard.BatteriesUpdate) (homewizard.Batteries, error)
}

// Provider registers the type; Meters is the gateway's manager.
type Provider struct{ Meters Meters }

// Info describes the type.
func (p Provider) Info() plugin.ProviderInfo {
	return plugin.ProviderInfo{
		Type: PluginType,
		Name: "Thuisbatterij (via P1-meter)",
		Description: "De Plug-In Batteries die je P1-meter aanstuurt: stand, vermogen en laadtoestand, en de stand kiezen " +
			"(nul op de meter, alleen laden, alleen ontladen, stand-by, volladen). De P1-meter moet via API v2 gekoppeld zijn " +
			"(Apparaten → Koppelen). Zoeken vindt hem vanzelf.",
		InstanceForm:    []plugin.Field{},
		DeviceForm:      []plugin.Field{},
		CanScan:         true,
		IntervalSeconds: 5,
	}
}

// Instance builds the instance; it does not touch the network.
func (p Provider) Instance(_ map[string]string, _ plugin.Env) (plugin.Instance, error) {
	if p.Meters == nil {
		return nil, errors.New("thuisbatterij: niet beschikbaar")
	}
	return &instance{meters: p.Meters, last: map[string]homewizard.Batteries{}}, nil
}

type instance struct {
	meters Meters
	mu     sync.Mutex
	last   map[string]homewizard.Batteries // by meter id: the latest poll
}

func (i *instance) Close() {}

// Scan lists the paired meters that report a battery group with at least
// one battery. A device's address is the meter's id.
func (i *instance) Scan(ctx context.Context) ([]plugin.Candidate, error) {
	var out []plugin.Candidate
	var meters []gateway.DeviceView
	notPaired := false
	for _, v := range i.meters.Views() {
		if v.Kind != homewizard.KindP1 && v.Kind != homewizard.KindKWH {
			continue
		}
		if !gateway.CanControlBatteries(v) {
			notPaired = notPaired || v.Enabled
			continue
		}
		b, err := i.meters.Batteries(ctx, v.ID)
		if err != nil || b.BatteryCount != nil && *b.BatteryCount == 0 {
			continue
		}
		meters = append(meters, v)
	}
	for _, v := range meters {
		name := "Thuisbatterij"
		if len(meters) > 1 {
			name += " (" + v.Name + ")"
		}
		out = append(out, plugin.Candidate{Address: v.ID, Name: name})
	}
	if len(out) == 0 && notPaired {
		return nil, errors.New("geen meter met batterijen gevonden; koppel de P1-meter eerst via API v2 (Apparaten → Koppelen)")
	}
	return out, nil
}

// Poll reads the battery group of the meter dev.Address.
func (i *instance) Poll(ctx context.Context, dev plugin.Device) (plugin.Reading, error) {
	b, err := i.meters.Batteries(ctx, dev.Address)
	if err != nil {
		if errors.Is(err, gateway.ErrNoBatteryControl) || errors.Is(err, gateway.ErrNotFound) || errors.Is(err, homewizard.ErrUnsupported) {
			return plugin.Reading{Online: false, Err: err.Error()}, nil
		}
		return plugin.Reading{}, err
	}
	i.mu.Lock()
	i.last[dev.Address] = b
	i.mu.Unlock()
	return reading(b, i.charge()), nil
}

// charge is the mean state of charge of the batteries the gateway reads.
func (i *instance) charge() *float64 {
	var sum float64
	n := 0
	for _, v := range i.meters.Views() {
		if v.Kind != homewizard.KindBattery || v.Status != gateway.StatusOnline || v.Reading == nil {
			continue
		}
		if soc, ok := v.Reading.Value("state_of_charge_pct"); ok {
			sum += soc
			n++
		}
	}
	if n == 0 {
		return nil
	}
	mean := sum / float64(n)
	return &mean
}

// Keys of the metrics and the control.
const (
	keyStand = "stand"
	keyMode  = "mode"
)

func reading(b homewizard.Batteries, soc *float64) plugin.Reading {
	num := func(key, label string, v float64, unit string) plugin.Metric {
		return plugin.Metric{Key: key, Label: label, Kind: plugin.MetricNumber, Value: v, Unit: unit}
	}
	ms := []plugin.Metric{
		{Key: keyStand, Label: "Stand", Kind: plugin.MetricText, Value: label(standOf(b))},
		num("power_w", "Vermogen", b.PowerW, "W"), // + charging, − discharging
		num("target_power_w", "Doelvermogen", b.TargetPowerW, "W"),
	}
	if soc != nil {
		ms = append(ms, num("state_of_charge_pct", "Laadtoestand", *soc, "%"))
	}
	ms = append(ms,
		plugin.Metric{Key: "charge_allowed", Label: "Laden toegestaan", Kind: plugin.MetricBool, Value: b.Permitted(homewizard.PermitCharge)},
		plugin.Metric{Key: "discharge_allowed", Label: "Ontladen toegestaan", Kind: plugin.MetricBool, Value: b.Permitted(homewizard.PermitDischarge)},
		num("max_consumption_w", "Max. laadvermogen", b.MaxConsumptionW, "W"),
		num("max_production_w", "Max. ontlaadvermogen", b.MaxProductionW, "W"),
	)
	if b.BatteryCount != nil {
		ms = append(ms, num("battery_count", "Batterijen", float64(*b.BatteryCount), ""))
	}
	return plugin.Reading{Online: true, Metrics: ms}
}

// Controls: one select, the stand. Before the first poll every stand is
// offered; after it, only those the meter's firmware knows.
func (i *instance) Controls(dev plugin.Device) []plugin.Control {
	i.mu.Lock()
	b, known := i.last[dev.Address]
	i.mu.Unlock()
	c := plugin.Control{Key: keyMode, Label: "Stand", Kind: plugin.ControlSelect, Metric: keyStand}
	for _, s := range stands {
		if !known || s.supported(b) {
			c.Options = append(c.Options, plugin.Option{Value: s.value, Label: s.label})
		}
	}
	return []plugin.Control{c}
}

// Set chooses a stand. The meter is read first, so the request fits its
// firmware (permissions since API 2.2.0, charge_to_full and predictive
// since 2.3.0).
func (i *instance) Set(ctx context.Context, dev plugin.Device, key string, value any) error {
	if key != keyMode {
		return plugin.ErrNoControl
	}
	v, _ := value.(string)
	s, ok := standByValue(v)
	if !ok {
		return fmt.Errorf("%w: onbekende stand %q", plugin.ErrValue, v)
	}
	cur, err := i.meters.Batteries(ctx, dev.Address)
	if err != nil {
		return err
	}
	if !s.supported(cur) {
		return fmt.Errorf("%w: de firmware van de meter kent de stand %q niet", plugin.ErrValue, s.label)
	}
	// a running "to full" is ended first: during it the permissions
	// cannot be changed
	if s.value != standFull && charging(cur) {
		off := false
		u := homewizard.BatteriesUpdate{ChargeToFull: &off}
		if cur.ChargeToFull == nil {
			u = homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero}
		}
		if cur, err = i.meters.SetBatteries(ctx, dev.Address, u); err != nil {
			return err
		}
	}
	b, err := i.meters.SetBatteries(ctx, dev.Address, s.update(cur))
	if err != nil {
		return err
	}
	i.mu.Lock()
	i.last[dev.Address] = b
	i.mu.Unlock()
	return nil
}
