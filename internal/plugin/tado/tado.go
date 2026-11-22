// Package tado is a plugin for tado° heating. It uses tado's cloud
// REST API (there is no local API): a one-time OAuth device-code login yields
// a refresh token kept in the data dir, and each zone (room) becomes a device.
// The bridge is never a device. Because tado rate-limits, all zones are read
// in one call per poll, at a slow default interval. When the user allows
// control, a zone can be set to a temperature, off, or back to its schedule;
// a manual setting is always a tado overlay with a timer that the gateway
// renews while it runs, so tado returns to its own schedule by itself when
// the gateway goes away.
package tado

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

func init() { plugin.Register(provider{}) }

// pollSeconds is the default cadence: one call per cycle, ~96 calls/day, well
// under tado's reported ~100/day cap for consumers.
const pollSeconds = 15 * 60

// intervalSeconds is the poll interval the provider asks for: pollSeconds,
// or a short one against a fake tado (UseBaseURL).
var intervalSeconds = pollSeconds

// UseBaseURL points the plugin at another tado, such as the fake one in
// hwsim: login at base/oauth2/..., the API at base/api/v2, polled every 10
// seconds. Call it before the plugin manager starts.
func UseBaseURL(base string) {
	base = strings.TrimRight(base, "/")
	apiBase = base + "/api/v2"
	deviceAuthURL = base + "/oauth2/device_authorize"
	tokenURL = base + "/oauth2/token"
	intervalSeconds = 10
}

type provider struct{}

func (provider) Info() plugin.ProviderInfo {
	return plugin.ProviderInfo{
		Type:        "tado",
		Name:        "tado° verwarming",
		Description: "Lees je tado°-kamers (via de tado-cloud; eenmalig inloggen). Bedienen kan als je dat toestaat. De bridge wordt niet getoond.",
		InstanceForm: []plugin.Field{
			{Key: "overlay_minutes", Label: "Handmatige instelling geldt (minuten)", Type: plugin.FieldNumber, Default: "120",
				Help: "Een instelling vanuit deze app vervalt na deze tijd vanzelf, tenzij de gateway hem verlengt. Valt de gateway uit, dan gaat tado terug naar het eigen schema. Elke instelling en verlenging telt mee voor het daglimiet van tado."},
		},
		DeviceForm:      nil, // devices come from scanning zones
		CanScan:         true,
		CanAddManual:    false,
		NeedsAuth:       true,
		IntervalSeconds: intervalSeconds,
	}
}

func (provider) Instance(cfg map[string]string, env plugin.Env) (plugin.Instance, error) {
	in := &instance{env: env, http: &http.Client{Timeout: 15 * time.Second},
		overlayFor: overlayDuration(cfg["overlay_minutes"]), overlays: map[string]*overlay{}, targets: map[string]float64{}}
	in.loadToken()
	return in, nil
}

type instance struct {
	env  plugin.Env
	http *http.Client

	mu           sync.Mutex
	refreshToken string
	accessToken  string
	accessExpiry time.Time
	authPending  bool
	authMsg      string
	cancelAuth   context.CancelFunc

	overlayFor time.Duration       // how long a manual setting lasts unless renewed
	overlays   map[string]*overlay // by "home:zone": manual settings this gateway keeps alive
	targets    map[string]float64  // by "home:zone": the set temperature at the last poll, while on
}

func (in *instance) Close() {
	in.mu.Lock()
	if in.cancelAuth != nil {
		in.cancelAuth()
		in.cancelAuth = nil
	}
	in.mu.Unlock()
}

// Scan lists the account's zones (rooms) as devices. The bridge and other
// physical devices are deliberately not included.
func (in *instance) Scan(ctx context.Context) ([]plugin.Candidate, error) {
	home, err := in.homeID(ctx)
	if err != nil {
		return nil, err
	}
	zs, err := in.zones(ctx, home)
	if err != nil {
		return nil, err
	}
	homeStr := strconv.Itoa(home)
	out := make([]plugin.Candidate, 0, len(zs))
	for _, z := range zs {
		out = append(out, plugin.Candidate{
			Address: homeStr + ":" + strconv.Itoa(z.ID),
			Name:    z.Name,
			Config:  map[string]string{"home_id": homeStr, "zone_id": strconv.Itoa(z.ID), "type": z.Type},
		})
	}
	return out, nil
}

// Poll satisfies the interface but is unused: the manager calls PollAll for a
// BatchPoller. It is here so a lone read still works if ever called.
func (in *instance) Poll(ctx context.Context, dev plugin.Device) (plugin.Reading, error) {
	res, err := in.PollAll(ctx, []plugin.Device{dev})
	if err != nil {
		return plugin.Reading{}, err
	}
	return res[dev.ID], nil
}

