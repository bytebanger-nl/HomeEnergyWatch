package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/bytebanger-nl/homeenergywatch/internal/gateway"
	"github.com/bytebanger-nl/homeenergywatch/internal/homewizard"
	"github.com/bytebanger-nl/homeenergywatch/internal/plugin"
)

// fakePlugins is an air conditioner "ac" with a power switch and a
// setpoint; drops makes Set calls accepted without changing the device.
type fakePlugins struct {
	mu      sync.Mutex
	view    plugin.DeviceView
	drops   int   // next Set calls that do not arrive
	fails   int   // next Set calls that fail
	err     error // the error of a failing Set (default: a timeout)
	calls   int
	block   chan struct{} // Set waits until closed, when set
	started chan struct{} // gets a value when Set is called, when set
}

func newPlugins() *fakePlugins {
	return &fakePlugins{view: plugin.DeviceView{
		ID: "ac", Name: "Airco",
		Controls: []plugin.Control{
			{Key: "power", Kind: plugin.ControlSwitch, Metric: "on"},
			{Key: "temp", Kind: plugin.ControlSetpoint, Metric: "target", Step: 0.5, Min: 16, Max: 30},
			{Key: "schedule", Kind: plugin.ControlAction},
		},
		Reading: &plugin.Reading{Online: true, Metrics: []plugin.Metric{
			{Key: "on", Kind: plugin.MetricBool, Value: false},
			{Key: "target", Kind: plugin.MetricNumber, Value: 20.0},
		}},
	}}
}

func (f *fakePlugins) DeviceViews() []plugin.DeviceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.view
	r := *v.Reading
	r.Metrics = append([]plugin.Metric(nil), r.Metrics...)
	v.Reading = &r
	return []plugin.DeviceView{v}
}

func (f *fakePlugins) Set(ctx context.Context, id, key string, value any) error {
	f.mu.Lock()
	block, started := f.block, f.started
	f.calls++
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if id != "ac" {
		return gateway.ErrNotFound
	}
	if f.fails > 0 {
		f.fails--
		if f.err != nil {
			return f.err
		}
		return errors.New("i/o timeout")
	}
	if f.drops > 0 {
		f.drops--
		return nil
	}
	metric := map[string]string{"power": "on", "temp": "target"}[key]
	for i := range f.view.Reading.Metrics {
		if f.view.Reading.Metrics[i].Key == metric {
			f.view.Reading.Metrics[i].Value = value
		}
	}
	return nil
}

func (f *fakePlugins) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeDevices is one HomeWizard socket "s1".
type fakeDevices struct {
	mu      sync.Mutex
	view    gateway.DeviceView
	fails   int
	ignored bool // the socket answers but keeps its state
	calls   int
}

func newDevices() *fakeDevices {
	off := false
	return &fakeDevices{view: gateway.DeviceView{ID: "s1", Name: "Droger", Kind: homewizard.KindSocket,
		Status: gateway.StatusOnline, CanSwitch: true, Reading: &homewizard.Reading{PowerOn: &off}}}
}

func (f *fakeDevices) Views() []gateway.DeviceView {
	f.mu.Lock()
	defer f.mu.Unlock()
	v := f.view
	r := *v.Reading
	v.Reading = &r
	return []gateway.DeviceView{v, {ID: "p1", Kind: homewizard.KindP1}}
}

func (f *fakeDevices) SetPower(_ context.Context, id string, on bool) (gateway.DeviceView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if id != "s1" {
		return gateway.DeviceView{}, gateway.ErrNotFound
	}
	if f.fails > 0 {
		f.fails--
		return gateway.DeviceView{}, errors.New("niet bereikbaar")
	}
	if !f.ignored {
		f.view.Reading.PowerOn = &on
	}
	return f.view, nil
}

type recorder struct {
	mu   sync.Mutex
	list []Status
}

func (r *recorder) add(s Status) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.list = append(r.list, s)
}

