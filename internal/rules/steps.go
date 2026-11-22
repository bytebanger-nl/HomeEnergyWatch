package rules

// Wait and regulate actions: an automation whose actions contain them does
// not finish when it fires. It runs the actions up to the first wait or
// regulate action and then has a run that the engine advances every
// interval: a wait ends when its condition holds or its time is up, after
// which the next actions run; a regulate action keeps a measured value
// (tado, a thermometer) at a target by stepping the setpoint of a heating
// device (an air conditioner, an electric radiator) one step at a time.
//
// A run is stored (table rule_runs), so a restart goes on where it was. It
// does not depend on the rule's conditions any more: the conditions start
// it ("colder than 18.5 °C"), and it ends by itself (the wait), when the
// device is switched off, or with StopWindow when the rule's time window
// ends. Saving, switching off or deleting the rule ends it as well.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// offGrace is how long a regulated device may still read as off after the
// run started: the action before it switched it on, but the next poll may
// not have seen that yet.
const offGrace = 5 * time.Minute

// run is where an automation is in its actions: at a wait or a regulate
// action.
type run struct {
	Step       int        `json:"step"`  // index in Actions
	Since      time.Time  `json:"since"` // the step started
	NextCheck  time.Time  `json:"next_check,omitempty"`
	SeenOn     bool       `json:"seen_on,omitempty"`      // regulate: the device was seen switched on
	Setpoint   *float64   `json:"setpoint,omitempty"`     // regulate: the last value sent
	LastStep   string     `json:"last_step,omitempty"`    // regulate: "omlaag" or "omhoog"
	LastStepAt *time.Time `json:"last_step_at,omitempty"` // regulate
	Phase      string     `json:"phase,omitempty"`        // regulate with Boost: PhaseHeat, PhaseCool, PhaseRegulate
	Adjust     string     `json:"adjust,omitempty"`       // regulate: adjustLimit or adjustLand after a retune

	// seen at the last evaluation, for the view only
	sensor, current *float64
}

func newRun(step int, now time.Time) *run { return &run{Step: step, Since: now} }

// RunView is the wait or regulate action an automation is at.
type RunView struct {
	Step       int        `json:"step"` // number of the action, from 1
	Type       string     `json:"type"` // ActionWait or ActionRegulate
	Since      time.Time  `json:"since"`
	Until      *time.Time `json:"until,omitempty"`     // wait: when it ends at the latest
	Sensor     *float64   `json:"sensor,omitempty"`    // regulate: the measured value now
	Setpoint   *float64   `json:"setpoint,omitempty"`  // regulate: the device's setpoint now
	LastStep   string     `json:"last_step,omitempty"` // regulate: "omlaag" or "omhoog"
	LastStepAt *time.Time `json:"last_step_at,omitempty"`
	NextCheck  *time.Time `json:"next_check,omitempty"` // regulate
	Phase      string     `json:"phase,omitempty"`      // regulate with Boost
}

func (r *run) view(actions []Action) *RunView {
	if r.Step < 0 || r.Step >= len(actions) {
		return nil
	}
	a := actions[r.Step]
	v := &RunView{Step: r.Step + 1, Type: a.Type, Since: r.Since, Sensor: r.sensor, LastStep: r.LastStep, LastStepAt: r.LastStepAt}
	switch a.Type {
	case ActionWait:
		t := r.Since.Add(time.Duration(a.MaxSeconds) * time.Second)
		v.Until = &t
	case ActionRegulate:
		v.Setpoint, v.Phase = r.current, r.Phase
		if v.Setpoint == nil {
			v.Setpoint = r.Setpoint
		}
		if !r.NextCheck.IsZero() && r.Phase != PhaseHeat && r.Phase != PhaseCool {
			t := r.NextCheck
			v.NextCheck = &t
		}
	}
	return v
}

// nextStep is the index of the first wait or regulate action from i on, or
// -1.
func nextStep(actions []Action, i int) int {
	for ; i < len(actions); i++ {
		if actions[i].Type == ActionWait || actions[i].Type == ActionRegulate {
			return i
		}
	}
	return -1
}

