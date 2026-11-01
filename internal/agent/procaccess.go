package agent

import (
	"bufio"
	"bytes"
	"errors"
	"os"
	"strconv"
	"strings"
)

// Reading /proc/<pid>/stat of another process makes the kernel run a ptrace
// access check (do_task_stat → ptrace_may_access(PTRACE_MODE_READ)). When
// the agent's uid and gid match the target's (root reading root daemons and
// kernel threads) the check reaches the LSM, and the docker-default
// AppArmor profile, which allows ptrace only between docker-default peers,
// denies it and logs
//
//	apparmor="DENIED" operation="ptrace" class="ptrace" profile="docker-default"
//	comm="srvmon-agent" requested_mask="read" peer="unconfined"
//
// for every such read: one line per host process per detail sample. The
// stat contents are served either way (the address fields are masked), so
// the denial costs nothing but log noise, yet it is noise on every host
// with --pid host. A confined agent therefore reads processes only from
// files the kernel serves without that check: comm, statm and the
// per-thread schedstat.

// procMode is how per-process CPU time is read.
type procMode int

const (
	procModeUnset procMode = iota
	// procModeStat: the agent is not confined by an LSM, so a failed
	// ptrace check is silent and /proc/<pid>/stat is the cheap source.
	procModeStat
	// procModeSched: the agent runs under a MAC profile; stat is read only
	// where the check is known to stop before the LSM.
	procModeSched
)

// procIDs are the agent's own credentials as the ptrace check sees them.
type procIDs struct {
	uid, gid  int
	sysPtrace bool // CAP_SYS_PTRACE in the effective set
}

const capSysPtrace = 19

// readMode looks at the agent's own LSM label: anything but "unconfined"
// (AppArmor) or nothing at all (no LSM with a process label) counts as
// confined.
func (d *DetailCollector) readMode() procMode {
	d.selfIDs = procIDs{uid: os.Geteuid(), gid: os.Getegid()}
	if b, err := os.ReadFile("/proc/self/status"); err == nil {
		if v, ok := statusField(b, "CapEff:"); ok {
			if caps, err := strconv.ParseUint(v, 16, 64); err == nil {
				d.selfIDs.sysPtrace = caps&(1<<capSysPtrace) != 0
			}
		}
	}
	paths := []string{"/proc/self/attr/apparmor/current", "/proc/self/attr/current"}
	if d.SelfAttr != "" {
		paths = []string{d.SelfAttr}
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		label := strings.TrimSpace(strings.TrimRight(string(b), "\x00\n"))
		if label == "" || label == "unconfined" {
			return procModeStat
		}
		return procModeSched
	}
	return procModeStat
}

// readProc reads one process: name, CPU time in ns and resident pages.
// cpuOK is false when its CPU time cannot be read without a ptrace check
// (a confined agent on a kernel without schedstat); the process is then
// listed with an unknown CPU and stat is never tried for it.
func (d *DetailCollector) readProc(pid int, dir string) (name string, cpuNs, rss uint64, cpuOK, ok bool) {
	if d.mode == procModeUnset {
		d.mode = d.readMode()
	}
	if d.mode == procModeSched && !d.noSchedstat {
		op := d.sched[pid]
		if op == nil {
			op = &opaqueProc{}
		}
		n, cpu, r, err := d.readOpaque(dir, op)
		switch {
		case err == nil:
			if d.sched == nil {
				d.sched = map[int]*opaqueProc{}
			}
			d.sched[pid] = op
			return n, cpu, r, true, true
		case errors.Is(err, errNoSchedstat):
			d.noSchedstat = true
			d.sched = nil
		default:
			return "", 0, 0, false, false
		}
	}
	if d.mode == procModeSched && !d.statSafe(dir) {
		n, r, err := d.nameRSS(dir)
		if err != nil {
			return "", 0, 0, false, false
		}
		return n, 0, r, false, true
	}
	b, err := os.ReadFile(d.proc(dir + "/stat"))
	if err != nil {
		return "", 0, 0, false, false
	}
	n, ticks, r, ok := parsePidStat(b)
	return n, ticks * (1e9 / clkTck), r, ok, ok
}

// statSafe reports whether reading the process's stat ends the ptrace check
// before the LSM hook: the kernel denies a reader without CAP_SYS_PTRACE
// whose uid or gid differs from the target's right away, silently.
func (d *DetailCollector) statSafe(dir string) bool {
	if d.selfIDs.sysPtrace {
		return false
	}
	b, err := os.ReadFile(d.proc(dir + "/status"))
	if err != nil {
		return false
	}
	same := func(key string, id int) bool {
		v, ok := statusField(b, key)
		if !ok {
			return true // unknown: assume the worst
		}
		f := strings.Fields(v)
		if len(f) < 3 {
			return true
		}
		for _, s := range f[:3] { // real, effective, saved
			if n, err := strconv.Atoi(s); err != nil || n != id {
				return false
			}
		}
		return true
	}
	return !(same("Uid:", d.selfIDs.uid) && same("Gid:", d.selfIDs.gid))
}

// nameRSS reads comm and the resident pages from statm.
func (d *DetailCollector) nameRSS(dir string) (string, uint64, error) {
	b, err := os.ReadFile(d.proc(dir + "/comm"))
	if err != nil {
		return "", 0, err
	}
	name := strings.TrimSpace(string(b))
	b, err = os.ReadFile(d.proc(dir + "/statm"))
	if err != nil {
		return "", 0, err
	}
	var rss uint64
	if f := strings.Fields(string(b)); len(f) > 1 {
		rss, _ = strconv.ParseUint(f[1], 10, 64)
	}
	return name, rss, nil
}

// statusField returns the value of a "Key:\tvalue" line of a status file.
func statusField(b []byte, key string) (string, bool) {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if v, ok := strings.CutPrefix(sc.Text(), key); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
}
