package sshgw

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/askidmobile/srvmon/internal/i18n"
	"golang.org/x/crypto/ssh"
)

// AgentUpdate replaces the srvmon-agent container that the add wizard
// installed with a.Image, keeping its token file and URL. It runs as the
// panel's SSH user, which is in the docker group (or has passwordless sudo).
// a.Token is not used: the token stays in /etc/srvmon/agent.token.
// On a failed start the previous container is put back.
func AgentUpdate(ctx context.Context, c *ssh.Client, a AgentInstall) error {
	_, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(agentUpdateScript(a)))
	if err == nil {
		return nil
	}
	switch {
	case strings.Contains(errOut, "srvmon:no-docker-access"):
		return errors.New(i18n.T("the panel's SSH user has no docker access on the server: the docker group or passwordless sudo is needed"))
	case strings.Contains(errOut, "srvmon:no-agent"):
		return errors.New(i18n.T("there is no srvmon-agent container on the server: reinstall the agent with the add wizard"))
	case strings.Contains(errOut, "srvmon:not-panel"):
		return errors.New(i18n.T("srvmon-agent on this server was not installed by the panel: update it there with docker compose pull && docker compose up -d"))
	case strings.Contains(errOut, "srvmon:pull-failed"):
		return errors.New(i18n.T("docker pull %s failed on the server", a.Image))
	case strings.Contains(errOut, "srvmon:run-failed"):
		return errors.New(i18n.T("the new agent %s did not start, the previous one is restored", a.Image))
	}
	return i18n.Errorf("agent update: %s", firstLine(errOut, err))
}

// agentUpdateScript must start the container with the same flags as
// agentScript.
func agentUpdateScript(a AgentInstall) string {
	var b strings.Builder
	fmt.Fprintf(&b, "set -e\nIMG=%s\nDEFURL=%s\nRU=%s\nRT=%s\n",
		shq(a.Image), shq(a.URL), shq(a.RegistryUser), shq(a.RegistryToken))
	b.WriteString(`if docker info >/dev/null 2>&1; then D=docker
elif sudo -n docker info >/dev/null 2>&1; then D="sudo -n docker"
else echo srvmon:no-docker-access >&2; exit 3; fi
$D inspect srvmon-agent >/dev/null 2>&1 || { echo srvmon:no-agent >&2; exit 3; }
ENVS=$($D inspect -f '{{range .Config.Env}}{{println .}}{{end}}' srvmon-agent)
printf '%s\n' "$ENVS" | grep -qx 'SRVMON_TOKEN_FILE=/etc/srvmon/agent.token' || {
  echo srvmon:not-panel >&2; exit 3; }
URL=$(printf '%s\n' "$ENVS" | sed -n 's/^SRVMON_URL=//p' | head -n 1)
[ -n "$URL" ] || URL=$DEFURL
REG=${IMG%%/*}
if [ -n "$RU" ]; then printf '%s' "$RT" | $D login "$REG" -u "$RU" --password-stdin >/dev/null; fi
pulled=0
$D pull -q "$IMG" >/dev/null 2>&1 && pulled=1
if [ -n "$RU" ]; then $D logout "$REG" >/dev/null 2>&1 || true; fi
[ "$pulled" = 1 ] || { echo srvmon:pull-failed >&2; exit 4; }
$D rm -f srvmon-agent-old >/dev/null 2>&1 || true
$D rename srvmon-agent srvmon-agent-old
$D stop srvmon-agent-old >/dev/null 2>&1 || true
if $D run -d --name srvmon-agent --restart unless-stopped \
  --network host --pid host --read-only --security-opt no-new-privileges:true \
  -e SRVMON_URL="$URL" -e SRVMON_TOKEN_FILE=/etc/srvmon/agent.token \
  -v /etc/srvmon/agent.token:/etc/srvmon/agent.token:ro \
  -v /proc:/host/proc:ro -v /:/host:ro,rslave \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  "$IMG" >/dev/null; then
  $D rm -f srvmon-agent-old >/dev/null 2>&1 || true
else
  $D rm -f srvmon-agent >/dev/null 2>&1 || true
  $D rename srvmon-agent-old srvmon-agent
  $D start srvmon-agent >/dev/null 2>&1 || true
  echo srvmon:run-failed >&2; exit 5
fi
`)
	return b.String()
}