// checkSteps checks where wait and regulate actions are: only in Actions,
// one regulate action at most and as the last, and StopWindow only with a
// time window among the conditions.
func checkSteps(r Rule) error {
	for _, a := range r.ElseActions {
		if a.Type == ActionWait || a.Type == ActionRegulate {
			return invalid("wachten en regelen kan alleen bij de acties, niet bij ‘als de voorwaarden niet meer gelden’")
		}
	}
	for i, a := range r.Actions {
		if a.Type != ActionRegulate {
			continue
		}
		if i != len(r.Actions)-1 {
			return invalid("regelen is altijd de laatste actie")
		}
		if a.StopWindow && !hasWindow(r) {
			return invalid("actie %d: stoppen aan het eind van het tijdvenster kan alleen met een tijdvenster bij de voorwaarden", i+1)
		}
	}
	return nil
}

func hasWindow(r Rule) bool {
	for _, c := range r.Conditions {
		if c.Device == TimeDevice && c.Value == 1 {
			return true
		}
	}
	return false
}

func normalizeWait(a Action) (Action, error) {
	if a.MaxSeconds < 60 || a.MaxSeconds > 24*3600 {
		return a, errors.New("wacht hooguit tussen 1 minuut en 24 uur")
	}
	out := Action{Type: ActionWait, MaxSeconds: a.MaxSeconds}
	if a.Until != nil {
		c := *a.Until
		if err := normalizeCondition(&c); err != nil {
			return a, fmt.Errorf("wachten tot: %v", err)
		}
		out.Until = &c
	}
	return out, nil
}

func normalizeRegulate(a Action) (Action, error) {
	switch {
	case !isPluginDevice(a.Device):
		return a, errors.New("kies het apparaat dat geregeld wordt")
	case !pluginMetricName.MatchString(a.Key):
		return a, errors.New("kies de temperatuur die geregeld wordt")
	case a.Sensor == nil:
		return a, errors.New("kies de meetwaarde waarop geregeld wordt")
	case a.Sensor.Device == TimeDevice || a.Sensor.Device == PriceDevice:
		return a, errors.New("regel op een meetwaarde van een apparaat")
	}
	if err := checkValue(a.Sensor.Device, a.Sensor.Metric); err != nil {
		return a, fmt.Errorf("meetwaarde: %v", err)
	}
	for _, f := range []float64{a.Target, a.Band, a.Step, a.Min, a.Max, a.Offset, a.Below, a.Above} {
		if !finite(f) {
			return a, errors.New("ongeldige waarde")
		}
	}
	if a.Below == 0 && a.Above == 0 {
		storedRange(&a) // only min and max given (an older rule, an API client)
	}
	if a.Below == 0 && a.Above == 0 {
		a.Below, a.Above = DefaultRange, DefaultRange
	}
	switch {
	case a.Offset < -10 || a.Offset > 10:
		return a, errors.New("de afwijking ligt tussen -10 en 10")
	case a.Below < -10 || a.Below > 10 || a.Above < -10 || a.Above > 10:
		return a, errors.New("het bereik onder en boven het doel ligt tussen -10 en 10")
	}
	a.Min, a.Max = round2(a.Target-a.Below), round2(a.Target+a.Above)
	switch {
	case a.Target < -50 || a.Target > 100:
		return a, errors.New("het doel ligt buiten -50 tot 100")
	case a.Band < 0.05 || a.Band > 5:
		return a, errors.New("de marge ligt tussen 0,05 en 5")
	case a.Step <= 0 || a.Step > 5:
		return a, errors.New("de stap ligt tussen 0 en 5")
	case a.EverySeconds < 60 || a.EverySeconds > 24*3600:
		return a, errors.New("controleer tussen elke minuut en elke 24 uur")
	case a.Min >= a.Max:
		return a, errors.New("het bereik onder of boven moet groter zijn dan 0")
	case a.OffAtEnd && !a.StopWindow:
		return a, errors.New("uitzetten aan het eind kan alleen als het regelen stopt met het tijdvenster")
	}
	return Action{Type: ActionRegulate, Device: a.Device, Key: a.Key,
		Sensor: &Operand{Device: a.Sensor.Device, Metric: a.Sensor.Metric},
		Target: a.Target, Band: a.Band, Step: a.Step, EverySeconds: a.EverySeconds,
		Offset: a.Offset, Below: a.Below, Above: a.Above, Min: a.Min, Max: a.Max, Boost: a.Boost,
		StopWindow: a.StopWindow, OffAtEnd: a.OffAtEnd, NotifySteps: a.NotifySteps}, nil
}

