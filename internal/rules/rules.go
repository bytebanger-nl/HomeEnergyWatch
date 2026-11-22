// Package rules evaluates rules against the live readings. There are two
// kinds: an alert only sends a push message, an automation switches Energy
// Sockets and sets plugin devices (and may send a message too). When the
// conditions of a rule (all of them, or any with Match "any") have held for
// its duration, its actions run. A rule fires once and is armed again only
// after its conditions were seen to be false; an automation then runs its
// ElseActions, so it can undo what it did (or pick the other option, e.g.
// heat with the central heating instead of the air conditioner).
//
// A new or changed alert starts unarmed: a dryer that is already below 5 W
// does not alert until it has run. A new or changed automation starts armed,
// so it acts on the situation of the moment.
//
// A condition compares a value (times Scale) with a number, or with another
// value times its own scale plus that number (Ref), e.g. "0.5 m³ × the gas
// price > 0.8 kWh × the electricity price". Values are reading values,
// today's usage (today_*), plugin metrics (device "plugin:<id>"), prices
// (device "price") and time windows (device "time").
package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/control"
	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// PluginPrefix marks a plugin device in a condition or action:
// "plugin:<plugin device id>".
const PluginPrefix = "plugin:"

func pluginID(device string) (string, bool) {
	id, ok := strings.CutPrefix(device, PluginPrefix)
	return id, ok && id != ""
}

// PriceDevice is the pseudo device of a condition on the electricity price;
// its only metric is MetricPrice.
const PriceDevice = "price"

// TimeDevice is the pseudo device of a time window: weekdays and a from/to
// time of day in the gateway's time zone. Its only metric is MetricWindow,
// 1 inside the window and 0 outside it.
const TimeDevice = "time"

// Metrics that are not reading values.
const (
	MetricPrice   = "price"    // current day-ahead price, EUR/kWh excl. VAT
	MetricOnline  = "online"   // 1 when the device is online, else 0
	MetricPowerOn = "power_on" // 1 when an Energy Socket is switched on
	MetricWindow  = "window"   // time: 1 inside the window, else 0
)

// Metrics of the price device besides MetricPrice. All-in prices follow the
// energy contract of the day (markup, energy tax, VAT).
const (
	MetricPriceAllIn    = "price_all_in"        // electricity, EUR/kWh all-in
	MetricGasPrice      = "gas_price"           // gas day price, EUR/m³ excl. VAT
	MetricGasPriceAllIn = "gas_price_all_in"    // gas, EUR/m³ all-in
	MetricPriceAvgToday = "price_avg_today"     // average electricity price of today, EUR/kWh excl. VAT
	MetricCheaperHours  = "price_cheaper_hours" // hours of today with a lower electricity price than now
)

// PriceMetrics are the metrics of the price device.
var PriceMetrics = []string{MetricPrice, MetricPriceAllIn, MetricGasPrice, MetricGasPriceAllIn, MetricPriceAvgToday, MetricCheaperHours}

// TodayPrefix marks today's usage of a HomeWizard device: "today_" plus a
// counter (energy_import_kwh, energy_export_kwh, gas_m3, water_m3) or
// "today_net_kwh" (import minus export).
const TodayPrefix = "today_"

// Operators of a condition.
const (
	OpBelow        = "<"
	OpBelowOrEqual = "<="
	OpAbove        = ">"
	OpAboveOrEqual = ">="
	OpEqual        = "="
	OpNotEqual     = "!="
)

// Kinds of rules.
const (
	KindAlert      = "alert"      // only sends a push message
	KindAutomation = "automation" // switches and sets devices
)

// Match says how the conditions combine.
const (
	MatchAll = "all" // all conditions (default)
	MatchAny = "any" // at least one
)

// Action types.
const (
	ActionNotify    = "notify"
	ActionSwitch    = "switch"
	ActionPluginSet = "plugin_set" // set a control of a plugin device
	// ActionWait pauses the actions of an automation until a condition
	// holds or MaxSeconds have passed; the actions after it run then.
	ActionWait = "wait"
	// ActionRegulate keeps a measured value at a target by stepping the
	// setpoint of a heating device (air conditioner, electric radiator);
	// always the last action. See steps.go.
	ActionRegulate = "regulate"
)

