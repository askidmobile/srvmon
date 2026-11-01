// Package heartbeat is the panel's dead man's switch: once a minute srvmon
// checks itself and pings an external URL (healthchecks.io, an Uptime Kuma
// push monitor, a second server). When the panel dies the pings stop and the
// external service raises the alarm through its own channels, which do not
// depend on the panel or on Telegram being reachable from it.
//
// While the panel runs but part of it is broken (database, alert engine,
// VictoriaMetrics, Telegram bot), it reports the problems to <url>/fail —
// the healthchecks.io convention — or, with that turned off, skips the ping
// so the external service fires after its grace period.
package heartbeat

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

const (
	// Interval between pings. The external check's period should match it.
	Interval = time.Minute
	// FailAfter: a self-check must fail this long before it is reported, so
	// a Telegram reconnect or one slow VictoriaMetrics answer does not page.
	FailAfter = 3 * time.Minute
	// pingTimeout bounds one request to the external URL.
	pingTimeout = 15 * time.Second
	// checkTimeout bounds all self-checks of one beat.
	checkTimeout = 10 * time.Second

	keyURL  = "heartbeat_url"  // sealed with the master key, base64
	keyFail = "heartbeat_fail" // "0": do not use <url>/fail
)

var urlAAD = []byte("srvmon heartbeat url")

// Errors returned by Configure and Test.
var (
	ErrNoSecrets     = errors.New("heartbeat: no master key")
	ErrNotConfigured = errors.New("heartbeat: url not set")
	ErrBadURL        = errors.New("heartbeat: bad url")
)

// Check is one self-check; a non-nil error is a problem.
type Check struct {
	Name string
	Run  func(ctx context.Context) error
}

// Status is what the settings screen shows.
type Status struct {
	Configured   bool       `json:"configured"`
	Target       string     `json:"target,omitempty"` // URL with its secret part hidden
	FailEndpoint bool       `json:"fail_endpoint"`
	LastPing     *time.Time `json:"last_ping,omitempty"`   // last request that reached the target
	LastResult   string     `json:"last_result,omitempty"` // ok | fail | skipped | error
	LastError    string     `json:"last_error,omitempty"`
	Problems     []string   `json:"problems,omitempty"` // reported self-check failures
}

// Options for New.
type Options struct {
	Store   *store.Store
	Secrets *secret.Box
	Checks  []Check
	Version string
	Log     *slog.Logger
	Client  *http.Client
}

// Monitor sends the heartbeat. Run starts it; it idles until a URL is set.
type Monitor struct {
	st      *store.Store
	box     *secret.Box
	checks  []Check
	version string
	log     *slog.Logger
	client  *http.Client
	now     func() time.Time

	beatMu sync.Mutex // one beat at a time (ticker and the test button)
	mu     sync.Mutex
	url    string
	fail   bool
	since  map[string]time.Time // check name → failing since
	status Status
	reload chan struct{}
}

// defaultClient uses classic key exchange only, like the Telegram client:
// some networks drop the two-packet ClientHello of post-quantum TLS.
func defaultClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
	}
	return &http.Client{Timeout: pingTimeout, Transport: tr}
}

// New creates the monitor and loads its settings.
func New(ctx context.Context, o Options) (*Monitor, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Client == nil {
		o.Client = defaultClient()
	}
	m := &Monitor{
		st: o.Store, box: o.Secrets, checks: o.Checks, version: o.Version, log: o.Log, client: o.Client,
		now: time.Now, since: map[string]time.Time{}, reload: make(chan struct{}, 1),
	}
	if err := m.load(ctx); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Monitor) load(ctx context.Context) error {
	var u string
	if v, err := m.st.GetSetting(ctx, keyURL); err == nil && v != "" && m.box != nil {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err == nil {
			pt, err := m.box.Open(raw, urlAAD)
			if err != nil {
				m.log.Error("heartbeat: cannot decrypt the ping URL; set it again in the panel")
			} else {
				u = string(pt)
			}
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	fail := true
	if v, err := m.st.GetSetting(ctx, keyFail); err == nil {
		fail = v != "0"
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	m.mu.Lock()
	if u != m.url {
		m.status.LastPing, m.status.LastResult, m.status.LastError = nil, "", ""
	}
	m.url, m.fail = u, fail
	m.status.Configured = u != ""
	m.status.Target = mask(u)
	m.status.FailEndpoint = fail
	m.mu.Unlock()
	return nil
}

// ValidateURL normalizes a ping URL: http(s), a host, no fragment.
func ValidateURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) > 500 || strings.ContainsAny(raw, " \t\r\n") {
		return "", ErrBadURL
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.Fragment != "" || u.User != nil {
		return "", ErrBadURL
	}
	return strings.TrimRight(u.String(), "/"), nil
}

// mask hides the secret part of a ping URL: the path and query usually are
// the token (hc-ping.com/<uuid>, /api/push/<token>).
func mask(raw string) string {
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "…"
	}
	p := strings.TrimPrefix(u.Path, "/")
	tail := ""
	if n := len(p); n > 4 {
		tail = "…" + p[n-4:]
	} else if n > 0 {
		tail = "…"
	}
	if u.RawQuery != "" {
		tail += "?…"
	}
	return u.Scheme + "://" + u.Host + "/" + tail
}

// Status returns the current state.
func (m *Monitor) Status() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.status
	s.Problems = append([]string(nil), s.Problems...)
	return s
}

