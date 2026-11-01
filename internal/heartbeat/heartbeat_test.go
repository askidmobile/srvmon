package heartbeat

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

type hit struct{ method, path, body string }

type receiver struct {
	mu   sync.Mutex
	hits []hit
	code int
}

func (r *receiver) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	b, _ := io.ReadAll(req.Body)
	r.mu.Lock()
	r.hits = append(r.hits, hit{req.Method, req.URL.Path, string(b)})
	code := r.code
	r.mu.Unlock()
	if code == 0 {
		code = http.StatusOK
	}
	w.WriteHeader(code)
}

func (r *receiver) last(t *testing.T) hit {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.hits) == 0 {
		t.Fatal("no ping received")
	}
	return r.hits[len(r.hits)-1]
}

func setup(t *testing.T, checks ...Check) (*Monitor, *receiver, string, *time.Time) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	rcv := &receiver{}
	ts := httptest.NewServer(rcv)
	t.Cleanup(ts.Close)
	m, err := New(context.Background(), Options{Store: st, Secrets: box, Checks: checks, Version: "v1", Client: ts.Client()})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return now }
	return m, rcv, ts.URL + "/ping/0123456789", &now
}

func TestValidateURL(t *testing.T) {
	for in, want := range map[string]string{
		"https://hc-ping.com/abc/":          "https://hc-ping.com/abc",
		" http://10.0.0.2:3001/api/push/x ": "http://10.0.0.2:3001/api/push/x",
	} {
		got, err := ValidateURL(in)
		if err != nil || got != want {
			t.Errorf("%q: %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "hc-ping.com/abc", "ftp://x/y", "https://", "https://a/b#c", "https://u:p@a/b", "https://a/b c"} {
		if _, err := ValidateURL(in); !errors.Is(err, ErrBadURL) {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestMask(t *testing.T) {
	if got := mask("https://hc-ping.com/1f3a9c2e-aaaa-bbbb"); got != "https://hc-ping.com/…bbbb" {
		t.Errorf("mask: %q", got)
	}
	if got := mask("https://kuma.example/api/push/secrettoken?status=up"); strings.Contains(got, "secret") || !strings.HasSuffix(got, "?…") {
		t.Errorf("mask with query: %q", got)
	}
}

func TestPingOK(t *testing.T) {
	m, rcv, u, _ := setup(t)
	ctx := context.Background()
	if err := m.Test(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("test without url: %v", err)
	}
	if err := m.Configure(ctx, &u, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if h := rcv.last(t); h.method != http.MethodGet || h.path != "/ping/0123456789" {
		t.Errorf("ping: %+v", h)
	}
	st := m.Status()
	if !st.Configured || st.LastResult != "ok" || st.LastPing == nil || strings.Contains(st.Target, "0123456789") {
		t.Errorf("status: %+v", st)
	}

	// The URL survives a restart: it is sealed in the store.
	m2, err := New(ctx, Options{Store: m.st, Secrets: m.box})
	if err != nil {
		t.Fatal(err)
	}
	if m2.url != u || !m2.fail {
		t.Errorf("reloaded: %q %v", m2.url, m2.fail)
	}
	raw, _ := m.st.GetSetting(ctx, keyURL)
	if strings.Contains(raw, "ping") {
		t.Error("url stored in clear")
	}
}

func TestFailingCheck(t *testing.T) {
	broken := errors.New("disk full")
	var mu sync.Mutex
	var fail error = broken
	check := Check{Name: "db", Run: func(context.Context) error { mu.Lock(); defer mu.Unlock(); return fail }}
	m, rcv, u, now := setup(t, check)
	ctx := context.Background()
	if err := m.Configure(ctx, &u, true); err != nil {
		t.Fatal(err)
	}
	// A fresh failure is not reported yet.
	if err := m.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if h := rcv.last(t); h.method != http.MethodGet {
		t.Errorf("fresh failure reported: %+v", h)
	}
	*now = now.Add(FailAfter)
	if err := m.Test(ctx); err != nil {
		t.Fatal(err)
	}
	h := rcv.last(t)
	if h.method != http.MethodPost || h.path != "/ping/0123456789/fail" || !strings.Contains(h.body, "db: disk full") {
		t.Errorf("fail ping: %+v", h)
	}
	if st := m.Status(); st.LastResult != "fail" || len(st.Problems) != 1 {
		t.Errorf("status: %+v", st)
	}

	// Without the /fail endpoint the ping is skipped.
	if err := m.Configure(ctx, nil, false); err != nil {
		t.Fatal(err)
	}
	n := len(rcv.hits)
	if err := m.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if len(rcv.hits) != n {
		t.Error("pinged while broken without /fail")
	}
	if st := m.Status(); st.LastResult != "skipped" {
		t.Errorf("status: %+v", st)
	}

	// Recovery: the next beat is a plain ping again.
	mu.Lock()
	fail = nil
	mu.Unlock()
	if err := m.Test(ctx); err != nil {
		t.Fatal(err)
	}
	if h := rcv.last(t); h.method != http.MethodGet {
		t.Errorf("after recovery: %+v", h)
	}
}

func TestPingError(t *testing.T) {
	m, rcv, u, _ := setup(t)
	ctx := context.Background()
	rcv.code = http.StatusNotFound
	if err := m.Configure(ctx, &u, true); err != nil {
		t.Fatal(err)
	}
	if err := m.Test(ctx); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("err = %v", err)
	}
	if st := m.Status(); st.LastResult != "error" || st.LastError == "" {
		t.Errorf("status: %+v", st)
	}
	// Removing the URL stops pings and clears the state.
	empty := ""
	if err := m.Configure(ctx, &empty, true); err != nil {
		t.Fatal(err)
	}
	if st := m.Status(); st.Configured || st.LastResult != "" {
		t.Errorf("after removal: %+v", st)
	}
}

func TestNoSecrets(t *testing.T) {
	st, _ := store.Open(":memory:")
	defer st.Close()
	m, err := New(context.Background(), Options{Store: st})
	if err != nil {
		t.Fatal(err)
	}
	u := "https://hc-ping.com/x"
	if err := m.Configure(context.Background(), &u, true); !errors.Is(err, ErrNoSecrets) {
		t.Errorf("err = %v", err)
	}
}
