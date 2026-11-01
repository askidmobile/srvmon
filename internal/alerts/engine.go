// Package alerts evaluates alert rules against live server state, keeps
// alerts in the store (firing → acked → resolved) and hands notifications to
// a Notifier (the Telegram bot).
package alerts

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

const (
	// Interval is how often rules are evaluated.
	Interval = 15 * time.Second
	// staleAfter: metric rules are not evaluated on older snapshots (the
	// agent_silent rule covers that case).
	staleAfter = 2 * time.Minute
	// startupGrace: right after a restart of srvmon-server agents have not
	// pushed yet, so agent_silent waits this long before it fires.
	startupGrace = time.Minute
	// keepHistory is how long closed alerts and events are kept.
	keepHistory = 90 * 24 * time.Hour
	// eventRetry: how long an undelivered event is still worth sending.
	eventRetry = 12 * time.Hour
	// forecastStale: disk forecasts older than this are not evaluated
	// (VictoriaMetrics unreachable).
	forecastStale = 3 * metrics.ForecastEvery
	// smartStale: agents read S.M.A.R.T. every 30 minutes.
	smartStale = 2 * time.Hour
)

// Notification is an alert that fired, resolved or an event.
type Notification struct {
	Alert    store.Alert
	Resolved bool
	// Diagnosis, when set, is the AI agent's read-only diagnosis of an alert
	// already notified: a follow-up to that notification.
	Diagnosis string
}

// Notifier delivers notifications. Notify must not block; it returns false
// when the notification was not accepted (e.g. Telegram is not configured).
type Notifier interface {
	Notify(n Notification) bool
}

type condKey struct {
	rule    int64
	server  int64
	subject string
}

// cond is one condition that currently holds.
type cond struct {
	subject string
	message string
}

// Engine evaluates rules. Create it with New and run Run in a goroutine.
type Engine struct {
	st     *store.Store
	latest *metrics.Latest
	log    *slog.Logger
	now    func() time.Time

	mu       sync.Mutex // serializes evaluation, events and acks
	pending  map[condKey]time.Time
	started  time.Time
	notifier Notifier
	onFire   func(store.Alert)

	restarts  *restartTracker
	forecasts *metrics.Forecasts
	lastEval  atomic.Int64 // unix nanos of the last successful Evaluate in Run
}

// New returns an engine over the store and the live metrics cache.
func New(st *store.Store, latest *metrics.Latest, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Engine{st: st, latest: latest, log: log, now: time.Now, pending: map[condKey]time.Time{},
		restarts: newRestartTracker()}
	e.started = e.now()
	return e
}

// SetNotifier sets where notifications go.
func (e *Engine) SetNotifier(n Notifier) {
	e.mu.Lock()
	e.notifier = n
	e.mu.Unlock()
}

// OnFire sets a hook called for every new firing alert (after its
// notification was handed over). It runs under the engine lock: it must not
// block or call back into the engine.
func (e *Engine) OnFire(f func(store.Alert)) {
	e.mu.Lock()
	e.onFire = f
	e.mu.Unlock()
}

// NotifyDiagnosis sends the AI diagnosis of an alert where its notification
// went. Nothing is sent when the alert was not notified (web only, silenced,
// held back), the silence started meanwhile, or quiet hours hold back a
// noncritical one.
func (e *Engine) NotifyDiagnosis(ctx context.Context, alertID int64, text string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.st.GetAlert(ctx, alertID)
	if err != nil || !a.Notified || a.Channel != store.ChanTelegram || e.notifier == nil {
		return false
	}
	until, err := e.st.SilencedUntil(ctx, store.SilenceKey{RuleID: a.RuleID, ServerID: a.ServerID, Subject: a.Subject}, e.now())
	if err != nil || !until.IsZero() {
		return false
	}
	// Quiet hours hold back everything but critical alerts; a diagnosis is
	// not worth waking up for either (it stays in the panel).
	if a.Severity != store.SevCrit {
		if q, err := LoadQuiet(ctx, e.st); err != nil || q.Active(e.now()) {
			return false
		}
	}
	return e.notifier.Notify(Notification{Alert: a, Diagnosis: text})
}

