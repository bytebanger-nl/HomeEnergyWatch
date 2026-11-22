// Package forecast holds the settings of the forecasts (the advance
// forecast of electricity, gas and water, and the heating forecast of
// ista): the percentages they use, with the user's own values when set.
// Every percentage has an automatic value, computed from the measurements
// where that is possible or else a default; a setting overrides it until it
// is reset.
package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// Settings are the user's own values; nil is automatic.
type Settings struct {
	// DayMinPct: the share of a day's quarter hours that must be measured
	// for the day to count as measured (the rest is filled in).
	DayMinPct *float64 `json:"day_min_pct,omitempty"`
	// PeriodMinPct: the share of the days of the previous period (the 12
	// months before the settlement period) that must be measured for the
	// forecast to use it.
	PeriodMinPct *float64 `json:"period_min_pct,omitempty"`
	// Trend: correct the rest of the period by how this period's usage so
	// far compares to the same days of the previous period.
	Trend *bool `json:"trend,omitempty"`
	// The uncertainty (±) of usage that follows the previous period, per
	// kind; automatic: computed from how the months of this period differ
	// from the same months of the previous period.
	ElectricityPct *float64 `json:"electricity_pct,omitempty"`
	GasPct         *float64 `json:"gas_pct,omitempty"`
	WaterPct       *float64 `json:"water_pct,omitempty"`
	HeatingPct     *float64 `json:"heating_pct,omitempty"`
	// The uncertainty of usage from a yearly usage entered with the contract.
	EnteredPct        *float64 `json:"entered_pct,omitempty"`
	HeatingEnteredPct *float64 `json:"heating_entered_pct,omitempty"`
	// The uncertainty of expected market prices (dynamic contracts) and of
	// heating prices taken over from the previous settlement.
	PricePct        *float64 `json:"price_pct,omitempty"`
	HeatingPricePct *float64 `json:"heating_price_pct,omitempty"`
}

// Values are the automatic values without measurements: the defaults.
type Values struct {
	DayMinPct         float64 `json:"day_min_pct"`
	PeriodMinPct      float64 `json:"period_min_pct"`
	Trend             bool    `json:"trend"`
	ElectricityPct    float64 `json:"electricity_pct"`
	GasPct            float64 `json:"gas_pct"`
	WaterPct          float64 `json:"water_pct"`
	HeatingPct        float64 `json:"heating_pct"`
	EnteredPct        float64 `json:"entered_pct"`
	HeatingEnteredPct float64 `json:"heating_entered_pct"`
	PricePct          float64 `json:"price_pct"`
	HeatingPricePct   float64 `json:"heating_price_pct"`
}

// Defaults are used when a value is neither set nor computed.
var Defaults = Values{
	DayMinPct:         92,
	PeriodMinPct:      90,
	Trend:             true,
	ElectricityPct:    8,
	GasPct:            8,
	WaterPct:          8,
	HeatingPct:        15,
	EnteredPct:        10,
	HeatingEnteredPct: 20,
	PricePct:          10,
	HeatingPricePct:   10,
}

// Computed uncertainties are kept within these bounds (%): a few months can
// agree by chance, or differ by a one-off.
const (
	MinComputedPct = 3
	MaxComputedPct = 25
)

// Sources of a used value.
const (
	SourceComputed = "computed" // from the measurements
	SourceDefault  = "default"  // Defaults
	SourceSetting  = "setting"  // the user's own value
)

// Used is one percentage a forecast used, for showing it.
type Used struct {
	// Key names the percentage: day_min, period_min, usage_electricity,
	// usage_gas, usage_water, usage_heating, entered, entered_heating,
	// price, price_heating, or trend_import, trend_export, trend_gas,
	// trend_water (Pct is then the change, e.g. -12 for 12% less).
	Key    string  `json:"key"`
	Pct    float64 `json:"pct"`
	Source string  `json:"source"`
	// Auto and AutoSource: what the automatic value is, when a setting
	// overrides it (or switches the trend off).
	Auto       *float64 `json:"auto,omitempty"`
	AutoSource string   `json:"auto_source,omitempty"`
	// Months: the number of months a computed value rests on.
	Months int `json:"months,omitempty"`
}

