# srvmon

Личная панель мониторинга серверов: метрики и docker-контейнеры, SSH-терминал в браузере, AI-агент, алерты в Telegram. Вход по паролю + TOTP.

- Архитектура: [docs/architecture.md](docs/architecture.md)
- Макеты: [docs/design](docs/design)
- Требования к серверу мониторинга: [docs/server-requirements.md](docs/server-requirements.md)

## Что уже есть (этап 1: каркас и CI/CD)

| Часть | Где | Что умеет |
| --- | --- | --- |
| `srvmon-server` | `cmd/server`, `internal/` | вход пароль (argon2id) → TOTP или резервный код, сессии в SQLite, rate limit + лог для fail2ban, `POST /api/ingest` → VictoriaMetrics, `GET /api/servers`, встроенный фронт |
| `srvmon-agent` | `cmd/agent`, `internal/agent` | CPU, RAM, swap, load, диски, сеть, uptime из `/proc`, контейнеры через docker.sock (ro), буфер на час при потере связи |
| Веб | `web/` | React + Vite + Tailwind 4, компоненты в стиле shadcn/ui в TUI-теме: экран входа и обзор серверов |
| Деплой | `deploy/` | Dockerfile'ы (distroless, amd64 + arm64), compose: Caddy + server + VictoriaMetrics, fail2ban jail, compose для агента |
| CI/CD | `.github/workflows/` | `ci.yml` проверки, `release.yml` образы в GHCR, `deploy.yml` выкатка по SSH с откатом |

## Локальная разработка

```bash
make test                 # go test -race
make dev-server           # API на :8080, данные в ./data, cookie без Secure
SRVMON_DATA_DIR=./data go run ./cmd/server init      # пароль + QR для Authenticator
SRVMON_DATA_DIR=./data go run ./cmd/server add-server -name local -group dev
make dev-web              # Vite на :5173, проксирует /api на :8080
SRVMON_URL=http://127.0.0.1:8080 SRVMON_TOKEN=a_... go run ./cmd/agent
```

Без `SRVMON_VM_URL` метрики не пишутся в VictoriaMetrics, но последний снимок виден в обзоре.

## Деплой

1. Подготовить сервер мониторинга (Debian/Ubuntu): скопировать `deploy/` и выполнить `sudo bash deploy/install-host.sh`. Скрипт ставит docker, fail2ban с jail `srvmon-auth` и создаёт `/opt/srvmon`.
2. Направить домен на сервер.
3. В GitHub: Settings → Environments → `production`:
   - secrets: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` (приватный ключ для деплоя), `DEPLOY_KNOWN_HOSTS` (вывод `ssh-keyscan <host>`), опционально `DEPLOY_PORT`, `GHCR_PULL_TOKEN`;
   - variables: `SRVMON_DOMAIN` (или задать его в `/opt/srvmon/.env` на сервере), опционально `DEPLOY_PATH` (по умолчанию `/opt/srvmon`).
4. Каждый push в `main`: CI → Release (образы `ghcr.io/askidmobile/srvmon-server|agent`, теги `sha-xxxxxxx` и `latest`) → Deploy (`docker compose pull && up -d`, ждёт healthcheck, при провале откатывает на прошлый тег). Пока `DEPLOY_HOST` не задан, Deploy пропускается. Ручной деплой нужного тега: Actions → Deploy → Run workflow.
5. Если на сервере уже есть свой nginx (или другой прокси) на 80/443: в GitHub variables задать `SRVMON_PROXY=external`. Тогда Caddy не запускается, srvmon слушает `127.0.0.1:8088` (`SRVMON_HTTP_BIND`), а в nginx подключается шаблон `deploy/nginx/srvmon.conf` с сертификатом от вашего certbot. `install-host.sh` в этом режиме переводит бан fail2ban в цепочку `INPUT`, поэтому после смены `SRVMON_PROXY` его нужно запустить ещё раз (`sudo bash /opt/srvmon/install-host.sh`); Deploy напомнит предупреждением, если цепочка не совпадает.
6. После первого деплоя один раз: `cd /opt/srvmon && docker compose exec -it server /srvmon-server init`.

### Агент на сервере

Сам сервер мониторинга появляется в панели автоматически: в его compose есть агент, `srvmon-server` при старте регистрирует хост под именем `SRVMON_SELF_NAME` (Deploy подставляет hostname) и кладёт токен в свой том.

Остальные серверы добавляются мастером в панели (`+ сервер`, `#/add`):

1. Имя, `host:port`, проект, группа, теги, описание; провайдер, тариф, стоимость, период, дата следующей оплаты (можно пропустить).
2. «Проверить host key»: srvmon подключается к sshd без входа и показывает fingerprint. Сверьте его на сервере (`ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub`) и подтвердите; дальше принимается только этот ключ.
3. Пользователь (root или с sudo) и пароль один раз. srvmon заходит по паролю, генерирует ed25519-ключ, создаёт пользователя `srvmon` (группа docker, опционально sudo без пароля), кладёт ключ в `authorized_keys`, проверяет вход по ключу, запускает контейнер `srvmon-agent` с токеном в `/etc/srvmon/agent.token` (0600) и по желанию выключает `PasswordAuthentication`. Пароль не пишется ни в БД, ни в логи. При ошибке до проверки ключа карточка сервера удаляется.

Приватные ключи хранятся в SQLite зашифрованными (XChaCha20-Poly1305) мастер-ключом `/data/master.key` (`SRVMON_MASTER_KEY_FILE`). Его нужно бэкапить вместе с БД: без него ключи не расшифровать.

Уже заведённому серверу (например, самому серверу мониторинга) ssh-доступ подключается кнопкой `+ ssh` в обзоре: тот же мастер без паспорта, агент по умолчанию не переустанавливается.

Образы в GHCR приватные, как и репозиторий. Чтобы мастер мог поставить агента, либо сделайте пакет `srvmon-agent` публичным (в нём нет секретов), либо задайте в `/opt/srvmon/.env` `SRVMON_AGENT_REGISTRY_USER` и `SRVMON_AGENT_REGISTRY_TOKEN` (PAT с `read:packages`): токен используется для одного `docker pull` и сразу разлогинивается. Без этого мастер добавит сервер и ключ, а про агента напишет предупреждение.

Вручную, без мастера:

```bash
docker compose exec server /srvmon-server add-server -name shop-db -group prod -tags db,public
# на целевом сервере, с deploy/agent/docker-compose.yml:
SRVMON_URL=https://mon.example.com SRVMON_TOKEN=a_... docker compose up -d
```

### Веб-терминал

`[3] терминал` (`#/term`, `#/term/<id>`): xterm.js ↔ WebSocket `/api/servers/<id>/terminal` ↔ SSH-клиент в `srvmon-server` ↔ sshd сервера. Вкладки, resize, shell в запущенный контейнер (`docker exec`), переподключение. Приватный ключ не покидает сервер мониторинга; WebSocket проверяет сессию и Origin, открытие и закрытие сессий пишутся в аудит, при истечении сессии панели терминал закрывается.
