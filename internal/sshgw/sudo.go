package sshgw

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/askidmobile/srvmon/internal/i18n"
	"golang.org/x/crypto/ssh"
)

// Sudo modes of the panel's SSH user on a server.
const (
	SudoNone    = "none"    // no sudoers file from srvmon
	SudoLimited = "limited" // service management and logs, see limitedVerbs
	SudoFull    = "full"    // NOPASSWD:ALL, root in the terminal
	SudoCustom  = "custom"  // detected only: rules srvmon did not write
)

// ValidSudoMode reports whether mode can be set.
func ValidSudoMode(mode string) bool {
	return mode == SudoNone || mode == SudoLimited || mode == SudoFull
}

// limitedVerbs are the systemctl verbs of the limited mode: look at and
// restart services, but not edit, enable, mask or link unit files.
var limitedVerbs = []string{
	"status", "start", "stop", "restart", "reload", "try-restart", "reload-or-restart",
	"is-active", "is-enabled", "is-failed", "list-units", "list-timers", "list-unit-files",
	"show", "cat",
}

// sudoersGen is a shell snippet that writes the sudoers rules of mode for
// $U into the file $T (empty for SudoNone). Binaries are looked up on the
// host, since sudoers needs full paths and distributions differ.
func sudoersGen(mode string) string {
	switch mode {
	case SudoFull:
		return `printf '# srvmon: full sudo, managed by the srvmon panel\n%s ALL=(ALL) NOPASSWD:ALL\n' "$U" > "$T"
`
	case SudoLimited:
		var b strings.Builder
		b.WriteString(`bin() { for d in /usr/bin /bin /usr/sbin /sbin /usr/local/bin; do if [ -x "$d/$1" ]; then echo "$d/$1"; return 0; fi; done; return 0; }
SC=$(bin systemctl); JC=$(bin journalctl); SV=$(bin service); RC=$(bin rc-service); DK=$(bin docker)
R=""
add() { if [ -z "$R" ]; then R="$1"; else R="$R, $1"; fi; }
if [ -n "$SC" ]; then
  for v in`)
		for _, v := range limitedVerbs {
			b.WriteString(" " + v)
		}
		b.WriteString(`; do add "$SC $v"; add "$SC $v *"; done
fi
[ -z "$JC" ] || add "$JC"
[ -z "$SV" ] || add "$SV"
[ -z "$RC" ] || add "$RC"
[ -z "$DK" ] || add "$DK"
{
  echo '# srvmon: limited sudo, managed by the srvmon panel'
  echo '# services (systemctl status/restart/..., service), journalctl, docker'
  [ -z "$R" ] || printf '%s ALL=(root) NOPASSWD: %s\n' "$U" "$R"
} > "$T"
`)
		return b.String()
	default:
		return `: > "$T"
`
	}
}

// sudoersRootScript installs the rules as root (onboarding). Prints
// sudo-ok when sudo took them.
func sudoersRootScript(mode string) string {
	if mode == SudoNone {
		return `rm -f "/etc/sudoers.d/$U"
`
	}
	return `if command -v visudo >/dev/null 2>&1 && [ -d /etc/sudoers.d ]; then
  T=$(mktemp)
` + sudoersGen(mode) + `  if visudo -cf "$T" >/dev/null; then
    chmod 440 "$T"; mv "$T" "/etc/sudoers.d/$U"; echo sudo-ok
  else rm -f "$T"; fi
fi
`
}

// SudoParams are the inputs of SetSudo.
type SudoParams struct {
	User string // the panel's SSH user
	Mode string
	// Image runs the one-off write-sudoers container: the panel's agent
	// image, with optional registry credentials.
	Image         string
	RegistryUser  string
	RegistryToken string
}

// SetSudo changes the sudo rules of the panel's SSH user over its own
// connection. The new file is checked with visudo on the server, then
// written by a one-off container of the agent image with /etc/sudoers.d
// mounted (the docker group is root-equivalent anyway, and this way the
// panel can also widen a narrowed sudo). Without docker it falls back to
// the user's current passwordless sudo. Returns the mode detected after.
func SetSudo(ctx context.Context, c *ssh.Client, p SudoParams) (string, error) {
	if !userRe.MatchString(p.User) {
		return "", fmt.Errorf("bad ssh user %q", p.User)
	}
	if !ValidSudoMode(p.Mode) {
		return "", fmt.Errorf("bad sudo mode %q", p.Mode)
	}
	_, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(setSudoScript(p)))
	if err != nil {
		switch {
		case strings.Contains(errOut, "srvmon:no-sudo"):
			return "", errors.New(i18n.T("sudo is not installed on the server (no visudo or /etc/sudoers.d)"))
		case strings.Contains(errOut, "srvmon:sudoers-invalid"):
			return "", errors.New(i18n.T("visudo refused the new sudoers file, nothing changed"))
		case strings.Contains(errOut, "srvmon:no-access"):
			return "", errors.New(i18n.T("the panel's SSH user has neither docker access nor passwordless sudo on the server"))
		case strings.Contains(errOut, "srvmon:write-failed"):
			return "", i18n.Errorf("writing the sudoers file failed: %s", firstLine(strings.ReplaceAll(errOut, "srvmon:write-failed", ""), err))
		}
		return "", i18n.Errorf("sudo change: %s", firstLine(errOut, err))
	}
	return DetectSudo(ctx, c)
}

