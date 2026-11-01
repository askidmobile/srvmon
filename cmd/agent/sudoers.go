package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

// sudoersUser is the account name rule of the panel (sshgw.userRe).
var sudoersUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

const maxSudoers = 16 << 10

// runWriteSudoers installs stdin as dir/<user> (root, 0440) or removes that
// file when stdin is empty. The panel checks the content with visudo on
// the host first; sudo skips the temporary file because its name has a dot.
func runWriteSudoers(args []string, in io.Reader, dir string) int {
	if err := writeSudoers(args, in, dir); err != nil {
		fmt.Fprintln(os.Stderr, "srvmon-agent write-sudoers:", err)
		return 1
	}
	return 0
}

func writeSudoers(args []string, in io.Reader, dir string) error {
	if len(args) != 1 || !sudoersUser.MatchString(args[0]) {
		return errors.New("usage: write-sudoers <user>")
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	b, err := io.ReadAll(io.LimitReader(in, maxSudoers+1))
	if err != nil {
		return err
	}
	if len(b) > maxSudoers {
		return errors.New("sudoers content is too large")
	}
	dst := filepath.Join(dir, args[0])
	if len(b) == 0 {
		if err := os.Remove(dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	if b[len(b)-1] != '\n' {
		return errors.New("sudoers content must end with a newline")
	}
	tmp := filepath.Join(dir, "."+args[0]+".srvmon-tmp")
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o440)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if werr == nil {
		werr = f.Sync()
	}
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr == nil {
		werr = os.Chmod(tmp, 0o440)
	}
	if werr == nil {
		werr = os.Rename(tmp, dst)
	}
	if werr != nil {
		_ = os.Remove(tmp)
		return werr
	}
	return nil
}