// Condition compares one value of one device, times Scale, with Value, or
// with Ref plus Value when Ref is set.
type Condition struct {
	Device string  `json:"device"`
	Metric string  `json:"metric"`
	Op     string  `json:"op"`
	Value  float64 `json:"value"`
	// Scale multiplies the value of the device; 0 means 1.
	Scale float64 `json:"scale,omitempty"`
	// Ref is a second value to compare with (times its scale, plus Value).
	Ref *Operand `json:"ref,omitempty"`
	// Text, when set, compares a text metric of a plugin device (e.g. the
	// mode "Verwarmen") instead of Value; only with "=" or "!=".
	Text string `json:"text,omitempty"`
	// Time window (device "time"): ISO weekdays (1 = Monday ... 7 = Sunday)
	// and "HH:MM" times; To before From runs past midnight into the next
	// day, From equal to To is the whole day.
	Days []int  `json:"days,omitempty"`
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// Operand is the second value of a comparison: a value of a device (as in a
// condition) times Scale (0 means 1).
type Operand struct {
	Device string  `json:"device"`
	Metric string  `json:"metric"`
	Scale  float64 `json:"scale,omitempty"`
}

func scale(f float64) float64 {
	if f == 0 {
		return 1
	}
	return f
}

// Action is run when a rule fires.
type Action struct {
	Type   string `json:"type"`
	Device string `json:"device,omitempty"` // switch: the socket; plugin_set: "plugin:<id>"
	On     bool   `json:"on,omitempty"`     // switch: on (true) or off
	Key    string `json:"key,omitempty"`    // plugin_set: the control
	Value  any    `json:"value,omitempty"`  // plugin_set: its value (bool, number, text; none for an action)

	// wait: Until (optional) ends the wait early; MaxSeconds always ends it.
	Until      *Condition `json:"until,omitempty"`
	MaxSeconds int        `json:"max_s,omitempty"`

	// regulate: Device and Key are the setpoint that is stepped, Sensor the
	// measured value that should stay within Target ± Band. Every
	// EverySeconds one Step down (above the band) or up (below it), within
	// Min and Max. The setpoint that usually keeps the room at its target
	// is Target + Offset (an air conditioner measures near the ceiling);
	// Min and Max are Target - Below and Target + Above: they move with the
	// target, not with the offset. Without Boost it starts at Target +
	// Offset; Boost first sets Max (room too cold) or Min (too warm) and
	// lands on Target + Offset once the room is within Band of the target.
	// It stops when the device is switched off, and with StopWindow when
	// the rule's time window ends (OffAtEnd then switches the device off).
	// NotifySteps sends a push message at every step and when it stops (to
	// check how it regulates).
	Sensor       *Operand `json:"sensor,omitempty"`
	Target       float64  `json:"target,omitempty"`
	Band         float64  `json:"band,omitempty"`
	Step         float64  `json:"step,omitempty"`
	EverySeconds int      `json:"every_s,omitempty"`
	Offset       float64  `json:"offset,omitempty"`
	Below        float64  `json:"below,omitempty"`
	Above        float64  `json:"above,omitempty"`
	Min          float64  `json:"min,omitempty"` // Target - Below (set by normalizeRegulate)
	Max          float64  `json:"max,omitempty"` // Target + Above
	Boost        bool     `json:"boost,omitempty"`
	StopWindow   bool     `json:"stop_window,omitempty"`
	OffAtEnd     bool     `json:"off_at_end,omitempty"`
	NotifySteps  bool     `json:"notify_steps,omitempty"`
}

// Rule fires when its conditions hold for ForSeconds.
type Rule struct {
	ID         int64       `json:"id"`
	Kind       string      `json:"kind"` // KindAlert or KindAutomation; derived from the actions when empty
	Name       string      `json:"name"`
	Enabled    bool        `json:"enabled"`
	Match      string      `json:"match,omitempty"` // MatchAll (default) or MatchAny
	Conditions []Condition `json:"conditions"`
	ForSeconds int         `json:"for_s"`
	Message    string      `json:"message,omitempty"` // push text; generated when empty
	Actions    []Action    `json:"actions"`
	// ElseActions (automations) run once when the conditions stop holding
	// after the rule fired.
	ElseActions []Action `json:"else_actions,omitempty"`
	// Manual (automations): a manual action, without conditions, started
	// and stopped by hand (Engine.Start, Stop; on the dashboard). Switched
	// on counts as its conditions holding, switched off as not holding, so
	// it runs its actions, waits, regulates and runs its ElseActions like
	// any automation.
	Manual bool `json:"manual,omitempty"`
}

// momentary says whether a manual action is a button rather than a switch:
// it has nothing that runs on (no wait or regulate action) and nothing to do
// when switched off, so it switches itself off once its actions ran.
func momentary(r Rule) bool {
	return r.Manual && len(r.ElseActions) == 0 && nextStep(r.Actions, 0) < 0
}

// kindOf is the kind of a rule stored before rules had one: an automation
// when it switches or sets a device.
func kindOf(r Rule) string {
	if r.Kind != "" {
		return r.Kind
	}
	for _, a := range append(append([]Action(nil), r.Actions...), r.ElseActions...) {
		if a.Type != ActionNotify {
			return KindAutomation
		}
	}
	return KindAlert
}

// State is the live state of a rule.
type State struct {
	Armed bool `json:"armed"`
	// Active: a manual action is switched on; Momentary: it is a button
	// (see momentary).
	Active      bool       `json:"active"`
	Momentary   bool       `json:"momentary,omitempty"`
	ActiveSince *time.Time `json:"active_since,omitempty"` // all conditions hold since
	LastFired   *time.Time `json:"last_fired,omitempty"`   // since the gateway started
	LastResult  string     `json:"last_result,omitempty"`
	// Run is the wait or regulate action an automation is at, if any.
	Run *RunView `json:"run,omitempty"`
}

// View is a rule with its state, as served by the API.
type View struct {
	Rule
	State State `json:"state"`
}

// ErrInvalid wraps validation errors (shown to the user, Dutch).
var ErrInvalid = errors.New("ongeldige regel")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

var (
	metricName       = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	pluginMetricName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

func (r *Rule) normalize() error {
	r.Name = strings.TrimSpace(r.Name)
	r.Message = strings.TrimSpace(r.Message)
	r.Kind = kindOf(*r)
	if r.Match == "" {
		r.Match = MatchAll
	}
	if r.Manual {
		if r.Kind != KindAutomation {
			return invalid("alleen een automatisering kan handmatig starten")
		}
		if len(r.Conditions) > 0 {
			return invalid("een handmatige actie heeft geen voorwaarden")
		}
		r.Conditions, r.ForSeconds, r.Match = []Condition{}, 0, MatchAll
	}
	switch {
	case r.Kind != KindAlert && r.Kind != KindAutomation:
		return invalid("onbekende soort regel %q", r.Kind)
	case r.Match != MatchAll && r.Match != MatchAny:
		return invalid("kies of alle voorwaarden of één ervan moeten gelden")
	case r.Name == "":
		return invalid("geef de regel een naam")
	case len(r.Name) > 80:
		return invalid("de naam is te lang")
	case len(r.Message) > 300:
		return invalid("de meldingstekst is te lang")
	case len(r.Conditions) == 0 && !r.Manual:
		return invalid("voeg minstens één voorwaarde toe")
	case len(r.Conditions) > 10:
		return invalid("hooguit 10 voorwaarden")
	case len(r.Actions) > 10 || len(r.ElseActions) > 10:
		return invalid("hooguit 10 acties")
	case r.ForSeconds < 0 || r.ForSeconds > 7*24*3600:
		return invalid("de duur moet tussen 0 en 7 dagen liggen")
	}
	for i := range r.Conditions {
		if err := normalizeCondition(&r.Conditions[i]); err != nil {
			return invalid("voorwaarde %d: %v", i+1, err)
		}
	}
	if r.Kind == KindAlert {
		// an alert only sends a message
		for _, a := range append(append([]Action(nil), r.Actions...), r.ElseActions...) {
			if a.Type != ActionNotify {
				return invalid("een melding bedient geen apparaten; maak daarvoor een automatisering")
			}
		}
		r.Actions, r.ElseActions = []Action{{Type: ActionNotify}}, nil
		return nil
	}
	controls := 0
	for _, list := range []*[]Action{&r.Actions, &r.ElseActions} {
		for i, a := range *list {
			n, err := normalizeAction(a)
			if err != nil {
				return invalid("actie %d: %v", i+1, err)
			}
			if n.Type != ActionNotify && n.Type != ActionWait {
				controls++
			}
			(*list)[i] = n
		}
	}
	if controls == 0 {
		return invalid("een automatisering schakelt of bedient minstens één apparaat")
	}
	if err := checkSteps(*r); err != nil {
		return err
	}
	if r.Actions == nil {
		r.Actions = []Action{}
	}
	return nil
}

var ops = map[string]bool{OpBelow: true, OpBelowOrEqual: true, OpAbove: true, OpAboveOrEqual: true, OpEqual: true, OpNotEqual: true}

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// checkValue checks the device and metric of a condition or operand.
func checkValue(device, metric string) error {
	_, isPlugin := pluginID(device)
	switch {
	case device == "":
		return errors.New("kies een apparaat")
	case isPlugin && !pluginMetricName.MatchString(metric):
		return errors.New("kies een meetwaarde")
	case !isPlugin && !metricName.MatchString(metric):
		return errors.New("kies een meetwaarde")
	case device == PriceDevice && !slices.Contains(PriceMetrics, metric):
		return errors.New("kies een prijs")
	}
	return nil
}

func normalizeCondition(c *Condition) error {
	c.Text = strings.TrimSpace(c.Text)
	_, isPlugin := pluginID(c.Device)
	if c.Device == TimeDevice {
		if err := checkWindow(*c); err != nil {
			return err
		}
		*c = Condition{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: c.Value,
			Days: normDays(c.Days), From: c.From, To: c.To}
		return nil
	}
	if err := checkValue(c.Device, c.Metric); err != nil {
		return err
	}
	switch {
	case len(c.Days) > 0 || c.From != "" || c.To != "":
		return errors.New("dagen en tijden horen bij een tijdvenster")
	case !ops[c.Op]:
		return errors.New("kies een vergelijking")
	case !isPlugin && c.Text != "":
		return errors.New("tekst vergelijken kan alleen bij een plugin-apparaat")
	case c.Text != "" && ((c.Op != OpEqual && c.Op != OpNotEqual) || len(c.Text) > 64):
		return errors.New("tekst kan alleen gelijk of ongelijk aan zijn (hooguit 64 tekens)")
	case c.Text != "" && c.Ref != nil:
		return errors.New("tekst kan niet met een tweede waarde worden vergeleken")
	case !finite(c.Value) || !finite(c.Scale) || math.Abs(c.Scale) > 1e6:
		return errors.New("ongeldige waarde")
	}
	if c.Text != "" {
		c.Value, c.Scale = 0, 0
	}
	if c.Ref != nil {
		if c.Ref.Device == TimeDevice {
			return errors.New("een tijdvenster kan geen tweede waarde zijn")
		}
		if err := checkValue(c.Ref.Device, c.Ref.Metric); err != nil {
			return fmt.Errorf("tweede waarde: %v", err)
		}
		if !finite(c.Ref.Scale) || math.Abs(c.Ref.Scale) > 1e6 {
			return errors.New("tweede waarde: ongeldige factor")
		}
		c.Ref = &Operand{Device: c.Ref.Device, Metric: c.Ref.Metric, Scale: c.Ref.Scale}
	}
	return nil
}

func normalizeAction(a Action) (Action, error) {
	switch a.Type {
	case ActionNotify:
		return Action{Type: ActionNotify}, nil
	case ActionSwitch:
		if a.Device == "" {
			return a, errors.New("kies de socket die geschakeld wordt")
		}
		return Action{Type: ActionSwitch, Device: a.Device, On: a.On}, nil
	case ActionPluginSet:
		if _, ok := pluginID(a.Device); !ok {
			return a, errors.New("kies het apparaat dat bediend wordt")
		}
		if !pluginMetricName.MatchString(a.Key) {
			return a, errors.New("kies wat er ingesteld wordt")
		}
		switch a.Value.(type) {
		case nil, bool, float64, string:
		default:
			return a, errors.New("ongeldige waarde")
		}
		return Action{Type: ActionPluginSet, Device: a.Device, Key: a.Key, Value: a.Value}, nil
	case ActionWait:
		return normalizeWait(a)
	case ActionRegulate:
		return normalizeRegulate(a)
	}
	return a, fmt.Errorf("onbekend type %q", a.Type)
}

// Devices is what the engine needs from the gateway.
type Devices interface {
	Views() []gateway.DeviceView
	SetPower(ctx context.Context, id string, on bool) (gateway.DeviceView, error)
}

// Plugins is what the engine needs from the plugin manager.
type Plugins interface {
	DeviceViews() []plugin.DeviceView
	Set(ctx context.Context, deviceID, key string, value any) error
}

// Message is one alert.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	Tag   string `json:"tag"` // a newer alert of the same rule replaces the older one
	URL   string `json:"url"` // opened when the notification is tapped
}

// Engine holds the rules and evaluates them.
type Engine struct {
	Store   *store.Store
	Devices Devices
	// Plugins gives plugin devices to conditions and plugin_set actions;
	// optional.
	Plugins Plugins
	// Loc is the time zone of time windows; default UTC.
	Loc *time.Location
	// Price returns a metric of the price device (one of PriceMetrics) at
	// t; optional.
	Price func(ctx context.Context, metric string, t time.Time) (float64, bool)
	// Notify sends a message to every subscribed app; optional.
	Notify   func(ctx context.Context, m Message) error
	Log      *slog.Logger
	Interval time.Duration // between evaluations; default 2 s

	mu sync.Mutex
	// stateMu orders the writes of the armed and active flags (storeState)
	stateMu sync.Mutex
	rules   []*entry
	wg      sync.WaitGroup // running actions
	// Control sends the commands of switch and plugin_set actions
	// (checked, tried again); nil: one of its own over Devices and Plugins.
	Control *control.Service
	ctlOnce sync.Once
	// the times of the engine's own Control (tests make them short); 0 is
	// the package control default
	retryPause, verifyWait, verifyTick time.Duration
}

type entry struct {
	rule      Rule
	armed     bool
	since     time.Time
	lastFired time.Time
	result    string
	firing    bool
	// run is the wait or regulate action the automation is at (nil when
	// none runs); busy while actions of the run are being sent.
	run  *run
	busy bool
	// active: a manual action is switched on (Start, Stop)
	active bool
}

// startArmed says whether a newly saved rule may fire before its conditions
// were false: an automation acts on the situation of the moment.
func startArmed(r Rule) bool { return r.Kind == KindAutomation }

func (e *entry) view() View {
	v := View{Rule: e.rule, State: State{Armed: e.armed, LastResult: e.result,
		Active: e.active, Momentary: momentary(e.rule)}}
	v.Conditions = append([]Condition{}, e.rule.Conditions...) // [] for a manual action, not null
	v.Actions = append([]Action(nil), e.rule.Actions...)
	if !e.since.IsZero() {
		t := e.since
		v.State.ActiveSince = &t
	}
	if !e.lastFired.IsZero() {
		t := e.lastFired
		v.State.LastFired = &t
	}
	if e.run != nil {
		v.State.Run = e.run.view(e.rule.Actions)
	}
	return v
}

// Load reads the rules from the store.
func (e *Engine) Load(ctx context.Context) error {
	stored, err := e.Store.Rules(ctx)
	if err != nil {
		return err
	}
	list := make([]*entry, 0, len(stored))
	for _, s := range stored {
		var r Rule
		if err := json.Unmarshal(s.Data, &r); err != nil {
			e.Log.Warn("regel niet gelezen", "id", s.ID, "err", err)
			continue
		}
		r.ID = s.ID
		r.Kind = kindOf(r)
		for i := range r.Actions {
			if r.Actions[i].Type == ActionRegulate {
				storedRange(&r.Actions[i])
			}
		}
		if r.Match == "" {
			r.Match = MatchAll
		}
		// a button that was pressed when the gateway stopped is not pressed
		// again: its actions ran or the press is lost, and either way it
		// would stay on (it went off and cannot go off again)
		active := s.Active && r.Manual && r.Enabled && !momentary(r)
		list = append(list, &entry{rule: r, armed: s.Armed, active: active})
	}
	if err := e.loadRuns(ctx, list); err != nil {
		return err
	}
	e.mu.Lock()
	e.rules = list
	e.mu.Unlock()
	return nil
}

// List returns all rules with their state.
func (e *Engine) List() []View {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]View, len(e.rules))
	for i, r := range e.rules {
		out[i] = r.view()
	}
	return out
}

