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

1. Подготовить сервер мониторинга (Debian/Ubuntu): скопировать `deploy/` и выполнить `sudo bash deploy/install-host.sh`. Скрипт ставит docker (если его нет) и создаёт `/opt/srvmon`. apt он не трогает: fail2ban работает контейнером в compose srvmon и ставится сам при деплое.
2. Направить домен на сервер.
3. В GitHub: Settings → Environments → `production`:
   - secrets: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` (приватный ключ для деплоя), `DEPLOY_KNOWN_HOSTS` (вывод `ssh-keyscan <host>`), опционально `DEPLOY_PORT`, `GHCR_PULL_TOKEN`;
   - variables: `SRVMON_DOMAIN` (или задать его в `/opt/srvmon/.env` на сервере), опционально `DEPLOY_PATH` (по умолчанию `/opt/srvmon`).
4. Каждый push в `main`: CI → Release (образы `ghcr.io/askidmobile/srvmon-server|agent`, теги `sha-xxxxxxx` и `latest`) → Deploy (`docker compose pull && up -d`, ждёт healthcheck, при провале откатывает на прошлый тег). Пока `DEPLOY_HOST` не задан, Deploy пропускается. Ручной деплой нужного тега: Actions → Deploy → Run workflow.
5. Если на сервере уже есть свой nginx (или другой прокси) на 80/443: в GitHub variables задать `SRVMON_PROXY=external`. Тогда Caddy не запускается, srvmon слушает `127.0.0.1:8088` (`SRVMON_HTTP_BIND`), а в nginx подключается шаблон `deploy/nginx/srvmon.conf` с сертификатом от вашего certbot. Цепочку бана fail2ban (`INPUT` в этом режиме, `DOCKER-USER` с Caddy) Deploy выставляет сам.
6. После первого деплоя один раз: `cd /opt/srvmon && docker compose exec -it server /srvmon-server init`.

### Агент на сервере

Сам сервер мониторинга появляется в панели автоматически: в его compose есть агент, `srvmon-server` при старте регистрирует хост под именем `SRVMON_SELF_NAME` (Deploy подставляет hostname) и кладёт токен в свой том.

Остальные серверы добавляются мастером в панели (`+ сервер`, `#/add`):

1. Имя, `host:port`, проект, группа, теги, описание; провайдер, тариф, стоимость, период, дата следующей оплаты (можно пропустить).
2. «Проверить host key»: srvmon подключается к sshd без входа и показывает fingerprint. Сверьте его на сервере (`ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub`) и подтвердите; дальше принимается только этот ключ.
3. Пользователь (root или с sudo) и разовый способ входа:
   - **пароль**;
   - **мой ключ**: свой приватный ключ (например `~/.ssh/id_ed25519`, не `.pub`), используется один раз и не сохраняется;
   - **ключ панели**: панель показывает свой публичный ключ и команду, которую нужно выполнить на сервере, чтобы добавить его в `authorized_keys` этого пользователя. После установки ключ остаётся только у `srvmon`.

   Для входа по ключу под не-root можно указать пароль sudo, если sudo его спрашивает.

   Дальше srvmon создаёт пользователя `srvmon` (группа docker, опционально sudo без пароля), кладёт ключ в `authorized_keys`, проверяет вход по ключу, запускает контейнер `srvmon-agent` с токеном в `/etc/srvmon/agent.token` (0600) и по желанию выключает `PasswordAuthentication`. Пароли и ключи для входа не пишутся ни в БД, ни в логи. При ошибке до проверки ключа карточка сервера удаляется.

Приватные ключи хранятся в SQLite зашифрованными (XChaCha20-Poly1305) мастер-ключом `/data/master.key` (`SRVMON_MASTER_KEY_FILE`). Его нужно бэкапить вместе с БД: без него ключи не расшифровать.

Уже заведённому серверу (например, самому серверу мониторинга) ssh-доступ подключается кнопкой `+ ssh` в обзоре: тот же мастер без паспорта, агент по умолчанию не переустанавливается.

Образы в GHCR приватные, как и репозиторий. Чтобы мастер мог поставить агента, либо сделайте пакет `srvmon-agent` публичным (в нём нет секретов), либо задайте в `/opt/srvmon/.env` `SRVMON_AGENT_REGISTRY_USER` и `SRVMON_AGENT_REGISTRY_TOKEN` (PAT с `read:packages`): токен используется для одного `docker pull` и сразу разлогинивается. Без этого мастер добавит сервер и ключ, а про агента напишет предупреждение.

Вручную, без мастера:

```bash
docker compose exec server /srvmon-server add-server -name shop-db -group prod -tags db,public
# на целевом сервере, с deploy/agent/docker-compose.yml:
SRVMON_URL=https://mon.example.com SRVMON_TOKEN=a_... docker compose up -d
```


### Алерты и Telegram

