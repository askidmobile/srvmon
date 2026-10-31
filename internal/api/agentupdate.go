package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// Agent updates: the panel replaces the srvmon-agent container over SSH
// with its own agent image (SRVMON_AGENT_IMAGE, pinned to the panel's
// release on deploy). It runs on demand from the UI and, unless
// SRVMON_AGENT_AUTOUPDATE=false, once per panel version for every agent
// that reports a different version.

const (
	agentUpdateTimeout  = 5 * time.Minute
	agentUpdateParallel = 3
	// agentVersionWait is how long a replaced agent has to report the
	// panel's version.
	agentVersionWait = 90 * time.Second
)

// agentUpdateView is the state of the last update of one server's agent.
type agentUpdateView struct {
	State string    `json:"state"` // running | ok | failed
	Image string    `json:"image"`
	Error string    `json:"error,omitempty"`
	At    time.Time `json:"at"`
}

type agentUpdates struct {
	mu    sync.Mutex
	m     map[int64]agentUpdateView
	tried map[int64]string // server -> image the auto-update already tried
	sem   chan struct{}
}

func newAgentUpdates() *agentUpdates {
	return &agentUpdates{m: map[int64]agentUpdateView{}, tried: map[int64]string{}, sem: make(chan struct{}, agentUpdateParallel)}
}

func (u *agentUpdates) get(id int64) (agentUpdateView, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.m[id]
	return v, ok
}

// begin marks the server's update as running; false if one already runs.
func (u *agentUpdates) begin(id int64, image string, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if v, ok := u.m[id]; ok && v.State == "running" {
		return false
	}
	u.m[id] = agentUpdateView{State: "running", Image: image, At: now}
	return true
}

func (u *agentUpdates) set(id int64, v agentUpdateView) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.m[id] = v
}

func (u *agentUpdates) drop(id int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.m, id)
}

// agentVersionKnown reports whether the panel has a release version to
// compare agents with; a dev build never marks agents outdated.
func (s *Server) agentVersionKnown() bool { return s.version != "" && s.version != "dev" }

// agentOutdated reports whether an agent reporting v differs from the panel.
func (s *Server) agentOutdated(v string) bool { return s.agentVersionKnown() && v != s.version }

// decorateAgent fills the agent version fields of a server view.
func (s *Server) decorateAgent(v *serverView) {
	if v.Snapshot != nil && s.agentOutdated(v.Snapshot.Report.AgentVersion) {
		v.AgentOutdated = true
		v.AgentLatest = s.version
	}
	if u, ok := s.updates.get(v.ID); ok {
		v.AgentUpdate = &u
	}
}

// maybeAutoUpdate starts an update when the server's agent reports another
// version than the panel's. It tries each image once per server; the button
// in the UI retries.
func (s *Server) maybeAutoUpdate(id int64, agentVersion string) {
	if !s.cfg.AgentAutoUpdate || !s.agentOutdated(agentVersion) {
		return
	}
	u := s.updates
	u.mu.Lock()
	if u.tried[id] == s.cfg.AgentImage {
		u.mu.Unlock()
		return
	}
	u.tried[id] = s.cfg.AgentImage
	u.mu.Unlock()
	go s.updateAgent(id, "", true)
}

// updateAgent replaces the agent of one server. It returns without a trace
// when the server has no stored SSH key (e.g. the panel's own host, whose
// agent is updated by the deploy).
func (s *Server) updateAgent(id int64, ip string, auto bool) {
	ctx, cancel := context.WithTimeout(context.Background(), agentUpdateTimeout)
	defer cancel()
	access, err := s.store.ServerSSH(ctx, id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("agent update: ssh access", "server", id, "err", err)
		}
		return
	}
	image := s.cfg.AgentImage
	started := s.now()
	if !s.updates.begin(id, image, started) {
		return
	}
	fail := func(err error) {
		s.log.Warn("agent update failed", "server", access.Name, "image", image, "auto", auto, "err", err)
		s.updates.set(id, agentUpdateView{State: "failed", Image: image, Error: err.Error(), At: s.now()})
	}
	select {
	case s.updates.sem <- struct{}{}:
		defer func() { <-s.updates.sem }()
	case <-ctx.Done():
		fail(ctx.Err())
		return
	}
	client, err := s.dialServer(ctx, access)
	if err != nil {
		fail(i18n.Errorf("ssh: %w", err))
		return
	}
	err = sshgw.AgentUpdate(ctx, client, sshgw.AgentInstall{
		Image: image, URL: s.cfg.PublicOrigin,
		RegistryUser: s.cfg.AgentRegistryUser, RegistryToken: s.cfg.AgentRegistryToken,
	})
	client.Close()
	event := "agent_update"
	if auto {
		event = "agent_autoupdate"
	}
	_ = s.store.Audit(context.Background(), ip, event, access.Name+" "+image)
	if err != nil {
		fail(err)
		return
	}
	if s.agentVersionKnown() {
		if v, ok := s.waitAgentVersion(ctx, id, started); !ok {
			if v == "" {
				fail(errors.New(i18n.T("the new agent has not reported yet: check docker logs srvmon-agent on the server")))
			} else {
				fail(errors.New(i18n.T("the new agent reports version %s instead of %s", short(v), short(s.version))))
			}
			return
		}
	}
	s.log.Info("agent updated", "server", access.Name, "image", image, "auto", auto)
	s.updates.set(id, agentUpdateView{State: "ok", Image: image, At: s.now()})
}

// waitAgentVersion waits for a report received after since that carries the
// panel's version; it returns the last version seen after since.
func (s *Server) waitAgentVersion(ctx context.Context, id int64, since time.Time) (string, bool) {
	ctx, cancel := context.WithTimeout(ctx, agentVersionWait)
	defer cancel()
	t := time.NewTicker(time.Second)
	defer t.Stop()
	last := ""
	for {
		if snap, ok := s.latest.Get(id); ok && snap.Received.After(since) {
			last = snap.Report.AgentVersion
			if last == s.version {
				return last, true
			}
		}
		select {
		case <-ctx.Done():
			return last, false
		case <-t.C:
		}
	}
}

func short(v string) string {
	if len(v) > 12 {
		return v[:12]
	}
	return v
}

// handleAgentUpdate starts the agent update of one server.
func (s *Server) handleAgentUpdate(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	if _, err := s.store.ServerSSH(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_ssh_access")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if u, ok := s.updates.get(id); ok && u.State == "running" {
		writeError(w, http.StatusConflict, "update_running")
		return
	}
	go s.updateAgent(id, s.clientIP(r), false)
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// handleAgentUpdateAll updates every outdated agent the panel can reach.
func (s *Server) handleAgentUpdateAll(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	n := 0
	ip := s.clientIP(r)
	for _, srv := range list {
		snap, ok := s.latest.Get(srv.ID)
		if !srv.HasSSH || !ok || !s.agentOutdated(snap.Report.AgentVersion) {
			continue
		}
		if u, ok := s.updates.get(srv.ID); ok && u.State == "running" {
			continue
		}
		go s.updateAgent(srv.ID, ip, false)
		n++
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"started": n})
}