// Save validates and stores a rule (ID 0 adds one). A new alert starts
// unarmed: it fires only after its conditions have been false; a new
// automation starts armed (startArmed). A changed rule that already went off
// does not go off again while its conditions still hold, and what it is
// waiting for or regulating goes on with the new settings (see reset).
func (e *Engine) Save(ctx context.Context, r Rule) (View, error) {
	if err := r.normalize(); err != nil {
		return View{}, err
	}
	if err := e.checkPluginActions(r.Actions); err != nil {
		return View{}, err
	}
	if err := e.checkPluginActions(r.ElseActions); err != nil {
		return View{}, err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return View{}, err
	}
	id, err := e.Store.SaveRule(ctx, store.StoredRule{ID: r.ID, Data: data})
	if err != nil {
		return View{}, err
	}
	r.ID = id
	v, err := e.reset(ctx, r)
	if err != nil {
		return View{}, err
	}
	// also when false: SaveRule reset the flag, and Evaluate may have
	// stored the old rule's state since
	if err := e.storeState(ctx, id); err != nil {
		return View{}, err
	}
	return v, nil
}

// reset puts a saved rule in place. A new rule, one switched on or off, and
// one whose run no longer fits it (it regulates something else now) start
// over. Otherwise the rule keeps whether it went off (it does not go off
// again while its conditions hold) and its run: a wait goes on, regulating
// goes on with the new settings (retuneRun).
func (e *Engine) reset(ctx context.Context, r Rule) (View, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, x := range e.rules {
		if x.rule.ID != r.ID {
			continue
		}
		old := x.rule
		if old.Enabled && r.Enabled && old.Manual == r.Manual && (x.run == nil || keepsRun(old, r, x.run)) {
			x.rule, x.armed, x.since = r, x.armed && startArmed(r), time.Time{}
			if x.run != nil {
				e.retuneRun(ctx, r, x.run)
			}
			e.Log.Info("regel: aangepast, gaat verder", "id", r.ID, "name", r.Name)
			return x.view(), nil
		}
		break
	}
	// what it was waiting for or regulating no longer applies (deleted
	// under the lock, so a run being saved cannot bring it back)
	if err := e.Store.DeleteRuleRun(ctx, r.ID); err != nil {
		return View{}, err
	}
	armed := startArmed(r)
	for _, x := range e.rules {
		if x.rule.ID == r.ID {
			x.rule, x.armed, x.since, x.result, x.run, x.busy, x.active = r, armed, time.Time{}, "", nil, false, false
			return x.view(), nil
		}
	}
	x := &entry{rule: r, armed: armed}
	e.rules = append(e.rules, x)
	return x.view(), nil
}

