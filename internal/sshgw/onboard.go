package sshgw

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/crypto/ssh"
)

// Step states for the onboarding log.
const (
	StepOK   = "ok"
	StepRun  = "run"
	StepWarn = "warn"
	StepFail = "fail"
	StepInfo = "info"
)

// Logger receives onboarding progress. Texts never contain the password.
type Logger func(state, text string)

// AgentInstall describes how to start srvmon-agent on the target.
type AgentInstall struct {
	Image string // e.g. ghcr.io/askidmobile/srvmon-agent:latest
	URL   string // public URL of the panel the agent pushes to
	Token string // agent token, written to a root-only file
	// Optional registry credentials when the image is private. They are
	// used for one pull and logged out right after.
	RegistryUser  string
	RegistryToken string
}

// OnboardParams are the inputs of Onboard.
type OnboardParams struct {
	Addr     string
	User     string // login used once with the password (root or a sudoer)
	Password string
	HostKey  ssh.PublicKey // confirmed by the owner after ProbeHostKey

	Key             KeyPair // installed for SSHUser
	SSHUser         string  // account srvmon uses from now on, normally "srvmon"
	Sudo            bool    // passwordless sudo for SSHUser
	DisablePassword bool    // PasswordAuthentication no
	Agent           *AgentInstall
}

// Result reports what Onboard achieved besides the mandatory key setup.
type Result struct {
	AgentInstalled   bool
	PasswordDisabled bool
}

var userRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// priv runs scripts with root rights over a password session.
type priv struct {
	c        *ssh.Client
	root     bool
	nopass   bool // sudo without password
	password string
}

func (p *priv) run(ctx context.Context, script string) (string, string, error) {
	switch {
	case p.root:
		return Run(ctx, p.c, "sh -s", strings.NewReader(script))
	case p.nopass:
		return Run(ctx, p.c, "sudo -n sh -s", strings.NewReader(script))
	default:
		// -k: always ask, so the password line is always consumed by sudo
		// and never reaches sh. -p '': no prompt text in the output.
		return Run(ctx, p.c, "sudo -S -k -p '' sh -s", strings.NewReader(p.password+"\n"+script))
	}
}