func (r *recorder) states() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, s := range r.list {
		out = append(out, s.State)
	}
	return out
}

func service(pl *fakePlugins, dev *fakeDevices) (*Service, *recorder) {
	rec := &recorder{}
	s := &Service{Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Publish: rec.add,
		RetryPause: time.Millisecond, VerifyWait: 5 * time.Millisecond, VerifyTick: time.Millisecond}
	if pl != nil {
		s.Plugins = pl
	}
	if dev != nil {
		s.Devices = dev
	}
	return s, rec
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// A command that is accepted but not seen on the device is sent again.
func TestApplyRetriesUntilSeen(t *testing.T) {
	pl := newPlugins()
	pl.drops = 1
	s, rec := service(pl, nil)
	st, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "temp", Value: 22.0, Source: "app"})
	if err != nil || st.State != StateOK || st.Attempt != 2 {
		t.Fatalf("status %+v, err %v; want ok at the second attempt", st, err)
	}
	if got := rec.states(); !equal(got, []string{StateBusy, StateRetry, StateOK}) {
		t.Fatalf("published %v", got)
	}
}

// Three attempts at most, then failed with the reason.
func TestApplyGivesUp(t *testing.T) {
	pl := newPlugins()
	pl.drops = 100
	s, _ := service(pl, nil)
	st, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "power", Value: true})
	if !errors.Is(err, ErrNotApplied) || st.State != StateFailed || st.Attempt != DefaultAttempts {
		t.Fatalf("status %+v, err %v", st, err)
	}
	if pl.callCount() != DefaultAttempts {
		t.Fatalf("%d calls, want %d", pl.callCount(), DefaultAttempts)
	}
}

// An offline device is not seen to take the value: tried again.
func TestApplyOfflineRetried(t *testing.T) {
	pl := newPlugins()
	pl.view.Reading.Online = false
	s, _ := service(pl, nil)
	_, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "power", Value: true})
	if !errors.Is(err, ErrNotApplied) || pl.callCount() != DefaultAttempts {
		t.Fatalf("err %v after %d calls", err, pl.callCount())
	}
}

// A control that cannot be used is not tried again.
func TestApplyPermanentNotRetried(t *testing.T) {
	pl := newPlugins()
	pl.fails, pl.err = 100, plugin.ErrControlOff
	s, _ := service(pl, nil)
	st, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "power", Value: true})
	if !errors.Is(err, plugin.ErrControlOff) || st.Attempt != 1 || pl.callCount() != 1 {
		t.Fatalf("status %+v, err %v, %d calls", st, err, pl.callCount())
	}
}

// A control without a metric cannot be checked: done once sent.
func TestApplyUncheckable(t *testing.T) {
	pl := newPlugins()
	s, _ := service(pl, nil)
	if st, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "schedule"}); err != nil || st.Attempt != 1 {
		t.Fatalf("status %+v, err %v", st, err)
	}
}

// After stop closes the attempt that runs is finished, but not repeated.
func TestApplyStopNoRetry(t *testing.T) {
	pl := newPlugins()
	pl.fails = 100
	s, _ := service(pl, nil)
	stop := make(chan struct{})
	close(stop)
	if _, err := s.Apply(context.Background(), stop, Command{Device: "plugin:ac", Key: "power", Value: true}); err == nil || pl.callCount() != 1 {
		t.Fatalf("err %v after %d calls, want one failed call", err, pl.callCount())
	}
}

