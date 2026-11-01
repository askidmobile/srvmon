package main

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// runHostSh is the host-sh subcommand: a one-off container of the panel
// with the host's / mounted at root runs the host's own /bin/sh there,
// reading the script from stdin. The panel uses it to set up the
// read-only account srvmon-ro (useradd, authorized_keys) on servers where
// its SSH user reaches root only through the docker group, which is
// root-equivalent anyway. The image has no shell or useradd of its own.
func runHostSh(args []string, root string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "usage: host-sh < script")
		return 2
	}
	if err := hostSh(root); err != nil {
		fmt.Fprintln(os.Stderr, "srvmon-agent host-sh:", err)
		return 1
	}
	return 0
}

func hostSh(root string) error {
	st, err := os.Stat(root + "/bin/sh")
	if err != nil || st.IsDir() {
		return errors.New(root + "/bin/sh not found: mount the host's / there")
	}
	if err := syscall.Chroot(root); err != nil {
		return fmt.Errorf("chroot %s: %w", root, err)
	}
	if err := os.Chdir("/"); err != nil {
		return err
	}
	env := []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	return syscall.Exec("/bin/sh", []string{"sh", "-s"}, env)
}