// Onboard performs the one-time setup: password login with the pinned host
// key, account and authorized_keys for SSHUser, a key login check, then the
// optional agent install and sshd hardening. The password is only held in
// params and in the SSH auth exchange; callers drop params afterwards.
func Onboard(ctx context.Context, p OnboardParams, log Logger) (Result, error) {
	var res Result
	if !userRe.MatchString(p.SSHUser) {
		return res, fmt.Errorf("bad ssh user %q", p.SSHUser)
	}
	log(StepRun, fmt.Sprintf("ssh %s@%s — вход по паролю", p.User, p.Addr))
	c, err := Dial(ctx, p.Addr, p.User, p.HostKey,
		ssh.Password(p.Password),
		ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
			ans := make([]string, len(questions))
			for i := range ans {
				ans[i] = p.Password
			}
			return ans, nil
		}))
	if err != nil {
		if strings.Contains(err.Error(), "host key mismatch") {
			return res, errors.New("host key не совпадает с подтверждённым: возможна подмена сервера")
		}
		if strings.Contains(err.Error(), "unable to authenticate") {
			return res, errors.New("неверный пользователь или пароль (или вход по паролю на сервере отключён)")
		}
		return res, fmt.Errorf("подключение: %w", err)
	}
	defer c.Close()
	log(StepOK, fmt.Sprintf("подключено, host key %s совпадает", ssh.FingerprintSHA256(p.HostKey)))

	pr := &priv{c: c, password: p.Password}
	uid, _, err := Run(ctx, c, "id -u", nil)
	if err != nil {
		return res, fmt.Errorf("id -u: %w", err)
	}
	pr.root = strings.TrimSpace(uid) == "0"
	if !pr.root {
		if _, _, err := Run(ctx, c, "sudo -n true", nil); err == nil {
			pr.nopass = true
		}
		if _, _, err := pr.run(ctx, "true\n"); err != nil {
			return res, fmt.Errorf("у %s нет root и sudo не сработал: нужен root или пользователь с sudo", p.User)
		}
		log(StepOK, "права root через sudo")
	}

	out, errOut, err := pr.run(ctx, userScript(p.SSHUser, p.Key.Public, p.Sudo))
	if err != nil {
		return res, fmt.Errorf("создание пользователя %s: %s", p.SSHUser, firstLine(errOut, err))
	}
	groups := "без docker"
	if strings.Contains(out, "docker-group") {
		groups = "группа docker"
	}
	log(StepOK, fmt.Sprintf("пользователь %s (%s), authorized_keys ← %s", p.SSHUser, groups, p.Key.Fingerprint))
	if p.Sudo {
		if strings.Contains(out, "sudo-ok") {
			log(StepOK, "sudo без пароля для "+p.SSHUser)
		} else {
			log(StepWarn, "sudo не настроен (нет sudo/visudo на сервере)")
		}
	}

	kc, err := DialKey(ctx, p.Addr, p.SSHUser, p.HostKey, p.Key.Private)
	if err != nil {
		return res, fmt.Errorf("вход по ключу не прошёл: %w", err)
	}
	who, _, err := Run(ctx, kc, "id -un", nil)
	kc.Close()
	if err != nil || strings.TrimSpace(who) != p.SSHUser {
		return res, fmt.Errorf("вход по ключу: неожиданный ответ %q %v", strings.TrimSpace(who), err)
	}
	log(StepOK, "проверка входа по ключу — ok")

	if a := p.Agent; a != nil {
		log(StepRun, "docker run srvmon-agent ("+a.Image+")")
		_, errOut, err := pr.run(ctx, agentScript(*a))
		switch {
		case err == nil:
			res.AgentInstalled = true
			log(StepOK, "srvmon-agent запущен")
		case strings.Contains(errOut, "no-docker"):
			log(StepWarn, "docker не установлен: агент не запущен, поставьте docker и переустановите агента")
		default:
			log(StepWarn, "агент не запущен: "+firstLine(errOut, err))
		}
	}

	if p.DisablePassword {
		_, errOut, err := pr.run(ctx, disablePasswordScript)
		if err != nil {
			log(StepWarn, "PasswordAuthentication не изменён: "+firstLine(errOut, err))
		} else {
			res.PasswordDisabled = true
			log(StepOK, "sshd: PasswordAuthentication no · reload")
		}
	}
	return res, nil
}

func firstLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if s == "" && err != nil {
		s = err.Error()
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// userScript creates the account, adds it to docker, installs the public key
// and optionally grants passwordless sudo. Prints markers for the log.
func userScript(user, pubKey string, sudo bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nU=%s\nKEY=%s\n", shq(user), shq(pubKey))
	b.WriteString(`if ! id -u "$U" >/dev/null 2>&1; then
  SH=$(command -v bash || echo /bin/sh)
  if command -v useradd >/dev/null 2>&1; then useradd -m -s "$SH" "$U"; else adduser -D -s "$SH" "$U"; fi
fi
# "*" = no password but not locked: sshd refuses key logins to locked
# accounts on systems without PAM.
if command -v usermod >/dev/null 2>&1; then usermod -p '*' "$U"; else echo "$U:*" | chpasswd -e; fi
if getent group docker >/dev/null 2>&1; then
  if command -v usermod >/dev/null 2>&1; then usermod -aG docker "$U"; else addgroup "$U" docker; fi
  echo docker-group
fi
H=$(getent passwd "$U" | cut -d: -f6)
G=$(id -gn "$U")
mkdir -p "$H/.ssh"
chown "$U:$G" "$H/.ssh"
chmod 700 "$H/.ssh"
touch "$H/.ssh/authorized_keys"
grep -qxF "$KEY" "$H/.ssh/authorized_keys" || printf '%s\n' "$KEY" >> "$H/.ssh/authorized_keys"
chown "$U:$G" "$H/.ssh/authorized_keys"
chmod 600 "$H/.ssh/authorized_keys"
`)
	if sudo {
		b.WriteString(`if command -v visudo >/dev/null 2>&1 && [ -d /etc/sudoers.d ]; then
  T=$(mktemp)
  printf '%s ALL=(ALL) NOPASSWD:ALL\n' "$U" > "$T"
  if visudo -cf "$T" >/dev/null; then
    chmod 440 "$T"; mv "$T" "/etc/sudoers.d/$U"; echo sudo-ok
  else rm -f "$T"; fi
fi
`)
	}
	return b.String()
}

// agentScript writes the token to a root-only file and (re)starts the agent
// container with the same mounts as deploy/agent/docker-compose.yml.
func agentScript(a AgentInstall) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nIMG=%s\nURL=%s\nTOKEN=%s\nRU=%s\nRT=%s\n",
		shq(a.Image), shq(a.URL), shq(a.Token), shq(a.RegistryUser), shq(a.RegistryToken))
	b.WriteString(`command -v docker >/dev/null 2>&1 || { echo no-docker >&2; exit 3; }
mkdir -p /etc/srvmon
chmod 700 /etc/srvmon
# Stage the new token: a running agent keeps its file until the pull works.
(umask 077; printf '%s\n' "$TOKEN" > /etc/srvmon/agent.token.new)
REG=${IMG%%/*}
if [ -n "$RU" ]; then printf '%s' "$RT" | docker login "$REG" -u "$RU" --password-stdin >/dev/null; fi
pulled=0
docker pull -q "$IMG" >/dev/null 2>&1 && pulled=1
if [ -n "$RU" ]; then docker logout "$REG" >/dev/null 2>&1 || true; fi
[ "$pulled" = 1 ] || { rm -f /etc/srvmon/agent.token.new; echo "docker pull $IMG не удался (приватный образ? нужен доступ к реестру)" >&2; exit 4; }
docker rm -f srvmon-agent >/dev/null 2>&1 || true
mv -f /etc/srvmon/agent.token.new /etc/srvmon/agent.token
docker run -d --name srvmon-agent --restart unless-stopped \
  --network host --pid host --read-only --security-opt no-new-privileges:true \
  -e SRVMON_URL="$URL" -e SRVMON_TOKEN_FILE=/etc/srvmon/agent.token \
  -v /etc/srvmon/agent.token:/etc/srvmon/agent.token:ro \
  -v /proc:/host/proc:ro -v /:/host:ro,rslave \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  "$IMG" >/dev/null
`)
	return b.String()
}

// disablePasswordScript turns off password logins. sshd takes the first
// value it reads, and an Include may come before or after other settings,
// so the directive goes to the very top of sshd_config. Success is judged by
// the effective config (sshd -T), not just by syntax (sshd -t); otherwise
// the change is reverted.
const disablePasswordScript = `set -e
F=/etc/ssh/sshd_config
SSHD=$(command -v sshd || echo /usr/sbin/sshd)
cp -p "$F" "$F.srvmon.bak"
if ! grep -q '^# srvmon: key-only logins' "$F"; then
  { printf '# srvmon: key-only logins\nPasswordAuthentication no\n'; cat "$F.srvmon.bak"; } > "$F"
fi
revert() { cp -p "$F.srvmon.bak" "$F"; echo "$1" >&2; exit 1; }
"$SSHD" -t || revert "sshd -t failed"
"$SSHD" -T 2>/dev/null | grep -qi '^passwordauthentication no$' || revert "PasswordAuthentication is still on (sshd -T)"
systemctl reload ssh 2>/dev/null || systemctl reload sshd 2>/dev/null || service ssh reload 2>/dev/null || service sshd reload 2>/dev/null || rc-service sshd reload
`