// PollAll reads every zone of the home in one API call and maps each device
// (zone) to a reading.
func (in *instance) PollAll(ctx context.Context, devices []plugin.Device) (map[string]plugin.Reading, error) {
	if len(devices) == 0 {
		return map[string]plugin.Reading{}, nil
	}
	home, err := strconv.Atoi(devices[0].Config["home_id"])
	if err != nil {
		return nil, err
	}
	states, err := in.zoneStates(ctx, home)
	if err != nil {
		return nil, err
	}
	in.renewOverlays(ctx, strconv.Itoa(home), states)
	out := make(map[string]plugin.Reading, len(devices))
	for _, d := range devices {
		zid := d.Config["zone_id"]
		st, ok := states[zid]
		if !ok {
			out[d.ID] = plugin.Reading{Online: false, Err: "kamer niet gevonden"}
			continue
		}
		in.noteTarget(strconv.Itoa(home)+":"+zid, st.Setting)
		out[d.ID] = plugin.Reading{Online: true, Metrics: zoneMetrics(st)}
	}
	return out, nil
}

// noteTarget remembers a zone's set temperature, which "Handmatig" holds.
func (in *instance) noteTarget(k string, s setting) {
	if s.Power != "ON" || s.Temperature == nil || s.Temperature.Celsius == nil {
		return
	}
	in.mu.Lock()
	if in.targets == nil {
		in.targets = map[string]float64{}
	}
	in.targets[k] = *s.Temperature.Celsius
	in.mu.Unlock()
}

// The labels of the "mode" metric, also the options of the mode control.
const (
	modeSchedule = "Schema"
	modeManual   = "Handmatig"
	modeOn       = "Aan"
	modeOff      = "Uit"
)

// zoneMode is how a zone runs: by its schedule, or by a manual setting
// (an overlay: on, or off).
func zoneMode(st zoneState) string {
	switch {
	case st.Overlay == nil:
		return modeSchedule
	case st.Overlay.Setting.Power != "ON":
		return modeOff
	case st.Setting.Type == "HOT_WATER" || st.Overlay.Setting.Type == "HOT_WATER":
		return modeOn
	}
	return modeManual
}

func zoneMetrics(st zoneState) []plugin.Metric {
	var m []plugin.Metric
	if st.SensorDataPoints.InsideTemperature != nil && st.SensorDataPoints.InsideTemperature.Celsius != nil {
		m = append(m, plugin.Metric{Key: "temp", Label: "Temperatuur", Kind: plugin.MetricNumber,
			Value: round1(*st.SensorDataPoints.InsideTemperature.Celsius), Unit: "°C"})
	}
	on := st.Setting.Power == "ON"
	if on && st.Setting.Temperature != nil && st.Setting.Temperature.Celsius != nil {
		m = append(m, plugin.Metric{Key: "target", Label: "Ingesteld", Kind: plugin.MetricNumber,
			Value: round1(*st.Setting.Temperature.Celsius), Unit: "°C"})
	}
	if st.SensorDataPoints.Humidity != nil && st.SensorDataPoints.Humidity.Percentage != nil {
		m = append(m, plugin.Metric{Key: "humidity", Label: "Luchtvochtigheid", Kind: plugin.MetricNumber,
			Value: round1(*st.SensorDataPoints.Humidity.Percentage), Unit: "%"})
	}
	if st.ActivityDataPoints.HeatingPower != nil && st.ActivityDataPoints.HeatingPower.Percentage != nil {
		m = append(m, plugin.Metric{Key: "heating", Label: "Verwarming", Kind: plugin.MetricNumber,
			Value: round1(*st.ActivityDataPoints.HeatingPower.Percentage), Unit: "%"})
	}
	m = append(m, plugin.Metric{Key: "power", Label: "Aan", Kind: plugin.MetricBool, Value: on})
	m = append(m, plugin.Metric{Key: "mode", Label: "Stand", Kind: plugin.MetricText, Value: zoneMode(st)})
	if hasOpenWindow(st.OpenWindow) {
		m = append(m, plugin.Metric{Key: "window", Label: "Raam open", Kind: plugin.MetricBool, Value: true})
	}
	return m
}

// hasOpenWindow reports whether the openWindow field is a non-null object.
func hasOpenWindow(raw []byte) bool {
	s := string(raw)
	return s != "" && s != "null"
}

func round1(v float64) float64 {
	return float64(int(v*10+0.5)) / 10
}
