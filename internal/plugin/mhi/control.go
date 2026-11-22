package mhi

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// resultError is a beaver command the unit answered with a non-zero result.
type resultError struct {
	command string
	code    int
}

func (e *resultError) Error() string {
	return fmt.Sprintf("%s: unit gaf result %d", e.command, e.code)
}

// Results of setAirconStat (see pywfrac): 2 = this client is not one of the
// module's accounts; 1, 11 and 12 = declined for now (another client holds
// the write lock for 60 s, or the unit is busy).
const (
	resultNotRegistered = 2
)

func resultCode(err error) int {
	var re *resultError
	if errors.As(err, &re) {
		return re.code
	}
	return -1
}

// operatorID is the account this gateway registers on a module. One per
// module of at most four; the Smart M-Air app usually holds one.
const operatorID = "hwlocal"

// registerAccount adds this gateway to the module's accounts, which
// setAirconStat requires. It stays until removed (deleteAccountInfo).
func (c *client) registerAccount(ctx context.Context, ip, airconID, timezone string) error {
	_, err := c.command(ctx, ip, "updateAccountInfo", map[string]any{
		"accountId": operatorID,
		"airconId":  airconID,
		"remote":    0,
		"timezone":  timezone,
	})
	if resultCode(err) == resultNotRegistered {
		return fmt.Errorf("de airco heeft al het maximum van 4 gekoppelde apps; verwijder er een in de Smart M-Air-app")
	}
	return err
}

func (c *client) setStat(ctx context.Context, ip, airconID, frame string) error {
	_, err := c.command(ctx, ip, "setAirconStat", map[string]any{"airconId": airconID, "airconStat": frame})
	return err
}

var (
	modeOptions = []plugin.Option{
		{Value: "AUTO", Label: "Automatisch"}, {Value: "COOL", Label: "Koelen"}, {Value: "HEAT", Label: "Verwarmen"},
		{Value: "FAN", Label: "Ventileren"}, {Value: "DRY", Label: "Ontvochtigen"},
	}
	fanOptions = []plugin.Option{
		{Value: "AUTO", Label: "Automatisch"}, {Value: "QUIET", Label: "Stil"}, {Value: "LOW", Label: "Laag"},
		{Value: "MEDIUM", Label: "Midden"}, {Value: "HIGH", Label: "Hoog"},
	}
	swingUDOptions = vaneOptions("Zwaaien", "Hoogst", "Hoog", "Laag", "Laagst")
	swingLROptions = vaneOptions("Zwaaien", "Links", "Links-midden", "Midden", "Rechts-midden", "Rechts",
		"Breed (links en rechts)", "Smal (naar het midden)")
)

// The vane positions as the Smart M-Air app numbers them (via pywfrac):
// 0 = swinging, then fixed positions.
func vaneOptions(labels ...string) []plugin.Option {
	out := make([]plugin.Option, len(labels))
	for i, l := range labels {
		out[i] = plugin.Option{Value: strconv.Itoa(i), Label: l}
	}
	return out
}

// optionLabel is the label of an option value, which is also the metric
// value (the PWA matches a select's current choice on it); a value without
// an option is shown as is.
func optionLabel(opts []plugin.Option, value string) string {
	for _, o := range opts {
		if o.Value == value {
			return o.Label
		}
	}
	return value
}

// Controls: on/off, mode, setpoint, fan, both vanes and 3D auto.
func (in *instance) Controls(plugin.Device) []plugin.Control {
	return []plugin.Control{
		{Key: "power", Label: "Aan", Kind: plugin.ControlSwitch, Metric: "power"},
		{Key: "mode", Label: "Modus", Kind: plugin.ControlSelect, Metric: "mode", Options: modeOptions},
		{Key: "preset", Label: "Temperatuur", Kind: plugin.ControlSetpoint, Metric: "preset",
			Min: minPresetC, Max: maxPresetC, Step: 0.5, Unit: "°C"},
		{Key: "fan", Label: "Ventilator", Kind: plugin.ControlSelect, Metric: "fan", Options: fanOptions},
		{Key: "swing_ud", Label: "Flap boven/onder", Kind: plugin.ControlSelect, Metric: "swing_ud", Options: swingUDOptions},
		{Key: "swing_lr", Label: "Flap links/rechts", Kind: plugin.ControlSelect, Metric: "swing_lr", Options: swingLROptions},
		{Key: "entrust", Label: "3D auto", Kind: plugin.ControlSwitch, Metric: "entrust"},
	}
}

// Set reads the unit's current state, changes one field and writes the
// whole state back (setAirconStat needs it complete).
func (in *instance) Set(ctx context.Context, dev plugin.Device, key string, value any) error {
	ip := dev.Address
	if ip == "" {
		return fmt.Errorf("geen IP-adres")
	}
	airconID := dev.Config["airconId"]
	if airconID == "" {
		di, err := in.cli.deviceInfo(ctx, ip)
		if err != nil {
			return err
		}
		airconID = di.AirconID
	}
	sc, err := in.cli.airconStat(ctx, ip, airconID)
	if err != nil {
		return err
	}
	st, err := decodeStat(sc.AirconStat)
	if err != nil {
		return err
	}
	switch key {
	case "power":
		st.Power, _ = value.(bool)
	case "mode":
		st.Mode, _ = value.(string)
	case "preset":
		st.PresetC, _ = value.(float64)
	case "fan":
		st.Fan, _ = value.(string)
	case "swing_ud", "swing_lr":
		s, _ := value.(string)
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			return fmt.Errorf("onbekende flapstand %q", s)
		}
		if key == "swing_ud" {
			st.SwingUD = n
		} else {
			st.SwingLR = n
		}
		if n > 0 {
			// 3D auto steers the vanes itself and would undo a fixed position
			st.Entrust = false
		}
	case "entrust":
		st.Entrust, _ = value.(bool)
	default:
		return plugin.ErrNoControl
	}
	if st.Fan == "" {
		st.Fan = "AUTO" // a fan step the decoder does not know: never guess another
	}
	frame, err := encodeStat(st)
	if err != nil {
		return err
	}
	err = in.cli.setStat(ctx, ip, airconID, frame)
	if resultCode(err) == resultNotRegistered {
		if err := in.cli.registerAccount(ctx, ip, airconID, sc.Timezone); err != nil {
			return fmt.Errorf("koppelen aan de airco: %w", err)
		}
		err = in.cli.setStat(ctx, ip, airconID, frame)
	}
	switch code := resultCode(err); code {
	case -1:
	case 1, 11, 12:
		return fmt.Errorf("de airco neemt nu geen opdracht aan (bezet of een andere app is bezig); probeer het over een minuut opnieuw")
	default:
		return err
	}
	if err != nil {
		return err
	}
	in.watch.Note(dev.ID, st.Power)
	return nil
}
