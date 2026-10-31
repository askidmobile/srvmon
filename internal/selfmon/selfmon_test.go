package selfmon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/store"
)

func TestEnsure(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	file := filepath.Join(dir, "self.token")

	created, err := Ensure(ctx, st, "monitor", file)
	if err != nil || !created {
		t.Fatalf("first: created=%v err=%v", created, err)
	}
	b, _ := os.ReadFile(file)
	tok := strings.TrimSpace(string(b))
	if _, name, err := st.AgentServer(ctx, auth.HashToken(tok)); err != nil || name != "monitor" {
		t.Fatalf("token does not resolve: %q %v", name, err)
	}

	// Restart: valid token is kept.
	if created, err := Ensure(ctx, st, "monitor", file); err != nil || created {
		t.Fatalf("second: created=%v err=%v", created, err)
	}
	if b2, _ := os.ReadFile(file); string(b2) != string(b) {
		t.Fatal("token changed on restart")
	}

	// Unknown token (e.g. restored database): replaced, same server reused.
	if err := os.WriteFile(file, []byte("a_bogus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if created, err := Ensure(ctx, st, "monitor", file); err != nil || !created {
		t.Fatalf("third: created=%v err=%v", created, err)
	}
	srvs, _ := st.ListServers(ctx)
	if len(srvs) != 1 {
		t.Fatalf("servers = %d, want 1", len(srvs))
	}
}
