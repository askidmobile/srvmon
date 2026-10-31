package alerts

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

type fakeNotifier struct {
	mu  sync.Mutex
	got []Notification
}

func (f *fakeNotifier) Notify(n Notification) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, n)
	return true
}

func (f *fakeNotifier) list() []Notification {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Notification(nil), f.got...)
}

type testEnv struct {
	st     *store.Store
	latest *metrics.Latest
	e      *Engine
	n      *fakeNotifier
	now    time.Time
	srvID  int64
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "a.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	// No quiet hours in tests unless a test sets them.
	if err := SaveQuiet(ctx, st, Quiet{}); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateServer(ctx, store.Server{Name: "shop-db"})
	if err != nil {
		t.Fatal(err)
	}
	te := &testEnv{st: st, latest: metrics.NewLatest(), n: &fakeNotifier{}, now: time.Now(), srvID: id}
	te.e = New(st, te.latest, nil)
	te.e.started = te.now.Add(-time.Hour)
	te.e.now = func() time.Time { return te.now }
	te.e.SetNotifier(te.n)
	return te
}

func (te *testEnv) push(r protocol.Report) {
	if r.Timestamp.IsZero() {
		r.Timestamp = time.Now()
	}
	te.latest.Put(te.srvID, r)
	te.e.Observe(te.srvID, r)
}

func (te *testEnv) eval(t *testing.T) {
	t.Helper()
	if err := te.e.Evaluate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (te *testEnv) open(t *testing.T) []store.Alert {
	t.Helper()
	a, err := te.st.OpenAlerts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func (te *testEnv) rule(t *testing.T, kind string) store.Rule {
	t.Helper()
	rules, _ := te.st.ListRules(context.Background())
	for _, r := range rules {
		if r.Kind == kind {
			return r
		}
	}
	t.Fatalf("no rule %s", kind)
	return store.Rule{}
}

func host(cpu float64) protocol.HostMetrics {
	return protocol.HostMetrics{CPUPercent: cpu, MemTotal: 100, MemAvailable: 50,
		Disks: []protocol.Disk{{Mount: "/", Total: 100, Used: 10}}}
}

func TestCPUFiresAfterDurationAndResolves(t *testing.T) {
	te := newTestEnv(t)
	te.push(protocol.Report{Host: host(97)})
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("cpu fired before its 5m duration")
	}
	// Pretend the condition has held for 5 minutes.
	for k := range te.e.pending {
		te.e.pending[k] = te.now.Add(-5 * time.Minute)
	}
	te.eval(t)
	open := te.open(t)
	if len(open) != 1 || open[0].Kind != store.KindCPU || open[0].Severity != store.SevWarn {
		t.Fatalf("open = %+v", open)
	}
	te.eval(t) // dedup: still one alert, one notification
	if len(te.open(t)) != 1 || len(te.n.list()) != 1 {
		t.Fatalf("dedup failed: open %d, notified %d", len(te.open(t)), len(te.n.list()))
	}
	te.push(protocol.Report{Host: host(20)})
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("cpu alert not resolved")
	}
	got := te.n.list()
	if len(got) != 2 || !got[1].Resolved {
		t.Fatalf("notifications = %+v", got)
	}
}

func TestDiskPerMountAndStaleData(t *testing.T) {
	te := newTestEnv(t)
	h := host(1)
	h.Disks = []protocol.Disk{{Mount: "/", Total: 100, Used: 90}, {Mount: "/data", Total: 100, Used: 10}}
	te.push(protocol.Report{Host: h})
	te.eval(t)
	open := te.open(t)
	if len(open) != 1 || open[0].Subject != "/" {
		t.Fatalf("open = %+v", open)
	}
	// Stale snapshot: the disk alert is kept, not resolved.
	te.now = te.now.Add(10 * time.Minute)
	te.eval(t)
	found := false
	for _, a := range te.open(t) {
		if a.Kind == store.KindDisk {
			found = true
		}
	}
	if !found {
		t.Fatal("disk alert resolved on stale data")
	}
}

func TestContainerDown(t *testing.T) {
	te := newTestEnv(t)
	r := te.rule(t, store.KindContainerDown)
	r.DurationSec = 0
	if err := te.st.UpdateRule(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	te.push(protocol.Report{Host: host(1), DockerOK: true, Containers: []protocol.Container{
		{Name: "job", State: "exited", Status: "Exited (0) 2 hours ago"},
		{Name: "mailer", State: "exited", Status: "Exited (137) 1 minute ago"},
		{Name: "web", State: "running", Status: "Up 2 hours (unhealthy)", Health: "unhealthy"},
		{Name: "db", State: "running", Status: "Up 2 hours (healthy)", Health: "healthy"},
	}})
	te.eval(t)
	subjects := map[string]bool{}
	for _, a := range te.open(t) {
		subjects[a.Subject] = true
	}
	if len(subjects) != 2 || !subjects["mailer"] || !subjects["web"] {
		t.Fatalf("subjects = %v", subjects)
	}
}

func TestAgentSilent(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	if err := te.st.AddAgentToken(ctx, te.srvID, "h"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := te.st.AgentServer(ctx, "h"); err != nil { // bumps last_seen
		t.Fatal(err)
	}
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("fired while agent is fresh")
	}
	te.now = te.now.Add(3 * time.Minute)
	te.e.started = te.now // our own restart: grace period
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("fired during startup grace")
	}
	te.e.started = te.now.Add(-time.Hour)
	te.eval(t)
	open := te.open(t)
	if len(open) != 1 || open[0].Kind != store.KindAgentSilent || open[0].Severity != store.SevCrit {
		t.Fatalf("open = %+v", open)
	}
}

