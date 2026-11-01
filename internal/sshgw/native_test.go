package sshgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// On a Proxmox VE node without docker, auto mode installs the systemd
// agent and never touches docker.
func TestOnboardSystemdOnProxmox(t *testing.T) {
	ts := newTestServer(t)
	key, err := GenerateKey("srvmon@test")
	if err != nil {
		t.Fatal(err)
	}
	bin := []byte("#!/bin/sh\necho v1\n")
	var scripts, staged []string
	ts.handler = func(cmd, stdin string) (string, uint32) {
		switch {
		case cmd == "id -u":
			return "0\n", 0
		case cmd == "uname -m":
			return "x86_64\n", 0
		case strings.Contains(cmd, "mktemp"):
			staged = append(staged, stdin)
			return "/tmp/srvmon-agent.abc", 0
		case cmd == "sh -s":
			scripts = append(scripts, stdin)
			switch {
			case stdin == detectModeScript:
				return "systemd\n", 0
			case strings.Contains(stdin, "authorized_keys"):
				pub, _ := ParseHostKey(key.Public)
				ts.mu.Lock()
				ts.authorized = append(ts.authorized, pub)
				ts.mu.Unlock()
				return "", 0
			case strings.Contains(stdin, "srvmon-agent.service"):
				return "srvmon:native-ok\n", 0
			}
			return "", 0
		}
		return "", 127
	}
	var log []string
	arches := []string{}
	res, err := Onboard(context.Background(), OnboardParams{
		Addr: ts.addr, User: "root", Password: "S3cr3t!pw", HostKey: ts.hostKey,
		Key: key, SSHUser: "srvmon",
		Agent: &AgentInstall{URL: "https://mon", Token: "a_tok", Mode: ModeAuto,
			Binaries: func(arch string) (AgentBinary, error) {
				arches = append(arches, arch)
				return AgentBinary{Data: bin, SHA256: "abc", Version: "v1"}, nil
			}},
	}, func(state, text string) { log = append(log, state+" "+text) })
	if err != nil {
		t.Fatalf("onboard: %v\n%s", err, strings.Join(log, "\n"))
	}
	if !res.AgentInstalled || res.AgentMode != ModeSystemd {
		t.Fatalf("result %+v\n%s", res, strings.Join(log, "\n"))
	}
	if len(arches) != 1 || arches[0] != "amd64" || len(staged) != 1 || staged[0] != string(bin) {
		t.Fatalf("binary: arches %v staged %q", arches, staged)
	}
	all := strings.Join(scripts, "\n")
	if strings.Contains(all, "get.docker.com") || strings.Contains(all, "docker run") {
		t.Fatal("systemd mode must not install or run docker")
	}
	inst := scripts[len(scripts)-1]
	for _, want := range []string{"U='srvmon'", "TOKEN='a_tok'", "SRC='/tmp/srvmon-agent.abc'", "SHA='abc'", "WANT='v1'",
		"ProtectSystem=strict", "NoNewPrivileges=yes", "ExecStart=" + NativeBin} {
		if !strings.Contains(inst, want) {
			t.Errorf("install script misses %q", want)
		}
	}
	for i, s := range scripts {
		shParse(t, i, s)
	}
}

func shParse(t *testing.T, i int, s string) {
	t.Helper()
	cmd := exec.Command("sh", "-n")
	cmd.Stdin = strings.NewReader(s)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("script %d: %v %s\n%s", i, err, out, s)
	}
}

func TestNativeScriptsParse(t *testing.T) {
	for i, s := range []string{
		NativeInstallScript(NativeParams{Owner: "srvmon", URL: "https://m", Token: "t", Src: "/tmp/x"}),
		nativeUpdateScript("", "", "keep"),
		nativeBranch(nativeCommit) + dockerAccess,
		nativeBranch(nativeRollback) + dockerAccess + agentRestoreScript,
		agentScript(AgentInstall{Image: "i", URL: "u", Token: "t"}),
		InstallShellScript("https://mon"),
	} {
		shParse(t, i, s)
	}
}