// A newer command for the same control stops the older one; another
// control is left alone.
func TestLatestCommandWins(t *testing.T) {
	pl := newPlugins()
	pl.block, pl.started = make(chan struct{}), make(chan struct{}, 4)
	s, _ := service(pl, nil)
	type result struct {
		st  Status
		err error
	}
	first := make(chan result, 1)
	go func() {
		st, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "temp", Value: 22.0})
		first <- result{st, err}
	}()
	<-pl.started
	pl.mu.Lock()
	pl.block = nil
	pl.mu.Unlock()
	st, err := s.Apply(context.Background(), nil, Command{Device: "plugin:ac", Key: "temp", Value: 24.0})
	if err != nil || st.State != StateOK {
		t.Fatalf("newer: %+v %v", st, err)
	}
	r := <-first
	if !errors.Is(r.err, ErrReplaced) || r.st.State != StateReplaced {
		t.Fatalf("older: %+v %v, want replaced", r.st, r.err)
	}
	list := s.List()
	if len(list) != 1 || list[0].ID != st.ID || list[0].State != StateOK {
		t.Fatalf("list %+v, want the newer command only", list)
	}
}

// Start checks the command first and sends it in the background.
func TestStart(t *testing.T) {
	s, rec := service(newPlugins(), newDevices())
	for _, c := range []struct {
		cmd  Command
		want error
	}{
		{Command{Device: "nope", Key: PowerKey, Value: true}, gateway.ErrNotFound},
		{Command{Device: "p1", Key: PowerKey, Value: true}, gateway.ErrCannotSwitch},
		{Command{Device: "s1", Key: PowerKey, Value: "aan"}, ErrInvalid},
		{Command{Device: "s1", Key: "temp", Value: true}, ErrInvalid},
		{Command{Device: "", Key: PowerKey}, ErrInvalid},
	} {
		if _, err := s.Start(c.cmd); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.cmd, err, c.want)
		}
	}
	st, err := s.Start(Command{Device: "s1", Key: PowerKey, Value: true, Source: "app"})
	if err != nil || st.State != StateBusy {
		t.Fatalf("start: %+v %v", st, err)
	}
	s.Close()
	if got := rec.states(); !equal(got, []string{StateBusy, StateOK}) {
		t.Fatalf("published %v", got)
	}
}

// A socket that does not answer is switched again; one that keeps its
// state because its switch lock is on is not.
func TestSocket(t *testing.T) {
	dev := newDevices()
	dev.fails = 2
	s, _ := service(nil, dev)
	st, err := s.Apply(context.Background(), nil, Command{Device: "s1", Key: PowerKey, Value: true})
	if err != nil || st.Attempt != 3 {
		t.Fatalf("status %+v, err %v; want ok at the third attempt", st, err)
	}

	dev = newDevices()
	dev.ignored = true
	locked := true
	dev.view.Reading.Locked = &locked
	s, _ = service(nil, dev)
	st, err = s.Apply(context.Background(), nil, Command{Device: "s1", Key: PowerKey, Value: true})
	if !errors.Is(err, ErrLocked) || dev.calls != 1 {
		t.Fatalf("status %+v, err %v after %d calls; want locked once", st, err, dev.calls)
	}
}

func TestValueMatches(t *testing.T) {
	mode := plugin.Control{Kind: plugin.ControlSelect, Options: []plugin.Option{{Value: "HEAT", Label: "Verwarmen"}}}
	sp := plugin.Control{Kind: plugin.ControlSetpoint, Step: 0.5}
	sw := plugin.Control{Kind: plugin.ControlSwitch}
	for _, c := range []struct {
		c          plugin.Control
		got, want  any
		check, hit bool
	}{
		{mode, "Verwarmen", "HEAT", true, true},
		{mode, "HEAT", "HEAT", true, true},
		{mode, "Koelen", "HEAT", true, false},
		{sp, 21.0, 21.0, true, true},
		{sp, 21.5, 21.0, true, false},
		{sw, true, true, true, true},
		{sw, false, true, true, false},
		{sw, 1.0, true, true, true},
		{plugin.Control{Kind: plugin.ControlAction}, nil, nil, false, false},
	} {
		check, hit := ValueMatches(c.c, c.got, c.want)
		if check != c.check || hit != c.hit {
			t.Errorf("%v %v %v: %v %v, want %v %v", c.c.Kind, c.got, c.want, check, hit, c.check, c.hit)
		}
	}
}
