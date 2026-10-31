#!/usr/bin/env bash
# One-time setup of the monitoring server host (Debian/Ubuntu). Run as root.
# The Deploy workflow uploads this script into DEPLOY_PATH, so after the
# first deploy:  sudo bash /opt/srvmon/install-host.sh
# Installs docker (if missing) and prepares DEPLOY_PATH for the CI deploy
# user. fail2ban is not installed here: it runs in the compose stack, so the
# host package manager (apt) is not used at all.
set -euo pipefail

DEPLOY_PATH="${DEPLOY_PATH:-/opt/srvmon}"
# Keep the current owner of an existing deploy folder (the CI deploy user).
if [[ -z "${DEPLOY_USER:-}" && -d "$DEPLOY_PATH" ]]; then
  DEPLOY_USER="$(stat -c %U "$DEPLOY_PATH")"
fi
DEPLOY_USER="${DEPLOY_USER:-${SUDO_USER:-root}}"
HERE="$(cd "$(dirname "$0")" && pwd)"

if ! command -v docker >/dev/null; then
  curl -fsSL https://get.docker.com | sh
fi

mkdir -p "$DEPLOY_PATH"
[[ -f "$DEPLOY_PATH/.env" ]] || cp "$HERE/.env.example" "$DEPLOY_PATH/.env"
chown -R "$DEPLOY_USER" "$DEPLOY_PATH"
if [[ "$DEPLOY_USER" != root ]]; then
  usermod -aG docker "$DEPLOY_USER"
fi

# A host fail2ban with the srvmon jails from an older install-host.sh would
# ban twice (and its srvmon-auth no longer sees auth.log): remove them.
removed=0
for f in /etc/fail2ban/jail.d/srvmon.conf /etc/fail2ban/jail.d/srvmon.local; do
  [[ -f "$f" ]] && rm -f "$f" && removed=1
done
if [[ "$removed" == 1 ]] && systemctl is-active -q fail2ban; then
  systemctl reload fail2ban || systemctl restart fail2ban
fi

echo "Done. Edit $DEPLOY_PATH/.env (SRVMON_DOMAIN), then let CI deploy."
echo "After the first deploy: cd $DEPLOY_PATH && docker compose exec -it server /srvmon-server init"
