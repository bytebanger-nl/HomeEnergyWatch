package gateway

import (
	"encoding/json"
	"sync"
)

// Event is pushed to live subscribers (the PWA via server-sent events).
type Event struct {
	// Type is "device" (one device changed) or "removed".
	Type string `json:"type"`
	Data any    `json:"data"`
}

// Hub fans out events to subscribers. Slow subscribers miss events instead
// of blocking the gateway.
type Hub struct {
	mu   sync.Mutex
	subs map[chan []byte]struct{}
}

// NewHub creates an empty hub.
func NewHub() *Hub { return &Hub{subs: map[chan []byte]struct{}{}} }

// Subscribe returns a channel of JSON encoded events and a cancel function.
func (h *Hub) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Subscribers returns the number of live subscribers.
func (h *Hub) Subscribers() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}

// Publish sends an event to all subscribers.
func (h *Hub) Publish(e Event) {
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- data:
		default: // subscriber is too slow; drop
		}
	}
}
