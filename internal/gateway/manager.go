// Package gateway manages the configured HomeWizard devices: it polls them,
// records history and publishes live updates.
package gateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/history"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/outage"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Device statuses.
const (
	StatusConnecting   = "connecting"
	StatusOnline       = "online"
	StatusOffline      = "offline"
	StatusDisabled     = "disabled"
	StatusNeedsPairing = "needs_pairing"
	StatusPairing      = "pairing"
	StatusAPIDisabled  = "api_disabled"
)

// DeviceView is what the PWA sees of a device. It never contains tokens.
type DeviceView struct {
	ID          string              `json:"id"`
	Name        string              `json:"name"`
	Kind        homewizard.Kind     `json:"kind"`
	Model       string              `json:"model"`
	ProductType string              `json:"product_type"`
	ProductName string              `json:"product_name"`
	Serial      string              `json:"serial"`
	Firmware    string              `json:"firmware"`
	Host        string              `json:"host"`
	APIVersion  int                 `json:"api_version"`
	V2Available bool                `json:"v2_available"`
	Paired      bool                `json:"paired"`
	Enabled     bool                `json:"enabled"`
	Source      string              `json:"source"`
	Status      string              `json:"status"`
	Message     string              `json:"message,omitempty"`
	LastSeen    *time.Time          `json:"last_seen,omitempty"`
	Reading     *homewizard.Reading `json:"reading,omitempty"`
	Today       map[string]float64  `json:"today,omitempty"`
	CanSwitch   bool                `json:"can_switch"`
	CanPair     bool                `json:"can_pair"`
	// ShowCounters: the PWA shows the meter readings on the device's card.
	ShowCounters bool `json:"show_counters"`
}

// Config configures the manager.
type Config struct {
	// Interval between two polls of a device.
	Interval time.Duration
	// ClientName is used as the v2 user name (local/<ClientName>).
	ClientName string
	// Client options (timeouts, CA pool).
	Client homewizard.Options
}

// Manager owns one worker per device.
type Manager struct {
	store    *store.Store
	recorder *history.Recorder
	hub      *Hub
	log      *slog.Logger
	cfg      Config

	// Outages logs when devices did not respond (optional); set before Start.
	Outages *outage.Log

	ctx     context.Context
	mu      sync.Mutex
	workers map[string]*worker
	// saveMu serializes read-modify-write of a device row (update, remove,
	// discovery, pairing), so none of them writes back a stale copy.
	saveMu sync.Mutex
}

// NewManager creates a manager. Call Start to begin polling.
func NewManager(st *store.Store, rec *history.Recorder, hub *Hub, log *slog.Logger, cfg Config) *Manager {
	if cfg.Interval <= 0 {
		cfg.Interval = 2 * time.Second
	}
	if cfg.ClientName == "" {
		cfg.ClientName = "homeenergywatch"
	}
	return &Manager{store: st, recorder: rec, hub: hub, log: log, cfg: cfg, workers: map[string]*worker{}}
}

// Interval returns the poll interval.
func (m *Manager) Interval() time.Duration { return m.cfg.Interval }

// Start loads all devices and starts polling them until ctx is done.
func (m *Manager) Start(ctx context.Context) error {
	m.ctx = ctx
	devices, err := m.store.Devices(ctx)
	if err != nil {
		return err
	}
	for _, d := range devices {
		m.startWorker(d)
	}
	return nil
}

// IDs returns all device ids.
func (m *Manager) IDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.workers))
	for id := range m.workers {
		ids = append(ids, id)
	}
	return ids
}

// Views returns all devices: P1 meters first, then by name.
func (m *Manager) Views() []DeviceView {
	m.mu.Lock()
	views := make([]DeviceView, 0, len(m.workers))
	for _, w := range m.workers {
		views = append(views, w.snapshot())
	}
	m.mu.Unlock()
	order := map[homewizard.Kind]int{homewizard.KindP1: 0, homewizard.KindBattery: 1, homewizard.KindKWH: 2, homewizard.KindSocket: 3, homewizard.KindWater: 4}
	sort.SliceStable(views, func(i, j int) bool {
		oi, oj := order[views[i].Kind], order[views[j].Kind]
		if oi != oj {
			return oi < oj
		}
		return strings.ToLower(views[i].Name) < strings.ToLower(views[j].Name)
	})
	return views
}

// View returns one device.
func (m *Manager) View(id string) (DeviceView, bool) {
	w := m.worker(id)
	if w == nil {
		return DeviceView{}, false
	}
	return w.snapshot(), true
}

