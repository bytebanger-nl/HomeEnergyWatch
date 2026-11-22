package tuya

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strconv"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

func init() { plugin.Register(provider{}) }

// provider is the Tuya / Smart Life provider type.
type provider struct{}

func (provider) Info() plugin.ProviderInfo {
	return plugin.ProviderInfo{
		Type: "tuya",
		Name: "Tuya / Smart Life",
		Description: "Tuya- en Smart Life-apparaten, lokaal uitgelezen via de local key. " +
			"Koppel ze via je Tuya IoT-project, of voeg er een handmatig toe. Bedienen kan als je dat toestaat " +
			"(alleen apparaten die via scannen zijn toegevoegd).",
		InstanceForm: []plugin.Field{
			{Key: "access_id", Label: "Tuya Access ID", Type: plugin.FieldText,
				Help: "Uit je Tuya IoT-project (Cloud → project → Overview). Nodig om apparaten + keys op te halen (scannen)."},
			{Key: "access_secret", Label: "Tuya Access Secret", Type: plugin.FieldPassword, Secret: true,
				Help: "Hoort bij de Access ID. Wordt alleen gebruikt om bij de Tuya-cloud je apparaten + local keys op te halen."},
			{Key: "region", Label: "Regio", Type: plugin.FieldText, Default: "EU",
				Help: "EU, US, CN of IN — het datacenter van je Tuya IoT-project. Meestal EU."},
			{Key: "max_on_hours", Label: "Automatisch uitzetten na (uur)", Type: plugin.FieldNumber,
				Help: "Een apparaat dat deze app heeft aangezet of hoger gezet, gaat na zoveel uur uit. Leeg of 0 = nooit. " +
					"Een Tuya-radiator heeft zelf geen terugvaltimer; dit werkt alleen zolang de gateway draait."},
		},
		DeviceForm: []plugin.Field{
			{Key: "address", Label: "IP-adres", Type: plugin.FieldText, Required: true,
				Help: "Het IP-adres van het apparaat in je netwerk."},
			{Key: "device_id", Label: "Device ID", Type: plugin.FieldText, Required: true,
				Help: "De device id (gwId) van het apparaat."},
			{Key: "local_key", Label: "Local key", Type: plugin.FieldPassword, Required: true, Secret: true,
				Help: "De 16-tekens local key van het apparaat."},
			{Key: "version", Label: "Protocolversie", Type: plugin.FieldText, Default: "3.3",
				Help: "3.3, 3.4 of 3.5. Nieuwere apparaten zijn vaak 3.4 of 3.5."},
		},
		CanScan:         true,
		CanAddManual:    true,
		CanLinkMeter:    true,
		CanRegulate:     true,
		IntervalSeconds: 30,
	}
}

func (provider) Instance(config map[string]string, _ plugin.Env) (plugin.Instance, error) {
	return &instance{
		accessID:     config["access_id"],
		accessSecret: config["access_secret"],
		region:       config["region"],
		watch:        plugin.NewWatchdog(config["max_on_hours"]),
		control:      config["_control"] == "true",
	}, nil
}

type instance struct {
	accessID, accessSecret, region string

	watch   *plugin.Watchdog
	control bool // the user allowed control (needed for the watchdog to act)
}

func (in *instance) Close() {}

// Poll reads one device locally over the LAN. When the watchdog finds a
// device the app switched on running too long, it switches it off.
func (in *instance) Poll(ctx context.Context, dev plugin.Device) (plugin.Reading, error) {
	d, err := localDevice(dev)
	if err != nil {
		return plugin.Reading{}, err
	}
	dps, err := d.Query(ctx)
	if err != nil {
		return plugin.Reading{}, err
	}
	schema := parseSchema(dev.Config["schema"])
	if id, ok := schema.powerDP(); ok && in.control {
		on, _ := dps[id].(bool)
		if in.watch.Expired(dev.ID, on, time.Now()) {
			if err := d.Set(ctx, map[string]any{id: false}); err == nil {
				in.watch.Note(dev.ID, false)
				dps[id] = false
			}
		}
	}
	return plugin.Reading{Online: true, Metrics: metricsFromDPS(dps, schema)}, nil
}

// localDevice is the protocol side of a stored device.
func localDevice(dev plugin.Device) (Device, error) {
	addr := dev.Address
	if addr == "" {
		addr = dev.Config["address"]
	}
	id := dev.Config["device_id"]
	key := dev.Config["local_key"]
	ver := dev.Config["version"]
	if ver == "" {
		ver = string(V33)
	}
	if addr == "" || id == "" || key == "" {
		return Device{}, fmt.Errorf("apparaat mist IP, device id of local key")
	}
	if len(key) != 16 {
		return Device{}, fmt.Errorf("local key moet 16 tekens zijn")
	}
	return Device{Addr: addr, ID: id, Key: []byte(key), Version: Version(ver)}, nil
}