// DefaultRange is how far the setpoint may go below and above Target when
// a regulate action gives no range.
const DefaultRange = 2

// storedRange sets Below and Above from the stored Min and Max, so a loaded
// rule keeps exactly the limits it was saved with, whichever way they were
// once derived (fixed limits before there was a range; target + offset in
// 0.1.95). A rule from before there was an offset gets the offset closest
// to 0 within its limits, so that landing after a boost stays inside them.
func storedRange(a *Action) {
	if a.Min >= a.Max {
		return
	}
	legacy := a.Below == 0 && a.Above == 0 && a.Offset == 0
	a.Below, a.Above = round2(a.Target-a.Min), round2(a.Max-a.Target)
	if legacy {
		a.Offset = round2(math.Max(-a.Below, math.Min(a.Above, 0)))
	}
}

func round2(f float64) float64 { return math.Round(f*100) / 100 }

func isPluginDevice(device string) bool {
	_, ok := pluginID(device)
	return ok
}

// checkRegulateControl checks a regulate action against the device: one
// that heats by itself (not a thermostat such as tado, which regulates the
// central heating on its own), a setpoint, a step the device can take and
// limits within its range.
func checkRegulateControl(v plugin.DeviceView, ctl plugin.Control, a *Action) error {
	if !v.CanRegulate {
		return fmt.Errorf("regelen kan alleen bij een apparaat dat zelf verwarmt (airco, elektrische radiator); %s regelt zelf", v.Name)
	}
	if ctl.Kind != plugin.ControlSetpoint {
		return fmt.Errorf("kies een temperatuur van %s om te regelen", v.Name)
	}
	if ctl.Step > 0 {
		n := math.Round(a.Step / ctl.Step)
		if n < 1 || math.Abs(n*ctl.Step-a.Step) > 1e-6 {
			return fmt.Errorf("de stap moet een veelvoud zijn van %s", formatNumber(ctl.Step))
		}
		a.Step = n * ctl.Step
	}
	if ctl.Max > ctl.Min && (a.Min < ctl.Min-1e-9 || a.Max > ctl.Max+1e-9) {
		return fmt.Errorf("%s kan van %s tot %s", v.Name, formatNumber(ctl.Min), formatNumber(ctl.Max))
	}
	return nil
}

// powerControl is the on/off switch of a device: "power", else the switch
// labelled "Aan" (Tuya), else none.
func powerControl(d plugin.DeviceView) *plugin.Control {
	var found *plugin.Control
	for _, c := range d.Controls {
		if c.Kind != plugin.ControlSwitch || c.Metric == "" {
			continue
		}
		if c.Key == "power" {
			return &c
		}
		if found == nil && c.Label == "Aan" {
			found = &c
		}
	}
	return found
}

func controlByKey(d plugin.DeviceView, key string) *plugin.Control {
	for _, c := range d.Controls {
		if c.Key == key {
			return &c
		}
	}
	return nil
}

// inWindows says whether all time windows among the rule's conditions hold.
func inWindows(r Rule, snap *snapshot) bool {
	for _, c := range r.Conditions {
		if c.Device == TimeDevice && snap.check(c) != isTrue {
			return false
		}
	}
	return true
}