func setSudoScript(p SudoParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nU=%s\nMODE=%s\nIMG=%s\nRU=%s\nRT=%s\n",
		shq(p.User), shq(p.Mode), shq(p.Image), shq(p.RegistryUser), shq(p.RegistryToken))
	b.WriteString(`VS=$(command -v visudo || { for d in /usr/sbin /sbin /usr/bin /bin; do [ -x "$d/visudo" ] && { echo "$d/visudo"; break; }; done; })
if [ "$MODE" != none ]; then
  [ -n "$VS" ] && [ -d /etc/sudoers.d ] || { echo srvmon:no-sudo >&2; exit 3; }
fi
T=$(mktemp)
trap 'rm -f "$T"' EXIT
`)
	b.WriteString(sudoersGen(p.Mode))
	b.WriteString(`if [ -s "$T" ]; then "$VS" -cf "$T" >/dev/null 2>&1 || { echo srvmon:sudoers-invalid >&2; exit 4; }; fi
if docker info >/dev/null 2>&1; then
  [ -d /etc/sudoers.d ] || exit 0
  REG=${IMG%%/*}
  if ! docker image inspect "$IMG" >/dev/null 2>&1; then
    if [ -n "$RU" ]; then printf '%s' "$RT" | docker login "$REG" -u "$RU" --password-stdin >/dev/null; fi
    docker pull -q "$IMG" >/dev/null 2>&1 || true
    if [ -n "$RU" ]; then docker logout "$REG" >/dev/null 2>&1 || true; fi
  fi
  if ! OUT=$(docker run --rm -i --pull never --network none --read-only --cap-drop ALL \
    --security-opt no-new-privileges=true --user 0:0 \
    -v /etc/sudoers.d:/sudoers.d --entrypoint /srvmon-agent "$IMG" write-sudoers "$U" < "$T" 2>&1); then
    printf 'srvmon:write-failed %s\n' "$(printf '%s' "$OUT" | tail -n 1)" >&2; exit 5
  fi
elif sudo -n true 2>/dev/null; then
  if [ -s "$T" ]; then
    sudo -n sh -c 'umask 337; cat > "/etc/sudoers.d/.$1.srvmon-tmp" && mv -f "/etc/sudoers.d/.$1.srvmon-tmp" "/etc/sudoers.d/$1"' sh "$U" < "$T" ||
      { echo srvmon:write-failed >&2; exit 5; }
  else
    sudo -n rm -f "/etc/sudoers.d/$U" || { echo srvmon:write-failed >&2; exit 5; }
  fi
else
  echo srvmon:no-access >&2; exit 6
fi
`)
	return b.String()
}

// DetectSudo reports the sudo mode of the connected user from sudo -n -l.
func DetectSudo(ctx context.Context, c *ssh.Client) (string, error) {
	out, _, err := Run(ctx, c, "command -v sudo >/dev/null 2>&1 || exit 0; LC_ALL=C sudo -n -l 2>/dev/null || true", nil)
	if err != nil {
		return "", err
	}
	return ClassifySudo(out), nil
}

// sudoRules returns the rules of sudo -l output, one per element, with the
// lines sudo wraps at the terminal width joined back.
func sudoRules(out string) []string {
	var rules []string
	in := false
	for _, line := range strings.Split(out, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.Contains(line, "may run the following commands"):
			in = true
		case !in || t == "":
		case strings.HasPrefix(t, "("):
			rules = append(rules, t)
		case len(rules) > 0:
			rules[len(rules)-1] += " " + t
		}
	}
	return rules
}

// ClassifySudo maps the output of sudo -n -l to a mode.
func ClassifySudo(out string) string {
	var nopass []string
	for _, r := range sudoRules(out) {
		i := strings.Index(r, "NOPASSWD:")
		if i < 0 {
			continue
		}
		cmds := strings.Split(r[i+len("NOPASSWD:"):], ",")
		for j := range cmds {
			cmds[j] = strings.Join(strings.Fields(cmds[j]), " ")
			if cmds[j] == "ALL" {
				return SudoFull
			}
		}
		nopass = append(nopass, cmds...)
	}
	if len(nopass) == 0 {
		return SudoNone
	}
	for _, c := range nopass {
		if !limitedCommand(c) {
			return SudoCustom
		}
	}
	return SudoLimited
}

func limitedCommand(cmd string) bool {
	f := strings.Fields(cmd)
	if len(f) == 0 {
		return true
	}
	base := f[0][strings.LastIndexByte(f[0], '/')+1:]
	switch base {
	case "journalctl", "service", "rc-service", "docker":
		return len(f) == 1
	case "systemctl":
		if len(f) < 2 || len(f) > 3 || (len(f) == 3 && f[2] != "*") {
			return false
		}
		for _, v := range limitedVerbs {
			if f[1] == v {
				return true
			}
		}
	}
	return false
}