// storeState stores whether rule id may fire and whether it is switched on
// (a manual action), as it is in memory now. Writers take turns (stateMu)
// and each writes the state of the moment, so the last write is always the
// latest state; the database call happens outside e.mu, so a busy database
// does not hold up the engine.
func (e *Engine) storeState(ctx context.Context, id int64) error {
	e.stateMu.Lock()
	defer e.stateMu.Unlock()
	e.mu.Lock()
	armed, active, found := false, false, false
	for _, x := range e.rules {
		if x.rule.ID == id {
			armed, active, found = x.armed, x.active, true
		}
	}
	e.mu.Unlock()
	if !found {
		return nil // deleted meanwhile
	}
	return e.Store.SetRuleState(ctx, id, armed, active)
}

// Delete removes a rule.
func (e *Engine) Delete(ctx context.Context, id int64) error {
	if err := e.Store.DeleteRule(ctx, id); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for i, x := range e.rules {
		if x.rule.ID == id {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			break
		}
	}
	return nil
}

// ErrNotManual: Start or Stop of a rule that is not a manual action.
var ErrNotManual = fmt.Errorf("%w: dit is geen handmatige actie", ErrInvalid)

// Start switches manual action id on: the next evaluation runs its actions
// (a button switches itself off again once they ran).
func (e *Engine) Start(ctx context.Context, id int64) (View, error) {
	return e.setActive(ctx, id, true)
}

