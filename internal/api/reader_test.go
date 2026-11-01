package api

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/store"
)

func TestReadOnlyRunsAsReader(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	var res execResult
	a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "docker ps -a"}, &res)
	if res.Status != "ok" || res.OutputNote == "" {
		t.Fatalf("%+v", res)
	}
	done := make(chan execResult, 1)
	go func() {
		var res execResult
		a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "docker restart crm"}, &res)
		done <- res
	}()
	act := a.waitPending(t)
	if code := a.postJSON(t, fmt.Sprintf("/api/agent/actions/%d/approve", act.ID), nil, nil); code != 200 {
		t.Fatal(code)
	}
	<-done
	if u := a.sshd.users(); len(u) != 2 || u[0] != "srvmon-ro" || u[1] != "srvmon" {
		t.Fatalf("reads must run as srvmon-ro, confirmed commands as srvmon: %q", u)
	}
}

func TestReaderSetUpOnFirstUse(t *testing.T) {
	var ready atomic.Bool
	a := newAgentEnvWith(t, "http://127.0.0.1:1", func(user, cmd, stdin string) (string, string, uint32) {
		if user == "srvmon" && cmd == "sh -s" && strings.Contains(stdin, "host-sh") {
			ready.Store(true)
			return "reader-ok\n", "", 0
		}
		return "out of " + cmd + "\n", "", 0
	})
	a.sshd.mu.Lock()
	a.sshd.deny = func(u string) bool { return u == "srvmon-ro" && !ready.Load() }
	a.sshd.mu.Unlock()
	var res execResult
	a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "uptime"}, &res)
	if res.Status != "ok" || !ready.Load() {
		t.Fatalf("%+v", res)
	}
	if u := a.sshd.users(); u[len(u)-1] != "srvmon-ro" {
		t.Fatalf("%q", u)
	}
	// The setup script authorizes the panel key without a pty.
	for _, e := range a.sshd.execs {
		if strings.Contains(e.stdin, "host-sh") && !strings.Contains(e.stdin, "no-pty") {
			t.Fatal(e.stdin)
		}
	}
}

func TestReaderSetupFailsClosed(t *testing.T) {
	a := newAgentEnvWith(t, "http://127.0.0.1:1", func(user, cmd, stdin string) (string, string, uint32) {
		if cmd == "sh -s" && strings.Contains(stdin, "host-sh") {
			return "reader-ok\n", "", 0 // set up, but sshd still refuses it (AllowUsers)
		}
		return "out of " + cmd + "\n", "", 0
	})
	a.sshd.mu.Lock()
	a.sshd.deny = func(u string) bool { return u == "srvmon-ro" }
	a.sshd.mu.Unlock()
	var res execResult
	a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "uptime"}, &res)
	if res.Status != "error" || !strings.Contains(res.Note, "read-only account") {
		t.Fatalf("%+v", res)
	}
	for _, c := range a.sshd.commands() {
		if strings.Contains(c, "uptime") {
			t.Fatal("ran as srvmon after the reader failed")
		}
	}
}

func TestReaderNoRootRunsAsSSHUser(t *testing.T) {
	a := newAgentEnvWith(t, "http://127.0.0.1:1", func(user, cmd, stdin string) (string, string, uint32) {
		if cmd == "sh -s" && strings.Contains(stdin, "host-sh") {
			return "", "srvmon:no-access\n", 6 // srvmon itself has no docker and no sudo
		}
		return "out of " + cmd + "\n", "", 0
	})
	a.sshd.mu.Lock()
	a.sshd.deny = func(u string) bool { return u == "srvmon-ro" }
	a.sshd.mu.Unlock()
	var res execResult
	a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": "uptime"}, &res)
	if res.Status != "ok" {
		t.Fatalf("%+v", res)
	}
	if u := a.sshd.users(); u[len(u)-1] != "srvmon" {
		t.Fatalf("%q", u)
	}
}

