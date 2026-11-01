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

func TestOnboard(t *testing.T) { testOnboard(t, true) }

// Without docker on the target the wizard installs it before the agent.
func TestOnboardInstallsDocker(t *testing.T) { testOnboard(t, false) }

func testOnboard(t *testing.T, hasDocker bool) {
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
		case cmd == "command -v docker":
			if hasDocker {
				return "/usr/bin/docker\n", 0
			}
			return "", 1
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
		Key: key, SSHUser: "srvmon", SudoMode: SudoFull, DisablePassword: true,
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
	want := 4
	if !hasDocker {
		want = 5
	}
	if len(scripts) != want {
		t.Fatalf("want %d scripts (user, [docker,] agent, sshd, sudo), got %d", want, len(scripts))
	}
	// sudo goes last: a narrower mode must not cut the login's own sudo
	// before the agent install and the sshd change.
	if last := scripts[len(scripts)-1]; !strings.Contains(last, "NOPASSWD:ALL") || strings.Contains(scripts[0], "sudoers") {
		t.Fatalf("sudo rules must be the last script:\n%s", last)
	}
	if !hasDocker {
		if !strings.Contains(scripts[1], "get.docker.com") || !strings.Contains(scripts[1], "U='srvmon'") ||
			!strings.Contains(scripts[2], "--name srvmon-agent ") {
			t.Fatalf("docker must be installed right before the agent:\n%s", scripts[1])
		}
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
	if err == nil || !strings.Contains(err.Error(), "password") {
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
		case cmd == "command -v docker":
			return "/usr/bin/docker\n", 0
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
		case cmd == "command -v docker":
			return "/usr/bin/docker\n", 0
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
	s := userScript("srvmon", "ssh-ed25519 AAAA test")
	create := strings.Index(s, `if ! id -u "$U"`)
	end := strings.Index(s[create:], "\nfi\n") + create
	for _, cmd := range []string{"usermod -p", "chpasswd"} {
		i := strings.Index(s, cmd)
		if i < 0 || i < create || i > end {
			t.Fatalf("%s must only run for a newly created account:\n%s", cmd, s)
		}
	}
}

// When sudo does not read the password (NOPASSWD), the password line must
// still never run as a shell command.
func TestSudoPasswordLineNeverExecuted(t *testing.T) {
	cmd, stdin := sudoPasswordCmd("echo PWLEAK; exit 7", "echo ok\n")
	shCmd := strings.TrimPrefix(cmd, "sudo -S -k -p '' ")
	for name, in := range map[string]string{
		"sudo did not read the password": stdin,
		"sudo read the password":         stdin[strings.IndexByte(stdin, '\n')+1:],
	} {
		c := exec.Command("sh", "-c", shCmd)
		c.Stdin = strings.NewReader(in)
		out, err := c.CombinedOutput()
		if err != nil || string(out) != "ok\n" {
			t.Fatalf("%s: %q %v", name, out, err)
		}
	}
}

func TestCappedBuffer(t *testing.T) {
	b := &CappedBuffer{Head: 4, Tail: 4}
	for _, s := range []string{"ab", "cdef", "ghij", "klmnop"} {
		b.Write([]byte(s))
	}
	if got := b.String(); got != "abcd\n[… 8 bytes skipped …]\nmnop" || b.Len() != 16 {
		t.Fatalf("%q %d", got, b.Len())
	}
	small := &CappedBuffer{Head: 4, Tail: 4}
	small.Write([]byte("abcdef"))
	if got := small.String(); got != "abcdef" {
		t.Fatalf("%q", got)
	}
	big := &CappedBuffer{Head: 2, Tail: 3}
	big.Write([]byte("0123456789"))
	if got := big.String(); got != "01\n[… 5 bytes skipped …]\n789" {
		t.Fatalf("%q", got)
	}
}

// Password logins are off: the owner's key (or the panel key added by hand)
// logs in instead. With DropLoginKey the panel key leaves root's
// authorized_keys once srvmon has it.
func TestOnboardWithLoginKey(t *testing.T) {
	ts := newTestServer(t)
	owner, _ := GenerateKey("owner")
	ownerPub, _ := ParseHostKey(owner.Public)
	ownerSigner, _ := ssh.ParsePrivateKey(owner.Private)
	panel, _ := GenerateKey("panel")
	panelPub, _ := ParseHostKey(panel.Public)
	panelSigner, _ := ssh.ParsePrivateKey(panel.Private)
	ts.handler = func(cmd, stdin string) (string, uint32) {
		switch {
		case cmd == "id -u":
			return "0\n", 0
		case cmd == "command -v docker":
			return "/usr/bin/docker\n", 0
		case strings.Contains(stdin, "authorized_keys"):
			ts.mu.Lock()
			ts.authorized = append(ts.authorized, panelPub)
			ts.mu.Unlock()
			return "", 0
		}
		return "", 0
	}
	for _, c := range []struct {
		name   string
		signer ssh.Signer
		drop   bool
	}{{"owner key", ownerSigner, false}, {"panel key", panelSigner, true}} {
		ts.mu.Lock()
		ts.authorized = []ssh.PublicKey{ownerPub}
		if c.drop {
			ts.authorized = []ssh.PublicKey{panelPub}
		}
		ts.execs = nil
		ts.mu.Unlock()
		_, err := Onboard(context.Background(), OnboardParams{
			Addr: ts.addr, User: "root", LoginKey: c.signer, DropLoginKey: c.drop,
			HostKey: ts.hostKey, Key: panel, SSHUser: "srvmon",
		}, func(string, string) {})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		ts.mu.Lock()
		dropped := false
		for _, e := range ts.execs {
			if strings.HasPrefix(e, "root: ") && strings.Contains(e, "grep -vF") {
				dropped = true
			}
		}
		ts.mu.Unlock()
		if dropped != c.drop {
			t.Fatalf("%s: drop ran = %v", c.name, dropped)
		}
	}
}

// A non-root login with a key and no sudo password must stop early when
// sudo wants one, instead of failing halfway.
func TestOnboardKeyNeedsSudoPassword(t *testing.T) {
	ts := newTestServer(t)
	owner, _ := GenerateKey("owner")
	ownerPub, _ := ParseHostKey(owner.Public)
	ownerSigner, _ := ssh.ParsePrivateKey(owner.Private)
	ts.authorized = []ssh.PublicKey{ownerPub}
	ts.handler = func(cmd, _ string) (string, uint32) {
		if cmd == "id -u" {
			return "1000\n", 0
		}
		return "", 1 // sudo -n fails: a password is required
	}
	_, err := Onboard(context.Background(), OnboardParams{
		Addr: ts.addr, User: "admin", LoginKey: ownerSigner, HostKey: ts.hostKey, Key: owner, SSHUser: "srvmon",
	}, func(string, string) {})
	if err == nil || !strings.Contains(err.Error(), "sudo password") {
		t.Fatalf("want a sudo password error, got %v", err)
	}
}

func TestDropKeyScript(t *testing.T) {
	if dropKeyScript("") != "exit 0\n" {
		t.Fatal("an empty key must not match every line")
	}
	k, _ := GenerateKey("c")
	dir := t.TempDir()
	lines := []string{"ssh-ed25519 AAAAother keep", "from=\"1.2.3.4\" " + k.Public + " pasted", "ssh-rsa AAAArsa keep2"}
	if err := writeFile(dir+"/.ssh/authorized_keys", strings.Join(lines, "\n")+"\n"); err != nil {
		t.Fatal(err)
	}
	c := exec.Command("sh", "-c", dropKeyScript(k.Public))
	c.Env = []string{"HOME=" + dir, "PATH=/usr/bin:/bin"}
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	got, _ := readFile(dir + "/.ssh/authorized_keys")
	if got != "ssh-ed25519 AAAAother keep\nssh-rsa AAAArsa keep2\n" {
		t.Fatalf("authorized_keys = %q", got)
	}
}

// The update must start the agent exactly like the first install.
func TestAgentUpdateScriptMatchesInstall(t *testing.T) {
	flags := func(script string) string {
		i := strings.Index(script, "--name srvmon-agent --restart")
		j := strings.Index(script[i:], `"$IMG"`)
		if i < 0 || j < 0 {
			t.Fatalf("no docker run in script")
		}
		return script[i : i+j]
	}
	a := AgentInstall{Image: "ghcr.io/x/srvmon-agent:sha-1", URL: "https://mon"}
	if in, up := flags(agentScript(a)), flags(agentUpdateScript(a)); in != up {
		t.Errorf("install:\n%s\nupdate:\n%s", in, up)
	}
}
