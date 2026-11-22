// Package control sends a command to a device and checks that it arrived:
// after the command it waits until the device reads the new value, and
// when the command failed or did not arrive (an air conditioner whose wifi
// module drops a request, a socket that is briefly offline) it sends it
// again. Every way of controlling a device goes through it: the app's
// tiles and buttons, and the actions of automations, for HomeWizard
// sockets and plugin devices alike.
//
// The latest command for one control wins: a newer command for the same
// device and control stops one that is still being tried, so an automation
// and the user never keep sending against each other.
package control

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/notice"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
	"github.com/bytebanger-nl/homeenergywatch/internal/store"
)

// PluginPrefix marks a plugin device in Command.Device ("plugin:<id>"), as
// in rules.
const PluginPrefix = "plugin:"

// PowerKey is the control of a HomeWizard socket.
const PowerKey = "power"

// Defaults of the Service's times.
const (
	DefaultAttempts   = 3
	DefaultRetryPause = 15 * time.Second
	DefaultVerifyWait = 30 * time.Second
	DefaultVerifyTick = time.Second
	// DefaultBudget is how long a command from the app may take, retries
	// included.
	DefaultBudget = 5 * time.Minute
	// keepFor is how long a finished command's status is kept for the
	// app's snapshot.
	keepFor = 10 * time.Minute
)

// States of a command.
const (
	StateBusy     = "busy"     // first attempt
	StateRetry    = "retry"    // a later attempt, after a failure
	StateOK       = "ok"       // the device reads the new value
	StateFailed   = "failed"   // given up
	StateReplaced = "replaced" // a newer command for the same control took over
)

var (
	// ErrNotApplied: the command was accepted but the device does not read
	// the new value.
	ErrNotApplied = errors.New("het apparaat neemt de nieuwe stand niet over")
	// ErrReplaced: a newer command for the same control took over.
	ErrReplaced = errors.New("vervangen door een nieuwere opdracht")
	// ErrLocked: the socket's switch lock is on.
	ErrLocked = errors.New("de schakelaar van dit stopcontact is vergrendeld")
	// ErrInvalid: the command itself is wrong.
	ErrInvalid = errors.New("ongeldige opdracht")
	// ErrUnavailable: the kind of device is not available here.
	ErrUnavailable = errors.New("dit soort apparaat is niet beschikbaar")
)

// Plugins is what the service needs from the plugin manager.
type Plugins interface {
	DeviceViews() []plugin.DeviceView
	Set(ctx context.Context, deviceID, key string, value any) error
}

// pluginChecker checks a command without sending it (plugin.Manager).
type pluginChecker interface {
	CheckSet(deviceID, key string, value any) error
}

// Devices is what the service needs from the gateway.
type Devices interface {
	Views() []gateway.DeviceView
	SetPower(ctx context.Context, id string, on bool) (gateway.DeviceView, error)
}

// Command changes one control of one device.
type Command struct {
	// Device is a HomeWizard device id, or PluginPrefix + a plugin device id.
	Device string `json:"device"`
	// Key is the plugin control, PowerKey for a HomeWizard socket.
	Key   string `json:"key"`
	Value any    `json:"value"`
	// Source is "app" or "rule"; RuleID the automation.
	Source string `json:"source"`
	RuleID int64  `json:"rule_id,omitempty"`
}

// Status is where a command stands. It is published on every change.
type Status struct {
	Command
	ID       int64     `json:"id"`
	State    string    `json:"state"`
	Attempt  int       `json:"attempt"`
	Attempts int       `json:"attempts"` // at most
	Error    string    `json:"error,omitempty"`
	Started  time.Time `json:"started"`
	Updated  time.Time `json:"updated"`
}

// Done reports whether the command is finished.
func (s Status) Done() bool { return s.State != StateBusy && s.State != StateRetry }

