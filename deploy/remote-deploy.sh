#!/usr/bin/env bash
# Runs on the monitoring server (called by .github/workflows/deploy.yml).
#   remote-deploy.sh <image-tag>
# Pulls the tag, restarts the stack, waits for srvmon-server to become
# healthy and rolls back to the previous tag if it does not.
set -euo pipefail

TAG="${1:?usage: remote-deploy.sh <image-tag>}"
cd "$(dirname "$0")"

[[ -f .env ]] || cp .env.example .env

# set_env KEY VALUE: upsert a line in .env
set_env() {
  if grep -q "^$1=" .env; then
    sed -i "s|^$1=.*|$1=$2|" .env
  else
    echo "$1=$2" >> .env
  fi
}
set_tag() { set_env SRVMON_TAG "$1"; }

# Optional value passed from GitHub (vars.SRVMON_DOMAIN). Store it in .env
# and drop it from the environment: an exported (even empty) variable would
# override .env for every compose call.
for key in SRVMON_DOMAIN SRVMON_PROXY SRVMON_HTTP_BIND; do
  if [[ -n "${!key:-}" ]]; then
    set_env "$key" "${!key}"
  fi
  unset "$key"
done
if grep -q '^SRVMON_DOMAIN=mon.example.com' .env; then
  echo "set SRVMON_DOMAIN in $(pwd)/.env or in the GitHub variable SRVMON_DOMAIN"
  exit 1
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

# The fail2ban ban chain depends on the proxy mode and is set by
# install-host.sh (root). This script has no root, so flag a mismatch.
if [[ -f /etc/fail2ban/jail.d/srvmon.conf ]]; then
  want=DOCKER-USER
  [[ "$PROXY" == external ]] && want=INPUT
  have="$(sed -n 's/^chain *= *//p' /etc/fail2ban/jail.d/srvmon.local 2>/dev/null || true)"
  have="${have:-DOCKER-USER}"
  if [[ "$have" != "$want" ]]; then
    echo "::warning::fail2ban jail srvmon-auth bans in $have, proxy mode $PROXY needs $want: run sudo bash $(pwd)/install-host.sh"
  fi
fi
if [[ -f /etc/fail2ban/jail.d/srvmon.conf && ! -f /etc/fail2ban/action.d/srvmon-notify.conf ]]; then
  echo "::warning::fail2ban bans are not reported to the panel yet: run sudo bash $(pwd)/install-host.sh"
fi

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

# Log directory for fail2ban, owned by the distroless nonroot user.
# install-host.sh prepares it; only fix it here when needed and possible.
if [[ "$(stat -c %u:%g /var/log/srvmon 2>/dev/null)" != "65532:65532" ]]; then
  if ! { mkdir -p /var/log/srvmon && chown 65532:65532 /var/log/srvmon; } 2>/dev/null &&
     ! sudo -n sh -c 'mkdir -p /var/log/srvmon && chown 65532:65532 /var/log/srvmon' 2>/dev/null; then
    echo "warning: /var/log/srvmon is not owned by 65532; run install-host.sh as root (fail2ban log will not be written)"
  fi
fi

set_tag "$TAG"
if [[ "$PROXY" == "external" ]]; then
  # Profile-disabled services are not orphans: stop a Caddy left from caddy mode.
  docker compose --profile caddy rm -sf caddy >/dev/null 2>&1 || true
fi
# Any failure here (pull, `up` erroring on an unhealthy dependency, or the
# health wait) goes to the rollback below instead of exiting via set -e.
if docker compose pull --quiet && docker compose up -d --remove-orphans && wait_healthy; then
  echo "deploy ok: $TAG"
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