// Pick returns the value to use: the setting when there is one, else the
// automatic value (with its source and the months it rests on).
func Pick(key string, setting *float64, auto float64, autoSource string, months int) Used {
	u := Used{Key: key, Pct: round1(auto), Source: autoSource}
	if autoSource == SourceComputed {
		u.Months = months
	}
	if setting != nil {
		a := u.Pct
		u = Used{Key: key, Pct: *setting, Source: SourceSetting, Auto: &a, AutoSource: autoSource, Months: u.Months}
	}
	return u
}

// Computed returns a computed uncertainty within the bounds, or the default
// when it could not be computed (ok false).
func Computed(v float64, ok bool, def float64) (float64, string) {
	if !ok {
		return def, SourceDefault
	}
	return math.Min(MaxComputedPct, math.Max(MinComputedPct, v)), SourceComputed
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// Or returns the setting, or def when it is not set.
func Or(setting *float64, def float64) float64 {
	if setting != nil {
		return *setting
	}
	return def
}

// TrendOn reports whether the trend correction is used.
func (s Settings) TrendOn() bool {
	if s.Trend != nil {
		return *s.Trend
	}
	return Defaults.Trend
}

// Check refuses values out of range.
func (s Settings) Check() error {
	check := func(name string, v *float64, lo, hi float64) error {
		if v != nil && (math.IsNaN(*v) || *v < lo || *v > hi) {
			return fmt.Errorf("%s moet tussen %g en %g%% liggen", name, lo, hi)
		}
		return nil
	}
	for _, c := range []struct {
		name   string
		v      *float64
		lo, hi float64
	}{
		{"Een dag telt als gemeten vanaf", s.DayMinPct, 50, 100},
		{"De vorige periode telt mee vanaf", s.PeriodMinPct, 50, 100},
		{"De marge op stroom", s.ElectricityPct, 0, 100},
		{"De marge op gas", s.GasPct, 0, 100},
		{"De marge op water", s.WaterPct, 0, 100},
		{"De marge op verwarming", s.HeatingPct, 0, 100},
		{"De marge op een ingevuld jaarverbruik", s.EnteredPct, 0, 100},
		{"De marge op een ingevuld verbruik voor verwarming", s.HeatingEnteredPct, 0, 100},
		{"De marge op marktprijzen", s.PricePct, 0, 100},
		{"De marge op de prijs voor verwarming", s.HeatingPricePct, 0, 100},
	} {
		if err := check(c.name, c.v, c.lo, c.hi); err != nil {
			return err
		}
	}
	return nil
}

// settingKey is the store key of the settings.
const settingKey = "forecast"

// Service loads and saves the settings. A nil Service has no settings.
type Service struct {
	Store *store.Store
}

// Load returns the settings; empty (all automatic) when none are stored.
func (s *Service) Load(ctx context.Context) (Settings, error) {
	var out Settings
	if s == nil || s.Store == nil {
		return out, nil
	}
	v, err := s.Store.Setting(ctx, settingKey)
	if err != nil || v == "" {
		return out, err
	}
	err = json.Unmarshal([]byte(v), &out)
	return out, err
}

// Save checks and stores the settings; all automatic removes them.
func (s *Service) Save(ctx context.Context, set Settings) error {
	if err := set.Check(); err != nil {
		return err
	}
	if set == (Settings{}) {
		return s.Store.SaveSetting(ctx, settingKey, "")
	}
	b, err := json.Marshal(set)
	if err != nil {
		return err
	}
	return s.Store.SaveSetting(ctx, settingKey, string(b))
}
