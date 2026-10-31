package sshgw

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"
	"net"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/ssh"
)

// testServer is a minimal in-process sshd: password "S3cr3t!pw" for user "root",
// any key in authorized, and "exec" requests answered by handler.
type testServer struct {
	addr       string
	hostKey    ssh.PublicKey
	mu         sync.Mutex
	authorized []ssh.PublicKey
	execs      []string
	handler    func(cmd, stdin string) (out string, status uint32)
}

func newTestServer(t *testing.T) *testServer {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	ts := &testServer{hostKey: signer.PublicKey()}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == "root" && string(pw) == "S3cr3t!pw" {
				return nil, nil
			}
			return nil, fmt.Errorf("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
			ts.mu.Lock()
			defer ts.mu.Unlock()
			for _, a := range ts.authorized {
				if string(a.Marshal()) == string(k.Marshal()) {
					return &ssh.Permissions{Extensions: map[string]string{"user": c.User()}}, nil
				}
			}
			return nil, fmt.Errorf("unknown key")
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	ts.addr = ln.Addr().String()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go ts.serve(c, cfg)
		}
	}()
	return ts
}

func (ts *testServer) serve(c net.Conn, cfg *ssh.ServerConfig) {
	sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		c.Close()
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer ch.Close()
			for req := range creqs {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var p struct{ Cmd string }
				_ = ssh.Unmarshal(req.Payload, &p)
				req.Reply(true, nil)
				ts.mu.Lock()
				ts.execs = append(ts.execs, sc.User()+": "+p.Cmd)
				ts.mu.Unlock()
				var stdin string
				if strings.HasSuffix(p.Cmd, "sh -s") {
					b, _ := io.ReadAll(ch)
					stdin = string(b)
				}
				out, status := "", uint32(0)
				if p.Cmd == "id -un" {
					out = sc.User() + "\n"
				} else {
					out, status = ts.handler(p.Cmd, stdin)
				}
				io.WriteString(ch, out)
				ch.SendRequest("exit-status", false, ssh.Marshal(struct{ S uint32 }{status}))
				return
			}
		}()
	}
}

func TestProbeAndPinnedDial(t *testing.T) {
	ts := newTestServer(t)
	ctx := context.Background()
	got, err := ProbeHostKey(ctx, ts.addr)
	if err != nil {
		t.Fatal(err)
	}
	if ssh.FingerprintSHA256(got) != ssh.FingerprintSHA256(ts.hostKey) {
		t.Fatal("probe returned another key")
	}
	parsed, err := ParseHostKey(FormatHostKey(got))
	if err != nil || string(parsed.Marshal()) != string(got.Marshal()) {
		t.Fatalf("host key roundtrip: %v", err)
	}
	c, err := Dial(ctx, ts.addr, "root", got, ssh.Password("S3cr3t!pw"))
	if err != nil {
		t.Fatal(err)
	}
	c.Close()

	other, _ := GenerateKey("x")
	otherPub, _ := ParseHostKey(other.Public)
	if _, err := Dial(ctx, ts.addr, "root", otherPub, ssh.Password("S3cr3t!pw")); err == nil {
		t.Fatal("dial must fail on host key mismatch")
	}
}

func TestOnboard(t *testing.T) {
	ts := newTestServer(t)
	key, err := GenerateKey("srvmon@test")
	if err != nil {
		t.Fatal(err)
	}
	var scripts []string
	ts.handler = func(cmd, stdin string) (string, uint32) {
		switch {
		case cmd == "id -u":
			return "0\n", 0
		case cmd == "sh -s":
			scripts = append(scripts, stdin)
			if strings.Contains(stdin, "authorized_keys") {
				// "install" the key so the key login check passes.
				pub, _ := ParseHostKey(key.Public)
				ts.mu.Lock()
				ts.authorized = append(ts.authorized, pub)
				ts.mu.Unlock()
				return "docker-group\nsudo-ok\n", 0
			}
			return "", 0
		}
		return "", 127
	}
	var log []string
	res, err := Onboard(context.Background(), OnboardParams{
		Addr: ts.addr, User: "root", Password: "S3cr3t!pw", HostKey: ts.hostKey,
		Key: key, SSHUser: "srvmon", Sudo: true, DisablePassword: true,
		Agent: &AgentInstall{Image: "ghcr.io/x/agent:latest", URL: "https://mon", Token: "a_tok"},
	}, func(state, text string) { log = append(log, state+" "+text) })
	if err != nil {
		t.Fatalf("onboard: %v\n%s", err, strings.Join(log, "\n"))
	}
	if !res.AgentInstalled || !res.PasswordDisabled {
		t.Fatalf("result %+v", res)
	}
	all := strings.Join(log, "\n")
	if strings.Contains(all, "S3cr3t") {
		t.Fatal("password leaked into log")
	}
	if len(scripts) != 3 {
		t.Fatalf("want user, agent, sshd scripts, got %d", len(scripts))
	}
	for _, s := range scripts {
		if strings.Contains(s, "S3cr3t") {
			t.Fatal("root path must not send the password to sh")
		}
	}
	// Generated scripts must at least parse.
	for i, s := range scripts {
		cmd := exec.Command("sh", "-n")
		cmd.Stdin = strings.NewReader(s)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("script %d: %v %s", i, err, out)
		}
	}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if !strings.Contains(strings.Join(ts.execs, "\n"), "srvmon: id -un") {
		t.Fatalf("key login not verified: %v", ts.execs)
	}
}

