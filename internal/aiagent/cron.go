package aiagent

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// Schedule is a parsed task schedule: a 5-field cron expression
// (minute hour day-of-month month day-of-week, UTC) or "@every <duration>".
type Schedule struct {
	every time.Duration
	min   [60]bool
	hour  [24]bool
	dom   [32]bool
	month [13]bool
	dow   [7]bool
	// Like cron: when both day fields are restricted, either may match.
	domAny, dowAny bool
}

var cronAliases = map[string]string{
	"@hourly":   "0 * * * *",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@weekly":   "0 0 * * 0",
	"@monthly":  "0 0 1 * *",
}

// MinEvery is the shortest allowed @every interval: every run is an LLM call.
const MinEvery = 5 * time.Minute

// ParseSchedule parses a cron expression or @every duration.
func ParseSchedule(expr string) (*Schedule, error) {
	expr = strings.TrimSpace(expr)
	if a, ok := cronAliases[expr]; ok {
		expr = a
	}
	if rest, ok := strings.CutPrefix(expr, "@every "); ok {
		d, err := time.ParseDuration(strings.TrimSpace(rest))
		if err != nil {
			return nil, fmt.Errorf("@every: %w", err)
		}
		if d < MinEvery {
			return nil, i18n.Errorf("@every: at most once every %s", MinEvery)
		}
		return &Schedule{every: d}, nil
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, errors.New(i18n.T("5 cron fields expected (min hour day month weekday) or @every 15m"))
	}
	s := &Schedule{domAny: f[2] == "*", dowAny: f[4] == "*"}
	steps := []struct {
		field  string
		lo, hi int
		set    func(int)
		name   string
	}{
		{f[0], 0, 59, func(i int) { s.min[i] = true }, i18n.T("minutes")},
		{f[1], 0, 23, func(i int) { s.hour[i] = true }, i18n.T("hours")},
		{f[2], 1, 31, func(i int) { s.dom[i] = true }, i18n.T("day of month")},
		{f[3], 1, 12, func(i int) { s.month[i] = true }, i18n.T("month")},
		{f[4], 0, 7, func(i int) { s.dow[i%7] = true }, i18n.T("weekday")},
	}
	for _, st := range steps {
		if err := parseField(st.field, st.lo, st.hi, st.set); err != nil {
			return nil, fmt.Errorf("%s: %w", st.name, err)
		}
	}
	// A run every minute is too frequent for an LLM task: selected minutes
	// must be at least 5 apart, also across the hour (59 → 0).
	var mins []int
	for m, ok := range s.min {
		if ok {
			mins = append(mins, m)
		}
	}
	for i, m := range mins {
		next := mins[(i+1)%len(mins)]
		gap := next - m
		if gap <= 0 {
			gap += 60
		}
		if gap < 5 {
			return nil, errors.New(i18n.T("minutes: at most once every 5 minutes"))
		}
	}
	// e.g. "0 0 31 2 *": valid fields, but the date never comes.
	if s.Next(time.Now()).IsZero() {
		return nil, errors.New(i18n.T("the schedule never fires"))
	}
	return s, nil
}

func parseField(field string, lo, hi int, set func(int)) error {
	for _, part := range strings.Split(field, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return i18n.Errorf("step %q", s)
			}
			step, part = n, base
		}
		from, to := lo, hi
		switch {
		case part == "*":
		case strings.Contains(part, "-"):
			a, b, _ := strings.Cut(part, "-")
			x, err1 := strconv.Atoi(a)
			y, err2 := strconv.Atoi(b)
			if err1 != nil || err2 != nil || x > y {
				return i18n.Errorf("range %q", part)
			}
			from, to = x, y
		default:
			x, err := strconv.Atoi(part)
			if err != nil {
				return i18n.Errorf("value %q", part)
			}
			from, to = x, x
			if step > 1 {
				to = hi
			}
		}
		if from < lo || to > hi {
			return i18n.Errorf("%d-%d outside %d-%d", from, to, lo, hi)
		}
		for i := from; i <= to; i += step {
			set(i)
		}
	}
	return nil
}

// Next returns the first run time strictly after t (UTC, minute precision).
func (s *Schedule) Next(t time.Time) time.Time {
	if s.every > 0 {
		return t.Add(s.every).Truncate(time.Second)
	}
	t = t.UTC().Truncate(time.Minute).Add(time.Minute)
	// Four years covers every valid combination, including Feb 29.
	for limit := t.AddDate(4, 0, 0); t.Before(limit); {
		if !s.month[t.Month()] {
			t = time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.dayMatches(t) {
			t = time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, time.UTC)
			continue
		}
		if !s.hour[t.Hour()] {
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour()+1, 0, 0, 0, time.UTC)
			continue
		}
		if !s.min[t.Minute()] {
			t = t.Add(time.Minute)
			continue
		}
		return t
	}
	return time.Time{}
}

func (s *Schedule) dayMatches(t time.Time) bool {
	dom, dow := s.dom[t.Day()], s.dow[t.Weekday()]
	switch {
	case s.domAny && s.dowAny:
		return true
	case s.domAny:
		return dow
	case s.dowAny:
		return dom
	}
	return dom || dow
}
