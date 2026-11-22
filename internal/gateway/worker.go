package gateway

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// offlineAfter consecutive failures mark a device offline.
const offlineAfter = 3

type worker struct {
	m      *Manager
	cancel context.CancelFunc
	wake   chan struct{}

	mu       sync.Mutex
	dev      store.Device
	cli      *homewizard.Client
	view     DeviceView
	failures int
	failedAt time.Time // first failed poll of the current run of failures
	pairing  bool
	pairMsg  string
	power    *bool
	locked   *bool
}

func (w *worker) setDevice(d store.Device) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.cli != nil {
		w.cli.Close()
	}
	w.dev = d
	serverName := ""
	if d.APIVersion >= 2 {
		serverName = homewizard.VerificationName(d.ProductType, d.Serial)
	}
	w.cli = homewizard.NewClient(d.Host, d.APIVersion, d.Token, serverName, w.m.cfg.Client)
	if w.view.Status == "" {
		w.view.Status = StatusConnecting
	}
	w.refreshLocked()
}

func (w *worker) device() store.Device {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dev
}

func (w *worker) client() *homewizard.Client {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cli
}

func (w *worker) snapshot() DeviceView {
	w.mu.Lock()
	defer w.mu.Unlock()
	v := w.view
	if v.Reading != nil {
		r := *v.Reading
		v.Reading = &r
	}
	return v
}

// refreshLocked recomputes the static part of the view. w.mu must be held.
func (w *worker) refreshLocked() {
	d := w.dev
	kind := homewizard.KindOf(d.ProductType)
	v := &w.view
	v.ID, v.Name, v.Kind = d.ID, d.Name, kind
	v.Model = homewizard.ModelName(d.ProductType)
	v.ProductType, v.ProductName, v.Serial, v.Firmware = d.ProductType, d.ProductName, d.Serial, d.Firmware
	v.Host, v.APIVersion, v.V2Available = d.Host, d.APIVersion, d.V2Available
	v.Paired = d.APIVersion >= 2 && d.Token != ""
	v.Enabled, v.Source, v.ShowCounters = d.Enabled, d.Source, d.ShowCounters
	v.CanSwitch = kind == homewizard.KindSocket && d.APIVersion < 2
	v.CanPair = (d.V2Available || d.APIVersion >= 2) && !v.Paired
	switch {
	case w.pairing:
		v.Status, v.Message = StatusPairing, "Druk op de knop van het apparaat om te koppelen."
	case !d.Enabled:
		v.Status, v.Message = StatusDisabled, ""
	case d.APIVersion >= 2 && d.Token == "":
		v.Status, v.Message = StatusNeedsPairing, "Koppel dit apparaat om het uit te lezen."
		if w.pairMsg != "" {
			v.Message = w.pairMsg
		}
	case v.Status == StatusDisabled || v.Status == StatusPairing || v.Status == StatusNeedsPairing && d.Token != "" && w.failures == 0:
		// the reason for this status no longer applies
		v.Status, v.Message = StatusConnecting, ""
	}
}

func (w *worker) beginPairing() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.pairing {
		return false
	}
	w.pairing, w.pairMsg = true, ""
	w.refreshLocked()
	return true
}

// takePairing carries an ongoing pairing over from the worker this one
// replaces (a restart by discovery), so it shows and ends on this one.
func (w *worker) takePairing(old *worker) {
	old.mu.Lock()
	pairing := old.pairing
	old.mu.Unlock()
	if pairing {
		w.beginPairing()
	}
}

func (w *worker) endPairing(msg string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pairing, w.pairMsg = false, msg
	if w.view.Status == StatusPairing {
		w.view.Status = StatusConnecting
	}
	w.refreshLocked()
	if w.view.Status == StatusConnecting && msg != "" {
		w.view.Message = msg
	}
}

func (w *worker) applyState(s homewizard.SocketState) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if s.PowerOn != nil {
		w.power = s.PowerOn
	}
	if s.SwitchLock != nil {
		w.locked = s.SwitchLock
	}
	if w.view.Reading != nil {
		r := *w.view.Reading
		r.PowerOn, r.Locked = w.power, w.locked
		w.view.Reading = &r
	}
}

