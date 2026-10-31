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
	"golang.org/x/crypto/ssh"
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
	// autoUpdateRecheck spaces the auto-update checks of a server that could
	// not be updated yet (no SSH key or an onboarding in progress).
	autoUpdateRecheck = time.Minute
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
	// checked limits how often an auto-update looks up a server that turned
	// out not to be updatable (no SSH key, onboarding in progress).
	checked map[int64]time.Time
	sem     chan struct{}
}

func newAgentUpdates() *agentUpdates {
	return &agentUpdates{
		m: map[int64]agentUpdateView{}, tried: map[int64]string{}, checked: map[int64]time.Time{},
		sem: make(chan struct{}, agentUpdateParallel),
	}
}

func (u *agentUpdates) get(id int64) (agentUpdateView, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	v, ok := u.m[id]
	return v, ok
}

// begin marks the server's update as running; false if one already runs
// or busy (an onboarding of the server) says so. u.mu also guards
// addOnboardJob, so an update and an onboarding never replace the same
// container at once. An automatic update also refuses an image it already
// tried and records the attempt only here, once it actually starts.
func (u *agentUpdates) begin(id int64, image string, now time.Time, busy func(int64) bool, auto bool) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if v, ok := u.m[id]; ok && v.State == "running" {
		return false
	}
	if auto && u.tried[id] == image {
		return false
	}
	if busy != nil && busy(id) {
		if auto {
			u.checked[id] = now
		}
		return false
	}
	if auto {
		u.tried[id] = image
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
	skip := u.tried[id] == s.cfg.AgentImage || s.now().Sub(u.checked[id]) < autoUpdateRecheck
	if !skip {
		u.checked[id] = s.now()
	}
	u.mu.Unlock()
	if !skip {
		go s.updateAgent(id, "", true)
	}
}

// updateAgent replaces the agent of one server. It returns without a trace
// when the server has no stored SSH key (e.g. the panel's own host, whose
// agent is updated by the deploy).
func (s *Server) updateAgent(id int64, ip string, auto bool) {
	access, err := s.store.ServerSSH(context.Background(), id)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("agent update: ssh access", "server", id, "err", err)
		}
		return
	}
	image := s.cfg.AgentImage
	started := s.now()
	if !s.updates.begin(id, image, started, s.jobs.active, auto) {
		return
	}
	fail := func(err error) {
		s.log.Warn("agent update failed", "server", access.Name, "image", image, "auto", auto, "err", err)
		s.updates.set(id, agentUpdateView{State: "failed", Image: image, Error: err.Error(), At: s.now()})
	}
	// Queue for a worker slot first: the timeout covers the update itself,
	// not the wait behind other servers.
	s.updates.sem <- struct{}{}
	defer func() { <-s.updates.sem }()
	ctx, cancel := context.WithTimeout(context.Background(), agentUpdateTimeout)
	defer cancel()
	client, err := s.dialServer(ctx, access)
	if err != nil {
		fail(i18n.Errorf("ssh: %w", err))
		return
	}
	defer client.Close()
	err = sshgw.AgentUpdate(ctx, client, sshgw.AgentInstall{
		Image: image, URL: s.cfg.PublicOrigin,
		RegistryUser: s.cfg.AgentRegistryUser, RegistryToken: s.cfg.AgentRegistryToken,
	})
	event := "agent_update"
	if auto {
		event = "agent_autoupdate"
	}
	_ = s.store.Audit(context.Background(), ip, event, access.Name+" "+image)
	if errors.Is(err, sshgw.ErrSwapInterrupted) {
		// The connection or the deadline broke the swap halfway: put the
		// previous agent back if it was already stopped.
		if rerr := s.agentFinish(access, sshgw.AgentRollback); rerr != nil && !errors.Is(rerr, sshgw.ErrNoPreviousAgent) {
			s.log.Warn("agent update: restoring after an interrupted swap", "server", access.Name, "err", rerr)
		}
	}
	if err != nil {
		fail(err)
		return
	}
	// The previous container is kept until the new agent reports the
	// panel's version; otherwise it is put back.
	if s.agentVersionKnown() {
		if v, ok := s.waitAgentVersion(ctx, id, started); !ok {
			var why error
			if v == "" {
				why = errors.New(i18n.T("the new agent has not reported yet: check docker logs srvmon-agent on the server"))
			} else {
				why = errors.New(i18n.T("the new agent reports version %s instead of %s", short(v), short(s.version)))
			}
			if rerr := s.agentFinish(access, sshgw.AgentRollback); rerr != nil && !errors.Is(rerr, sshgw.ErrNoPreviousAgent) {
				fail(i18n.Errorf("%s; restoring the previous agent failed: %s", why.Error(), rerr.Error()))
			} else {
				fail(i18n.Errorf("%s; the previous agent is restored", why.Error()))
			}
			return
		}
	}
	if err := s.agentFinish(access, sshgw.AgentCommit); err != nil {
		s.log.Warn("agent update: removing the previous container", "server", access.Name, "err", err)
	}
	s.log.Info("agent updated", "server", access.Name, "image", image, "auto", auto)
	s.updates.set(id, agentUpdateView{State: "ok", Image: image, At: s.now()})
}

// agentFinish runs the commit or rollback step over a new connection with
// its own deadline: a slow update must not leave no time to restore the
// previous agent.
func (s *Server) agentFinish(access store.SSHAccess, step func(context.Context, *ssh.Client) error) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	client, err := s.dialServer(ctx, access)
	if err != nil {
		return err
	}
	defer client.Close()
	return step(ctx, client)
}

var errAgentUpdating = errors.New("the agent of this server is being updated")

// addOnboardJob registers an onboarding unless the server's agent is being
// updated.
func (s *Server) addOnboardJob(j *job) (string, error) {
	u := s.updates
	u.mu.Lock()
	defer u.mu.Unlock()
	if v, ok := u.m[j.serverID]; ok && v.State == "running" {
		return "", errAgentUpdating
	}
	return s.jobs.add(j)
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
	if s.jobs.active(id) {
		writeError(w, http.StatusConflict, "onboarding")
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