func TestOnboardWrongPassword(t *testing.T) {
	ts := newTestServer(t)
	key, _ := GenerateKey("")
	_, err := Onboard(context.Background(), OnboardParams{
		Addr: ts.addr, User: "root", Password: "nope", HostKey: ts.hostKey, Key: key, SSHUser: "srvmon",
	}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "пароль") {
		t.Fatalf("want auth error, got %v", err)
	}
}

func TestShq(t *testing.T) {
	out, err := exec.Command("sh", "-c", "printf %s "+shq(`a'b"c $x`)).Output()
	if err != nil || string(out) != `a'b"c $x` {
		t.Fatalf("shq: %q %v", out, err)
	}
}

// After the commit point cancelling must not interrupt the optional steps:
// sshd may be switched to key-only logins, and the key is already saved.
func TestOnboardCancelAfterCommitFinishesSteps(t *testing.T) {
	ts := newTestServer(t)
	key, _ := GenerateKey("")
	ctx, cancel := context.WithCancel(context.Background())
	var hardened bool
	ts.handler = func(cmd, stdin string) (string, uint32) {
		switch {
		case cmd == "id -u":
			return "0\n", 0
		case strings.Contains(stdin, "authorized_keys"):
			pub, _ := ParseHostKey(key.Public)
			ts.mu.Lock()
			ts.authorized = append(ts.authorized, pub)
			ts.mu.Unlock()
			return "", 0
		case strings.Contains(stdin, "PasswordAuthentication"):
			ts.mu.Lock()
			hardened = true
			ts.mu.Unlock()
			return "", 0
		default:
			cancel() // the owner presses "cancel" while the agent installs
			return "", 1
		}
	}
	committed := false
	res, err := Onboard(ctx, OnboardParams{
		Addr: ts.addr, User: "root", Password: "S3cr3t!pw", HostKey: ts.hostKey, Key: key, SSHUser: "srvmon",
		Agent: &AgentInstall{Image: "img", URL: "u", Token: "t"}, DisablePassword: true,
		Commit: func(ctx context.Context) error { committed = true; return ctx.Err() },
	}, func(string, string) {})
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err != nil || !committed || !hardened || !res.PasswordDisabled {
		t.Fatalf("err=%v committed=%v hardened=%v res=%+v", err, committed, hardened, res)
	}
}

// Cancelling before the commit point aborts before any optional step.
func TestOnboardCancelBeforeCommit(t *testing.T) {
	ts := newTestServer(t)
	key, _ := GenerateKey("")
	ctx, cancel := context.WithCancel(context.Background())
	var optional bool
	ts.handler = func(cmd, stdin string) (string, uint32) {
		switch {
		case cmd == "id -u":
			return "0\n", 0
		case strings.Contains(stdin, "authorized_keys"):
			pub, _ := ParseHostKey(key.Public)
			ts.mu.Lock()
			ts.authorized = append(ts.authorized, pub)
			ts.mu.Unlock()
			cancel() // the owner cancels while the account is being set up
			return "", 0
		default:
			ts.mu.Lock()
			optional = true
			ts.mu.Unlock()
			return "", 0
		}
	}
	_, err := Onboard(ctx, OnboardParams{
		Addr: ts.addr, User: "root", Password: "S3cr3t!pw", HostKey: ts.hostKey, Key: key, SSHUser: "srvmon",
		Agent: &AgentInstall{Image: "img", URL: "u", Token: "t"}, DisablePassword: true,
		Commit: func(ctx context.Context) error { return ctx.Err() },
	}, func(string, string) {})
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if err == nil || optional {
		t.Fatalf("err=%v optional steps ran=%v", err, optional)
	}
}

// An existing account's password hash must survive onboarding: only an
// account created by the script gets the "*" (key-only) password.
func TestUserScriptKeepsExistingPassword(t *testing.T) {
	s := userScript("srvmon", "ssh-ed25519 AAAA test", false)
	create := strings.Index(s, `if ! id -u "$U"`)
	end := strings.Index(s[create:], "\nfi\n") + create
	for _, cmd := range []string{"usermod -p", "chpasswd"} {
		i := strings.Index(s, cmd)
		if i < 0 || i < create || i > end {
			t.Fatalf("%s must only run for a newly created account:\n%s", cmd, s)
		}
	}
}
