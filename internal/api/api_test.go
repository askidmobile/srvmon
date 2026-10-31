package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/config"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

type env struct {
	srv     *Server
	ts      *httptest.Server
	client  *http.Client
	secret  string
	authLog *bytes.Buffer
	st      *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hash, _ := auth.HashPassword("hunter2")
	secret, _ := auth.NewTOTPSecret()
	if err := st.SetCredentials(context.Background(), hash, secret, []string{auth.HashToken("aaaa-bbbb-cccc")}); err != nil {
		t.Fatal(err)
	}
	logBuf := &bytes.Buffer{}
	web := fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}}
	s := New(Options{
		Config:  config.Config{SessionTTL: time.Hour},
		Store:   st,
		AuthLog: logBuf,
		Web:     web,
		Version: "test",
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	jar, _ := cookiejar.New(nil)
	return &env{srv: s, ts: ts, client: &http.Client{Jar: jar}, secret: secret, authLog: logBuf, st: st}
}

func (e *env) post(t *testing.T, path string, body any) *http.Response {
	t.Helper()
	b, _ := json.Marshal(body)
	resp, err := e.client.Post(e.ts.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func (e *env) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := e.client.Get(e.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLoginFlow(t *testing.T) {
	e := newEnv(t)
	if r := e.get(t, "/api/servers"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("servers before login: %d", r.StatusCode)
	}
	if r := e.post(t, "/api/auth/totp", map[string]string{"code": "000000"}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("totp without password must fail: %d", r.StatusCode)
	}
	if r := e.post(t, "/api/auth/password", map[string]string{"password": "hunter2"}); r.StatusCode != http.StatusOK {
		t.Fatalf("password: %d", r.StatusCode)
	}
	code, _ := auth.TOTPCode(e.secret, auth.TOTPStep(time.Now()))
	if r := e.post(t, "/api/auth/totp", map[string]string{"code": code}); r.StatusCode != http.StatusOK {
		t.Fatalf("totp: %d", r.StatusCode)
	}
	if r := e.get(t, "/api/servers"); r.StatusCode != http.StatusOK {
		t.Fatalf("servers after login: %d", r.StatusCode)
	}
	// Same code again (new password step) must be rejected as a replay.
	e.post(t, "/api/auth/password", map[string]string{"password": "hunter2"})
	if r := e.post(t, "/api/auth/totp", map[string]string{"code": code}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replayed totp: %d", r.StatusCode)
	}
	// Recovery code works once.
	e.post(t, "/api/auth/password", map[string]string{"password": "hunter2"})
	if r := e.post(t, "/api/auth/totp", map[string]string{"code": "AAAA-BBBB-CCCC"}); r.StatusCode != http.StatusOK {
		t.Fatalf("recovery code: %d", r.StatusCode)
	}
	if r := e.post(t, "/api/auth/logout", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", r.StatusCode)
	}
	if r := e.get(t, "/api/servers"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("servers after logout: %d", r.StatusCode)
	}
}

func TestBruteForceIsLimitedAndLogged(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		if r := e.post(t, "/api/auth/password", map[string]string{"password": "nope"}); r.StatusCode != http.StatusUnauthorized {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	if r := e.post(t, "/api/auth/password", map[string]string{"password": "hunter2"}); r.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after 5 failures, got %d", r.StatusCode)
	}
	lines := strings.Count(e.authLog.String(), "srvmon auth failure ip=127.0.0.1 stage=password")
	if lines != 5 {
		t.Fatalf("fail2ban log lines = %d:\n%s", lines, e.authLog.String())
	}
}

func TestCrossOriginRejected(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/auth/password", strings.NewReader(`{"password":"hunter2"}`))
	req.Header.Set("Origin", "https://evil.example")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-origin post: %d", resp.StatusCode)
	}
}

func TestIngest(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "shop-db"})
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("agent-token"))

	body, _ := json.Marshal([]protocol.Report{{Hostname: "shop-db", Host: protocol.HostMetrics{CPUPercent: 42}}})
	send := func(token string) int {
		req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/ingest", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := send("wrong"); c != http.StatusUnauthorized {
		t.Fatalf("bad token: %d", c)
	}
	if c := send("agent-token"); c != http.StatusOK {
		t.Fatalf("ingest: %d", c)
	}
	snap, ok := e.srv.latest.Get(id)
	if !ok || snap.Report.Host.CPUPercent != 42 {
		t.Fatalf("snapshot not stored: %+v", snap)
	}
}

func TestSPAFallback(t *testing.T) {
	e := newEnv(t)
	r := e.get(t, "/servers/shop-db")
	defer r.Body.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r.Body)
	if r.StatusCode != http.StatusOK || !strings.Contains(buf.String(), "app") {
		t.Fatalf("spa fallback: %d %q", r.StatusCode, buf.String())
	}
	if r := e.get(t, "/api/unknown"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown api must 404: %d", r.StatusCode)
	}
}
