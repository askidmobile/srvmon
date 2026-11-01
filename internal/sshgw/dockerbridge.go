package sshgw

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
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// The docker bridge gives ReaderUser, which is not in the docker group, a
// read-only Docker Engine API for the AI agent's commands. sshd creates a
// unix socket on the server for the reader's session (a streamlocal remote
// forward, mode 0600, owned by the reader); the docker CLI there talks to it
// through DOCKER_HOST, and the panel answers each request itself: only GET
// and HEAD on the endpoints below, environment variables removed from
// container and image inspect, and the allowed ones forwarded to the
// daemon over the panel SSH user's own connection. Endpoints that hand out
// file contents (export, archive, images/get), attach and exec are never
// reached.

var dockerReadPath = regexp.MustCompile(`^(/v1\.[0-9]{1,3})?/(` +
	`_ping|version|info|events|system/df|containers/json|images/json|networks|volumes|` +
	`containers/[A-Za-z0-9][A-Za-z0-9_.-]{0,127}/(json|logs|top|changes|stats)|` +
	`images/[A-Za-z0-9][A-Za-z0-9_.:@/-]{0,255}/(json|history)|` +
	`networks/[A-Za-z0-9][A-Za-z0-9_.-]{0,127}|volumes/[A-Za-z0-9][A-Za-z0-9_.-]{0,127})$`)

var (
	containerInspect = regexp.MustCompile(`^(/v1\.[0-9]{1,3})?/containers/[^/]+/json$`)
	imageInspect     = regexp.MustCompile(`^(/v1\.[0-9]{1,3})?/images/.+/json$`)
)

const maxDockerInspect = 8 << 20

// DockerReadAllowed reports whether the bridge forwards a request.
func DockerReadAllowed(method, path, rawPath string) bool {
	if method != http.MethodGet && method != http.MethodHead {
		return false
	}
	// What the bridge checks must be what the daemon routes: no escapes,
	// no dot segments, no empty segments.
	if rawPath != "" || strings.Contains(path, "..") || strings.Contains(path, "//") || strings.Contains(path, "/./") {
		return false
	}
	return dockerReadPath.MatchString(path)
}

// NewDockerReadHandler returns the filtering handler; dial reaches the
// docker daemon's socket.
func NewDockerReadHandler(dial func(ctx context.Context) (net.Conn, error), log *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = "docker"
			pr.Out.Host = "docker"
			pr.Out.Header.Del("Upgrade")
			pr.Out.Header.Del("Connection")
		},
		Transport: &http.Transport{
			DialContext:           func(ctx context.Context, _, _ string) (net.Conn, error) { return dial(ctx) },
			ResponseHeaderTimeout: 30 * time.Second,
			DisableKeepAlives:     true,
		},
		FlushInterval: -1, // logs -f, events, stats stream
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode != http.StatusOK {
				return nil
			}
			p := resp.Request.URL.Path
			switch {
			case containerInspect.MatchString(p):
				return stripEnv(resp, "Config")
			case imageInspect.MatchString(p):
				return stripEnv(resp, "Config", "ContainerConfig")
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn("docker bridge", "path", r.URL.Path, "err", err)
			http.Error(w, `{"message":"srvmon docker bridge: upstream error"}`, http.StatusBadGateway)
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !DockerReadAllowed(r.Method, r.URL.Path, r.URL.RawPath) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"refused by srvmon: the AI agent's read-only docker access allows only reading"}`)
			return
		}
		rp.ServeHTTP(w, r)
	})
}

// stripEnv removes Env from the named objects of an inspect answer.
func stripEnv(resp *http.Response, keys ...string) error {
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDockerInspect+1))
	resp.Body.Close()
	if err != nil {
		return err
	}
	if len(b) > maxDockerInspect {
		return errors.New("inspect answer is too large")
	}
	var v map[string]json.RawMessage
	if err := json.Unmarshal(b, &v); err != nil {
		return fmt.Errorf("inspect: %w", err)
	}
	for _, k := range keys {
		raw, ok := v[k]
		if !ok || string(raw) == "null" {
			continue
		}
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("inspect %s: %w", k, err)
		}
		delete(cfg, "Env")
		if v[k], err = json.Marshal(cfg); err != nil {
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

// DockerBridge asks sshd on reader's server for a unix socket at path and
// serves h on it until stop is called. Fails when sshd refuses streamlocal
// forwarding (AllowStreamLocalForwarding no): the reader then has no docker.
func DockerBridge(reader *ssh.Client, path string, h http.Handler) (stop func(), err error) {
	ln, err := reader.ListenUnix(path)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return func() { _ = srv.Close() }, nil
}
