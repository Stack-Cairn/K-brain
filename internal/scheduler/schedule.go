package schedule

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type Schedule struct {
	Every time.Duration
	At    time.Time
}

var everyRe = regexp.MustCompile(`^@every\s+(\d+(?:\.\d+)?)(s|m|h|d)$`)

func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if m := everyRe.FindStringSubmatch(expr); m != nil {
		n, err := strconv.ParseFloat(m[1], 64)
		if err != nil || n <= 0 {
			return Schedule{}, fmt.Errorf("bad interval %q", m[1])
		}
		unit := map[string]time.Duration{"s": time.Second, "m": time.Minute, "h": time.Hour, "d": 24 * time.Hour}[m[2]]
		return Schedule{Every: time.Duration(n * float64(unit))}, nil
	}
	if at, ok := strings.CutPrefix(expr, "@at"); ok {
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(at))
		if err != nil {
			return Schedule{}, fmt.Errorf("@at needs an RFC3339 time (e.g. @at 2026-07-26T17:00:00Z): %w", err)
		}
		return Schedule{At: t}, nil
	}
	return Schedule{}, fmt.Errorf("schedule must be @every <dur> or @at <rfc3339>, got %q", expr)
}

func (s Schedule) String() string {
	if s.Every > 0 {
		return "@every " + s.Every.String()
	}
	return "@at " + s.At.Format(time.RFC3339)
}

func (s Schedule) NextAfter(anchor, t time.Time) (time.Time, bool) {
	if s.Every > 0 {
		next := anchor
		for !next.After(t) {
			next = next.Add(s.Every)
		}
		return next, true
	}
	if s.At.IsZero() {
		return time.Time{}, false
	}
	if s.At.After(t) {
		return s.At, true
	}
	return time.Time{}, false
}
