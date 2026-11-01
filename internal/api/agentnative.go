package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// Agent binaries for the systemd mode (hosts without docker). The panel
// image carries them next to the server binary, built from the same
// commit, so the agent's version always matches the panel's.

var agentArches = map[string]bool{"amd64": true, "arm64": true}

type agentBinCache struct {
	mu sync.Mutex
	m  map[string]cachedBin
	// limiter counts wrong tokens on the binary download, apart from the
	// login limiter so a misconfigured server cannot lock the owner out.
	limiter *auth.Limiter
}

type cachedBin struct {
	mod  time.Time
	size int64
	bin  sshgw.AgentBinary
}

func agentBinName(arch string) string { return "srvmon-agent-linux-" + arch }

// agentBinaries returns the agent binary for arch from SRVMON_AGENT_BIN_DIR.
func (s *Server) agentBinaries(arch string) (sshgw.AgentBinary, error) {
	if !agentArches[arch] {
		return sshgw.AgentBinary{}, i18n.Errorf("no agent build for the %s architecture", arch)
	}
	if s.cfg.AgentBinDir == "" {
		return sshgw.AgentBinary{}, errors.New(i18n.T("the panel has no agent binaries (SRVMON_AGENT_BIN_DIR)"))
	}
	path := filepath.Join(s.cfg.AgentBinDir, agentBinName(arch))
	st, err := os.Stat(path)
	if err != nil {
		return sshgw.AgentBinary{}, i18n.Errorf("the panel has no agent binary %s", path)
	}
	c := &s.bins
	c.mu.Lock()
	defer c.mu.Unlock()
	if b, ok := c.m[arch]; ok && b.mod.Equal(st.ModTime()) && b.size == st.Size() {
		return b.bin, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return sshgw.AgentBinary{}, err
	}
	sum := sha256.Sum256(data)
	bin := sshgw.AgentBinary{Data: data, SHA256: hex.EncodeToString(sum[:])}
	if s.agentVersionKnown() {
		bin.Version = s.version
	}
	if c.m == nil {
		c.m = map[string]cachedBin{}
	}
	c.m[arch] = cachedBin{mod: st.ModTime(), size: st.Size(), bin: bin}
	return bin, nil
}

// handleAgentInstallScript serves the manual install script of the
// systemd agent. It holds no secrets: the token comes from SRVMON_TOKEN.
func (s *Server) handleAgentInstallScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(sshgw.InstallShellScript(s.publicURL(r))))
}

// handleAgentBinary hands the agent binary to a holder of a live agent
// token (the install script).
func (s *Server) handleAgentBinary(w http.ResponseWriter, r *http.Request) {
	arch, ok := strings.CutPrefix(r.PathValue("file"), "srvmon-agent-linux-")
	if !ok || !agentArches[arch] {
		http.NotFound(w, r)
		return
	}
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	ip := s.clientIP(r)
	if blocked, _ := s.bins.limiter.Blocked(ip); blocked {
		writeError(w, http.StatusTooManyRequests, "too_many_attempts")
		return
	}
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if valid, err := s.store.AgentTokenValid(r.Context(), auth.HashToken(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	} else if !valid {
		s.bins.limiter.Fail(ip)
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	bin, err := s.agentBinaries(arch)
	if err != nil {
		writeError(w, http.StatusNotFound, "no_agent_binary")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(bin.Data)))
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(bin.Data)
}

// agentTokenView answers a token reissue. The token is shown once: the
// panel keeps only its hash.
type agentTokenView struct {
	Token string `json:"token"`
	// Systemd and Docker are shell commands that install an agent with
	// this token by hand.
	Systemd string `json:"systemd"`
	Docker  string `json:"docker"`
	// Written: the panel put the token on the server over SSH; the running
	// agent switches to it within a minute.
	Written    bool   `json:"written"`
	WriteError string `json:"write_error,omitempty"`
}

// handleAgentToken issues a new agent token ({"write": true} also puts it
// on the server over SSH). The server's other tokens stay valid until an
// agent first pushes with the new one, so a running agent is not cut off.
func (s *Server) handleAgentToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	var req struct {
		Write bool `json:"write"`
	}
	if r.ContentLength != 0 {
		if err := readJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "bad_request")
			return
		}
	}
	ctx := r.Context()
	srv, err := s.store.GetServer(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	token, err := auth.NewToken("a_")
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if err := s.store.ReissueAgentToken(ctx, id, auth.HashToken(token)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	url := s.publicURL(r)
	view := agentTokenView{
		Token:   token,
		Systemd: fmt.Sprintf("curl -fsSL %s/agent/install.sh | SRVMON_TOKEN=%s sh", url, token),
		Docker: "mkdir -p /etc/srvmon && (umask 077; echo " + sshgw.Quote(token) + " > /etc/srvmon/agent.token)\n" +
			sshgw.ManualDockerScript(s.cfg.AgentImage, url),
	}
	_ = s.store.Audit(ctx, s.clientIP(r), "agent_token_reissue", srv.Name)
	if req.Write {
		if err := s.writeAgentToken(id, token); err != nil {
			view.WriteError = err.Error()
		} else {
			view.Written = true
		}
	}
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) writeAgentToken(id int64, token string) error {
	access, err := s.store.ServerSSH(context.Background(), id)
	if errors.Is(err, store.ErrNotFound) {
		return errors.New(i18n.T("the panel has no SSH access to this server"))
	} else if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	client, err := s.dialServer(ctx, access)
	if err != nil {
		return i18n.Errorf("ssh: %w", err)
	}
	defer client.Close()
	return sshgw.WriteAgentToken(ctx, client, s.cfg.AgentImage, token)
}
