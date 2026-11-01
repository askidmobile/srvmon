//go:build !linux

package agent

func nvmeSmartLog(string) ([]byte, error) { return nil, errSmartUnsupported }

func ataSmart(string) ([]byte, *bool, error) { return nil, nil, errSmartUnsupported }
