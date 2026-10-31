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