// Configure stores a new URL (nil keeps the current one, "" removes it) and
// whether failures go to <url>/fail.
func (m *Monitor) Configure(ctx context.Context, rawURL *string, failEndpoint bool) error {
	if rawURL != nil && *rawURL != "" {
		u, err := ValidateURL(*rawURL)
		if err != nil {
			return err
		}
		if m.box == nil {
			return ErrNoSecrets
		}
		sealed, err := m.box.Seal([]byte(u), urlAAD)
		if err != nil {
			return err
		}
		if err := m.st.SetSetting(ctx, keyURL, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			return err
		}
	} else if rawURL != nil {
		if err := m.st.SetSetting(ctx, keyURL, ""); err != nil {
			return err
		}
	}
	f := "1"
	if !failEndpoint {
		f = "0"
	}
	if err := m.st.SetSetting(ctx, keyFail, f); err != nil {
		return err
	}
	if err := m.load(ctx); err != nil {
		return err
	}
	select {
	case m.reload <- struct{}{}:
	default:
	}
	return nil
}

// Run beats every Interval until ctx is done; the first beat goes out right
// away so a restart shows up as soon as the panel is back.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(Interval)
	defer t.Stop()
	for {
		m.beat(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.reload:
			t.Reset(Interval)
		}
	}
}

// Test runs one beat now and returns its delivery error.
func (m *Monitor) Test(ctx context.Context) error {
	m.mu.Lock()
	configured := m.url != ""
	m.mu.Unlock()
	if !configured {
		return ErrNotConfigured
	}
	return m.beat(ctx)
}

// problems runs the self-checks and returns those failing for FailAfter.
func (m *Monitor) problems(ctx context.Context) []string {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	now := m.now()
	results := make([]error, len(m.checks))
	var wg sync.WaitGroup
	for i, c := range m.checks {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = c.Run(ctx)
		}()
	}
	wg.Wait()
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for i, c := range m.checks {
		if results[i] == nil {
			delete(m.since, c.Name)
			continue
		}
		if _, ok := m.since[c.Name]; !ok {
			m.since[c.Name] = now
		}
		if now.Sub(m.since[c.Name]) >= FailAfter {
			out = append(out, c.Name+": "+results[i].Error())
		}
	}
	sort.Strings(out)
	return out
}

func (m *Monitor) beat(ctx context.Context) error {
	m.beatMu.Lock()
	defer m.beatMu.Unlock()
	problems := m.problems(ctx)
	m.mu.Lock()
	target, useFail := m.url, m.fail
	m.status.Problems = problems
	m.mu.Unlock()
	if target == "" {
		return nil
	}
	result := "ok"
	var err error
	switch {
	case len(problems) == 0:
		err = m.ping(ctx, http.MethodGet, target, "")
	case useFail:
		result = "fail"
		body := i18n.T("srvmon %s: problems:", m.version) + "\n" + strings.Join(problems, "\n")
		err = m.ping(ctx, http.MethodPost, target+"/fail", body)
	default:
		result = "skipped"
	}
	now := m.now()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.url != target { // reconfigured during the request
		return err
	}
	if err != nil {
		m.status.LastResult, m.status.LastError = "error", err.Error()
		m.log.Warn("heartbeat: ping failed", "err", err)
		return err
	}
	m.status.LastResult, m.status.LastError = result, ""
	if result != "skipped" {
		m.status.LastPing = &now
	}
	return nil
}

func (m *Monitor) ping(ctx context.Context, method, target, body string) error {
	ctx, cancel := context.WithTimeout(ctx, pingTimeout)
	defer cancel()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "srvmon/"+m.version)
	if body != "" {
		req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	}
	resp, err := m.client.Do(req)
	if err != nil {
		// Do not echo the URL: it carries the check's secret.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("%s", i18n.T("no connection: %s", err.Error()))
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
