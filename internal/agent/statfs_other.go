//go:build !(linux || darwin || freebsd)

package agent

import "errors"

func statfs(string) (uint64, uint64, error) { return 0, 0, errors.New("statfs: unsupported OS") }
