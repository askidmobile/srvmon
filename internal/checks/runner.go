package checks

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/store"
)

const (
	// Interval is how often every target is checked.
	Interval = time.Minute
	// retryAfter: a failed check is repeated once before it counts as down.
	retryAfter = 3 * time.Second
	// parallel caps concurrent checks.
	parallel = 8
	// Domain registrations change rarely: look them up twice a day, and
	// retry failed lookups hourly.
	domainEvery = 12 * time.Hour
	domainRetry = time.Hour
	domainTick  = 5 * time.Minute
)

// Lookup finds the expiry of a domain registration.
type Lookup interface {
	Expiry(ctx context.Context, domain string) (time.Time, string, error)
}

// Runner checks the targets of all servers. Create it with New and start Run.
type Runner struct {
	st     *store.Store
	vm     *metrics.Writer
	lookup Lookup
	log    *slog.Logger
	now    func() time.Time
	probe  func(ctx context.Context, t Target) Result
	retry  time.Duration

	kick       chan struct{}
	domainKick chan struct{}

	mu      sync.Mutex
	list    []store.Check
	loaded  bool
	results map[int64]Result
	domains map[string]*domainState
}

type domainState struct {
	info DomainInfo
	done bool // at least one lookup finished (with or without a date)
	next time.Time
}

// New returns a runner. vm may be nil.
func New(st *store.Store, vm *metrics.Writer, log *slog.Logger) *Runner {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Runner{
		st: st, vm: vm, lookup: NewDomainLookup(), log: log, now: time.Now, probe: probe, retry: retryAfter,
		kick: make(chan struct{}, 1), domainKick: make(chan struct{}, 1),
		results: map[int64]Result{}, domains: map[string]*domainState{},
	}
}

// Kick runs the checks now (after targets changed).
func (r *Runner) Kick() {
	select {
	case r.kick <- struct{}{}:
	default:
	}
}

// Run checks every Interval and looks up domains until ctx is done.
func (r *Runner) Run(ctx context.Context) {
	go r.runDomains(ctx)
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		r.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.kick:
		}
	}
}

// Reload reads the targets from the store, so State shows a change at once.
func (r *Runner) Reload(ctx context.Context) ([]store.Check, error) {
	list, err := r.st.ListChecks(ctx)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	r.list, r.loaded = list, true
	ids := map[int64]bool{}
	for _, c := range list {
		ids[c.ID] = true
	}
	for id := range r.results {
		if !ids[id] {
			delete(r.results, id)
		}
	}
	r.mu.Unlock()
	select {
	case r.domainKick <- struct{}{}:
	default:
	}
	return list, nil
}

// RunOnce reloads the targets and checks all of them.
func (r *Runner) RunOnce(ctx context.Context) {
	list, err := r.Reload(ctx)
	if err != nil {
		if ctx.Err() == nil {
			r.log.Error("checks: list", "err", err)
		}
		return
	}
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for _, c := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(c store.Check) {
			defer wg.Done()
			defer func() { <-sem }()
			res := r.checkOne(ctx, c)
			if ctx.Err() != nil {
				return
			}
			r.mu.Lock()
			defer r.mu.Unlock()
			if !r.has(c.ID) {
				return // removed while it was being checked
			}
			prev, had := r.results[c.ID]
			if had && res.Cert == nil {
				// A timeout says nothing about the certificate: keep the
				// last one seen, so its expiry alert does not flap.
				res.Cert = prev.Cert
			}
			if had && !prev.Up && !res.Up && prev.DownSince != nil {
				res.DownSince = prev.DownSince
			} else if !res.Up {
				ts := res.Checked
				res.DownSince = &ts
			}
			r.results[c.ID] = res
		}(c)
	}
	wg.Wait()
	r.writeMetrics(ctx, list)
}

// has reports whether a check is in the current list. Called with r.mu.
func (r *Runner) has(id int64) bool {
	for _, c := range r.list {
		if c.ID == id {
			return true
		}
	}
	return false
}

func (r *Runner) checkOne(ctx context.Context, c store.Check) Result {
	t, err := Parse(c.Target)
	if err != nil {
		return Result{Error: err.Error(), Checked: r.now().UTC()}
	}
	res := r.probe(ctx, t)
	if res.Up || ctx.Err() != nil {
		return res
	}
	select {
	case <-ctx.Done():
		return res
	case <-time.After(r.retry):
	}
	return r.probe(ctx, t)
}

