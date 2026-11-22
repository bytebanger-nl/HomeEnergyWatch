package plugin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Types returns the available provider types and their forms.
func (m *Manager) Types() []ProviderInfo { return Providers() }

// instanceRec returns the stored record of one instance.
func (m *Manager) instanceRec(ctx context.Context, id string) (store.PluginInstance, error) {
	all, err := m.store.Plugins(ctx)
	if err != nil {
		return store.PluginInstance{}, err
	}
	for _, p := range all {
		if p.ID == id {
			return p, nil
		}
	}
	return store.PluginInstance{}, store.ErrNotFound
}

// secretKeys returns the keys of secret fields in a form.
func secretKeys(form []Field) map[string]bool {
	out := map[string]bool{}
	for _, f := range form {
		if f.Secret {
			out[f.Key] = true
		}
	}
	return out
}

// validate checks required fields and that the type can build an instance.
func (m *Manager) validate(p Provider, config map[string]string) error {
	for _, f := range p.Info().InstanceForm {
		if f.Required && strings.TrimSpace(config[f.Key]) == "" {
			return fmt.Errorf("%s is verplicht", f.Label)
		}
	}
	inst, err := p.Instance(config, Env{DataDir: m.dataDir})
	if err != nil {
		return err
	}
	inst.Close()
	return nil
}

// AddInstance creates a new plugin instance of the given type.
func (m *Manager) AddInstance(ctx context.Context, typ, name string, config map[string]string) (InstanceView, error) {
	p, ok := provider(typ)
	if !ok {
		return InstanceView{}, ErrUnknownType
	}
	if config == nil {
		config = map[string]string{}
	}
	delete(config, controlKey) // control starts off; only the control flag turns it on
	if err := m.validate(p, config); err != nil {
		return InstanceView{}, err
	}
	if name == "" {
		name = p.Info().Name
	}
	rec := store.PluginInstance{ID: newID(), Type: typ, Name: name, Config: config, Enabled: true}
	if err := m.store.SavePlugin(ctx, rec); err != nil {
		return InstanceView{}, err
	}
	if err := m.Load(ctx); err != nil {
		return InstanceView{}, err
	}
	m.broadcast()
	return m.instanceView(rec.ID), nil
}

// UpdateInstance changes an instance's name, enabled state and/or config.
// A secret config field left empty keeps its stored value.
// control turns control of the devices on or off (Controller providers only).
func (m *Manager) UpdateInstance(ctx context.Context, id string, name *string, enabled, control *bool, config map[string]string) (InstanceView, error) {
	rec, err := m.instanceRec(ctx, id)
	if err != nil {
		return InstanceView{}, err
	}
	p, ok := provider(rec.Type)
	if !ok {
		return InstanceView{}, ErrUnknownType
	}
	if name != nil {
		rec.Name = *name
	}
	if enabled != nil {
		rec.Enabled = *enabled
	}
	if config != nil {
		secret := secretKeys(p.Info().InstanceForm)
		merged := map[string]string{}
		for k, v := range config {
			if secret[k] && v == "" {
				if old, ok := rec.Config[k]; ok {
					merged[k] = old
					continue
				}
			}
			merged[k] = v
		}
		delete(merged, controlKey) // only through the control flag
		if v, ok := rec.Config[controlKey]; ok {
			merged[controlKey] = v
		}
		if err := m.validate(p, merged); err != nil {
			return InstanceView{}, err
		}
		rec.Config = merged
	}
	if control != nil {
		if err := m.setControl(p, &rec, *control); err != nil {
			return InstanceView{}, err
		}
	}
	if err := m.store.SavePlugin(ctx, rec); err != nil {
		return InstanceView{}, err
	}
	if err := m.Load(ctx); err != nil {
		return InstanceView{}, err
	}
	m.broadcast()
	return m.instanceView(rec.ID), nil
}

// DeleteInstance removes an instance and its devices.
func (m *Manager) DeleteInstance(ctx context.Context, id string) error {
	if err := m.store.DeletePlugin(ctx, id); err != nil {
		return err
	}
	if err := m.Load(ctx); err != nil {
		return err
	}
	m.broadcast()
	return nil
}

// AddDevice adds one device to an instance.
func (m *Manager) AddDevice(ctx context.Context, pluginID string, c Candidate, name string) (DeviceView, error) {
	m.addMu.Lock()
	defer m.addMu.Unlock()
	rec, err := m.instanceRec(ctx, pluginID)
	if err != nil {
		return DeviceView{}, err
	}
	existing, err := m.store.PluginDevices(ctx, rec.ID)
	if err != nil {
		return DeviceView{}, err
	}
	for _, d := range existing {
		if sameAddress(d.Address, c.Address) {
			return DeviceView{}, ErrDuplicate
		}
	}
	if name == "" {
		name = c.Name
	}
	if name == "" {
		name = c.Address
	}
	delete(c.Config, controlKey) // only through UpdateDevice's control flag
	dev := store.PluginDevice{
		ID: newID(), PluginID: rec.ID, Name: name, Address: c.Address, Config: c.Config, Enabled: true,
	}
	if err := m.store.SavePluginDevice(ctx, dev); err != nil {
		return DeviceView{}, err
	}
	if err := m.Load(ctx); err != nil {
		return DeviceView{}, err
	}
	m.broadcast()
	return DeviceView{
		ID: dev.ID, PluginID: rec.ID, PluginType: rec.Type, PluginName: rec.Name,
		Name: dev.Name, Address: dev.Address, Enabled: true, CanLinkMeter: canLinkMeter(rec.Type),
		CanRegulate: canRegulate(rec.Type),
	}, nil
}