// poke triggers an immediate poll.
func (w *worker) poke() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

func (w *worker) run(ctx context.Context) {
	defer w.client().Close()
	for {
		delay := w.poll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-time.After(delay):
		}
	}
}

// poll reads the device once and returns the delay until the next poll.
func (w *worker) poll(ctx context.Context) time.Duration {
	interval := w.m.cfg.Interval
	w.mu.Lock()
	d, cli, pairing := w.dev, w.cli, w.pairing
	w.mu.Unlock()
	if pairing || !d.Enabled || (d.APIVersion >= 2 && d.Token == "") {
		w.m.Outages.End(ctx, d.ID, time.Now()) // not polled: no longer an outage
		w.mu.Lock()
		w.refreshLocked()
		w.mu.Unlock()
		return 5 * time.Second
	}

	timeout := 5 * time.Second
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	reading, err := cli.Measurement(pctx)
	kind := homewizard.KindOf(d.ProductType)
	if err == nil && kind == homewizard.KindSocket && d.APIVersion < 2 {
		if state, serr := cli.State(pctx); serr == nil {
			w.applyState(state)
		}
	}
	if ctx.Err() != nil {
		return interval
	}
	if err != nil {
		return w.fail(ctx, d, err, interval)
	}

	w.mu.Lock()
	reading.PowerOn, reading.Locked = w.power, w.locked
	w.mu.Unlock()
	w.m.recorder.Add(ctx, d.ID, kind, reading)
	w.m.Outages.Online(ctx, d.ID, reading.Time)
	today := w.m.recorder.Today(ctx, d.ID, reading)

	w.mu.Lock()
	prevStatus := w.view.Status
	w.failures = 0
	w.pairMsg = ""
	seen := reading.Time
	w.view.LastSeen, w.view.Reading, w.view.Today = &seen, &reading, today
	w.view.Status, w.view.Message = StatusOnline, ""
	w.refreshLocked()
	view := w.view
	w.mu.Unlock()
	if prevStatus != StatusOnline {
		w.m.log.Info("device online", "id", d.ID, "host", d.Host, "api", d.APIVersion)
	}
	w.m.hub.Publish(Event{Type: "device", Data: view})

	// keep a steady cadence regardless of request duration
	if wait := interval - time.Since(start); wait > 0 {
		return wait
	}
	return 0
}

func (w *worker) fail(ctx context.Context, d store.Device, err error, interval time.Duration) time.Duration {
	w.mu.Lock()
	if w.failures == 0 {
		w.failedAt = time.Now()
	}
	w.failures++
	failedAt := w.failedAt
	prev := w.view.Status
	status, msg := prev, ""
	switch {
	case errors.Is(err, homewizard.ErrUnauthorized) && d.APIVersion >= 2:
		status, msg = StatusNeedsPairing, "Het apparaat accepteert de koppeling niet meer. Koppel opnieuw."
	case errors.Is(err, homewizard.ErrAPIDisabled):
		status, msg = StatusAPIDisabled, "Zet 'Lokale API' aan in de HomeWizard Energy-app."
	case w.failures >= offlineAfter:
		status, msg = StatusOffline, "Niet bereikbaar: "+err.Error()
	}
	w.view.Status, w.view.Message = status, msg
	if status == StatusNeedsPairing {
		w.view.CanPair = true
	}
	view := w.view
	failures := w.failures
	w.mu.Unlock()

	if status == StatusOffline {
		w.m.Outages.Offline(ctx, d.ID, failedAt, err.Error())
	}
	if status != prev {
		w.m.log.Warn("device status", "id", d.ID, "status", status, "err", err)
		w.m.hub.Publish(Event{Type: "device", Data: view})
	}
	// back off up to 30s while a device is unreachable
	backoff := interval * time.Duration(1<<min(failures, 4))
	return min(backoff, 30*time.Second)
}