func (r *Runner) writeMetrics(ctx context.Context, list []store.Check) {
	if r.vm == nil || len(list) == 0 {
		return
	}
	servers, err := r.st.ListServers(ctx)
	if err != nil {
		return
	}
	names := map[int64]string{}
	for _, s := range servers {
		names[s.ID] = s.Name
	}
	var b bytes.Buffer
	r.mu.Lock()
	for _, c := range list {
		res, ok := r.results[c.ID]
		name, known := names[c.ServerID]
		if !ok || !known {
			continue
		}
		ts := res.Checked.UnixMilli()
		labels := fmt.Sprintf(`{server="%s",target="%s"}`, metrics.EscapeLabel(name), metrics.EscapeLabel(c.Target))
		up := 0
		if res.Up {
			up = 1
		}
		fmt.Fprintf(&b, "srvmon_check_up%s %d %d\n", labels, up, ts)
		if res.Up {
			fmt.Fprintf(&b, "srvmon_check_latency_ms%s %d %d\n", labels, res.LatencyMS, ts)
		}
		if res.Cert != nil {
			fmt.Fprintf(&b, "srvmon_check_cert_days%s %g %d\n", labels,
				res.Cert.NotAfter.Sub(res.Checked).Hours()/24, ts)
		}
	}
	r.mu.Unlock()
	if err := r.vm.WriteText(ctx, b.Bytes()); err != nil && ctx.Err() == nil {
		r.log.Warn("checks: write metrics", "err", err)
	}
}

// --- domains ---

func (r *Runner) runDomains(ctx context.Context) {
	t := time.NewTicker(domainTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-r.domainKick:
		}
		r.LookupDomains(ctx)
	}
}

// LookupDomains refreshes the domains whose lookup is due.
func (r *Runner) LookupDomains(ctx context.Context) {
	r.mu.Lock()
	want := map[string]bool{}
	for _, c := range r.list {
		if t, err := Parse(c.Target); err == nil {
			if d := t.Domain(); d != "" {
				want[d] = true
			}
		}
	}
	for d := range r.domains {
		if !want[d] {
			delete(r.domains, d)
		}
	}
	now := r.now()
	var due []string
	for d := range want {
		st, ok := r.domains[d]
		if !ok {
			st = &domainState{info: DomainInfo{Domain: d}}
			r.domains[d] = st
		}
		if !now.Before(st.next) {
			due = append(due, d)
		}
	}
	r.mu.Unlock()
	sort.Strings(due)
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, time.Minute)
		exp, src, err := r.lookup.Expiry(lctx, d)
		cancel()
		if ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		st, ok := r.domains[d]
		if ok {
			st.done = true
			st.info.Checked = r.now().UTC()
			st.info.Source = src
			if err != nil {
				// Keep the last known date: a registry outage does not
				// change when the domain expires.
				st.info.Error = err.Error()
				st.next = r.now().Add(domainRetry)
			} else {
				e := exp.UTC()
				st.info.Expires, st.info.Error = &e, ""
				st.next = r.now().Add(domainEvery)
			}
		}
		r.mu.Unlock()
		if err != nil {
			r.log.Info("checks: domain lookup", "domain", d, "err", err)
		}
	}
}

// --- views ---

// CheckState is a target with its latest result.
type CheckState struct {
	ID     int64   `json:"id"`
	Target string  `json:"target"`
	Kind   string  `json:"kind"`
	TLS    bool    `json:"tls"`
	Domain string  `json:"domain,omitempty"`
	Result *Result `json:"result,omitempty"`
}

// ServerState is what is known about the checks of one server.
type ServerState struct {
	Checks  []CheckState `json:"checks"`
	Domains []DomainInfo `json:"domains"`
	// Ready: every check has a result (the runner has loaded the current
	// targets and checked them at least once).
	Ready bool `json:"-"`
	// DomainsReady: every domain had a lookup.
	DomainsReady bool `json:"-"`
}

// State returns the checks of a server with their latest results, in the
// order they were added.
func (r *Runner) State(serverID int64) ServerState {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := ServerState{Checks: []CheckState{}, Domains: []DomainInfo{}, Ready: r.loaded, DomainsReady: r.loaded}
	seen := map[string]bool{}
	for _, c := range r.list {
		if c.ServerID != serverID {
			continue
		}
		cs := CheckState{ID: c.ID, Target: c.Target}
		if t, err := Parse(c.Target); err == nil {
			cs.Kind, cs.TLS, cs.Domain = t.Kind, t.TLS, t.Domain()
		}
		if res, ok := r.results[c.ID]; ok {
			res := res
			cs.Result = &res
		} else {
			st.Ready = false
		}
		st.Checks = append(st.Checks, cs)
		if cs.Domain != "" && !seen[cs.Domain] {
			seen[cs.Domain] = true
			if d, ok := r.domains[cs.Domain]; ok && d.done {
				st.Domains = append(st.Domains, d.info)
			} else {
				st.DomainsReady = false
			}
		}
	}
	return st
}

// Summary counts the checks of a server and those that are down.
func (r *Runner) Summary(serverID int64) (total, down int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.list {
		if c.ServerID != serverID {
			continue
		}
		total++
		if res, ok := r.results[c.ID]; ok && !res.Up {
			down++
		}
	}
	return total, down
}

// Domains returns every domain looked up so far, for the payments overview.
func (r *Runner) Domains() []DomainInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []DomainInfo{}
	for _, d := range r.domains {
		if d.done {
			out = append(out, d.info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Domain < out[j].Domain })
	return out
}
