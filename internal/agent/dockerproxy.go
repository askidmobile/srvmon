package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"
)

// The docker proxy runs as its own container (srvmon-dockerproxy) next to
// the agent. It alone mounts the host's docker socket and serves the agent a
// unix socket with the few read-only Docker Engine API calls the agent makes.
// Like docker-socket-proxy, but built into the agent image, so a server
// pulls nothing new, and it also strips environment variables (secrets)
// from container inspect.

// dockerProxyPath matches the endpoints the agent uses, with an optional
// API version prefix: /_ping, /version, /containers/json and
// /containers/{id}/json|logs. Container ids and names only.
var dockerProxyPath = regexp.MustCompile(`^(/v1\.[0-9]{1,3})?/(_ping|version|containers/json|containers/[A-Za-z0-9][A-Za-z0-9_.-]{0,127}/(json|logs))$`)

var inspectPath = regexp.MustCompile(`^(/v1\.[0-9]{1,3})?/containers/[^/]+/json$`)

// maxInspect bounds a container inspect answer the proxy rewrites.
const maxInspect = 8 << 20

// DockerProxyAllowed reports whether the proxy forwards a request.
func DockerProxyAllowed(method, path, rawPath string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	// Escaped paths (%2F and friends) are refused outright: what the proxy
	// checks must be what the daemon routes.
	if rawPath != "" {
		return false
	}
	return dockerProxyPath.MatchString(path)
}

// NewDockerProxy returns the filtering handler in front of the docker socket
// at upstream.
func NewDockerProxy(upstream string, log *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "docker"
			pr.Out.Host = "docker"
			// No connection upgrades (attach/exec style hijacks).
			pr.Out.Header.Del("Upgrade")
			pr.Out.Header.Del("Connection")
		},
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", upstream)
			},
			ResponseHeaderTimeout: 30 * time.Second,
		},
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK || !inspectPath.MatchString(resp.Request.URL.Path) {
				return nil
			}
			return stripInspectEnv(resp)
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("docker proxy", "path", r.URL.Path, "err", err)
			http.Error(w, `{"message":"docker proxy: upstream error"}`, http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !DockerProxyAllowed(r.Method, r.URL.Path, r.URL.RawPath) {
			log.Warn("docker proxy: refused", "method", r.Method, "path", r.URL.EscapedPath())
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"refused by srvmon-dockerproxy"}`)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// stripInspectEnv removes Config.Env from a container inspect answer.
func stripInspectEnv(resp *http.Response) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxInspect+1))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if len(b) > maxInspect {
		return errors.New("container inspect is too large")
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("container inspect: %w", err)
	}
	if raw, ok := v["Config"]; ok && string(raw) != "null" {
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("container inspect config: %w", err)
		}
		delete(cfg, "Env")
		if v["Config"], err = json.Marshal(cfg); err != nil {
			return err
		}
	}
	out, err := json.Marshal(v)
	if err != nil {
		return err
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", strconv.Itoa(len(out)))
	resp.Header.Del("Transfer-Encoding")
	return nil
}

// ServeDockerProxy listens on the unix socket listen (root-only, 0600) and
// forwards allowed requests to the docker socket upstream until ctx ends.
func ServeDockerProxy(ctx context.Context, listen, upstream string, log *slog.Logger) error {
	if err := setNonDumpable(); err != nil {
		return fmt.Errorf("prctl PR_SET_DUMPABLE: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(listen), 0o755); err != nil {
		return err
	}
	if err := os.Remove(listen); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	ln, err := net.Listen("unix", listen)
	if err != nil {
		return err
	}
	if err := os.Chmod(listen, 0o600); err != nil {
		ln.Close()
		return err
	}
	srv := &http.Server{
		Handler:           NewDockerProxy(upstream, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	log.Info("srvmon docker proxy listening", "listen", listen, "upstream", upstream)
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
