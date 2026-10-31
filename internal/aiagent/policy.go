// Package aiagent holds the srvmon side of the AI agent: which commands the
// agent may run without the owner's confirmation, the task schedule parser
// and the client for the srvmon-ai container.
package aiagent

import (
	"fmt"
	"strings"
)

// Classify reports whether cmd only reads state and can run without
// confirmation. Anything it cannot prove read-only needs the owner's
// approval: the check is an allowlist, so a false "no" costs one click and
// a false "yes" is what it must never produce.
//
// Accepted: a pipeline (single |) of allowlisted programs with plain or
// quoted arguments. Rejected outright: ; & || && redirections, command
// substitution, variables, subshells, env assignments, paths to binaries,
// sudo and anything not on the list.
func Classify(cmd string) (readOnly bool, reason string) {
	segs, err := splitPipeline(cmd)
	if err != nil {
		return false, err.Error()
	}
	for _, words := range segs {
		if len(words) == 0 {
			return false, "пустая команда в конвейере"
		}
		if r := checkProgram(words); r != "" {
			return false, r
		}
	}
	return true, ""
}

// splitPipeline tokenizes a command into pipeline segments of words,
// refusing every shell feature that could run or write something.
func splitPipeline(cmd string) ([][]string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return nil, fmt.Errorf("пустая команда")
	}
	if len(cmd) > 2000 {
		return nil, fmt.Errorf("слишком длинная команда")
	}
	var segs [][]string
	var words []string
	var cur strings.Builder
	inWord := false
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case c == '\'':
			j := strings.IndexByte(cmd[i+1:], '\'')
			if j < 0 {
				return nil, fmt.Errorf("незакрытая кавычка")
			}
			cur.WriteString(cmd[i+1 : i+1+j])
			inWord = true
			i += j + 1
		case c == '"':
			j := strings.IndexByte(cmd[i+1:], '"')
			if j < 0 {
				return nil, fmt.Errorf("незакрытая кавычка")
			}
			body := cmd[i+1 : i+1+j]
			if strings.ContainsAny(body, "$`\\") {
				return nil, fmt.Errorf("подстановка внутри кавычек")
			}
			cur.WriteString(body)
			inWord = true
			i += j + 1
		case c == ' ' || c == '\t':
			flush()
		case c == '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				return nil, fmt.Errorf("оператор ||")
			}
			flush()
			segs = append(segs, words)
			words = nil
		case strings.IndexByte(";&<>`$(){}\\\n\r!#~", c) >= 0:
			return nil, fmt.Errorf("спецсимвол %q", string(c))
		case c < 0x20 || c == 0x7f:
			return nil, fmt.Errorf("управляющий символ")
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	segs = append(segs, words)
	return segs, nil
}

// anyArgs: programs that only read whatever their arguments are.
var anyArgs = set("cat", "head", "tail", "grep", "egrep", "fgrep", "zgrep", "zcat", "ls", "df", "du", "free",
	"uptime", "uname", "hostname", "whoami", "id", "ps", "pgrep", "wc", "cut", "tr", "date", "stat", "file",
	"which", "lscpu", "lsblk", "lsmem", "nproc", "vmstat", "iostat", "mpstat", "w", "who", "last", "ss",
	"netstat", "jq", "md5sum", "sha256sum", "getent", "lsof", "ping", "dig", "nslookup", "host", "findmnt",
	"lsb_release", "basename", "dirname", "realpath", "readlink", "column", "nl", "rev", "true", "echo",
	"printf", "tac", "diff", "cmp", "sha1sum", "base64", "blkid", "lspci", "lsusb", "sensors", "arch")

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

func hasArg(words []string, bad ...string) string {
	for _, w := range words[1:] {
		for _, b := range bad {
			if w == b || (strings.HasPrefix(b, "--") && strings.HasPrefix(w, b+"=")) {
				return w
			}
			// Short flags may be combined (-cn): look for the letter.
			if len(b) == 2 && b[0] == '-' && b[1] != '-' && len(w) > 1 && w[0] == '-' && w[1] != '-' &&
				strings.IndexByte(w[1:], b[1]) >= 0 {
				return w
			}
		}
	}
	return ""
}

func positional(words []string) []string {
	var out []string
	for _, w := range words[1:] {
		if !strings.HasPrefix(w, "-") {
			out = append(out, w)
		}
	}
	return out
}

