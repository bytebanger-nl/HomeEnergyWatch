package plugin

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// controlKey is the instance config key that records whether the user
// allowed control of the instance's devices. It is managed by the framework
// (UpdateInstance's control flag), never by a provider's form.
const controlKey = "_control"

var (
	// ErrNoControl: the provider cannot change devices, or the device has
	// no such control.
	ErrNoControl = errors.New("plugin: no such control")
	// ErrControlOff: control is not allowed for this instance (the default).
	ErrControlOff = errors.New("plugin: control not allowed")
	// ErrDisabled: the instance or the device is turned off.
	ErrDisabled = errors.New("plugin of apparaat staat uit")
	// ErrValue: the value does not fit the control (CheckValue).
	ErrValue = errors.New("plugin: invalid value")
)

// valueError is a CheckValue error: its own text, matching ErrValue.
type valueError struct{ msg string }

func (e valueError) Error() string   { return e.msg }
func (e valueError) Is(t error) bool { return t == ErrValue }

func controlAllowed(rec store.PluginInstance) bool { return rec.Config[controlKey] == "true" }

// deviceControlAllowed: control is on for one device unless the user turned
// it off there (controlKey "false" in the device config). The instance
// switch still comes first.
func deviceControlAllowed(d store.PluginDevice) bool { return d.Config[controlKey] != "false" }

// setControl turns control on or off for an instance record.
func (m *Manager) setControl(p Provider, rec *store.PluginInstance, on bool) error {
	if rec.Config == nil {
		rec.Config = map[string]string{}
	}
	if !on {
		delete(rec.Config, controlKey)
		return nil
	}
	inst, err := p.Instance(rec.Config, Env{DataDir: m.dataDir, InstanceID: rec.ID})
	if err != nil {
		return err
	}
	_, ok := inst.(Controller)
	inst.Close()
	if !ok {
		return ErrNoControl
	}
	rec.Config[controlKey] = "true"
	return nil
}

// decorate fills in the parts of a device view that come from the current
// configuration rather than the poll: linked meters and controls. Call with
// m.mu held.
func (m *Manager) decorate(li *liveInstance, v *DeviceView, d store.PluginDevice) {
	v.CanLinkMeter = canLinkMeter(li.rec.Type)
	v.CanRegulate = canRegulate(li.rec.Type)
	if v.CanLinkMeter {
		v.Meter = m.meters[d.ID]
	}
	v.TabID = d.TabID
	v.Controls = nil
	v.CanControl, v.Control = false, false
	if c, ok := li.inst.(Controller); ok && controlAllowed(li.rec) {
		v.CanControl = true
		v.Control = deviceControlAllowed(d)
		if v.Control {
			v.Controls = c.Controls(Device{ID: d.ID, Name: d.Name, Address: d.Address, Config: d.Config})
		}
	}
}

// target is a control that may be used, with the value it gets.
type target struct {
	c   Controller
	rec store.PluginInstance
	dev store.PluginDevice
	pd  Device
	v   any
}

// resolve finds a plugin device's control and checks that it may be used
// with value.
func (m *Manager) resolve(deviceID, key string, value any) (target, error) {
	m.mu.Lock()
	var (
		li  *liveInstance
		dev store.PluginDevice
	)
	for _, l := range m.live {
		for _, d := range l.devices {
			if d.ID == deviceID {
				li, dev = l, d
			}
		}
	}
	if li == nil {
		m.mu.Unlock()
		return target{}, store.ErrNotFound
	}
	rec, inst := li.rec, li.inst
	m.mu.Unlock()

	c, ok := inst.(Controller)
	if !ok {
		return target{}, ErrNoControl
	}
	if !controlAllowed(rec) || !deviceControlAllowed(dev) {
		return target{}, ErrControlOff
	}
	if !rec.Enabled || !dev.Enabled {
		return target{}, ErrDisabled
	}
	pd := Device{ID: dev.ID, Name: dev.Name, Address: dev.Address, Config: dev.Config}
	var ctl *Control
	for _, x := range c.Controls(pd) {
		if x.Key == key {
			x := x
			ctl = &x
		}
	}
	if ctl == nil {
		return target{}, ErrNoControl
	}
	v, err := CheckValue(*ctl, value)
	if err != nil {
		return target{}, err
	}
	return target{c: c, rec: rec, dev: dev, pd: pd, v: v}, nil
}

// CheckSet checks what Set checks, without sending anything.
func (m *Manager) CheckSet(deviceID, key string, value any) error {
	_, err := m.resolve(deviceID, key, value)
	return err
}

// Set changes one control of a plugin device: value is checked against the
// control (range, step, options) before the provider sees it. After a
// change the instance is polled at once, so the new state shows. It sends
// once; package control checks that it arrived and tries again.
func (m *Manager) Set(ctx context.Context, deviceID, key string, value any) error {
	t, err := m.resolve(deviceID, key, value)
	if err != nil {
		return err
	}
	sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := t.c.Set(sctx, t.pd, key, t.v); err != nil {
		m.log.Warn("plugin bediening mislukt", "device", t.dev.Name, "control", key, "value", t.v, "err", err)
		// poll anyway: when the device took the command but its reply did
		// not come, the real state shows sooner
		go m.pollDue(context.WithoutCancel(ctx), true, t.rec.ID)
		return err
	}
	m.log.Info("plugin bediend", "plugin", t.rec.Name, "device", t.dev.Name, "control", key, "value", t.v)
	go m.pollDue(context.WithoutCancel(ctx), true, t.rec.ID)
	return nil
}

// CheckValue converts and checks a value (as decoded from JSON) for a
// control: a bool for a switch, a number within range rounded to the step
// for a setpoint, one of the options for a select, nothing for an action.
func CheckValue(c Control, value any) (any, error) {
	switch c.Kind {
	case ControlSwitch:
		b, ok := value.(bool)
		if !ok {
			return nil, valueErr("%s: aan of uit verwacht", c.Label)
		}
		return b, nil
	case ControlSetpoint:
		f, ok := Number(value)
		if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, valueErr("%s: getal verwacht", c.Label)
		}
		if c.Step > 0 {
			f = math.Round(f/c.Step) * c.Step
		}
		if f < c.Min || f > c.Max {
			return nil, valueErr("%s: tussen %g en %g", c.Label, c.Min, c.Max)
		}
		return f, nil
	case ControlSelect:
		s, ok := value.(string)
		if ok {
			for _, o := range c.Options {
				if o.Value == s {
					return s, nil
				}
			}
		}
		return nil, valueErr("%s: onbekende keuze", c.Label)
	case ControlAction:
		return nil, nil
	}
	return nil, ErrNoControl
}

func valueErr(format string, a ...any) error { return valueError{fmt.Sprintf(format, a...)} }
