package tado

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

const (
	defaultOverlay = 2 * time.Hour
	minOverlay     = 30 * time.Minute
	maxOverlay     = 12 * time.Hour
)

// overlayDuration parses the configured overlay length in minutes. The
// minimum keeps a renewal possible within one poll interval.
func overlayDuration(s string) time.Duration {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultOverlay
	}
	d := time.Duration(n) * time.Minute
	return min(max(d, minOverlay), maxOverlay)
}

// overlay is a manual setting this gateway put on a zone and keeps alive.
type overlay struct {
	setting setting
	until   time.Time
}

// defaultManualC is the temperature "Handmatig" holds when the zone had
// none (it was off).
const defaultManualC = 20.0

// Controls: a heating zone has a temperature and a mode (schedule, manual,
// off); a hot water zone the mode (schedule, on, off); other zones only back
// to the schedule. The mode's labels are those of the "mode" metric. The
// actions "off" and "schedule" are what the mode replaced: hidden, but rules
// saved with them keep working.
func (in *instance) Controls(dev plugin.Device) []plugin.Control {
	schedule := plugin.Control{Key: "schedule", Label: "Terug naar schema", Kind: plugin.ControlAction}
	hiddenOff := plugin.Control{Key: "off", Label: "Uit", Kind: plugin.ControlAction, Hidden: true}
	hiddenSchedule := schedule
	hiddenSchedule.Hidden = true
	mode := func(manual string) plugin.Control {
		return plugin.Control{Key: "mode", Label: "Stand", Kind: plugin.ControlSelect, Metric: "mode",
			Options: []plugin.Option{
				{Value: "schedule", Label: modeSchedule},
				{Value: "manual", Label: manual},
				{Value: "off", Label: modeOff},
			}}
	}
	switch dev.Config["type"] {
	case "HEATING":
		return []plugin.Control{
			{Key: "target", Label: "Temperatuur", Kind: plugin.ControlSetpoint, Metric: "target",
				Min: 5, Max: 25, Step: 0.5, Unit: "°C"},
			mode(modeManual), hiddenOff, hiddenSchedule,
		}
	case "HOT_WATER":
		return []plugin.Control{mode(modeOn), hiddenOff, hiddenSchedule}
	}
	return []plugin.Control{schedule}
}

// Set puts a timed overlay on the zone (temperature or off) or removes it
// (back to schedule).
func (in *instance) Set(ctx context.Context, dev plugin.Device, key string, value any) error {
	home, zone := dev.Config["home_id"], dev.Config["zone_id"]
	if home == "" || zone == "" {
		return fmt.Errorf("tado: kamer zonder home/zone-id")
	}
	typ := dev.Config["type"]
	if typ == "" {
		typ = "HEATING"
	}
	k := home + ":" + zone
	var st setting
	switch key {
	case "schedule":
		if err := in.clearOverlay(ctx, home, zone); err != nil {
			return err
		}
		in.mu.Lock()
		delete(in.overlays, k)
		in.mu.Unlock()
		return nil
	case "off":
		st = setting{Type: typ, Power: "OFF"}
	case "mode":
		switch value {
		case "schedule":
			return in.Set(ctx, dev, "schedule", nil)
		case "off":
			return in.Set(ctx, dev, "off", nil)
		case "manual":
			st = setting{Type: typ, Power: "ON"}
			if typ == "HEATING" {
				// hold the temperature the zone has now
				in.mu.Lock()
				c, ok := in.targets[k]
				in.mu.Unlock()
				if !ok {
					c = defaultManualC
				}
				st.Temperature = &celsius{Celsius: &c}
			}
		default:
			return plugin.ErrNoControl
		}
	case "target":
		c, _ := value.(float64)
		st = setting{Type: typ, Power: "ON", Temperature: &celsius{Celsius: &c}}
	default:
		return plugin.ErrNoControl
	}
	in.mu.Lock()
	d := in.overlayFor
	in.mu.Unlock()
	if err := in.setOverlay(ctx, home, zone, st, d); err != nil {
		return err
	}
	in.mu.Lock()
	in.overlays[k] = &overlay{setting: st, until: time.Now().Add(d)}
	in.mu.Unlock()
	return nil
}

// renewOverlays extends the overlays this gateway set before they run out,
// so a setting holds while the gateway runs. An overlay that is gone or was
// changed elsewhere (the tado app, a thermostat) is let go.
func (in *instance) renewOverlays(ctx context.Context, home string, states map[string]zoneState) {
	in.mu.Lock()
	type renew struct {
		zone string
		ov   overlay
	}
	var todo []renew
	now := time.Now()
	for k, ov := range in.overlays {
		h, zone, ok := splitKey(k)
		if !ok || h != home {
			continue
		}
		st, found := states[zone]
		if !found || st.Overlay == nil || !sameSetting(st.Overlay.Setting, ov.setting) || now.After(ov.until) {
			delete(in.overlays, k)
			continue
		}
		// renew when the overlay would end before the next poll (plus margin)
		if ov.until.Sub(now) < pollSeconds*time.Second+5*time.Minute {
			todo = append(todo, renew{zone: zone, ov: *ov})
		}
	}
	d := in.overlayFor
	in.mu.Unlock()
	for _, r := range todo {
		if err := in.setOverlay(ctx, home, r.zone, r.ov.setting, d); err != nil {
			continue // tried again on the next poll while the old one lasts
		}
		in.mu.Lock()
		if ov, ok := in.overlays[home+":"+r.zone]; ok {
			ov.until = time.Now().Add(d)
		}
		in.mu.Unlock()
	}
}

func splitKey(k string) (home, zone string, ok bool) {
	for i := 0; i < len(k); i++ {
		if k[i] == ':' {
			return k[:i], k[i+1:], true
		}
	}
	return "", "", false
}

func sameSetting(a, b setting) bool {
	if a.Power != b.Power {
		return false
	}
	if a.Power != "ON" {
		return true
	}
	if a.Temperature == nil || b.Temperature == nil || a.Temperature.Celsius == nil || b.Temperature.Celsius == nil {
		return a.Temperature == nil && b.Temperature == nil
	}
	d := *a.Temperature.Celsius - *b.Temperature.Celsius
	return d < 0.05 && d > -0.05
}
