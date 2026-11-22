// Package outage keeps the log of when devices did not respond, for every
// kind of device (HomeWizard devices and plugin devices report to the same
// Log), so the PWA can show how often and how long a device was away.
//
// A stop and start of the gateway is not an outage of its devices: an outage
// open when the gateway stops is ended then and marked cut, the time the
// gateway was not running is never counted, and right after a start a device
// that does not answer yet (the network or the device's Wi-Fi still coming
// up) is only counted once it was seen since the start or Grace has passed.
package outage

import (
	"context"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// DefaultGrace is how long after a start a device that was not seen yet may
// fail without that being an outage.
const DefaultGrace = 2 * time.Minute

// Log records outages. A nil *Log records nothing, so a worker without one
// (tests) needs no checks.
type Log struct {
	store *store.Store
	log   *slog.Logger
	now   func() time.Time
	grace time.Duration

	// OnChange is called after an outage started or ended (optional; set
	// before Start), e.g. to tell the PWA.
	OnChange func()

	mu      sync.Mutex
	started time.Time
	seen    map[string]bool // devices that responded since the start
	open    map[string]bool // devices with an open outage
}

// New creates a Log; call Start before devices report.
func New(st *store.Store, log *slog.Logger) *Log {
	return &Log{store: st, log: log, now: time.Now, grace: DefaultGrace,
		seen: map[string]bool{}, open: map[string]bool{}}
}

// Start ends the outages that were still open when the gateway last stopped
// without Stop (a crash or power cut): they end at the last moment the
// gateway is known to have run.
func (l *Log) Start(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	l.started = l.now()
	l.mu.Unlock()
	return l.store.CutOutages(ctx, time.Time{})
}

// Run notes every minute that the gateway runs (store.AliveKey), until ctx
// is done, so an outage open when the gateway is killed (a crash, a power
// cut) ends at most a minute before it stopped, not at its last sample.
func (l *Log) Run(ctx context.Context) {
	if l == nil {
		return
	}
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		if err := l.store.SaveSetting(ctx, store.AliveKey, strconv.FormatInt(l.now().UnixMilli(), 10)); err != nil && ctx.Err() == nil {
			l.log.Error("gateway-hartslag niet opgeslagen", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// Stop ends every open outage now, marked as cut by the gateway stopping.
func (l *Log) Stop(ctx context.Context) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.open = map[string]bool{}
	at := l.now()
	l.mu.Unlock()
	if err := l.store.CutOutages(ctx, at); err != nil {
		l.log.Error("storingen afsluiten", "err", err)
	}
}

// Online reports that device id responded at at; an open outage ends.
func (l *Log) Online(ctx context.Context, id string, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.seen[id] = true
	wasOpen := l.open[id]
	delete(l.open, id)
	l.mu.Unlock()
	if !wasOpen {
		return
	}
	if err := l.store.EndOutage(ctx, id, at); err != nil {
		l.log.Error("storing afsluiten", "device", id, "err", err)
	}
	l.changed()
}

// Offline reports that device id counts as offline, not responding since
// since (its first failed poll), with the error message. Within the grace
// after a start a device not seen yet is not counted; once the grace has
// passed its outage starts at the end of the grace at the earliest.
func (l *Log) Offline(ctx context.Context, id string, since time.Time, message string) {
	if l == nil {
		return
	}
	now := l.now()
	l.mu.Lock()
	if l.open[id] {
		l.mu.Unlock()
		return
	}
	if !l.seen[id] {
		graceEnd := l.started.Add(l.grace)
		if now.Before(graceEnd) {
			l.mu.Unlock()
			return
		}
		if since.Before(graceEnd) {
			since = graceEnd
		}
	}
	l.open[id] = true
	l.mu.Unlock()
	if err := l.store.StartOutage(ctx, id, since, message); err != nil {
		l.log.Error("storing vastleggen", "device", id, "err", err)
	}
	l.changed()
}

// End ends the open outage of device id at at without counting the device
// as seen: it is no longer polled (disabled, being paired).
func (l *Log) End(ctx context.Context, id string, at time.Time) {
	if l == nil {
		return
	}
	l.mu.Lock()
	wasOpen := l.open[id]
	delete(l.open, id)
	l.mu.Unlock()
	if !wasOpen {
		return
	}
	if err := l.store.EndOutage(ctx, id, at); err != nil {
		l.log.Error("storing afsluiten", "device", id, "err", err)
	}
	l.changed()
}

func (l *Log) changed() {
	if l.OnChange != nil {
		l.OnChange()
	}
}

// Forget drops what is known of a removed device.
func (l *Log) Forget(id string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	delete(l.seen, id)
	delete(l.open, id)
	l.mu.Unlock()
}