// SetForecasts sets where disk fill forecasts come from (rule disk_fill).
func (e *Engine) SetForecasts(f *metrics.Forecasts) {
	e.mu.Lock()
	e.forecasts = f
	e.mu.Unlock()
}

// Observe feeds every pushed report to the restart tracker: container
// restarts happen between evaluation ticks.
func (e *Engine) Observe(serverID int64, reports ...protocol.Report) {
	e.restarts.observe(serverID, e.now(), reports...)
}

// ResetRule drops the pending timers of a rule whose condition changed, so
// time held under the old threshold or duration does not count.
func (e *Engine) ResetRule(ruleID int64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for k := range e.pending {
		if k.rule == ruleID {
			delete(e.pending, k)
		}
	}
}

// Forget drops the in-memory state of a deleted server.
func (e *Engine) Forget(serverID int64) { e.restarts.forget(serverID) }

// Run evaluates rules every Interval until ctx is done.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	purge := time.NewTicker(6 * time.Hour)
	defer purge.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := e.Evaluate(ctx); err != nil {
				if ctx.Err() == nil {
					e.log.Error("alerts: evaluate", "err", err)
				}
			} else {
				e.lastEval.Store(e.now().UnixNano())
			}
		case <-purge.C:
			if err := e.st.PurgeAlerts(ctx, e.now().Add(-keepHistory)); err != nil {
				e.log.Error("alerts: purge", "err", err)
			}
		}
	}
}

// Healthy reports whether Run keeps evaluating rules: the heartbeat's
// self-check. Right after start it waits a few intervals.
func (e *Engine) Healthy() error {
	now := e.now()
	last := e.started
	if v := e.lastEval.Load(); v != 0 {
		last = time.Unix(0, v)
	}
	if d := now.Sub(last); d > 4*Interval {
		return fmt.Errorf("%s", i18n.T("rules not evaluated for %s", d.Round(time.Second).String()))
	}
	return nil
}

