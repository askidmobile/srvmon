package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// Tool API for the srvmon-ai container, served on a separate listener that
// only the compose network reaches (SRVMON_AI_TOOLS_LISTEN). This is the
// only way the agent touches servers: it has no SSH keys, and every command
// goes through Classify here, so a command that changes something waits for
// the owner whatever the agent or its model decides.

const (
	toolReadTimeout    = 60 * time.Second
	toolWriteTimeout   = 10 * time.Minute
	agentConfirmWait   = 15 * time.Minute
	toolOutputMax      = 32 << 10
	toolOutputKeepHead = 8 << 10
	toolKeepalive      = 30 * time.Second
)

// AgentToolsHandler serves the tool API. It is nil-safe to mount only when
// the agent is configured.
func (s *Server) AgentToolsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tools/servers", s.toolServers)
	mux.HandleFunc("GET /tools/servers/{name}", s.toolServer)
	mux.HandleFunc("POST /tools/exec", s.toolExec)
	mux.HandleFunc("GET /tools/tasks", s.toolTasks)
	mux.HandleFunc("POST /tools/tasks", s.toolNewTask)
	mux.HandleFunc("DELETE /tools/tasks/{id}", s.toolDeleteTask)
	mux.HandleFunc("GET /tools/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		want := "Bearer " + s.cfg.AIToken
		got := r.Header.Get("Authorization")
		if s.cfg.AIToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type toolServerView struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Project     string   `json:"project,omitempty"`
	Group       string   `json:"group,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Host        string   `json:"host,omitempty"`
	Online      bool     `json:"online"`
	SSH         bool     `json:"ssh"`
	LastSeen    string   `json:"last_seen,omitempty"`
	CPU         *float64 `json:"cpu_percent,omitempty"`
	Load1       *float64 `json:"load1,omitempty"`
	MemUsedPct  *float64 `json:"mem_used_percent,omitempty"`
	DiskMaxPct  *float64 `json:"disk_max_used_percent,omitempty"`
	Containers  string   `json:"containers,omitempty"` // "12 running / 13, 1 unhealthy"
}

func pct(a, b uint64) *float64 {
	if b == 0 {
		return nil
	}
	v := math.Round(float64(a)/float64(b)*1000) / 10
	return &v
}

func round1(v float64) *float64 {
	r := math.Round(v*10) / 10
	return &r
}

func (s *Server) toolView(srv store.Server) toolServerView {
	v := toolServerView{Name: srv.Name, Description: srv.Description, Project: srv.Project, Group: srv.Group,
		Tags: srv.Tags, Host: srv.Host, SSH: srv.HasSSH}
	if srv.LastSeen != nil {
		v.LastSeen = srv.LastSeen.Format(time.RFC3339)
	}
	snap, ok := s.latest.Get(srv.ID)
	if !ok {
		return v
	}
	v.Online = s.now().Sub(snap.Received) < offlineAfter
	h := snap.Report.Host
	v.CPU, v.Load1 = round1(h.CPUPercent), round1(h.Load1)
	if h.MemTotal > 0 && h.MemAvailable <= h.MemTotal {
		v.MemUsedPct = pct(h.MemTotal-h.MemAvailable, h.MemTotal)
	}
	for _, d := range h.Disks {
		if p := pct(d.Used, d.Total); p != nil && (v.DiskMaxPct == nil || *p > *v.DiskMaxPct) {
			v.DiskMaxPct = p
		}
	}
	if snap.Report.DockerOK {
		running, bad := 0, 0
		for _, c := range snap.Report.Containers {
			if c.State == "running" {
				running++
			}
			if c.Health == "unhealthy" || c.State == "restarting" || c.State == "exited" || c.State == "dead" {
				bad++
			}
		}
		v.Containers = fmt.Sprintf("%d running / %d", running, len(snap.Report.Containers))
		if bad > 0 {
			v.Containers += fmt.Sprintf(", %d problem", bad)
		}
	}
	return v
}

func (s *Server) toolServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]toolServerView, 0, len(list))
	for _, srv := range list {
		out = append(out, s.toolView(srv))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) serverByName(ctx context.Context, name string) (store.Server, error) {
	id, err := s.store.ServerIDByName(ctx, strings.TrimSpace(name))
	if err != nil {
		return store.Server{}, err
	}
	return s.store.GetServer(ctx, id)
}

func (s *Server) toolServer(w http.ResponseWriter, r *http.Request) {
	srv, err := s.serverByName(r.Context(), r.PathValue("name"))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown_server")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := map[string]any{"server": s.toolView(srv)}
	if snap, ok := s.latest.Get(srv.ID); ok {
		h := snap.Report.Host
		out["host"] = map[string]any{
			"cpu_cores": h.CPUCores, "load": []float64{h.Load1, h.Load5, h.Load15},
			"mem_total": h.MemTotal, "mem_available": h.MemAvailable,
			"swap_total": h.SwapTotal, "swap_free": h.SwapFree, "uptime_sec": int64(h.UptimeSec), "disks": h.Disks,
		}
		out["containers"] = snap.Report.Containers
		out["docker_ok"] = snap.Report.DockerOK
		out["report_at"] = snap.Received.UTC().Format(time.RFC3339)
	}
	d := s.latest.Details(srv.ID, 30)
	out["inventory"] = d.Inventory
	out["recent_warnings"] = d.Logs
	if srv.Provider != "" {
		out["provider"] = srv.Provider + " " + srv.Plan
	}
	writeJSON(w, http.StatusOK, out)
}

type execRequest struct {
	Server  string `json:"server"`
	Command string `json:"command"`
	Reason  string `json:"reason"`
	ChatID  int64  `json:"chat_id"`
	TaskID  int64  `json:"task_id"`
}

type execResult struct {
	Status   string `json:"status"` // ok | error | rejected | expired
	ReadOnly bool   `json:"read_only"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
	ActionID int64  `json:"action_id,omitempty"`
	Note     string `json:"note,omitempty"`
}

