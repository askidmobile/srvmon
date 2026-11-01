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
	// No SSH key: nothing starts and the image is not marked as tried, so
	// the update still happens once SSH access is added.
	s.updateAgent(id, "", true, nil)
	if _, ok := s.updates.get(id); ok {
		t.Errorf("state set for a server without ssh")
	}
	if s.updates.tried[id] != "" {
		t.Errorf("tried marked without an attempt: %v", s.updates.tried)
	}
	img := s.cfg.AgentImage
	if !s.updates.begin(id, img, time.Now(), nil, true) {
		t.Fatal("auto begin refused")
	}
	if s.updates.tried[id] != img {
		t.Errorf("tried = %v", s.updates.tried)
	}
	if s.updates.begin(id, img, time.Now(), nil, false) {
		t.Errorf("begin must refuse a second running update")
	}
	s.updates.set(id, agentUpdateView{State: "failed"})
	if s.updates.begin(id, img, time.Now(), nil, true) {
		t.Errorf("auto update retried the same image")
	}
	if !s.updates.begin(id, img, time.Now(), nil, false) {
		t.Errorf("manual retry refused")
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
	if s.updates.begin(7, "img", time.Now(), s.jobs.active, false) {
		t.Error("update started during an onboarding")
	}
	if !s.updates.begin(8, "img", time.Now(), s.jobs.active, false) {
		t.Fatal("update of another server refused")
	}
	if _, err := s.addOnboardJob(&job{serverID: 8, cancel: func() {}}); err != errAgentUpdating {
		t.Errorf("onboarding during an update: %v", err)
	}
}
