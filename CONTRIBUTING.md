# Contributing to srvmon

Thanks for your interest! Issues and pull requests are welcome in English or Russian.

## Reporting bugs and ideas

Open an [issue](https://github.com/askidmobile/srvmon/issues) with:

- what you did, what you expected and what happened;
- srvmon version (image tag or commit), host OS, docker version, proxy mode (`SRVMON_PROXY`);
- relevant logs: `docker compose logs --tail=100 server` (and `agent` / `ai` if related).

Remove domains, IP addresses, tokens and keys before posting. Security problems go through [SECURITY.md](SECURITY.md), not public issues.

## Development setup

Requirements: Go (version from `go.mod`), Node.js 22+, docker for images.

```bash
make test        # go test -race ./...
make lint        # gofmt, go vet, typecheck of web/ and ai/
make dev-server  # API on :8080 with data in ./data
make dev-web     # Vite on :5173, proxies /api to :8080
cd ai && npm ci && npm test
```

See the README for running an agent against a local server.

## Pull requests

- One change per PR; describe what and why, and how you tested it.
- `make lint` and `make test` must pass; CI runs the same checks.
- Add or update tests for behaviour changes, especially in auth, the SSH gateway and the AI command policy (`internal/aiagent/policy.go`).
- Keep the UI in the existing TUI style (see `docs/design`).
- User-facing text is written in English in the code and translated through dictionaries keyed by that English text: `t("…")` / `tn(…)` with `web/src/i18n/<lang>.ts` in the panel, `i18n.T("…")` / `i18n.N(…)` with `internal/i18n/<lang>.go` on the server. Add the Russian and Chinese translation with every new string (a missing one falls back to English). Prompts and tool texts for the AI agent stay in English; the agent replies in the panel language.
- Don't commit secrets, real hostnames or IP addresses; use `example.com` and documentation IPs (`192.0.2.0/24`, `203.0.113.0/24`).

## License of contributions

Unless you explicitly state otherwise, any contribution intentionally submitted for inclusion in the work by you, as defined in the Apache-2.0 license, shall be dual licensed under [MIT](LICENSE-MIT) or [Apache-2.0](LICENSE-APACHE), without any additional terms or conditions.
