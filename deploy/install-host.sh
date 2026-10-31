#!/usr/bin/env bash
# One-time setup of the monitoring server host (Debian/Ubuntu). Run as root.
# The Deploy workflow uploads this script with fail2ban/ into DEPLOY_PATH, so
# after the first deploy:  sudo bash /opt/srvmon/install-host.sh
# Installs docker (if missing) and fail2ban with the srvmon-auth jail.
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
apt-get update -qq
apt-get install -y -qq fail2ban

install -m 0644 "$HERE/fail2ban/filter.d/srvmon-auth.conf" /etc/fail2ban/filter.d/srvmon-auth.conf
install -m 0644 "$HERE/fail2ban/jail.d/srvmon.conf" /etc/fail2ban/jail.d/srvmon.conf
mkdir -p /var/log/srvmon && touch /var/log/srvmon/auth.log
chown -R 65532:65532 /var/log/srvmon
systemctl enable --now fail2ban
systemctl reload fail2ban || systemctl restart fail2ban

mkdir -p "$DEPLOY_PATH"
[[ -f "$DEPLOY_PATH/.env" ]] || cp "$HERE/.env.example" "$DEPLOY_PATH/.env"
chown -R "$DEPLOY_USER" "$DEPLOY_PATH"
if [[ "$DEPLOY_USER" != root ]]; then
  usermod -aG docker "$DEPLOY_USER"
fi

echo "Done. Edit $DEPLOY_PATH/.env (SRVMON_DOMAIN), then let CI deploy."
echo "After the first deploy: cd $DEPLOY_PATH && docker compose exec -it server /srvmon-server init"
