package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

var tokenRe = regexp.MustCompile(`^[A-Za-z0-9_-]{16,256}$`)

// runWriteToken writes the token from stdin into the file named by args[0].
// The file is rewritten in place: a bind mount of it into the agent
// container keeps pointing at the same inode.
func runWriteToken(args []string, stdin io.Reader) int {
	if len(args) != 1 || args[0] == "" {
		fmt.Fprintln(os.Stderr, "usage: srvmon-agent write-token FILE < token")
		return 2
	}
	line, err := bufio.NewReader(io.LimitReader(stdin, 1024)).ReadString('\n')
	if err != nil && err != io.EOF {
		fmt.Fprintln(os.Stderr, "write-token:", err)
		return 1
	}
	tok := strings.TrimSpace(line)
	if !tokenRe.MatchString(tok) {
		fmt.Fprintln(os.Stderr, "write-token: not a token")
		return 2
	}
	f, err := os.OpenFile(args[0], os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "write-token:", err)
		return 1
	}
	if _, err := f.WriteString(tok + "\n"); err != nil {
		f.Close()
		fmt.Fprintln(os.Stderr, "write-token:", err)
		return 1
	}
	if err := f.Close(); err != nil {
		fmt.Fprintln(os.Stderr, "write-token:", err)
		return 1
	}
	return 0
}