func TestRestartsFromUptime(t *testing.T) {
	te := newTestEnv(t)
	base := time.Now().Add(-time.Minute)
	statuses := []string{"Up 2 hours", "Up 3 seconds", "Up 10 seconds", "Up 1 second", "Up 4 seconds", "Up Less than a second"}
	for i, s := range statuses {
		te.push(protocol.Report{Timestamp: base.Add(time.Duration(i) * 5 * time.Second), Host: host(1), DockerOK: true,
			Containers: []protocol.Container{{Name: "mailer", State: "running", Status: s}}})
	}
	if n := te.e.restarts.counts(te.srvID, base.Add(-time.Hour))["mailer"]; n != 3 {
		t.Fatalf("restarts = %d, want 3", n)
	}
	te.eval(t)
	open := te.open(t)
	if len(open) != 1 || open[0].Kind != store.KindContainerRestarts {
		t.Fatalf("open = %+v", open)
	}
}

func TestDockerUptime(t *testing.T) {
	cases := map[string]time.Duration{
		"Up Less than a second":       0,
		"Up 5 seconds":                5 * time.Second,
		"Up About a minute":           time.Minute,
		"Up 3 hours (healthy)":        3 * time.Hour,
		"Up 2 days (Paused)":          48 * time.Hour,
		"Exited (1) 2 minutes ago":    -1,
		"Up About an hour":            time.Hour,
		"Up 1 week":                   7 * 24 * time.Hour,
		"Restarting (1) 1 second ago": -1,
	}
	for s, want := range cases {
		state := "running"
		if s[:2] != "Up" {
			state = "exited"
		}
		if got := dockerUptime(protocol.Container{State: state, Status: s}); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
}

func TestPaymentDue(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	cost := 15.9
	due := te.now.AddDate(0, 0, 2).Format("2006-01-02")
	if err := te.st.UpdateServerCard(ctx, te.srvID, store.ServerCard{Cost: &cost, Currency: "EUR", NextPayment: due,
		Provider: "Hetzner"}); err != nil {
		t.Fatal(err)
	}
	te.eval(t)
	open := te.open(t)
	if len(open) != 1 || open[0].Kind != store.KindPaymentDue || open[0].Subject != due {
		t.Fatalf("open = %+v", open)
	}
	if want := "оплата 15.9 EUR (Hetzner) через 2 дн."; open[0].Message[:len(want)] != want {
		t.Fatalf("message = %q", open[0].Message)
	}
	// Paid: the date moves on, the alert resolves.
	if err := te.st.UpdateServerCard(ctx, te.srvID, store.ServerCard{NextPayment: te.now.AddDate(0, 1, 0).Format("2006-01-02")}); err != nil {
		t.Fatal(err)
	}
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("payment alert not resolved")
	}
}

func TestDisabledRuleClosesQuietly(t *testing.T) {
	te := newTestEnv(t)
	h := host(1)
	h.Disks[0].Used = 99
	te.push(protocol.Report{Host: h})
	te.eval(t)
	if len(te.open(t)) != 1 {
		t.Fatal("no disk alert")
	}
	r := te.rule(t, store.KindDisk)
	r.Enabled = false
	_ = te.st.UpdateRule(context.Background(), r)
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("alert of disabled rule still open")
	}
	if got := te.n.list(); len(got) != 1 {
		t.Fatalf("closing a disabled rule must not notify: %+v", got)
	}
}

