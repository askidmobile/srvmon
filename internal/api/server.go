// Package api is the HTTP layer of srvmon-server: auth, ingest, REST and the
// embedded web UI.
package api

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/config"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

// Server wires dependencies for the HTTP handlers.
type Server struct {
	cfg       config.Config
	store     *store.Store
	log       *slog.Logger
	authLog   io.Writer
	authLogMu sync.Mutex
	limiter   *auth.Limiter
	vm        *metrics.Writer
	latest    *metrics.Latest
	web       fs.FS
	version   string
	secrets   *secret.Box
	jobs      *jobs
	csp       string

	totpMu  sync.Mutex    // serializes TOTP replay check-and-set
	hashSem chan struct{} // caps concurrent argon2id verifications
	now     func() time.Time
}

// Options for New.
type Options struct {
	Config  config.Config
	Store   *store.Store
	Log     *slog.Logger
	AuthLog io.Writer // fail2ban log; nil = discard
	VM      *metrics.Writer
	Web     fs.FS // built frontend (contents of web/dist)
	Version string
	Secrets *secret.Box // seals SSH private keys; nil disables the add wizard
}

// New builds the server.
func New(o Options) *Server {
	if o.AuthLog == nil {
		o.AuthLog = io.Discard
	}
	if o.Log == nil {
		o.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	srv := &Server{
		cfg:     o.Config,
		store:   o.Store,
		log:     o.Log,
		authLog: o.AuthLog,
		limiter: auth.NewLimiter(5, 10*time.Minute),
		vm:      o.VM,
		latest:  metrics.NewLatest(),
		web:     o.Web,
		version: o.Version,
		secrets: o.Secrets,
		jobs:    &jobs{m: map[string]*job{}},
		now:     time.Now,
		hashSem: make(chan struct{}, 2),
	}
	// 'self' covers same-origin WebSocket in current browsers; older Safari
	// needs the wss:// origin spelled out.
	connect := "'self'"
	if u, err := url.Parse(o.Config.PublicOrigin); err == nil && u.Host != "" {
		ws := "ws"
		if u.Scheme == "https" {
			ws = "wss"
		}
		connect += " " + ws + "://" + u.Host
	}
	srv.csp = "default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; img-src 'self' data:; connect-src " +
		connect + "; frame-ancestors 'none'"
	return srv
}

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("POST /api/auth/password", s.handlePassword)
	mux.HandleFunc("POST /api/auth/totp", s.handleTOTP)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)

	mux.HandleFunc("POST /api/ingest", s.handleIngest)

	mux.Handle("GET /api/servers", s.requireSession(http.HandlerFunc(s.handleServers)))
	mux.Handle("POST /api/servers/probe", s.requireSession(http.HandlerFunc(s.handleProbe)))
	mux.Handle("POST /api/servers/onboard", s.requireSession(http.HandlerFunc(s.handleOnboard)))
	mux.Handle("GET /api/onboard/{job}", s.requireSession(http.HandlerFunc(s.handleOnboardStatus)))
	mux.Handle("DELETE /api/onboard/{job}", s.requireSession(http.HandlerFunc(s.handleOnboardCancel)))
	mux.Handle("DELETE /api/servers/{id}", s.requireSession(http.HandlerFunc(s.handleDeleteServer)))
	mux.Handle("GET /api/servers/{id}/terminal", s.requireSession(http.HandlerFunc(s.handleTerminal)))
	mux.Handle("POST /api/servers/{id}/containers/{name}/{action}", s.requireSession(http.HandlerFunc(s.handleContainerAction)))
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found")
	})

	mux.Handle("/", s.staticHandler())
	return s.securityHeaders(s.checkOrigin(mux))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if _, err := s.store.GetSetting(r.Context(), "healthcheck"); err != nil && err != store.ErrNotFound {
		writeError(w, http.StatusServiceUnavailable, "db")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
}

// clientIP returns the caller IP, honouring X-Forwarded-For from Caddy when
// TrustProxy is set. The rightmost entry is the one added by our proxy.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			if ip := strings.TrimSpace(parts[len(parts)-1]); net.ParseIP(ip) != nil {
				return ip
			}
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Content-Security-Policy", s.csp)
		next.ServeHTTP(w, r)
	})
}

// checkOrigin rejects cross-site state-changing browser requests (CSRF
// defence in depth on top of SameSite=Strict cookies). Agents call ingest
// without an Origin header, so requests without Origin pass.
func (s *Server) checkOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		origin := r.Header.Get("Origin")
		if origin != "" && !s.originAllowed(origin, r.Host) {
			writeError(w, http.StatusForbidden, "bad_origin")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) originAllowed(origin, host string) bool {
	if s.cfg.PublicOrigin != "" {
		return strings.EqualFold(strings.TrimRight(s.cfg.PublicOrigin, "/"), origin)
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, host)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func readJSON(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad json: %w", err)
	}
	return nil
}
