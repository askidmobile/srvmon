// Package api is the HTTP layer of srvmon-server: auth, ingest, REST and the
// embedded web UI.
package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/backup"
	"github.com/askidmobile/srvmon/internal/config"
	"github.com/askidmobile/srvmon/internal/heartbeat"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/notify"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
	"github.com/askidmobile/srvmon/internal/telegram"
)

// Server wires dependencies for the HTTP handlers.
type Server struct {
	cfg       config.Config
	store     *store.Store
	log       *slog.Logger
	authLog   io.Writer
	authLogMu sync.Mutex
	// aiSaveMu orders AI settings saves with their push to srvmon-ai, so the
	// last saved setup is the one srvmon-ai runs.
	aiSaveMu  sync.Mutex
	limiter   *auth.Limiter
	vm        *metrics.Writer
	vmRead    *metrics.Reader
	latest    *metrics.Latest
	web       fs.FS
	version   string
	secrets   *secret.Box
	jobs      *jobs
	updates   *agentUpdates
	setupKeys *setupKeys
	csp       string
	alerts    *alerts.Engine
	bot       *telegram.Bot      // nil when its settings cannot be loaded
	heartbeat *heartbeat.Monitor // nil when its settings cannot be loaded
	backup    *backup.Runner     // nil without a master key
	emergency *emergencyState
	hub       *notify.Hub // nil when its settings cannot be loaded

	ai              *aiagent.Client // nil: agent disabled
	agent           *agentState
	mcp             *mcpState
	diag            *diagnoser
	notify          func(ctx context.Context, text string) error
	onActionPending func(store.AgentAction)

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
	VMRead  *metrics.Reader // nil: graphs use only in-memory history
	Web     fs.FS           // built frontend (contents of web/dist)
	Version string
	Secrets *secret.Box // seals SSH private keys; nil disables the add wizard
	// Notify sends a critical HTML message for agent tasks; default: the
	// notification hub (Telegram and the Shoutrrr channels).
	Notify func(ctx context.Context, text string) error
	// NotifySender delivers to Shoutrrr channels; default: Shoutrrr (tests
	// put a fake here).
	NotifySender notify.Sender
	// OnActionPending is called when the agent asks to run a command that
	// needs confirmation; default: Telegram approve / reject buttons, which
	// call Server.DecideAgentAction.
	OnActionPending func(store.AgentAction)
	// TelegramAPI overrides the Bot API base URL (tests, or a proxy where
	// api.telegram.org is blocked).
	TelegramAPI string
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
		cfg:       o.Config,
		store:     o.Store,
		log:       o.Log,
		authLog:   o.AuthLog,
		limiter:   auth.NewLimiter(5, 10*time.Minute),
		vm:        o.VM,
		vmRead:    o.VMRead,
		latest:    metrics.NewLatest(),
		web:       o.Web,
		version:   o.Version,
		secrets:   o.Secrets,
		jobs:      &jobs{m: map[string]*job{}},
		updates:   newAgentUpdates(),
		setupKeys: &setupKeys{m: map[string]setupKey{}},
		now:       time.Now,
		hashSem:   make(chan struct{}, 2),

		ai:              aiagent.NewClient(o.Config.AIURL, o.Config.AIToken),
		agent:           newAgentState(),
		mcp:             newMCPState(),
		diag:            newDiagnoser(),
		notify:          o.Notify,
		onActionPending: o.OnActionPending,
		emergency:       newEmergencyState(),
	}
	// The interface language is one setting for the whole installation:
	// Telegram, alerts and the agent use it too.
	if v, err := o.Store.GetSetting(context.Background(), i18n.SettingKey); err == nil {
		i18n.Set(v)
	}
	srv.alerts = alerts.New(o.Store, srv.latest, o.Log)
	srv.alerts.OnFire(srv.onAlertFired)
	if o.Secrets != nil {
		srv.backup = backup.NewRunner(o.Store, o.Secrets, o.Log, o.Version)
		srv.backup.OnFailure = func(ctx context.Context, msg string) {
			srv.alerts.Event(ctx, store.KindBackup, 0, "", "", i18n.T("panel backup failed: %s", msg))
		}
	}
	// Every notification goes through the hub: it hands alerts to the
	// Telegram bot and to the Shoutrrr channels set up in the panel.
	hub, err := notify.New(context.Background(), notify.Options{
		Store: o.Store, Secrets: o.Secrets, Log: o.Log, Sender: o.NotifySender, PanelURL: o.Config.PublicOrigin,
	})
	if err != nil {
		o.Log.Error("notify: load channels", "err", err)
	} else {
		srv.hub = hub
		srv.alerts.SetNotifier(hub)
		if srv.notify == nil {
			srv.notify = hub.SendText
		}
	}
	bot, err := telegram.New(context.Background(), telegram.Options{
		Store: o.Store, Secrets: o.Secrets, Engine: srv.alerts, Latest: srv.latest, Log: o.Log, API: o.TelegramAPI,
	})
	if err != nil {
		o.Log.Error("telegram: load settings", "err", err)
	} else {
		srv.bot = bot
		if hub != nil {
			hub.SetTelegram(bot)
		} else {
			srv.alerts.SetNotifier(bot)
		}
		srv.wireAgentTelegram(bot)
	}
	hb, err := heartbeat.New(context.Background(), heartbeat.Options{
		Store: o.Store, Secrets: o.Secrets, Version: o.Version, Log: o.Log, Checks: srv.selfChecks(),
	})
	if err != nil {
		o.Log.Error("heartbeat: load settings", "err", err)
	} else {
		srv.heartbeat = hb
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

// Start runs the background work: alert evaluation, notification delivery,
// the heartbeat, the Telegram bot and, when fail2banEvents is set, following
// fail2ban bans of the host.
func (s *Server) Start(ctx context.Context, fail2banEvents, hostName string) {
	go s.alerts.Run(ctx)
	if s.heartbeat != nil {
		go s.heartbeat.Run(ctx)
	}
	if s.backup != nil {
		go s.backup.Run(ctx)
	}
	if s.hub != nil {
		go s.hub.Run(ctx)
	}
	if s.bot != nil {
		go s.bot.Run(ctx)
	}
	if fail2banEvents != "" {
		go s.alerts.TailFail2ban(ctx, fail2banEvents, hostName)
	}
}

// Bot returns the Telegram bot (nil if it failed to load).
func (s *Server) Bot() *telegram.Bot { return s.bot }

// Handler returns the root HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("POST /api/auth/password", s.handlePassword)
	mux.HandleFunc("POST /api/auth/totp", s.handleTOTP)
	mux.HandleFunc("POST /api/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/auth/me", s.handleMe)
	mux.Handle("PUT /api/settings/lang", s.requireSession(http.HandlerFunc(s.handleSetLang)))

	mux.HandleFunc("POST /api/ingest", s.handleIngest)

	mux.Handle("GET /api/servers", s.requireSession(http.HandlerFunc(s.handleServers)))
	mux.Handle("POST /api/servers/probe", s.requireSession(http.HandlerFunc(s.handleProbe)))
	mux.Handle("POST /api/servers/setup-key", s.requireSession(http.HandlerFunc(s.handleSetupKey)))
	mux.Handle("POST /api/servers/onboard", s.requireSession(http.HandlerFunc(s.handleOnboard)))
	mux.Handle("GET /api/onboard/{job}", s.requireSession(http.HandlerFunc(s.handleOnboardStatus)))
	mux.Handle("DELETE /api/onboard/{job}", s.requireSession(http.HandlerFunc(s.handleOnboardCancel)))
	mux.Handle("DELETE /api/servers/{id}", s.requireSession(http.HandlerFunc(s.handleDeleteServer)))
	mux.Handle("GET /api/servers/{id}/terminal", s.requireSession(http.HandlerFunc(s.handleTerminal)))
	mux.Handle("POST /api/servers/{id}/containers/{name}/{action}", s.requireSession(http.HandlerFunc(s.handleContainerAction)))
	mux.Handle("GET /api/servers/{id}", s.requireSession(http.HandlerFunc(s.handleServer)))
	mux.Handle("PATCH /api/servers/{id}", s.requireSession(http.HandlerFunc(s.handleServerCard)))
	mux.Handle("GET /api/servers/{id}/history", s.requireSession(http.HandlerFunc(s.handleHistory)))
	mux.Handle("POST /api/servers/{id}/agent/update", s.requireSession(http.HandlerFunc(s.handleAgentUpdate)))
	mux.Handle("POST /api/servers/agents/update", s.requireSession(http.HandlerFunc(s.handleAgentUpdateAll)))
	mux.Handle("GET /api/servers/{id}/sudo", s.requireSession(http.HandlerFunc(s.handleSudo)))
	mux.Handle("PUT /api/servers/{id}/sudo", s.requireSession(http.HandlerFunc(s.handleSetSudo)))

	agent := func(pattern string, h http.HandlerFunc) { mux.Handle(pattern, s.requireSession(h)) }
	agent("GET /api/agent/status", s.handleAgentStatus)
	agent("GET /api/agent/chats", s.handleAgentChats)
	agent("POST /api/agent/chats", s.handleAgentNewChat)
	agent("GET /api/agent/chats/{id}", s.handleAgentChat)
	agent("DELETE /api/agent/chats/{id}", s.handleAgentDeleteChat)
	agent("PUT /api/agent/chats/{id}/model", s.handleAgentChatModel)
	agent("GET /api/agent/settings", s.handleAISettings)
	agent("PUT /api/agent/settings", s.handleAISaveSettings)
	agent("GET /api/agent/providers", s.handleAIProviders)
	agent("POST /api/agent/models", s.handleAIModels)
	agent("POST /api/agent/test", s.handleAITest)
	agent("POST /api/agent/chats/{id}/run", s.handleAgentRun)
	agent("POST /api/agent/chats/{id}/abort", s.handleAgentAbort)
	agent("GET /api/agent/actions", s.handleAgentActions)
	agent("POST /api/agent/actions/{id}/{decision}", s.handleAgentDecide)
	agent("GET /api/agent/tasks", s.handleAgentTasks)
	agent("POST /api/agent/tasks", s.handleAgentNewTask)
	agent("PATCH /api/agent/tasks/{id}", s.handleAgentUpdateTask)
	agent("DELETE /api/agent/tasks/{id}", s.handleAgentDeleteTask)
	agent("POST /api/agent/tasks/{id}/run", s.handleAgentRunTask)
	agent("GET /api/agent/diagnose", s.handleDiagnoseSettings)
	agent("PUT /api/agent/diagnose", s.handleDiagnoseSave)
	agent("POST /api/alerts/{id}/diagnose", s.handleAlertDiagnose)

	s.alertRoutes(mux)
	s.mcpRoutes(mux)
	s.backupRoutes(mux)
	s.accessRoutes(mux)
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

// writeJSON encodes v before sending the status line: an encoding error
// (a NaN in a report) becomes a 500 instead of a 200 with an empty body, and
// Content-Length lets the client tell a complete body from a cut one.
func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		slog.Error("encode response", "err", err)
		status = http.StatusInternalServerError
		b = []byte(`{"error":"internal"}`)
	}
	b = append(b, '\n')
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	_, _ = w.Write(b)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func readJSON(r *http.Request, v any) error { return readJSONLimit(r, v, 8<<10) }

// readJSONLimit is readJSON with a body limit for larger requests.
func readJSONLimit(r *http.Request, v any, limit int64) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, limit))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("bad json: %w", err)
	}
	return nil
}
