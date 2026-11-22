package plugin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/outage"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// baseTick is how often the manager wakes to see which instances are due to
// poll. Each instance polls at its provider's own interval, a multiple of
// this at most.
const baseTick = 5 * time.Second

// clampInterval keeps a provider's suggested poll interval sane.
func clampInterval(seconds int) time.Duration {
	d := time.Duration(seconds) * time.Second
	if d < baseTick {
		return baseTick
	}
	if d > time.Hour {
		return time.Hour
	}
	return d
}

// ErrUnknownType is returned when a plugin type is not registered.
var ErrUnknownType = errors.New("plugin: unknown type")

// ErrDuplicate is returned when an instance already has a device with the
// same address.
var ErrDuplicate = errors.New("plugin: device already added")

// DeviceView is the API/stream shape of one plugin device and its last
// reading.
type DeviceView struct {
	ID         string     `json:"id"`
	PluginID   string     `json:"plugin_id"`
	PluginType string     `json:"plugin_type"`
	PluginName string     `json:"plugin_name"`
	Name       string     `json:"name"`
	Address    string     `json:"address"`
	Enabled    bool       `json:"enabled"`
	LastSeen   *time.Time `json:"last_seen,omitempty"`
	Reading    *Reading   `json:"reading,omitempty"`
	// Meter is the HomeWizard device (socket or kWh meter) that measures
	// this device, whose power and usage the PWA shows with it; one meter
	// may measure several devices. CanLinkMeter: the type allows one.
	Meter        string `json:"meter,omitempty"`
	CanLinkMeter bool   `json:"can_link_meter,omitempty"`
	// CanRegulate: the type allows a regulate action (ProviderInfo).
	CanRegulate bool `json:"can_regulate,omitempty"`
	// Controls are what can be set on this device; only present when the
	// provider can control devices and the user allowed it for the instance
	// and did not turn it off for this device.
	Controls []Control `json:"controls,omitempty"`
	// CanControl: control is allowed for the instance, so the device has a
	// switch of its own; Control is that switch (on unless turned off).
	CanControl bool `json:"can_control,omitempty"`
	Control    bool `json:"control,omitempty"`
	// TabID is the user's own tab the device is shown on; 0 = Klimaat.
	TabID int64 `json:"tab_id,omitempty"`
}

// InstanceView is the API shape of one configured plugin and its devices.
// The stored config is intentionally not returned (it may hold secrets).
type InstanceView struct {
	ID      string       `json:"id"`
	Type    string       `json:"type"`
	Name    string       `json:"name"`
	Enabled bool         `json:"enabled"`
	Devices []DeviceView `json:"devices"`
	// Auth is set for instances of a provider that needs authorisation, so the
	// PWA can show the connect state. Nil for providers without auth.
	Auth *AuthState `json:"auth,omitempty"`
	// CanControl: the provider can change devices (implements Controller).
	// Control: the user allowed that for this instance (off by default).
	CanControl bool `json:"can_control"`
	Control    bool `json:"control"`
}

// liveInstance is a configured instance with its built provider instance and
// cached devices and readings.
type liveInstance struct {
	rec      store.PluginInstance
	inst     Instance
	interval time.Duration
	devices  []store.PluginDevice
	lastPoll time.Time
	gen      uint64 // changes on every Load, so a poll can tell it is stale
}

// Manager loads configured plugins from the store, polls their devices and
// fans the readings out over the gateway hub as "plugin" events.
type Manager struct {
	store   *store.Store
	hub     *gateway.Hub
	log     *slog.Logger
	dataDir string // per-instance tokens etc. live here

	mu     sync.Mutex
	live   map[string]*liveInstance // by plugin id
	latest map[string]DeviceView    // by device id
	gen    uint64                   // last generation handed out by Load
	meters map[string]string        // plugin device id -> linked HomeWizard meter
	fails  map[string]int           // device id -> consecutive failed polls (settle)
	online map[string]bool          // device id -> last logged online state (settle)
	// failedAt: device id -> first failed poll of the current run (settle)
	failedAt map[string]time.Time

	// Outages logs when devices did not respond (optional); set before Run.
	Outages *outage.Log
	// Notices gets a plugin that could not be loaded (optional).
	Notices *notice.Log

	// Retention is how long plugin samples (numeric metrics over time) are
	// kept; zero keeps them forever. Set before Run.
	Retention time.Duration

	addMu sync.Mutex // serialises AddDevice, so two requests cannot add the same device
}

