package agent

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParsePidStat(t *testing.T) {
	// comm with spaces and a ')' inside.
	in := []byte("1234 (my (weird) proc) S 1 1234 1234 0 -1 4194560 100 0 0 0 250 50 0 0 20 0 1 0 100 1000000 300 18446744073709551615 0 0 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0")
	name, ticks, rss, hidden, ok := parsePidStat(in)
	if !ok || hidden || name != "my (weird) proc" || ticks != 300 || rss != 300 {
		t.Fatalf("got %q %d %d %v %v", name, ticks, rss, hidden, ok)
	}
	// A process the reader may not ptrace: startcode/endcode read as 1.
	masked := []byte("77 (upowerd) S 1 77 77 0 -1 4194560 100 0 0 0 5 5 0 0 20 0 3 0 100 1000000 300 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0")
	if _, _, _, hidden, ok := parsePidStat(masked); !ok || !hidden {
		t.Fatalf("masked stat: hidden=%v ok=%v", hidden, ok)
	}
}

// A non-dumpable host process: its stat is read once, then the collector
// switches to comm/statm/schedstat so the AppArmor denial is not repeated.
func TestDetailCollectorOpaqueProcess(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("stat", "cpu 0 0 0 0 0 0 0 0\n")
	write("77/stat", "77 (upowerd) S 1 77 77 0 -1 4194560 100 0 0 0 5 5 0 0 20 0 2 0 100 1000000 300 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0")
	write("77/comm", "upowerd\n")
	write("77/statm", "5000 300 200 10 0 400 0\n")
	write("77/task/77/schedstat", "1000000000 0 10\n")
	write("77/task/78/schedstat", "500000000 0 10\n")
	d := &DetailCollector{HostProc: dir, HostRoot: dir}
	d.Collect(nil)
	if !d.opaque[77] {
		t.Fatal("pid 77 not marked opaque")
	}
	// From now on stat must not be read: make it unreadable garbage.
	write("77/stat", "garbage")
	d.prevAt = d.prevAt.Add(-100 * time.Second)
	write("77/task/77/schedstat", "2000000000 0 10\n") // +1 s over ~100 s
	det, _ := d.Collect(nil)
	if det.ProcsTotal != 1 {
		t.Fatalf("procs = %d", det.ProcsTotal)
	}
	p := det.Procs[0]
	if p.Name != "upowerd" || p.MemBytes != 300*uint64(os.Getpagesize()) {
		t.Errorf("proc = %+v", p)
	}
	if p.CPUPercent < 0.99 || p.CPUPercent > 1.01 {
		t.Errorf("proc cpu = %v", p.CPUPercent)
	}
	// The pid exits: it leaves the opaque set.
	_ = os.RemoveAll(filepath.Join(dir, "77"))
	d.Collect(nil)
	if len(d.opaque) != 0 {
		t.Errorf("opaque = %v", d.opaque)
	}
}

func TestContainerIDFromCgroup(t *testing.T) {
	id := "4f2b1c0d9e8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c"
	for _, in := range []string{
		"0::/system.slice/docker-" + id + ".scope\n",
		"12:memory:/docker/" + id + "\n",
	} {
		if got := containerIDFromCgroup([]byte(in)); got != id {
			t.Errorf("%q -> %q", in, got)
		}
	}
	if got := containerIDFromCgroup([]byte("0::/user.slice/user-1000.slice/session-1.scope\n")); got != "" {
		t.Errorf("host process mapped to %q", got)
	}
}

func TestParseDiskstats(t *testing.T) {
	in := []byte(`   8       0 sda 100 0 1000 0 50 0 2000 0 0 0 0
   8       1 sda1 100 0 999 0 50 0 1999 0 0 0 0
 259       0 nvme0n1 10 0 100 0 5 0 200 0 0 0 0
 259       1 nvme0n1p1 10 0 99 0 5 0 199 0 0 0 0
   7       0 loop0 10 0 5000 0 0 0 0 0 0 0 0
 253       0 dm-0 1 0 777 0 1 0 777 0 0 0 0
`)
	r, w := parseDiskstats(in)
	if r != 1100 || w != 2200 {
		t.Fatalf("read=%d written=%d", r, w)
	}
}

