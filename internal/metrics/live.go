package metrics

import (
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

const (
	// ringSpan is how much recent history Latest keeps in memory per server.
	// It covers the live tail of graphs (VictoriaMetrics hides the newest
	// ~30 s of data by default) and works without VictoriaMetrics at all.
	ringSpan = time.Hour
	// maxLogs is the size of the per-server log buffer.
	maxLogs = 300
	// watchTTL is how long a server counts as watched after the last request
	// from its screen. The screen polls every 2 s.
	watchTTL = 10 * time.Second
)

// Snapshot is the last report of a server (without detail and inventory,
// which are kept separately) plus the time it was received.
type Snapshot struct {
	Received time.Time       `json:"received"`
	Report   protocol.Report `json:"report"`
}

// Point is one sample of the live graphs. Net values are bytes per second
// summed over interfaces; nil when unknown.
type Point struct {
	Time time.Time
	CPU  float64
	Rx   *float64
	Tx   *float64
}

type serverState struct {
	snap      Snapshot
	hasSnap   bool
	detail    *protocol.Detail
	detailAt  time.Time
	inventory *protocol.Inventory
	logs      []protocol.LogLine // oldest first
	logKeys   map[logKey]struct{}
	ring      []Point
	prevNet   *netSample
	watched   time.Time
}

type logKey struct {
	t      int64
	source string
	msg    string
}

type netSample struct {
	t      time.Time
	rx, tx uint64
}

// Latest keeps live state per server: the last report, the last detail,
// inventory, a log buffer and an hour of graph points. It also tracks which
// servers have their screen open.
type Latest struct {
	mu   sync.RWMutex
	data map[int64]*serverState
	now  func() time.Time
}

// NewLatest returns an empty cache.
func NewLatest() *Latest { return &Latest{data: map[int64]*serverState{}, now: time.Now} }

func (l *Latest) state(id int64) *serverState {
	st, ok := l.data[id]
	if !ok {
		st = &serverState{logKeys: map[logKey]struct{}{}}
		l.data[id] = st
	}
	return st
}

// Put stores a batch of reports of one server, oldest first.
func (l *Latest) Put(serverID int64, reports ...protocol.Report) {
	if len(reports) == 0 {
		return
	}
	now := l.now().UTC()
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.state(serverID)
	for _, r := range reports {
		if r.Detail != nil {
			st.detail, st.detailAt = r.Detail, r.Timestamp
			st.addLogs(r.Detail.Logs)
		}
		if r.Inventory != nil {
			st.inventory = r.Inventory
		}
		st.addPoint(r)
	}
	last := reports[len(reports)-1]
	last.Detail, last.Inventory = nil, nil
	st.snap = Snapshot{Received: now, Report: last}
	st.hasSnap = true
}

func (st *serverState) addLogs(lines []protocol.LogLine) {
	for _, ln := range lines {
		k := logKey{ln.Time.UnixNano(), ln.Source, ln.Msg}
		if _, dup := st.logKeys[k]; dup {
			continue
		}
		st.logKeys[k] = struct{}{}
		// Keep the buffer ordered by time; lines usually arrive in order.
		i := len(st.logs)
		for i > 0 && st.logs[i-1].Time.After(ln.Time) {
			i--
		}
		st.logs = append(st.logs, protocol.LogLine{})
		copy(st.logs[i+1:], st.logs[i:])
		st.logs[i] = ln
	}
	if n := len(st.logs) - maxLogs; n > 0 {
		for _, ln := range st.logs[:n] {
			delete(st.logKeys, logKey{ln.Time.UnixNano(), ln.Source, ln.Msg})
		}
		st.logs = append([]protocol.LogLine(nil), st.logs[n:]...)
	}
}

func (st *serverState) addPoint(r protocol.Report) {
	t := r.Timestamp
	if t.IsZero() {
		return
	}
	if n := len(st.ring); n > 0 && !t.After(st.ring[n-1].Time) {
		return // out of order (agent clock jump); skip
	}
	p := Point{Time: t, CPU: r.Host.CPUPercent}
	var rx, tx uint64
	for _, n := range r.Host.Net {
		rx += n.RxBytes
		tx += n.TxBytes
	}
	if prev := st.prevNet; prev != nil && rx >= prev.rx && tx >= prev.tx {
		if dt := t.Sub(prev.t).Seconds(); dt > 0 {
			rxr, txr := float64(rx-prev.rx)/dt, float64(tx-prev.tx)/dt
			p.Rx, p.Tx = &rxr, &txr
		}
	}
	st.prevNet = &netSample{t: t, rx: rx, tx: tx}
	st.ring = append(st.ring, p)
	cut := 0
	for cut < len(st.ring) && t.Sub(st.ring[cut].Time) > ringSpan {
		cut++
	}
	if cut > 0 {
		st.ring = append([]Point(nil), st.ring[cut:]...)
	}
}

// Get returns the last report of a server.
func (l *Latest) Get(serverID int64) (Snapshot, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st, ok := l.data[serverID]
	if !ok || !st.hasSnap {
		return Snapshot{}, false
	}
	return st.snap, true
}

// Details is everything the server screen shows besides the snapshot.
type Details struct {
	Detail    *protocol.Detail    `json:"detail,omitempty"`
	DetailAt  *time.Time          `json:"detail_at,omitempty"`
	Inventory *protocol.Inventory `json:"inventory,omitempty"`
	Logs      []protocol.LogLine  `json:"logs"` // newest first
}

// Details returns detail, inventory and the newest logs (up to limit).
func (l *Latest) Details(serverID int64, limit int) Details {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := Details{Logs: []protocol.LogLine{}}
	st, ok := l.data[serverID]
	if !ok {
		return out
	}
	out.Detail, out.Inventory = st.detail, st.inventory
	if st.detail != nil {
		t := st.detailAt
		out.DetailAt = &t
	}
	for i := len(st.logs) - 1; i >= 0 && len(out.Logs) < limit; i-- {
		out.Logs = append(out.Logs, st.logs[i])
	}
	return out
}

// Points returns the in-memory graph points newer than since.
func (l *Latest) Points(serverID int64, since time.Time) []Point {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st, ok := l.data[serverID]
	if !ok {
		return nil
	}
	var out []Point
	for _, p := range st.ring {
		if p.Time.After(since) {
			out = append(out, p)
		}
	}
	return out
}

// Watch marks the server screen as open.
func (l *Latest) Watch(serverID int64) {
	now := l.now()
	l.mu.Lock()
	l.state(serverID).watched = now
	l.mu.Unlock()
}

// Watching reports whether the server screen was open recently; the ingest
// handler then asks the agent for detailed data.
func (l *Latest) Watching(serverID int64) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st, ok := l.data[serverID]
	return ok && !st.watched.IsZero() && l.now().Sub(st.watched) < watchTTL
}

// Forget drops all state of a server (after it is deleted).
func (l *Latest) Forget(serverID int64) {
	l.mu.Lock()
	delete(l.data, serverID)
	l.mu.Unlock()
}
