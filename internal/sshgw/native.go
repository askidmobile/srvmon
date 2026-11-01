package sshgw

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/askidmobile/srvmon/internal/i18n"
	"golang.org/x/crypto/ssh"
)

// Agent install modes.
const (
	// ModeAuto: docker when the server has it; a systemd service on a
	// Proxmox VE node without docker; otherwise docker is installed.
	ModeAuto = "auto"
	// ModeDocker: the srvmon-agent and srvmon-dockerproxy containers.
	ModeDocker = "docker"
	// ModeSystemd: the agent binary as a systemd service, no docker.
	ModeSystemd = "systemd"
)

// ValidAgentMode reports whether m is a known install mode.
func ValidAgentMode(m string) bool {
	return m == ModeAuto || m == ModeDocker || m == ModeSystemd
}

// Paths of the systemd agent. The directory and the binary belong to the
// panel's SSH user, so the panel updates the agent without root: replacing
// the binary is as powerful as the docker group a container agent needs.
const (
	NativeDir   = "/usr/local/lib/srvmon-agent"
	NativeBin   = NativeDir + "/srvmon-agent"
	NativeEnv   = "/etc/srvmon/agent.env"
	NativeToken = "/etc/srvmon/agent.token"
	NativeUnit  = "/etc/systemd/system/srvmon-agent.service"
)

// AgentBinary is the agent executable for one architecture.
type AgentBinary struct {
	Data    []byte
	SHA256  string // hex; empty skips the check
	Version string // what `srvmon-agent version` prints; empty skips the check
}

// AgentBinaries returns the agent binary for a GOARCH (amd64, arm64).
type AgentBinaries func(arch string) (AgentBinary, error)

// archOf maps `uname -m` to a GOARCH.
func archOf(uname string) (string, bool) {
	switch strings.TrimSpace(uname) {
	case "x86_64", "amd64":
		return "amd64", true
	case "aarch64", "arm64", "armv8l":
		return "arm64", true
	}
	return "", false
}

func hostArch(ctx context.Context, c *ssh.Client) (string, error) {
	out, _, err := Run(ctx, c, "uname -m", nil)
	if err != nil {
		return "", fmt.Errorf("uname -m: %w", err)
	}
	a, ok := archOf(out)
	if !ok {
		return "", i18n.Errorf("no agent build for the %s architecture", strings.TrimSpace(out))
	}
	return a, nil
}

// detectModeScript prints the mode ModeAuto resolves to.
const detectModeScript = `if command -v docker >/dev/null 2>&1; then echo docker
elif { [ -d /etc/pve ] || command -v pveversion >/dev/null 2>&1; } && [ -d /run/systemd/system ]; then echo systemd
else echo docker; fi
`

func detectMode(ctx context.Context, c *ssh.Client) string {
	out, _, err := Run(ctx, c, "sh -s", strings.NewReader(detectModeScript))
	if err == nil && strings.TrimSpace(out) == ModeSystemd {
		return ModeSystemd
	}
	return ModeDocker
}

// NativeParams are the inputs of NativeInstallScript.
type NativeParams struct {
	Owner string // owner of the binary and the token: the panel's SSH user; "" = root
	URL   string
	Token string // "" keeps the token file on the server
	Smart bool
	Src   string // staged binary on the server, removed by the script
	SHA   string // expected sha256 of Src, "" skips the check
	Want  string // expected `version` output, "" skips the check
}

// NativeInstallScript (re)installs the systemd agent; it runs as root.
func NativeInstallScript(p NativeParams) string {
	sm := "0"
	if p.Smart {
		sm = "1"
	}
	return fmt.Sprintf("set -e\nU=%s\nURL=%s\nTOKEN=%s\nSM=%s\nSRC=%s\nSHA=%s\nWANT=%s\n",
		shq(p.Owner), shq(p.URL), shq(p.Token), sm, shq(p.Src), shq(p.SHA), shq(p.Want)) + nativeInstallBody
}