// NewManager creates a plugin manager. Call Load before Run. dataDir is where
// providers keep per-instance state such as OAuth tokens.
func NewManager(st *store.Store, hub *gateway.Hub, log *slog.Logger, dataDir string) *Manager {
	return &Manager{
		store:    st,
		hub:      hub,
		log:      log,
		dataDir:  dataDir,
		live:     map[string]*liveInstance{},
		latest:   map[string]DeviceView{},
		meters:   map[string]string{},
		fails:    map[string]int{},
		online:   map[string]bool{},
		failedAt: map[string]time.Time{},
	}
}

func newID() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// Load (re)builds the in-memory instances from the store, preserving any
// readings already held for devices that are still polled (existing and
// enabled, in an enabled instance). Safe to call again after a config change.
func (m *Manager) Load(ctx context.Context) error {
	instances, err := m.store.Plugins(ctx)
	if err != nil {
		return err
	}
	devices, err := m.store.AllPluginDevices(ctx)
	if err != nil {
		return err
	}
	meters, err := m.store.PluginMeters(ctx)
	if err != nil {
		return err
	}
	byPlugin := map[string][]store.PluginDevice{}
	for _, d := range devices {
		byPlugin[d.PluginID] = append(byPlugin[d.PluginID], d)
	}

	m.mu.Lock()
	old := m.live
	m.meters = meters
	m.live = map[string]*liveInstance{}
	m.gen++
	for _, rec := range instances {
		p, ok := provider(rec.Type)
		if !ok {
			m.log.Warn("plugin met onbekend type overgeslagen", "id", rec.ID, "type", rec.Type)
			continue
		}
		inst, err := p.Instance(rec.Config, Env{DataDir: m.dataDir, InstanceID: rec.ID})
		if err != nil {
			m.log.Warn("plugin niet geladen", "id", rec.ID, "type", rec.Type, "err", err)
			m.Notices.Add(notice.KindPlugin, rec.ID, "Plugin niet geladen", rec.Name+": "+err.Error())
			continue
		}
		if prev, ok := old[rec.ID]; ok && prev.inst != nil {
			prev.inst.Close()
		}
		m.live[rec.ID] = &liveInstance{
			rec:      rec,
			inst:     inst,
			interval: clampInterval(p.Info().IntervalSeconds),
			devices:  byPlugin[rec.ID],
			gen:      m.gen,
		}
	}
	// close instances that disappeared, and drop readings of devices that
	// are gone or no longer polled (a frozen reading would look current)
	for id, prev := range old {
		if _, kept := m.live[id]; !kept && prev.inst != nil {
			prev.inst.Close()
		}
	}
	keep := map[string]bool{}
	for _, li := range m.live {
		if !li.rec.Enabled {
			continue
		}
		for _, d := range li.devices {
			if d.Enabled {
				keep[d.ID] = true
			}
		}
	}
	var stopped []string // no longer polled: an open outage ends
	for id := range m.latest {
		if !keep[id] {
			delete(m.latest, id)
			stopped = append(stopped, id)
		}
	}
	for id := range m.fails {
		if !keep[id] {
			delete(m.fails, id)
		}
	}
	for id := range m.online {
		if !keep[id] {
			delete(m.online, id)
		}
	}
	for id := range m.failedAt {
		if !keep[id] {
			delete(m.failedAt, id)
		}
	}
	m.mu.Unlock()
	now := time.Now()
	for _, id := range stopped {
		m.Outages.End(ctx, id, now)
	}
	return nil
}

// broadcast publishes the full current set of plugin device views, so live
// subscribers pick up added, renamed or removed devices at once (a CRUD
// change does not otherwise produce a per-device poll event).
func (m *Manager) broadcast() {
	m.hub.Publish(gateway.Event{Type: "plugins", Data: m.DeviceViews()})
}

// Run polls due instances until ctx is cancelled.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(baseTick)
	defer t.Stop()
	m.prune(ctx)
	lastPrune := time.Now()
	m.pollDue(ctx, true, "")
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return
		case <-t.C:
			m.pollDue(ctx, false, "")
			if time.Since(lastPrune) > 24*time.Hour {
				m.prune(ctx)
				lastPrune = time.Now()
			}
		}
	}
}

// prune drops plugin samples older than the retention.
func (m *Manager) prune(ctx context.Context) {
	if m.Retention <= 0 {
		return
	}
	if _, err := m.store.PrunePluginSamples(ctx, time.Now().Add(-m.Retention)); err != nil {
		m.log.Error("prune plugin samples", "err", err)
	}
}