// Stop switches manual action id off: the next evaluation runs its
// ElseActions, ends a wait and lets regulating stop as any automation does.
func (e *Engine) Stop(ctx context.Context, id int64) (View, error) {
	return e.setActive(ctx, id, false)
}

func (e *Engine) setActive(ctx context.Context, id int64, on bool) (View, error) {
	e.mu.Lock()
	var v View
	found := false
	for _, x := range e.rules {
		if x.rule.ID != id {
			continue
		}
		found = true
		switch {
		case !x.rule.Manual:
			e.mu.Unlock()
			return View{}, ErrNotManual
		case on && !x.rule.Enabled:
			e.mu.Unlock()
			return View{}, invalid("deze actie is uitgeschakeld")
		}
		x.active = on
		v = x.view()
	}
	e.mu.Unlock()
	if !found {
		return View{}, store.ErrNotFound
	}
	if err := e.storeState(ctx, id); err != nil {
		return View{}, err
	}
	if on {
		e.Log.Info("actie: aangezet", "id", id, "name", v.Name)
	} else {
		e.Log.Info("actie: uitgezet", "id", id, "name", v.Name)
	}
	return v, nil
}

// PricesNow returns the metrics of the price device now (those that are
// known), so the PWA can show them next to a condition.
func (e *Engine) PricesNow(ctx context.Context) map[string]float64 {
	out := map[string]float64{}
	if e.Price == nil {
		return out
	}
	now := time.Now()
	for _, m := range PriceMetrics {
		if v, ok := e.Price(ctx, m, now); ok {
			out[m] = v
		}
	}
	return out
}

// Run evaluates the rules every Interval until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	interval := e.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			e.wg.Wait()
			return
		case now := <-t.C:
			e.Evaluate(ctx, now)
		}
	}
}

// truth is the outcome of a condition: a value can be unknown (device
// offline, value missing), which neither fires nor arms a rule.
type truth int

const (
	unknown truth = iota
	isFalse
	isTrue
)

// checkPluginActions checks each plugin_set action against the device's
// controls: the device must allow control and the value must fit (a
// setpoint is rounded to its step here).
func (e *Engine) checkPluginActions(actions []Action) error {
	var views map[string]plugin.DeviceView
	for i, a := range actions {
		if a.Type != ActionPluginSet && a.Type != ActionRegulate {
			continue
		}
		if e.Plugins == nil {
			return invalid("actie %d: plugins zijn niet beschikbaar", i+1)
		}
		if views == nil {
			views = map[string]plugin.DeviceView{}
			for _, v := range e.Plugins.DeviceViews() {
				views[v.ID] = v
			}
		}
		id, _ := pluginID(a.Device)
		v, ok := views[id]
		if !ok {
			return invalid("actie %d: het plugin-apparaat bestaat niet (meer)", i+1)
		}
		if len(v.Controls) == 0 {
			return invalid("actie %d: bediening staat uit voor %s (Instellingen → Plugins)", i+1, v.Name)
		}
		var ctl *plugin.Control
		for _, c := range v.Controls {
			if c.Key == a.Key {
				ctl = &c
			}
		}
		if ctl == nil {
			return invalid("actie %d: %s heeft die bediening niet", i+1, v.Name)
		}
		if a.Type == ActionRegulate {
			if err := checkRegulateControl(v, *ctl, &actions[i]); err != nil {
				return invalid("actie %d: %v", i+1, err)
			}
			continue
		}
		val, err := plugin.CheckValue(*ctl, a.Value)
		if err != nil {
			return invalid("actie %d: %v", i+1, err)
		}
		actions[i].Value = val
	}
	return nil
}

// snapshot gives the values conditions are checked against.
type snapshot struct {
	devices map[string]gateway.DeviceView
	plugins map[string]plugin.DeviceView
	price   func(metric string) (float64, bool)
	now     time.Time // in the engine's time zone
}

// pluginMetric returns a metric of a plugin device; ok is false when the
// device is unknown or offline, or has no such metric now.
func (s *snapshot) pluginMetric(device, metric string) (plugin.Metric, bool) {
	id, _ := pluginID(device)
	d, ok := s.plugins[id]
	if !ok || d.Reading == nil || !d.Reading.Online {
		return plugin.Metric{}, false
	}
	for _, m := range d.Reading.Metrics {
		if m.Key == metric {
			return m, true
		}
	}
	return plugin.Metric{}, false
}

// value returns a value of a device; ok is false when it is unknown now.
func (s *snapshot) value(device, metric string) (float64, bool) {
	if device == PriceDevice {
		return s.price(metric)
	}
	if id, ok := pluginID(device); ok {
		if metric == MetricOnline {
			d, found := s.plugins[id]
			if !found {
				return 0, false
			}
			if d.Reading != nil && d.Reading.Online {
				return 1, true
			}
			return 0, true
		}
		m, ok := s.pluginMetric(device, metric)
		if !ok {
			return 0, false
		}
		return plugin.Number(m.Value)
	}
	d, ok := s.devices[device]
	if !ok {
		return 0, false
	}
	if metric == MetricOnline {
		if d.Status == gateway.StatusOnline {
			return 1, true
		}
		return 0, true
	}
	if d.Status != gateway.StatusOnline || d.Reading == nil {
		return 0, false
	}
	if metric == MetricPowerOn {
		if d.Reading.PowerOn == nil {
			return 0, false
		}
		if *d.Reading.PowerOn {
			return 1, true
		}
		return 0, true
	}
	if key, ok := strings.CutPrefix(metric, TodayPrefix); ok {
		if key == "net_kwh" {
			imp, ok := d.Today["energy_import_kwh"]
			if !ok {
				return 0, false
			}
			return imp - d.Today["energy_export_kwh"], true
		}
		v, ok := d.Today[key]
		return v, ok
	}
	v, ok := d.Reading.Values[metric]
	return v, ok
}

