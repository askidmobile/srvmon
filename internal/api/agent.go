package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/store"
)

// AI agent, browser side: chats streamed from srvmon-ai, confirmations of
// commands that change a server, and scheduled tasks. The agent itself runs
// in the srvmon-ai container and reaches servers only through the tool API
// in agent_tools.go.

const (
	agentMaxPrompt  = 4000
	agentRunTimeout = 30 * time.Minute
	toolOutputShown = 4000 // per tool call in the stored transcript
)

// agentState is the in-memory part: running chats and commands waiting for
// a decision.
type agentState struct {
	mu      sync.Mutex
	runs    map[int64]context.CancelFunc // chat id -> abort
	waiters map[int64]chan string        // action id -> decided state
}

func newAgentState() *agentState {
	return &agentState{runs: map[int64]context.CancelFunc{}, waiters: map[int64]chan string{}}
}

func (s *Server) agentEnabled(w http.ResponseWriter) bool {
	if s.ai == nil {
		writeError(w, http.StatusServiceUnavailable, "agent_disabled")
		return false
	}
	return true
}

func (s *Server) handleAgentStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"enabled": s.ai != nil}
	if s.ai != nil {
		st, err := s.ai.Status(r.Context())
		if err != nil {
			out["ready"] = false
			out["error"] = "srvmon-ai недоступен"
		} else {
			out["ready"] = st.Ready
			out["provider"] = st.Provider
			out["model"] = st.Model
			if st.Error != "" {
				out["error"] = st.Error
			}
		}
		models := []string{}
		if a, ok, _ := s.loadAISettings(r.Context()); ok {
			models = a.Models
		} else if m, _ := out["model"].(string); m != "" {
			models = []string{m}
		}
		out["models"] = models
		pending, _ := s.store.ListAgentActions(r.Context(), store.ActionPending, 100)
		out["pending"] = len(pending)
	}
	writeJSON(w, http.StatusOK, out)
}

type chatView struct {
	store.AgentChat
	Server  string `json:"server,omitempty"`
	Running bool   `json:"running"`
}

func (s *Server) chatView(ctx context.Context, c store.AgentChat) chatView {
	v := chatView{AgentChat: c}
	if c.ServerID != nil {
		if srv, err := s.store.GetServer(ctx, *c.ServerID); err == nil {
			v.Server = srv.Name
		}
	}
	s.agent.mu.Lock()
	_, v.Running = s.agent.runs[c.ID]
	s.agent.mu.Unlock()
	return v
}