// Tune changes the target and band of a rule's regulate action. Unlike
// Save it keeps the rule's state and its run: regulating goes on with the
// new values from its next check.
func (e *Engine) Tune(ctx context.Context, id int64, target, band float64) (View, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range e.rules {
		if x.rule.ID != id {
			continue
		}
		r := x.rule
		r.Actions = append([]Action(nil), r.Actions...)
		i := len(r.Actions) - 1
		if i < 0 || r.Actions[i].Type != ActionRegulate {
			return View{}, invalid("deze automatisering regelt niets")
		}
		a := r.Actions[i]
		a.Target, a.Band = target, band
		n, err := normalizeRegulate(a)
		if err != nil {
			return View{}, invalid("%v", err)
		}
		r.Actions[i] = n
		return e.retuneLocked(ctx, x, r)
	}
	return View{}, store.ErrNotFound
}

// keepsRun says whether run, of rule cur, can go on in its changed version
// next: both are on and next has the same kind of step there, regulating
// the same setpoint on the same measured value.
func keepsRun(cur, next Rule, run *run) bool {
	i := run.Step
	if !cur.Enabled || !next.Enabled || i < 0 || i >= len(cur.Actions) || i >= len(next.Actions) {
		return false
	}
	a, b := cur.Actions[i], next.Actions[i]
	switch {
	case a.Type != b.Type || nextStep(next.Actions, i) != i:
		return false
	case a.Type == ActionWait:
		return true
	}
	return a.Device == b.Device && a.Key == b.Key && a.Sensor != nil && b.Sensor != nil &&
		a.Sensor.Device == b.Sensor.Device && a.Sensor.Metric == b.Sensor.Metric
}

// What a regulate run corrects at its next check after a retune (run.Adjust).
const (
	adjustLimit = "limit" // keep the setpoint within the (new) range, at the (new) boost limit
	adjustLand  = "land"  // boost was switched off while boosting: go to target + offset
)

// retuneLocked (with e.mu held) stores r, which differs from x.rule only in
// the settings of its regulate action, and keeps the rule's state and run.
// A regulate run goes on with the new settings: its next check corrects the
// setpoint for them (run.Adjust).
func (e *Engine) retuneLocked(ctx context.Context, x *entry, r Rule) (View, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return View{}, err
	}
	if err := e.Store.UpdateRuleData(ctx, r.ID, data); err != nil {
		return View{}, err
	}
	old := x.rule
	x.rule = r
	if x.run != nil && keepsRun(old, r, x.run) {
		e.retuneRun(ctx, r, x.run)
	}
	e.Log.Info("regel: regelen aangepast", "id", r.ID, "name", r.Name)
	return x.view(), nil
}

// retuneRun (with e.mu held) lets run go on with the settings of next, the
// changed version of its rule: its next check corrects the setpoint for them
// (run.Adjust). A run at a wait needs nothing.
func (e *Engine) retuneRun(ctx context.Context, next Rule, run *run) {
	a := next.Actions[run.Step]
	if a.Type != ActionRegulate {
		return
	}
	if !a.Boost && (run.Phase == PhaseHeat || run.Phase == PhaseCool) {
		// boost switched off while boosting: land on target + offset now
		run.Phase, run.Adjust = PhaseRegulate, adjustLand
	}
	if run.Adjust == "" {
		run.Adjust = adjustLimit
	}
	if data, err := json.Marshal(run); err == nil {
		if err := e.Store.SaveRuleRun(ctx, next.ID, data); err != nil {
			e.Log.Warn("regel: voortgang niet opgeslagen", "id", next.ID, "err", err)
		}
	}
}

// loadRuns gives the stored runs to their rules; a run that no longer fits
// its rule is dropped.
func (e *Engine) loadRuns(ctx context.Context, list []*entry) error {
	stored, err := e.Store.RuleRuns(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]*entry{}
	for _, x := range list {
		byID[x.rule.ID] = x
	}
	for id, data := range stored {
		var r run
		x := byID[id]
		ok := x != nil && json.Unmarshal(data, &r) == nil && r.Step >= 0 && r.Step < len(x.rule.Actions) &&
			nextStep(x.rule.Actions, r.Step) == r.Step && x.rule.Enabled
		if !ok {
			if err := e.Store.DeleteRuleRun(ctx, id); err != nil {
				return err
			}
			continue
		}
		if a := x.rule.Actions[r.Step]; a.Type == ActionRegulate && !a.Boost && r.Phase == "" {
			// regulating since before a start without boost set
			// Target + Offset: it goes on from where it is
			r.Phase = PhaseRegulate
		}
		x.run = &r
		// a run from before periods were recorded gets one from its start
		e.markRegulating(ctx, x.rule, &r)
	}
	return nil
}

