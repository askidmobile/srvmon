package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "m.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		s.Close()
	}
}

func TestCredentialsAndRecovery(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if err := s.CreateSession(ctx, "sess", SessionFull, "1.2.3.4", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.SetCredentials(ctx, "hash", "SECRET", []string{"r1", "r2"}); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting(ctx, KeyPasswordHash); v != "hash" {
		t.Fatalf("password hash = %q", v)
	}
	if _, err := s.SessionKind(ctx, "sess"); err != ErrNotFound {
		t.Fatal("init must drop existing sessions")
	}
	if ok, _ := s.UseRecoveryCode(ctx, "r1"); !ok {
		t.Fatal("recovery code should work once")
	}
	if ok, _ := s.UseRecoveryCode(ctx, "r1"); ok {
		t.Fatal("recovery code reused")
	}
}

func TestSessionsExpire(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	if err := s.CreateSession(ctx, "old", SessionPending, "ip", -time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SessionKind(ctx, "old"); err != ErrNotFound {
		t.Fatal("expired session must not be found")
	}
}

func TestServersAndTokens(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	id, err := s.CreateServer(ctx, Server{Name: "shop-db", Group: "prod", Tags: []string{"db"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddAgentToken(ctx, id, "tok"); err != nil {
		t.Fatal(err)
	}
	gotID, name, err := s.AgentServer(ctx, "tok")
	if err != nil || gotID != id || name != "shop-db" {
		t.Fatalf("AgentServer = %d %q %v", gotID, name, err)
	}
	list, err := s.ListServers(ctx)
	if err != nil || len(list) != 1 || list[0].LastSeen == nil || list[0].Tags[0] != "db" {
		t.Fatalf("ListServers = %+v %v", list, err)
	}
	if _, _, err := s.AgentServer(ctx, "nope"); err != ErrNotFound {
		t.Fatal("unknown token must be rejected")
	}
}

func TestServerIDsAreNotReused(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	a, _ := s.CreateServer(ctx, Server{Name: "a"})
	b, _ := s.CreateServer(ctx, Server{Name: "b"})
	if err := s.DeleteServer(ctx, b); err != nil {
		t.Fatal(err)
	}
	c, err := s.CreateServer(ctx, Server{Name: "c"})
	if err != nil {
		t.Fatal(err)
	}
	if c == b || c <= a {
		t.Fatalf("id reused: a=%d b=%d c=%d", a, b, c)
	}
}

func TestAlertIDsAreNotReused(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now()
	a, err := s.CreateAlert(ctx, Alert{Kind: KindLogin, Severity: SevInfo, Channel: ChanWeb, State: AlertEvent,
		Message: "x", StartedAt: now.Add(-100 * 24 * time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeAlerts(ctx, now.Add(-90*24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	b, _ := s.CreateAlert(ctx, Alert{Kind: KindLogin, Severity: SevInfo, Channel: ChanWeb, State: AlertEvent,
		Message: "y", StartedAt: now})
	if b.ID <= a.ID {
		t.Fatalf("id %d reused after purge (old %d)", b.ID, a.ID)
	}
}

func TestRuleIDsAreNotReused(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	id, _ := s.CreateRule(ctx, Rule{Kind: KindCPU, Threshold: 50, Severity: SevWarn, Channel: ChanWeb})
	_ = s.DeleteRule(ctx, id)
	id2, _ := s.CreateRule(ctx, Rule{Kind: KindMem, Threshold: 50, Severity: SevWarn, Channel: ChanWeb})
	if id2 <= id {
		t.Fatalf("rule id %d reused", id2)
	}
}

func TestServerChecks(t *testing.T) {
	st, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, _ := st.CreateServer(ctx, Server{Name: "a"})
	if err := st.SetServerChecks(ctx, id, []string{"https://x.org/", "x.org:22"}); err != nil {
		t.Fatal(err)
	}
	before, _ := st.ServerChecks(ctx, id)
	if err := st.SetServerChecks(ctx, id, []string{"x.org:22", "tls://x.org:993"}); err != nil {
		t.Fatal(err)
	}
	after, _ := st.ServerChecks(ctx, id)
	if len(after) != 2 || after[0].ID != before[1].ID || after[1].Target != "tls://x.org:993" {
		t.Fatalf("before %+v after %+v", before, after)
	}
	if err := st.SetServerChecks(ctx, 999, nil); err != ErrNotFound {
		t.Fatalf("unknown server: %v", err)
	}
	if err := st.DeleteServer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if all, _ := st.ListChecks(ctx); len(all) != 0 {
		t.Fatalf("checks outlive their server: %+v", all)
	}
}
