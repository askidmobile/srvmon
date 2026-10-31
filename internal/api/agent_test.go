package api

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

const testAIToken = "test-token-0123456789abcdef0123456789"

// fakeSSHD accepts one client key and answers exec requests with handler.
type fakeSSHD struct {
	addr    string
	hostKey ssh.PublicKey
	mu      sync.Mutex
	execs   []string
}

func newFakeSSHD(t *testing.T, clientKey ssh.PublicKey, handler func(cmd string) (string, uint32)) *fakeSSHD {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	f := &fakeSSHD{hostKey: signer.PublicKey()}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if bytes.Equal(k.Marshal(), clientKey.Marshal()) {
			return nil, nil
		}
		return nil, fmt.Errorf("denied")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go func() {
						defer ch.Close()
						for req := range creqs {
							var p struct{ Cmd string }
							_ = ssh.Unmarshal(req.Payload, &p)
							req.Reply(req.Type == "exec", nil)
							if req.Type != "exec" {
								continue
							}
							f.mu.Lock()
							f.execs = append(f.execs, p.Cmd)
							f.mu.Unlock()
							out, st := handler(p.Cmd)
							io.WriteString(ch, out)
							ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{st}))
							return
						}
					}()
				}
			}()
		}
	}()
	return f
}

func (f *fakeSSHD) commands() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.execs...)
}

// agentEnv is an env with a server reachable over (fake) SSH and the tool
// API mounted.
type agentEnv struct {
	*env
	sshd  *fakeSSHD
	tools *httptest.Server
	srvID int64
}

func newAgentEnv(t *testing.T, aiURL string) *agentEnv {
	t.Helper()
	e := newEnv(t)
	e.loginSSH(t)
	e.srv.cfg.AIToken = testAIToken
	e.srv.ai = aiagent.NewClient(aiURL, testAIToken)
	kp, err := sshgw.GenerateKey("test")
	if err != nil {
		t.Fatal(err)
	}
	signer, _ := ssh.ParsePrivateKey(kp.Private)
	sshd := newFakeSSHD(t, signer.PublicKey(), func(cmd string) (string, uint32) {
		if strings.Contains(cmd, "fail") {
			return "boom\n", 3
		}
		return "out of " + cmd + "\n", 0
	})
	ctx := context.Background()
	id, err := e.st.CreateServer(ctx, store.Server{Name: "prod-api-01"})
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(sshd.addr)
	var p int
	fmt.Sscan(port, &p)
	sealed, err := e.srv.secrets.Seal(kp.Private, keyAAD(id))
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerSSH(ctx, id, host, p, "srvmon", sshgw.FormatHostKey(sshd.hostKey), sealed); err != nil {
		t.Fatal(err)
	}
	tools := httptest.NewServer(e.srv.AgentToolsHandler())
	t.Cleanup(tools.Close)
	return &agentEnv{env: e, sshd: sshd, tools: tools, srvID: id}
}

func (a *agentEnv) tool(t *testing.T, method, path string, body any, out any) int {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, a.tools.URL+path, rd)
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func TestAgentRoutesRequireSession(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/agent/status"},
		{"GET", "/api/agent/chats"},
		{"POST", "/api/agent/chats"},
		{"POST", "/api/agent/chats/1/run"},
		{"GET", "/api/agent/actions"},
		{"POST", "/api/agent/actions/1/approve"},
		{"GET", "/api/agent/tasks"},
		{"POST", "/api/agent/tasks"},
	} {
		req, _ := http.NewRequest(c.method, e.ts.URL+c.path, strings.NewReader("{}"))
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s: %d, want 401", c.method, c.path, resp.StatusCode)
		}
	}
}

func TestToolAPIRequiresToken(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	for _, tok := range []string{"", "Bearer wrong", "Bearer " + testAIToken[:10]} {
		req, _ := http.NewRequest("GET", a.tools.URL+"/tools/servers", nil)
		if tok != "" {
			req.Header.Set("Authorization", tok)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("token %q: %d", tok, resp.StatusCode)
		}
	}
	var list []toolServerView
	if code := a.tool(t, "GET", "/tools/servers", nil, &list); code != 200 || len(list) != 1 || !list[0].SSH {
		t.Fatalf("servers: %d %+v", code, list)
	}
	// The panel session does not open the tool API, and the tool token does
	// not open the panel API.
	req, _ := http.NewRequest("GET", a.ts.URL+"/tools/servers", nil)
	req.Header.Set("Authorization", "Bearer "+testAIToken)
	resp, _ := http.DefaultClient.Do(req)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(body), "prod-api-01") {
		t.Fatal("tool API reachable on the public handler")
	}
}