// saveRun stores r if it is still the run of its rule.
func (e *Engine) saveRun(ctx context.Context, id int64, r *run) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range e.rules {
		if x.rule.ID != id || x.run != r {
			continue
		}
		data, err := json.Marshal(r)
		if err == nil {
			err = e.Store.SaveRuleRun(ctx, id, data)
		}
		if err != nil {
			e.Log.Warn("regel: voortgang niet opgeslagen", "id", id, "err", err)
		}
		e.markRegulating(ctx, x.rule, r)
	}
}

// markRegulating records that rule regulates since its run reached the
// regulate action (no-op while it waits, or when a period is open already).
// A period ends with the run (Store.DeleteRuleRun).
func (e *Engine) markRegulating(ctx context.Context, rule Rule, r *run) {
	if r.Step < 0 || r.Step >= len(rule.Actions) || rule.Actions[r.Step].Type != ActionRegulate {
		return
	}
	if err := e.Store.StartRegulatePeriod(ctx, rule.ID, r.Since); err != nil {
		e.Log.Warn("regel: regelperiode niet opgeslagen", "id", rule.ID, "err", err)
	}
}

// RegulatePeriods returns when automation id regulated within [from, to).
func (e *Engine) RegulatePeriods(ctx context.Context, id int64, from, to time.Time) ([]store.Period, error) {
	return e.Store.RegulatePeriods(ctx, id, from, to)
}

// LastRegulated returns, per automation that ever regulated, when it last
// did.
func (e *Engine) LastRegulated(ctx context.Context) (map[int64]time.Time, error) {
	return e.Store.LastRegulated(ctx)
}

// dropRun ends the run of a rule in the store (the entry is already
// cleared).
func (e *Engine) dropRun(ctx context.Context, id int64) {
	if err := e.Store.DeleteRuleRun(ctx, id); err != nil {
		e.Log.Warn("regel: voortgang niet verwijderd", "id", id, "err", err)
	}
}

// job is what a run needs sent: the actions after a wait, or a new setpoint.
type job struct {
	view    View
	token   *run     // the run the job belongs to (nil: it ended)
	actions []Action // sent in order
	prefix  string   // before the result
	body    string   // the push message's first line (default: "Verder na het wachten.")
	quiet   bool     // leaves the rule's result as it is (a message about a stop)
}

