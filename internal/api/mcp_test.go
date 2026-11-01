package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/store"
)

type mcpCreated struct {
	Token    store.MCPToken `json:"token"`
	Secret   string         `json:"secret"`
	Endpoint string         `json:"endpoint"`
}

func (a *agentEnv) newMCPToken(t *testing.T, scopes ...string) mcpCreated {
	t.Helper()
	var c mcpCreated
	if code := a.postJSON(t, "/api/mcp/tokens", map[string]any{"name": "laptop", "scopes": scopes}, &c); code != 200 {
		t.Fatalf("create token: %d", code)
	}
	if !strings.HasPrefix(c.Secret, "smcp_") || !strings.HasPrefix(c.Secret, c.Token.Prefix) || !strings.HasSuffix(c.Endpoint, "/mcp") {
		t.Fatalf("created %+v", c)
	}
	return c
}

// rpc posts one JSON-RPC message to /mcp; out gets the decoded answer.
func (a *agentEnv) rpc(t *testing.T, secret string, body string, out any) int {
	t.Helper()
	req, _ := http.NewRequest("POST", a.ts.URL+"/mcp", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if secret != "" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		b, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("decode %q: %v", b, err)
		}
	}
	return resp.StatusCode
}

type toolResult struct {
	Result struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	} `json:"result"`
	Error *rpcError `json:"error"`
}

func (r toolResult) text() string {
	if len(r.Result.Content) == 0 {
		return ""
	}
	return r.Result.Content[0].Text
}

func (a *agentEnv) call(t *testing.T, secret, tool string, args any) toolResult {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 7, "method": "tools/call",
		"params": map[string]any{"name": tool, "arguments": args}})
	var r toolResult
	if code := a.rpc(t, secret, string(b), &r); code != 200 {
		t.Fatalf("%s: %d", tool, code)
	}
	return r
}

func TestMCPTokensRequireSession(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/mcp/tokens"}, {"POST", "/api/mcp/tokens"}, {"DELETE", "/api/mcp/tokens/1"},
	} {
		if r := e.do(t, c.method, c.path, map[string]any{}); r.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: %d", c.method, c.path, r.StatusCode)
		}
	}
}