// Evaluate checks all rules once.
func (e *Engine) Evaluate(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	rules, err := e.st.ListRules(ctx)
	if err != nil {
		return err
	}
	servers, err := e.st.ListServers(ctx)
	if err != nil {
		return err
	}
	open, err := e.st.OpenAlerts(ctx)
	if err != nil {
		return err
	}
	byID := map[int64]store.Rule{}
	for _, r := range rules {
		byID[r.ID] = r
	}
	openBy := map[condKey]store.Alert{}
	for _, a := range open {
		// Open alerts follow the rule's current channel and severity, for
		// delayed notifications and for the recovery message alike.
		if r, ok := byID[a.RuleID]; ok && (a.Channel != r.Channel || a.Severity != r.Severity) {
			if err := e.st.SetAlertNotify(ctx, a.ID, r.Channel, r.Severity); err != nil {
				return err
			}
			a.Channel, a.Severity = r.Channel, r.Severity
		}
		openBy[condKey{a.RuleID, a.ServerID, a.Subject}] = a
	}
	// Keys that were evaluated this tick, and those whose condition holds
	// long enough. Alerts of keys not evaluated (stale data) stay as they are.
	evaluated := map[condKey]bool{} // value: should be firing
	ruleOK := map[int64]bool{}
	serverOK := map[int64]bool{}
	for _, srv := range servers {
		serverOK[srv.ID] = true
	}
	for _, r := range rules {
		if !r.Enabled || store.EventKind(r.Kind) {
			continue
		}
		ruleOK[r.ID] = true
		for _, srv := range servers {
			conds, ok := e.check(r, srv, now)
			if !ok {
				// No fresh data: open alerts stay, but unobserved time
				// must not count towards a duration.
				for k := range e.pending {
					if k.rule == r.ID && k.server == srv.ID {
						delete(e.pending, k)
					}
				}
				continue
			}
			holds := map[string]cond{}
			for _, c := range conds {
				holds[c.subject] = c
			}
			// Subjects that no longer hold on an evaluated server are false.
			for k := range openBy {
				if k.rule == r.ID && k.server == srv.ID {
					if _, still := holds[k.subject]; !still {
						evaluated[k] = false
					}
				}
			}
			for k := range e.pending {
				if k.rule == r.ID && k.server == srv.ID {
					if _, still := holds[k.subject]; !still {
						delete(e.pending, k)
					}
				}
			}
			wait := r.Duration()
			if r.Kind == store.KindAgentSilent || r.Kind == store.KindContainerRestarts {
				wait = 0 // the duration is part of the condition itself
			}
			for _, c := range conds {
				k := condKey{r.ID, srv.ID, c.subject}
				since, seen := e.pending[k]
				if !seen {
					since = now
					e.pending[k] = now
				}
				if now.Sub(since) < wait {
					continue
				}
				evaluated[k] = true
				if a, isOpen := openBy[k]; isOpen {
					if a.Message != c.message && a.Kind != store.KindAgentSilent {
						_ = e.st.SetAlertMessage(ctx, a.ID, c.message)
						a.Message = c.message
					}
					// Held back by quiet hours or not accepted by the bot
					// (not configured, queue full): try again.
					if a.State == store.AlertFiring && !a.Notified {
						e.notify(ctx, a, false)
					}
					continue
				}
				a, err := e.st.CreateAlert(ctx, store.Alert{
					RuleID: r.ID, Kind: r.Kind, ServerID: srv.ID, ServerName: srv.Name, Subject: c.subject,
					Severity: r.Severity, Channel: r.Channel, State: store.AlertFiring, Message: c.message,
					StartedAt: now.UTC(),
				})
				if err != nil {
					return err
				}
				e.log.Info("alert firing", "id", a.ID, "kind", a.Kind, "server", a.ServerName, "subject", a.Subject)
				e.notify(ctx, a, false)
				if e.onFire != nil {
					e.onFire(a)
				}
			}
		}
	}
	// Timers of conditions whose rule is disabled or deleted, or whose
	// server is gone, start over when they come back.
	for k := range e.pending {
		if !ruleOK[k.rule] || !serverOK[k.server] {
			delete(e.pending, k)
		}
	}
	for k, a := range openBy {
		firing, seen := evaluated[k]
		switch {
		case !ruleOK[k.rule] || !serverOK[k.server]:
			// Rule disabled/deleted or server deleted: close quietly.
			_, _ = e.st.ResolveAlert(ctx, a.ID, now)
		case seen && !firing:
			ok, err := e.st.ResolveAlert(ctx, a.ID, now)
			if err != nil {
				return err
			}
			if ok {
				e.log.Info("alert resolved", "id", a.ID, "kind", a.Kind, "server", a.ServerName)
				ts := now.UTC()
				a.State, a.ResolvedAt = store.AlertResolved, &ts
				if a.Notified {
					// Kept until delivered: quiet hours or a full queue
					// only delay the recovery message.
					if err := e.st.SetResolvePending(ctx, a.ID, true); err != nil {
						return err
					}
					e.notify(ctx, a, true)
				}
			}
		}
	}
	// Events held back by quiet hours (or sent before the bot was set up)
	// go out once they may.
	events, err := e.st.UnnotifiedEvents(ctx, now.Add(-eventRetry))
	if err != nil {
		return err
	}
	for _, a := range events {
		// The rule may have been disabled, deleted or changed meanwhile.
		// Events that are no longer wanted (rule off, web only, silenced)
		// are dropped rather than retried.
		r, ok := byID[a.RuleID]
		if ok && r.Enabled {
			a.Channel, a.Severity = r.Channel, r.Severity
			if e.notify(ctx, a, false) {
				continue
			}
		}
		_ = e.st.SetAlertChannel(ctx, a.ID, store.ChanWeb)
	}
	resolved, err := e.st.PendingResolutions(ctx, now.Add(-eventRetry))
	if err != nil {
		return err
	}
	for _, a := range resolved {
		if r, ok := byID[a.RuleID]; ok {
			a.Channel, a.Severity = r.Channel, r.Severity
		}
		e.notify(ctx, a, true)
	}
	return nil
}

