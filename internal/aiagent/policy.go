// Package aiagent holds the srvmon side of the AI agent: which commands the
// agent may run without the owner's confirmation, the task schedule parser
// and the client for the srvmon-ai container.
package aiagent

import (
	"errors"
	"strings"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// Classify reports whether cmd only reads state and can run without
// confirmation. Anything it cannot prove read-only needs the owner's
// approval: the check is an allowlist, so a false "no" costs one click and
// a false "yes" is what it must never produce.
//
// Accepted: a pipeline (single |) of allowlisted programs with plain or
// quoted arguments. Rejected outright: ; & || && redirections, command
// substitution, variables, subshells, unquoted globs, env assignments, paths
// to binaries, sudo and anything not on the list.
func Classify(cmd string) (readOnly bool, reason string) {
	segs, err := splitPipeline(cmd)
	if err != nil {
		return false, err.Error()
	}
	for _, words := range segs {
		if len(words) == 0 {
			return false, i18n.T("empty command in a pipeline")
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
		return nil, errors.New(i18n.T("empty command"))
	}
	if len(cmd) > 2000 {
		return nil, errors.New(i18n.T("command too long"))
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
				return nil, errors.New(i18n.T("unclosed quote"))
			}
			cur.WriteString(cmd[i+1 : i+1+j])
			inWord = true
			i += j + 1
		case c == '"':
			j := strings.IndexByte(cmd[i+1:], '"')
			if j < 0 {
				return nil, errors.New(i18n.T("unclosed quote"))
			}
			body := cmd[i+1 : i+1+j]
			if strings.ContainsAny(body, "$`\\") {
				return nil, errors.New(i18n.T("substitution inside quotes"))
			}
			cur.WriteString(body)
			inWord = true
			i += j + 1
		case c == ' ' || c == '\t':
			flush()
		case c == '|':
			if i+1 < len(cmd) && cmd[i+1] == '|' {
				return nil, errors.New(i18n.T("operator ||"))
			}
			flush()
			segs = append(segs, words)
			words = nil
		case strings.IndexByte(";&<>`$(){}\\\n\r!#~", c) >= 0:
			return nil, i18n.Errorf("special character %q", string(c))
		case c < 0x20 || c == 0x7f:
			return nil, errors.New(i18n.T("control character"))
		case c == '*' || c == '?' || c == '[':
			// The remote shell would expand it into names nobody checked
			// (a file named -exec turns into a find action).
			return nil, errors.New(i18n.T("unquoted file name pattern"))
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
	"uptime", "uname", "whoami", "id", "ps", "pgrep", "wc", "cut", "tr", "stat",
	"which", "lscpu", "lsblk", "lsmem", "nproc", "vmstat", "iostat", "mpstat", "w", "who", "last",
	"netstat", "jq", "md5sum", "sha256sum", "getent", "lsof", "ping", "dig", "nslookup", "host", "findmnt",
	"lsb_release", "basename", "dirname", "realpath", "readlink", "column", "nl", "rev", "true", "echo",
	"printf", "tac", "diff", "cmp", "sha1sum", "base64", "blkid", "lspci", "lsusb", "arch")

func set(xs ...string) map[string]bool {
	m := make(map[string]bool, len(xs))
	for _, x := range xs {
		m[x] = true
	}
	return m
}

// hasArg returns the first word that is one of the options in bad. A long
// option also matches by unambiguous prefix (getopt_long takes --dia for
// --diag), so any prefix of a bad long option counts.
func hasArg(words []string, bad ...string) string {
	for _, w := range words[1:] {
		name, _, _ := strings.Cut(w, "=")
		for _, b := range bad {
			if w == b || (strings.HasPrefix(b, "--") && len(name) > 2 && strings.HasPrefix(b, name)) {
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

func checkProgram(words []string) string {
	prog := words[0]
	if strings.ContainsAny(prog, "/=") {
		return i18n.T("the command is given by a path or with an environment variable")
	}
	if anyArgs[prog] {
		return ""
	}
	switch prog {
	case "sort":
		// Allowlist: sort also takes -o FILE and --compress-program=PROG.
		return checkSort(words)
	case "date":
		return checkDate(words)
	case "hostname":
		for _, w := range words[1:] {
			if !hostnameFlags[w] {
				return i18n.T("hostname %s changes the host name", w)
			}
		}
		return ""
	case "ss":
		if a := hasArg(words, "-K", "-D", "--kill", "--diag"); a != "" {
			return "ss " + a
		}
		return ""
	case "sensors":
		if a := hasArg(words, "-s", "--set"); a != "" {
			return i18n.T("sensors %s changes thresholds", a)
		}
		return ""
	case "file":
		if a := hasArg(words, "-C", "--compile"); a != "" {
			return i18n.T("file %s writes a file", a)
		}
		return ""
	case "uniq":
		// uniq [INPUT [OUTPUT]]: "-" is stdin and still an operand.
		pos, why := parseOpts(words, uniqOpts)
		if why == "" && len(pos) > 1 {
			why = i18n.T("uniq with an output file")
		}
		return why
	case "dmesg":
		_, why := parseOpts(words, dmesgOpts)
		return why
	case "find":
		for _, w := range words[1:] {
			switch w {
			case "-delete", "-exec", "-execdir", "-ok", "-okdir", "-fprint", "-fprint0", "-fprintf", "-fls":
				return "find " + w
			}
		}
		return ""
	case "journalctl":
		// Positional words are matches (FIELD=value) or paths: read-only.
		_, why := parseOpts(joinBoot(words), journalOpts)
		return why
	case "systemctl":
		return checkSub(words, systemctlOpts, set("status", "show", "cat", "is-active", "is-enabled", "is-failed",
			"list-units", "list-unit-files", "list-timers", "list-sockets", "list-dependencies", "list-jobs"), true)
	case "top":
		if hasArg(words, "-b") == "" {
			return i18n.T("top without -b")
		}
		return ""
	case "hostnamectl", "timedatectl", "loginctl", "networkctl", "resolvectl":
		return checkSub(words, ctlOpts, set("status", "show", "list", "list-sessions", "list-users", "statistics",
			"show-session", "show-user", "list-timezones", "timesync-status"), true)
	case "ip":
		return checkIP(words)
	case "docker":
		return checkDocker(words)
	}
	return i18n.T("%s is not in the read-only command list", prog)
}

// opts maps an allowed option to whether it takes a value. Anything not in
// the map is refused: options of these programs can write files
// (journalctl --cursor-file, docker compose config -o), so a list of bad
// options is never complete.
type opts map[string]bool

func spec(noValue, value string) opts {
	o := opts{}
	for _, f := range strings.Fields(noValue) {
		o[f] = false
	}
	for _, f := range strings.Fields(value) {
		o[f] = true
	}
	return o
}

// parseOpts walks words[1:] like getopt (options may follow positionals) and
// returns the positional words, or why the command is refused.
func parseOpts(words []string, o opts) ([]string, string) {
	prog := words[0]
	var pos []string
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--":
			return append(pos, words[i+1:]...), ""
		case w == "-" || !strings.HasPrefix(w, "-"):
			pos = append(pos, w)
		case strings.HasPrefix(w, "--"):
			name, _, hasVal := strings.Cut(w, "=")
			takes, ok := o[name]
			if !ok {
				return nil, i18n.T("%s %s is not in the safe option list", prog, name)
			}
			if hasVal && !takes {
				return nil, i18n.T("%s %s without a value", prog, name)
			}
			if takes && !hasVal {
				i++
			}
		default:
			// Short flags, possibly combined (-xe, -n50, -u nginx).
			for j := 1; j < len(w); j++ {
				f := "-" + string(w[j])
				takes, ok := o[f]
				if !ok {
					return nil, i18n.T("%s %s is not in the safe option list", prog, f)
				}
				if takes {
					if j == len(w)-1 {
						i++
					}
					break
				}
			}
		}
	}
	return pos, ""
}

// checkSub accepts `prog [options] sub ...` when sub is in allowed and all
// options are in o. With emptyOK a bare `prog` is accepted.
func checkSub(words []string, o opts, allowed map[string]bool, emptyOK bool) string {
	pos, why := parseOpts(words, o)
	if why != "" {
		return why
	}
	if len(pos) == 0 {
		if emptyOK {
			return ""
		}
		return i18n.T("%s without a subcommand", words[0])
	}
	if !allowed[pos[0]] {
		return words[0] + " " + pos[0]
	}
	return ""
}

var journalOpts = spec(
	"-f --follow -r --reverse -x --catalog -e --pager-end -a --all -q --quiet -l --full --no-full -m --merge "+
		"-k --dmesg -b --no-pager --no-hostname --utc --list-boots --disk-usage --system --user -N --fields "+
		"--no-tail --show-cursor --header --version --help",
	"-u --unit --user-unit -n --lines -S --since -U --until -p --priority -g --grep -o --output "+
		"-t --identifier -T --exclude-identifier --output-fields -F --field --boot --facility -c --cursor "+
		"--after-cursor -M --machine")

var uniqOpts = spec("-c --count -d --repeated -D --all-repeated -u --unique -i --ignore-case -z --zero-terminated "+
	"--group --version --help", "-f --skip-fields -s --skip-chars -w --check-chars")

var dmesgOpts = spec("-T --ctime -H --human -k --kernel -u --userspace -x --decode -L -t --notime -r --raw "+
	"-d --show-delta -e --reltime -P --nopager -w --follow -W --follow-new -S --syslog --version --help",
	"-l --level -f --facility --color --since --until --time-format -F --file")

var systemctlOpts = spec(
	"-a --all -l --full -r --recursive --reverse --after --before --failed --value --no-legend --no-pager "+
		"--plain -q --quiet --user --system --show-types --version --help",
	"-t --type --state -p --property -n --lines -o --output")

var ctlOpts = spec("-a --all -l --full --value --no-legend --no-pager --version --help", "-p --property")

// ip prefix-matches its words (`ip link s` is `set`), so only full,
// known read-only spellings pass.
var ipFlags = set("-4", "-6", "-s", "-stats", "-statistics", "-d", "-details", "-j", "-json", "-p", "-pretty",
	"-br", "-brief", "-c", "-color", "-o", "-oneline", "-h", "-human")

var ipObjects = set("address", "addr", "a", "link", "l", "route", "r", "ro", "neighbour", "neighbor", "neigh", "n",
	"rule", "ru", "maddress", "maddr", "m")

var ipShow = set("show", "list", "lst", "ls")

func checkIP(words []string) string {
	i := 1
	for ; i < len(words) && strings.HasPrefix(words[i], "-"); i++ {
		if !ipFlags[words[i]] {
			return i18n.T("%s %s is not in the safe option list", "ip", words[i])
		}
	}
	if i == len(words) {
		return i18n.T("ip without an object")
	}
	obj := words[i]
	if !ipObjects[obj] {
		return "ip " + obj
	}
	if i+1 == len(words) {
		return "" // `ip addr` shows
	}
	cmd := words[i+1]
	if ipShow[cmd] || (cmd == "get" && (obj == "route" || obj == "r" || obj == "ro")) {
		return ""
	}
	return "ip " + obj + " " + cmd
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

// docker compose options before the subcommand.
var composeGlobal = spec("--compatibility --version --help",
	"-f --file -p --project-name --project-directory --env-file --profile --ansi --progress --parallel")

// Options after a docker subcommand are not checked one by one; these write
// files and are refused.
var dockerFileOpts = set("-o", "--output")

func checkDocker(words []string) string {
	if len(words) < 2 {
		return ""
	}
	sub := words[1]
	if strings.HasPrefix(sub, "-") {
		if sub == "--version" || sub == "-v" {
			return ""
		}
		return i18n.T("docker global flags")
	}
	if dockerRead[sub] {
		if sub == "stats" && hasArg(words, "--no-stream") == "" {
			return i18n.T("docker stats without --no-stream")
		}
		return ""
	}
	group, ok := dockerGroups[sub]
	if !ok {
		return "docker " + sub
	}
	for _, w := range words[2:] {
		name, _, _ := strings.Cut(w, "=")
		if dockerFileOpts[name] || (len(w) > 2 && w[0] == '-' && w[1] != '-' && strings.ContainsRune(w[1:], 'o')) {
			return i18n.T("docker %s %s writes to a file", sub, w)
		}
	}
	for i := 2; i < len(words); i++ {
		w := words[i]
		if strings.HasPrefix(w, "-") {
			if sub != "compose" {
				continue
			}
			name, _, hasVal := strings.Cut(w, "=")
			takes, ok := composeGlobal[name]
			if !ok {
				return i18n.T("%s %s is not in the safe option list", "docker compose", name)
			}
			if takes && !hasVal {
				i++
			}
			continue
		}
		if !group[w] {
			return "docker " + sub + " " + w
		}
		if w == "stats" && hasArg(words, "--no-stream") == "" {
			return i18n.T("docker stats without --no-stream")
		}
		return ""
	}
	return i18n.T("%s without a subcommand", "docker "+sub)
}

var hostnameFlags = set("-s", "-f", "-d", "-i", "-I", "-A", "-a", "-y", "--short", "--fqdn", "--long", "--domain",
	"--ip-address", "--all-ip-addresses", "--all-fqdns", "--alias", "--nis")

// sortShort: flags without a value; k and t take one (attached or next word).
const sortShort = "bdfgiMhnRrVcCusmz"

var sortLong = set("--ignore-leading-blanks", "--dictionary-order", "--ignore-case", "--general-numeric-sort",
	"--ignore-nonprinting", "--month-sort", "--human-numeric-sort", "--numeric-sort", "--random-sort",
	"--reverse", "--version-sort", "--check", "--merge", "--stable", "--unique", "--zero-terminated")

func checkSort(words []string) string {
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case w == "--":
			return ""
		case w == "-" || !strings.HasPrefix(w, "-"):
			// An input file: sort only reads it.
		case strings.HasPrefix(w, "--"):
			name, _, hasVal := strings.Cut(w, "=")
			if name == "--key" || name == "--field-separator" || name == "--check" {
				if !hasVal && name != "--check" {
					i++
				}
				continue
			}
			if hasVal || !sortLong[name] {
				return i18n.T("%s %s is not in the safe option list", "sort", name)
			}
		default:
			for j := 1; j < len(w); j++ {
				c := w[j]
				if c == 'k' || c == 't' {
					if j == len(w)-1 {
						i++
					}
					break
				}
				if strings.IndexByte(sortShort, c) < 0 {
					return i18n.T("%s %s is not in the safe option list", "sort", "-"+string(c))
				}
			}
		}
	}
	return ""
}

var dateLong = set("--utc", "--universal", "--rfc-email", "--debug", "--iso-8601", "--rfc-3339", "--date", "--file",
	"--reference", "--help", "--version")

// checkDate allows printing dates; date -s and date MMDDhhmm set the clock.
// Long options must be spelled out: getopt_long would take --se for --set.
func checkDate(words []string) string {
	for i := 1; i < len(words); i++ {
		w := words[i]
		switch {
		case strings.HasPrefix(w, "+"):
		case strings.HasPrefix(w, "--"):
			name, _, hasVal := strings.Cut(w, "=")
			if !dateLong[name] {
				return i18n.T("%s %s is not in the safe option list", "date", name)
			}
			if !hasVal && (name == "--date" || name == "--file" || name == "--reference") {
				i++
			}
		case strings.HasPrefix(w, "-") && len(w) > 1:
		short:
			for j := 1; j < len(w); j++ {
				switch w[j] {
				case 'u', 'R':
				case 'I':
					break short // -I[FMT]: the rest is its value
				case 'd', 'f', 'r':
					if j == len(w)-1 {
						i++
					}
					break short
				default:
					return i18n.T("%s %s is not in the safe option list", "date", "-"+string(w[j]))
				}
			}
		default:
			return i18n.T("date %s sets the time", w)
		}
	}
	return ""
}

// joinBoot turns `journalctl -b -1` (an optional value as the next word,
// which journalctl accepts) into `-b-1` so parseOpts does not read -1 as a flag.
func joinBoot(words []string) []string {
	out := make([]string, 0, len(words))
	for i := 0; i < len(words); i++ {
		w := words[i]
		if (w == "-b" || w == "--boot") && i+1 < len(words) && isInt(words[i+1]) {
			out = append(out, "--boot="+words[i+1])
			i++
			continue
		}
		out = append(out, w)
	}
	return out
}

func isInt(s string) bool {
	s = strings.TrimPrefix(s, "-")
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
