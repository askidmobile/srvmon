package sshgw

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runEmergency(t *testing.T, home string, lines []string) (string, error) {
	t.Helper()
	c := exec.Command("sh", "-s")
	c.Stdin = strings.NewReader(emergencyScript(lines))
	c.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := c.CombinedOutput()
	return string(out), err
}

func TestEmergencyKeysBlock(t *testing.T) {
	panel, err := GenerateKey("srvmon@panel")
	if err != nil {
		t.Fatal(err)
	}
	k1, _ := GenerateKey("owner@laptop")
	k2, _ := GenerateKey("owner@yubikey")
	home := t.TempDir()
	ak := filepath.Join(home, ".ssh", "authorized_keys")
	if err := writeFile(ak, panel.Public+"\n"); err != nil {
		t.Fatal(err)
	}

	if out, err := runEmergency(t, home, []string{k1.Public, k2.Public}); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ := readFile(ak)
	want := panel.Public + "\n" + emergencyBegin + "\n" + k1.Public + "\n" + k2.Public + "\n" + emergencyEnd + "\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// Rewriting replaces the block instead of adding a second one.
	if out, err := runEmergency(t, home, []string{k2.Public}); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ = readFile(ak)
	if strings.Count(got, emergencyBegin) != 1 || strings.Contains(got, k1.Public) || !strings.HasPrefix(got, panel.Public+"\n") {
		t.Fatalf("after replace:\n%s", got)
	}
	// An empty list removes the block, the panel key stays.
	if out, err := runEmergency(t, home, nil); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if got, _ = readFile(ak); got != panel.Public+"\n" {
		t.Fatalf("after removal:\n%s", got)
	}
	// Malformed blocks must not swallow the panel key.
	for _, broken := range []string{
		emergencyBegin + "\n" + panel.Public + "\n",
		emergencyEnd + "\n" + emergencyBegin + "\n" + panel.Public + "\n",
		emergencyBegin + "\n" + emergencyBegin + "\n" + emergencyEnd + "\n" + panel.Public + "\n" + emergencyEnd + "\n",
	} {
		if err := writeFile(ak, broken); err != nil {
			t.Fatal(err)
		}
		for _, keys := range [][]string{{k1.Public}, nil} {
			out, err := runEmergency(t, home, keys)
			if err == nil || !strings.Contains(out, "srvmon:bad-block") {
				t.Fatalf("broken block accepted: %v %s", err, out)
			}
			if got, _ = readFile(ak); got != broken {
				t.Fatalf("file changed:\n%s", got)
			}
		}
	}
}

func TestEmergencyScriptNoFile(t *testing.T) {
	home := t.TempDir()
	k, _ := GenerateKey("owner")
	if out, err := runEmergency(t, home, []string{k.Public}); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	got, _ := readFile(filepath.Join(home, ".ssh", "authorized_keys"))
	if !strings.Contains(got, k.Public) {
		t.Fatalf("got %q", got)
	}
}

func TestParseEmergencyKey(t *testing.T) {
	k, _ := GenerateKey("me@laptop")
	ek, err := ParseEmergencyKey(`from="10.0.0.1",no-pty ` + k.Public + "  ")
	if err != nil {
		t.Fatal(err)
	}
	if ek.Line != k.Public || ek.Comment != "me@laptop" || ek.Type != "ssh-ed25519" || !strings.HasPrefix(ek.Fingerprint, "SHA256:") {
		t.Fatalf("%+v", ek)
	}
	for _, bad := range []string{"", "hello", string(k.Private), k.Public + "\n" + k.Public} {
		if _, err := ParseEmergencyKey(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	// Markers or newlines in the comment cannot break the block.
	ek, err = ParseEmergencyKey(strings.TrimSuffix(k.Public, "me@laptop") + "a # srvmon emergency keys: end")
	if err != nil || strings.Contains(ek.Line, "#") {
		t.Fatalf("%+v %v", ek, err)
	}
}
