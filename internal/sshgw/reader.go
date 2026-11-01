package sshgw

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// ReaderUser is the account the panel runs the AI agent's read-only
// commands as. Unlike the panel's SSH user it is in no group (above all not
// in docker, which is root-equivalent) and has no sudo, so what a
// read-only command can read is bounded by the server's own file
// permissions. It signs in with the panel's key for the server, without a
// pty or agent/X11 forwarding. Docker reaches it only through the panel's
// filtering bridge (DockerBridge).
const ReaderUser = "srvmon-ro"

// ErrNoRootAccess: the panel's SSH user has neither root, nor the docker
// group, nor passwordless sudo, so the reader account cannot be set up.
// Such a user is itself unprivileged.
var ErrNoRootAccess = errors.New("no root access")

// readerRootScript creates ReaderUser (if missing), takes it out of every
// supplementary group and makes the panel key its only authorized key.
// Runs as root on the host. Prints reader-ok.
func readerRootScript(pubKey string) string {
	return "set -e\nR=" + shq(ReaderUser) + "\nKEY=" + shq(strings.TrimSpace(pubKey)) + "\n" + `if ! id -u "$R" >/dev/null 2>&1; then
  if command -v useradd >/dev/null 2>&1; then useradd -m -s /bin/sh "$R"; else adduser -D -s /bin/sh "$R"; fi
  if command -v usermod >/dev/null 2>&1; then usermod -p '*' "$R"; else echo "$R:*" | chpasswd -e; fi
fi
G=$(id -gn "$R")
for g in $(id -Gn "$R"); do
  [ "$g" = "$G" ] && continue
  if command -v gpasswd >/dev/null 2>&1; then gpasswd -d "$R" "$g" >/dev/null; else delgroup "$R" "$g" >/dev/null; fi
done
H=$(getent passwd "$R" | cut -d: -f6)
[ -n "$H" ] && [ "$H" != / ] || { echo "no home directory for $R" >&2; exit 4; }
mkdir -p "$H/.ssh"
chown "$R:$G" "$H" "$H/.ssh"
chmod 700 "$H/.ssh"
printf 'no-agent-forwarding,no-X11-forwarding,no-pty %s\n' "$KEY" > "$H/.ssh/authorized_keys"
chown "$R:$G" "$H/.ssh/authorized_keys"
chmod 600 "$H/.ssh/authorized_keys"
echo reader-ok
`
}

// ReaderParams are the inputs of EnsureReader.
type ReaderParams struct {
	PublicKey string // the panel's key for the server, authorized_keys format
	// Image runs the one-off host-sh container when the SSH user reaches
	// root only through docker: the panel's agent image.
	Image         string
	RegistryUser  string
	RegistryToken string
}

// EnsureReader sets up ReaderUser over the panel SSH user's connection c,
// with whatever root access that user has: root itself, passwordless sudo,
// or the docker group (a one-off container of the agent image chroots into
// the host). Returns ErrNoRootAccess when it has none.
func EnsureReader(ctx context.Context, c *ssh.Client, p ReaderParams) error {
	if strings.TrimSpace(p.PublicKey) == "" {
		return errors.New("no public key")
	}
	out, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(ensureReaderScript(p)))
	switch {
	case strings.Contains(errOut, "srvmon:no-access"):
		return ErrNoRootAccess
	case err != nil:
		return i18n.Errorf("setting up %s: %s", ReaderUser, firstLine(errOut, err))
	case !strings.Contains(out, "reader-ok"):
		return i18n.Errorf("setting up %s: %s", ReaderUser, firstLine(errOut+out, nil))
	}
	return nil
}

func ensureReaderScript(p ReaderParams) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nRS=%s\nIMG=%s\nRU=%s\nRT=%s\n",
		shq(readerRootScript(p.PublicKey)), shq(p.Image), shq(p.RegistryUser), shq(p.RegistryToken))
	b.WriteString(`if [ "$(id -u)" = 0 ]; then printf '%s' "$RS" | sh -s; exit; fi
if sudo -n true 2>/dev/null; then printf '%s' "$RS" | sudo -n sh -s; exit; fi
D=""
if docker info >/dev/null 2>&1; then D=docker
elif sudo -n docker info >/dev/null 2>&1; then D="sudo -n docker"; fi
[ -n "$D" ] && [ -n "$IMG" ] || { echo srvmon:no-access >&2; exit 6; }
REG=${IMG%%/*}
if [ -n "$RU" ]; then printf '%s' "$RT" | $D login "$REG" -u "$RU" --password-stdin >/dev/null 2>&1 || true; fi
$D pull -q "$IMG" >/dev/null 2>&1 || true
if [ -n "$RU" ]; then $D logout "$REG" >/dev/null 2>&1 || true; fi
printf '%s' "$RS" | $D run --rm -i --pull never --network none --security-opt no-new-privileges=true \
  --user 0:0 -v /:/host --entrypoint /srvmon-agent "$IMG" host-sh
`)
	return b.String()
}
