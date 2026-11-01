package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/store"
)

// MCP server: the agent's tool API for MCP clients (Claude Desktop, Cursor,
// Claude Code), served at /mcp over the Streamable HTTP transport. It is
// stateless: every POST carries one JSON-RPC message (or a batch) and gets
// a JSON answer, no session and no server-sent events.
//
// Access is by MCP tokens created in the panel settings, each with scopes.
// Commands go through the same aiagent.Classify as the built-in agent:
// read-only ones run at once, anything else becomes a pending action the
// owner approves in the panel or Telegram. MCP clients give up on a tool
// call after about a minute, so run_command waits mcpWait and then answers
// "pending"; the action itself keeps waiting agentConfirmWait on our side
// and action_status fetches its result.

const (
	mcpTokenPrefix  = "smcp_"
	mcpReadTimeout  = 40 * time.Second
	mcpMaxTokens    = 50
	mcpMaxPending   = 20
	mcpBodyMax      = 64 << 10
	mcpTouchEvery   = time.Minute
	mcpServerName   = "srvmon"
	mcpLatestProto  = "2025-06-18"
	mcpInstructions = "srvmon monitors the owner's servers. Start with list_servers. run_command runs a shell command on a server over SSH: " +
		"read-only commands run at once, anything that changes a server waits for the owner to approve it in the srvmon panel or Telegram " +
		"(up to 15 minutes). When run_command answers status \"pending\", tell the user and call action_status with the action_id later. " +
		"Do not retry a rejected command."
)

// mcpWait is how long a tool call waits for a confirmation (a var for tests).
var mcpWait = 40 * time.Second

var mcpProtocols = map[string]bool{"2025-11-25": true, "2025-06-18": true, "2025-03-26": true, "2024-11-05": true}

// mcpState tracks confirmations started from MCP: done is closed when the
// action reaches its final state.
type mcpState struct {
	mu      sync.Mutex
	pending map[int64]chan struct{}
}

func newMCPState() *mcpState { return &mcpState{pending: map[int64]chan struct{}{}} }

// --- token management (panel session) ---

type mcpTokensView struct {
	Endpoint string           `json:"endpoint"`
	Scopes   []string         `json:"scopes"`
	Tokens   []store.MCPToken `json:"tokens"`
}

