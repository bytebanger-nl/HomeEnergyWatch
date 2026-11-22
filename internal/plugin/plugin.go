// Package plugin is a generic framework for read-only integrations that run
// alongside the HomeWizard energy core. A provider type (e.g. MHI air
// conditioners) is compiled in and registers itself; it declares a config
// form the PWA renders, optionally scans the network for devices, and polls
// each configured device for a set of generic metrics shown as a tile on the
// Klimaat screen; providers that implement Controller can also change their
// devices once the user allows it. The energy core (devices, history, costs)
// is untouched: this is a parallel subsystem for everything that is not a
// HomeWizard meter (and for what a meter controls besides measuring, such
// as the batteries behind a P1 meter, package hwbattery).
package plugin

import (
	"context"
	"sort"
	"sync"
)

// FieldType is the input type of a config field, so the PWA can render the
// right control.
type FieldType string

const (
	FieldText     FieldType = "text"
	FieldPassword FieldType = "password"
	FieldNumber   FieldType = "number"
	FieldBool     FieldType = "bool"
)

// Field describes one config field of an instance or a device. The PWA builds
// the plugin's config screen from a list of these.
type Field struct {
	Key      string    `json:"key"`
	Label    string    `json:"label"`
	Type     FieldType `json:"type"`
	Required bool      `json:"required,omitempty"`
	Help     string    `json:"help,omitempty"`
	Default  string    `json:"default,omitempty"`
	// Secret values (tokens, passwords) are stored but never returned by the
	// API once set, like the device tokens of the energy core.
	Secret bool `json:"secret,omitempty"`
}

// ProviderInfo is the static description of a provider type, returned by
// GET /api/plugins so the PWA can offer it and render its forms.
type ProviderInfo struct {
	Type         string  `json:"type"`
	Name         string  `json:"name"`
	Description  string  `json:"description,omitempty"`
	InstanceForm []Field `json:"instance_form"`
	DeviceForm   []Field `json:"device_form"`
	CanScan      bool    `json:"can_scan"`
	CanAddManual bool    `json:"can_add_manual"`
	// NeedsAuth marks a provider whose instance must be authorised once before
	// it can scan or poll (e.g. a cloud account via OAuth). The PWA then shows
	// a "connect" step driven by the Authenticator interface.
	NeedsAuth bool `json:"needs_auth,omitempty"`
	// CanLinkMeter: a device of this type can be measured by a HomeWizard
	// socket or kWh meter (an air conditioner, an electric radiator), so the
	// PWA offers to link one. Not for devices without their own electricity
	// use, such as tado rooms.
	CanLinkMeter bool `json:"can_link_meter,omitempty"`
	// CanRegulate: a device of this type heats (or cools) by itself, so an
	// automation may step its setpoint to keep another measured value at a
	// target (rules.ActionRegulate). Not for thermostats such as tado,
	// which regulate the central heating themselves.
	CanRegulate bool `json:"can_regulate,omitempty"`
	// IntervalSeconds is the suggested poll cadence for this provider. The
	// manager clamps it to a sane range. Cloud providers (rate limits) use a
	// large value; local ones a small one.
	IntervalSeconds int `json:"interval_seconds"`
}

// Env is handed to a provider when it builds an instance: a per-instance data
// directory for tokens and the like, and the instance's own id.
type Env struct {
	DataDir    string
	InstanceID string
}

// AuthChallenge is what the user must do to authorise an instance (OAuth
// device-code flow): open the URL and confirm the code.
type AuthChallenge struct {
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete,omitempty"`
	UserCode                string `json:"user_code"`
	ExpiresInSeconds        int    `json:"expires_in,omitempty"`
	Message                 string `json:"message,omitempty"`
}

// AuthState is the current authorisation status of an instance.
type AuthState struct {
	Connected bool   `json:"connected"`
	Pending   bool   `json:"pending"`
	Message   string `json:"message,omitempty"`
}

// Candidate is a device found by Scan or entered manually, ready to be saved.
type Candidate struct {
	Address string            `json:"address"` // stable identifier, e.g. an IP
	Name    string            `json:"name,omitempty"`
	Config  map[string]string `json:"config,omitempty"`
	Added   bool              `json:"added,omitempty"` // set by Manager.Scan: the instance already has this device
}

// MetricKind hints the PWA how to render a metric value.
type MetricKind string

const (
	MetricNumber MetricKind = "number"
	MetricText   MetricKind = "text"
	MetricBool   MetricKind = "bool"
)

