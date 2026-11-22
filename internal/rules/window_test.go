package rules

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInWindow(t *testing.T) {
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	at := func(day, hh, mm int) time.Time { // 2026-11-09 is a Monday
		return time.Date(2026, 11, 9+day-1, hh, mm, 0, 0, ams)
	}
	weekdays := Condition{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{1, 2, 3, 4, 5}, From: "06:30", To: "07:30"}
	night := Condition{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{5}, From: "22:00", To: "06:00"}
	allDay := Condition{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{6, 7}, From: "00:00", To: "00:00"}
	for _, c := range []struct {
		c    Condition
		t    time.Time
		want bool
	}{
		{weekdays, at(1, 6, 29), false},
		{weekdays, at(1, 6, 30), true},
		{weekdays, at(5, 7, 29), true},
		{weekdays, at(5, 7, 30), false}, // the end is exclusive
		{weekdays, at(6, 7, 0), false},  // Saturday
		{night, at(5, 23, 0), true},     // Friday night
		{night, at(6, 5, 59), true},     // ... into Saturday morning
		{night, at(6, 6, 0), false},
		{night, at(6, 23, 0), false}, // Saturday night is not in it
		{night, at(5, 5, 0), false},  // Friday morning belongs to Thursday night
		{allDay, at(7, 12, 0), true},
		{allDay, at(1, 0, 1), false},
	} {
		if got := inWindow(c.c, c.t); got != c.want {
			t.Errorf("%v %s-%s at %s: %v, want %v", c.c.Days, c.c.From, c.c.To, c.t.Format("Mon 15:04"), got, c.want)
		}
		out := c.c
		out.Value = 0
		if got := inWindow(out, c.t); got == c.want {
			t.Errorf("outside %v %s-%s at %s: %v", c.c.Days, c.c.From, c.c.To, c.t.Format("Mon 15:04"), got)
		}
	}
	if s := describeWindow(weekdays); s != "Tijd: ma-vr 06:30-07:30" {
		t.Errorf("describe %q", s)
	}
	if s := describeWindow(Condition{Days: []int{1, 3, 6, 7}, From: "22:00", To: "07:00"}); s != "Tijd: buiten ma, wo, za, zo 22:00-07:00" {
		t.Errorf("describe %q", s)
	}
}

// The bathroom radiator: warm on weekday mornings, fired once when the
// window starts.
func TestWindowRuleFiresAtStart(t *testing.T) {
	h := newHarness(t)
	ams, err := time.LoadLocation("Europe/Amsterdam")
	if err != nil {
		t.Skip("no tzdata:", err)
	}
	h.eng.Loc = ams
	h.now = time.Date(2026, 11, 9, 6, 0, 0, 0, ams) // Monday 06:00
	ctx := context.Background()
	v, err := h.eng.Save(ctx, Rule{Name: "Badkamer ochtend", Enabled: true,
		Conditions: []Condition{{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{5, 1, 2, 3, 4, 1}, From: "06:30", To: "07:30"}},
		Actions:    []Action{{Type: ActionNotify}}})
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Conditions[0].Days; len(got) != 5 || got[0] != 1 || got[4] != 5 {
		t.Fatalf("days not normalized: %v", got)
	}
	h.step(time.Minute) // 06:01, outside: arms
	h.step(29 * time.Minute)
	if n := len(h.messages()); n != 1 {
		t.Fatalf("at 06:30: %d messages", n)
	}
	for range 60 {
		h.step(time.Minute)
	}
	if n := len(h.messages()); n != 1 {
		t.Fatalf("fired again within the window: %d", n)
	}
	if body := h.messages()[0].Body; body != "Tijd: ma-vr 06:30-07:30" {
		t.Fatalf("message %q", body)
	}

	for _, bad := range []Condition{
		{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, From: "06:30", To: "07:30"},
		{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{8}, From: "06:30", To: "07:30"},
		{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{1}, From: "6:30", To: "07:30"},
		{Device: TimeDevice, Metric: MetricWindow, Op: OpEqual, Value: 1, Days: []int{1}, From: "06:30", To: "24:00"},
		{Device: TimeDevice, Metric: MetricWindow, Op: OpAbove, Value: 1, Days: []int{1}, From: "06:30", To: "07:30"},
		{Device: "sock", Metric: "power_w", Op: OpAbove, Days: []int{1}},
	} {
		_, err := h.eng.Save(ctx, Rule{Name: "x", Conditions: []Condition{bad}, Actions: []Action{{Type: ActionNotify}}})
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("accepted %+v: %v", bad, err)
		}
	}
}