func checkProgram(words []string) string {
	prog := words[0]
	if strings.ContainsAny(prog, "/=") {
		return "команда задана путём или с переменной окружения"
	}
	if anyArgs[prog] {
		return ""
	}
	switch prog {
	case "sort":
		if a := hasArg(words, "-o", "--output"); a != "" {
			return "sort пишет в файл"
		}
		return ""
	case "uniq":
		if len(positional(words)) > 1 {
			return "uniq с файлом вывода"
		}
		return ""
	case "dmesg":
		if a := hasArg(words, "-c", "-C", "-D", "-E", "-n", "--clear", "--read-clear", "--console-off",
			"--console-on", "--console-level"); a != "" {
			return "dmesg " + a + " меняет состояние"
		}
		return ""
	case "find":
		for _, w := range words[1:] {
			switch w {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return "find " + w
			}
		}
		return ""
	case "journalctl":
		if a := hasArg(words, "--vacuum-size", "--vacuum-time", "--vacuum-files", "--rotate", "--flush", "--sync",
			"--relinquish-var", "--smart-relinquish-var", "--setup-keys", "--update-catalog"); a != "" {
			return "journalctl " + a
		}
		return ""
	case "systemctl":
		return checkSub(words, "systemctl", set("status", "show", "cat", "is-active", "is-enabled", "is-failed",
			"list-units", "list-unit-files", "list-timers", "list-sockets", "list-dependencies", "list-jobs"), nil, true)
	case "top":
		if hasArg(words, "-b") == "" {
			return "top без -b"
		}
		return ""
	case "hostnamectl", "timedatectl", "loginctl", "networkctl", "resolvectl":
		return checkSub(words, prog, set("status", "show", "list", "list-sessions", "list-users", "statistics",
			"show-session", "show-user", "list-timezones", "timesync-status"), nil, true)
	case "ip":
		return checkIP(words)
	case "docker":
		return checkDocker(words)
	}
	return prog + " нет в списке команд только для чтения"
}

// checkSub accepts `prog [flags] sub ...` when sub is in allowed. Flags in
// valued take a value. With emptyOK a bare `prog` is accepted.
func checkSub(words []string, prog string, allowed map[string]bool, valued map[string]bool, emptyOK bool) string {
	for i := 1; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			if w == "--version" || w == "--help" {
				return ""
			}
			if valued[w] {
				i++
			}
			continue
		}
		if allowed[w] {
			return ""
		}
		return prog + " " + w
	}
	if emptyOK {
		return ""
	}
	return prog + " без подкоманды"
}

var ipWrite = set("add", "del", "delete", "change", "replace", "set", "flush", "exec", "append", "prepend",
	"save", "restore", "update", "attach", "detach", "up", "down", "load", "unload")

func checkIP(words []string) string {
	for _, w := range words[1:] {
		if ipWrite[w] {
			return "ip " + w
		}
		if w == "-b" || w == "-batch" || w == "-force" || strings.HasPrefix(w, "-n") || w == "-netns" {
			return "ip " + w
		}
	}
	return ""
}

var dockerRead = set("ps", "images", "inspect", "logs", "top", "version", "info", "events", "port", "diff",
	"history", "stats")

var dockerGroups = map[string]map[string]bool{
	"container": set("ls", "list", "ps", "inspect", "logs", "top", "port", "diff", "stats"),
	"image":     set("ls", "list", "inspect", "history"),
	"network":   set("ls", "list", "inspect"),
	"volume":    set("ls", "list", "inspect"),
	"system":    set("df", "info", "events"),
	"compose":   set("ps", "logs", "config", "ls", "images", "top", "version", "events", "port"),
}

// docker compose flags that take a value (before the subcommand).
var composeValued = set("-f", "--file", "-p", "--project-name", "--project-directory", "--env-file", "--profile",
	"--ansi", "--progress", "--parallel")

func checkDocker(words []string) string {
	if len(words) < 2 {
		return ""
	}
	sub := words[1]
	if strings.HasPrefix(sub, "-") {
		if sub == "--version" || sub == "-v" {
			return ""
		}
		return "глобальные флаги docker"
	}
	if dockerRead[sub] {
		if sub == "stats" && hasArg(words, "--no-stream") == "" {
			return "docker stats без --no-stream"
		}
		return ""
	}
	group, ok := dockerGroups[sub]
	if !ok {
		return "docker " + sub
	}
	var valued map[string]bool
	if sub == "compose" {
		valued = composeValued
	}
	for i := 2; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			if valued[w] {
				i++
			}
			continue
		}
		if !group[w] {
			return "docker " + sub + " " + w
		}
		if w == "stats" && hasArg(words, "--no-stream") == "" {
			return "docker stats без --no-stream"
		}
		return ""
	}
	return "docker " + sub + " без подкоманды"
}