func TestSilenceAndAck(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	h := host(1)
	h.Disks[0].Used = 99
	te.push(protocol.Report{Host: h})
	te.eval(t)
	a := te.open(t)[0]
	if _, _, err := te.e.Silence(ctx, a.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	got, _ := te.st.GetAlert(ctx, a.ID)
	if got.State != store.AlertAcked {
		t.Fatalf("state = %s", got.State)
	}
	// Resolve and fire again within the silence: no new notifications.
	h.Disks[0].Used = 10
	te.push(protocol.Report{Host: h})
	te.eval(t)
	h.Disks[0].Used = 99
	te.push(protocol.Report{Host: h})
	te.eval(t)
	if n := len(te.n.list()); n != 1 { // only the first firing
		t.Fatalf("notifications = %d, want 1", n)
	}
}

func TestQuietHours(t *testing.T) {
	q := Quiet{From: "23:00", To: "07:00", TZ: "UTC"}
	at := func(h, m int) time.Time { return time.Date(2026, 10, 31, h, m, 0, 0, time.UTC) }
	for _, c := range []struct {
		t    time.Time
		want bool
	}{{at(23, 30), true}, {at(3, 0), true}, {at(7, 0), false}, {at(12, 0), false}} {
		if got := q.Active(c.t); got != c.want {
			t.Errorf("%v: %v", c.t, got)
		}
	}
	if (Quiet{From: "1:00"}).Validate() == nil {
		t.Error("half-set quiet hours accepted")
	}

	te := newTestEnv(t)
	now := te.now.UTC()
	from := now.Add(-time.Hour).Format("15:04")
	to := now.Add(time.Hour).Format("15:04")
	if err := SaveQuiet(context.Background(), te.st, Quiet{From: from, To: to, TZ: "UTC"}); err != nil {
		t.Fatal(err)
	}
	h := host(1)
	h.Disks[0].Used = 99 // warn: held back
	te.push(protocol.Report{Host: h})
	te.eval(t)
	if len(te.n.list()) != 0 {
		t.Fatal("warn sent during quiet hours")
	}
	te.e.Event(context.Background(), store.KindLogin, 0, "", "1.2.3.4", "вход") // info: held back
	if len(te.n.list()) != 0 {
		t.Fatal("info sent during quiet hours")
	}
}

func TestEventsAndFail2banTail(t *testing.T) {
	te := newTestEnv(t)
	path := filepath.Join(t.TempDir(), "f2b.log")
	if err := os.WriteFile(path, []byte("2026-10-31T08:00:00Z ban jail=sshd ip=9.9.9.9 failures=5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { te.e.TailFail2ban(ctx, path, "monitor"); close(done) }()
	time.Sleep(200 * time.Millisecond) // first pass skips existing lines
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString("2026-10-31T09:00:00Z ban jail=srvmon-auth ip=203.0.113.7 failures=5\n")
	f.Close()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) && len(te.n.list()) == 0 {
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	<-done
	got := te.n.list()
	if len(got) != 1 || got[0].Alert.Subject != "203.0.113.7" || got[0].Alert.State != store.AlertEvent {
		t.Fatalf("notifications = %+v", got)
	}
	if _, ok := parseFail2ban("2026-10-31T09:00:00Z unban jail=sshd ip=1.1.1.1"); ok {
		t.Error("unban parsed as ban")
	}
}

func TestFormatDuration(t *testing.T) {
	for d, want := range map[time.Duration]string{
		90 * time.Second: "1м30с", 2 * time.Hour: "2ч", 26 * time.Hour: "1д2ч", 5 * time.Minute: "5м", 0: "0с",
	} {
		if got := FormatDuration(d); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestRestartCounterDoesNotDoubleCount(t *testing.T) {
	te := newTestEnv(t)
	base := time.Now().Add(-time.Minute)
	n0, n1 := 4, 5
	// Detail mode: uptime drops at once, the cached counter catches up later.
	reps := []protocol.Container{
		{Name: "mailer", State: "running", Status: "Up 2 hours", Restarts: &n0},
		{Name: "mailer", State: "running", Status: "Up 1 second", Restarts: &n0},
		{Name: "mailer", State: "running", Status: "Up 3 seconds", Restarts: &n0},
		{Name: "mailer", State: "running", Status: "Up 5 seconds", Restarts: &n1},
	}
	for i, c := range reps {
		te.push(protocol.Report{Timestamp: base.Add(time.Duration(i) * 2 * time.Second), Host: host(1), DockerOK: true,
			Containers: []protocol.Container{c}})
	}
	if n := te.e.restarts.counts(te.srvID, base.Add(-time.Hour))["mailer"]; n != 1 {
		t.Fatalf("restarts = %d, want 1", n)
	}
}

func TestDisabledRuleForgetsPendingTimer(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	te.push(protocol.Report{Host: host(97)})
	te.eval(t)
	r := te.rule(t, store.KindCPU)
	r.Enabled = false
	_ = te.st.UpdateRule(ctx, r)
	te.eval(t)
	for k := range te.e.pending {
		if k.rule == r.ID {
			t.Fatal("pending timer of a disabled rule kept")
		}
	}
}

func TestQuietHoursDelayNotRecordedLoss(t *testing.T) {
	te := newTestEnv(t)
	ctx := context.Background()
	now := te.now.UTC()
	_ = SaveQuiet(ctx, te.st, Quiet{From: now.Add(-time.Hour).Format("15:04"), To: now.Add(time.Hour).Format("15:04"), TZ: "UTC"})
	h := host(1)
	h.Disks[0].Used = 99
	te.push(protocol.Report{Host: h})
	te.eval(t)
	if len(te.n.list()) != 0 {
		t.Fatal("sent during quiet hours")
	}
	_ = SaveQuiet(ctx, te.st, Quiet{})
	te.push(protocol.Report{Host: h})
	te.eval(t)
	te.eval(t)
	if got := te.n.list(); len(got) != 1 || got[0].Resolved {
		t.Fatalf("after quiet hours: %+v", got)
	}
}
