package agent

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// CgroupUsage reads CPU and memory of docker containers straight from the
// host cgroup filesystem (HostRoot/sys/fs/cgroup), the same counters
// `docker stats` shows. It needs neither the docker API nor extra
// privileges, so it is cheap enough to run on every report.
type CgroupUsage struct {
	HostRoot string

	paths map[string]cgPaths // container id -> where its counters live
	prev  map[string]cgCPU
}

type cgPaths struct {
	v2       string // unified: cpu.stat, memory.current, memory.stat
	cpuacct  string // v1
	memory   string // v1
	notFound bool
}

type cgCPU struct {
	at    time.Time
	usage uint64 // ns
}

// Usage returns CPU (percent of one core) and memory per running container
// id. Containers whose cgroup is not found are left out; the caller can
// fall back to summing processes.
func (c *CgroupUsage) Usage(ids []string, now time.Time) map[string]ContainerUsage {
	if c.paths == nil {
		c.paths = map[string]cgPaths{}
		c.prev = map[string]cgCPU{}
	}
	out := make(map[string]ContainerUsage, len(ids))
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		live[id] = true
		p, ok := c.paths[id]
		if !ok {
			p = c.find(id)
			c.paths[id] = p
		}
		if p.notFound {
			continue
		}
		cpuNs, mem, ok := c.read(p)
		if !ok {
			// The container was recreated or moved: look it up again next time.
			delete(c.paths, id)
			delete(c.prev, id)
			continue
		}
		u := ContainerUsage{MemBytes: mem, noCPU: true}
		if prev, ok := c.prev[id]; ok && cpuNs >= prev.usage {
			if dt := now.Sub(prev.at).Seconds(); dt > 0 {
				u.CPUPercent, u.noCPU = float64(cpuNs-prev.usage)/1e9/dt*100, false
			}
		}
		c.prev[id] = cgCPU{at: now, usage: cpuNs}
		out[id] = u
	}
	for id := range c.paths {
		if !live[id] {
			delete(c.paths, id)
			delete(c.prev, id)
		}
	}
	return out
}

func (c *CgroupUsage) root() string { return filepath.Join(c.HostRoot, "sys/fs/cgroup") }

// find locates the cgroup of a container for the systemd and cgroupfs
// drivers, cgroup v2 and v1.
func (c *CgroupUsage) find(id string) cgPaths {
	root := c.root()
	rel := []string{"system.slice/docker-" + id + ".scope", "docker/" + id}
	for _, r := range rel {
		p := filepath.Join(root, r)
		if fileExists(filepath.Join(p, "cpu.stat")) && fileExists(filepath.Join(p, "memory.current")) {
			return cgPaths{v2: p}
		}
	}
	var out cgPaths
	for _, ctl := range []string{"cpuacct", "cpu,cpuacct"} {
		for _, r := range rel {
			p := filepath.Join(root, ctl, r)
			if fileExists(filepath.Join(p, "cpuacct.usage")) {
				out.cpuacct = p
				break
			}
		}
		if out.cpuacct != "" {
			break
		}
	}
	for _, r := range rel {
		p := filepath.Join(root, "memory", r)
		if fileExists(filepath.Join(p, "memory.usage_in_bytes")) {
			out.memory = p
			break
		}
	}
	if out.cpuacct == "" || out.memory == "" {
		return cgPaths{notFound: true}
	}
	return out
}

// read returns cumulative CPU time in ns and memory in use without the
// inactive page cache, like `docker stats`.
func (c *CgroupUsage) read(p cgPaths) (cpuNs, mem uint64, ok bool) {
	if p.v2 != "" {
		b, err := os.ReadFile(filepath.Join(p.v2, "cpu.stat"))
		if err != nil {
			return 0, 0, false
		}
		usec, found := statValue(b, "usage_usec")
		if !found {
			return 0, 0, false
		}
		cur, err := readUint(filepath.Join(p.v2, "memory.current"))
		if err != nil {
			return 0, 0, false
		}
		ms, _ := os.ReadFile(filepath.Join(p.v2, "memory.stat"))
		inactive, _ := statValue(ms, "inactive_file")
		return usec * 1000, subFloor(cur, inactive), true
	}
	ns, err := readUint(filepath.Join(p.cpuacct, "cpuacct.usage"))
	if err != nil {
		return 0, 0, false
	}
	cur, err := readUint(filepath.Join(p.memory, "memory.usage_in_bytes"))
	if err != nil {
		return 0, 0, false
	}
	ms, _ := os.ReadFile(filepath.Join(p.memory, "memory.stat"))
	inactive, _ := statValue(ms, "total_inactive_file")
	return ns, subFloor(cur, inactive), true
}

func subFloor(a, b uint64) uint64 {
	if b > a {
		return 0
	}
	return a - b
}

// statValue finds "key value" in a cgroup stat file.
func statValue(b []byte, key string) (uint64, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), " ")
		if ok && k == key {
			n, err := strconv.ParseUint(strings.TrimSpace(v), 10, 64)
			return n, err == nil
		}
	}
	return 0, false
}

func readUint(p string) (uint64, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