func TestToolExecReadOnlyRunsImmediately(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	var res execResult
	if code := a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "docker ps -a"}, &res); code != 200 {
		t.Fatal(code)
	}
	if res.Status != "ok" || !res.ReadOnly || !strings.Contains(res.Output, "docker ps -a") {
		t.Fatalf("%+v", res)
	}
	cmds := a.sshd.commands()
	if len(cmds) != 1 || cmds[0] != "timeout -k 5 60 sh -c 'docker ps -a'" {
		t.Fatalf("ran %q", cmds)
	}
	pending, _ := a.st.ListAgentActions(context.Background(), "", 10)
	if len(pending) != 0 {
		t.Fatalf("read-only command created an action: %+v", pending)
	}
	if code := a.tool(t, "POST", "/tools/exec", map[string]any{"server": "nope", "command": "ls"}, nil); code != 404 {
		t.Fatalf("unknown server: %d", code)
	}
}

func (a *agentEnv) waitPending(t *testing.T) store.AgentAction {
	t.Helper()
	for i := 0; i < 200; i++ {
		list, _ := a.st.ListAgentActions(context.Background(), store.ActionPending, 10)
		if len(list) == 1 {
			return list[0]
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("no pending action")
	return store.AgentAction{}
}

func TestToolExecMutatingWaitsForApproval(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	done := make(chan execResult, 1)
	go func() {
		var res execResult
		a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "docker restart crm", "reason": "OOM", "chat_id": 7}, &res)
		done <- res
	}()
	act := a.waitPending(t)
	if act.Command != "docker restart crm" || act.Reason != "OOM" || act.ChatID == nil || *act.ChatID != 7 {
		t.Fatalf("%+v", act)
	}
	if n := len(a.sshd.commands()); n != 0 {
		t.Fatalf("ran before approval: %d", n)
	}
	var decided store.AgentAction
	if code := a.postJSON(t, fmt.Sprintf("/api/agent/actions/%d/approve", act.ID), nil, &decided); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	res := <-done
	if res.Status != "ok" || res.ReadOnly || !strings.Contains(res.Output, "docker restart crm") {
		t.Fatalf("%+v", res)
	}
	got, _ := a.st.GetAgentAction(context.Background(), act.ID)
	if got.State != store.ActionDone || got.ExitCode == nil || *got.ExitCode != 0 {
		t.Fatalf("%+v", got)
	}
	if code := a.postJSON(t, fmt.Sprintf("/api/agent/actions/%d/approve", act.ID), nil, nil); code != http.StatusConflict {
		t.Fatalf("second decision: %d", code)
	}

	go func() {
		var res execResult
		a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "rm -rf /srv/x"}, &res)
		done <- res
	}()
	act = a.waitPending(t)
	if code := a.postJSON(t, fmt.Sprintf("/api/agent/actions/%d/reject", act.ID), nil, nil); code != 200 {
		t.Fatalf("reject: %d", code)
	}
	if res := <-done; res.Status != "rejected" {
		t.Fatalf("%+v", res)
	}
	if n := len(a.sshd.commands()); n != 1 {
		t.Fatalf("rejected command ran: %v", a.sshd.commands())
	}
}

func TestToolExecFailedCommand(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	var res execResult
	a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "cat /fail"}, &res)
	if res.Status != "error" || res.ExitCode == nil || *res.ExitCode != 3 {
		t.Fatalf("%+v", res)
	}
}

