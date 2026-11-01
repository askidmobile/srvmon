package sshgw

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// Emergency keys are the owner's own public keys, added next to the panel
// key in the panel account's authorized_keys on every server. If the panel
// (or its master key) is lost, the owner still gets in with them. They live
// in a block between two marker lines that the panel rewrites as a whole;
// lines outside the block, the panel key among them, are never touched.
const (
	emergencyBegin = "# srvmon emergency keys: begin (managed by the srvmon panel, edit in settings)"
	emergencyEnd   = "# srvmon emergency keys: end"
	// MaxEmergencyKeys bounds the list.
	MaxEmergencyKeys = 10
)

// EmergencyKey is a parsed public key.
type EmergencyKey struct {
	Line        string `json:"line"` // normalized: type, base64, comment
	Type        string `json:"type"`
	Fingerprint string `json:"fingerprint"`
	Comment     string `json:"comment"`
}

// ParseEmergencyKey accepts one authorized_keys line (options are dropped)
// and returns it normalized.
func ParseEmergencyKey(line string) (EmergencyKey, error) {
	line = strings.TrimSpace(line)
	if strings.Contains(line, "PRIVATE KEY") {
		return EmergencyKey{}, errors.New(i18n.T("this is a private key: paste the public one (.pub)"))
	}
	pub, comment, _, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	if err != nil || len(strings.TrimSpace(string(rest))) > 0 {
		return EmergencyKey{}, errors.New(i18n.T("not an SSH public key"))
	}
	switch pub.Type() {
	case ssh.KeyAlgoRSA:
		if k, ok := pub.(ssh.CryptoPublicKey); ok {
			if rk, ok := k.CryptoPublicKey().(interface{ Size() int }); ok && rk.Size()*8 < 3072 {
				return EmergencyKey{}, errors.New(i18n.T("RSA keys shorter than 3072 bits are not accepted: use ed25519"))
			}
		}
	case ssh.KeyAlgoDSA:
		return EmergencyKey{}, errors.New(i18n.T("DSA keys are not accepted: use ed25519"))
	}
	comment = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || r == '#' {
			return -1
		}
		return r
	}, strings.TrimSpace(comment))
	if len(comment) > 100 {
		comment = comment[:100]
	}
	norm := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if comment != "" {
		norm += " " + comment
	}
	return EmergencyKey{Line: norm, Type: pub.Type(), Fingerprint: ssh.FingerprintSHA256(pub), Comment: comment}, nil
}

// EmergencyHash identifies a key list, to tell which servers have the
// current one.
func EmergencyHash(lines []string) string {
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(h[:8])
}

// SetEmergencyKeys rewrites the emergency block in the SSH user's
// authorized_keys (an empty list removes it). It runs as that user: only
// its own file changes.
func SetEmergencyKeys(ctx context.Context, c *ssh.Client, lines []string) error {
	_, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(emergencyScript(lines)))
	if err != nil {
		if strings.Contains(errOut, "srvmon:bad-block") {
			return errors.New(i18n.T("authorized_keys has a broken emergency block (begin without end): fix it by hand"))
		}
		return fmt.Errorf("authorized_keys: %s", firstLine(errOut, err))
	}
	return nil
}

func emergencyScript(lines []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nB=%s\nE=%s\nKEYS=%s\n", shq(emergencyBegin), shq(emergencyEnd), shq(strings.Join(lines, "\n")))
	b.WriteString(`umask 077
D="$HOME/.ssh"
F="$D/authorized_keys"
mkdir -p "$D"
chmod 700 "$D"
touch "$F"
nb=$(grep -cxF "$B" "$F" || true)
ne=$(grep -cxF "$E" "$F" || true)
# An unterminated block would swallow the lines after it, the panel key
# among them: stop instead.
[ "$nb" = "$ne" ] || { echo srvmon:bad-block >&2; exit 5; }
T=$(mktemp "$D/.authorized_keys.XXXXXX")
trap 'rm -f "$T"' EXIT
awk -v b="$B" -v e="$E" '$0==b{skip=1;next} $0==e{skip=0;next} !skip' "$F" > "$T"
if [ -n "$KEYS" ]; then
  printf '%s\n%s\n%s\n' "$B" "$KEYS" "$E" >> "$T"
fi
chmod 600 "$T"
mv -f "$T" "$F"
trap - EXIT
`)
	return b.String()
}