// Metric is one labelled value in a reading.
type Metric struct {
	Key   string     `json:"key"`
	Label string     `json:"label"`
	Kind  MetricKind `json:"kind"`
	Value any        `json:"value"`
	Unit  string     `json:"unit,omitempty"`
}

// Reading is the generic state of one device at a moment in time.
type Reading struct {
	Online  bool     `json:"online"`
	Metrics []Metric `json:"metrics,omitempty"`
	Err     string   `json:"error,omitempty"`
}

// Device is a configured device of an instance, passed to Poll.
type Device struct {
	ID      string
	Name    string
	Address string
	Config  map[string]string
}

// Provider is a compiled-in integration type.
type Provider interface {
	// Info describes the provider and its config forms.
	Info() ProviderInfo
	// Instance builds a configured instance from stored instance config and
	// the per-instance environment (data dir, instance id).
	Instance(config map[string]string, env Env) (Instance, error)
}

// Instance is a configured plugin that scans for and polls devices. All
// methods must be read-only with respect to the physical device.
type Instance interface {
	// Scan finds devices on the network. Providers without CanScan return
	// (nil, nil) or an error.
	Scan(ctx context.Context) ([]Candidate, error)
	// Poll reads one device, read-only.
	Poll(ctx context.Context, dev Device) (Reading, error)
	// Close releases any resources held by the instance.
	Close()
}

// Authenticator is implemented by instances that need a one-time
// authorisation (e.g. an OAuth device-code flow). StartAuth begins the flow
// and returns what the user must do; the instance completes it in the
// background and persists its token. AuthStatus reports progress.
type Authenticator interface {
	StartAuth(ctx context.Context) (AuthChallenge, error)
	AuthStatus(ctx context.Context) AuthState
}

// BatchPoller is implemented by instances that read all their devices in one
// call (e.g. a cloud API with a rate limit). When present, the manager calls
// PollAll once per cycle instead of Poll per device; the result is keyed by
// device id, and a device missing from the map is treated as offline.
type BatchPoller interface {
	PollAll(ctx context.Context, devices []Device) (map[string]Reading, error)
}

// ControlKind is how the PWA renders a control.
type ControlKind string

const (
	// ControlSwitch takes a bool (on/off).
	ControlSwitch ControlKind = "switch"
	// ControlSetpoint takes a number between Min and Max in steps of Step.
	ControlSetpoint ControlKind = "setpoint"
	// ControlSelect takes one of the Options' values.
	ControlSelect ControlKind = "select"
	// ControlAction is a button without a value (e.g. "back to schedule").
	ControlAction ControlKind = "action"
)

// Option is one choice of a select control.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Control is one thing a device lets you set. The PWA builds its buttons
// from these, like the config forms: no provider-specific frontend code.
type Control struct {
	Key   string      `json:"key"`
	Label string      `json:"label"`
	Kind  ControlKind `json:"kind"`
	// Metric is the key of the metric that holds the current value, if any.
	Metric  string   `json:"metric,omitempty"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Step    float64  `json:"step,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Options []Option `json:"options,omitempty"`
	// Confirm, when set, is a question the PWA asks before an action.
	Confirm string `json:"confirm,omitempty"`
	// Hidden controls still work (Manager.Set, rules saved earlier) but
	// the PWA offers them nowhere: a control replaced by a clearer one.
	Hidden bool `json:"hidden,omitempty"`
}

// Controller is implemented by instances that can change their devices.
// It is optional: the read-only contract of Instance still holds for every
// other provider, and the manager only calls Set when the user allowed
// control for the instance. Set receives a value already checked against
// the control: bool for a switch, float64 for a setpoint, string for a
// select, nil for an action.
type Controller interface {
	Controls(dev Device) []Control
	Set(ctx context.Context, dev Device, key string, value any) error
}

var (
	registryMu sync.RWMutex
	registry   = map[string]Provider{}
)

// Register adds a provider type to the global registry. Provider packages
// call it from an init function; importing the package is enough to enable
// the type. Registering a type twice panics (a programming error).
func Register(p Provider) {
	registryMu.Lock()
	defer registryMu.Unlock()
	typ := p.Info().Type
	if _, dup := registry[typ]; dup {
		panic("plugin: duplicate provider type " + typ)
	}
	registry[typ] = p
}

// Providers returns every registered provider's info, sorted by name.
func Providers() []ProviderInfo {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]ProviderInfo, 0, len(registry))
	for _, p := range registry {
		out = append(out, p.Info())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// provider returns a registered provider by type.
func provider(typ string) (Provider, bool) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	p, ok := registry[typ]
	return p, ok
}