// check evaluates one state rule for one server. ok=false means the data
// needed is missing or stale, so open alerts keep their state.
func (e *Engine) check(r store.Rule, srv store.Server, now time.Time) ([]cond, bool) {
	snap, hasSnap := e.latest.Get(srv.ID)
	rep := snap.Report
	// Both received and collected recently: after an outage the agent first
	// uploads its buffer, oldest reports first, which must not fire or
	// resolve anything. (Agents are expected to keep NTP time.)
	fresh := hasSnap && now.Sub(snap.Received) < staleAfter && now.Sub(rep.Timestamp) < staleAfter
	switch r.Kind {
	case store.KindAgentSilent:
		last := time.Time{}
		if srv.LastSeen != nil {
			last = *srv.LastSeen
		}
		if hasSnap && snap.Received.After(last) {
			last = snap.Received
		}
		if last.IsZero() {
			return nil, false // agent never pushed: nothing to miss yet
		}
		if now.Sub(e.started) < startupGrace {
			// Right after our own restart agents had no one to push to.
			return nil, false
		}
		if quiet := now.Sub(last); quiet > r.Duration() {
			return []cond{{message: i18n.T("agent not responding for %s (last data %s UTC)",
				FormatDuration(quiet), last.UTC().Format("02.01 15:04"))}}, true
		}
		return nil, true
	case store.KindCPU:
		if !fresh {
			return nil, false
		}
		if v := rep.Host.CPUPercent; v > r.Threshold {
			return []cond{{message: fmt.Sprintf("cpu %.0f%% > %.0f%%%s", v, r.Threshold, forText(r))}}, true
		}
		return nil, true
	case store.KindMem:
		if !fresh || rep.Host.MemTotal == 0 {
			return nil, false
		}
		v := float64(rep.Host.MemUsed()) / float64(rep.Host.MemTotal) * 100
		if v > r.Threshold {
			return []cond{{message: i18n.T("memory %.0f%% > %.0f%%%s", v, r.Threshold, forText(r))}}, true
		}
		return nil, true
	case store.KindDisk:
		if !fresh {
			return nil, false
		}
		var out []cond
		for _, d := range rep.Host.Disks {
			if d.Total == 0 {
				continue
			}
			if v := float64(d.Used) / float64(d.Total) * 100; v > r.Threshold {
				out = append(out, cond{subject: d.Mount, message: i18n.T("disk %s is %.0f%% full (> %.0f%%), %s free",
					d.Mount, v, r.Threshold, formatBytes(d.Total-d.Used))})
			}
		}
		return out, true
	case store.KindDiskFill:
		hours, at := e.forecasts.Server(srv.Name)
		if at.IsZero() || now.Sub(at) > forecastStale || !fresh {
			return nil, false
		}
		var out []cond
		for mount, h := range hours {
			if h < r.Threshold {
				out = append(out, cond{subject: mount, message: i18n.T("disk %s will be full in about %s at the current growth rate",
					mount, FormatDuration(time.Duration(h*float64(time.Hour))))})
			}
		}
		return out, true
	case store.KindSmart:
		disks, at := e.latest.Smart(srv.ID)
		if at.IsZero() || now.Sub(at) > smartStale {
			return nil, false
		}
		var out []cond
		for _, d := range disks {
			if why := SmartProblem(d); why != "" {
				name := d.Device
				if d.Model != "" {
					name += " (" + d.Model + ")"
				}
				out = append(out, cond{subject: d.Device, message: i18n.T("disk %s: %s", name, why)})
			}
		}
		return out, true
	case store.KindContainerDown:
		if !fresh || !rep.DockerOK {
			return nil, false
		}
		var out []cond
		for _, c := range rep.Containers {
			if why := containerDown(c); why != "" {
				out = append(out, cond{subject: c.Name, message: i18n.T("container %s %s", c.Name, why)})
			}
		}
		return out, true
	case store.KindContainerRestarts:
		if !fresh || !rep.DockerOK {
			return nil, false
		}
		var out []cond
		for name, n := range e.restarts.counts(srv.ID, now.Add(-r.Duration())) {
			if float64(n) > r.Threshold {
				out = append(out, cond{subject: name, message: i18n.N(n, "container %s restarted %d time in %s", "container %s restarted %d times in %s",
					name, n, FormatDuration(r.Duration()))})
			}
		}
		return out, true
	case store.KindPaymentDue:
		if srv.NextPayment == "" {
			return nil, true
		}
		due, err := time.Parse("2006-01-02", srv.NextPayment)
		if err != nil {
			return nil, false
		}
		today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		days := int(math.Round(due.Sub(today).Hours() / 24))
		if float64(days) > r.Threshold {
			return nil, true
		}
		return []cond{{subject: srv.NextPayment, message: PaymentText(srv, days)}}, true
	}
	return nil, false
}

