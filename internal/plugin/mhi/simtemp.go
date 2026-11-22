package mhi

// IndoorByte and OutdoorByte return the thermistor byte whose temperature is
// closest to c: the reverse of the tables, for the simulator in hwsim.
func IndoorByte(c float64) byte  { return closestByte(&indoorTempList, c) }
func OutdoorByte(c float64) byte { return closestByte(&outdoorTempList, c) }

func closestByte(table *[256]float64, c float64) byte {
	best, diff := 0, -1.0
	for i, v := range table {
		d := v - c
		if d < 0 {
			d = -d
		}
		if diff < 0 || d < diff {
			best, diff = i, d
		}
	}
	return byte(best)
}