// localize points a script at dir instead of the real agent paths.
func localize(s, dir string) string {
	s = strings.ReplaceAll(s, NativeEnv, filepath.Join(dir, "agent.env"))
	return strings.ReplaceAll(s, NativeDir, filepath.Join(dir, "lib"))
}

func runLocal(t *testing.T, script string) (string, error) {
	t.Helper()
	out, err := exec.Command("sh", "-c", script).CombinedOutput()
	return string(out), err
}

// The update swap, commit and rollback on a real file system.
func TestNativeUpdateSwap(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, version string) []byte {
		b := []byte("#!/bin/sh\necho " + version + "\n")
		if err := os.WriteFile(filepath.Join(lib, name), b, 0o755); err != nil {
			t.Fatal(err)
		}
		return b
	}
	run := func(name string) string {
		out, err := exec.Command(filepath.Join(lib, name), "version").Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	env := filepath.Join(dir, "agent.env")
	if err := os.WriteFile(env, []byte("SRVMON_URL=https://m\nSRVMON_SMART=0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	write("srvmon-agent", "v1")

	// A binary with the wrong checksum is refused and removed.
	write("srvmon-agent.new", "v2")
	if out, err := runLocal(t, localize(nativeUpdateScript(strings.Repeat("0", 64), "v2", "keep"), dir)); err == nil || !strings.Contains(out, "srvmon:bad-binary checksum") {
		t.Fatalf("bad checksum: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(lib, "srvmon-agent.new")); !os.IsNotExist(err) {
		t.Fatal("refused binary left behind")
	}
	// A binary reporting another version is refused.
	write("srvmon-agent.new", "v3")
	if out, err := runLocal(t, localize(nativeUpdateScript("", "v2", "keep"), dir)); err == nil || !strings.Contains(out, "version v3") {
		t.Fatalf("wrong version: %v %s", err, out)
	}
	if run("srvmon-agent") != "v1" {
		t.Fatal("refused update replaced the binary")
	}

	b := write("srvmon-agent.new", "v2")
	sum := sha256.Sum256(b)
	if out, err := runLocal(t, localize(nativeUpdateScript(hex.EncodeToString(sum[:]), "v2", "1"), dir)); err != nil {
		t.Fatalf("update: %v %s", err, out)
	}
	if run("srvmon-agent") != "v2" || run("srvmon-agent.old") != "v1" {
		t.Fatal("swap did not happen")
	}
	if e, _ := os.ReadFile(env); string(e) != "SRVMON_URL=https://m\nSRVMON_SMART=1\n" {
		t.Fatalf("env %q", e)
	}
	// Rollback puts v1 back; a second rollback has nothing to restore.
	if out, err := runLocal(t, localize(nativeRollback, dir)); err != nil {
		t.Fatalf("rollback: %v %s", err, out)
	}
	if run("srvmon-agent") != "v1" {
		t.Fatal("rollback did not restore v1")
	}
	if e, _ := os.ReadFile(env); string(e) != "SRVMON_URL=https://m\nSRVMON_SMART=0\n" {
		t.Fatalf("rollback kept the new env %q", e)
	}
	if _, err := os.Stat(filepath.Join(lib, "agent.env.old")); !os.IsNotExist(err) {
		t.Fatal("rollback left the env backup")
	}
	if out, err := runLocal(t, localize(nativeRollback, dir)); err == nil || !strings.Contains(out, "srvmon:no-old") {
		t.Fatalf("second rollback: %v %s", err, out)
	}
	// Commit removes the kept binary.
	write("srvmon-agent.old", "v0")
	write("agent.env.old", "x")
	if out, err := runLocal(t, localize(nativeCommit, dir)); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(lib, "srvmon-agent.old")); !os.IsNotExist(err) {
		t.Fatal("commit kept the old binary")
	}
	if _, err := os.Stat(filepath.Join(lib, "agent.env.old")); !os.IsNotExist(err) {
		t.Fatal("commit kept the env backup")
	}
}

func TestArchOf(t *testing.T) {
	for in, want := range map[string]string{"x86_64\n": "amd64", "aarch64": "arm64", "riscv64": ""} {
		if got, _ := archOf(in); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}
