# srvmon

[English](README.md) · **Русский** · [中文](README.zh-CN.md)

Self-hosted панель мониторинга своих серверов: метрики и docker-контейнеры, SSH-терминал в браузере, AI-агент, который работает на серверах с вашего подтверждения, и алерты в Telegram. Вход по паролю + код TOTP (Google Authenticator и совместимые), подбор блокирует fail2ban.

Интерфейс в стиле TUI (как btop), тёмная тема с зелёным акцентом. Интерфейс на английском (по умолчанию), русском и китайском; язык переключается в настройках и действует также на сообщения Telegram-бота и ответы AI-агента.

- Архитектура: [docs/architecture.md](docs/architecture.md)
- Макеты: [docs/design](docs/design)
- Требования к серверу мониторинга: [docs/server-requirements.md](docs/server-requirements.md)
- Telegram-бот, если `api.telegram.org` недоступен (хостинг в России): [docs/telegram-proxy.md](docs/telegram-proxy.md)

## Возможности

- **Метрики и контейнеры.** Небольшой агент на Go (`srvmon-agent`) на каждом сервере читает CPU, RAM, swap, load, диски, сеть и uptime из `/proc`, контейнеры через docker.sock (только чтение) и отправляет их в панель. При потере связи буферизует данные до часа. Метрики хранятся в VictoriaMetrics.
- **Экран сервера.** CPU по ядрам, память, диски, сеть, процессы, контейнеры со статусом, health и рестартами, логи syslog и docker; всё обновляется, пока экран открыт.
- **Мастер добавления.** Проверка host key, разовый вход по паролю или своему ключу. srvmon создаёт пользователя `srvmon` со своим ed25519-ключом, ставит агента и по желанию выключает вход по паролю. Разовый пароль или ключ нигде не сохраняется.
- **Веб-терминал.** xterm.js ↔ WebSocket ↔ SSH-клиент в `srvmon-server` ↔ sshd. Вкладки, resize, `docker exec` в контейнер. Приватные ключи не покидают сервер мониторинга и хранятся зашифрованными (XChaCha20-Poly1305).
- **AI-агент.** На SDK [pi](https://pi.dev), любой провайдер (DeepSeek, Anthropic, OpenAI, OpenRouter, Google, Ollama, любой OpenAI- или Anthropic-совместимый адрес). Команды только для чтения выполняются сразу, всё, что меняет сервер, ждёт вашего подтверждения. Решает `srvmon-server`, а не модель. Задачи по расписанию (cron) отчитываются в Telegram.
- **Алерты и Telegram.** Агент молчит, контейнер упал или unhealthy, рестарты, CPU, диск, скорая оплата хостинга, баны fail2ban, попытки входа. Тихие часы, ack и «тишина». Бот отвечает на `/status`, `/server`, `/alerts`, `/ack`, `/bills`, `/ask`.
- **Безопасность.** Пароль (argon2id) + TOTP или резервный код, без логина. Rate limit, журнал аудита, jail'ы fail2ban для панели и sshd.

## Состав

| Часть | Где | Что умеет |
| --- | --- | --- |
| `srvmon-server` | `cmd/server`, `internal/` | API, вход, SQLite, алерты, Telegram-бот, SSH-шлюз, встроенный фронт |
| `srvmon-agent` | `cmd/agent`, `internal/agent` | Метрики хоста и контейнеров, push на сервер |
| `srvmon-ai` | `ai/` | AI-агент (pi SDK) только с инструментами srvmon, без своих SSH-ключей |
| Веб | `web/` | React + Vite + Tailwind 4, компоненты в стиле shadcn/ui, xterm.js |
| Деплой | `deploy/` | Distroless-образы (amd64 + arm64), compose: Caddy или внешний прокси, server, VictoriaMetrics, fail2ban |
| CI/CD | `.github/workflows/` | `ci.yml` проверки, `release.yml` образы в GHCR, `deploy.yml` выкатка по SSH с откатом |

## Быстрый старт (ручная установка)

Нужен хост на Debian/Ubuntu с публичным IP, домен с A-записью на него и порты 80/443 (или свой reverse proxy). Подробнее: [docs/server-requirements.md](docs/server-requirements.md).

```bash
git clone https://github.com/askidmobile/srvmon.git
cd srvmon
sudo bash deploy/install-host.sh            # ставит docker, если его нет, создаёт /opt/srvmon
sudo cp -r deploy/docker-compose.yml deploy/Caddyfile deploy/remote-deploy.sh \
  deploy/install-host.sh deploy/fail2ban deploy/nginx /opt/srvmon/
sudo nano /opt/srvmon/.env                  # SRVMON_DOMAIN=mon.example.com
cd /opt/srvmon && sudo bash remote-deploy.sh latest
sudo docker compose exec -it server /srvmon-server init   # пароль + QR для Authenticator
```

Откройте `https://<ваш домен>`, введите пароль, затем код. Сам сервер мониторинга появится в панели автоматически, остальные добавляются кнопкой `+ сервер`.

Если на хосте уже есть nginx (или другой прокси) на 80/443, задайте в `.env` `SRVMON_PROXY=external`. Тогда Caddy не запускается, srvmon слушает `127.0.0.1:8088` (`SRVMON_HTTP_BIND`), а в nginx подключается шаблон [deploy/nginx/srvmon.conf](deploy/nginx/srvmon.conf) с сертификатом от вашего certbot. Цепочку бана fail2ban (`INPUT` в этом режиме, `DOCKER-USER` с Caddy) `remote-deploy.sh` выставляет сам.

Бэкапьте том `/data` вместе с `/data/master.key` (`SRVMON_MASTER_KEY_FILE`): без мастер-ключа сохранённые SSH-ключи не расшифровать.

## Автодеплой из своего форка

1. Подготовить хост как выше (`install-host.sh`) и направить на него домен.
2. В GitHub: Settings → Environments → `production`:
   - secrets: `DEPLOY_HOST`, `DEPLOY_USER`, `DEPLOY_SSH_KEY` (приватный ключ для деплоя), `DEPLOY_KNOWN_HOSTS` (вывод `ssh-keyscan <host>`), опционально `DEPLOY_PORT`, `GHCR_PULL_TOKEN`, `AI_API_KEY`;
   - variables: `SRVMON_DOMAIN`, опционально `SRVMON_PROXY`, `SRVMON_HTTP_BIND`, `DEPLOY_PATH` (по умолчанию `/opt/srvmon`);
   - ограничьте окружение веткой `main` (Deployment branches).
3. Каждый push в `main`: CI → Release (образы `ghcr.io/<owner>/srvmon-server|agent|ai`, теги `sha-xxxxxxx` и `latest`) → Deploy (`docker compose pull && up -d`, ждёт healthcheck, при провале откатывает на прошлый тег). Пока `DEPLOY_HOST` не задан, Deploy пропускается. Ручной деплой нужного тега: Actions → Deploy → Run workflow.
4. После первого деплоя один раз: `cd /opt/srvmon && docker compose exec -it server /srvmon-server init`.

В форке замените `ghcr.io/askidmobile/` на `ghcr.io/<ваш-owner>/` в `deploy/docker-compose.yml` (образы server, agent и ai) и в `internal/config/config.go`: Release публикует теги `sha-*` только в вашем пространстве имён, а Deploy запрашивает именно их. Образы основного репозитория предназначены для ручной установки выше. Если ваш образ агента в GHCR приватный, задайте в `.env` `SRVMON_AGENT_REGISTRY_USER` и `SRVMON_AGENT_REGISTRY_TOKEN` (PAT с `read:packages`): токен используется для одного `docker pull` и сразу разлогинивается.

Обновление агентов: панель ставит образ агента своего релиза (`SRVMON_AGENT_IMAGE`, при деплое закреплён на развёрнутом теге). Если агент сообщает другую версию, панель один раз на релиз заменяет его контейнер по SSH (`SRVMON_AGENT_AUTOUPDATE=false` отключает это); обзор и паспорт сервера помечают устаревшие агенты и дают кнопки обновления. Агенты, поставленные вручную через `deploy/agent/docker-compose.yml`, обновляются там же: `docker compose pull && docker compose up -d`.

## Добавление серверов

Мастер в панели (`+ сервер`, `#/add`):

1. Имя, `host:port`, проект, группа, теги, описание; провайдер, тариф, стоимость, период, дата следующей оплаты (можно пропустить).
2. «Проверить host key»: srvmon подключается к sshd без входа и показывает fingerprint. Сверьте его на сервере (`ssh-keygen -lf /etc/ssh/ssh_host_ecdsa_key.pub`) и подтвердите; дальше принимается только этот ключ.
3. Пользователь (root или с sudo) и разовый способ входа:
   - **пароль**;
   - **мой ключ**: свой приватный ключ (например `~/.ssh/id_ed25519`, не `.pub`), используется один раз и не сохраняется;
   - **ключ панели**: панель показывает свой публичный ключ и команду, которую нужно выполнить на сервере, чтобы добавить его в `authorized_keys`.

   Для входа по ключу под не-root можно указать пароль sudo, если sudo его спрашивает.
4. srvmon создаёт пользователя `srvmon` (группа docker, опционально sudo без пароля), кладёт ключ в `authorized_keys`, проверяет вход по ключу, запускает контейнер `srvmon-agent` с токеном в `/etc/srvmon/agent.token` (0600) и по желанию выключает `PasswordAuthentication`. Пароли и ключи для входа не пишутся ни в БД, ни в логи. При ошибке до проверки ключа карточка сервера удаляется.

Уже заведённому серверу (например, самому серверу мониторинга) ssh-доступ подключается кнопкой `+ ssh` в обзоре.

Вручную, без мастера:

```bash
docker compose exec server /srvmon-server add-server -name shop-db -group prod -tags db,public
# на целевом сервере, с deploy/agent/docker-compose.yml:
SRVMON_URL=https://mon.example.com SRVMON_TOKEN=a_... docker compose up -d
```

## Алерты и Telegram

Экран `[5] алерты` (`#/alerts`): активные алерты с кнопками ack и «тишина 1ч», правила, Telegram-бот, тихие часы и история за 90 дней. Правила по умолчанию: агент молчит > 2 мин (crit), контейнер упал или unhealthy 1 мин (crit), рестартов > 2 за час, CPU > 90% 5 мин, диск > 85%, оплата через ≤ 3 дня, плюс события: бан fail2ban, подбор пароля к панели, вход в панель.

Бот:

1. В Telegram откройте [@BotFather](https://t.me/BotFather), отправьте `/newbot`, задайте имя и username (должен заканчиваться на `bot`). BotFather пришлёт токен вида `123456789:AA…`.
2. В панели: алерты → telegram-бот → вставьте токен → «сохранить».
3. Напишите своему боту `/start`. В панели появится «боту написал …», нажмите «использовать этот чат». Для группы добавьте бота в группу и напишите там `/start@имя_бота`.
4. «Отправить тест». Дальше бот отвечает только этому чату.

**Сервер в России?** `api.telegram.org` с российских хостингов часто недоступен. Поднимите бесплатный прокси на Cloudflare Workers и укажите его в `SRVMON_TELEGRAM_API`: пошаговая инструкция в [docs/telegram-proxy.md](docs/telegram-proxy.md).

## AI-агент

Контейнер `srvmon-ai` (pi SDK, `ai/`) с инструментами srvmon: список серверов и контейнеров, команды на серверах через SSH-шлюз, задачи по расписанию. Панель агента справа в `[3] терминал` (F9 показывает и прячет её) и отдельный раздел `[4] агент` с журналом подтверждений.

- Команды только для чтения (`docker ps/logs/inspect`, `df`, `journalctl`, `systemctl status` …) выполняются сразу. Всё, что меняет сервер, появляется карточкой «⚠ подтвердите» и выполняется только после «✓ выполнить». Решает `srvmon-server` (`internal/aiagent/policy.go`), а не модель; у контейнера агента нет SSH-ключей.
- Задачи: «каждые 15 минут проверяй рестарты контейнеров, если > 2 за час — в телеграм» превращаются в задачу с cron `*/15 * * * *` (UTC). Бот присылает команды агента на подтверждение с кнопками, а `/ask <вопрос>` спрашивает агента прямо из Telegram.

Включение: `[6] настройки` → «AI-агент». Выберите провайдера (из каталога pi или «свой» OpenAI/Anthropic-совместимый адрес), вставьте ключ API (хранится зашифрованным и в браузер не возвращается), загрузите и отметьте модели, «Сохранить и применить». Пока в панели ничего не сохранено, `srvmon-ai` берёт `SRVMON_AI_PROVIDER`, `SRVMON_AI_MODEL`, `SRVMON_AI_API_KEY` из `.env`.

## Локальная разработка

```bash
make test                 # go test -race
make dev-server           # API на :8080, данные в ./data, cookie без Secure
SRVMON_DATA_DIR=./data go run ./cmd/server init      # пароль + QR для Authenticator
SRVMON_DATA_DIR=./data go run ./cmd/server add-server -name local -group dev
make dev-web              # Vite на :5173, проксирует /api на :8080
SRVMON_URL=http://127.0.0.1:8080 SRVMON_TOKEN=a_... go run ./cmd/agent
cd ai && npm ci && npm test   # тесты AI-агента с фейковой моделью
```

Без `SRVMON_VM_URL` метрики не пишутся в VictoriaMetrics, но последний снимок виден в обзоре.

## Участие и безопасность

- Как прислать изменения: [CONTRIBUTING.md](CONTRIBUTING.md)
- Об уязвимостях сообщайте по [SECURITY.md](SECURITY.md), не в публичных issues.

## Лицензия

Код распространяется по двойной лицензии на выбор: [MIT](LICENSE-MIT) или [Apache-2.0](LICENSE-APACHE) (`MIT OR Apache-2.0`).

Если явно не указано иное, любой вклад, намеренно отправленный для включения в проект, распространяется на условиях той же двойной лицензии без дополнительных условий.

<a id="donate"></a>

## Поддержать проект

Если srvmon вам пригодился, его можно поддержать. Донаты идут на тестовые серверы и дальнейшее развитие проекта.

Адрес для перевода **USDT в сети TON**:

```text
UQAqmiUAf-kCo7pw1HM4KDrf4r8XCAsDDolODnZJnAydZ37O
```

> [!WARNING]
> Отправляйте только **USDT** и только в **сети TON**. Монеты TON, другие токены и переводы из других сетей (TRC-20, ERC-20, BEP-20) на этот адрес не зачисляются — средства можно потерять.
