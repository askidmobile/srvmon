package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteToken(t *testing.T) {
	f := filepath.Join(t.TempDir(), "agent.token")
	if err := os.WriteFile(f, []byte("old_token_old_token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st0, _ := os.Stat(f)
	if rc := runWriteToken([]string{f}, strings.NewReader("a_newtokennewtoken12\n")); rc != 0 {
		t.Fatalf("rc %d", rc)
	}
	b, _ := os.ReadFile(f)
	if string(b) != "a_newtokennewtoken12\n" {
		t.Fatalf("content %q", b)
	}
	if st1, _ := os.Stat(f); !os.SameFile(st0, st1) {
		t.Fatal("the file must be rewritten in place")
	}
	for _, bad := range []string{"", "short", "has space in it here", "x;rm -rf /;xxxxxxxxx"} {
		if rc := runWriteToken([]string{f}, strings.NewReader(bad)); rc == 0 {
			t.Errorf("%q accepted", bad)
		}
	}
	if rc := runWriteToken(nil, strings.NewReader("a_newtokennewtoken12")); rc == 0 {
		t.Error("no file accepted")
	}
}
