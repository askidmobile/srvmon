package alerts

import (
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

func openOf(te *testEnv, t *testing.T, kind string) []store.Alert {
	var out []store.Alert
	for _, a := range te.open(t) {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

func TestDiskFillForecast(t *testing.T) {
	te := newTestEnv(t)
	f := metrics.NewForecasts()
	te.e.SetForecasts(f)
	te.push(protocol.Report{Host: host(1)})
	// No forecast yet: nothing fires.
	te.eval(t)
	if n := len(openOf(te, t, store.KindDiskFill)); n != 0 {
		t.Fatalf("fired without a forecast: %d", n)
	}
	f.Set(map[string]map[string]float64{"shop-db": {"/": 13.5, "/data": 200}}, te.now)
	te.eval(t)
	open := openOf(te, t, store.KindDiskFill)
	if len(open) != 1 || open[0].Subject != "/" {
		t.Fatalf("open = %+v", open)
	}
	// A stale forecast keeps the alert as it is.
	f.Set(map[string]map[string]float64{}, te.now.Add(-time.Hour))
	te.eval(t)
	if len(openOf(te, t, store.KindDiskFill)) != 1 {
		t.Fatal("resolved on a stale forecast")
	}
	// The disk stopped growing.
	f.Set(map[string]map[string]float64{}, te.now)
	te.eval(t)
	if len(openOf(te, t, store.KindDiskFill)) != 0 {
		t.Fatal("not resolved")
	}
}

func TestSmartRule(t *testing.T) {
	te := newTestEnv(t)
	ok, bad := true, false
	one, zero := uint64(1), uint64(0)
	te.push(protocol.Report{Host: host(1), Smart: []protocol.DiskHealth{
		{Device: "sda", Model: "HDD", Passed: &ok, Pending: &one},
		{Device: "sdb", Passed: &ok, Pending: &zero, Reallocated: &one},
		{Device: "sdc", Error: "standby"},
		{Device: "nvme0n1", Passed: &bad, CriticalWarning: 4},
	}})
	te.eval(t)
	open := openOf(te, t, store.KindSmart)
	if len(open) != 2 {
		t.Fatalf("open = %+v", open)
	}
	subjects := map[string]bool{}
	for _, a := range open {
		subjects[a.Subject] = true
	}
	if !subjects["sda"] || !subjects["nvme0n1"] {
		t.Fatalf("subjects = %v", subjects)
	}
	// Reports without SMART data keep the last read; a new read resolves.
	te.push(protocol.Report{Host: host(1)})
	te.eval(t)
	if len(openOf(te, t, store.KindSmart)) != 2 {
		t.Fatal("resolved by a report without SMART data")
	}
	// A disk that went to sleep keeps its alert: its health is unknown.
	te.push(protocol.Report{Host: host(1), Smart: []protocol.DiskHealth{{Device: "sda", Error: "standby"}}})
	te.eval(t)
	if open := openOf(te, t, store.KindSmart); len(open) != 1 || open[0].Subject != "sda" {
		t.Fatalf("after standby: %+v", open)
	}
	te.push(protocol.Report{Host: host(1), Smart: []protocol.DiskHealth{{Device: "sda", Passed: &ok, Pending: &zero}}})
	te.eval(t)
	if len(openOf(te, t, store.KindSmart)) != 0 {
		t.Fatal("not resolved")
	}
}
