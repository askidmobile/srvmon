package sshgw

import (
	"os"
	"path/filepath"
)

func writeFile(p, s string) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	return os.WriteFile(p, []byte(s), 0o600)
}

func readFile(p string) (string, error) {
	b, err := os.ReadFile(p)
	return string(b), err
}
