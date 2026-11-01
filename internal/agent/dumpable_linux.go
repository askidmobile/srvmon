package agent

import "syscall"

// setNonDumpable marks the process non-dumpable. /proc/<pid>/root (and the
// other ptrace-guarded procfs entries) of such a process need
// CAP_SYS_PTRACE, so the agent, which sees host processes through
// /host/proc, cannot reach the docker socket inside the proxy's
// filesystem.
func setNonDumpable() error {
	const prSetDumpable = 4
	if _, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prSetDumpable, 0, 0); e != 0 {
		return e
	}
	return nil
}