// record stores the numeric (and boolean, as 0/1) metrics of a reading, for
// the history of a plugin device.
func (m *Manager) record(ctx context.Context, deviceID string, t time.Time, r Reading) {
	if !r.Online {
		return
	}
	var samples []store.PluginSample
	for _, mt := range r.Metrics {
		if v, ok := Number(mt.Value); ok {
			samples = append(samples, store.PluginSample{Metric: mt.Key, Value: v})
		}
	}
	if err := m.store.AddPluginSamples(ctx, deviceID, t, samples); err != nil && ctx.Err() == nil {
		m.log.Warn("plugin samples", "device", deviceID, "err", err)
	}
}

// Number converts a metric value to a number: numbers as they are, booleans
// as 0 or 1. Text is not a number.
func Number(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int:
		return float64(x), true
	case int8:
		return float64(x), true
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint:
		return float64(x), true
	case uint8:
		return float64(x), true
	case uint16:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func (m *Manager) closeAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, li := range m.live {
		if li.inst != nil {
			li.inst.Close()
		}
	}
}

// pollTarget is a snapshot of what to poll, taken under the lock so the
// network calls themselves run without it held.
type pollTarget struct {
	inst Instance
	gen  uint64
	dev  store.PluginDevice
	view DeviceView
}

// pollDue polls every instance whose interval has passed, or all of them
// with force; only, when set, limits it to one instance.
func (m *Manager) pollDue(ctx context.Context, force bool, only string) {
	now := time.Now()
	var targets []pollTarget
	var batches []batchTarget
	m.mu.Lock()
	for _, li := range m.live {
		if !li.rec.Enabled || (only != "" && li.rec.ID != only) {
			continue
		}
		if !force && now.Sub(li.lastPoll) < li.interval {
			continue
		}
		li.lastPoll = now
		var devs []store.PluginDevice
		for _, d := range li.devices {
			if d.Enabled {
				devs = append(devs, d)
			}
		}
		if len(devs) == 0 {
			continue
		}
		// A rate-limited cloud provider reads all its devices in one call.
		if bp, ok := li.inst.(BatchPoller); ok {
			batches = append(batches, batchTarget{inst: bp, gen: li.gen, rec: li.rec, devices: devs})
			continue
		}
		for _, d := range devs {
			targets = append(targets, pollTarget{
				inst: li.inst,
				gen:  li.gen,
				dev:  d,
				view: DeviceView{
					ID: d.ID, PluginID: li.rec.ID, PluginType: li.rec.Type,
					PluginName: li.rec.Name, Name: d.Name, Address: d.Address, Enabled: true,
				},
			})
		}
	}
	m.mu.Unlock()

	var wg sync.WaitGroup
	for _, t := range targets {
		wg.Add(1)
		go func(t pollTarget) {
			defer wg.Done()
			m.pollOne(ctx, t)
		}(t)
	}
	for _, b := range batches {
		wg.Add(1)
		go func(b batchTarget) {
			defer wg.Done()
			m.pollBatch(ctx, b)
		}(b)
	}
	wg.Wait()
}

// batchTarget is one instance whose devices are read together (BatchPoller).
type batchTarget struct {
	inst    BatchPoller
	gen     uint64
	rec     store.PluginInstance
	devices []store.PluginDevice
}

func (m *Manager) pollBatch(ctx context.Context, b batchTarget) {
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	devs := make([]Device, len(b.devices))
	for i, d := range b.devices {
		devs[i] = Device{ID: d.ID, Name: d.Name, Address: d.Address, Config: d.Config}
	}
	results, err := b.inst.PollAll(pctx, devs)
	if ctx.Err() != nil {
		return
	}
	seen := time.Now()
	for _, d := range b.devices {
		var r Reading
		switch {
		case err != nil:
			r = Reading{Online: false, Err: err.Error()}
		case results[d.ID].Online || len(results[d.ID].Metrics) > 0 || results[d.ID].Err != "":
			r = results[d.ID]
		default:
			r = Reading{Online: false, Err: "geen meting"}
		}
		view := DeviceView{
			ID: d.ID, PluginID: b.rec.ID, PluginType: b.rec.Type, PluginName: b.rec.Name,
			Name: d.Name, Address: d.Address, Enabled: true,
		}
		m.mu.Lock()
		li, ok := m.live[b.rec.ID]
		if !ok || li.gen != b.gen {
			m.mu.Unlock()
			continue
		}
		m.decorate(li, &view, d)
		prev, hadPrev := m.latest[d.ID]
		fresh := m.settle(&view, r, seen, prev, hadPrev)
		m.latest[d.ID] = view
		m.mu.Unlock()
		if fresh {
			m.record(ctx, d.ID, seen, r)
			m.noteOutage(ctx, d.ID, r, seen)
		}
		m.hub.Publish(gateway.Event{Type: "plugin", Data: view})
	}
}

// canLinkMeter reports whether devices of a plugin type can have a meter.
func canLinkMeter(typ string) bool {
	p, ok := provider(typ)
	return ok && p.Info().CanLinkMeter
}

