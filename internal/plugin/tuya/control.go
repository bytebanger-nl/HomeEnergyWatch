package tuya

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// noControl lists dp codes that are shown but never offered as a control,
// even when the model calls them writable: the child lock (a safety setting
// that belongs on the device), the sensor calibration and factory resets.
var noControl = map[string]bool{
	"child_lock":      true,
	"temp_correction": true,
	"factory_reset":   true,
}

// enumLabels gives Dutch labels for common Tuya enum values (heaters,
// thermostats and radiator valves).
var enumLabels = map[string]string{
	"auto":       "Automatisch",
	"manual":     "Handmatig",
	"comfort":    "Comfort",
	"eco":        "Eco",
	"anti_frost": "Vorstbeveiliging",
	"antifreeze": "Vorstbeveiliging",
	"frost":      "Vorstbeveiliging",
	"holiday":    "Vakantie",
	"program":    "Programma",
	"smart":      "Programma",
	"boost":      "Boost",
	"timer":      "Timer",
	"hot":        "Verwarmen",
	"cold":       "Koelen",
	"wind":       "Ventileren",
	"off":        "Uit",
	"low":        "Laag",
	"middle":     "Midden",
	"high":       "Hoog",
}

func enumLabel(v string) string {
	if l, ok := enumLabels[strings.ToLower(v)]; ok {
		return l
	}
	return v
}

// control returns the control for one dp, if it can be one: writable in the
// model (rw), a bool, value or enum, and not on the noControl list.
func (e dpEntry) control(id string) (plugin.Control, bool) {
	if e.Access != "rw" || noControl[e.Code] {
		return plugin.Control{}, false
	}
	c := plugin.Control{Key: "dp" + id, Label: labelFor(e.Code), Metric: "dp" + id}
	switch e.Type {
	case "bool":
		c.Kind = plugin.ControlSwitch
	case "value":
		div := math.Pow10(max(e.Scale, 0))
		step := e.Step
		if step <= 0 {
			step = 1
		}
		if e.Max <= e.Min {
			return plugin.Control{}, false
		}
		c.Kind = plugin.ControlSetpoint
		c.Min, c.Max, c.Step, c.Unit = e.Min/div, e.Max/div, step/div, e.Unit
	case "enum":
		if len(e.Range) == 0 {
			return plugin.Control{}, false
		}
		c.Kind = plugin.ControlSelect
		for _, v := range e.Range {
			c.Options = append(c.Options, plugin.Option{Value: v, Label: enumLabel(v)})
		}
	default:
		return plugin.Control{}, false
	}
	return c, true
}

// ids returns the schema's dp ids in numeric order.
func (s dpSchema) ids() []string {
	ids := make([]string, 0, len(s))
	for id := range s {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return dpLess(ids[i], ids[j]) })
	return ids
}

// powerDP is the writable on/off dp (switch, or switch_1 on multi-channel
// devices), which the watchdog uses to switch a device off.
func (s dpSchema) powerDP() (string, bool) {
	for _, code := range []string{"switch", "switch_1"} {
		for _, id := range s.ids() {
			if e := s[id]; e.Code == code && e.Type == "bool" && e.Access == "rw" {
				return id, true
			}
		}
	}
	return "", false
}

// Controls are derived from the device model stored at scan time. A device
// added by hand (no model) has none.
func (in *instance) Controls(dev plugin.Device) []plugin.Control {
	schema := parseSchema(dev.Config["schema"])
	var out []plugin.Control
	for _, id := range schema.ids() {
		if c, ok := schema[id].control(id); ok {
			out = append(out, c)
		}
	}
	return out
}

// Set writes one dp. The manager has already checked the value against the
// control; here it goes back to the dp's raw form (a setpoint in the dp's
// own scale).
func (in *instance) Set(ctx context.Context, dev plugin.Device, key string, value any) error {
	schema := parseSchema(dev.Config["schema"])
	id := strings.TrimPrefix(key, "dp")
	e, ok := schema[id]
	if !ok || !strings.HasPrefix(key, "dp") {
		return plugin.ErrNoControl
	}
	if _, ok := e.control(id); !ok {
		return plugin.ErrNoControl
	}
	raw := value
	if e.Type == "value" {
		f, _ := value.(float64)
		raw = int64(math.Round(f * math.Pow10(max(e.Scale, 0))))
	}
	d, err := localDevice(dev)
	if err != nil {
		return err
	}
	power, watched := schema.powerDP()
	raised := false
	if watched && e.Type == "value" {
		// Only a higher setting starts the watchdog: read the current one.
		if cur, err := d.Query(ctx); err == nil {
			f, ok := toFloat(cur[id])
			raised = !ok || toInt(raw) > int64(f)
		}
	}
	if err := d.Set(ctx, map[string]any{id: raw}); err != nil {
		return err
	}
	// The watchdog: switching off ends it, switching on or a higher setting
	// starts it (a device seen off at the next poll is forgotten).
	switch {
	case !watched:
	case id == power:
		on, _ := value.(bool)
		in.watch.Note(dev.ID, on)
	case raised:
		in.watch.Note(dev.ID, true)
	}
	return nil
}

func toInt(v any) int64 {
	n, _ := v.(int64)
	return n
}