func TestDetailCollector(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	id := "4f2b1c0d9e8a7b6c5d4e3f2a1b0c9d8e7f6a5b4c3d2e1f0a9b8c7d6e5f4a3b2c"
	write("stat", "cpu 0 0 0 0 0 0 0 0\ncpu0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0\n")
	write("net/sockstat", "sockets: used 100\nTCP: inuse 12 orphan 0 tw 3 alloc 20 mem 1\n")
	write("10/stat", "10 (nginx) S 1 0 0 0 0 0 0 0 0 0 100 0 0 0 0 0 1 0 0 0 10 0")
	write("10/cgroup", "0::/system.slice/docker-"+id+".scope\n")
	write("10/status", "Name:\tnginx\nUid:\t33\t33\t33\t33\n")
	write("10/cmdline", "nginx: worker\x00process\x00")
	write("11/stat", "11 (sshd) S 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 1 0 0 0 5 0")
	write("11/cgroup", "0::/system.slice/ssh.service\n")
	write("etc/passwd", "root:x:0:0::/root:/bin/sh\nwww-data:x:33:33::/var/www:/usr/sbin/nologin\n")
	d := &DetailCollector{HostProc: dir, HostRoot: dir}
	ids := map[string]string{id: "web"}
	d.Collect(ids)
	// Pretend 100 s passed: the real time between the two calls (tens of
	// milliseconds under -race on CI) then barely moves the result.
	d.prevAt = d.prevAt.Add(-100 * time.Second)
	write("stat", "cpu 100 0 100 200 0 0 0 0\ncpu0 100 0 0 0 0 0 0 0\ncpu1 0 0 100 200 0 0 0 0\n")
	write("10/stat", "10 (nginx) S 1 0 0 0 0 0 0 0 0 0 150 0 0 0 0 0 1 0 0 0 10 0")
	det, usage := d.Collect(ids)
	if len(det.Cores) != 2 || det.Cores[0] != 100 || det.Cores[1] < 33 || det.Cores[1] > 34 {
		t.Errorf("cores = %v", det.Cores)
	}
	if det.CPUUser != 25 || det.CPUSystem != 25 {
		t.Errorf("user=%v system=%v", det.CPUUser, det.CPUSystem)
	}
	if det.TCPConns != 12 || det.ProcsTotal != 2 {
		t.Errorf("tcp=%d procs=%d", det.TCPConns, det.ProcsTotal)
	}
	p := det.Procs[0]
	if p.Name != "nginx" || p.Container != "web" || p.User != "www-data" || p.Cmd != "nginx: worker process" {
		t.Errorf("top proc = %+v", p)
	}
	if p.CPUPercent < 0.49 || p.CPUPercent > 0.51 { // 50 ticks over ~100 s
		t.Errorf("proc cpu = %v", p.CPUPercent)
	}
	if u := usage["web"]; u.MemBytes != 10*uint64(os.Getpagesize()) {
		t.Errorf("container usage = %+v", u)
	}
}

func TestParseSyslogLine(t *testing.T) {
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	l, ok := parseSyslogLine("2026-10-31T08:09:58.123456+00:00 host kernel: Memory cgroup out of memory: Killed process 31207 (node)", now)
	if !ok || l.Level != "ERR" || l.Source != "kernel" || l.Time.Hour() != 8 {
		t.Fatalf("rfc3339: %+v %v", l, ok)
	}
	l, ok = parseSyslogLine("Oct 31 06:12:40 host sshd[123]: Invalid user admin from 1.2.3.4 port 51022", now)
	if !ok || l.Level != "WARN" || l.Source != "sshd" || l.Time.Year() != 2026 {
		t.Fatalf("classic: %+v %v", l, ok)
	}
	if _, ok := parseSyslogLine("2026-10-31T07:30:00+00:00 host systemd[1]: logrotate.service: Deactivated successfully.", now); ok {
		t.Fatal("info line kept")
	}
}

func TestDemuxDockerLogs(t *testing.T) {
	frame := func(stream byte, s string) []byte {
		h := []byte{stream, 0, 0, 0, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
		return append(h, s...)
	}
	in := append(frame(1, "2026-10-31T08:10:02.000000001Z ok\n"), frame(2, "2026-10-31T08:10:03Z error: boom\n")...)
	got := demuxDockerLogs(in)
	if len(got) != 2 || got[1] != "2026-10-31T08:10:03Z error: boom" {
		t.Fatalf("mux: %q", got)
	}
	got = demuxDockerLogs([]byte("2026-10-31T08:10:02Z tty line\r\n"))
	if len(got) != 1 || got[0] != "2026-10-31T08:10:02Z tty line" {
		t.Fatalf("tty: %q", got)
	}
}

func TestFormatPorts(t *testing.T) {
	got := formatPorts([]dockerPort{
		{PrivatePort: 80, PublicPort: 8080, Type: "tcp"},
		{PrivatePort: 80, PublicPort: 8080, Type: "tcp"}, // IPv6 duplicate
		{PrivatePort: 6379, Type: "tcp"},
	})
	if len(got) != 2 || got[0] != "6379/tcp" || got[1] != "8080→80/tcp" {
		t.Fatalf("ports = %q", got)
	}
}
