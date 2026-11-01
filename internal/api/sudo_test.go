package api

import (
	"context"
	"testing"

	"github.com/askidmobile/srvmon/internal/store"
)

func TestSudoNeedsSSHAndValidMode(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "web"})
	path := "/api/servers/" + itoa(id) + "/sudo"
	if resp := e.get(t, path); resp.StatusCode != 404 {
		t.Errorf("GET without ssh: %d, want 404", resp.StatusCode)
	}
	if resp := e.do(t, "PUT", path, map[string]string{"mode": "limited"}); resp.StatusCode != 404 {
		t.Errorf("PUT without ssh: %d, want 404", resp.StatusCode)
	}
	if err := e.st.SetServerSSH(context.Background(), id, "10.0.0.1", 22, "srvmon", "ssh-ed25519 AAAA", []byte("x")); err != nil {
		t.Skipf("SetServerSSH: %v", err)
	}
	for _, mode := range []string{"", "root", "custom"} {
		if resp := e.do(t, "PUT", path, map[string]string{"mode": mode}); resp.StatusCode != 400 {
			t.Errorf("PUT mode %q: %d, want 400", mode, resp.StatusCode)
		}
	}
}

func TestOnboardRejectsBadSudoMode(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	var out struct{ Error string }
	code := e.postJSON(t, "/api/servers/onboard", map[string]any{
		"card": map[string]any{"name": "web", "host": "10.0.0.1", "port": 22}, "user": "root",
		"password": "pw", "host_key": "x", "sudo_mode": "root",
	}, &out)
	if code != 400 || out.Error != "bad_sudo_mode" {
		t.Errorf("status %d %q, want 400 bad_sudo_mode", code, out.Error)
	}
}
