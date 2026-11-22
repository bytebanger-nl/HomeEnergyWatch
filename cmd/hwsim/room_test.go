package main

import (
	"math"
	"testing"
	"time"
)

var testRoom = roomParams{start: 17, outdoor: 5, tau: 4 * time.Hour, heatTau: 45 * time.Minute,
	cvSupply: 26, acOffset: 1.3, speed: 1}

func near(t *testing.T, what string, got, want, tol float64) {
	t.Helper()
	if math.Abs(got-want) > tol {
		t.Errorf("%s = %.3f, want %.3f ± %.3f", what, got, want, tol)
	}
}

func TestRoomCoolsFirstOrder(t *testing.T) {
	got, cv, ac := roomStep(testRoom, roomInputs{}, 17, 4*time.Hour)
	// one time constant: 63 % of the way to the outdoor temperature
	near(t, "after tau", got, 5+12*math.Exp(-1), 0.01)
	if cv || ac {
		t.Errorf("heating without a source: cv %v ac %v", cv, ac)
	}
	got, _, _ = roomStep(testRoom, roomInputs{}, 17, 100*time.Hour)
	near(t, "long after", got, 5, 0.01)
}

func TestRoomCVHeatsToTargetAndHolds(t *testing.T) {
	in := roomInputs{cvOn: true, cvTarget: 19}
	got, cv, _ := roomStep(testRoom, in, 17, 10*time.Minute)
	if got <= 17 || got >= 19 || !cv {
		t.Fatalf("after 10 min: %.2f, heating %v; want between 17 and 19 and heating", got, cv)
	}
	// 17 -> 19 towards 26 with 45 min: 45·ln(9/7) ≈ 11.3 min
	got, cv, _ = roomStep(testRoom, in, 17, 3*time.Hour)
	near(t, "held at target", got, 19, 0.15)
	if !cv {
		t.Error("CV not heating while it holds its target")
	}
	// a room warmer than the target cools down to it and stays there
	_, cv, _ = roomStep(testRoom, in, 22, time.Minute)
	if cv {
		t.Error("CV heating in a room above its target")
	}
	got, _, _ = roomStep(testRoom, in, 22, 24*time.Hour)
	near(t, "cooled to target", got, 19, 0.001)
}

func TestRoomACHeatsBelowSetpoint(t *testing.T) {
	in := roomInputs{acHeat: true, acSet: 21}
	got, _, ac := roomStep(testRoom, in, 17, 45*time.Minute)
	near(t, "after heatTau", got, 19.7-2.7*math.Exp(-1), 0.01)
	if !ac {
		t.Error("air conditioner not heating below its goal")
	}
	got, _, _ = roomStep(testRoom, in, 17, 12*time.Hour)
	near(t, "settled", got, 21-1.3, 0.01)
	// switched off: the room cools again
	cooled, _, _ := roomStep(testRoom, roomInputs{}, got, time.Hour)
	if cooled >= got {
		t.Errorf("no cooling after switching off: %.2f -> %.2f", got, cooled)
	}
}

func TestRoomFasterSourceWins(t *testing.T) {
	both := roomInputs{cvOn: true, cvTarget: 18, acHeat: true, acSet: 22}
	got, _, _ := roomStep(testRoom, both, 17, 12*time.Hour)
	near(t, "air conditioner above the CV target", got, 22-1.3, 0.01)
}

func TestRoomSpeed(t *testing.T) {
	clock := time.Date(2026, 11, 11, 6, 0, 0, 0, time.UTC)
	p := testRoom
	p.speed = 30
	r := newRoom(p)
	r.now = func() time.Time { return clock }
	r.last = clock
	clock = clock.Add(8 * time.Minute) // 4 hours of room time
	near(t, "4 h of room time in 8 min", r.Temp(), 5+12*math.Exp(-1), 0.01)

	r.SetAC(true, 21)
	clock = clock.Add(24 * time.Minute) // 12 hours
	near(t, "air conditioner settled", r.Temp(), 19.7, 0.01)
	if _, ac := r.Heating(); !ac {
		t.Error("Heating does not report the air conditioner")
	}
}
