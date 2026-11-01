package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDockerProxyAllowed(t *testing.T) {
	cases := []struct {
		method, path, raw string
		want              bool
	}{
		{"GET", "/_ping", "", true},
		{"HEAD", "/_ping", "", true},
		{"GET", "/version", "", true},
		{"GET", "/v1.43/version", "", true},
		{"GET", "/containers/json", "", true},
		{"GET", "/v1.45/containers/abc123/json", "", true},
		{"GET", "/containers/my_app-1.web/logs", "", true},
		{"POST", "/containers/abc/restart", "", false},
		{"POST", "/containers/create", "", false},
		{"DELETE", "/containers/abc", "", false},
		{"GET", "/containers/abc/export", "", false},
		{"GET", "/containers/abc/archive", "", false},
		{"GET", "/images/json", "", false},
		{"GET", "/info", "", false},
		{"GET", "/events", "", false},
		{"GET", "/secrets", "", false},
		{"GET", "/containers/../images/json", "", false},
		{"GET", "/containers/a/b/json", "/containers/a%2Fb/json", false},
		{"GET", "/v2/version", "", false},
	}
	for _, c := range cases {
		if got := DockerProxyAllowed(c.method, c.path, c.raw); got != c.want {
			t.Errorf("%s %s = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// fakeDocker serves a docker-like API on a unix socket.
func fakeDocker(t *testing.T) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /containers/{id}/json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Id":"`+r.PathValue("id")+`","RestartCount":3,"Config":{"Image":"x","Env":["DB_PASSWORD=hunter2"]}}`)
	})
	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"Version":"29.0.0"}`)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("upstream got %s %s", r.Method, r.URL.Path)
	})
	// The daemon accepts every path with or without a version prefix.
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/v1.43")
		mux.ServeHTTP(w, r)
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	return sock
}

func TestDockerProxy(t *testing.T) {
	up := fakeDocker(t)
	listen := filepath.Join(t.TempDir(), "run", "docker.sock")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- ServeDockerProxy(ctx, listen, up, slog.New(slog.DiscardHandler)) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	d := NewDocker(listen)
	var err error
	for i := 0; i < 50; i++ {
		var v string
		if v, err = d.Version(context.Background()); err == nil {
			if v != "29.0.0" {
				t.Fatalf("version %q", v)
			}
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	n, err := d.RestartCount(context.Background(), "abc")
	if err != nil || n != 3 {
		t.Fatalf("restarts %d %v", n, err)
	}
	var v map[string]any
	if err := d.getJSON(context.Background(), "/v1.43/containers/abc/json", &v); err != nil {
		t.Fatal(err)
	}
	cfg, _ := v["Config"].(map[string]any)
	if _, ok := cfg["Env"]; ok || cfg["Image"] != "x" {
		t.Fatalf("Env must be stripped, the rest kept: %v", v)
	}
	req, _ := http.NewRequest(http.MethodPost, "http://docker/containers/abc/restart", nil)
	resp, err := d.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("POST got %d", resp.StatusCode)
	}
	var e struct{ Message string }
	req, _ = http.NewRequest(http.MethodGet, "http://docker/info", nil)
	resp, err = d.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = json.NewDecoder(resp.Body).Decode(&e)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || e.Message == "" {
		t.Fatalf("GET /info got %d %q", resp.StatusCode, e.Message)
	}
}