// canRegulate reports whether devices of a plugin type may be regulated.
func canRegulate(typ string) bool {
	p, ok := provider(typ)
	return ok && p.Info().CanRegulate
}

func (m *Manager) pollOne(ctx context.Context, t pollTarget) {
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	r, err := t.inst.Poll(pctx, Device{ID: t.dev.ID, Name: t.dev.Name, Address: t.dev.Address, Config: t.dev.Config})
	if ctx.Err() != nil {
		return
	}
	view := t.view
	if err != nil {
		r = Reading{Online: false, Err: err.Error()}
	}
	seen := time.Now()

	m.mu.Lock()
	// Drop the result when the config changed during the poll: the device
	// may be removed, disabled or renamed, and publishing it would bring the
	// old state back in the PWA. Load resets lastPoll, so the next tick polls
	// again with the current config.
	li, ok := m.live[t.dev.PluginID]
	if !ok || li.gen != t.gen {
		m.mu.Unlock()
		return
	}
	m.decorate(li, &view, t.dev)
	prev, hadPrev := m.latest[t.dev.ID]
	fresh := m.settle(&view, r, seen, prev, hadPrev)
	m.latest[t.dev.ID] = view
	m.mu.Unlock()
	if fresh {
		m.record(ctx, t.dev.ID, seen, r)
		m.noteOutage(ctx, t.dev.ID, r, seen)
	}
	m.hub.Publish(gateway.Event{Type: "plugin", Data: view})
}

// Views returns every configured instance with its devices and last readings,
// for the API and the stream snapshot.
func (m *Manager) Views() []InstanceView {
	m.mu.Lock()
	out := make([]InstanceView, 0, len(m.live))
	type authRef struct {
		idx  int
		inst Authenticator
	}
	var auths []authRef
	for _, li := range m.live {
		_, canControl := li.inst.(Controller)
		iv := InstanceView{ID: li.rec.ID, Type: li.rec.Type, Name: li.rec.Name, Enabled: li.rec.Enabled,
			CanControl: canControl, Control: canControl && controlAllowed(li.rec)}
		for _, d := range li.devices {
			// Always use the current device fields (name/address/enabled);
			// only the reading and last-seen come from the cached poll, so a
			// rename or toggle shows at once instead of after the next poll.
			v := DeviceView{
				ID: d.ID, PluginID: li.rec.ID, PluginType: li.rec.Type,
				PluginName: li.rec.Name, Name: d.Name, Address: d.Address, Enabled: d.Enabled,
			}
			m.decorate(li, &v, d)
			if last, ok := m.latest[d.ID]; ok {
				v.LastSeen, v.Reading = last.LastSeen, last.Reading
			}
			iv.Devices = append(iv.Devices, v)
		}
		if a, ok := li.inst.(Authenticator); ok {
			auths = append(auths, authRef{idx: len(out), inst: a})
		}
		out = append(out, iv)
	}
	m.mu.Unlock()
	// Query auth status outside the lock: it reads the instance's own state.
	for _, a := range auths {
		st := a.inst.AuthStatus(context.Background())
		out[a.idx].Auth = &st
	}
	return out
}

// ErrNoAuth is returned when auth is requested for a provider that does not
// support it.
var ErrNoAuth = errors.New("plugin: provider has no authorisation step")

func (m *Manager) authenticator(id string) (Authenticator, error) {
	m.mu.Lock()
	li, ok := m.live[id]
	var inst Instance
	if ok {
		inst = li.inst
	}
	m.mu.Unlock()
	if !ok || inst == nil {
		return nil, store.ErrNotFound
	}
	a, ok := inst.(Authenticator)
	if !ok {
		return nil, ErrNoAuth
	}
	return a, nil
}

// StartAuth begins the authorisation flow of an instance and returns what the
// user must do.
func (m *Manager) StartAuth(ctx context.Context, id string) (AuthChallenge, error) {
	a, err := m.authenticator(id)
	if err != nil {
		return AuthChallenge{}, err
	}
	return a.StartAuth(ctx)
}

// AuthStatus reports the authorisation status of an instance.
func (m *Manager) AuthStatus(ctx context.Context, id string) (AuthState, error) {
	a, err := m.authenticator(id)
	if err != nil {
		return AuthState{}, err
	}
	return a.AuthStatus(ctx), nil
}

// DeviceViews returns a flat list of every plugin device view, for the stream
// snapshot's "plugins" field.
func (m *Manager) DeviceViews() []DeviceView {
	var out []DeviceView
	for _, iv := range m.Views() {
		out = append(out, iv.Devices...)
	}
	return out
}