func (m *Manager) worker(id string) *worker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.workers[id]
}

func (m *Manager) findBySerial(serial string) *worker {
	if serial == "" {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		if strings.EqualFold(w.device().Serial, serial) {
			return w
		}
	}
	return nil
}

func (m *Manager) startWorker(d store.Device) {
	ctx, cancel := context.WithCancel(m.ctx)
	w := &worker{m: m, cancel: cancel, wake: make(chan struct{}, 1)}
	w.setDevice(d)
	m.mu.Lock()
	if old := m.workers[d.ID]; old != nil {
		old.cancel()
		w.takePairing(old)
	}
	m.workers[d.ID] = w
	m.mu.Unlock()
	go w.run(ctx)
}

// ErrNotFound is returned for unknown device ids.
var ErrNotFound = errors.New("device not found")

// ErrCannotSwitch: the device is not a socket that can be switched (API v1).
var ErrCannotSwitch = errors.New("dit apparaat kan niet geschakeld worden")

// DeviceUpdate holds the fields of a device that can be changed; nil keeps
// a field as it is.
type DeviceUpdate struct {
	Name         *string
	Enabled      *bool
	ShowCounters *bool
}

// Update changes the name, enabled state or display settings of a device.
func (m *Manager) Update(ctx context.Context, id string, u DeviceUpdate) (DeviceView, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	w := m.worker(id)
	if w == nil {
		return DeviceView{}, ErrNotFound
	}
	d := w.device()
	if u.Name != nil {
		n := strings.TrimSpace(*u.Name)
		if n == "" || len(n) > 60 {
			return DeviceView{}, errors.New("naam moet 1 tot 60 tekens zijn")
		}
		d.Name = n
	}
	if u.Enabled != nil {
		d.Enabled = *u.Enabled
	}
	if u.ShowCounters != nil {
		d.ShowCounters = *u.ShowCounters
	}
	if err := m.store.SaveDevice(ctx, d); err != nil {
		return DeviceView{}, err
	}
	w.setDevice(d)
	w.poke()
	view := w.snapshot()
	m.hub.Publish(Event{Type: "device", Data: view})
	return view, nil
}

// Remove deletes a device; purge also deletes its history.
func (m *Manager) Remove(ctx context.Context, id string, purge bool) error {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	m.mu.Lock()
	w := m.workers[id]
	delete(m.workers, id)
	m.mu.Unlock()
	if w == nil {
		return ErrNotFound
	}
	w.cancel()
	if err := m.store.DeleteDevice(ctx, id, purge); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	m.recorder.Forget(id)
	m.Outages.Forget(id)
	m.hub.Publish(Event{Type: "removed", Data: map[string]string{"id": id}})
	return nil
}

// SetPower switches an Energy Socket.
func (m *Manager) SetPower(ctx context.Context, id string, on bool) (DeviceView, error) {
	w := m.worker(id)
	if w == nil {
		return DeviceView{}, ErrNotFound
	}
	if !w.snapshot().CanSwitch {
		return DeviceView{}, ErrCannotSwitch
	}
	state, err := w.client().SetState(ctx, homewizard.SocketState{PowerOn: &on})
	if err != nil {
		return DeviceView{}, err
	}
	w.applyState(state)
	w.poke()
	view := w.snapshot()
	m.hub.Publish(Event{Type: "device", Data: view})
	return view, nil
}

// Identify makes the device blink.
func (m *Manager) Identify(ctx context.Context, id string) error {
	w := m.worker(id)
	if w == nil {
		return ErrNotFound
	}
	return w.client().Identify(ctx)
}

// NormalizeHost strips schemes, paths and default ports from user input.
func NormalizeHost(input string) (string, error) {
	h := strings.TrimSpace(input)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "http://"), "https://")
	if i := strings.IndexAny(h, "/?#"); i >= 0 {
		h = h[:i]
	}
	if h == "" {
		return "", errors.New("vul een IP-adres of hostnaam in")
	}
	if host, port, err := net.SplitHostPort(h); err == nil && (port == "80" || port == "443") {
		h = host
	}
	return h, nil
}