// SmartProblem returns why a disk's S.M.A.R.T. data is alarming, or "".
// Reallocated sectors alone do not count: many healthy disks carry a few
// for years; pending and uncorrectable sectors mean data at risk now.
func SmartProblem(d protocol.DiskHealth) string {
	if d.Error != "" {
		return ""
	}
	var why []string
	if d.Passed != nil && !*d.Passed {
		why = append(why, i18n.T("S.M.A.R.T. self-assessment FAILED"))
	}
	if d.CriticalWarning != 0 {
		why = append(why, i18n.T("NVMe critical warning 0x%02x", d.CriticalWarning))
	}
	if d.Pending != nil && *d.Pending > 0 {
		why = append(why, i18n.T("%d pending sectors", *d.Pending))
	}
	if d.Uncorrectable != nil && *d.Uncorrectable > 0 {
		why = append(why, i18n.T("%d uncorrectable sectors", *d.Uncorrectable))
	}
	if d.PercentUsed != nil && *d.PercentUsed >= 100 {
		why = append(why, i18n.T("endurance used %d%%", *d.PercentUsed))
	}
	if d.AvailableSpare != nil && d.SpareThreshold != nil && *d.SpareThreshold > 0 && *d.AvailableSpare <= *d.SpareThreshold {
		why = append(why, i18n.T("spare capacity %d%% (threshold %d%%)", *d.AvailableSpare, *d.SpareThreshold))
	}
	return strings.Join(why, ", ")
}

// PaymentText describes an upcoming payment of a server.
func PaymentText(srv store.Server, days int) string {
	sum := ""
	if srv.Cost != nil {
		sum = fmt.Sprintf(" %s %s", trimFloat(*srv.Cost), srv.Currency)
	}
	date := srv.NextPayment
	if t, err := time.Parse("2006-01-02", srv.NextPayment); err == nil {
		date = t.Format("02.01.2006")
	}
	prov := ""
	if srv.Provider != "" {
		prov = " (" + srv.Provider + ")"
	}
	switch {
	case days < 0:
		return i18n.N(-days, "payment%s%s is %d day overdue (%s)", "payment%s%s is %d days overdue (%s)", sum, prov, -days, date)
	case days == 0:
		return i18n.T("payment%s%s is due today (%s)", sum, prov, date)
	}
	return i18n.N(days, "payment%s%s due in %d day (%s)", "payment%s%s due in %d days (%s)", sum, prov, days, date)
}

func forText(r store.Rule) string {
	if r.DurationSec <= 0 {
		return ""
	}
	return " " + i18n.T("for over %s", FormatDuration(r.Duration()))
}

// containerDown returns why a container counts as down, or "".
func containerDown(c protocol.Container) string {
	switch {
	case c.Health == "unhealthy":
		return "unhealthy"
	case c.State == "dead":
		return "dead"
	case c.State == "exited":
		// Exited (0) is a finished one-shot job or a manual stop.
		code := exitCode(c.Status)
		if code != 0 {
			return i18n.T("crashed (exit %d)", code)
		}
	}
	return ""
}

// exitCode parses "Exited (137) 3 minutes ago"; -1 when unknown.
func exitCode(status string) int {
	_, rest, ok := strings.Cut(status, "(")
	if !ok {
		return -1
	}
	num, _, ok := strings.Cut(rest, ")")
	if !ok {
		return -1
	}
	var n int
	if _, err := fmt.Sscanf(num, "%d", &n); err != nil {
		return -1
	}
	return n
}

