package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteSudoers(t *testing.T) {
	dir := t.TempDir()
	rule := "srvmon ALL=(root) NOPASSWD: /usr/bin/journalctl\n"
	if err := writeSudoers([]string{"srvmon"}, strings.NewReader(rule), dir); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(dir, "srvmon")
	st, err := os.Stat(f)
	if err != nil || st.Mode().Perm() != 0o440 {
		t.Fatalf("stat %v %v", st, err)
	}
	if b, _ := os.ReadFile(f); string(b) != rule {
		t.Fatalf("content %q", b)
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Fatalf("leftover files: %v", ents)
	}
	// Empty input removes the file; removing twice is fine.
	for i := 0; i < 2; i++ {
		if err := writeSudoers([]string{"srvmon"}, strings.NewReader(""), dir); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(f); !os.IsNotExist(err) {
		t.Fatalf("not removed: %v", err)
	}
	for _, bad := range [][]string{nil, {"../etc"}, {"Root"}, {"a", "b"}} {
		if err := writeSudoers(bad, strings.NewReader(rule), dir); err == nil {
			t.Errorf("args %q accepted", bad)
		}
	}
	if err := writeSudoers([]string{"srvmon"}, strings.NewReader("no newline"), dir); err == nil {
		t.Error("content without a final newline accepted")
	}
	if err := writeSudoers([]string{"srvmon"}, strings.NewReader(rule), filepath.Join(dir, "missing")); err == nil {
		t.Error("missing dir accepted")
	}
}
