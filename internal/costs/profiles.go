package costs

import "time"

// Seasonal profiles: the share of a year's usage per calendar month. They
// approximate the standard Dutch household profiles that grid operators and
// suppliers use for advance payments (NEDU E1A for electricity, G1A for gas)
// and the yield of solar panels in the Netherlands. A forecast only falls
// back on them for days without own measurements of a year earlier.
var (
	electricityProfile = normalized([12]float64{9.9, 8.6, 8.8, 7.8, 7.4, 6.9, 7.1, 7.3, 7.5, 8.4, 9.3, 10.9})
	gasProfile         = normalized([12]float64{17.0, 14.8, 12.8, 8.1, 4.6, 2.6, 2.1, 2.2, 3.4, 7.2, 11.4, 13.8})
	solarProfile       = normalized([12]float64{2.5, 4.5, 8.0, 11.5, 13.5, 13.5, 13.5, 11.5, 9.0, 6.0, 3.5, 2.0})
	// household water use is nearly flat, a little higher in summer
	waterProfile = normalized([12]float64{8.1, 7.4, 8.1, 8.1, 8.6, 8.9, 9.4, 9.2, 8.3, 8.2, 7.8, 8.1})
)

func normalized(p [12]float64) [12]float64 {
	sum := 0.0
	for _, v := range p {
		sum += v
	}
	for i := range p {
		p[i] /= sum
	}
	return p
}

// dayShare returns the share of the year of local day d: the month's share
// spread evenly over its days.
func dayShare(p [12]float64, d time.Time) float64 {
	days := time.Date(d.Year(), d.Month()+1, 0, 0, 0, 0, 0, d.Location()).Day()
	return p[d.Month()-1] / float64(days)
}
