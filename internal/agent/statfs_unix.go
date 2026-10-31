//go:build linux || darwin || freebsd

package agent

import "syscall"

func statfs(path string) (total, used uint64, err error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bsize := uint64(st.Bsize) //nolint:unconvert // int64 on linux, uint32 on darwin
	total = st.Blocks * bsize
	free := st.Bfree * bsize
	return total, total - free, nil
}
