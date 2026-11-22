package mhi

import (
	"math"
	"testing"
)

func TestIndoorByteRoundTrip(t *testing.T) {
	for _, c := range []float64{15, 17.3, 19, 21.5, 23.2} {
		if got := indoorTempList[IndoorByte(c)]; math.Abs(got-c) > 0.3 {
			t.Errorf("IndoorByte(%v) gives %v", c, got)
		}
		if got := outdoorTempList[OutdoorByte(c)]; math.Abs(got-c) > 0.6 {
			t.Errorf("OutdoorByte(%v) gives %v", c, got)
		}
	}
}