Экран `[5] алерты` (`#/alerts`): активные алерты с кнопками ack и «тишина 1ч», правила, Telegram-бот, тихие часы и история за 90 дней. Правила по умолчанию: агент молчит > 2 мин (crit), контейнер упал или unhealthy 1 мин (crit), рестартов > 2 за час, CPU > 90% 5 мин, диск > 85%, оплата через ≤ 3 дня, плюс события: бан fail2ban, подбор пароля к панели, вход в панель.

Бот:

1. В Telegram откройте [@BotFather](https://t.me/BotFather), отправьте `/newbot`, задайте имя и username (должен заканчиваться на `bot`). BotFather пришлёт токен вида `123456789:AA…`.
2. В панели: алерты → telegram-бот → вставьте токен → «сохранить». Панель проверит токен и подключится.
3. Напишите своему боту `/start`. В панели появится «боту написал …» — нажмите «использовать этот чат». Для группы добавьте бота в группу и напишите там `/start@имя_бота`.
4. «Отправить тест». Дальше бот отвечает только этому чату: `/status`, `/server <имя>`, `/alerts`, `/ack <id>`, `/bills`, `/ask <вопрос>`.

Баны fail2ban сервера мониторинга приходят алертами сами: контейнер `fail2ban` пишет их через action `srvmon-notify`. Если `api.telegram.org` с сервера недоступен, задайте в `.env` `SRVMON_TELEGRAM_API` с адресом прокси Bot API.

### Веб-терминал

`[3] терминал` (`#/term`, `#/term/<id>`): xterm.js ↔ WebSocket `/api/servers/<id>/terminal` ↔ SSH-клиент в `srvmon-server` ↔ sshd сервера. Вкладки, resize, shell в запущенный контейнер (`docker exec`), переподключение. Приватный ключ не покидает сервер мониторинга; WebSocket проверяет сессию и Origin, открытие и закрытие сессий пишутся в аудит, при истечении сессии панели терминал закрывается.

### AI-агент

Контейнер `srvmon-ai` (pi SDK, `ai/`) с инструментами srvmon: список серверов и контейнеров, команды на серверах через SSH-шлюз, задачи по расписанию. Панель агента — справа в `[3] терминал` (F9 показывает и прячет её) и отдельный раздел `[4] агент` (`#/agent`, `#/agent/<id>`) с журналом подтверждений.

- Команды только для чтения (`docker ps/logs/inspect`, `df`, `journalctl`, `systemctl status`, `cat | grep` …) выполняются сразу. Всё, что меняет сервер, появляется карточкой «⚠ подтвердите» и выполняется только после «✓ выполнить» (ждёт до 15 минут). Решает srvmon-server (`internal/aiagent/policy.go`), а не модель; у контейнера агента нет SSH-ключей.
- Задачи: «каждые 15 минут проверяй рестарты контейнеров, если > 2 за час — в телеграм» → задача с cron `*/15 * * * *` (UTC). Ответ, начинающийся с `ALERT`, или ошибка уходят в Telegram, когда подключён бот. Там же бот присылает команды агента на подтверждение с кнопками «выполнить» / «отклонить», а `/ask <вопрос>` спрашивает агента прямо из Telegram.

Включение — в панели, `[6] настройки` → «AI-агент» (`#/settings`):

1. Провайдер: любой из каталога pi (DeepSeek, Anthropic, OpenAI, OpenRouter, Google, Mistral, xAI, Groq, …) или «свой» — OpenAI- или Anthropic-совместимый адрес (Ollama, vLLM, LiteLLM, корпоративный шлюз). У провайдера из каталога можно задать свой адрес API (прокси).
2. Ключ API. Он хранится на сервере зашифрованным мастер-ключом, отдельно для каждого провайдера, и в браузер не возвращается.
3. «Загрузить список у провайдера» показывает модели, доступные по этому ключу. Отметьте модели, которые можно выбирать в чате, и звёздочкой — модель по умолчанию. Id модели можно ввести и вручную.
4. «Сохранить и применить» — srvmon-ai переключается сразу, без перезапуска. «Проверить связь» отправляет модели короткий запрос.

Пока в панели ничего не сохранено, `srvmon-ai` берёт настройки из переменных `SRVMON_AI_PROVIDER`, `SRVMON_AI_MODEL`, `SRVMON_AI_API_KEY` (секрет `AI_API_KEY` в окружении `production`, если он задан). После первого сохранения в панели они не используются.

Токен между `server` и `ai` (`SRVMON_AI_TOKEN`) генерируется при деплое. Tool API агента слушает `:8081` только внутри compose-сети и не проксируется наружу.

Локально: `cd ai && npm ci && npm test` (тест с фейковой моделью pi), запуск — `SRVMON_AI_TOKEN=... SRVMON_TOOLS_URL=http://127.0.0.1:8081 SRVMON_AI_DATA=./data npm start` и `srvmon-server` с `SRVMON_AI_URL=http://127.0.0.1:8090 SRVMON_AI_TOKEN=...`.
