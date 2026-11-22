package report

import (
	"fmt"
	"math"
	"strings"

	"github.com/bytebanger-nl/homeenergywatch/internal/forecast"
)

// percentLabels name the percentages a forecast uses (as in the PWA).
var percentLabels = map[string]string{
	"period_min":        "vorige periode telt mee vanaf",
	"day_min":           "dag telt als gemeten vanaf",
	"usage_electricity": "marge op het stroomverbruik",
	"usage_gas":         "marge op het gasverbruik",
	"usage_water":       "marge op het waterverbruik",
	"usage_heating":     "marge op het verbruik",
	"entered":           "marge op het ingevulde jaarverbruik",
	"entered_heating":   "marge op het ingevulde verbruik",
	"price":             "marge op de marktprijzen",
	"price_heating":     "marge op de prijs uit de vorige afrekening",
	"trend_import":      "trend afname",
	"trend_export":      "trend teruglevering",
	"trend_gas":         "trend gas",
	"trend_water":       "trend water",
}

// percentagesText lists the percentages a forecast used, the two minimums
// only when the user set them; "" when there is nothing to list.
func percentagesText(list []forecast.Used) string {
	var parts []string
	for _, u := range list {
		if (u.Key == "period_min" || u.Key == "day_min") && u.Source != forecast.SourceSetting {
			continue
		}
		var value string
		switch {
		case strings.HasPrefix(u.Key, "trend_") && u.Source == forecast.SourceSetting:
			value = "uit"
		case strings.HasPrefix(u.Key, "trend_"):
			sign := "+"
			if u.Pct < 0 {
				sign = "-"
			}
			value = sign + percent(math.Abs(u.Pct))
		case u.Key == "period_min" || u.Key == "day_min":
			value = percent(u.Pct)
		default:
			value = "±" + percent(u.Pct)
		}
		src := "standaard"
		switch u.Source {
		case forecast.SourceComputed:
			src = "berekend"
			if u.Months > 0 {
				src = fmt.Sprintf("berekend uit %d maand", u.Months)
				if u.Months > 1 {
					src += "en"
				}
			}
		case forecast.SourceSetting:
			src = "zelf ingesteld"
		}
		label := percentLabels[u.Key]
		if label == "" {
			label = u.Key
		}
		parts = append(parts, label+" "+value+" ("+src+")")
	}
	if len(parts) == 0 {
		return ""
	}
	return "Gebruikte percentages: " + strings.Join(parts, "; ") + "."
}

// percent is a percentage with one decimal, or none when it is whole.
func percent(v float64) string {
	if v == math.Round(v) {
		return num(v, 0) + "%"
	}
	return num(v, 1) + "%"
}
