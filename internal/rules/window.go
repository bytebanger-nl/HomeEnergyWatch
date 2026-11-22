package rules

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// parseClock reads "HH:MM" as minutes after midnight.
func parseClock(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil || len(s) != 5 {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

func checkWindow(c Condition) error {
	if c.Metric != MetricWindow {
		return fmt.Errorf("bij een tijdvenster hoort de meetwaarde venster")
	}
	if c.Op != OpEqual || (c.Value != 0 && c.Value != 1) {
		return fmt.Errorf("kies binnen of buiten het tijdvenster")
	}
	if len(c.Days) == 0 {
		return fmt.Errorf("kies minstens één dag")
	}
	for _, d := range c.Days {
		if d < 1 || d > 7 {
			return fmt.Errorf("ongeldige dag %d", d)
		}
	}
	if _, ok := parseClock(c.From); !ok {
		return fmt.Errorf("ongeldige begintijd (UU:MM)")
	}
	if _, ok := parseClock(c.To); !ok {
		return fmt.Errorf("ongeldige eindtijd (UU:MM)")
	}
	return nil
}

func normDays(days []int) []int {
	out := slices.Clone(days)
	slices.Sort(out)
	return slices.Compact(out)
}

// isoDay is the ISO weekday of t: 1 = Monday ... 7 = Sunday.
func isoDay(t time.Time) int {
	if d := int(t.Weekday()); d != 0 {
		return d
	}
	return 7
}

// inWindow reports whether now (already in the right time zone) falls in
// the window. A window past midnight belongs to the day it starts on.
func inWindow(c Condition, now time.Time) bool {
	from, _ := parseClock(c.From)
	to, _ := parseClock(c.To)
	m := now.Hour()*60 + now.Minute()
	today := slices.Contains(c.Days, isoDay(now))
	var in bool
	switch {
	case from == to:
		in = today
	case from < to:
		in = today && m >= from && m < to
	default:
		yesterday := slices.Contains(c.Days, isoDay(now.AddDate(0, 0, -1)))
		in = (today && m >= from) || (yesterday && m < to)
	}
	if c.Value == 0 {
		return !in
	}
	return in
}

var dayNames = [...]string{"", "ma", "di", "wo", "do", "vr", "za", "zo"}

// describeWindow says what a time window is, e.g. "Tijd: ma-vr 06:30-07:30"
// or "Tijd: buiten za, zo 22:00-07:00".
func describeWindow(c Condition) string {
	days := describeDays(c.Days)
	when := c.From + "-" + c.To
	if c.From == c.To {
		when = "hele dag"
	}
	if c.Value == 0 {
		return fmt.Sprintf("Tijd: buiten %s %s", days, when)
	}
	return fmt.Sprintf("Tijd: %s %s", days, when)
}

func describeDays(days []int) string {
	if len(days) == 7 {
		return "elke dag"
	}
	// a run of three or more consecutive days as "ma-vr"
	var parts []string
	for i := 0; i < len(days); {
		j := i
		for j+1 < len(days) && days[j+1] == days[j]+1 {
			j++
		}
		if j-i >= 2 {
			parts = append(parts, dayNames[days[i]]+"-"+dayNames[days[j]])
		} else {
			for k := i; k <= j; k++ {
				parts = append(parts, dayNames[days[k]])
			}
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
}