func (s *Server) mcpEndpoint(r *http.Request) string {
	if o := strings.TrimRight(s.cfg.PublicOrigin, "/"); o != "" {
		return o + "/mcp"
	}
	scheme := "http"
	if r.TLS != nil || (s.cfg.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/mcp"
}

func (s *Server) handleMCPTokens(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListMCPTokens(r.Context())
	if err != nil {
		s.internalError(w, "mcp tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, mcpTokensView{Endpoint: s.mcpEndpoint(r), Scopes: store.MCPScopes, Tokens: list})
}

func (s *Server) handleMCPNewToken(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
		Days   int      `json:"days"`
	}
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	t := store.MCPToken{Name: strings.TrimSpace(in.Name), Scopes: []string{}}
	if t.Name == "" || utf8.RuneCountInString(t.Name) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": "name"})
		return
	}
	for _, sc := range store.MCPScopes {
		for _, want := range in.Scopes {
			if want == sc {
				t.Scopes = append(t.Scopes, sc)
				break
			}
		}
	}
	if len(t.Scopes) == 0 || len(t.Scopes) != len(in.Scopes) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": "scopes"})
		return
	}
	switch in.Days {
	case 0:
	case 30, 90, 365:
		exp := s.now().Add(time.Duration(in.Days) * 24 * time.Hour)
		t.ExpiresAt = &exp
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": "days"})
		return
	}
	list, err := s.store.ListMCPTokens(r.Context())
	if err != nil {
		s.internalError(w, "mcp tokens", err)
		return
	}
	if len(list) >= mcpMaxTokens {
		writeError(w, http.StatusConflict, "too_many_tokens")
		return
	}
	secret, err := auth.NewToken(mcpTokenPrefix)
	if err != nil {
		s.internalError(w, "mcp token", err)
		return
	}
	t.Prefix = secret[:len(mcpTokenPrefix)+6]
	t, err = s.store.CreateMCPToken(r.Context(), t, auth.HashToken(secret))
	if err != nil {
		s.internalError(w, "mcp token", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "mcp_token_created", t.Name+" "+strings.Join(t.Scopes, ","))
	// The secret is shown once; only its hash stays.
	writeJSON(w, http.StatusOK, map[string]any{"token": t, "secret": secret, "endpoint": s.mcpEndpoint(r)})
}

func (s *Server) handleMCPDeleteToken(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	t, err := s.store.DeleteMCPToken(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "mcp token", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "mcp_token_revoked", t.Name)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// --- transport ---

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

// mcpAuth checks the bearer token. Failures count towards the same per-IP
// limit as the panel login and land in the fail2ban log.
func (s *Server) mcpAuth(w http.ResponseWriter, r *http.Request) (store.MCPToken, bool) {
	ip := s.clientIP(r)
	if blocked, wait := s.limiter.Blocked(ip); blocked {
		w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		writeError(w, http.StatusTooManyRequests, "too_many_attempts")
		return store.MCPToken{}, false
	}
	secret, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	secret = strings.TrimSpace(secret)
	var tok store.MCPToken
	err := store.ErrNotFound
	if ok && strings.HasPrefix(strings.ToLower(secret), mcpTokenPrefix) && len(secret) < 200 {
		tok, err = s.store.MCPTokenByHash(r.Context(), auth.HashToken(secret), s.now())
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.internalError(w, "mcp auth", err)
			return tok, false
		}
		if secret != "" {
			s.limiter.Fail(ip)
			s.authFailure(r.Context(), ip, "mcp_token")
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="srvmon"`)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return tok, false
	}
	if tok.LastUsedAt == nil || s.now().Sub(*tok.LastUsedAt) > mcpTouchEvery || tok.LastIP != ip {
		_ = s.store.TouchMCPToken(r.Context(), tok.ID, ip, s.now())
	}
	return tok, true
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		// No server-initiated stream and no sessions to delete.
		w.Header().Set("Allow", "POST")
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	tok, ok := s.mcpAuth(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, mcpBodyMax+1))
	if err != nil || len(body) > mcpBodyMax {
		writeJSON(w, http.StatusRequestEntityTooLarge, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{-32600, "request too large"}})
		return
	}
	body = bytes.TrimSpace(body)
	batch := len(body) > 0 && body[0] == '['
	var msgs []rpcMessage
	if batch {
		err = json.Unmarshal(body, &msgs)
	} else {
		var m rpcMessage
		err = json.Unmarshal(body, &m)
		msgs = []rpcMessage{m}
	}
	if err != nil || len(msgs) == 0 || len(msgs) > 20 {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{-32700, "parse error"}})
		return
	}
	var out []rpcResponse
	for _, m := range msgs {
		// Notifications and responses from the client need no answer.
		if m.Method == "" || len(m.ID) == 0 || string(m.ID) == "null" {
			continue
		}
		res, rerr := s.mcpDispatch(r.Context(), tok, m)
		resp := rpcResponse{JSONRPC: "2.0", ID: m.ID}
		if rerr != nil {
			resp.Error = rerr
		} else {
			resp.Result = res
		}
		out = append(out, resp)
	}
	if len(out) == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if batch {
		writeJSON(w, http.StatusOK, out)
		return
	}
	writeJSON(w, http.StatusOK, out[0])
}

func (s *Server) mcpDispatch(ctx context.Context, tok store.MCPToken, m rpcMessage) (any, *rpcError) {
	if m.JSONRPC != "2.0" {
		return nil, &rpcError{-32600, "jsonrpc must be 2.0"}
	}
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
		proto := p.ProtocolVersion
		if !mcpProtocols[proto] {
			proto = mcpLatestProto
		}
		return map[string]any{
			"protocolVersion": proto,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": mcpServerName, "title": "srvmon", "version": s.version},
			"instructions":    mcpInstructions,
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := []mcpTool{}
		for _, t := range mcpTools {
			if tok.Has(t.scope) {
				tools = append(tools, t)
			}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, &rpcError{-32602, "bad params"}
		}
		var tool *mcpTool
		for i := range mcpTools {
			if mcpTools[i].Name == p.Name {
				tool = &mcpTools[i]
			}
		}
		if tool == nil {
			return nil, &rpcError{-32602, "unknown tool: " + p.Name}
		}
		if !tok.Has(tool.scope) {
			return mcpText(true, fmt.Sprintf("this token has no %q scope; add it to a token in srvmon settings", tool.scope)), nil
		}
		if len(p.Arguments) == 0 || string(p.Arguments) == "null" {
			p.Arguments = json.RawMessage("{}")
		}
		return s.mcpCall(ctx, tok, tool.Name, p.Arguments), nil
	}
	return nil, &rpcError{-32601, "method not found: " + m.Method}
}

// --- tools ---

type mcpTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	scope       string
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

var (
	readOnly = map[string]any{"readOnlyHint": true, "openWorldHint": false}
	changes  = map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false}
)

var mcpTools = []mcpTool{
	{Name: "list_servers", Title: "List servers", scope: store.ScopeRead, Annotations: readOnly,
		Description: "All servers in srvmon with status, CPU, memory, max disk usage and a container summary.",
		InputSchema: obj(map[string]any{})},
	{Name: "get_server", Title: "Server details", scope: store.ScopeRead, Annotations: readOnly,
		Description: "One server in detail: load, memory, disks, every docker container with state and health, inventory, recent warnings from its logs.",
		InputSchema: obj(map[string]any{"server": str("server name from list_servers")}, "server")},
	{Name: "list_alerts", Title: "Open alerts", scope: store.ScopeRead, Annotations: readOnly,
		Description: "Alerts that are open now, and how many fired in the last 24 hours.",
		InputSchema: obj(map[string]any{})},
	{Name: "list_tasks", Title: "Scheduled tasks", scope: store.ScopeRead, Annotations: readOnly,
		Description: "Scheduled tasks of the srvmon AI agent with their last result.",
		InputSchema: obj(map[string]any{})},
	{Name: "run_command", Title: "Run a command", scope: store.ScopeExec, Annotations: changes,
		Description: "Run a shell command on a server over SSH (non-interactive, sh -c). Read-only commands (ps, df, docker ps, docker logs, journalctl, cat …) " +
			"run at once with a 40 s limit. Any other command waits for the owner to approve it in the srvmon panel or Telegram; if it is not decided " +
			"within 40 s the answer is status \"pending\" with an action_id for action_status. Output is capped at 32 KiB.",
		InputSchema: obj(map[string]any{
			"server":  str("server name from list_servers"),
			"command": str("the shell command, up to 2000 characters"),
			"reason":  str("why the command is needed, shown to the owner when it needs approval"),
		}, "server", "command")},
	{Name: "action_status", Title: "Command status", scope: store.ScopeExec, Annotations: readOnly,
		Description: "State and output of a command that needed approval. Waits up to 40 s for a change while it is pending or running.",
		InputSchema: obj(map[string]any{"action_id": map[string]any{"type": "integer", "description": "action_id from run_command"}}, "action_id")},
	{Name: "create_task", Title: "Create a scheduled task", scope: store.ScopeTasks, Annotations: changes,
		Description: "Schedule a prompt for the srvmon AI agent, for example a daily check. Its commands follow the same approval rules.",
		InputSchema: obj(map[string]any{
			"name":     str("short name, 1–64 characters"),
			"schedule": str("5-field cron in UTC (at most every 5 minutes) or @every 15m"),
			"prompt":   str("what the agent should do, up to 4000 characters"),
			"servers":  map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "server names; empty means all"},
			"notify":   map[string]any{"type": "string", "enum": []string{"", "telegram"}, "description": "telegram: send the result to Telegram"},
		}, "name", "schedule", "prompt")},
	{Name: "delete_task", Title: "Delete a scheduled task", scope: store.ScopeTasks, Annotations: changes,
		Description: "Delete a scheduled task by id (see list_tasks).",
		InputSchema: obj(map[string]any{"id": map[string]any{"type": "integer"}}, "id")},
}

func mcpText(isError bool, text string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
}

func mcpJSON(v any) map[string]any {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return mcpText(true, "internal error")
	}
	return mcpText(false, string(b))
}

func mcpArgs(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

func (s *Server) mcpCall(ctx context.Context, tok store.MCPToken, name string, raw json.RawMessage) map[string]any {
	who := "mcp:" + tok.Name
	switch name {
	case "list_servers":
		out, err := s.toolServerList(ctx)
		if err != nil {
			return mcpText(true, "internal error")
		}
		return mcpJSON(out)
	case "get_server":
		var a struct {
			Server string `json:"server"`
		}
		if err := mcpArgs(raw, &a); err != nil {
			return mcpText(true, "bad arguments: "+err.Error())
		}
		srv, err := s.serverByName(ctx, a.Server)
		if errors.Is(err, store.ErrNotFound) {
			return mcpText(true, "unknown server "+strconv.Quote(a.Server)+"; see list_servers")
		} else if err != nil {
			return mcpText(true, "internal error")
		}
		return mcpJSON(s.toolServerDetail(srv))
	case "list_alerts":
		open, err := s.store.OpenAlerts(ctx)
		if err != nil {
			return mcpText(true, "internal error")
		}
		n, err := s.store.CountAlertsSince(ctx, s.now().Add(-24*time.Hour))
		if err != nil {
			return mcpText(true, "internal error")
		}
		return mcpJSON(map[string]any{"open": open, "last24h": n})
	case "list_tasks":
		list, err := s.store.ListAgentTasks(ctx)
		if err != nil {
			return mcpText(true, "internal error")
		}
		return mcpJSON(list)
	case "run_command":
		var a struct {
			Server  string `json:"server"`
			Command string `json:"command"`
			Reason  string `json:"reason"`
		}
		if err := mcpArgs(raw, &a); err != nil {
			return mcpText(true, "bad arguments: "+err.Error())
		}
		return s.mcpRun(ctx, who, a.Server, a.Command, a.Reason)
	case "action_status":
		var a struct {
			ActionID int64 `json:"action_id"`
		}
		if err := mcpArgs(raw, &a); err != nil {
			return mcpText(true, "bad arguments: "+err.Error())
		}
		return s.mcpActionStatus(ctx, a.ActionID)
	case "create_task":
		var in taskInput
		if err := mcpArgs(raw, &in); err != nil {
			return mcpText(true, "bad arguments: "+err.Error())
		}
		t, field, err := s.createTask(ctx, in, who)
		if field != "" {
			return mcpText(true, "invalid "+field+": "+taskHints[field])
		}
		if err != nil {
			return mcpText(true, "internal error")
		}
		if s.ai == nil {
			return mcpJSON(map[string]any{"task": t, "note": "the srvmon AI agent is off: the task is saved but will not run until it is set up"})
		}
		return mcpJSON(t)
	case "delete_task":
		var a struct {
			ID int64 `json:"id"`
		}
		if err := mcpArgs(raw, &a); err != nil {
			return mcpText(true, "bad arguments: "+err.Error())
		}
		t, err := s.store.GetAgentTask(ctx, a.ID)
		if errors.Is(err, store.ErrNotFound) {
			return mcpText(true, "no task with this id; see list_tasks")
		} else if err != nil {
			return mcpText(true, "internal error")
		}
		if err := s.store.DeleteAgentTask(ctx, t.ID); err != nil {
			return mcpText(true, "internal error")
		}
		_ = s.store.Audit(ctx, who, "agent_task_deleted", t.Name)
		return mcpText(false, "deleted task #"+strconv.FormatInt(t.ID, 10)+" "+t.Name)
	}
	return mcpText(true, "unknown tool")
}

var taskHints = map[string]string{
	"name":     "name of 1–64 characters",
	"prompt":   "a prompt is required, up to 4000 characters",
	"schedule": "a 5-field cron (UTC, at most every 5 minutes) or @every 15m",
	"notify":   "notify: empty or telegram",
	"servers":  "an unknown server in the list",
}

func (s *Server) mcpRun(ctx context.Context, who, server, command, reason string) map[string]any {
	command = strings.TrimSpace(command)
	if command == "" || len(command) > 2000 {
		return mcpText(true, "command must be 1–2000 characters")
	}
	srv, err := s.serverByName(ctx, server)
	if errors.Is(err, store.ErrNotFound) {
		return mcpText(true, "unknown server "+strconv.Quote(server)+"; see list_servers")
	} else if err != nil {
		return mcpText(true, "internal error")
	}
	access, err := s.store.ServerSSH(ctx, srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		return mcpText(true, "srvmon has no ssh access to this server")
	} else if err != nil {
		return mcpText(true, "internal error")
	}

	ro, why := aiagent.Classify(command)
	if ro {
		code, out, err := s.runOnServer(ctx, access, command, mcpReadTimeout)
		_ = s.store.Audit(context.Background(), who, "agent_exec", srv.Name+": "+truncate(command, 500))
		res := execResult{Status: "ok", ReadOnly: true, ExitCode: &code, Output: out}
		if err != nil {
			res.Status, res.Note, res.ExitCode = "error", err.Error(), nil
		} else if code != 0 {
			res.Status = "error"
		}
		return mcpJSON(res)
	}

	s.mcp.mu.Lock()
	busy := len(s.mcp.pending) >= mcpMaxPending
	s.mcp.mu.Unlock()
	if busy {
		return mcpText(true, "too many commands already wait for the owner; wait for them with action_status")
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = why
	}
	action, err := s.store.CreateAgentAction(ctx, store.AgentAction{
		ServerID: srv.ID, ServerName: srv.Name, Command: command, Reason: truncate(reason, 500), Source: who,
	})
	if err != nil {
		return mcpText(true, "internal error")
	}
	s.superviseAction(action, access, who)
	return s.mcpActionStatus(ctx, action.ID)
}

// superviseAction waits for the owner's decision on an MCP action and runs
// it when approved, independent of the MCP request that asked for it.
func (s *Server) superviseAction(action store.AgentAction, access store.SSHAccess, who string) {
	ch := make(chan string, 1)
	done := make(chan struct{})
	s.agent.mu.Lock()
	s.agent.waiters[action.ID] = ch
	s.agent.mu.Unlock()
	s.mcp.mu.Lock()
	s.mcp.pending[action.ID] = done
	s.mcp.mu.Unlock()
	_ = s.store.Audit(context.Background(), who, "agent_action_pending", fmt.Sprintf("#%d %s: %s", action.ID, action.ServerName, truncate(action.Command, 500)))
	if s.onActionPending != nil {
		s.onActionPending(action)
	}
	go func() {
		defer func() {
			s.mcp.mu.Lock()
			delete(s.mcp.pending, action.ID)
			s.mcp.mu.Unlock()
			close(done)
		}()
		timer := time.NewTimer(agentConfirmWait)
		var decision string
		select {
		case decision = <-ch:
		case <-timer.C:
		}
		timer.Stop()
		s.agent.mu.Lock()
		delete(s.agent.waiters, action.ID)
		s.agent.mu.Unlock()
		if decision == "" {
			if ok, _ := s.store.DecideAgentAction(context.Background(), action.ID, store.ActionExpired, "timeout"); !ok {
				if a, err := s.store.GetAgentAction(context.Background(), action.ID); err == nil {
					decision = a.State
				}
			}
		}
		if decision != store.ActionApproved {
			return
		}
		code, out, err := s.runOnServer(context.Background(), access, action.Command, toolWriteTimeout)
		ok := err == nil && code == 0
		stored := out
		if err != nil {
			stored = strings.TrimSpace(out + "\n" + err.Error())
		}
		_ = s.store.FinishAgentAction(context.Background(), action.ID, ok, code, truncate(stored, toolOutputMax))
		_ = s.store.Audit(context.Background(), who, "agent_exec_confirmed", fmt.Sprintf("#%d %s: exit %d", action.ID, action.ServerName, code))
	}()
}

type mcpActionView struct {
	ActionID int64  `json:"action_id"`
	Status   string `json:"status"` // pending | running | done | failed | rejected | expired
	Server   string `json:"server"`
	Command  string `json:"command"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output,omitempty"`
	Note     string `json:"note,omitempty"`
}

// mcpActionStatus waits up to mcpWait for an MCP action to finish and
// reports its state. Only actions from MCP clients are visible: the built-in
// agent's commands stay in the panel.
func (s *Server) mcpActionStatus(ctx context.Context, id int64) map[string]any {
	a, err := s.store.GetAgentAction(ctx, id)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !strings.HasPrefix(a.Source, "mcp:")) {
		return mcpText(true, "no command with this action_id")
	} else if err != nil {
		return mcpText(true, "internal error")
	}
	s.mcp.mu.Lock()
	done := s.mcp.pending[id]
	s.mcp.mu.Unlock()
	if done != nil {
		timer := time.NewTimer(mcpWait)
		select {
		case <-done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
		if a2, err := s.store.GetAgentAction(context.Background(), id); err == nil {
			a = a2
		}
	}
	v := mcpActionView{ActionID: a.ID, Status: a.State, Server: a.ServerName, Command: a.Command, ExitCode: a.ExitCode, Output: a.Output}
	switch a.State {
	case store.ActionPending:
		v.Note = "waiting for the owner to approve it in the srvmon panel or Telegram (up to 15 minutes from " +
			a.CreatedAt.Format(time.RFC3339) + "); tell the user and call action_status again"
	case store.ActionApproved:
		v.Status = "running"
		v.Note = "approved and running; call action_status again for the output"
	case store.ActionRejected:
		v.Note = "the owner rejected the command; do not retry it"
	case store.ActionExpired:
		v.Note = "the command was not approved in time"
	}
	return mcpJSON(v)
}

func (s *Server) mcpRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/mcp", s.handleMCP)
	// MCP clients look for OAuth metadata after a 401; answer JSON, not the
	// SPA page, so they report the bad token instead of a parse error.
	mux.HandleFunc("GET /.well-known/oauth-protected-resource", s.notFoundJSON)
	mux.HandleFunc("GET /.well-known/oauth-protected-resource/", s.notFoundJSON)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.notFoundJSON)
	mux.Handle("GET /api/mcp/tokens", s.requireSession(http.HandlerFunc(s.handleMCPTokens)))
	mux.Handle("POST /api/mcp/tokens", s.requireSession(http.HandlerFunc(s.handleMCPNewToken)))
	mux.Handle("DELETE /api/mcp/tokens/{id}", s.requireSession(http.HandlerFunc(s.handleMCPDeleteToken)))
}

func (s *Server) notFoundJSON(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotFound, "not_found")
}
