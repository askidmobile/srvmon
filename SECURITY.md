# Security policy

srvmon holds SSH keys to your servers and can run commands on them, so security reports are taken seriously.

## Supported versions

Only the latest commit on `main` (and the `latest` / most recent `sha-*` images on GHCR) receives fixes. Please update before reporting.

## Reporting a vulnerability

**Do not open a public issue for security problems.**

Report privately through GitHub: **Security → Report a vulnerability** on this repository ([private vulnerability reporting](https://github.com/askidmobile/srvmon/security/advisories/new)).

Please include:

- affected component (`srvmon-server`, `srvmon-agent`, `srvmon-ai`, web UI, deploy scripts) and version or commit;
- steps to reproduce or a proof of concept;
- impact as you see it (auth bypass, key disclosure, command execution, etc.).

Remove real hostnames, IP addresses, tokens and keys from logs and screenshots.

You should get a first answer within 7 days. After a fix is released the advisory is published with credit to the reporter, unless you ask otherwise.

## Scope

In scope: authentication (password, TOTP, sessions, rate limiting), the SSH gateway and web terminal, storage of SSH keys and API keys, the agent ingest API, the AI agent command approval policy, Telegram bot command handling, and the deploy scripts.

Out of scope: vulnerabilities in third-party services (Telegram, LLM providers, Cloudflare), issues that require an already compromised monitoring server, and missing hardening of the host you deploy srvmon on.

## Hardening tips

- Keep the panel behind HTTPS (bundled Caddy or your own proxy) and make sure the real client IP reaches srvmon, otherwise rate limiting and fail2ban see every client as one address.
- Back up `/data/master.key` separately from the database and restrict access to it.
- Restrict the GitHub `production` environment to the `main` branch if you use the deploy workflow.