// sides returns the two sides of a numeric comparison: the device's value
// times its scale, and the threshold (Value, plus Ref times its scale).
func (s *snapshot) sides(c Condition) (left, right float64, ok bool) {
	if c.Device == TimeDevice {
		if inWindow(c, s.now) {
			return 1, c.Value, true
		}
		return 0, c.Value, true
	}
	v, ok := s.value(c.Device, c.Metric)
	if !ok {
		return 0, 0, false
	}
	left, right = v*scale(c.Scale), c.Value
	if c.Ref != nil {
		r, ok := s.value(c.Ref.Device, c.Ref.Metric)
		if !ok {
			return 0, 0, false
		}
		right += r * scale(c.Ref.Scale)
	}
	return left, right, true
}

func compare(op string, a, b float64) bool {
	const eps = 1e-9
	switch op {
	case OpBelow:
		return a < b
	case OpBelowOrEqual:
		return a <= b+eps
	case OpAbove:
		return a > b
	case OpAboveOrEqual:
		return a >= b-eps
	case OpEqual:
		return math.Abs(a-b) < eps
	case OpNotEqual:
		return math.Abs(a-b) >= eps
	}
	return false
}

func (s *snapshot) check(c Condition) truth {
	if c.Text != "" {
		m, ok := s.pluginMetric(c.Device, c.Metric)
		if !ok {
			return unknown
		}
		same := strings.EqualFold(strings.TrimSpace(fmt.Sprint(m.Value)), c.Text)
		if same == (c.Op != OpNotEqual) {
			return isTrue
		}
		return isFalse
	}
	left, right, ok := s.sides(c)
	if !ok {
		return unknown
	}
	if compare(c.Op, left, right) {
		return isTrue
	}
	return isFalse
}

// rule combines the conditions: with MatchAll one false condition makes the
// rule false, with MatchAny one true condition makes it true; otherwise an
// unknown condition makes it unknown.
func (s *snapshot) rule(r Rule) truth {
	decisive, other := isFalse, isTrue
	if r.Match == MatchAny {
		decisive, other = isTrue, isFalse
	}
	out := other
	for _, c := range r.Conditions {
		switch s.check(c) {
		case decisive:
			return decisive
		case unknown:
			out = unknown
		}
	}
	return out
}

// Evaluate checks every rule once at now.
func (e *Engine) Evaluate(ctx context.Context, now time.Time) {
	loc := e.Loc
	if loc == nil {
		loc = time.UTC
	}
	snap := &snapshot{devices: map[string]gateway.DeviceView{}, plugins: map[string]plugin.DeviceView{}, now: now.In(loc)}
	for _, d := range e.Devices.Views() {
		snap.devices[d.ID] = d
	}
	if e.Plugins != nil {
		for _, d := range e.Plugins.DeviceViews() {
			snap.plugins[d.ID] = d
		}
	}
	// looked up once per metric, only when a condition needs it; the
	// messages read them too
	type priced struct {
		v  float64
		ok bool
	}
	var priceMu sync.Mutex
	priceCache := map[string]priced{}
	snap.price = func(metric string) (float64, bool) {
		if e.Price == nil {
			return 0, false
		}
		priceMu.Lock()
		defer priceMu.Unlock()
		p, done := priceCache[metric]
		if !done {
			p.v, p.ok = e.Price(ctx, metric, now)
			priceCache[metric] = p
		}
		return p.v, p.ok
	}

	type change struct {
		id    int64
		armed bool
	}
	type firing struct {
		view      View
		otherwise bool // the conditions stopped holding: ElseActions
		run       *run // the wait or regulate action that follows the first actions
	}
	var changes []change
	var fire []firing
	var drops []int64
	e.mu.Lock()
	for _, x := range e.rules {
		if !x.rule.Enabled {
			x.since = time.Time{}
			continue
		}
		truth := isFalse
		switch {
		case x.rule.Manual && x.active:
			truth = isTrue
		case !x.rule.Manual:
			truth = snap.rule(x.rule)
		}
		switch truth {
		case isTrue:
			if x.since.IsZero() {
				x.since = now
			}
			if x.armed && !x.firing && now.Sub(x.since) >= time.Duration(x.rule.ForSeconds)*time.Second {
				x.armed, x.firing, x.lastFired = false, true, now
				changes = append(changes, change{x.rule.ID, false})
				f := firing{view: x.view()}
				if i := nextStep(x.rule.Actions, 0); i >= 0 {
					// a new start replaces a run that was still going
					f.run = newRun(i, now)
					x.run, x.busy = f.run, true
				}
				fire = append(fire, f)
			}
		case isFalse:
			x.since = time.Time{}
			if !x.armed && !x.firing {
				if x.rule.Kind == KindAutomation && len(x.rule.ElseActions) > 0 && x.busy {
					// actions of the run are still being sent (those after
					// a wait): the else actions wait for them, so they
					// come last
					break
				}
				x.armed = true
				changes = append(changes, change{x.rule.ID, true})
				if x.rule.Kind == KindAutomation && len(x.rule.ElseActions) > 0 {
					x.firing = true
					// the actions after a pending wait would undo the
					// else actions: the wait ends here (regulating goes on;
					// it stops by itself when the else actions switch its
					// device off)
					if x.run != nil && x.run.Step < len(x.rule.Actions) && x.rule.Actions[x.run.Step].Type == ActionWait {
						x.run = nil
						drops = append(drops, x.rule.ID)
					}
					fire = append(fire, firing{view: x.view(), otherwise: true})
				}
			}
		case unknown:
			x.since = time.Time{}
		}
	}
	e.mu.Unlock()

	for _, c := range changes {
		if err := e.storeState(ctx, c.id); err != nil {
			e.Log.Warn("regelstatus niet opgeslagen", "id", c.id, "err", err)
		}
	}

	for _, id := range drops {
		e.dropRun(ctx, id)
	}
	for _, f := range fire {
		if f.otherwise {
			e.Log.Info("regel: voorwaarden gelden niet meer", "id", f.view.ID, "name", f.view.Name)
		} else {
			e.Log.Info("regel gaat af", "id", f.view.ID, "name", f.view.Name)
		}
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.fire(ctx, f.view, snap, f.otherwise, f.run)
		}()
	}
	e.advanceRuns(ctx, snap, now)
}

