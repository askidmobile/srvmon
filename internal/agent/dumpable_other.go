//go:build !linux

package agent

func setNonDumpable() error { return nil }