// Service sends commands.
type Service struct {
	Plugins Plugins // optional
	Devices Devices // optional
	// Publish gets every status change; optional. It is called without
	// the service's lock held, in order per command.
	Publish func(Status)
	Log     *slog.Logger
	// Notices gets a command of an automation that did not take effect
	// (optional; the app shows its own commands' result itself).
	Notices *notice.Log
	// Attempts and the times; 0 is the default of the same name (tests
	// make them short).
	Attempts                           int
	RetryPause, VerifyWait, VerifyTick time.Duration
	Budget                             time.Duration

	mu      sync.Mutex
	seq     int64
	running map[string]*job   // by control
	recent  map[string]Status // by control: the latest command's status
	pubMu   sync.Mutex        // orders Publish calls
	base    context.Context
	stop    context.CancelFunc
	wg      sync.WaitGroup
}

type job struct {
	id     int64
	cancel context.CancelCauseFunc
}

func controlOf(c Command) string { return c.Device + "\x00" + c.Key }

func (s *Service) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// Permanent reports whether trying again cannot help: no such device or
// control, control not allowed, a wrong value, a locked socket.
func Permanent(err error) bool {
	for _, p := range []error{ErrInvalid, ErrUnavailable, ErrLocked, ErrReplaced,
		plugin.ErrNoControl, plugin.ErrControlOff, plugin.ErrValue, plugin.ErrDisabled,
		store.ErrNotFound, gateway.ErrNotFound, gateway.ErrCannotSwitch} {
		if errors.Is(err, p) {
			return true
		}
	}
	return false
}

// Check checks a command without sending it: the device and control exist,
// control is allowed and the value fits. Start does this first.
func (s *Service) Check(c Command) error {
	if c.Device == "" || c.Key == "" {
		return fmt.Errorf("%w: apparaat en bediening ontbreken", ErrInvalid)
	}
	if id, ok := strings.CutPrefix(c.Device, PluginPrefix); ok {
		if s.Plugins == nil {
			return ErrUnavailable
		}
		if pc, ok := s.Plugins.(pluginChecker); ok {
			return pc.CheckSet(id, c.Key, c.Value)
		}
		return nil
	}
	if s.Devices == nil {
		return ErrUnavailable
	}
	if c.Key != PowerKey {
		return fmt.Errorf("%w: een stopcontact kent alleen aan/uit", ErrInvalid)
	}
	if _, ok := c.Value.(bool); !ok {
		return fmt.Errorf("%w: aan of uit verwacht", ErrInvalid)
	}
	for _, v := range s.Devices.Views() {
		if v.ID == c.Device {
			if !v.CanSwitch {
				return gateway.ErrCannotSwitch
			}
			return nil
		}
	}
	return gateway.ErrNotFound
}

// Apply sends a command and waits until it arrived or was given up; the
// error says why it failed (nil when the device reads the new value),
// ErrReplaced when a newer command for the same control took over. When
// stop closes (shutdown) the attempt that runs is finished, but not tried
// again; ctx ending stops everything.
func (s *Service) Apply(ctx context.Context, stop <-chan struct{}, c Command) (Status, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	st := s.begin(c, cancel)
	return s.run(ctx, stop, st)
}

// Start checks a command and, when it can be used, sends it in the
// background (DefaultBudget at most); the returned status is its first.
// Progress comes through Publish and List.
func (s *Service) Start(c Command) (Status, error) {
	if err := s.Check(c); err != nil {
		return Status{}, err
	}
	s.mu.Lock()
	if s.base == nil {
		s.base, s.stop = context.WithCancel(context.Background())
	}
	stop := s.base.Done()
	s.mu.Unlock()
	ctx, cancelT := context.WithTimeout(context.Background(), cmp.Or(s.Budget, DefaultBudget))
	ctx, cancel := context.WithCancelCause(ctx)
	st := s.begin(c, cancel)
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer cancelT()
		defer cancel(nil)
		s.run(ctx, stop, st)
	}()
	return st, nil
}