func (s *Server) handleAgentChats(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAgentChats(r.Context(), 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]chatView, 0, len(list))
	for _, c := range list {
		out = append(out, s.chatView(r.Context(), c))
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAgentNewChat(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServerID *int64 `json:"server_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if req.ServerID != nil {
		if ok, _ := s.store.ServerExists(r.Context(), *req.ServerID); !ok {
			writeError(w, http.StatusBadRequest, "bad_server")
			return
		}
	}
	c, err := s.store.CreateAgentChat(r.Context(), req.ServerID, "")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, s.chatView(r.Context(), c))
}

func (s *Server) chatFromPath(w http.ResponseWriter, r *http.Request) (store.AgentChat, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return store.AgentChat{}, false
	}
	c, err := s.store.GetAgentChat(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return c, false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return c, false
	}
	return c, true
}

func (s *Server) handleAgentChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	msgs, err := s.store.AgentMessages(r.Context(), c.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"chat": s.chatView(r.Context(), c), "messages": msgs})
}

func (s *Server) handleAgentDeleteChat(w http.ResponseWriter, r *http.Request) {
	c, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	s.agent.mu.Lock()
	if cancel := s.agent.runs[c.ID]; cancel != nil {
		cancel()
	}
	s.agent.mu.Unlock()
	if err := s.store.DeleteAgentChat(r.Context(), c.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if s.ai != nil {
		if err := s.ai.ForgetChat(r.Context(), c.ID); err != nil {
			s.log.Warn("srvmon-ai forget chat", "chat", c.ID, "err", err)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentAbort(w http.ResponseWriter, r *http.Request) {
	c, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	s.agent.mu.Lock()
	cancel := s.agent.runs[c.ID]
	s.agent.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": cancel != nil})
}

// transcript turns the event stream into the stored assistant message.
type transcript struct {
	text  strings.Builder
	final string
	tools []store.AgentToolCall
	index map[string]int
	err   string
}

func (t *transcript) add(ev aiagent.Event) {
	switch ev.T {
	case "text":
		t.text.WriteString(ev.D)
	case "tool":
		if t.index == nil {
			t.index = map[string]int{}
		}
		t.index[ev.ID] = len(t.tools)
		t.tools = append(t.tools, store.AgentToolCall{ID: ev.ID, Name: ev.Name, Args: ev.Args})
	case "tool_end":
		if i, ok := t.index[ev.ID]; ok {
			t.tools[i].OK = ev.OK
			t.tools[i].Output = truncate(ev.Out, toolOutputShown)
		}
	case "error":
		t.err = ev.Msg
	case "done":
		t.final = ev.Text
	}
}

func (t *transcript) message() store.AgentMessage {
	text := t.final
	if text == "" {
		text = t.text.String()
	}
	return store.AgentMessage{Role: "assistant", Text: text, Tools: t.tools, Error: t.err}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "\n…"
}

// handleAgentRun sends a message and streams the reply as NDJSON. The run
// is detached from the request: closing the tab does not stop it (use
// abort), and its result is stored either way.
func (s *Server) handleAgentRun(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	c, ok := s.chatFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	req.Text = strings.TrimSpace(req.Text)
	if req.Text == "" || utf8.RuneCountInString(req.Text) > agentMaxPrompt {
		writeError(w, http.StatusBadRequest, "bad_text")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), agentRunTimeout)
	defer cancel()
	s.agent.mu.Lock()
	if s.agent.runs[c.ID] != nil {
		s.agent.mu.Unlock()
		writeError(w, http.StatusConflict, "busy")
		return
	}
	s.agent.runs[c.ID] = cancel
	s.agent.mu.Unlock()
	defer func() {
		s.agent.mu.Lock()
		delete(s.agent.runs, c.ID)
		s.agent.mu.Unlock()
	}()

	if _, err := s.store.AddAgentMessage(ctx, c.ID, store.AgentMessage{Role: "user", Text: req.Text}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	run := aiagent.RunRequest{Text: req.Text, Model: c.Model}
	if c.ServerID != nil {
		if srv, err := s.store.GetServer(ctx, *c.ServerID); err == nil {
			run.Server = srv.Name
		}
	}
	_ = s.store.Audit(ctx, s.clientIP(r), "agent_prompt", "chat "+strconv.FormatInt(c.ID, 10))

	h := w.Header()
	h.Set("Content-Type", "application/x-ndjson")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no") // nginx: do not buffer the stream
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	send := func(ev aiagent.Event) {
		if r.Context().Err() != nil {
			return
		}
		_ = enc.Encode(ev)
		if flusher != nil {
			flusher.Flush()
		}
	}
	send(aiagent.Event{T: "start"})

	var tr transcript
	err := s.ai.Run(ctx, c.ID, run, func(ev aiagent.Event) {
		tr.add(ev)
		send(ev)
	})
	if err != nil {
		msg := "srvmon-ai: " + err.Error()
		switch {
		case errors.Is(err, context.Canceled):
			msg = "остановлено"
		case errors.Is(err, context.DeadlineExceeded):
			msg = "превышено время ответа"
		case aiagent.IsBusy(err):
			msg = "агент ещё отвечает в этом чате"
		}
		if tr.err == "" {
			tr.err = msg
			send(aiagent.Event{T: "error", Msg: msg})
		}
	}
	// The browser may be gone; the transcript is stored regardless.
	if _, err := s.store.AddAgentMessage(context.Background(), c.ID, tr.message()); err != nil {
		s.log.Error("store agent message", "err", err)
	}
}

// --- confirmations ---

func (s *Server) handleAgentActions(w http.ResponseWriter, r *http.Request) {
	state := r.URL.Query().Get("state")
	switch state {
	case "", store.ActionPending, store.ActionDone, store.ActionFailed, store.ActionRejected, store.ActionExpired, store.ActionApproved:
	default:
		writeError(w, http.StatusBadRequest, "bad_state")
		return
	}
	list, err := s.store.ListAgentActions(r.Context(), state, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleAgentDecide(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	var state string
	switch r.PathValue("decision") {
	case "approve":
		state = store.ActionApproved
	case "reject":
		state = store.ActionRejected
	default:
		writeError(w, http.StatusBadRequest, "bad_decision")
		return
	}
	a, err := s.store.GetAgentAction(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !s.DecideAgentAction(r.Context(), id, state, "panel "+s.clientIP(r)) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_decided", "state": a.State})
		return
	}
	a, _ = s.store.GetAgentAction(r.Context(), id)
	writeJSON(w, http.StatusOK, a)
}

// DecideAgentAction approves or rejects a pending command and wakes the tool
// call waiting for it. by names who decided (panel IP, telegram, ...), so
// the Telegram bot can confirm actions through the same path. It returns
// false when the action is not pending any more.
func (s *Server) DecideAgentAction(ctx context.Context, id int64, state, by string) bool {
	if state != store.ActionApproved && state != store.ActionRejected {
		return false
	}
	s.agent.mu.Lock()
	ch := s.agent.waiters[id]
	s.agent.mu.Unlock()
	if ch == nil {
		// Nobody waits (srvmon restarted or the call timed out): expire it.
		if ok, _ := s.store.DecideAgentAction(ctx, id, store.ActionExpired, by); ok {
			_ = s.store.Audit(ctx, by, "agent_action_expired", strconv.FormatInt(id, 10))
		}
		return false
	}
	ok, err := s.store.DecideAgentAction(ctx, id, state, by)
	if err != nil || !ok {
		return false
	}
	_ = s.store.Audit(ctx, by, "agent_action_"+state, strconv.FormatInt(id, 10))
	select {
	case ch <- state:
	default:
	}
	return true
}

// --- tasks ---

type taskInput struct {
	Name     string   `json:"name"`
	Schedule string   `json:"schedule"`
	Prompt   string   `json:"prompt"`
	Servers  []string `json:"servers"`
	Notify   string   `json:"notify"`
	Enabled  *bool    `json:"enabled"`
}

// validate fills t from in and returns the bad field name, if any.
func (s *Server) validateTask(ctx context.Context, in taskInput, t *store.AgentTask) string {
	t.Name = strings.TrimSpace(in.Name)
	if t.Name == "" || utf8.RuneCountInString(t.Name) > 64 {
		return "name"
	}
	t.Prompt = strings.TrimSpace(in.Prompt)
	if t.Prompt == "" || utf8.RuneCountInString(t.Prompt) > agentMaxPrompt {
		return "prompt"
	}
	t.Schedule = strings.TrimSpace(in.Schedule)
	sched, err := aiagent.ParseSchedule(t.Schedule)
	if err != nil {
		return "schedule"
	}
	switch in.Notify {
	case "", "telegram":
		t.Notify = in.Notify
	default:
		return "notify"
	}
	if len(in.Servers) > 50 {
		return "servers"
	}
	t.Servers = []string{}
	for _, name := range in.Servers {
		name = strings.TrimSpace(name)
		if name == "" || name == "*" || name == "all" {
			continue
		}
		if _, err := s.store.ServerIDByName(ctx, name); err != nil {
			return "servers"
		}
		t.Servers = append(t.Servers, name)
	}
	if in.Enabled != nil {
		t.Enabled = *in.Enabled
	}
	next := sched.Next(s.now())
	if next.IsZero() {
		return "schedule"
	}
	t.NextRunAt = &next
	return ""
}

func (s *Server) handleAgentTasks(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListAgentTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) createTask(ctx context.Context, in taskInput, by string) (store.AgentTask, string, error) {
	t := store.AgentTask{Enabled: true}
	if field := s.validateTask(ctx, in, &t); field != "" {
		return t, field, nil
	}
	t, err := s.store.CreateAgentTask(ctx, t)
	if err == nil {
		_ = s.store.Audit(ctx, by, "agent_task_created", t.Name+" "+t.Schedule)
	}
	return t, "", err
}

func (s *Server) handleAgentNewTask(w http.ResponseWriter, r *http.Request) {
	var in taskInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	t, field, err := s.createTask(r.Context(), in, s.clientIP(r))
	if field != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": field})
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) taskFromPath(w http.ResponseWriter, r *http.Request) (store.AgentTask, bool) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return store.AgentTask{}, false
	}
	t, err := s.store.GetAgentTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return t, false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return t, false
	}
	return t, true
}

func (s *Server) handleAgentUpdateTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	// Fields left out keep their value.
	in := taskInput{Name: t.Name, Schedule: t.Schedule, Prompt: t.Prompt, Servers: t.Servers, Notify: t.Notify}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if field := s.validateTask(r.Context(), in, &t); field != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": field})
		return
	}
	if err := s.store.UpdateAgentTask(r.Context(), t); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "agent_task_updated", t.Name)
	t, _ = s.store.GetAgentTask(r.Context(), t.ID)
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) handleAgentDeleteTask(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteAgentTask(r.Context(), t.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "agent_task_deleted", t.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleAgentRunTask(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if !s.startTask(t, "manual") {
		writeError(w, http.StatusConflict, "busy")
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
