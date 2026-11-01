package sshgw

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestClassifySudo(t *testing.T) {
	head := "Matching Defaults entries for srvmon on host:\n    env_reset, secure_path=/usr/sbin\\:/usr/bin\n\n" +
		"User srvmon may run the following commands on host:\n"
	cases := map[string]string{
		"":                                           SudoNone,
		"sudo: a password is required":               SudoNone,
		head + "    (ALL) NOPASSWD: ALL\n":           SudoFull,
		head + "    (ALL : ALL) NOPASSWD: ALL\n":     SudoFull,
		head + "    (ALL : ALL) ALL\n":               SudoNone,
		head + "    (root) NOPASSWD: /usr/bin/apt\n": SudoCustom,
		// sudo wraps long rules at 80 columns.
		head + "    (root) NOPASSWD: /usr/bin/systemctl status, /usr/bin/systemctl status *,\n" +
			"        /usr/bin/systemctl restart, /usr/bin/systemctl\n        restart *, /usr/bin/journalctl, /usr/bin/docker\n": SudoLimited,
		head + "    (root) NOPASSWD: /usr/bin/systemctl status, /usr/bin/systemctl edit *\n": SudoCustom,
		head + "    (root) NOPASSWD: /usr/bin/journalctl\n    (ALL : ALL) ALL\n":             SudoLimited,
	}
	for out, want := range cases {
		if got := ClassifySudo(out); got != want {
			t.Errorf("ClassifySudo(%q) = %s, want %s", out, got, want)
		}
	}
}

// genSudoers runs sudoersGen for mode and returns the file it writes.
func genSudoers(t *testing.T, mode string) string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "srvmon")
	cmd := exec.Command("sh", "-c", "set -e\nU=srvmon\nT="+shq(f)+"\n"+sudoersGen(mode))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", mode, err, out)
	}
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The generated rules must be valid sudoers, and a limited file must be
// classified as limited once sudo lists it.
func TestSudoersGen(t *testing.T) {
	if s := genSudoers(t, SudoNone); s != "" {
		t.Errorf("none: %q", s)
	}
	full := genSudoers(t, SudoFull)
	if !strings.Contains(full, "srvmon ALL=(ALL) NOPASSWD:ALL\n") {
		t.Errorf("full: %q", full)
	}
	lim := genSudoers(t, SudoLimited)
	if strings.Contains(lim, "NOPASSWD:ALL") || strings.Contains(lim, "edit") {
		t.Errorf("limited grants too much: %q", lim)
	}
	if i := strings.Index(lim, "NOPASSWD: "); i >= 0 {
		// What sudo -l would print for this rule.
		rule := strings.TrimSpace(lim[i:])
		if got := ClassifySudo("User srvmon may run the following commands on h:\n    (root) " + rule + "\n"); got != SudoLimited {
			t.Errorf("limited rules classified as %s: %s", got, rule)
		}
	}
	visudo, err := exec.LookPath("visudo")
	if err != nil {
		t.Skip("no visudo")
	}
	for mode, s := range map[string]string{SudoFull: full, SudoLimited: lim} {
		f := filepath.Join(t.TempDir(), "s")
		if err := os.WriteFile(f, []byte(s), 0o440); err != nil {
			t.Fatal(err)
		}
		if out, err := exec.Command(visudo, "-cf", f).CombinedOutput(); err != nil {
			t.Errorf("visudo %s: %v\n%s\n%s", mode, err, out, s)
		}
	}
}

func TestSudoScriptsParse(t *testing.T) {
	for _, mode := range []string{SudoNone, SudoLimited, SudoFull} {
		scripts := map[string]string{
			"set":  setSudoScript(SudoParams{User: "srvmon", Mode: mode, Image: "ghcr.io/x/a:1"}),
			"user": userScript("srvmon", "ssh-ed25519 AAAA k", mode),
		}
		for name, s := range scripts {
			if out, err := exec.Command("sh", "-n", "-c", s).CombinedOutput(); err != nil {
				t.Errorf("%s %s: %v\n%s", name, mode, err, out)
			}
		}
	}
	if s := userScript("srvmon", "ssh-ed25519 AAAA k", ""); strings.Contains(s, "sudoers") {
		t.Error("an empty mode must leave sudo alone")
	}
}
