package agent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFileWatch(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "srvmon-agent")
	env := filepath.Join(dir, "agent.env")
	missing := filepath.Join(dir, "later")
	if err := os.WriteFile(bin, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(env, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := newFileWatch([]string{bin, env, ""})
	if f := w.changed(); f != "" {
		t.Fatalf("changed at start: %s", f)
	}
	// Rename a new file over the binary, as the panel's update does.
	tmp := bin + ".new"
	if err := os.WriteFile(tmp, []byte("v2"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, bin); err != nil {
		t.Fatal(err)
	}
	if f := w.changed(); f != bin {
		t.Fatalf("rename over: got %q", f)
	}

	w = newFileWatch([]string{env})
	later := time.Now().Add(time.Minute)
	if err := os.WriteFile(env, []byte("A=2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(env, later, later)
	if f := w.changed(); f != env {
		t.Fatalf("in place: got %q", f)
	}

	w = newFileWatch([]string{missing})
	if f := w.changed(); f != "" {
		t.Fatalf("still missing: %q", f)
	}
	if err := os.WriteFile(missing, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if f := w.changed(); f != missing {
		t.Fatalf("appeared: got %q", f)
	}
	var nilW *fileWatch
	if nilW.changed() != "" {
		t.Fatal("nil watch")
	}
}