// UpdateDevice changes a device's name, enabled state and/or whether it may
// be controlled (on by default; the instance's control switch comes first).
func (m *Manager) UpdateDevice(ctx context.Context, id string, name *string, enabled, control *bool) error {
	devices, err := m.store.AllPluginDevices(ctx)
	if err != nil {
		return err
	}
	for _, d := range devices {
		if d.ID != id {
			continue
		}
		if name != nil {
			d.Name = *name
		}
		if enabled != nil {
			d.Enabled = *enabled
		}
		if control != nil {
			if d.Config == nil {
				d.Config = map[string]string{}
			}
			if *control {
				delete(d.Config, controlKey)
			} else {
				d.Config[controlKey] = "false"
			}
		}
		if err := m.store.SavePluginDevice(ctx, d); err != nil {
			return err
		}
		if err := m.Load(ctx); err != nil {
			return err
		}
		m.broadcast()
		return nil
	}
	return store.ErrNotFound
}

// ErrNoMeter: devices of this plugin type cannot have a meter (tado rooms).
var ErrNoMeter = errors.New("plugin: no meter for this type")

// SetMeter links a HomeWizard meter to a plugin device ("" removes it). The
// caller checks that it is a socket or kWh meter.
func (m *Manager) SetMeter(ctx context.Context, id, meter string) error {
	m.mu.Lock()
	typ := ""
	for _, li := range m.live {
		for _, d := range li.devices {
			if d.ID == id {
				typ = li.rec.Type
			}
		}
	}
	m.mu.Unlock()
	if typ == "" {
		return store.ErrNotFound
	}
	if !canLinkMeter(typ) {
		return ErrNoMeter
	}
	if err := m.store.SetPluginMeter(ctx, id, meter); err != nil {
		return err
	}
	if err := m.Load(ctx); err != nil {
		return err
	}
	m.broadcast()
	return nil
}

// DeleteDevice removes one device.
func (m *Manager) DeleteDevice(ctx context.Context, id string) error {
	if err := m.store.DeletePluginDevice(ctx, id); err != nil {
		return err
	}
	if err := m.Load(ctx); err != nil {
		return err
	}
	m.broadcast()
	return nil
}

// Scan asks an instance to find devices on the network.
func (m *Manager) Scan(ctx context.Context, pluginID string) ([]Candidate, error) {
	m.mu.Lock()
	li, ok := m.live[pluginID]
	var inst Instance
	if ok {
		inst = li.inst
	}
	m.mu.Unlock()
	if !ok || inst == nil {
		return nil, store.ErrNotFound
	}
	cands, err := inst.Scan(ctx)
	if err != nil {
		return nil, err
	}
	existing, err := m.store.PluginDevices(ctx, pluginID)
	if err != nil {
		return nil, err
	}
	refreshed := false
	for i := range cands {
		for _, d := range existing {
			if sameAddress(d.Address, cands[i].Address) {
				cands[i].Added = true
				if refreshConfig(&d, cands[i].Config) {
					if err := m.store.SavePluginDevice(ctx, d); err != nil {
						return nil, err
					}
					m.log.Info("plugin-apparaat bijgewerkt na scan", "device", d.Name)
					refreshed = true
				}
				break
			}
		}
	}
	if refreshed {
		if err := m.Load(ctx); err != nil {
			return nil, err
		}
		m.broadcast()
	}
	return cands, nil
}

// refreshConfig takes over what a scan found for a device that is already
// added (a new local key after re-pairing, a newer device model), so the
// user does not have to remove and re-add it and lose its meter, tab and
// history. Keys the scan does not report are kept. It reports a change.
func refreshConfig(d *store.PluginDevice, found map[string]string) bool {
	changed := false
	for k, v := range found {
		if k == controlKey || v == "" || d.Config[k] == v {
			continue
		}
		if d.Config == nil {
			d.Config = map[string]string{}
		}
		d.Config[k] = v
		changed = true
	}
	return changed
}

// sameAddress reports whether two device addresses (an IP, a host name or a
// provider id such as a tado zone) name the same device.
func sameAddress(a, b string) bool {
	return strings.EqualFold(strings.TrimSpace(a), strings.TrimSpace(b))
}

func (m *Manager) instanceView(id string) InstanceView {
	for _, iv := range m.Views() {
		if iv.ID == id {
			return iv
		}
	}
	return InstanceView{ID: id}
}

// HasDevice reports whether a plugin device with this id is configured.
func (m *Manager) HasDevice(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, li := range m.live {
		for _, d := range li.devices {
			if d.ID == id {
				return true
			}
		}
	}
	return false
}

// Series returns one metric of a plugin device in [from, to), averaged into
// at most about points buckets.
func (m *Manager) Series(ctx context.Context, id, metric string, from, to time.Time, points int) ([]store.PluginPoint, time.Duration, error) {
	if !m.HasDevice(id) {
		return nil, 0, store.ErrNotFound
	}
	step := to.Sub(from) / time.Duration(points)
	if step < time.Minute {
		step = time.Minute
	}
	pts, err := m.store.PluginSeries(ctx, id, metric, from, to, step)
	return pts, step, err
}

// PollType polls every instance of a type now, e.g. after a provider got
// new data by itself.
func (m *Manager) PollType(ctx context.Context, typ string) {
	m.mu.Lock()
	var ids []string
	for id, li := range m.live {
		if li.rec.Type == typ {
			ids = append(ids, id)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		m.pollDue(ctx, true, id)
	}
}

// Reload rereads the configuration after a change made outside the plugin
// manager (a removed HomeWizard meter drops its links) and publishes it.
func (m *Manager) Reload(ctx context.Context) {
	if err := m.Load(ctx); err != nil {
		m.log.Error("plugins herladen", "err", err)
		return
	}
	m.broadcast()
}
