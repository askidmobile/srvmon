package agent

import (
	"syscall"
	"testing"
)

func TestSetNonDumpable(t *testing.T) {
	if err := setNonDumpable(); err != nil {
		t.Fatal(err)
	}
	const prGetDumpable = 3
	v, _, e := syscall.RawSyscall(syscall.SYS_PRCTL, prGetDumpable, 0, 0)
	if e != 0 || v != 0 {
		t.Fatalf("dumpable = %d, %v", v, e)
	}
}
