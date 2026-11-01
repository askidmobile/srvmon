package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// The sudo mode of the panel's SSH user on a server: read live with
// sudo -n -l, changed over SSH (sshgw.SetSudo). Nothing is stored in the db,
// so a change made by hand on the server shows up as is.

const sudoTimeout = 2 * time.Minute

func (s *Server) sudoAccess(w http.ResponseWriter, r *http.Request) (store.SSHAccess, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id")
		return store.SSHAccess{}, false
	}
	access, err := s.store.ServerSSH(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_ssh_access")
		return access, false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return access, false
	}
	return access, true
}

// handleSudo reports the current sudo mode of the panel's user.
func (s *Server) handleSudo(w http.ResponseWriter, r *http.Request) {
	access, ok := s.sudoAccess(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	client, err := s.dialServer(ctx, access)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "ssh", "message": err.Error()})
		return
	}
	defer client.Close()
	mode, err := sshgw.DetectSudo(ctx, client)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "ssh", "message": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"mode": mode, "user": access.User})
}

// handleSetSudo changes the sudo mode of the panel's user.
func (s *Server) handleSetSudo(w http.ResponseWriter, r *http.Request) {
	access, ok := s.sudoAccess(w, r)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &req); err != nil || !sshgw.ValidSudoMode(req.Mode) {
		writeError(w, http.StatusBadRequest, "bad_sudo_mode")
		return
	}
	if s.jobs.active(access.ServerID) {
		writeError(w, http.StatusConflict, "onboarding")
		return
	}
	// The change must finish once started: a closed tab must not cut the
	// write of the sudoers file halfway.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), sudoTimeout)
	defer cancel()
	client, err := s.dialServer(ctx, access)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "ssh", "message": err.Error()})
		return
	}
	defer client.Close()
	mode, err := sshgw.SetSudo(ctx, client, sshgw.SudoParams{
		User: access.User, Mode: req.Mode, Image: s.cfg.AgentImage,
		RegistryUser: s.cfg.AgentRegistryUser, RegistryToken: s.cfg.AgentRegistryToken,
	})
	_ = s.store.Audit(context.Background(), s.clientIP(r), "sudo_mode", access.Name+" "+req.Mode)
	if err != nil {
		s.log.Warn("sudo mode", "server", access.Name, "mode", req.Mode, "err", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sudo", "message": err.Error()})
		return
	}
	s.log.Info("sudo mode", "server", access.Name, "mode", mode)
	writeJSON(w, http.StatusOK, map[string]string{"mode": mode, "user": access.User})
}