// TestSeededSecretsDoNotReachTheModel is the regression for a normal
// incident run: fake secrets sit in an .env file (directly and behind a
// symlink), in a container's environment and in a journal line. Each read
// either waits for the owner or comes back masked, while systemctl status
// and the metrics tools keep working.
func TestSeededSecretsDoNotReachTheModel(t *testing.T) {
	const tok = "FAKE-ghp_0123456789abcdefghijklmnopqrstuvwxyz"
	a := newAgentEnvWith(t, "http://127.0.0.1:1", func(user, cmd, stdin string) (string, string, uint32) {
		switch {
		case isResolve(stdin):
			var paths []string
			for _, l := range strings.Split(strings.TrimSpace(stdin), "\n") {
				switch {
				case strings.Contains(l, "'/srv/app/settings.conf'"):
					paths = append(paths, "/srv/app/.env") // a symlink to the .env
				case strings.Contains(l, "'/srv/app/odd.conf'"):
					paths = append(paths, "/srv/app/odd\n/.env") // a symlink into a directory named "odd\n"
				default:
					paths = append(paths, "/home/srvmon-ro/x")
				}
			}
			return resolved(paths...), "", 0
		case strings.Contains(cmd, "docker inspect app"):
			return `[{"Name":"/app","Config":{"Env":["API_TOKEN=` + tok + `","DB_PASSWORD=hunter2 with spaces"]}}]` + "\n", "", 0
		case strings.Contains(cmd, "journalctl"):
			return "Nov 01 10:00:00 app[1]: auth ok token=" + tok + "\nNov 01 10:00:01 app[1]: Authorization: Bearer " + tok + "\n", "", 0
		case strings.Contains(cmd, "systemctl status nginx"):
			return "● nginx.service - A high performance web server\n     Active: active (running) since Sun 2026-11-01\n", "", 0
		case strings.Contains(cmd, "cat"):
			return "API_TOKEN=" + tok + "\n", "", 0
		}
		return "out of " + cmd + "\n", "", 0
	})

	exec := func(cmd string) execResult {
		t.Helper()
		var res execResult
		a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": cmd}, &res)
		return res
	}
	for _, cmd := range []string{"docker inspect app", "journalctl -u app -n 20 --no-pager"} {
		res := exec(cmd)
		if res.Status != "ok" || res.OutputNote == "" {
			t.Fatalf("%s: %+v", cmd, res)
		}
		if strings.Contains(res.Output, tok) || strings.Contains(res.Output, "hunter2") || strings.Contains(res.Output, "with spaces") {
			t.Fatalf("%s: a seeded secret reached the model:\n%s", cmd, res.Output)
		}
	}
	if res := exec("systemctl status nginx"); res.Status != "ok" || !strings.Contains(res.Output, "Active: active (running)") {
		t.Fatalf("systemctl status: %+v", res)
	}
	var view map[string]any
	if code := a.tool(t, "GET", "/tools/servers/prod-api-01", nil, &view); code != 200 {
		t.Fatalf("metrics: %d", code)
	}

	// The .env itself and a symlink to it wait for the owner and do not run.
	for _, cmd := range []string{"cat /srv/app/.env", "cat /srv/app/settings.conf", "date -f/srv/app/settings.conf",
		"cat -- /srv/app/odd.conf", "cat /srv/app/odd.conf"} {
		done := make(chan execResult, 1)
		go func() { done <- exec(cmd) }()
		act := a.waitPending(t)
		if !strings.Contains(act.Reason, ".env") && !strings.Contains(act.Reason, "secrets") {
			t.Fatalf("%s: reason %q", cmd, act.Reason)
		}
		if code := a.postJSON(t, fmt.Sprintf("/api/agent/actions/%d/reject", act.ID), nil, nil); code != 200 {
			t.Fatal(code)
		}
		if res := <-done; res.Status != "rejected" || strings.Contains(res.Output, tok) {
			t.Fatalf("%s: %+v", cmd, res)
		}
	}
	for _, c := range a.sshd.commands() {
		if strings.Contains(c, "cat ") || strings.Contains(c, "date -f") {
			t.Fatalf("a secret file was read without confirmation: %q", c)
		}
	}
	if acts, _ := a.st.ListAgentActions(context.Background(), store.ActionPending, 10); len(acts) != 0 {
		t.Fatalf("%+v", acts)
	}
}

// A setup cut short by the caller (cancelled request) is not cached as a
// failure: the next read sets the reader up again.
func TestReaderSetupCancelledIsNotCached(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := newAgentEnvWith(t, "http://127.0.0.1:1", func(user, cmd, stdin string) (string, string, uint32) {
		if cmd == "sh -s" && strings.Contains(stdin, "host-sh") {
			cancel()
			time.Sleep(50 * time.Millisecond)
			return "", "", 1
		}
		return "out of " + cmd + "\n", "", 0
	})
	a.sshd.mu.Lock()
	a.sshd.deny = func(u string) bool { return u == "srvmon-ro" }
	a.sshd.mu.Unlock()
	acc, err := a.st.ServerSSH(context.Background(), a.srvID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.srv.readerClient(ctx, acc); err == nil {
		t.Fatal("setup succeeded after cancel")
	}
	if st := a.srv.readerStateOf(a.srvID); st.failed != nil || st.self {
		t.Fatalf("cached %+v", st)
	}
}

// Concurrent first reads set the reader up once: the others wait and then
// sign in as the account the first one created.
func TestReaderSetupIsSerialized(t *testing.T) {
	var setups atomic.Int32
	var a *agentEnv
	a = newAgentEnvWith(t, "http://127.0.0.1:1", func(user, cmd, stdin string) (string, string, uint32) {
		if cmd == "sh -s" && strings.Contains(stdin, "host-sh") {
			if setups.Add(1) > 1 {
				return "", "useradd: user 'srvmon-ro' already exists\n", 9
			}
			time.Sleep(200 * time.Millisecond)
			a.sshd.mu.Lock()
			a.sshd.deny = nil
			a.sshd.mu.Unlock()
			return "reader-ok\n", "", 0
		}
		return "out of " + cmd + "\n", "", 0
	})
	a.sshd.mu.Lock()
	a.sshd.deny = func(u string) bool { return u == "srvmon-ro" }
	a.sshd.mu.Unlock()
	acc, err := a.st.ServerSSH(context.Background(), a.srvID)
	if err != nil {
		t.Fatal(err)
	}
	errs := make(chan error, 4)
	for range 4 {
		go func() {
			c, asReader, err := a.srv.readerClient(context.Background(), acc)
			if err == nil {
				c.Close()
				if !asReader {
					err = fmt.Errorf("not as reader")
				}
			}
			errs <- err
		}()
	}
	for range 4 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	if n := setups.Load(); n != 1 {
		t.Fatalf("set up %d times", n)
	}
}