// fire runs the actions of a rule (otherwise: its ElseActions). With r, only
// the actions before the wait or regulate action r is at; the run goes on
// from there.
func (e *Engine) fire(ctx context.Context, v View, snap *snapshot, otherwise bool, r *run) {
	actions := v.Actions
	if otherwise {
		actions = v.ElseActions
	}
	if r != nil {
		actions = actions[:r.Step]
	}
	// an alert says what it saw; an automation's push only says why it
	// comes: the automation went off (or its own message) or stopped
	body := v.Message
	switch {
	case otherwise && v.Kind == KindAutomation:
		body = "Gestopt."
	case otherwise:
		body = "Voorwaarden gelden niet meer."
	case body == "" && v.Kind == KindAutomation:
		body = "Afgegaan."
	case body == "":
		body = describe(v.Rule, snap)
	}
	result := e.send(ctx, v, snap, actions, body)
	switch {
	case otherwise && v.Manual:
		result = "Uitgezet: " + result
	case otherwise:
		result = "Voorwaarden golden niet meer: " + result
	}
	off := false // a button switches itself off once its actions ran
	e.mu.Lock()
	for _, x := range e.rules {
		if x.rule.ID == v.ID {
			x.firing, x.result = false, result
			if r != nil && x.run == r {
				x.busy = false
			}
			if !otherwise && momentary(x.rule) && x.active {
				x.active, off = false, true
			}
		}
	}
	e.mu.Unlock()
	if r != nil {
		e.saveRun(ctx, v.ID, r)
	}
	if off {
		if err := e.storeState(ctx, v.ID); err != nil {
			e.Log.Warn("regelstatus niet opgeslagen", "id", v.ID, "err", err)
		}
	}
}

// send runs actions (switches first, so the message can say whether they
// worked) and returns what happened. Wait and regulate actions are not
// handled here.
func (e *Engine) send(ctx context.Context, v View, snap *snapshot, actions []Action, body string) string {
	// the actions are finished on shutdown, but without retries
	stop := ctx.Done()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendBudget)
	defer cancel()
	var lines, problems []string
	notify := false
	for _, a := range actions {
		switch a.Type {
		case ActionNotify:
			notify = true
		case ActionSwitch:
			name := deviceName(snap, a.Device)
			word := "uit"
			if a.On {
				word = "aan"
			}
			st, err := e.control().Apply(ctx, stop, control.Command{Device: a.Device, Key: control.PowerKey, Value: a.On, Source: "rule", RuleID: v.ID})
			if line, problem := outcome(name+" "+word+"gezet", name+" "+word+"zetten", st, err); problem {
				problems = append(problems, line)
			} else {
				lines = append(lines, line)
			}
		case ActionPluginSet:
			if line, err := e.setPlugin(ctx, stop, v.ID, snap, a); err != nil {
				problems = append(problems, line)
			} else {
				lines = append(lines, line)
			}
		}
	}
	// what failed is always pushed, also by a rule without a notify action
	if (notify || len(problems) > 0) && e.Notify != nil {
		// an alert lists what its actions did; an automation's push keeps to
		// the reason and only adds what failed
		all := []string{body}
		if v.Kind != KindAutomation {
			all = append(all, lines...)
		}
		all = append(all, problems...)
		url := "/#/meldingen"
		if v.Kind == KindAutomation {
			url = "/#/automatisering"
		}
		msg := Message{Title: v.Name, Body: strings.Join(all, "\n"), Tag: fmt.Sprintf("rule-%d", v.ID), URL: url}
		if err := e.Notify(ctx, msg); err != nil {
			e.Log.Warn("regel: melding niet verstuurd", "id", v.ID, "err", err)
			problems = append(problems, "melding niet verstuurd: "+err.Error())
		}
	}
	result := "Uitgevoerd"
	if all := append(lines, problems...); len(all) > 0 {
		result = strings.Join(all, " ")
	}
	return result
}

// sendBudget is how long the actions of one firing may take together,
// retries included.
const sendBudget = 5 * time.Minute

// control returns the service that sends the engine's commands: the
// shared one when set, else one of its own over Devices and Plugins (tests).
func (e *Engine) control() *control.Service {
	e.ctlOnce.Do(func() {
		if e.Control == nil {
			var pl control.Plugins
			if e.Plugins != nil {
				pl = e.Plugins
			}
			e.Control = &control.Service{Devices: e.Devices, Plugins: pl, Log: e.Log,
				RetryPause: e.retryPause, VerifyWait: e.verifyWait, VerifyTick: e.verifyTick}
		}
	})
	return e.Control
}

// setPlugin sets a control of a plugin device through package control
// (checked, tried again) and says what it did ("Airco: Temperatuur
// 21 °C.") or what failed.
func (e *Engine) setPlugin(ctx context.Context, stop <-chan struct{}, ruleID int64, snap *snapshot, a Action) (string, error) {
	what := describeSet(snap, a)
	if e.Plugins == nil {
		return what + " mislukt: plugins niet beschikbaar", errors.New("plugins niet beschikbaar")
	}
	st, err := e.control().Apply(ctx, stop, control.Command{Device: a.Device, Key: a.Key, Value: a.Value, Source: "rule", RuleID: ruleID})
	line, problem := outcome(what, what, st, err)
	if !problem {
		return line, nil
	}
	return line, err
}

// outcome says what a command did: done ("Airco: Aan aan (poging 2)."),
// taken over by a newer command (not a problem), or failed (a problem).
func outcome(done, failed string, st control.Status, err error) (line string, problem bool) {
	switch {
	case err == nil && st.Attempt > 1:
		return fmt.Sprintf("%s (poging %d).", done, st.Attempt), false
	case err == nil:
		return done + ".", false
	case errors.Is(err, control.ErrReplaced):
		return failed + ": " + err.Error() + ".", false
	case st.Attempt > 1:
		return fmt.Sprintf("%s mislukt na %d pogingen: %v", failed, st.Attempt, err), true
	}
	return fmt.Sprintf("%s mislukt: %v", failed, err), true
}

func deviceName(snap *snapshot, id string) string {
	if pid, ok := pluginID(id); ok {
		if d, ok := snap.plugins[pid]; ok && d.Name != "" {
			return d.Name
		}
		return pid
	}
	if d, ok := snap.devices[id]; ok && d.Name != "" {
		return d.Name
	}
	return id
}

