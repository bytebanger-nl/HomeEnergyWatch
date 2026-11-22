package plugin

import (
	"context"
	"time"
)

// offlineAfter consecutive failed polls mark a plugin device offline, as
// with HomeWizard devices (gateway.offlineAfter). Until then the last good
// reading is kept: a single timeout (a Wi-Fi module that answers late, a
// short network hiccup of the gateway) does not blank the card, drop its
// values from rules or leave a gap in the history.
const offlineAfter = 3

// noteOutage tells the outage log what a published poll result means: a
// device that counts as offline (after the grace above) has not responded
// since its first failed poll.
func (m *Manager) noteOutage(ctx context.Context, id string, r Reading, seen time.Time) {
	if m.Outages == nil || ctx.Err() != nil {
		return
	}
	if r.Online {
		m.Outages.Online(ctx, id, seen)
		return
	}
	m.mu.Lock()
	since, ok := m.failedAt[id]
	m.mu.Unlock()
	if !ok {
		since = seen
	}
	msg := r.Err
	if msg == "" {
		msg = "apparaat meldt zich offline"
	}
	m.Outages.Offline(ctx, id, since, msg)
}

// settle turns a poll result into what is published, given the previous
// view (prev, ok when there is one). A failed poll (an error) within the
// grace keeps the previous reading and its LastSeen, and is not recorded.
// It also logs a change of online state. m.mu must be held.
func (m *Manager) settle(view *DeviceView, r Reading, seen time.Time, prev DeviceView, hadPrev bool) (record bool) {
	failed := !r.Online && r.Err != ""
	if failed {
		if m.fails[view.ID] == 0 {
			m.failedAt[view.ID] = seen
		}
		m.fails[view.ID]++
	} else {
		delete(m.fails, view.ID)
		delete(m.failedAt, view.ID)
	}
	if failed && m.fails[view.ID] < offlineAfter && hadPrev && prev.Reading != nil && prev.Reading.Online {
		view.Reading, view.LastSeen = prev.Reading, prev.LastSeen
		m.log.Debug("plugin-apparaat: meting mislukt, vorige meting blijft staan",
			"device", view.Name, "poging", m.fails[view.ID], "err", r.Err)
		return false
	}
	rr := r
	s := seen
	view.Reading, view.LastSeen = &rr, &s

	was, known := m.online[view.ID]
	if !known || was != r.Online {
		m.online[view.ID] = r.Online
		if r.Online {
			m.log.Info("plugin-apparaat online", "plugin", view.PluginName, "device", view.Name)
		} else {
			m.log.Warn("plugin-apparaat offline", "plugin", view.PluginName, "device", view.Name,
				"mislukt", m.fails[view.ID], "err", r.Err)
		}
	}
	return true
}