// advanceRuns moves every run on: ends a wait, steps a setpoint, or stops.
func (e *Engine) advanceRuns(ctx context.Context, snap *snapshot, now time.Time) {
	var jobs []job
	var saves []*run
	var saveIDs, drops, offs []int64
	// a manual action whose run ended by itself (its wait was the last, or
	// regulating stopped: the device was switched off by hand) is switched
	// off without its ElseActions (armed again: they would undo nothing that
	// still runs), so it does not show as on while nothing happens
	selfOff := func(x *entry) {
		if x.rule.Manual && x.active {
			x.active, x.armed = false, true
			offs = append(offs, x.rule.ID)
		}
	}
	e.mu.Lock()
	for _, x := range e.rules {
		r := x.run
		if r == nil || x.busy {
			continue
		}
		if !x.rule.Enabled || r.Step >= len(x.rule.Actions) {
			x.run = nil
			drops = append(drops, x.rule.ID)
			continue
		}
		a := x.rule.Actions[r.Step]
		switch a.Type {
		case ActionWait:
			reason := ""
			switch {
			case a.Until != nil && snap.check(*a.Until) == isTrue:
				reason = "Wachten klaar (" + describe(Rule{Conditions: []Condition{*a.Until}}, snap) + ")"
			case now.Sub(r.Since) >= time.Duration(a.MaxSeconds)*time.Second:
				reason = "Wachten klaar na " + formatDuration(time.Duration(a.MaxSeconds)*time.Second)
			}
			if reason == "" {
				continue
			}
			end := len(x.rule.Actions)
			next := nextStep(x.rule.Actions, r.Step+1)
			if next >= 0 {
				end = next
			}
			j := job{actions: x.rule.Actions[r.Step+1 : end], prefix: reason + ": "}
			if next >= 0 {
				r.Step, r.Since, r.NextCheck = next, now, time.Time{}
				j.token = r
				saves, saveIDs = append(saves, r), append(saveIDs, x.rule.ID)
			} else {
				x.run = nil
				drops = append(drops, x.rule.ID)
				selfOff(x)
			}
			x.busy = true
			j.view = x.view()
			jobs = append(jobs, j)
			e.Log.Info("regel: wachten klaar", "id", x.rule.ID, "name", x.rule.Name)
		case ActionRegulate:
			j, save, stop := e.regulate(x, a, r, snap, now)
			if stop != "" {
				x.run, x.result = nil, stop
				drops = append(drops, x.rule.ID)
				selfOff(x)
				e.Log.Info("regel: regelen gestopt", "id", x.rule.ID, "name", x.rule.Name, "reason", stop)
				if j == nil && a.NotifySteps {
					j = &job{actions: []Action{{Type: ActionNotify}}, body: stopPush(stop), quiet: true}
				}
			} else if save {
				saves, saveIDs = append(saves, r), append(saveIDs, x.rule.ID)
			}
			if j != nil {
				x.busy = true
				j.view = x.view()
				jobs = append(jobs, *j)
			}
		}
	}
	e.mu.Unlock()

	for _, id := range drops {
		e.dropRun(ctx, id)
	}
	for i, r := range saves {
		e.saveRun(ctx, saveIDs[i], r)
	}
	for _, id := range offs {
		if err := e.storeState(ctx, id); err != nil {
			e.Log.Warn("regelstatus niet opgeslagen", "id", id, "err", err)
		}
	}
	for _, j := range jobs {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			result := ""
			if len(j.actions) > 0 {
				body := j.body
				if body == "" {
					body = "Verder na het wachten."
				}
				result = e.send(ctx, j.view, snap, j.actions, body)
			}
			e.mu.Lock()
			for _, x := range e.rules {
				if x.rule.ID != j.view.ID {
					continue
				}
				if x.run == j.token || x.run == nil {
					x.busy = false
				}
				if j.quiet {
					// keep the result
				} else if result != "" {
					x.result = j.prefix + result
				} else if j.prefix != "" {
					x.result = j.prefix + "regelen begint."
				}
			}
			e.mu.Unlock()
		}()
	}
}

