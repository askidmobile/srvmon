package aiagent

import (
	"fmt"
	"strconv"
	"strings"
	"time"
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
			return nil, fmt.Errorf("@every: не чаще раза в %s", MinEvery)
		}
		return &Schedule{every: d}, nil
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("ожидается 5 полей cron (мин час день месяц день_недели) или @every 15m")
	}
	s := &Schedule{domAny: f[2] == "*", dowAny: f[4] == "*"}
	steps := []struct {
		field  string
		lo, hi int
		set    func(int)
		name   string
	}{
		{f[0], 0, 59, func(i int) { s.min[i] = true }, "минуты"},
		{f[1], 0, 23, func(i int) { s.hour[i] = true }, "часы"},
		{f[2], 1, 31, func(i int) { s.dom[i] = true }, "день месяца"},
		{f[3], 1, 12, func(i int) { s.month[i] = true }, "месяц"},
		{f[4], 0, 7, func(i int) { s.dow[i%7] = true }, "день недели"},
	}
	for _, st := range steps {
		if err := parseField(st.field, st.lo, st.hi, st.set); err != nil {
			return nil, fmt.Errorf("%s: %w", st.name, err)
		}
	}
	// A run every minute is too frequent for an LLM task.
	n := 0
	for _, m := range s.min {
		if m {
			n++
		}
	}
	if n > 12 {
		return nil, fmt.Errorf("минуты: не чаще раза в 5 минут")
	}
	return s, nil
}

func parseField(field string, lo, hi int, set func(int)) error {
	for _, part := range strings.Split(field, ",") {
		step := 1
		if base, s, ok := strings.Cut(part, "/"); ok {
			n, err := strconv.Atoi(s)
			if err != nil || n <= 0 {
				return fmt.Errorf("шаг %q", s)
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
				return fmt.Errorf("диапазон %q", part)
			}
			from, to = x, y
		default:
			x, err := strconv.Atoi(part)
			if err != nil {
				return fmt.Errorf("значение %q", part)
			}
			from, to = x, x
			if step > 1 {
				to = hi
			}
		}
		if from < lo || to > hi {
			return fmt.Errorf("%d-%d вне %d-%d", from, to, lo, hi)
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