func TestToolTasks(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	var bad map[string]string
	if code := a.tool(t, "POST", "/tools/tasks", map[string]any{"name": "x", "schedule": "* * * * *", "prompt": "p"}, &bad); code != 400 || bad["field"] != "schedule" {
		t.Fatalf("%d %v", code, bad)
	}
	var task store.AgentTask
	if code := a.tool(t, "POST", "/tools/tasks", map[string]any{
		"name": "restart-watch", "schedule": "*/15 * * * *", "prompt": "проверь рестарты", "servers": []string{"prod-api-01"}, "notify": "telegram",
	}, &task); code != 200 {
		t.Fatal(code)
	}
	if task.ID == 0 || task.NextRunAt == nil || !task.Enabled || task.Servers[0] != "prod-api-01" {
		t.Fatalf("%+v", task)
	}
	var list []store.AgentTask
	if a.tool(t, "GET", "/tools/tasks", nil, &list); len(list) != 1 {
		t.Fatalf("%+v", list)
	}
	if code := a.tool(t, "DELETE", fmt.Sprintf("/tools/tasks/%d", task.ID), nil, nil); code != 200 {
		t.Fatal(code)
	}
}

// fakeAI imitates srvmon-ai: it streams a reply with one tool call.
func fakeAI(t *testing.T) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+testAIToken {
			w.WriteHeader(401)
			return
		}
		switch {
		case r.URL.Path == "/v1/status":
			json.NewEncoder(w).Encode(aiagent.Status{Ready: true, Provider: "deepseek", Model: "deepseek-v4-pro"})
		case strings.HasSuffix(r.URL.Path, "/run"):
			var req aiagent.RunRequest
			json.NewDecoder(r.Body).Decode(&req)
			enc := json.NewEncoder(w)
			enc.Encode(aiagent.Event{T: "text", D: "Смотрю "})
			enc.Encode(aiagent.Event{T: "tool", ID: "c1", Name: "srvmon_exec", Args: json.RawMessage(`{"server":"prod-api-01"}`)})
			enc.Encode(aiagent.Event{T: "tool_end", ID: "c1", OK: true, Out: "ok"})
			enc.Encode(aiagent.Event{T: "text", D: "контекст " + req.Server})
			enc.Encode(aiagent.Event{T: "done", Text: "Смотрю контекст " + req.Server})
		default:
			w.WriteHeader(200)
		}
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestAgentChatRunStreamsAndStores(t *testing.T) {
	ai := fakeAI(t)
	a := newAgentEnv(t, ai.URL)
	var chat chatView
	if code := a.postJSON(t, "/api/agent/chats", map[string]any{"server_id": a.srvID}, &chat); code != 200 || chat.Server != "prod-api-01" {
		t.Fatalf("%d %+v", code, chat)
	}
	b, _ := json.Marshal(map[string]string{"text": "почему рестарты?"})
	resp, err := a.client.Post(fmt.Sprintf("%s/api/agent/chats/%d/run", a.ts.URL, chat.ID), "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/x-ndjson" {
		t.Fatalf("content type %q", ct)
	}
	var types []string
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		var ev aiagent.Event
		json.Unmarshal(sc.Bytes(), &ev)
		types = append(types, ev.T)
	}
	resp.Body.Close()
	if strings.Join(types, ",") != "start,text,tool,tool_end,text,done" {
		t.Fatalf("events %v", types)
	}
	var got struct {
		Chat     chatView             `json:"chat"`
		Messages []store.AgentMessage `json:"messages"`
	}
	r := a.get(t, fmt.Sprintf("/api/agent/chats/%d", chat.ID))
	json.NewDecoder(r.Body).Decode(&got)
	r.Body.Close()
	if len(got.Messages) != 2 || got.Messages[0].Role != "user" || got.Messages[1].Text != "Смотрю контекст prod-api-01" ||
		len(got.Messages[1].Tools) != 1 || !got.Messages[1].Tools[0].OK || got.Chat.Title != "почему рестарты?" {
		t.Fatalf("%+v", got)
	}
	var st map[string]any
	r = a.get(t, "/api/agent/status")
	json.NewDecoder(r.Body).Decode(&st)
	r.Body.Close()
	if st["ready"] != true || st["model"] != "deepseek-v4-pro" {
		t.Fatalf("%v", st)
	}
}

func TestAgentDisabled(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	var chat chatView
	e.postJSON(t, "/api/agent/chats", map[string]any{}, &chat)
	if code := e.postJSON(t, fmt.Sprintf("/api/agent/chats/%d/run", chat.ID), map[string]string{"text": "hi"}, nil); code != http.StatusServiceUnavailable {
		t.Fatalf("%d", code)
	}
}
