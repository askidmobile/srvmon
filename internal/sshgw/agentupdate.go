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
// On a failed start the previous container is put back. Otherwise the
// previous container stays, stopped, as srvmon-agent-old until the caller
// confirms the new agent with AgentCommit or reverts with AgentRollback.
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
	return fmt.Errorf("%w: %w", ErrSwapInterrupted, i18n.Errorf("agent update: %s", firstLine(errOut, err)))
}

// ErrSwapInterrupted wraps an update error that may have left the swap
// halfway (lost connection, deadline): the caller should run AgentRollback.
var ErrSwapInterrupted = errors.New("agent swap interrupted")

// ErrNoPreviousAgent: AgentRollback found no srvmon-agent-old to restore.
var ErrNoPreviousAgent = errors.New("no previous agent to restore")

// AgentCommit removes the previous agent kept by AgentUpdate.
func AgentCommit(ctx context.Context, c *ssh.Client) error {
	_, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(dockerAccess+"$D rm -f srvmon-agent-old srvmon-dockerproxy-old >/dev/null 2>&1 || true\n"))
	if err != nil {
		return i18n.Errorf("agent update: %s", firstLine(errOut, err))
	}
	return nil
}

// AgentRollback puts back the previous agent kept by AgentUpdate.
func AgentRollback(ctx context.Context, c *ssh.Client) error {
	_, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(dockerAccess+agentRestoreScript))
	if strings.Contains(errOut, "srvmon:no-old") {
		return ErrNoPreviousAgent
	}
	if err != nil {
		return i18n.Errorf("agent update: %s", firstLine(errOut, err))
	}
	return nil
}

// dockerAccess sets $D to a docker command the SSH user may run.
const dockerAccess = `set -e
if docker info >/dev/null 2>&1; then D=docker
elif sudo -n docker info >/dev/null 2>&1; then D="sudo -n docker"
else echo srvmon:no-docker-access >&2; exit 3; fi
`

// agentRestoreScript replaces srvmon-agent with srvmon-agent-old, and the
// docker proxy with srvmon-dockerproxy-old if the update replaced one. An
// agent from before the proxy keeps running beside the new proxy, unused.
const agentRestoreScript = `$D inspect srvmon-agent-old >/dev/null 2>&1 || { echo srvmon:no-old >&2; exit 6; }
$D rm -f srvmon-agent >/dev/null 2>&1 || true
$D rename srvmon-agent-old srvmon-agent
if $D inspect srvmon-dockerproxy-old >/dev/null 2>&1; then
  $D rm -f srvmon-dockerproxy >/dev/null 2>&1 || true
  $D rename srvmon-dockerproxy-old srvmon-dockerproxy
  $D start srvmon-dockerproxy >/dev/null
fi
$D start srvmon-agent >/dev/null
`

// agentUpdateScript starts the containers with the same flags as
// agentScript. An agent installed before srvmon-dockerproxy existed gets
// the proxy here.
func agentUpdateScript(a AgentInstall) string {
	var b strings.Builder
	b.WriteString(dockerAccess)
	fmt.Fprintf(&b, "IMG=%s\nDEFURL=%s\nRU=%s\nRT=%s\nSM=%s\n",
		shq(a.Image), shq(a.URL), shq(a.RegistryUser), shq(a.RegistryToken), smartWanted(a.Smart))
	b.WriteString(`$D inspect srvmon-agent >/dev/null 2>&1 || { echo srvmon:no-agent >&2; exit 3; }
ENVS=$($D inspect -f '{{range .Config.Env}}{{println .}}{{end}}' srvmon-agent)
printf '%s\n' "$ENVS" | grep -qx 'SRVMON_TOKEN_FILE=/etc/srvmon/agent.token' || {
  echo srvmon:not-panel >&2; exit 3; }
URL=$(printf '%s\n' "$ENVS" | sed -n 's/^SRVMON_URL=//p' | head -n 1)
[ -n "$URL" ] || URL=$DEFURL
if [ "$SM" = keep ]; then
  if printf '%s\n' "$ENVS" | grep -qx 'SRVMON_SMART=1'; then SM=1; else SM=0; fi
fi
` + smartScript + `REG=${IMG%%/*}
if [ -n "$RU" ]; then printf '%s' "$RT" | $D login "$REG" -u "$RU" --password-stdin >/dev/null; fi
pulled=0
$D pull -q "$IMG" >/dev/null 2>&1 && pulled=1
if [ -n "$RU" ]; then $D logout "$REG" >/dev/null 2>&1 || true; fi
[ "$pulled" = 1 ] || { echo srvmon:pull-failed >&2; exit 4; }
$D rm -f srvmon-agent-old srvmon-dockerproxy-old >/dev/null 2>&1 || true
$D rename srvmon-agent srvmon-agent-old
$D stop srvmon-agent-old >/dev/null 2>&1 || true
if $D inspect srvmon-dockerproxy >/dev/null 2>&1; then
  $D rename srvmon-dockerproxy srvmon-dockerproxy-old
  $D stop srvmon-dockerproxy-old >/dev/null 2>&1 || true
fi
if ` + dockerProxyRun + ` && \
  ` + agentRun + `; then
  :
else
` + agentRestoreScript + `  echo srvmon:run-failed >&2; exit 5
fi
`)
	return b.String()
}
