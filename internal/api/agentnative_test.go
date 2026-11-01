package api

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

func TestAgentBinaries(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.srv.cfg.AgentBinDir = dir
	if _, err := e.srv.agentBinaries("amd64"); err == nil {
		t.Fatal("missing binary accepted")
	}
	if _, err := e.srv.agentBinaries("mips"); err == nil {
		t.Fatal("unknown arch accepted")
	}
	if err := os.WriteFile(filepath.Join(dir, "srvmon-agent-linux-amd64"), []byte("bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := e.srv.agentBinaries("amd64")
	if err != nil {
		t.Fatal(err)
	}
	// sha256("bin"); the panel's version is the expected agent version.
	if string(b.Data) != "bin" || b.SHA256 != "51a1f05af85e342e3c849b47d387086476282d5f50dc240c19216d6edfb1eb5a" || b.Version != "test" {
		t.Fatalf("binary %+v", b)
	}
}

func TestAgentBinaryDownloadNeedsToken(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	e.srv.cfg.AgentBinDir = dir
	_ = os.WriteFile(filepath.Join(dir, "srvmon-agent-linux-arm64"), []byte("arm"), 0o755)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "pve"})
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("a_good"))
	get := func(path, token string) (int, string) {
		req, _ := http.NewRequest(http.MethodGet, e.ts.URL+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if c, _ := get("/agent/srvmon-agent-linux-arm64", ""); c != http.StatusUnauthorized {
		t.Errorf("no token: %d", c)
	}
	if c, _ := get("/agent/srvmon-agent-linux-arm64", "a_bad"); c != http.StatusUnauthorized {
		t.Errorf("bad token: %d", c)
	}
	if c, b := get("/agent/srvmon-agent-linux-arm64", "a_good"); c != http.StatusOK || b != "arm" {
		t.Errorf("good token: %d %q", c, b)
	}
	if c, _ := get("/agent/srvmon-agent-linux-amd64", "a_good"); c != http.StatusNotFound {
		t.Errorf("missing arch: %d", c)
	}
	if c, _ := get("/agent/other", "a_good"); c != http.StatusNotFound {
		t.Errorf("other file: %d", c)
	}
	c, script := get("/agent/install.sh", "")
	if c != http.StatusOK || !strings.HasPrefix(script, "#!/bin/sh") || !strings.Contains(script, "URL='"+e.ts.URL+"'") ||
		!strings.Contains(script, "/agent/srvmon-agent-linux-$A") {
		t.Errorf("install.sh: %d\n%s", c, script)
	}
}

// A reissued token works next to the old one until an agent first pushes
// with it; then the old one is revoked.
func TestAgentTokenReissue(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "pve"})
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("a_old"))
	if c := e.postJSON(t, "/api/servers/"+itoa(id)+"/agent/token", nil, nil); c != http.StatusUnauthorized {
		t.Fatalf("without session: %d", c)
	}
	e.login(t)
	var v agentTokenView
	if c := e.postJSON(t, "/api/servers/"+itoa(id)+"/agent/token", map[string]bool{"write": true}, &v); c != http.StatusOK {
		t.Fatalf("reissue: %d", c)
	}
	if !strings.HasPrefix(v.Token, "a_") || !strings.Contains(v.Systemd, "SRVMON_TOKEN="+v.Token) ||
		!strings.Contains(v.Docker, v.Token) || !strings.Contains(v.Docker, "--name srvmon-agent ") {
		t.Fatalf("view %+v", v)
	}
	// No SSH key stored: the token is issued, writing it is not possible.
	if v.Written || v.WriteError == "" {
		t.Errorf("write without ssh: %+v", v)
	}
	if c := e.postJSON(t, "/api/servers/999/agent/token", nil, nil); c != http.StatusNotFound {
		t.Errorf("unknown server: %d", c)
	}
	e.ingest(t, "a_old", protocol.Report{Hostname: "pve"})
	e.ingest(t, v.Token, protocol.Report{Hostname: "pve"})
	if ok, _ := e.st.AgentTokenValid(ctx, auth.HashToken("a_old")); ok {
		t.Error("the old token survived the new one's first push")
	}
	if ok, _ := e.st.AgentTokenValid(ctx, auth.HashToken(v.Token)); !ok {
		t.Error("the new token is not valid")
	}
}

// A server without an agent token is SSH only; with one it waits for or
// has an agent.
func TestServerHasAgent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "ssh-only"})
	srv, _ := e.st.GetServer(ctx, id)
	if srv.HasAgent || srv.LastSeen != nil {
		t.Fatalf("fresh server %+v", srv)
	}
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("a_x"))
	if srv, _ = e.st.GetServer(ctx, id); !srv.HasAgent {
		t.Fatal("token not counted")
	}
	_ = e.st.RevokeAgentTokens(ctx, id)
	if srv, _ = e.st.GetServer(ctx, id); srv.HasAgent {
		t.Fatal("revoked token counted")
	}
}

// The AI agent learns how each server's agent runs.
func TestToolViewAgent(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "pve"})
	view := func() string {
		srv, _ := e.st.GetServer(ctx, id)
		return e.srv.toolView(srv).Agent
	}
	if v := view(); v != "none" {
		t.Errorf("no token: %q", v)
	}
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("a_pve"))
	if v := view(); v != "waiting" {
		t.Errorf("token, no push: %q", v)
	}
	e.ingest(t, "a_pve", protocol.Report{Hostname: "pve", AgentMode: "systemd"})
	if v := view(); v != "systemd" {
		t.Errorf("systemd agent: %q", v)
	}
	e.ingest(t, "a_pve", protocol.Report{Hostname: "pve"})
	if v := view(); v != "docker" {
		t.Errorf("agent without mode: %q", v)
	}
}
