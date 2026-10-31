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

# Optional values passed from GitHub (vars.SRVMON_DOMAIN, vars.ACME_EMAIL).
[[ -n "${SRVMON_DOMAIN:-}" ]] && set_env SRVMON_DOMAIN "$SRVMON_DOMAIN"
[[ -n "${ACME_EMAIL:-}" ]] && set_env ACME_EMAIL "$ACME_EMAIL"
if grep -q '^SRVMON_DOMAIN=mon.example.com' .env; then
  echo "set SRVMON_DOMAIN in $(pwd)/.env or in the GitHub variable SRVMON_DOMAIN"
  exit 1
fi

wait_healthy() {
  local id status
  for _ in $(seq 1 30); do
    id="$(docker compose ps -q server)"
    status="$(docker inspect -f '{{.State.Health.Status}}' "$id" 2>/dev/null || echo starting)"
    [[ "$status" == "healthy" ]] && return 0
    sleep 3
  done
  echo "server health: $status"
  return 1
}

PREV="$(grep '^SRVMON_TAG=' .env | cut -d= -f2 || true)"
echo "deploy: ${PREV:-none} -> $TAG"

# Log directory for fail2ban, writable by the distroless nonroot user.
mkdir -p /var/log/srvmon 2>/dev/null || sudo mkdir -p /var/log/srvmon
chown 65532:65532 /var/log/srvmon 2>/dev/null || sudo chown 65532:65532 /var/log/srvmon

set_tag "$TAG"
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