// describeSet says what a plugin_set action does, e.g. "Woonkamer:
// Temperatuur 21 °C" or "Woonkamer: Terug naar schema".
func describeSet(snap *snapshot, a Action) string {
	name := deviceName(snap, a.Device)
	id, _ := pluginID(a.Device)
	label, unit := a.Key, ""
	var options []plugin.Option
	if d, ok := snap.plugins[id]; ok {
		for _, c := range d.Controls {
			if c.Key == a.Key {
				label, unit, options = c.Label, c.Unit, c.Options
			}
		}
	}
	switch v := a.Value.(type) {
	case nil:
		return name + ": " + label
	case bool:
		if v {
			return name + ": " + label + " aan"
		}
		return name + ": " + label + " uit"
	case float64:
		return name + ": " + label + " " + formatValue("", v, unit)
	case string:
		for _, o := range options {
			if o.Value == v && o.Label != "" {
				v = o.Label
			}
		}
		return name + ": " + label + " " + v
	}
	return name + ": " + label
}

// describe makes the default message: the conditions with their values,
// e.g. "Droger: vermogen 3 W (lager dan 5 W)".
func describe(r Rule, snap *snapshot) string {
	parts := make([]string, 0, len(r.Conditions))
	for _, c := range r.Conditions {
		if c.Device == TimeDevice {
			parts = append(parts, describeWindow(c))
			continue
		}
		name, unit := valueName(snap, c.Device, c.Metric)
		op := opWords[c.Op]
		if c.Text != "" {
			if m, ok := snap.pluginMetric(c.Device, c.Metric); ok {
				parts = append(parts, fmt.Sprintf("%s %v", name, m.Value))
			} else {
				parts = append(parts, fmt.Sprintf("%s %s %s", name, op, c.Text))
			}
			continue
		}
		if s := scale(c.Scale); s != 1 {
			name = formatNumber(s) + " × " + name
		}
		threshold := formatValue(c.Metric, c.Value, unit)
		if c.Ref != nil {
			refName, refUnit := valueName(snap, c.Ref.Device, c.Ref.Metric)
			if s := scale(c.Ref.Scale); s != 1 {
				refName = formatNumber(s) + " × " + refName
			}
			threshold = refName
			if v, ok := snap.value(c.Ref.Device, c.Ref.Metric); ok {
				threshold += " (" + formatValue(c.Ref.Metric, v, refUnit) + ")"
			}
			if c.Value != 0 {
				threshold += " + " + formatNumber(c.Value)
			}
		}
		if v, ok := snap.value(c.Device, c.Metric); ok {
			parts = append(parts, fmt.Sprintf("%s %s (%s %s)", name, formatValue(c.Metric, v, unit), op, threshold))
		} else {
			parts = append(parts, fmt.Sprintf("%s %s %s", name, op, threshold))
		}
	}
	join := ", "
	if r.Match == MatchAny {
		join = " of "
	}
	text := strings.Join(parts, join)
	if r.ForSeconds > 0 {
		text += " sinds " + formatDuration(time.Duration(r.ForSeconds)*time.Second)
	}
	return text
}

var opWords = map[string]string{
	OpBelow: "lager dan", OpBelowOrEqual: "hooguit", OpAbove: "hoger dan",
	OpAboveOrEqual: "minstens", OpEqual: "gelijk aan", OpNotEqual: "niet gelijk aan",
}

// valueName names a value of a device for a message, e.g. "Droger:
// vermogen" or "Gasprijs all-in", with its unit.
func valueName(snap *snapshot, device, metric string) (name, unit string) {
	label, unit := metricLabel(metric)
	if device == PriceDevice {
		return strings.ToUpper(label[:1]) + label[1:], unit
	}
	if _, isPlugin := pluginID(device); isPlugin && metric != MetricOnline {
		label, unit = metric, ""
		if m, ok := snap.pluginMetric(device, metric); ok {
			label, unit = strings.ToLower(m.Label), m.Unit
		}
	}
	return deviceName(snap, device) + ": " + label, unit
}

func metricLabel(metric string) (label, unit string) {
	switch metric {
	case "power_w":
		return "vermogen", "W"
	case "water_lpm":
		return "waterverbruik", "L/min"
	case "state_of_charge_pct":
		return "laadniveau", "%"
	case "voltage_v":
		return "spanning", "V"
	case "current_a":
		return "stroom", "A"
	case MetricPrice:
		return "stroomprijs", "€/kWh"
	case MetricPriceAllIn:
		return "stroomprijs all-in", "€/kWh"
	case MetricGasPrice:
		return "gasprijs", "€/m³"
	case MetricGasPriceAllIn:
		return "gasprijs all-in", "€/m³"
	case MetricPriceAvgToday:
		return "gemiddelde stroomprijs vandaag", "€/kWh"
	case MetricCheaperHours:
		return "uren goedkoper dan nu", "uur"
	case MetricOnline:
		return "online", ""
	case MetricPowerOn:
		return "aan", ""
	case TodayPrefix + "energy_import_kwh":
		return "afname vandaag", "kWh"
	case TodayPrefix + "energy_export_kwh":
		return "teruglevering vandaag", "kWh"
	case TodayPrefix + "net_kwh":
		return "netto afname vandaag", "kWh"
	case TodayPrefix + "gas_m3":
		return "gas vandaag", "m³"
	case TodayPrefix + "water_m3":
		return "water vandaag", "m³"
	}
	return metric, ""
}

func formatValue(metric string, v float64, unit string) string {
	switch metric {
	case MetricOnline, MetricPowerOn:
		if v >= 0.5 {
			return "ja"
		}
		return "nee"
	case MetricPrice, MetricPriceAllIn, MetricGasPrice, MetricGasPriceAllIn, MetricPriceAvgToday:
		return strings.Replace(fmt.Sprintf("%.3f %s", v, unit), ".", ",", 1)
	}
	s := formatNumber(v)
	if unit == "" {
		return s
	}
	return s + " " + unit
}

// formatNumber writes a number the Dutch way with at most two decimals.
func formatNumber(v float64) string {
	s := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", v), "0"), ".")
	if math.Abs(v) >= 100 {
		s = fmt.Sprintf("%.0f", v)
	}
	if s == "-0" {
		s = "0"
	}
	return strings.Replace(s, ".", ",", 1)
}

func formatDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d s", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Minutes()))
	case d%time.Hour == 0:
		return fmt.Sprintf("%d uur", int(d.Hours()))
	default:
		return fmt.Sprintf("%d uur %d min", int(d.Hours()), int(d.Minutes())%60)
	}
}
