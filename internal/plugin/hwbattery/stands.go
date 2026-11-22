package hwbattery

import "github.com/bytebanger-nl/homeenergywatch/internal/homewizard"

// The stands the app offers. The API has a mode and two permissions; a
// stand is one combination, so the app shows one choice.
const (
	standZero       = "zero"
	standCharge     = "charge_only"
	standDischarge  = "discharge_only"
	standStandby    = "standby"
	standFull       = "to_full"
	standPredictive = "predictive"
)

type stand struct {
	value, label string
	// needs: "permissions" (API 2.2.0) or "charge_to_full" (2.3.0)
	needs  string
	update func(cur homewizard.Batteries) homewizard.BatteriesUpdate
}

func permissions(p ...string) *[]string { return &p }

var stands = []stand{
	{value: standZero, label: "Nul op de meter", update: func(cur homewizard.Batteries) homewizard.BatteriesUpdate {
		u := homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero}
		if cur.Permissions != nil {
			u.Permissions = permissions(homewizard.PermitCharge, homewizard.PermitDischarge)
		}
		return u
	}},
	{value: standCharge, label: "Alleen laden", needs: "permissions", update: func(homewizard.Batteries) homewizard.BatteriesUpdate {
		return homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero, Permissions: permissions(homewizard.PermitCharge)}
	}},
	{value: standDischarge, label: "Alleen ontladen", needs: "permissions", update: func(homewizard.Batteries) homewizard.BatteriesUpdate {
		return homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero, Permissions: permissions(homewizard.PermitDischarge)}
	}},
	{value: standStandby, label: "Stand-by", update: func(cur homewizard.Batteries) homewizard.BatteriesUpdate {
		if cur.Permissions != nil {
			// standby is deprecated: zero without permissions is the same
			return homewizard.BatteriesUpdate{Mode: homewizard.BatteryZero, Permissions: permissions()}
		}
		return homewizard.BatteriesUpdate{Mode: homewizard.BatteryStandby}
	}},
	{value: standFull, label: "Volladen", update: func(cur homewizard.Batteries) homewizard.BatteriesUpdate {
		if cur.ChargeToFull != nil {
			on := true
			return homewizard.BatteriesUpdate{ChargeToFull: &on}
		}
		return homewizard.BatteriesUpdate{Mode: homewizard.BatteryToFull}
	}},
	{value: standPredictive, label: "Slim (HomeWizard)", needs: "charge_to_full", update: func(homewizard.Batteries) homewizard.BatteriesUpdate {
		return homewizard.BatteriesUpdate{Mode: homewizard.BatteryPredictive}
	}},
}

// supported reports whether the meter's firmware knows the stand: the
// fields it reports show its API version.
func (s stand) supported(b homewizard.Batteries) bool {
	switch s.needs {
	case "permissions":
		return b.Permissions != nil
	case "charge_to_full":
		return b.ChargeToFull != nil
	}
	return true
}

func standByValue(v string) (stand, bool) {
	for _, s := range stands {
		if s.value == v {
			return s, true
		}
	}
	return stand{}, false
}

func label(value string) string {
	if s, ok := standByValue(value); ok {
		return s.label
	}
	return value
}

// charging reports whether a "to full" runs.
func charging(b homewizard.Batteries) bool {
	return b.Mode == homewizard.BatteryToFull || b.ChargeToFull != nil && *b.ChargeToFull
}

// standOf is the stand the meter is in.
func standOf(b homewizard.Batteries) string {
	switch {
	case charging(b):
		return standFull
	case b.Mode == homewizard.BatteryPredictive:
		return standPredictive
	case b.Mode == homewizard.BatteryStandby:
		return standStandby
	}
	charge, discharge := b.Permitted(homewizard.PermitCharge), b.Permitted(homewizard.PermitDischarge)
	switch {
	case charge && discharge:
		return standZero
	case charge:
		return standCharge
	case discharge:
		return standDischarge
	case b.Mode == homewizard.BatteryZero:
		return standStandby
	}
	return b.Mode
}