func limitOutput(s string) string {
	if len(s) <= toolOutputMax {
		return s
	}
	head := truncate(s, toolOutputKeepHead)
	tail := s[len(s)-(toolOutputMax-toolOutputKeepHead):]
	for len(tail) > 0 && tail[0]&0xC0 == 0x80 {
		tail = tail[1:]
	}
	return head + fmt.Sprintf("[… пропущено %d байт …]\n", len(s)-toolOutputMax) + tail
}

// runOnServer runs cmd over SSH under `timeout`, so a streaming command
// (logs -f, events) ends on the server too, not only on our side.
func (s *Server) runOnServer(ctx context.Context, a store.SSHAccess, cmd string, limit time.Duration) (int, string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit+20*time.Second)
	defer cancel()
	client, err := s.dialServer(ctx, a)
	if err != nil {
		return -1, "", fmt.Errorf("ssh: %w", err)
	}
	defer client.Close()
	secs := int(limit / time.Second)
	wrapped := fmt.Sprintf("timeout -k 5 %d sh -c %s", secs, sshgw.Quote(cmd))
	// Capture is bounded while receiving: `head -c 10G /dev/zero` must not
	// fill our memory before limitOutput trims it.
	out, errOut, err := sshgw.RunCapped(ctx, client, wrapped, toolOutputKeepHead, toolOutputMax-toolOutputKeepHead)
	code := sshgw.ExitCode(err)
	text := out
	if strings.TrimSpace(errOut) != "" {
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += "[stderr]\n" + errOut
	}
	if code == 124 {
		text += fmt.Sprintf("\n[остановлено по таймауту %s]", limit)
	}
	if code == -1 && err != nil {
		return code, limitOutput(text), err
	}
	return code, limitOutput(text), nil
}

func optID(v int64) *int64 {
	if v <= 0 {
		return nil
	}
	return &v
}