// Scan lists the devices of the configured Tuya IoT project (with their local
// keys) and locates each on the LAN, so the user does not type keys or IPs.
// Devices that cannot be found on the LAN (offline) are left out.
func (in *instance) Scan(ctx context.Context) ([]plugin.Candidate, error) {
	if in.accessID == "" || in.accessSecret == "" {
		return nil, fmt.Errorf("vul eerst de Tuya Access ID en Access Secret van je IoT-project in")
	}
	cloud := newCloudClient(in.accessID, in.accessSecret, in.region)
	devices, err := cloud.listDevices(ctx)
	if err != nil {
		return nil, err
	}
	if len(devices) == 0 {
		return nil, fmt.Errorf("geen apparaten gevonden in je Tuya-project; is je Smart Life-account gekoppeld?")
	}

	// Find each device on the LAN: broadcasts first (fast, gives the version),
	// then a port scan + handshake match for whatever is left.
	located := discoverLAN(ctx, 4*time.Second)
	located = matchByScan(ctx, devices, located)

	out := make([]plugin.Candidate, 0, len(devices))
	for _, d := range devices {
		info, ok := located[d.ID]
		if !ok || info.IP == "" || !info.Version.known() {
			continue // offline or unreachable on the LAN
		}
		name := d.Name
		if name == "" {
			name = "Tuya " + lastOctet(info.IP)
		}
		cfg := map[string]string{
			"device_id": d.ID,
			"local_key": d.LocalKey,
			"version":   string(info.Version),
			"category":  d.Category,
		}
		if schema := cloud.deviceSchema(ctx, d.ID); schema != "" {
			cfg["schema"] = schema
		}
		out = append(out, plugin.Candidate{Address: info.IP, Name: name, Config: cfg})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("apparaten gevonden in je project, maar geen enkel bereikbaar op het LAN (staan ze aan en op hetzelfde netwerk?)")
	}
	return out, nil
}

func lastOctet(ip string) string {
	if i := net.ParseIP(ip).To4(); i != nil {
		return strconv.Itoa(int(i[3]))
	}
	return ip
}

// dpEntry is the per-dp metadata (from the cloud device model) that lets Poll
// label, unit and scale a local datapoint, and Controls offer the writable
// ones. Schemas stored before control existed only have code, unit and
// scale: such a device shows its values but has no controls until it is
// scanned again.
type dpEntry struct {
	Code  string `json:"code"`
	Unit  string `json:"unit,omitempty"`
	Scale int    `json:"scale,omitempty"`
	// Type is the model type: bool, value, enum, string, raw or bitmap.
	Type string `json:"type,omitempty"`
	// Access is the model's accessMode: rw, ro or wr.
	Access string `json:"access,omitempty"`
	// Min, Max and Step of a value, in raw (unscaled) units.
	Min  float64 `json:"min,omitempty"`
	Max  float64 `json:"max,omitempty"`
	Step float64 `json:"step,omitempty"`
	// Range holds the values of an enum.
	Range []string `json:"range,omitempty"`
}

// dpSchema maps a dp id (as a string) to its metadata.
type dpSchema map[string]dpEntry

// parseSchema reads a stored schema; a missing or broken one is empty.
func parseSchema(schemaJSON string) dpSchema {
	var schema dpSchema
	if schemaJSON != "" {
		_ = json.Unmarshal([]byte(schemaJSON), &schema)
	}
	return schema
}

// codeLabels gives friendly Dutch labels for common Tuya datapoint codes.
var codeLabels = map[string]string{
	"switch":             "Aan",
	"switch_1":           "Aan",
	"mode":               "Modus",
	"temp_set":           "Ingesteld",
	"temp_current":       "Temperatuur",
	"temp_correction":    "Correctie",
	"child_lock":         "Kinderslot",
	"cur_power":          "Vermogen",
	"cur_voltage":        "Spanning",
	"cur_current":        "Stroom",
	"add_ele":            "Energie",
	"work_mode":          "Modus",
	"battery_percentage": "Accu",
	"humidity_value":     "Luchtvochtigheid",
	"boost_time":         "Boost",
	"open_window":        "Open raam",
}

// metricsFromDPS turns a dps map into plugin metrics. With a schema (from a
// scan) it labels, scales and adds units, and shows enum values in Dutch;
// without one it falls back to generic "DP n" metrics typed by the JSON value.
func metricsFromDPS(dps map[string]any, schema dpSchema) []plugin.Metric {
	ids := make([]string, 0, len(dps))
	for id := range dps {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return dpLess(ids[i], ids[j]) })

	out := make([]plugin.Metric, 0, len(dps))
	for _, id := range ids {
		v := dps[id]
		m := plugin.Metric{Key: "dp" + id}
		if s, ok := schema[id]; ok {
			m.Label = labelFor(s.Code)
			m.Unit = s.Unit
			m.Value = scaled(v, s.Scale)
			if str, ok := v.(string); ok && s.Type == "enum" {
				m.Value = enumLabel(str)
			}
			m.Kind = kindOf(m.Value)
		} else {
			m.Label = "DP " + id
			m.Value = v
			m.Kind = kindOf(v)
		}
		out = append(out, m)
	}
	return out
}

func labelFor(code string) string {
	if l, ok := codeLabels[code]; ok {
		return l
	}
	if code != "" {
		return code
	}
	return "DP"
}

// scaled divides a numeric value by 10^scale (Tuya reports e.g. 181 for 18.1).
func scaled(v any, scale int) any {
	if scale <= 0 {
		return v
	}
	f, ok := toFloat(v)
	if !ok {
		return v
	}
	div := 1.0
	for i := 0; i < scale; i++ {
		div *= 10
	}
	return f / div
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

func kindOf(v any) plugin.MetricKind {
	switch v.(type) {
	case bool:
		return plugin.MetricBool
	case float64, int, json.Number:
		return plugin.MetricNumber
	default:
		return plugin.MetricText
	}
}

// dpLess orders dp ids numerically when possible, else lexically.
func dpLess(a, b string) bool {
	ai, aerr := strconv.Atoi(a)
	bi, berr := strconv.Atoi(b)
	if aerr == nil && berr == nil {
		return ai < bi
	}
	return a < b
}
