#!/usr/bin/env bash
# Runs on the monitoring server (called by .github/workflows/deploy.yml).
#   remote-deploy.sh <image-tag>
# Pulls the tag, restarts the stack, waits for srvmon-server to become
# healthy and rolls back to the previous tag if it does not.
set -euo pipefail

TAG="${1:?usage: remote-deploy.sh <image-tag> | --ai-key (key on stdin)}"
cd "$(dirname "$0")"

[[ -f .env ]] || cp .env.example .env
# .env holds tokens and the AI provider key.
chmod 600 .env

# set_env KEY VALUE: upsert a line in .env
set_env() {
  if grep -q "^$1=" .env; then
    sed -i "s|^$1=.*|$1=$2|" .env
  else
    echo "$1=$2" >> .env
  fi
}
set_tag() { set_env SRVMON_TAG "$1"; }

# remote-deploy.sh --ai-key: store the AI provider key read from stdin
# (deploy.yml pipes the AI_API_KEY secret, so it never shows in argv).
if [[ "$TAG" == "--ai-key" ]]; then
  IFS= read -r key || true
  if [[ ! "$key" =~ ^[A-Za-z0-9._:-]+$ ]]; then
    echo "AI_API_KEY: unexpected characters, not stored"
    exit 1
  fi
  set_env SRVMON_AI_API_KEY "$key"
  echo "ai key stored"
  exit 0
fi

# Optional value passed from GitHub (vars.SRVMON_DOMAIN). Store it in .env
# and drop it from the environment: an exported (even empty) variable would
# override .env for every compose call.
for key in SRVMON_DOMAIN SRVMON_PROXY SRVMON_HTTP_BIND SRVMON_AI_PROVIDER SRVMON_AI_MODEL SRVMON_SMART; do
  if [[ -n "${!key:-}" ]]; then
    set_env "$key" "${!key}"
  fi
  unset "$key"
done
if grep -q '^SRVMON_DOMAIN=mon.example.com' .env; then
  echo "set SRVMON_DOMAIN in $(pwd)/.env or in the GitHub variable SRVMON_DOMAIN"
  exit 1
fi

# Shared token between srvmon-server and srvmon-ai, generated once.
if ! grep -q '^SRVMON_AI_TOKEN=.\{32,\}' .env; then
  set_env SRVMON_AI_TOKEN "$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')"
fi

# The monitoring host shows up in the panel under its hostname.
grep -q '^SRVMON_SELF_NAME=' .env || set_env SRVMON_SELF_NAME "$(hostname -s)"

PROXY="$(grep '^SRVMON_PROXY=' .env | cut -d= -f2 || true)"
case "${PROXY:-caddy}" in
  caddy) set_env COMPOSE_PROFILES caddy ;;
  external) set_env COMPOSE_PROFILES "" ;;
  *) echo "SRVMON_PROXY must be caddy or external, got: $PROXY"; exit 1 ;;
esac
PROXY="${PROXY:-caddy}"
echo "proxy: $PROXY"

# S.M.A.R.T. of the host's disks: the agent needs device access, added by an
# override file only when asked for (SRVMON_SMART=1).
if grep -q '^SRVMON_SMART=1$' .env; then
  set_env COMPOSE_FILE docker-compose.yml:docker-compose.smart.yml
  echo "smart: on"
else
  sed -i '/^COMPOSE_FILE=/d' .env
fi

# fail2ban runs in the compose stack (service fail2ban). Its ban chain follows
# the proxy mode; sshd is watched only when the host writes /var/log/auth.log.
chain=DOCKER-USER
[[ "$PROXY" == external ]] && chain=INPUT
sshd=false
[[ -f /var/log/auth.log ]] && sshd=true
mkdir -p fail2ban/container
printf '# Written by remote-deploy.sh on every deploy.\n[srvmon-auth]\nchain = %s\n\n[sshd]\nenabled = %s\n' \
  "$chain" "$sshd" > fail2ban/container/zz-srvmon.local
echo "fail2ban: srvmon-auth chain $chain, sshd $sshd"
if systemctl is-active -q fail2ban 2>/dev/null && [[ -f /etc/fail2ban/jail.d/srvmon.conf ]]; then
  echo "::warning::host fail2ban also has the srvmon jails (old install-host.sh); bans are doubled. Run sudo bash $(pwd)/install-host.sh, it removes them."
fi

# `up` does not restart fail2ban when only its jail files changed: reload it,
# then print the jails. A fail2ban problem is a warning, not a failed deploy.
fail2ban_check() {
  local out
  for _ in $(seq 1 10); do
    if docker compose exec -T fail2ban fail2ban-client reload >/dev/null 2>&1 &&
       out="$(docker compose exec -T fail2ban fail2ban-client status 2>&1)"; then
      echo "fail2ban: $(echo "$out" | sed -n 's/.*Jail list:[[:space:]]*//p')"
      return 0
    fi
    sleep 3
  done
  echo "::warning::fail2ban container is not answering:"
  docker compose logs --tail=30 fail2ban || true
}

wait_healthy() {
  local id status
  for _ in $(seq 1 30); do
    id="$(docker compose ps -q server)"
    status="$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null || echo starting)"
    if [[ "$status" == "healthy" ]]; then
      [[ "$PROXY" == "caddy" ]] || return 0
      # Caddy must be up too, otherwise the panel is unreachable.
      id="$(docker compose ps -q caddy)"
      [[ "$(docker inspect -f '{{.State.Running}}' "$id" 2>/dev/null)" == "true" ]] && return 0
      status="healthy, caddy not running"
    fi
    sleep 3
  done
  echo "health: $status"
  return 1
}

PREV="$(grep '^SRVMON_TAG=' .env | cut -d= -f2 || true)"
echo "deploy: ${PREV:-none} -> $TAG"

set_tag "$TAG"
if [[ "$PROXY" == "external" ]]; then
  # Profile-disabled services are not orphans: stop a Caddy left from caddy mode.
  docker compose --profile caddy rm -sf caddy >/dev/null 2>&1 || true
fi
# Any failure here (pull, `up` erroring on an unhealthy dependency, or the
# health wait) goes to the rollback below instead of exiting via set -e.
if docker compose pull --quiet && docker compose up -d --remove-orphans && wait_healthy; then
  echo "deploy ok: $TAG"
  fail2ban_check
  docker image prune -f >/dev/null || true
  exit 0
fi

echo "deploy failed, logs:"
docker compose logs --tail=50 server || true
if [[ -n "$PREV" && "$PREV" != "$TAG" ]]; then
  echo "rolling back to $PREV"
  set_tag "$PREV"
  if docker compose up -d --remove-orphans && wait_healthy; then
    echo "rollback ok: $PREV"
  else
    echo "rollback is unhealthy too"
  fi
fi
exit 1
