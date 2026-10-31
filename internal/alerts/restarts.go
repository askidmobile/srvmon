package alerts

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// restartTracker counts container restarts from the stream of reports. The
// docker list API has no restart counter, so a restart is seen as:
//   - the container entering the "restarting" state;
//   - its uptime ("Up 3 seconds") going down between two reports;
//   - the RestartCount from inspect growing (sent only in detail mode).
type restartTracker struct {
	mu   sync.Mutex
	data map[int64]map[string]*containerTrack
}

type containerTrack struct {
	state    string
	uptime   time.Duration // -1 when not running or unknown
	restarts *int
	seen     time.Time
	events   []time.Time
}

const (
	keepRestarts = 24 * time.Hour
	forgetAfter  = 2 * time.Hour
)

func newRestartTracker() *restartTracker {
	return &restartTracker{data: map[int64]map[string]*containerTrack{}}
}

func (t *restartTracker) observe(serverID int64, now time.Time, reports ...protocol.Report) {
	t.mu.Lock()
	defer t.mu.Unlock()
	m := t.data[serverID]
	if m == nil {
		m = map[string]*containerTrack{}
		t.data[serverID] = m
	}
	for _, r := range reports {
		if !r.DockerOK {
			continue
		}
		at := r.Timestamp
		if at.IsZero() || at.After(now) {
			at = now
		}
		for _, c := range r.Containers {
			cur := &containerTrack{state: c.State, uptime: dockerUptime(c), restarts: c.Restarts, seen: at}
			prev, ok := m[c.Name]
			if !ok {
				m[c.Name] = cur
				continue
			}
			if !at.After(prev.seen) {
				continue // replayed or out-of-order batch
			}
			n := 0
			switch {
			case cur.state == "restarting" && prev.state != "restarting":
				n = 1
			case cur.uptime >= 0 && prev.uptime >= 0 && cur.uptime < prev.uptime:
				n = 1
			}
			if prev.restarts != nil && cur.restarts != nil && *cur.restarts > *prev.restarts {
				n = max(n, *cur.restarts-*prev.restarts)
			}
			if cur.restarts == nil {
				cur.restarts = prev.restarts
			}
			cur.events = prev.events
			for i := 0; i < n; i++ {
				cur.events = append(cur.events, at)
			}
			m[c.Name] = cur
		}
	}
	for name, ct := range m {
		cut := 0
		for cut < len(ct.events) && now.Sub(ct.events[cut]) > keepRestarts {
			cut++
		}
		ct.events = ct.events[cut:]
		if now.Sub(ct.seen) > forgetAfter {
			delete(m, name)
		}
	}
}

// counts returns restarts per container since t (only non-zero counts).
func (t *restartTracker) counts(serverID int64, since time.Time) map[string]int {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]int{}
	for name, ct := range t.data[serverID] {
		n := 0
		for _, ev := range ct.events {
			if !ev.Before(since) {
				n++
			}
		}
		if n > 0 {
			out[name] = n
		}
	}
	return out
}

func (t *restartTracker) forget(serverID int64) {
	t.mu.Lock()
	delete(t.data, serverID)
	t.mu.Unlock()
}

// dockerUptime parses the uptime of a running container from its status
// ("Up 3 hours (healthy)"); -1 when not running. Docker rounds the value
// (units.HumanDuration) but it only ever grows while the container runs.
func dockerUptime(c protocol.Container) time.Duration {
	if c.State != "running" {
		return -1
	}
	s, ok := strings.CutPrefix(c.Status, "Up ")
	if !ok {
		return -1
	}
	if i := strings.Index(s, " ("); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	switch s {
	case "Less than a second":
		return 0
	case "About a minute":
		return time.Minute
	case "About an hour":
		return time.Hour
	}
	num, unit, ok := strings.Cut(s, " ")
	if !ok {
		return -1
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return -1
	}
	unit = strings.TrimSuffix(unit, "s")
	mult := map[string]time.Duration{
		"second": time.Second, "minute": time.Minute, "hour": time.Hour, "day": 24 * time.Hour,
		"week": 7 * 24 * time.Hour, "month": 30 * 24 * time.Hour, "year": 365 * 24 * time.Hour,
	}[unit]
	if mult == 0 {
		return -1
	}
	return time.Duration(n) * mult
}
