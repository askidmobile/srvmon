package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// Emergency access: the owner's own public keys, kept by the panel in the
// authorized_keys of its SSH account on every server (see
// sshgw.SetEmergencyKeys), so losing the panel does not lock the servers.

const (
	settingEmergencyKeys  = "access.emergency_keys"  // JSON []string, normalized lines
	settingEmergencyState = "access.emergency_state" // JSON map[server id]emergencyResult
	emergencyTimeout      = time.Minute
)

type emergencyResult struct {
	Hash  string    `json:"hash"` // key list applied (or attempted)
	At    time.Time `json:"at"`
	Error string    `json:"error,omitempty"`
}

// emergencyState serializes runs and their results.
type emergencyState struct {
	mu      sync.Mutex
	running map[int64]bool
	again   map[int64]bool // the keys changed during a run: run once more
	sem     chan struct{}
}

func newEmergencyState() *emergencyState {
	return &emergencyState{running: map[int64]bool{}, again: map[int64]bool{}, sem: make(chan struct{}, 4)}
}

func (s *Server) accessRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /api/access/emergency", s.handleEmergency)
	h("PUT /api/access/emergency", s.handleEmergencySave)
	h("POST /api/access/emergency/apply", s.handleEmergencyApply)
}

func (s *Server) emergencyKeys(ctx context.Context) ([]string, error) {
	var keys []string
	v, err := s.store.GetSetting(ctx, settingEmergencyKeys)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return keys, json.Unmarshal([]byte(v), &keys)
}

func (s *Server) emergencyResults(ctx context.Context) map[int64]emergencyResult {
	m := map[int64]emergencyResult{}
	if v, err := s.store.GetSetting(ctx, settingEmergencyState); err == nil {
		_ = json.Unmarshal([]byte(v), &m)
	}
	return m
}

func (s *Server) setEmergencyResult(ctx context.Context, id int64, r emergencyResult) {
	s.emergency.mu.Lock()
	defer s.emergency.mu.Unlock()
	m := s.emergencyResults(ctx)
	m[id] = r
	b, _ := json.Marshal(m)
	if err := s.store.SetSetting(ctx, settingEmergencyState, string(b)); err != nil {
		s.log.Error("emergency keys: save state", "err", err)
	}
}

type emergencyServer struct {
	ID    int64      `json:"id"`
	Name  string     `json:"name"`
	State string     `json:"state"` // ok | pending | running | failed | no_ssh
	At    *time.Time `json:"at,omitempty"`
	Error string     `json:"error,omitempty"`
}

type emergencyView struct {
	Keys    []sshgw.EmergencyKey `json:"keys"`
	Servers []emergencyServer    `json:"servers"`
}

func (s *Server) emergencyView(ctx context.Context) (emergencyView, error) {
	v := emergencyView{Keys: []sshgw.EmergencyKey{}, Servers: []emergencyServer{}}
	lines, err := s.emergencyKeys(ctx)
	if err != nil {
		return v, err
	}
	for _, l := range lines {
		if k, err := sshgw.ParseEmergencyKey(l); err == nil {
			v.Keys = append(v.Keys, k)
		}
	}
	hash := sshgw.EmergencyHash(lines)
	results := s.emergencyResults(ctx)
	servers, err := s.store.ListServers(ctx)
	if err != nil {
		return v, err
	}
	s.emergency.mu.Lock()
	defer s.emergency.mu.Unlock()
	for _, srv := range servers {
		es := emergencyServer{ID: srv.ID, Name: srv.Name, State: "pending"}
		r, seen := results[srv.ID]
		if seen {
			at := r.At
			es.At = &at
		}
		switch {
		case !srv.HasSSH:
			es.State = "no_ssh"
		case s.emergency.running[srv.ID]:
			es.State = "running"
		case seen && r.Hash == hash && r.Error == "":
			es.State = "ok"
		case seen && r.Hash == hash:
			es.State, es.Error = "failed", r.Error
		case len(lines) == 0 && !seen:
			// Nothing to apply and never applied: nothing to remove either.
			es.State = "ok"
		}
		v.Servers = append(v.Servers, es)
	}
	return v, nil
}

func (s *Server) handleEmergency(w http.ResponseWriter, r *http.Request) {
	v, err := s.emergencyView(r.Context())
	if err != nil {
		s.log.Error("emergency keys", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleEmergencySave(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Keys []string `json:"keys"`
	}
	if err := readJSONLimit(r, &req, 64<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if len(req.Keys) > sshgw.MaxEmergencyKeys {
		writeError(w, http.StatusBadRequest, "too_many_keys")
		return
	}
	lines := []string{}
	seen := map[string]bool{}
	for i, l := range req.Keys {
		k, err := sshgw.ParseEmergencyKey(l)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_key", "index": i, "message": err.Error()})
			return
		}
		if !seen[k.Fingerprint] {
			seen[k.Fingerprint] = true
			lines = append(lines, k.Line)
		}
	}
	b, _ := json.Marshal(lines)
	if err := s.store.SetSetting(r.Context(), settingEmergencyKeys, string(b)); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "emergency_keys", sshgw.EmergencyHash(lines))
	s.applyEmergencyAll()
	s.handleEmergency(w, r)
}

func (s *Server) handleEmergencyApply(w http.ResponseWriter, r *http.Request) {
	s.applyEmergencyAll()
	s.handleEmergency(w, r)
}

// applyEmergencyAll brings every server with SSH access to the current key
// list in the background.
func (s *Server) applyEmergencyAll() {
	servers, err := s.store.ListServers(context.Background())
	if err != nil {
		s.log.Error("emergency keys: servers", "err", err)
		return
	}
	for _, srv := range servers {
		if srv.HasSSH {
			go func() { _ = s.applyEmergency(srv.ID) }()
		}
	}
}

// applyEmergency writes the current key list on one server. A call while
// the server is being updated makes that run repeat once it ends, so the
// latest list always lands.
func (s *Server) applyEmergency(id int64) error {
	e := s.emergency
	e.mu.Lock()
	if e.running[id] {
		e.again[id] = true
		e.mu.Unlock()
		return nil
	}
	e.running[id] = true
	e.mu.Unlock()
	for {
		err := s.applyEmergencyOnce(id)
		e.mu.Lock()
		if !e.again[id] {
			delete(e.running, id)
			e.mu.Unlock()
			return err
		}
		delete(e.again, id)
		e.mu.Unlock()
	}
}

func (s *Server) applyEmergencyOnce(id int64) error {
	s.emergency.sem <- struct{}{}
	defer func() { <-s.emergency.sem }()

	ctx, cancel := context.WithTimeout(context.Background(), emergencyTimeout)
	defer cancel()
	lines, err := s.emergencyKeys(ctx)
	if err != nil {
		return err
	}
	res := emergencyResult{Hash: sshgw.EmergencyHash(lines), At: s.now().UTC()}
	err = func() error {
		access, err := s.store.ServerSSH(ctx, id)
		if err != nil {
			return err
		}
		c, err := s.dialServer(ctx, access)
		if err != nil {
			return i18n.Errorf("ssh: %w", err)
		}
		defer c.Close()
		return sshgw.SetEmergencyKeys(ctx, c, lines)
	}()
	if errors.Is(err, store.ErrNotFound) {
		return err // deleted, or no SSH key any more
	}
	if err != nil {
		res.Error = err.Error()
		s.log.Warn("emergency keys", "server", id, "err", err)
	}
	s.setEmergencyResult(context.Background(), id, res)
	return err
}
