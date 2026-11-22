package plugin

import (
	"strconv"
	"sync"
	"time"
)

// Watchdog switches off devices the app turned on once they have run longer
// than a configured maximum. Air conditioners and electric radiators have no
// timer of their own (unlike tado's overlays), so this is the guard against
// an automation that keeps one running; it only works while the gateway
// runs. A provider calls Note after each write and Expired on each poll.
type Watchdog struct {
	max time.Duration // 0 = off

	mu sync.Mutex
	on map[string]time.Time // device id -> switched on by the app at
}

// NewWatchdog takes the maximum as an instance setting in hours ("" or 0 =
// off).
func NewWatchdog(hours string) *Watchdog {
	h, _ := strconv.ParseFloat(hours, 64)
	if h < 0 {
		h = 0
	}
	return &Watchdog{max: time.Duration(h * float64(time.Hour)), on: map[string]time.Time{}}
}

// Note records a write: the device is on after it (keeps the first time), or
// off.
func (w *Watchdog) Note(id string, on bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !on {
		delete(w.on, id)
		return
	}
	if _, ok := w.on[id]; !ok {
		w.on[id] = time.Now()
	}
}

// Expired reports whether a device the app switched on has run too long. A
// device seen off is forgotten.
func (w *Watchdog) Expired(id string, on bool, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	since, ok := w.on[id]
	if !ok {
		return false
	}
	if !on {
		delete(w.on, id)
		return false
	}
	return w.max > 0 && now.Sub(since) >= w.max
}
