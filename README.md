# srvmon

**English** · [Русский](README.ru.md) · [中文](README.zh-CN.md)

Self-hosted monitoring panel for your own servers: host metrics and docker containers, an SSH terminal in the browser, an AI agent that works on your servers with your approval, and Telegram alerts. Sign-in is a password plus a TOTP code (Google Authenticator and compatible apps), protected by fail2ban.

The interface is a dark TUI-style dashboard (btop-like) with a green accent. The UI language is currently Russian.

- Architecture: [docs/architecture.md](docs/architecture.md)
- Mockups: [docs/design](docs/design)
- Monitoring server requirements: [docs/server-requirements.md](docs/server-requirements.md)
- Telegram bot where `api.telegram.org` is blocked (e.g. Russia): [docs/telegram-proxy.md](docs/telegram-proxy.md)

## Features

- **Metrics and containers.** A small Go agent (`srvmon-agent`) on each server reads CPU, RAM, swap, load, disks, network and uptime from `/proc`, containers through the docker socket (read-only), and pushes them to the panel. It buffers for an hour if the panel is unreachable. Metrics are stored in VictoriaMetrics.
- **Server screen.** Per-core CPU, memory, disks, network, processes, containers with status/health/restarts, and syslog + docker logs, all live while the screen is open.
- **Onboarding wizard.** Add a server, verify its SSH host key, log in once with a password or your own key. srvmon creates a `srvmon` user with its own ed25519 key, installs the agent and can disable password auth. The one-time password or key is never stored.
- **Web terminal.** xterm.js ↔ WebSocket ↔ SSH client inside `srvmon-server` ↔ sshd. Tabs, resize, `docker exec` into a container. Private keys never leave the monitoring server; they are encrypted at rest (XChaCha20-Poly1305).
- **AI agent.** Built on the [pi](https://pi.dev) SDK, any provider (DeepSeek, Anthropic, OpenAI, OpenRouter, Google, Ollama, any OpenAI- or Anthropic-compatible endpoint). Read-only commands run immediately; anything that changes a server waits for your explicit approval. The policy is enforced by `srvmon-server`, not by the model. Scheduled tasks (cron) report to Telegram.
- **Alerts and Telegram.** Agent offline, container down/unhealthy, restart loops, CPU, disk, upcoming hosting payments, fail2ban bans, login attempts. Quiet hours, ack and silence. The bot answers `/status`, `/server`, `/alerts`, `/ack`, `/bills`, `/ask`.
- **Security.** Password (argon2id) + TOTP or backup code, no username. Rate limiting, an audit log and fail2ban jails for the panel and sshd.

## Components

| Part | Where | What it does |
| --- | --- | --- |
| `srvmon-server` | `cmd/server`, `internal/` | API, auth, SQLite store, alerts, Telegram bot, SSH gateway, embedded web UI |
| `srvmon-agent` | `cmd/agent`, `internal/agent` | Host and container metrics, push to the server |
| `srvmon-ai` | `ai/` | AI agent (pi SDK) with srvmon tools only, no SSH keys of its own |
| Web | `web/` | React + Vite + Tailwind 4, shadcn-style components, xterm.js |
| Deploy | `deploy/` | Distroless images (amd64 + arm64), compose: Caddy or external proxy, server, VictoriaMetrics, fail2ban |
| CI/CD | `.github/workflows/` | `ci.yml` checks, `release.yml` images to GHCR, `deploy.yml` SSH deploy with rollback |

## Quick start (manual install)

Requirements: a Debian/Ubuntu host with a public IP, a domain pointing to it, ports 80/443 (or your own reverse proxy). See [docs/server-requirements.md](docs/server-requirements.md).

```bash
git clone https://github.com/askidmobile/srvmon.git
cd srvmon
sudo bash deploy/install-host.sh            # installs docker if missing, creates /opt/srvmon
sudo cp -r deploy/docker-compose.yml deploy/Caddyfile deploy/remote-deploy.sh \
  deploy/install-host.sh deploy/fail2ban deploy/nginx /opt/srvmon/
sudo nano /opt/srvmon/.env                  # set SRVMON_DOMAIN=mon.example.com
cd /opt/srvmon && sudo bash remote-deploy.sh latest
sudo docker compose exec -it server /srvmon-server init   # password + QR code for the authenticator app
```

Open `https://<your domain>`, enter the password, then the TOTP code. The monitoring host itself appears in the panel automatically. Add other servers with `+ server` in the panel.

If the host already runs nginx (or another proxy) on 80/443, set `SRVMON_PROXY=external` in `.env`: Caddy is not started, srvmon listens on `127.0.0.1:8088` (`SRVMON_HTTP_BIND`), and you include [deploy/nginx/srvmon.conf](deploy/nginx/srvmon.conf) in your nginx.

Back up the `/data` volume together with `/data/master.key`: without the master key the stored SSH keys cannot be decrypted.

## Continuous deployment from your fork

1. Prepare the host as above (`install-host.sh`) and point the domain at it.
2. In GitHub: Settings → Environments → `production`:
   - secrets: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` (private deploy key), `DEPLOY_KNOWN_HOSTS` (output of `ssh-keyscan <host>`), optional `DEPLOY_PORT`, `GHCR_PULL_TOKEN`, `AI_API_KEY`;
   - variables: `SRVMON_DOMAIN`, optional `SRVMON_PROXY`, `SRVMON_HTTP_BIND`, `DEPLOY_PATH` (default `/opt/srvmon`).
   - Restrict the environment to the `main` branch (Deployment branches).
3. Every push to `main`: CI → Release (images `ghcr.io/<owner>/srvmon-server|agent|ai`, tags `sha-xxxxxxx` and `latest`) → Deploy (`docker compose pull && up -d`, waits for the healthcheck, rolls back to the previous tag on failure). Deploy is skipped until `DEPLOY_HOST` is set. Manual deploy of a tag: Actions → Deploy → Run workflow.
4. After the first deploy, once: `cd /opt/srvmon && docker compose exec -it server /srvmon-server init`.

In a fork, replace `ghcr.io/askidmobile/` with `ghcr.io/<your-owner>/` in `deploy/docker-compose.yml` (server, agent and ai images) and in `internal/config/config.go`: Release publishes `sha-*` tags only under your own namespace, and Deploy asks for exactly those tags. The upstream images are meant for the manual install above. If your agent image on GHCR is private, set `SRVMON_AGENT_REGISTRY_USER` and `SRVMON_AGENT_REGISTRY_TOKEN` (PAT with `read:packages`) in `.env` so the wizard can pull it.

## Adding servers

The add wizard (`+ server`, `#/add`):

1. Name, `host:port`, project, group, tags, description; optionally provider, plan, cost and next payment date.
2. "Check host key": srvmon connects to sshd without logging in and shows the fingerprint. Compare it on the server (`ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub`) and confirm; only this key is accepted afterwards.
3. User (root or with sudo) and a one-time login method: **password**, **my key** (your private key, used once and not saved), or **panel key** (the panel shows its public key and a command to add it to `authorized_keys`).
4. srvmon creates the `srvmon` user (docker group, optional passwordless sudo), installs its key, verifies key login, starts the `srvmon-agent` container with a token in `/etc/srvmon/agent.token` (0600) and optionally disables `PasswordAuthentication`.

Manually, without the wizard:

```bash
docker compose exec server /srvmon-server add-server -name shop-db -group prod -tags db,public
# on the target server, with deploy/agent/docker-compose.yml:
SRVMON_URL=https://mon.example.com SRVMON_TOKEN=a_... docker compose up -d
```

## Telegram alerts

1. Create a bot with [@BotFather](https://t.me/BotFather) (`/newbot`) and copy its token.
2. In the panel: alerts → telegram bot → paste the token → save.
3. Send `/start` to the bot, then click "use this chat" in the panel. For a group, add the bot and send `/start@your_bot` there.
4. "Send test".

If the monitoring server cannot reach `api.telegram.org` (Russian hosting, corporate networks), run a tiny Cloudflare Worker proxy and set `SRVMON_TELEGRAM_API`: see [docs/telegram-proxy.md](docs/telegram-proxy.md).

## AI agent

Enable it in the panel: settings → AI agent. Pick a provider (any from the pi catalog or a custom OpenAI/Anthropic-compatible URL), paste an API key (stored encrypted, never returned to the browser), load and select models, then "save and apply". Until something is saved in the panel, `srvmon-ai` uses `SRVMON_AI_PROVIDER`, `SRVMON_AI_MODEL` and `SRVMON_AI_API_KEY` from `.env`.

The agent can list servers and containers, run commands through the SSH gateway and run scheduled tasks. Commands that change a server show up as a confirmation card in the panel and in Telegram and run only after you approve them.

## Local development

```bash
make test                 # go test -race
make dev-server           # API on :8080, data in ./data, cookies without Secure
SRVMON_DATA_DIR=./data go run ./cmd/server init      # password + QR for the authenticator
SRVMON_DATA_DIR=./data go run ./cmd/server add-server -name local -group dev
make dev-web              # Vite on :5173, proxies /api to :8080
SRVMON_URL=http://127.0.0.1:8080 SRVMON_TOKEN=a_... go run ./cmd/agent
cd ai && npm ci && npm test   # AI agent tests with a fake model
```

Without `SRVMON_VM_URL` metrics are not written to VictoriaMetrics, but the latest snapshot is visible in the overview.

## Contributing and security

- How to contribute: [CONTRIBUTING.md](CONTRIBUTING.md)
- Reporting vulnerabilities: [SECURITY.md](SECURITY.md). Please do not open public issues for security problems.

## License

Licensed under either of [MIT](LICENSE-MIT) or [Apache-2.0](LICENSE-APACHE) at your option.

Unless you explicitly state otherwise, any contribution intentionally submitted for inclusion in the work by you, as defined in the Apache-2.0 license, shall be dual licensed as above, without any additional terms or conditions.

<a id="donate"></a>

## Support the project

If srvmon is useful to you, you can support its development. Donations pay for test servers and further work on the project.

Address for **USDT on the TON network**:

```text
UQAqmiUAf-kCo7pw1HM4KDrf4r8XCAsDDolODnZJnAydZ37O
```

> [!WARNING]
> Send only **USDT** and only on the **TON network**. TON coins, other tokens and transfers from other networks (TRC-20, ERC-20, BEP-20) sent to this address are not credited and may be lost.
