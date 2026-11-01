package agent

import (
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCgroupUsageV2(t *testing.T) {
	root := t.TempDir()
	id := "abc123"
	dir := filepath.Join(root, "sys/fs/cgroup/system.slice/docker-"+id+".scope")
	writeFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 1000000\nuser_usec 800000\n")
	writeFile(t, filepath.Join(dir, "memory.current"), "104857600\n")
	writeFile(t, filepath.Join(dir, "memory.stat"), "anon 50\ninactive_file 4857600\nactive_file 1\n")

	c := &CgroupUsage{HostRoot: root}
	t0 := time.Unix(1000, 0)
	u := c.Usage([]string{id, "missing"}, t0)
	if _, ok := u["missing"]; ok {
		t.Fatal("container without a cgroup reported")
	}
	got := u[id]
	if got.MemBytes != 100<<20-4857600 || !got.noCPU {
		t.Fatalf("first sample = %+v", got)
	}
	// 0.5 s of CPU in 5 s: 10% of one core.
	writeFile(t, filepath.Join(dir, "cpu.stat"), "usage_usec 1500000\n")
	got = c.Usage([]string{id}, t0.Add(5*time.Second))[id]
	if got.noCPU || math.Abs(got.CPUPercent-10) > 1e-9 {
		t.Fatalf("second sample = %+v", got)
	}
	// Gone containers are forgotten.
	c.Usage(nil, t0.Add(10*time.Second))
	if len(c.paths) != 0 || len(c.prev) != 0 {
		t.Fatalf("state kept: %v %v", c.paths, c.prev)
	}
}

func TestCgroupUsageV1(t *testing.T) {
	root := t.TempDir()
	id := "def456"
	cg := filepath.Join(root, "sys/fs/cgroup")
	writeFile(t, filepath.Join(cg, "cpu,cpuacct/docker", id, "cpuacct.usage"), "2000000000\n")
	writeFile(t, filepath.Join(cg, "memory/docker", id, "memory.usage_in_bytes"), "3000\n")
	writeFile(t, filepath.Join(cg, "memory/docker", id, "memory.stat"), "total_inactive_file 1000\n")
	c := &CgroupUsage{HostRoot: root}
	got := c.Usage([]string{id}, time.Unix(0, 0))[id]
	if got.MemBytes != 2000 {
		t.Fatalf("v1 = %+v", got)
	}
}
