package metrics

import (
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

func TestLatestLogsDedupAndOrder(t *testing.T) {
	l := NewLatest()
	t0 := time.Date(2026, 10, 31, 8, 0, 0, 0, time.UTC)
	line := func(sec int, msg string) protocol.LogLine {
		return protocol.LogLine{Time: t0.Add(time.Duration(sec) * time.Second), Level: "ERR", Source: "x", Msg: msg}
	}
	l.Put(1, protocol.Report{Timestamp: t0, Detail: &protocol.Detail{Logs: []protocol.LogLine{line(2, "b"), line(1, "a")}}})
	// Agent restart re-sends a line; it must not be duplicated.
	l.Put(1, protocol.Report{Timestamp: t0.Add(time.Second), Detail: &protocol.Detail{Logs: []protocol.LogLine{line(2, "b"), line(3, "c")}}})
	d := l.Details(1, 10)
	if len(d.Logs) != 3 || d.Logs[0].Msg != "c" || d.Logs[2].Msg != "a" {
		t.Fatalf("logs = %+v", d.Logs)
	}
	if snap, _ := l.Get(1); snap.Report.Detail != nil {
		t.Fatal("snapshot keeps detail")
	}
}

func TestWatching(t *testing.T) {
	l := NewLatest()
	now := time.Now()
	l.now = func() time.Time { return now }
	if l.Watching(1) {
		t.Fatal("watching before Watch")
	}
	l.Watch(1)
	if !l.Watching(1) {
		t.Fatal("not watching after Watch")
	}
	if _, ok := l.Get(1); ok {
		t.Fatal("Watch must not invent a snapshot")
	}
	now = now.Add(watchTTL + time.Second)
	if l.Watching(1) {
		t.Fatal("still watching after TTL")
	}
}

func TestBuildHistoryMergesMemoryTail(t *testing.T) {
	end := time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC)
	vm := map[string][]sample{"cpu": {{end.Add(-10 * time.Minute), 10}, {end.Add(-time.Minute), 20}}}
	v := 90.0
	mem := []Point{
		{Time: end.Add(-2 * time.Minute), CPU: 99}, // older than VM tail: ignored
		{Time: end.Add(-5 * time.Second), CPU: 80, Rx: &v},
	}
	h := BuildHistory("15m", end, vm, mem)
	if len(h.CPU) != HistoryPoints || h.StepSec != 9.375 {
		t.Fatalf("shape: %d %v", len(h.CPU), h.StepSec)
	}
	if h.CPU[95] == nil || *h.CPU[95] != 80 || h.Rx[95] == nil || *h.Rx[95] != 90 {
		t.Fatalf("tail cpu=%v rx=%v", h.CPU[95], h.Rx[95])
	}
	nonNil := 0
	for _, p := range h.CPU {
		if p != nil {
			nonNil++
			if *p == 99 {
				t.Fatal("memory point older than VM data used")
			}
		}
	}
	if nonNil != 3 {
		t.Fatalf("buckets with data = %d", nonNil)
	}
}