// Close stops the commands started in the background and waits for them.
func (s *Service) Close() {
	s.mu.Lock()
	if s.stop != nil {
		s.stop()
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// List returns the status of the latest command per control: the running
// ones and those that finished in the last minutes, oldest first.
func (s *Service) List() []Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	out := []Status{}
	for k, st := range s.recent {
		if st.Done() && now.Sub(st.Updated) > keepFor {
			delete(s.recent, k)
			continue
		}
		out = append(out, st)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// begin registers a command: an older one for the same control is stopped.
func (s *Service) begin(c Command, cancel context.CancelCauseFunc) Status {
	s.mu.Lock()
	if s.running == nil {
		s.running, s.recent = map[string]*job{}, map[string]Status{}
	}
	s.seq++
	key := controlOf(c)
	if old := s.running[key]; old != nil {
		old.cancel(ErrReplaced)
	}
	s.running[key] = &job{id: s.seq, cancel: cancel}
	now := time.Now()
	st := Status{Command: c, ID: s.seq, State: StateBusy, Attempt: 1,
		Attempts: cmp.Or(s.Attempts, DefaultAttempts), Started: now, Updated: now}
	s.recent[key] = st
	s.mu.Unlock()
	s.publish(st)
	return st
}

// update records and publishes a status; a status of a command that was
// replaced in the meantime is not published over the newer one.
func (s *Service) update(st Status) {
	st.Updated = time.Now()
	s.mu.Lock()
	key := controlOf(st.Command)
	if st.Done() {
		if j := s.running[key]; j != nil && j.id == st.ID {
			delete(s.running, key)
		}
	}
	if cur, ok := s.recent[key]; ok && cur.ID > st.ID {
		s.mu.Unlock()
		return
	}
	s.recent[key] = st
	s.mu.Unlock()
	s.publish(st)
}

func (s *Service) publish(st Status) {
	if s.Publish == nil {
		return
	}
	s.pubMu.Lock()
	defer s.pubMu.Unlock()
	s.Publish(st)
}

// cause is why ctx ended: ErrReplaced, or nil for a plain stop.
func cause(ctx context.Context) error {
	if errors.Is(context.Cause(ctx), ErrReplaced) {
		return ErrReplaced
	}
	return nil
}

func (s *Service) run(ctx context.Context, stop <-chan struct{}, st Status) (Status, error) {
	pause := cmp.Or(s.RetryPause, DefaultRetryPause)
	var err error
	for {
		if err = s.send(ctx, st.Command); err == nil {
			err = s.verify(ctx, stop, st.Command)
		}
		if err == nil {
			st.State, st.Error = StateOK, ""
			if st.Attempt > 1 {
				s.log().Info("bediening gelukt na opnieuw proberen", "device", st.Device, "control", st.Key, "attempt", st.Attempt, "source", st.Source)
			}
			s.update(st)
			return st, nil
		}
		if c := cause(ctx); c != nil {
			err = c
		}
		if st.Attempt >= st.Attempts || Permanent(err) || ctx.Err() != nil || closed(stop) {
			break
		}
		s.log().Warn("bediening niet gelukt, opnieuw", "device", st.Device, "control", st.Key, "attempt", st.Attempt, "source", st.Source, "err", err)
		st.State, st.Error = StateRetry, err.Error()
		st.Attempt++
		s.update(st)
		if !sleep(ctx, stop, pause) {
			if c := cause(ctx); c != nil {
				err = c
			}
			st.Attempt-- // the next attempt was not made
			break
		}
	}
	if errors.Is(err, ErrReplaced) {
		st.State, st.Error = StateReplaced, err.Error()
	} else {
		st.State, st.Error = StateFailed, err.Error()
		s.log().Warn("bediening mislukt", "device", st.Device, "control", st.Key, "attempts", st.Attempt, "source", st.Source, "err", err)
		if st.Source == "rule" {
			s.Notices.Add(notice.KindControl, st.Device, "Automatisering kon niet bedienen",
				fmt.Sprintf("%s na %d pogingen: %v", st.Key, st.Attempt, err))
		}
	}
	s.update(st)
	return st, err
}

func (s *Service) send(ctx context.Context, c Command) error {
	if id, ok := strings.CutPrefix(c.Device, PluginPrefix); ok {
		if s.Plugins == nil {
			return ErrUnavailable
		}
		return s.Plugins.Set(ctx, id, c.Key, c.Value)
	}
	if s.Devices == nil {
		return ErrUnavailable
	}
	on, ok := c.Value.(bool)
	if c.Key != PowerKey || !ok {
		return fmt.Errorf("%w: een stopcontact kent alleen aan/uit", ErrInvalid)
	}
	_, err := s.Devices.SetPower(ctx, c.Device, on)
	return err
}

// verify waits until the device reads the value c sets. Nil as well when
// that cannot be checked (an action such as "back to schedule", a control
// without a metric); ErrNotApplied when the time is up or ctx ended first.
func (s *Service) verify(ctx context.Context, stop <-chan struct{}, c Command) error {
	wait, tick := cmp.Or(s.VerifyWait, DefaultVerifyWait), cmp.Or(s.VerifyTick, DefaultVerifyTick)
	deadline := time.Now().Add(wait)
	for {
		checkable, ok, err := s.applied(c)
		if err != nil {
			return err
		}
		if !checkable || ok {
			return nil
		}
		if time.Now().After(deadline) || !sleep(ctx, stop, tick) {
			return ErrNotApplied
		}
	}
}

// applied says whether the device reads the value c sets, and whether that
// can be checked at all.
func (s *Service) applied(c Command) (checkable, ok bool, err error) {
	if id, isPlugin := strings.CutPrefix(c.Device, PluginPrefix); isPlugin {
		for _, d := range s.Plugins.DeviceViews() {
			if d.ID != id {
				continue
			}
			var ctl *plugin.Control
			for i := range d.Controls {
				if d.Controls[i].Key == c.Key {
					ctl = &d.Controls[i]
				}
			}
			if ctl == nil || ctl.Metric == "" {
				return false, false, nil
			}
			if d.Reading == nil || !d.Reading.Online {
				return true, false, nil // offline: not seen (yet)
			}
			for _, m := range d.Reading.Metrics {
				if m.Key == ctl.Metric {
					checkable, ok := ValueMatches(*ctl, m.Value, c.Value)
					return checkable, ok, nil
				}
			}
			return false, false, nil
		}
		return false, false, nil
	}
	want, _ := c.Value.(bool)
	for _, v := range s.Devices.Views() {
		if v.ID != c.Device {
			continue
		}
		return socketApplied(v, want)
	}
	return false, false, nil
}

// socketApplied checks a HomeWizard socket's view: its state is read back
// from the socket with every poll and in the answer to the command.
func socketApplied(v gateway.DeviceView, want bool) (checkable, ok bool, err error) {
	r := v.Reading
	if v.Kind != homewizard.KindSocket || r == nil || r.PowerOn == nil {
		return false, false, nil
	}
	if v.Status != gateway.StatusOnline {
		return true, false, nil
	}
	if *r.PowerOn == want {
		return true, true, nil
	}
	if r.Locked != nil && *r.Locked {
		return true, false, ErrLocked
	}
	return true, false, nil
}

// ValueMatches compares a metric's value with the value sent to its
// control: a switch by on/off, a setpoint within half a step, a select by
// its option's value or label (an air conditioner shows "Verwarmen" for
// HEAT).
func ValueMatches(c plugin.Control, got, want any) (checkable, ok bool) {
	switch c.Kind {
	case plugin.ControlSwitch:
		w, ok1 := want.(bool)
		g, ok2 := asFloat(got)
		if !ok1 || !ok2 {
			return false, false
		}
		return true, (g >= 0.5) == w
	case plugin.ControlSetpoint:
		w, ok1 := asFloat(want)
		g, ok2 := asFloat(got)
		if !ok1 || !ok2 {
			return false, false
		}
		return true, math.Abs(g-w) <= math.Max(c.Step/2, 0.05)
	case plugin.ControlSelect:
		w, ok1 := want.(string)
		g, ok2 := got.(string)
		if !ok1 || !ok2 {
			return false, false
		}
		if strings.EqualFold(g, w) {
			return true, true
		}
		for _, o := range c.Options {
			if strings.EqualFold(o.Value, w) {
				return true, strings.EqualFold(g, o.Label)
			}
		}
		return true, false
	}
	return false, false
}

func asFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float64:
		return x, true
	case int:
		return float64(x), true
	}
	return 0, false
}

// sleep waits d; false when stop closed or ctx ended first.
func sleep(ctx context.Context, stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-stop:
	case <-ctx.Done():
	}
	return false
}

func closed(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return true
	default:
		return false
	}
}