// nativeUnitText is the service: root, but with a read-only view of the
// host and only the capabilities the collectors use (reading any file and
// /proc of other processes; raw disk reads for S.M.A.R.T.).
const nativeUnitText = `[Unit]
Description=srvmon agent: pushes host metrics to the srvmon panel
Documentation=https://github.com/askidmobile/srvmon
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
Environment=SRVMON_AGENT_MODE=systemd
Environment=SRVMON_TOKEN_FILE=` + NativeToken + `
Environment=SRVMON_ENV_FILE=` + NativeEnv + `
Environment=DOCKER_SOCK=
EnvironmentFile=` + NativeEnv + `
ExecStart=` + NativeBin + `
# The agent exits when the panel replaces its binary or environment file.
Restart=always
RestartSec=3
NoNewPrivileges=yes
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectKernelLogs=yes
ProtectControlGroups=yes
ProtectClock=yes
ProtectHostname=yes
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK
CapabilityBoundingSet=CAP_DAC_READ_SEARCH CAP_SYS_PTRACE CAP_SYS_RAWIO CAP_SYS_ADMIN
DevicePolicy=closed
DeviceAllow=block-sd r
DeviceAllow=block-blkext r
UMask=0077
MemoryMax=256M
CPUQuota=50%

[Install]
WantedBy=multi-user.target
`

// nativeInstallBody expects U, URL, TOKEN, SM, SRC, SHA and WANT.
const nativeInstallBody = `command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ] || {
  rm -f "$SRC"; echo srvmon:no-systemd >&2; exit 3; }
OWN=root:root
if [ -n "$U" ]; then OWN="$U:$(id -gn "$U")"; fi
DIR=` + NativeDir + `
mkdir -p "$DIR"
chown "$OWN" "$DIR"
chmod 755 "$DIR"
N="$DIR/srvmon-agent.new"
cp "$SRC" "$N"
rm -f "$SRC"
bad() { rm -f "$N"; echo "srvmon:bad-binary $1" >&2; exit 4; }
if [ -n "$SHA" ] && command -v sha256sum >/dev/null 2>&1; then
  echo "$SHA  $N" | sha256sum -c - >/dev/null 2>&1 || bad checksum
fi
chmod 755 "$N"
V=$("$N" version 2>/dev/null) || bad "does not run"
if [ -n "$WANT" ] && [ "$V" != "$WANT" ]; then bad "version $V"; fi
chown "$OWN" "$N"
mv -f "$N" "$DIR/srvmon-agent"
rm -f "$DIR/srvmon-agent.old"
mkdir -p /etc/srvmon
chmod 711 /etc/srvmon
if [ -n "$TOKEN" ]; then (umask 077; printf '%s\n' "$TOKEN" > ` + NativeToken + `); fi
[ -s ` + NativeToken + ` ] || { echo srvmon:no-token >&2; exit 5; }
chown "$OWN" ` + NativeToken + `
chmod 600 ` + NativeToken + `
(umask 077; printf 'SRVMON_URL=%s\nSRVMON_SMART=%s\n' "$URL" "$SM" > ` + NativeEnv + `)
chown "$OWN" ` + NativeEnv + `
# A container agent the panel installed before is replaced, not doubled.
if command -v docker >/dev/null 2>&1 && docker inspect srvmon-agent >/dev/null 2>&1; then
  if docker inspect -f '{{range .Config.Env}}{{println .}}{{end}}' srvmon-agent | grep -qx 'SRVMON_TOKEN_FILE=/etc/srvmon/agent.token'; then
    docker rm -f srvmon-agent srvmon-agent-old srvmon-dockerproxy srvmon-dockerproxy-old >/dev/null 2>&1 || true
    echo srvmon:docker-removed
  fi
fi
cat > ` + NativeUnit + ` <<'SRVMON_UNIT'
` + nativeUnitText + `SRVMON_UNIT
systemctl daemon-reload
systemctl enable srvmon-agent.service >/dev/null 2>&1
systemctl restart srvmon-agent.service
sleep 3
systemctl is-active --quiet srvmon-agent.service || {
  journalctl -u srvmon-agent.service -n 3 --no-pager -o cat >&2 2>/dev/null || true
  echo srvmon:not-active >&2; exit 6; }
echo srvmon:native-ok
`

// nativeRemoveScript removes a systemd agent before a container agent is
// started in its place; it runs as root.
const nativeRemoveScript = `if [ -f ` + NativeUnit + ` ]; then
  systemctl disable --now srvmon-agent.service >/dev/null 2>&1 || true
  rm -f ` + NativeUnit + `
  systemctl daemon-reload >/dev/null 2>&1 || true
  rm -rf ` + NativeDir + ` ` + NativeEnv + `
fi
`