func TestMCPHandshakeAndScopes(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	tok := a.newMCPToken(t, "read")

	var init struct {
		Result struct {
			ProtocolVersion string `json:"protocolVersion"`
			ServerInfo      struct {
				Name string `json:"name"`
			} `json:"serverInfo"`
			Capabilities map[string]any `json:"capabilities"`
		} `json:"result"`
	}
	if code := a.rpc(t, tok.Secret, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`, &init); code != 200 {
		t.Fatal(code)
	}
	if init.Result.ProtocolVersion != "2025-03-26" || init.Result.ServerInfo.Name != "srvmon" || init.Result.Capabilities["tools"] == nil {
		t.Fatalf("initialize: %+v", init)
	}
	// Notifications get 202 without a body.
	if code := a.rpc(t, tok.Secret, `{"jsonrpc":"2.0","method":"notifications/initialized"}`, nil); code != http.StatusAccepted {
		t.Fatalf("notification: %d", code)
	}

	var list struct {
		Result struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		} `json:"result"`
	}
	a.rpc(t, tok.Secret, `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`, &list)
	names := []string{}
	for _, tl := range list.Result.Tools {
		names = append(names, tl.Name)
	}
	if strings.Join(names, ",") != "list_servers,get_server,list_alerts,list_tasks" {
		t.Fatalf("read token sees %v", names)
	}

	r := a.call(t, tok.Secret, "list_servers", nil)
	if r.Result.IsError || !strings.Contains(r.text(), "prod-api-01") {
		t.Fatalf("list_servers: %+v", r)
	}
	r = a.call(t, tok.Secret, "get_server", map[string]any{"server": "prod-api-01"})
	if r.Result.IsError || !strings.Contains(r.text(), `"name": "prod-api-01"`) {
		t.Fatalf("get_server: %+v", r)
	}
	// No exec scope: the command does not run.
	r = a.call(t, tok.Secret, "run_command", map[string]any{"server": "prod-api-01", "command": "uptime"})
	if !r.Result.IsError || !strings.Contains(r.text(), `"exec"`) || len(a.sshd.commands()) != 0 {
		t.Fatalf("run_command without scope: %+v %v", r, a.sshd.commands())
	}
	r = a.call(t, tok.Secret, "nope", nil)
	if r.Error == nil || r.Error.Code != -32602 {
		t.Fatalf("unknown tool: %+v", r)
	}
	var unknown struct {
		Error *rpcError `json:"error"`
	}
	a.rpc(t, tok.Secret, `{"jsonrpc":"2.0","id":3,"method":"resources/list"}`, &unknown)
	if unknown.Error == nil || unknown.Error.Code != -32601 {
		t.Fatalf("unknown method: %+v", unknown)
	}
	// A batch answers every request in it.
	var batch []rpcResponse
	a.rpc(t, tok.Secret, `[{"jsonrpc":"2.0","id":1,"method":"ping"},{"jsonrpc":"2.0","method":"notifications/x"},{"jsonrpc":"2.0","id":2,"method":"ping"}]`, &batch)
	if len(batch) != 2 {
		t.Fatalf("batch: %+v", batch)
	}
}

func TestMCPAuth(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	tok := a.newMCPToken(t, "read", "exec")

	// The listing never shows the secret.
	resp := a.do(t, "GET", "/api/mcp/tokens", nil)
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || bytes.Contains(body, []byte(tok.Secret)) || !bytes.Contains(body, []byte(tok.Token.Prefix)) {
		t.Fatalf("list: %d %s", resp.StatusCode, body)
	}
	ping := `{"jsonrpc":"2.0","id":1,"method":"ping"}`
	if code := a.rpc(t, tok.Secret, ping, nil); code != 200 {
		t.Fatalf("valid token: %d", code)
	}
	if code := a.rpc(t, "", ping, nil); code != 401 {
		t.Fatalf("no token: %d", code)
	}
	// The panel session cookie does not open /mcp.
	if r := a.do(t, "POST", "/mcp", json.RawMessage(ping)); r.StatusCode != 401 {
		t.Fatalf("session cookie: %d", r.StatusCode)
	}
	if code := a.rpc(t, tok.Secret, ping, nil); code != 200 {
		t.Fatal("valid token after a missing one")
	}
	// GET has no stream here.
	if r := a.do(t, "GET", "/mcp", nil); r.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", r.StatusCode)
	}

	// Revoked: 401.
	if r := a.do(t, "DELETE", "/api/mcp/tokens/"+itoa(tok.Token.ID), nil); r.StatusCode != 200 {
		t.Fatalf("revoke: %d", r.StatusCode)
	}
	if code := a.rpc(t, tok.Secret, ping, nil); code != 401 {
		t.Fatalf("revoked token: %d", code)
	}
	if !strings.Contains(a.authLog.String(), "stage=mcp_token") {
		t.Fatalf("auth log: %q", a.authLog.String())
	}
	// Guessing is limited like the login.
	for i := 0; i < 5; i++ {
		a.rpc(t, "smcp_guess", ping, nil)
	}
	if code := a.rpc(t, tok.Secret, ping, nil); code != http.StatusTooManyRequests {
		t.Fatalf("after guesses: %d", code)
	}
}

func TestMCPTokenValidation(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	for _, body := range []map[string]any{
		{"name": "", "scopes": []string{"read"}},
		{"name": "x", "scopes": []string{}},
		{"name": "x", "scopes": []string{"root"}},
		{"name": "x", "scopes": []string{"read"}, "days": 7},
	} {
		if code := a.postJSON(t, "/api/mcp/tokens", body, nil); code != 400 {
			t.Errorf("%v: %d", body, code)
		}
	}
	var c mcpCreated
	a.postJSON(t, "/api/mcp/tokens", map[string]any{"name": "short", "scopes": []string{"read"}, "days": 30}, &c)
	if c.Token.ExpiresAt == nil || c.Token.ExpiresAt.Before(time.Now().Add(29*24*time.Hour)) {
		t.Fatalf("expiry: %+v", c.Token)
	}
	// An expired token stops working.
	a.srv.now = func() time.Time { return time.Now().Add(31 * 24 * time.Hour) }
	if code := a.rpc(t, c.Secret, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, nil); code != 401 {
		t.Fatalf("expired: %d", code)
	}
}

func TestMCPRunCommand(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	tok := a.newMCPToken(t, "read", "exec")
	var notified []store.AgentAction
	var mu sync.Mutex
	a.srv.onActionPending = func(x store.AgentAction) {
		mu.Lock()
		notified = append(notified, x)
		mu.Unlock()
	}

	// Read-only: runs at once, no action.
	r := a.call(t, tok.Secret, "run_command", map[string]any{"server": "prod-api-01", "command": "docker ps -a"})
	if r.Result.IsError || !strings.Contains(r.text(), `"read_only": true`) {
		t.Fatalf("read-only: %+v", r)
	}
	if cmds := a.sshd.commands(); len(cmds) != 1 || cmds[0] != "timeout -k 5 40 sh -c 'docker ps -a'" {
		t.Fatalf("ran %q", cmds)
	}

	// Changing, approved while the call waits.
	done := make(chan toolResult, 1)
	go func() {
		done <- a.call(t, tok.Secret, "run_command", map[string]any{"server": "prod-api-01", "command": "docker restart api", "reason": "api hangs"})
	}()
	act := a.waitPending(t)
	if act.Source != "mcp:laptop" || act.Reason != "api hangs" || act.ChatID != nil {
		t.Fatalf("action %+v", act)
	}
	if len(a.sshd.commands()) != 1 {
		t.Fatal("ran before approval")
	}
	if !a.srv.DecideAgentAction(context.Background(), act.ID, store.ActionApproved, "test") {
		t.Fatal("decide")
	}
	r = <-done
	if r.Result.IsError || !strings.Contains(r.text(), `"status": "done"`) || !strings.Contains(r.text(), "out of") {
		t.Fatalf("approved: %+v", r)
	}
	mu.Lock()
	if len(notified) != 1 || notified[0].ID != act.ID {
		t.Fatalf("notified %+v", notified)
	}
	mu.Unlock()

	// Not decided within the wait: pending, then action_status.
	old := mcpWait
	mcpWait = 50 * time.Millisecond
	t.Cleanup(func() { mcpWait = old })
	r = a.call(t, tok.Secret, "run_command", map[string]any{"server": "prod-api-01", "command": "rm -rf /tmp/x"})
	if !strings.Contains(r.text(), `"status": "pending"`) {
		t.Fatalf("pending: %+v", r)
	}
	act = a.waitPending(t)
	a.srv.DecideAgentAction(context.Background(), act.ID, store.ActionRejected, "test")
	mcpWait = 2 * time.Second
	r = a.call(t, tok.Secret, "action_status", map[string]any{"action_id": act.ID})
	if !strings.Contains(r.text(), `"status": "rejected"`) {
		t.Fatalf("status: %+v", r)
	}
	if len(a.sshd.commands()) != 2 {
		t.Fatalf("rejected command ran: %q", a.sshd.commands())
	}

	// Actions of the built-in agent are not visible over MCP.
	own, _ := a.st.CreateAgentAction(context.Background(), store.AgentAction{ServerID: a.srvID, ServerName: "prod-api-01", Command: "reboot"})
	r = a.call(t, tok.Secret, "action_status", map[string]any{"action_id": own.ID})
	if !r.Result.IsError {
		t.Fatalf("agent action visible: %+v", r)
	}
	r = a.call(t, tok.Secret, "run_command", map[string]any{"server": "nope", "command": "ls"})
	if !r.Result.IsError || !strings.Contains(r.text(), "unknown server") {
		t.Fatalf("unknown server: %+v", r)
	}
}

func TestMCPTasks(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	tok := a.newMCPToken(t, "read", "tasks")
	r := a.call(t, tok.Secret, "create_task", map[string]any{"name": "disk", "schedule": "@every 1h", "prompt": "check disks"})
	if r.Result.IsError || !strings.Contains(r.text(), `"name": "disk"`) {
		t.Fatalf("create: %+v", r)
	}
	r = a.call(t, tok.Secret, "create_task", map[string]any{"name": "x", "schedule": "never", "prompt": "p"})
	if !r.Result.IsError || !strings.Contains(r.text(), "schedule") {
		t.Fatalf("bad schedule: %+v", r)
	}
	list, _ := a.st.ListAgentTasks(context.Background())
	if len(list) != 1 {
		t.Fatalf("tasks %+v", list)
	}
	r = a.call(t, tok.Secret, "delete_task", map[string]any{"id": list[0].ID})
	if r.Result.IsError {
		t.Fatalf("delete: %+v", r)
	}
	if list, _ = a.st.ListAgentTasks(context.Background()); len(list) != 0 {
		t.Fatalf("left %+v", list)
	}
}

// Concurrent calls cannot queue more than mcpMaxPending confirmations.
func TestMCPPendingCapIsAtomic(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	tok := a.newMCPToken(t, "exec")
	a.srv.onActionPending = nil
	old := mcpWait
	mcpWait = 10 * time.Millisecond
	t.Cleanup(func() { mcpWait = old })
	var wg sync.WaitGroup
	for i := 0; i < mcpMaxPending+15; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			a.call(t, tok.Secret, "run_command", map[string]any{"server": "prod-api-01", "command": "touch /tmp/x"})
		}()
	}
	wg.Wait()
	list, _ := a.st.ListAgentActions(context.Background(), store.ActionPending, 100)
	if len(list) != mcpMaxPending {
		t.Fatalf("pending %d, want %d", len(list), mcpMaxPending)
	}
	r := a.call(t, tok.Secret, "run_command", map[string]any{"server": "prod-api-01", "command": "touch /tmp/y"})
	if !r.Result.IsError || !strings.Contains(r.text(), "too many") {
		t.Fatalf("over the cap: %+v", r)
	}
}