// regulate does one evaluation of a regulate action (with e.mu held): a job
// when a setpoint must be sent or the device switched off, whether the run
// changed (save), and why it stopped (stop, empty while it goes on).
func (e *Engine) regulate(x *entry, a Action, r *run, snap *snapshot, now time.Time) (j *job, save bool, stop string) {
	id, _ := pluginID(a.Device)
	d, known := snap.plugins[id]
	var ctl, power *plugin.Control
	if known {
		ctl, power = controlByKey(d, a.Key), powerControl(d)
	}
	r.sensor, r.current = nil, nil
	if v, ok := snap.value(a.Sensor.Device, a.Sensor.Metric); ok {
		r.sensor = &v
	}
	if ctl != nil && ctl.Metric != "" {
		if v, ok := snap.value(a.Device, ctl.Metric); ok {
			r.current = &v
		}
	}
	name := deviceName(snap, a.Device)

	// switched off (by hand, by another rule): stop
	if power != nil {
		if on, ok := snap.value(a.Device, power.Metric); ok {
			switch {
			case on >= 0.5 && !r.SeenOn:
				r.SeenOn, save = true, true
			case on < 0.5 && (r.SeenOn || now.Sub(r.Since) >= offGrace):
				return nil, false, fmt.Sprintf("Regelen gestopt: %s staat uit.", name)
			}
		}
	}
	// the time window ended: stop, and switch off when asked
	if a.StopWindow && !inWindows(x.rule, snap) {
		stop = "Regelen gestopt: het tijdvenster is voorbij."
		if a.OffAtEnd && power != nil {
			off := Action{Type: ActionPluginSet, Device: a.Device, Key: power.Key, Value: false}
			j := &job{actions: []Action{off}, prefix: "Tijdvenster voorbij: "}
			if a.NotifySteps {
				j.actions, j.body = append(j.actions, Action{Type: ActionNotify}), stopPush(stop)
			}
			return j, false, stop
		}
		return nil, false, stop
	}

	every := time.Duration(a.EverySeconds) * time.Second
	base := r.current
	if base == nil {
		base = r.Setpoint
	}

	// the settings changed while regulating (retune): correct the setpoint
	// for them once
	if r.Adjust != "" {
		if ctl == nil || base == nil {
			return nil, save, "" // unknown now: try again next time
		}
		want := *base
		switch {
		case r.Adjust == adjustLand:
			want = a.Target + a.Offset
		case a.Boost && r.Phase == PhaseHeat:
			want = a.Max
		case a.Boost && r.Phase == PhaseCool:
			want = a.Min
		}
		land := r.Adjust == adjustLand
		r.Adjust, save = "", true
		if j := e.setpoint(x, a, r, ctl, snap, now, base, want, "aangepast", name); j != nil {
			switch {
			case land:
				// as when boost lands by itself: steps after an interval
				r.NextCheck = now.Add(every)
			case r.Phase != PhaseHeat && r.Phase != PhaseCool:
				// a step that is due waits one minute, until the device
				// has reported the new setpoint, not a whole interval
				if soon := now.Add(min(every, time.Minute)); r.NextCheck.Before(soon) {
					r.NextCheck = soon
				}
			}
			return j, true, ""
		}
	}

	// without boost: start at Target + Offset, then step from there (at
	// that value already: the first step comes as usual)
	if !a.Boost && r.Phase == "" {
		if ctl == nil {
			return nil, save, "" // unknown now: try again next time
		}
		r.Phase, save = PhaseRegulate, true
		if j := e.setpoint(x, a, r, ctl, snap, now, base, a.Target+a.Offset, "start", name); j != nil {
			r.NextCheck = now.Add(every)
			return j, true, ""
		}
	}

	// boost: first all the way up (or down), then land at Target + Offset
	if a.Boost && r.Phase != PhaseRegulate {
		if r.sensor == nil || ctl == nil {
			return nil, save, "" // unknown now: try again next time
		}
		sensor := *r.sensor
		switch r.Phase {
		case "":
			switch {
			case sensor < a.Target-a.Band-1e-9:
				r.Phase = PhaseHeat
				return e.setpoint(x, a, r, ctl, snap, now, base, a.Max, "opwarmen", name), true, ""
			case sensor > a.Target+a.Band+1e-9:
				r.Phase = PhaseCool
				return e.setpoint(x, a, r, ctl, snap, now, base, a.Min, "afkoelen", name), true, ""
			}
		case PhaseHeat:
			if sensor < a.Target-a.Band-1e-9 {
				return nil, save, ""
			}
		case PhaseCool:
			if sensor > a.Target+a.Band+1e-9 {
				return nil, save, ""
			}
		}
		// within the band (or never outside it): regulate from here on
		landing := r.Phase != ""
		r.Phase, r.NextCheck = PhaseRegulate, now.Add(every)
		if !landing {
			return nil, true, ""
		}
		return e.setpoint(x, a, r, ctl, snap, now, base, a.Target+a.Offset, "terug", name), true, ""
	}

	if r.NextCheck.IsZero() {
		r.NextCheck, save = r.Since.Add(every), true
	}
	if now.Before(r.NextCheck) {
		return nil, save, ""
	}
	r.NextCheck, save = now.Add(every), true
	if r.sensor == nil || base == nil || ctl == nil {
		return nil, save, "" // unknown now: try again next time
	}
	sensor, value := *r.sensor, *base
	dir := ""
	switch {
	case sensor > a.Target+a.Band+1e-9:
		value, dir = value-a.Step, "omlaag"
	case sensor < a.Target-a.Band-1e-9:
		value, dir = value+a.Step, "omhoog"
	default:
		return nil, save, ""
	}
	value = limitSetpoint(a, ctl, value)
	if math.Abs(value-*base) < 1e-9 {
		limit := "minimum"
		if dir == "omhoog" {
			limit = "maximum"
		}
		x.result = fmt.Sprintf("Regelen: %s, maar %s staat al op het %s (%s).", measured(snap, a, sensor), name, limit, formatValue("", value, ctl.Unit))
		return nil, save, ""
	}
	return e.setpoint(x, a, r, ctl, snap, now, base, value, dir, name), true, ""
}

