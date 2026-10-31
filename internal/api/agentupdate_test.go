package api

import (
	"context"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

func TestAgentOutdated(t *testing.T) {
	e := newEnv(t) // panel version "test"
	s := e.srv
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "web"})
	s.latest.Put(id, protocol.Report{AgentVersion: "old"})
	snap, _ := s.latest.Get(id)
	v := serverView{Server: store.Server{ID: id}, Snapshot: &snap}
	s.decorateAgent(&v)
	if !v.AgentOutdated || v.AgentLatest != "test" {
		t.Errorf("old agent: %+v", v)
	}
	s.latest.Put(id, protocol.Report{AgentVersion: "test"})
	snap, _ = s.latest.Get(id)
	v = serverView{Server: store.Server{ID: id}, Snapshot: &snap}
	s.decorateAgent(&v)
	if v.AgentOutdated {
		t.Errorf("current agent marked outdated")
	}
	s.version = "dev"
	if s.agentOutdated("old") {
		t.Errorf("a dev panel marks agents outdated")
	}
}

// The auto-update tries each image once per server, and a server without a
// stored SSH key (the panel's own host) gets no update state at all.
func TestAutoUpdateOncePerImage(t *testing.T) {
	e := newEnv(t)
	s := e.srv
	s.cfg.AgentAutoUpdate = true
	s.cfg.AgentImage = "ghcr.io/x/srvmon-agent:sha-1"
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "self"})
	s.maybeAutoUpdate(id, "old")
	if s.updates.tried[id] != s.cfg.AgentImage {
		t.Fatalf("tried = %v", s.updates.tried)
	}
	// Runs synchronously here: no SSH key, so it leaves no state.
	s.updateAgent(id, "", true)
	if _, ok := s.updates.get(id); ok {
		t.Errorf("state set for a server without ssh")
	}
	s.maybeAutoUpdate(id, "old") // same image: no second try
	s.updates.mu.Lock()
	n := len(s.updates.m)
	s.updates.mu.Unlock()
	if n != 0 {
		t.Errorf("states = %d", n)
	}
	if !s.updates.begin(id, "img", time.Now(), nil) || s.updates.begin(id, "img", time.Now(), nil) {
		t.Errorf("begin must refuse a second running update")
	}
}

func TestAgentUpdateNeedsSSH(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "web"})
	resp := e.post(t, "/api/servers/"+itoa(id)+"/agent/update", nil)
	if resp.StatusCode != 404 {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

// An onboarding and an update of the same server exclude each other.
func TestAgentUpdateExcludesOnboarding(t *testing.T) {
	e := newEnv(t)
	s := e.srv
	if _, err := s.addOnboardJob(&job{serverID: 7, cancel: func() {}}); err != nil {
		t.Fatal(err)
	}
	if s.updates.begin(7, "img", time.Now(), s.jobs.active) {
		t.Error("update started during an onboarding")
	}
	if !s.updates.begin(8, "img", time.Now(), s.jobs.active) {
		t.Fatal("update of another server refused")
	}
	if _, err := s.addOnboardJob(&job{serverID: 8, cancel: func() {}}); err != errAgentUpdating {
		t.Errorf("onboarding during an update: %v", err)
	}
}