// AddManual adds a device by address. It tries API v1 first and falls back
// to API v2, which then needs pairing.
func (m *Manager) AddManual(ctx context.Context, input string) (DeviceView, error) {
	host, err := NormalizeHost(input)
	if err != nil {
		return DeviceView{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	v1 := homewizard.NewClient(host, 1, "", "", m.cfg.Client)
	defer v1.Close()
	info, errV1 := v1.Info(ctx)
	if errV1 == nil {
		if !strings.HasPrefix(info.APIVersion, "v1") && info.APIVersion != "" && info.APIVersion != "1" {
			m.log.Warn("unexpected api version on v1 endpoint", "host", host, "api_version", info.APIVersion)
		}
		return m.upsert(ctx, Candidate{Host: host, APIVersion: 1, Serial: info.Serial, ProductType: info.ProductType,
			ProductName: info.ProductName, Firmware: info.FirmwareVersion, APIEnabled: true}, "manual")
	}

	v2 := homewizard.NewClient(host, 2, "", "", m.cfg.Client)
	defer v2.Close()
	info, errV2 := v2.Info(ctx)
	switch {
	case errV2 == nil:
		return m.upsert(ctx, Candidate{Host: host, APIVersion: 2, Serial: info.Serial, ProductType: info.ProductType,
			ProductName: info.ProductName, Firmware: info.FirmwareVersion, APIEnabled: true}, "manual")
	case errors.Is(errV2, homewizard.ErrUnauthorized):
		// A v2 device that does not reveal itself before pairing.
		return m.upsert(ctx, Candidate{Host: host, APIVersion: 2, APIEnabled: true}, "manual")
	case errors.Is(errV1, homewizard.ErrAPIDisabled):
		return DeviceView{}, errors.New("apparaat gevonden, maar de lokale API staat uit: zet 'Lokale API' aan in de HomeWizard Energy-app")
	default:
		m.log.Info("manual add failed", "host", host, "v1", errV1, "v2", errV2)
		return DeviceView{}, fmt.Errorf("geen HomeWizard-apparaat bereikbaar op %s", host)
	}
}

// Candidate is a device found by discovery or manual input.
type Candidate struct {
	Host        string
	APIVersion  int
	Serial      string
	ProductType string
	ProductName string
	Firmware    string
	APIEnabled  bool
}

// AddDiscovered merges a device found via mDNS.
func (m *Manager) AddDiscovered(ctx context.Context, c Candidate) {
	if _, err := m.upsert(ctx, c, "mdns"); err != nil {
		m.log.Error("add discovered device", "host", c.Host, "err", err)
	}
}

func (m *Manager) upsert(ctx context.Context, c Candidate, source string) (DeviceView, error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	w := m.findBySerial(c.Serial)
	if w == nil && c.Serial == "" {
		w = m.worker("host-" + c.Host)
	}
	if w != nil {
		d := w.device()
		changed := false
		set := func(dst *string, v string) {
			if v != "" && *dst != v {
				*dst = v
				changed = true
			}
		}
		set(&d.Firmware, c.Firmware)
		set(&d.ProductName, c.ProductName)
		if c.APIVersion >= 2 {
			set(&d.V2Host, c.Host)
			if !d.V2Available {
				d.V2Available, changed = true, true
			}
			if d.APIVersion >= 2 {
				set(&d.Host, c.Host)
			}
		} else if d.APIVersion < 2 || d.Token == "" {
			// v1 needs no pairing: prefer it over an unpaired v2 endpoint.
			set(&d.Host, c.Host)
			if d.APIVersion != 1 {
				d.APIVersion, changed = 1, true
			}
		}
		if !changed {
			return w.snapshot(), nil
		}
		if err := m.store.SaveDevice(ctx, d); err != nil {
			return DeviceView{}, err
		}
		m.log.Info("device updated", "id", d.ID, "host", d.Host, "api", d.APIVersion)
		m.restart(d)
		return m.mustView(d.ID), nil
	}

	d := store.Device{
		ID:          deviceID(c),
		Serial:      strings.ToLower(c.Serial),
		ProductType: c.ProductType,
		ProductName: c.ProductName,
		Firmware:    c.Firmware,
		Host:        c.Host,
		APIVersion:  max(c.APIVersion, 1),
		Enabled:     true,
		Source:      source,
	}
	if c.APIVersion >= 2 {
		d.V2Host, d.V2Available = c.Host, true
	}
	d.Name = m.defaultName(d)
	if err := m.store.SaveDevice(ctx, d); err != nil {
		return DeviceView{}, err
	}
	m.log.Info("device added", "id", d.ID, "type", d.ProductType, "host", d.Host, "api", d.APIVersion, "source", source)
	m.startWorker(d)
	view := m.mustView(d.ID)
	m.hub.Publish(Event{Type: "device", Data: view})
	return view, nil
}

func (m *Manager) mustView(id string) DeviceView {
	v, _ := m.View(id)
	return v
}

func (m *Manager) restart(d store.Device) {
	m.startWorker(d)
	m.hub.Publish(Event{Type: "device", Data: m.mustView(d.ID)})
}

func deviceID(c Candidate) string {
	if c.Serial != "" {
		return strings.ToLower(c.Serial)
	}
	return "host-" + c.Host
}

func (m *Manager) defaultName(d store.Device) string {
	base := homewizard.ModelName(d.ProductType)
	if base == "" {
		base = "HomeWizard-apparaat"
	}
	if d.Serial != "" && len(d.Serial) >= 4 && homewizard.KindOf(d.ProductType) != homewizard.KindP1 {
		return base + " " + strings.ToUpper(d.Serial[len(d.Serial)-4:])
	}
	return base
}

// Pair starts pairing with an API v2 device. The user must press the button
// on the device within 90 seconds. Progress is published as device events.
func (m *Manager) Pair(id string) (DeviceView, error) {
	w := m.worker(id)
	if w == nil {
		return DeviceView{}, ErrNotFound
	}
	d := w.device()
	host := d.V2Host
	if host == "" && d.APIVersion >= 2 {
		host = d.Host
	}
	if host == "" {
		return DeviceView{}, errors.New("dit apparaat ondersteunt geen API v2")
	}
	if !w.beginPairing() {
		return w.snapshot(), nil
	}
	go m.pairLoop(w, host)
	return w.snapshot(), nil
}

func (m *Manager) pairLoop(w *worker, host string) {
	d := w.device()
	client := homewizard.NewClient(host, 2, "", homewizard.VerificationName(d.ProductType, d.Serial), m.cfg.Client)
	defer client.Close()
	ctx, cancel := context.WithTimeout(m.ctx, 90*time.Second)
	defer cancel()
	m.hub.Publish(Event{Type: "device", Data: w.snapshot()})

	for {
		if m.worker(d.ID) == nil {
			return // removed while pairing
		}
		token, err := client.RequestToken(ctx, m.cfg.ClientName)
		if err == nil {
			client.Token = token
			info, infoErr := client.Info(ctx)
			m.savePaired(d.ID, host, token, info, infoErr)
			return
		}
		if !errors.Is(err, homewizard.ErrButtonPending) {
			m.log.Info("pairing request failed", "id", d.ID, "err", err)
		}
		select {
		case <-ctx.Done():
			m.endPairing(d.ID, "Koppelen verlopen: de knop is niet op tijd ingedrukt.")
			return
		case <-time.After(2 * time.Second):
		}
	}
}

// savePaired stores the token of a paired device on its latest row: while
// the button was pending it may have been renamed, updated by discovery or
// removed (then the token is dropped, so a removed device stays removed).
func (m *Manager) savePaired(id, host, token string, info homewizard.Info, infoErr error) {
	m.saveMu.Lock()
	defer m.saveMu.Unlock()
	cur := m.worker(id)
	if cur == nil {
		m.log.Info("pairing done, but the device was removed", "id", id)
		return
	}
	d := cur.device()
	d.Token, d.APIVersion, d.Host, d.V2Host, d.V2Available = token, 2, host, host, true
	if infoErr == nil {
		d.ProductType, d.ProductName, d.Firmware = info.ProductType, info.ProductName, info.FirmwareVersion
		if d.Serial == "" {
			d.Serial = strings.ToLower(info.Serial)
		}
		if strings.HasPrefix(d.Name, "HomeWizard-apparaat") || d.Name == "" {
			d.Name = m.defaultName(d)
		}
	}
	if err := m.store.SaveDevice(context.Background(), d); err != nil {
		m.log.Error("save paired device", "id", id, "err", err)
		m.endPairing(id, "Opslaan mislukt: "+err.Error())
		return
	}
	m.log.Info("device paired", "id", id)
	// the pairing is over: the new worker must not take it over, or it
	// would show "pairing" and never poll
	cur.endPairing("")
	m.restart(d)
}

// endPairing ends the pairing on the device's current worker (discovery
// may have replaced the one that started it) and publishes it.
func (m *Manager) endPairing(id, msg string) {
	if w := m.worker(id); w != nil {
		w.endPairing(msg)
		m.hub.Publish(Event{Type: "device", Data: w.snapshot()})
	}
}