// Event records a one-off event (fail2ban ban, login, brute force) for every
// enabled rule of its kind and notifies about it.
func (e *Engine) Event(ctx context.Context, kind string, serverID int64, serverName, subject, message string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rules, err := e.st.ListRules(ctx)
	if err != nil {
		e.log.Error("alerts: event", "err", err)
		return
	}
	now := e.now().UTC()
	for _, r := range rules {
		if r.Kind != kind || !r.Enabled {
			continue
		}
		a, err := e.st.CreateAlert(ctx, store.Alert{
			RuleID: r.ID, Kind: kind, ServerID: serverID, ServerName: serverName, Subject: subject,
			Severity: r.Severity, Channel: r.Channel, State: store.AlertEvent, Message: message,
			StartedAt: now, ResolvedAt: &now,
		})
		if err != nil {
			e.log.Error("alerts: event", "err", err)
			return
		}
		e.notify(ctx, a, false)
	}
}

// notify hands a notification to the notifier unless the rule's channel is
// web only, the key is silenced or quiet hours hold it back. It reports
// whether the notification is merely deferred (quiet hours, notifier not
// ready) and worth retrying. Called with e.mu.
func (e *Engine) notify(ctx context.Context, a store.Alert, resolved bool) (deferred bool) {
	deferred = e.deliver(ctx, a, resolved)
	if resolved && !deferred {
		// Sent, or not wanted any more (web only, silenced).
		_ = e.st.SetResolvePending(ctx, a.ID, false)
	}
	return deferred
}

func (e *Engine) deliver(ctx context.Context, a store.Alert, resolved bool) (deferred bool) {
	if e.notifier == nil {
		return true
	}
	if a.Channel != store.ChanTelegram {
		return false
	}
	until, err := e.st.SilencedUntil(ctx, store.SilenceKey{RuleID: a.RuleID, ServerID: a.ServerID, Subject: a.Subject}, e.now())
	if err != nil {
		return true
	}
	if !until.IsZero() {
		return false
	}
	if a.Severity != store.SevCrit {
		if q, err := LoadQuiet(ctx, e.st); err == nil && q.Active(e.now()) {
			return true
		}
	}
	if !e.notifier.Notify(Notification{Alert: a, Resolved: resolved}) {
		return true
	}
	if !resolved {
		_ = e.st.MarkNotified(ctx, a.ID)
	}
	return false
}

// Ack acknowledges a firing alert.
func (e *Engine) Ack(ctx context.Context, id int64) (store.Alert, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, err := e.st.AckAlert(ctx, id, e.now()); err != nil {
		return store.Alert{}, err
	}
	return e.st.GetAlert(ctx, id)
}

// Silence mutes notifications of the alert's rule, server and subject for d.
// A firing alert is acknowledged too.
func (e *Engine) Silence(ctx context.Context, id int64, d time.Duration) (store.Alert, time.Time, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, err := e.st.GetAlert(ctx, id)
	if err != nil {
		return a, time.Time{}, err
	}
	until := e.now().Add(d).UTC()
	if err := e.st.SetSilence(ctx, store.SilenceKey{RuleID: a.RuleID, ServerID: a.ServerID, Subject: a.Subject}, until); err != nil {
		return a, until, err
	}
	if _, err := e.st.AckAlert(ctx, id, e.now()); err != nil {
		return a, until, err
	}
	a, err = e.st.GetAlert(ctx, id)
	return a, until, err
}

// FormatDuration renders 90s as "1m30s", 2h as "2h" (units in the current
// language).
func FormatDuration(d time.Duration) string {
	d = d.Round(time.Second)
	if d <= 0 {
		return i18n.T("%ds", 0)
	}
	var b strings.Builder
	units := []struct {
		d    time.Duration
		name string
	}{{24 * time.Hour, "%dd"}, {time.Hour, "%dh"}, {time.Minute, "%dm"}, {time.Second, "%ds"}}
	parts := 0
	for _, u := range units {
		if d >= u.d && parts < 2 {
			n := d / u.d
			d -= n * u.d
			b.WriteString(i18n.T(u.name, n))
			parts++
		} else if parts > 0 {
			break
		}
	}
	return b.String()
}

func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func trimFloat(f float64) string {
	s := fmt.Sprintf("%.2f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	return s
}