func (s *Server) toolExec(w http.ResponseWriter, r *http.Request) {
	var req execRequest
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	req.Command = strings.TrimSpace(req.Command)
	if req.Command == "" || len(req.Command) > 2000 {
		writeError(w, http.StatusBadRequest, "bad_command")
		return
	}
	srv, err := s.serverByName(r.Context(), req.Server)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "unknown_server")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	access, err := s.store.ServerSSH(r.Context(), srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		writeJSON(w, http.StatusOK, execResult{Status: "error", Note: "у srvmon нет ssh-доступа к этому серверу"})
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	who := "agent"
	if req.TaskID > 0 {
		who = "agent task " + strconv.FormatInt(req.TaskID, 10)
	}

	if ro, _ := aiagent.Classify(req.Command); ro {
		code, out, err := s.runOnServer(r.Context(), access, req.Command, toolReadTimeout)
		_ = s.store.Audit(context.Background(), who, "agent_exec", srv.Name+": "+truncate(req.Command, 500))
		res := execResult{Status: "ok", ReadOnly: true, ExitCode: &code, Output: out}
		if err != nil {
			res.Status, res.Note, res.ExitCode = "error", err.Error(), nil
		} else if code != 0 {
			res.Status = "error"
		}
		writeJSON(w, http.StatusOK, res)
		return
	}

	_, why := aiagent.Classify(req.Command)
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = why
	}
	action, err := s.store.CreateAgentAction(r.Context(), store.AgentAction{
		ChatID: optID(req.ChatID), TaskID: optID(req.TaskID), ServerID: srv.ID, ServerName: srv.Name,
		Command: req.Command, Reason: truncate(reason, 500),
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	ch := make(chan string, 1)
	s.agent.mu.Lock()
	s.agent.waiters[action.ID] = ch
	s.agent.mu.Unlock()
	defer func() {
		s.agent.mu.Lock()
		delete(s.agent.waiters, action.ID)
		s.agent.mu.Unlock()
	}()
	_ = s.store.Audit(context.Background(), who, "agent_action_pending", fmt.Sprintf("#%d %s: %s", action.ID, srv.Name, truncate(req.Command, 500)))
	if s.onActionPending != nil {
		s.onActionPending(action)
	}

	// The wait can take minutes: answer the headers now and keep the
	// connection busy with JSON whitespace, so HTTP clients with header or
	// idle-body timeouts (Node fetch: 5 min) keep waiting.
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	finish := func(res execResult) { _ = json.NewEncoder(w).Encode(res) }

	timer := time.NewTimer(agentConfirmWait)
	defer timer.Stop()
	keepalive := time.NewTicker(toolKeepalive)
	defer keepalive.Stop()
	var decision string
wait:
	for {
		select {
		case decision = <-ch:
			break wait
		case <-timer.C:
			break wait
		case <-r.Context().Done():
			break wait
		case <-keepalive.C:
			_, _ = w.Write([]byte(" "))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	if decision == "" {
		// Timed out or the agent went away. A decision may have landed in
		// between: only expire what is still pending.
		if ok, _ := s.store.DecideAgentAction(context.Background(), action.ID, store.ActionExpired, "timeout"); !ok {
			if a, err := s.store.GetAgentAction(context.Background(), action.ID); err == nil {
				decision = a.State
			}
		}
	}
	switch decision {
	case store.ActionApproved:
	case store.ActionRejected:
		finish(execResult{Status: "rejected", ActionID: action.ID, Note: "владелец отклонил команду"})
		return
	default:
		finish(execResult{Status: "expired", ActionID: action.ID, Note: "команду не подтвердили вовремя"})
		return
	}
	// Approved: run even if the agent stopped waiting, the owner asked for
	// it. Keep the response alive while it runs (up to 10 minutes).
	type runResult struct {
		code int
		out  string
		err  error
	}
	done := make(chan runResult, 1)
	go func() {
		code, out, err := s.runOnServer(context.Background(), access, req.Command, toolWriteTimeout)
		done <- runResult{code, out, err}
	}()
	var rr runResult
run:
	for {
		select {
		case rr = <-done:
			break run
		case <-keepalive.C:
			_, _ = w.Write([]byte(" "))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
	code, out, runErr := rr.code, rr.out, rr.err
	ok := runErr == nil && code == 0
	stored := out
	if runErr != nil {
		stored = strings.TrimSpace(out + "\n" + runErr.Error())
	}
	_ = s.store.FinishAgentAction(context.Background(), action.ID, ok, code, truncate(stored, toolOutputMax))
	_ = s.store.Audit(context.Background(), who, "agent_exec_confirmed", fmt.Sprintf("#%d %s: exit %d", action.ID, srv.Name, code))
	res := execResult{Status: "ok", ExitCode: &code, Output: out, ActionID: action.ID}
	if !ok {
		res.Status = "error"
		if runErr != nil {
			res.Note = runErr.Error()
		}
	}
	finish(res)
}

func (s *Server) toolTasks(w http.ResponseWriter, r *http.Request) {
	s.handleAgentTasks(w, r)
}

func (s *Server) toolNewTask(w http.ResponseWriter, r *http.Request) {
	var in taskInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	t, field, err := s.createTask(r.Context(), in, "agent")
	if field != "" {
		msg := map[string]string{
			"name":     "имя 1–64 символа",
			"prompt":   "промпт обязателен, до 4000 символов",
			"schedule": "cron из 5 полей (UTC, не чаще раза в 5 минут) или @every 15m",
			"notify":   "notify: пусто или telegram",
			"servers":  "неизвестный сервер в списке",
		}[field]
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": field, "hint": msg})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) toolDeleteTask(w http.ResponseWriter, r *http.Request) {
	s.handleAgentDeleteTask(w, r)
}