// stageBinary copies the binary to a private temp file of the login user
// and returns its path.
func stageBinary(ctx context.Context, c *ssh.Client, data []byte) (string, error) {
	out, errOut, err := Run(ctx, c, `umask 077; f=$(mktemp "${TMPDIR:-/tmp}/srvmon-agent.XXXXXX") && cat > "$f" && printf '%s' "$f"`, bytes.NewReader(data))
	if err != nil {
		return "", i18n.Errorf("copying the agent binary: %s", firstLine(errOut, err))
	}
	return strings.TrimSpace(out), nil
}

// installNative installs the systemd agent during the onboarding.
func installNative(ctx context.Context, c *ssh.Client, pr *priv, owner string, a AgentInstall, log Logger) bool {
	if a.Binaries == nil {
		log(StepWarn, i18n.T("the panel has no agent binaries (SRVMON_AGENT_BIN_DIR): the agent is not started"))
		return false
	}
	arch, err := hostArch(ctx, c)
	if err != nil {
		log(StepWarn, i18n.T("the agent is not started: %s", err.Error()))
		return false
	}
	bin, err := a.Binaries(arch)
	if err != nil {
		log(StepWarn, i18n.T("the agent is not started: %s", err.Error()))
		return false
	}
	log(StepRun, i18n.T("systemd service srvmon-agent (linux/%s, no docker)", arch))
	src, err := stageBinary(ctx, c, bin.Data)
	if err != nil {
		log(StepWarn, i18n.T("the agent is not started: %s", err.Error()))
		return false
	}
	out, errOut, err := pr.run(ctx, NativeInstallScript(NativeParams{
		Owner: owner, URL: a.URL, Token: a.Token, Smart: a.Smart != nil && *a.Smart,
		Src: src, SHA: bin.SHA256, Want: bin.Version,
	}))
	if strings.Contains(out, "srvmon:docker-removed") {
		log(StepInfo, i18n.T("the previous srvmon-agent container is removed"))
	}
	if err != nil {
		_, _, _ = Run(ctx, c, "rm -f "+shq(src), nil)
		log(StepWarn, i18n.T("the agent is not started: %s", nativeError(errOut, err)))
		return false
	}
	log(StepOK, i18n.T("srvmon-agent started as a systemd service"))
	return true
}

func nativeError(errOut string, err error) string {
	switch {
	case strings.Contains(errOut, "srvmon:no-systemd"):
		return i18n.T("the server has no systemd")
	case strings.Contains(errOut, "srvmon:bad-binary"):
		return i18n.T("the agent binary does not run on the server (%s)", strings.TrimSpace(after(errOut, "srvmon:bad-binary")))
	case strings.Contains(errOut, "srvmon:no-token"):
		return i18n.T("there is no agent token on the server")
	case strings.Contains(errOut, "srvmon:not-active"):
		return i18n.T("the service did not start: %s", firstLine(errOut, err))
	}
	return firstLine(errOut, err)
}

