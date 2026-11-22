package main

import (
	"fmt"
	"log"
	"math"
	"sync"
	"time"
)

// A simulated room for testing automations that combine tado and the MHI
// air conditioner: one room temperature that the fake tado reports as its
// measurement and the MHI unit as its indoor temperature.
//
// The model is first order. Without a heat source the room cools towards
// the outdoor temperature with time constant tau. The central heating (CV)
// heats while tado's target is above the room temperature, towards
// cvSupply with time constant heatTau, and holds the room at the target. The
// air conditioner, when on and heating, heats towards its setpoint minus
// acOffset (it measures at the ceiling, where it is warmer) with heatTau and
// holds that. When both heat, the faster one wins.
//
// Time runs speed times faster than the clock: with speed 30 an hour of the
// room passes in two minutes. The gateway's own timers (rule durations,
// wait steps, regulate intervals) run on the clock.

type roomParams struct {
	start    float64       // room temperature at the start
	outdoor  float64       // outdoor temperature
	tau      time.Duration // cooling time constant (room time)
	heatTau  time.Duration // heating time constant of CV and air conditioner (room time)
	cvSupply float64       // what the CV heats towards
	acOffset float64       // room = air conditioner setpoint - acOffset
	speed    float64       // room seconds per second
}

// roomInputs are the heat sources at a moment.
type roomInputs struct {
	cvOn     bool    // tado heats (setting ON with a temperature)
	cvTarget float64 // tado's target
	acHeat   bool    // the air conditioner is on and heating
	acSet    float64 // its setpoint
}

// roomStep advances the temperature t by dt of room time, in steps of at
// most 30 s so that the CV switches off close to its target. It also reports
// whether the CV and the air conditioner heat at the end: below their goal,
// or holding it.
func roomStep(p roomParams, in roomInputs, t float64, dt time.Duration) (temp float64, cvHeats, acHeats bool) {
	const sub = 30 * time.Second
	for dt > 0 {
		d := min(dt, sub)
		dt -= d
		cvHeats = in.cvOn && t < in.cvTarget
		acGoal := in.acSet - p.acOffset
		acHeats = in.acHeat && t < acGoal
		approach := func(goal float64, tau time.Duration) float64 {
			if tau <= 0 {
				return goal
			}
			return goal + (t-goal)*math.Exp(-d.Seconds()/tau.Seconds())
		}
		next := approach(p.outdoor, p.tau) // losses only
		if cvHeats {
			next = max(next, approach(p.cvSupply, p.heatTau))
		}
		if acHeats {
			next = max(next, approach(acGoal, p.heatTau))
		}
		// a source that is on holds its temperature against the losses
		if in.cvOn && t >= in.cvTarget {
			next = max(next, min(t, in.cvTarget))
		}
		if in.acHeat && t >= acGoal {
			next = max(next, min(t, acGoal))
		}
		t = next
	}
	// a source also heats while it holds its temperature against the losses
	cvHeats = in.cvOn && t <= in.cvTarget+0.005
	acHeats = in.acHeat && t <= in.acSet-p.acOffset+0.005
	return t, cvHeats, acHeats
}

// room is the shared state: the temperature, the inputs set by the fake tado
// and the MHI unit, and the time it was last advanced. It never calls into
// those, so they may call it while holding their own locks.
type room struct {
	mu      sync.Mutex
	p       roomParams
	temp    float64
	in      roomInputs
	cvHeats bool
	acHeats bool
	last    time.Time     // clock time of the last advance
	elapsed time.Duration // room time since the start
	now     func() time.Time
}

func newRoom(p roomParams) *room {
	if p.speed <= 0 {
		p.speed = 1
	}
	r := &room{p: p, temp: p.start, now: time.Now}
	r.last = r.now()
	return r
}

// advanceLocked brings the room up to the clock. r.mu must be held.
func (r *room) advanceLocked() {
	now := r.now()
	real := now.Sub(r.last)
	if real <= 0 {
		return
	}
	r.last = now
	dt := time.Duration(float64(real) * r.p.speed)
	r.elapsed += dt
	r.temp, r.cvHeats, r.acHeats = roomStep(r.p, r.in, r.temp, dt)
}

// Temp is the room temperature now.
func (r *room) Temp() float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked()
	return r.temp
}

// Outdoor is the outdoor temperature.
func (r *room) Outdoor() float64 { return r.p.outdoor }

// SetCV is called by the fake tado when its setting changes.
func (r *room) SetCV(on bool, target float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked()
	r.in.cvOn, r.in.cvTarget = on, target
}

// SetAC is called by the MHI unit when its state changes.
func (r *room) SetAC(heat bool, set float64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked()
	r.in.acHeat, r.in.acSet = heat, set
}

// Heating reports whether the CV and the air conditioner heat now.
func (r *room) Heating() (cv, ac bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked()
	return r.cvHeats, r.acHeats
}

// String is one log line of the room's state.
func (r *room) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.advanceLocked()
	cv := "uit"
	if r.in.cvOn {
		cv = fmt.Sprintf("doel %.1f", r.in.cvTarget)
		if r.cvHeats {
			cv += " (verwarmt)"
		}
	}
	ac := "uit"
	if r.in.acHeat {
		ac = fmt.Sprintf("verwarmen %.1f", r.in.acSet)
		if r.acHeats {
			ac += " (verwarmt)"
		}
	}
	h := int(r.elapsed.Minutes())
	return fmt.Sprintf("kamer %5.2f °C  kamertijd +%d:%02d  cv %s  airco %s", r.temp, h/60, h%60, cv, ac)
}

// logEvery writes the room's state to the log at an interval of clock time.
func (r *room) logEvery(d time.Duration) {
	for range time.Tick(d) {
		log.Printf("ROOM     %s", r)
	}
}
