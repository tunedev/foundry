// Package work holds the rules for an unattended run over friction issues.
package work

import (
	"fmt"
	"strings"
	"time"
)

// Window is the daily stretch of local time in which a run may start an issue. The zero Window is always open.
type Window struct {
	start, end int
	set        bool
}

// ParseWindow reads "HH:MM-HH:MM"; an end before the start wraps past midnight. "" is always open.
func ParseWindow(s string) (Window, error) {
	if s == "" {
		return Window{}, nil
	}
	a, b, ok := strings.Cut(s, "-")
	start, err1 := clock(a)
	end, err2 := clock(b)
	if !ok || err1 != nil || err2 != nil || start == end {
		return Window{}, fmt.Errorf("work_window %q must be HH:MM-HH:MM with different times", s)
	}
	return Window{start: start, end: end, set: true}, nil
}

func clock(s string) (int, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	return t.Hour()*60 + t.Minute(), err
}

func (w Window) Contains(t time.Time) bool {
	if !w.set {
		return true
	}
	m := t.Hour()*60 + t.Minute()
	if w.start < w.end {
		return m >= w.start && m < w.end
	}
	return m >= w.start || m < w.end
}