// Phases of a regulate run; none ("") until its first check, which starts
// it: with Boost at Max or Min (PhaseHeat, PhaseCool) when the measured
// value is outside the band, without at Target + Offset.
const (
	PhaseHeat     = "opwarmen" // the setpoint is at Max until the room is warm enough
	PhaseCool     = "afkoelen" // at Min until it is cool enough
	PhaseRegulate = "regelen"  // stepping as usual
)

// limitSetpoint rounds value to the control's step and keeps it within
// the action's range and the device's.
func limitSetpoint(a Action, ctl *plugin.Control, value float64) float64 {
	if ctl.Step > 0 {
		value = math.Round(value/ctl.Step) * ctl.Step
	}
	value = math.Max(a.Min, math.Min(a.Max, value))
	if ctl.Max > ctl.Min {
		value = math.Max(ctl.Min, math.Min(ctl.Max, value))
	}
	return value
}

func measured(snap *snapshot, a Action, sensor float64) string {
	sensorName, unit := valueName(snap, a.Sensor.Device, a.Sensor.Metric)
	return sensorName + " " + formatValue(a.Sensor.Metric, sensor, unit)
}

// setpoint is the job that sends value (limited) as the new setpoint, dir
// saying why ("start", "omhoog", "omlaag", "opwarmen", "afkoelen", "terug"); nil
// when the device is at that value already.
func (e *Engine) setpoint(x *entry, a Action, r *run, ctl *plugin.Control, snap *snapshot, now time.Time, base *float64, value float64, dir, name string) *job {
	value = limitSetpoint(a, ctl, value)
	if base != nil && math.Abs(value-*base) < 1e-9 {
		return nil
	}
	from := "?"
	if base != nil {
		from = formatValue("", *base, ctl.Unit)
	}
	m := "?"
	if r.sensor != nil {
		m = measured(snap, a, *r.sensor)
	}
	r.Setpoint, r.LastStep = &value, dir
	t := now
	r.LastStepAt = &t
	e.Log.Info("regel: regelen", "id", x.rule.ID, "name", x.rule.Name, "sensor", m,
		"device", name, "from", from, "to", value, "dir", dir)
	set := Action{Type: ActionPluginSet, Device: a.Device, Key: a.Key, Value: value}
	j := &job{token: r, actions: []Action{set}, prefix: "Regelen (" + m + ", " + dir + "): "}
	if a.NotifySteps {
		j.actions = append(j.actions, Action{Type: ActionNotify})
		switch dir {
		case "opwarmen":
			j.body = "Snel opwarmen: " + name + " op " + formatValue("", value, ctl.Unit) + "."
		case "afkoelen":
			j.body = "Snel afkoelen: " + name + " op " + formatValue("", value, ctl.Unit) + "."
		case "start":
			j.body = "Start: " + name + " op " + formatValue("", value, ctl.Unit) + "."
		case "terug":
			j.body = "Doel bijna bereikt: " + name + " terug naar " + formatValue("", value, ctl.Unit) + "."
		default:
			j.body = "Bijgeregeld van " + from + " naar " + formatValue("", value, ctl.Unit) + "."
		}
	}
	return j
}

// stopPush is the push message of a stopped regulate step: "Gestopt: Airco
// staat uit." (the rule's status line keeps "Regelen gestopt: ...").
func stopPush(stop string) string {
	return strings.Replace(stop, "Regelen gestopt", "Gestopt", 1)
}
