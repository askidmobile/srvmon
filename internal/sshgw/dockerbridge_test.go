package sshgw

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDockerReadAllowed(t *testing.T) {
	ok := []string{
		"/_ping", "/v1.47/version", "/v1.47/info", "/v1.47/events", "/v1.47/system/df",
		"/v1.47/containers/json", "/v1.47/containers/app/json", "/v1.47/containers/app/logs",
		"/v1.47/containers/app/top", "/v1.47/containers/app/stats", "/v1.47/containers/app/changes",
		"/v1.47/images/json", "/v1.47/images/nginx:1.27/json", "/v1.47/images/ghcr.io/x/y:tag/history",
		"/v1.47/networks", "/v1.47/networks/bridge", "/v1.47/volumes", "/v1.47/volumes/pgdata",
	}
	for _, p := range ok {
		if !DockerReadAllowed("GET", p, "") {
			t.Errorf("want allowed: %s", p)
		}
	}
	bad := [][2]string{
		{"POST", "/v1.47/containers/app/restart"},
		{"POST", "/v1.47/containers/app/exec"},
		{"DELETE", "/v1.47/containers/app"},
		{"GET", "/v1.47/containers/app/export"},    // the container's files
		{"GET", "/v1.47/containers/app/archive"},   // a file from it
		{"GET", "/v1.47/containers/app/attach/ws"}, // its stdin
		{"GET", "/v1.47/images/get"},               // image tarball
		{"GET", "/v1.47/images/nginx/get"},         // image tarball
		{"GET", "/v1.47/images/x/../../containers/app/export/json"},
		{"GET", "/v1.47/containers//json"},
		{"GET", "/v1.47/secrets"},
		{"GET", "/v1.47/plugins"},
	}
	for _, b := range bad {
		if DockerReadAllowed(b[0], b[1], "") {
			t.Errorf("want refused: %s %s", b[0], b[1])
		}
	}
	if DockerReadAllowed("GET", "/v1.47/containers/app/json", "/v1.47/containers/app%2Fjson") {
		t.Error("escaped path allowed")
	}
}

func TestDockerReadHandlerStripsEnv(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1.47/containers/"):
			io.WriteString(w, `{"Name":"/app","Config":{"Image":"x","Env":["API_TOKEN=FAKE123"]},"State":{"Status":"running"}}`)
		case strings.HasPrefix(r.URL.Path, "/v1.47/images/"):
			io.WriteString(w, `{"Id":"sha256:1","Config":{"Env":["SECRET=FAKE456"]},"ContainerConfig":{"Env":["SECRET=FAKE789"]}}`)
		default:
			io.WriteString(w, `{}`)
		}
	}))
	defer upstream.Close()
	addr := strings.TrimPrefix(upstream.URL, "http://")
	h := NewDockerReadHandler(func(ctx context.Context) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp", addr)
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	get := func(method, path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code, rec.Body.String()
	}
	if code, body := get("GET", "/v1.47/containers/app/json"); code != 200 || strings.Contains(body, "FAKE") || !strings.Contains(body, `"running"`) {
		t.Fatalf("container inspect: %d %s", code, body)
	}
	if code, body := get("GET", "/v1.47/images/nginx:1/json"); code != 200 || strings.Contains(body, "FAKE") {
		t.Fatalf("image inspect: %d %s", code, body)
	}
	if code, _ := get("POST", "/v1.47/containers/app/stop"); code != http.StatusForbidden {
		t.Fatalf("POST: %d", code)
	}
	if code, _ := get("GET", "/v1.47/containers/app/export"); code != http.StatusForbidden {
		t.Fatalf("export: %d", code)
	}
}

func TestReaderScripts(t *testing.T) {
	s := readerRootScript("ssh-ed25519 AAAA srvmon-ro")
	for _, want := range []string{"R='srvmon-ro'", "no-agent-forwarding,no-X11-forwarding,no-pty", "gpasswd -d", "reader-ok"} {
		if !strings.Contains(s, want) {
			t.Errorf("reader script lacks %q", want)
		}
	}
	e := ensureReaderScript(ReaderParams{PublicKey: "ssh-ed25519 AAAA", Image: "ghcr.io/x/agent:1"})
	for _, want := range []string{"sudo -n sh -s", "host-sh", "-v /:/host", "--network none", "srvmon:no-access"} {
		if !strings.Contains(e, want) {
			t.Errorf("ensure script lacks %q", want)
		}
	}
}