func after(s, marker string) string {
	_, rest, _ := strings.Cut(s, marker)
	if i := strings.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// nativeUpdateScript swaps in the binary uploaded to srvmon-agent.new;
// it runs as the binary's owner. The agent notices the new file and exits,
// and systemd starts the new one. The previous binary stays as
// srvmon-agent.old until AgentCommit or AgentRollback.
func nativeUpdateScript(sha, want, smart string) string {
	return fmt.Sprintf("set -e\nDIR=%s\nSHA=%s\nWANT=%s\nSM=%s\n", NativeDir, shq(sha), shq(want), smart) + `N="$DIR/srvmon-agent.new"
bad() { rm -f "$N"; echo "srvmon:bad-binary $1" >&2; exit 4; }
if [ -n "$SHA" ] && command -v sha256sum >/dev/null 2>&1; then
  echo "$SHA  $N" | sha256sum -c - >/dev/null 2>&1 || bad checksum
fi
chmod 755 "$N"
V=$("$N" version 2>/dev/null) || bad "does not run"
if [ -n "$WANT" ] && [ "$V" != "$WANT" ]; then bad "version $V"; fi
E=` + NativeEnv + `
if [ "$SM" != keep ] && [ ! -w "$E" ]; then rm -f "$N"; echo srvmon:no-write >&2; exit 4; fi
cp -p "$DIR/srvmon-agent" "$DIR/srvmon-agent.old"
rm -f "$DIR/agent.env.old"
mv -f "$N" "$DIR/srvmon-agent"
if [ "$SM" != keep ]; then
  # /etc/srvmon belongs to root: the backup goes next to the binary.
  cp "$E" "$DIR/agent.env.old"
  C=$(grep -v '^SRVMON_SMART=' "$E" || true)
  printf '%s\nSRVMON_SMART=%s\n' "$C" "$SM" > "$E"
fi
`
}

// nativeUploadCmd checks the agent is a systemd one the SSH user may
// update and writes stdin to srvmon-agent.new.
const nativeUploadCmd = `[ -f ` + NativeUnit + ` ] || { echo srvmon:no-agent >&2; exit 3; }; ` +
	`[ -w ` + NativeDir + ` ] && [ -w ` + NativeBin + ` ] || { echo srvmon:no-write >&2; exit 4; }; ` +
	`cat > ` + NativeDir + `/srvmon-agent.new`

// nativeBranch runs native when the server has the systemd agent.
func nativeBranch(native string) string {
	return "if [ -f " + NativeUnit + " ]; then\n" + native + "exit 0\nfi\n"
}

const nativeCommit = `rm -f ` + NativeDir + `/srvmon-agent.old ` + NativeDir + `/agent.env.old
`

const nativeRollback = `[ -f ` + NativeDir + `/srvmon-agent.old ] || { echo srvmon:no-old >&2; exit 6; }
if [ -f ` + NativeDir + `/agent.env.old ]; then
  cat ` + NativeDir + `/agent.env.old > ` + NativeEnv + `
  rm -f ` + NativeDir + `/agent.env.old
fi
mv -f ` + NativeDir + `/srvmon-agent.old ` + NativeBin + `
`

// agentMode tells which agent the panel installed on the server.
func agentMode(ctx context.Context, c *ssh.Client) (string, error) {
	out, _, err := Run(ctx, c, "[ -f "+NativeUnit+" ] && echo systemd || echo docker", nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// nativeUpdate replaces the binary of a systemd agent.
func nativeUpdate(ctx context.Context, c *ssh.Client, a AgentInstall) error {
	if a.Binaries == nil {
		return errors.New(i18n.T("the panel has no agent binaries (SRVMON_AGENT_BIN_DIR)"))
	}
	arch, err := hostArch(ctx, c)
	if err != nil {
		return err
	}
	bin, err := a.Binaries(arch)
	if err != nil {
		return err
	}
	if _, errOut, err := Run(ctx, c, nativeUploadCmd, bytes.NewReader(bin.Data)); err != nil {
		return nativeUpdateError(errOut, err)
	}
	if _, errOut, err := Run(ctx, c, "sh -s", strings.NewReader(nativeUpdateScript(bin.SHA256, bin.Version, smartWanted(a.Smart)))); err != nil {
		return nativeUpdateError(errOut, err)
	}
	return nil
}

func nativeUpdateError(errOut string, err error) error {
	switch {
	case strings.Contains(errOut, "srvmon:no-agent"):
		return errors.New(i18n.T("there is no srvmon-agent service on the server: reinstall the agent with the add wizard"))
	case strings.Contains(errOut, "srvmon:no-write"):
		return errors.New(i18n.T("the panel's SSH user cannot replace %s: reinstall the agent with the add wizard", NativeBin))
	case strings.Contains(errOut, "srvmon:bad-binary"):
		return errors.New(i18n.T("the agent binary does not run on the server (%s)", strings.TrimSpace(after(errOut, "srvmon:bad-binary"))))
	}
	return fmt.Errorf("%w: %w", ErrSwapInterrupted, i18n.Errorf("agent update: %s", firstLine(errOut, err)))
}

// WriteAgentToken replaces the agent token on the server in place: the
// agent re-reads the file and switches to it. A container agent's token
// file belongs to root, so a one-off container of image writes it.
func WriteAgentToken(ctx context.Context, c *ssh.Client, image, token string) error {
	script := fmt.Sprintf("set -e\nIMG=%s\n", shq(image)) +
		nativeBranch(`[ -w `+NativeToken+` ] || { echo srvmon:no-write >&2; exit 4; }
cat > `+NativeToken+`
`) + dockerAccess + `$D inspect srvmon-agent >/dev/null 2>&1 || { echo srvmon:no-agent >&2; exit 3; }
$D inspect -f '{{range .Config.Env}}{{println .}}{{end}}' srvmon-agent | grep -qx 'SRVMON_TOKEN_FILE=/etc/srvmon/agent.token' || {
  echo srvmon:not-panel >&2; exit 3; }
$D run --rm -i --pull missing --network none --cap-drop ALL --security-opt no-new-privileges=true \
  -v /etc/srvmon:/etc/srvmon --entrypoint /srvmon-agent "$IMG" write-token /etc/srvmon/agent.token >/dev/null
# Agents older than the periodic token reload read the file only at start.
$D restart srvmon-agent >/dev/null
`
	// The script goes in the command line, the token on stdin: neither
	// lands in a file or the process list.
	_, errOut, err := Run(ctx, c, "sh -c "+shq(script), strings.NewReader(token+"\n"))
	if err == nil {
		return nil
	}
	switch {
	case strings.Contains(errOut, "srvmon:no-write"):
		return errors.New(i18n.T("the panel's SSH user cannot write %s", NativeToken))
	case strings.Contains(errOut, "srvmon:no-docker-access"):
		return errors.New(i18n.T("the panel's SSH user has no docker access on the server: the docker group or passwordless sudo is needed"))
	case strings.Contains(errOut, "srvmon:no-agent"):
		return errors.New(i18n.T("there is no srvmon-agent on the server: install it with the command below"))
	case strings.Contains(errOut, "srvmon:not-panel"):
		return errors.New(i18n.T("srvmon-agent on this server was not installed by the panel: put the token into its configuration by hand"))
	}
	return i18n.Errorf("writing the token: %s", firstLine(errOut, err))
}

// InstallShellScript is the panel's /agent/install.sh: it installs the
// systemd agent by hand, as root, with the token in SRVMON_TOKEN, e.g.
//
//	curl -fsSL https://mon.example.com/agent/install.sh | SRVMON_TOKEN=... sh
//
// The binary comes from the panel, which hands it out only for a live agent
// token. SRVMON_OWNER (default: srvmon when that user exists) owns the
// binary so the panel can update it over SSH; SRVMON_SMART=1 turns on
// S.M.A.R.T.
func InstallShellScript(panelURL string) string {
	return `#!/bin/sh
# srvmon agent as a systemd service, without docker.
set -e
URL=` + shq(panelURL) + `
TOKEN=${SRVMON_TOKEN:-}
[ -n "$TOKEN" ] || { echo "srvmon: set SRVMON_TOKEN (the agent token from the panel)" >&2; exit 2; }
[ "$(id -u)" = 0 ] || { echo "srvmon: run as root (sudo)" >&2; exit 2; }
case $(uname -m) in
  x86_64|amd64) A=amd64 ;;
  aarch64|arm64|armv8l) A=arm64 ;;
  *) echo "srvmon: no agent build for $(uname -m)" >&2; exit 2 ;;
esac
SRC=$(mktemp)
B="$URL/agent/srvmon-agent-linux-$A"
if command -v curl >/dev/null 2>&1; then
  curl -fsS -H "Authorization: Bearer $TOKEN" -o "$SRC" "$B" || { rm -f "$SRC"; echo "srvmon: download failed: $B" >&2; exit 2; }
elif command -v wget >/dev/null 2>&1; then
  wget -q --header="Authorization: Bearer $TOKEN" -O "$SRC" "$B" || { rm -f "$SRC"; echo "srvmon: download failed: $B" >&2; exit 2; }
else
  rm -f "$SRC"; echo "srvmon: curl or wget is needed" >&2; exit 2
fi
U=${SRVMON_OWNER:-}
if [ -z "$U" ] && id -u srvmon >/dev/null 2>&1; then U=srvmon; fi
SM=0
if [ "${SRVMON_SMART:-0}" = 1 ]; then SM=1; fi
SHA=
WANT=
` + nativeInstallBody + `echo "srvmon-agent is running: systemctl status srvmon-agent"
`
}

// ManualDockerScript starts the two agent containers by hand, the way the
// add wizard does, with the token already in /etc/srvmon/agent.token.
func ManualDockerScript(image, url string) string {
	return "IMG=" + shq(image) + " URL=" + shq(url) + " D=docker; set --\n" + dockerProxyRun + "\n" + agentRun + "\n"
}
