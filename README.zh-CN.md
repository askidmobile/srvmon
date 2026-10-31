# srvmon

[English](README.md) · [Русский](README.ru.md) · **中文**

自托管的服务器监控面板：主机指标与 Docker 容器、浏览器内的 SSH 终端、经你确认后才在服务器上执行操作的 AI 代理，以及 Telegram 告警。登录方式为密码 + TOTP 动态码（Google Authenticator 及兼容应用），并由 fail2ban 防护暴力破解。

界面为深色 TUI 风格（类似 btop），绿色强调色。目前界面语言为俄语。

- 架构：[docs/architecture.md](docs/architecture.md)（俄语）
- 设计稿：[docs/design](docs/design)
- 监控服务器配置要求：[docs/server-requirements.md](docs/server-requirements.md)（俄语）
- 无法访问 `api.telegram.org` 时的 Telegram 代理：[docs/telegram-proxy.md](docs/telegram-proxy.md)（俄语）

## 功能

- **指标与容器。** 每台服务器上运行一个小型 Go 代理（`srvmon-agent`），从 `/proc` 读取 CPU、内存、swap、负载、磁盘、网络和运行时间，通过 docker socket（只读）读取容器信息并推送到面板。面板不可达时最多缓存一小时。指标存储在 VictoriaMetrics 中。
- **服务器详情页。** 按核心的 CPU、内存、磁盘、网络、进程、容器状态/健康检查/重启次数、syslog 与 docker 日志，页面打开期间实时刷新。
- **添加向导。** 校验 SSH 主机密钥，用密码或你自己的私钥登录一次。srvmon 创建 `srvmon` 用户及其 ed25519 密钥，安装代理，并可关闭密码登录。一次性密码或密钥不会被保存。
- **Web 终端。** xterm.js ↔ WebSocket ↔ `srvmon-server` 内的 SSH 客户端 ↔ sshd。支持多标签、窗口缩放、`docker exec` 进入容器。私钥从不离开监控服务器，并以 XChaCha20-Poly1305 加密存储。
- **AI 代理。** 基于 [pi](https://pi.dev) SDK，支持任意提供商（DeepSeek、Anthropic、OpenAI、OpenRouter、Google、Ollama，以及任何 OpenAI / Anthropic 兼容接口）。只读命令直接执行；任何会修改服务器的命令都必须经你明确批准，策略由 `srvmon-server` 而非模型执行。定时任务（cron）结果可发送到 Telegram。
- **告警与 Telegram。** 代理离线、容器停止或不健康、频繁重启、CPU、磁盘、主机续费提醒、fail2ban 封禁、登录尝试。支持静默时段、确认与临时静音。机器人命令：`/status`、`/server`、`/alerts`、`/ack`、`/bills`、`/ask`。
- **安全。** 密码（argon2id）+ TOTP 或备用码，无需用户名。限流、审计日志，以及面板和 sshd 的 fail2ban 规则。

## 组件

| 组件 | 位置 | 作用 |
| --- | --- | --- |
| `srvmon-server` | `cmd/server`、`internal/` | API、认证、SQLite、告警、Telegram 机器人、SSH 网关、内嵌 Web 界面 |
| `srvmon-agent` | `cmd/agent`、`internal/agent` | 主机与容器指标，推送到服务器 |
| `srvmon-ai` | `ai/` | AI 代理（pi SDK），只能使用 srvmon 工具，自身没有 SSH 密钥 |
| Web | `web/` | React + Vite + Tailwind 4，shadcn 风格组件，xterm.js |
| 部署 | `deploy/` | Distroless 镜像（amd64 + arm64），compose：Caddy 或外部代理、server、VictoriaMetrics、fail2ban |
| CI/CD | `.github/workflows/` | `ci.yml` 检查，`release.yml` 推送镜像到 GHCR，`deploy.yml` 通过 SSH 部署并支持回滚 |

## 快速开始（手动安装）

需要：一台带公网 IP 的 Debian/Ubuntu 主机、指向它的域名、80/443 端口（或你自己的反向代理）。

```bash
git clone https://github.com/askidmobile/srvmon.git
cd srvmon
sudo bash deploy/install-host.sh            # 如未安装则安装 docker，并创建 /opt/srvmon
sudo cp -r deploy/docker-compose.yml deploy/Caddyfile deploy/remote-deploy.sh \
  deploy/install-host.sh deploy/fail2ban deploy/nginx /opt/srvmon/
sudo nano /opt/srvmon/.env                  # 设置 SRVMON_DOMAIN=mon.example.com
cd /opt/srvmon && sudo bash remote-deploy.sh latest
sudo docker compose exec -it server /srvmon-server init   # 设置密码并显示身份验证器二维码
```

打开 `https://<你的域名>`，输入密码，再输入动态码。监控主机本身会自动出现在面板中，其他服务器通过面板中的 `+ сервер`（添加服务器）按钮添加。

如果主机上已有 nginx（或其他代理）占用 80/443，在 `.env` 中设置 `SRVMON_PROXY=external`：此时不启动 Caddy，srvmon 监听 `127.0.0.1:8088`（`SRVMON_HTTP_BIND`），在你的 nginx 中引入 [deploy/nginx/srvmon.conf](deploy/nginx/srvmon.conf)。

请将 `/data` 卷与 `/data/master.key` 一同备份：没有主密钥就无法解密已保存的 SSH 密钥。

## 从你的 fork 自动部署

1. 按上文准备主机（`install-host.sh`）并解析域名。
2. 在 GitHub：Settings → Environments → `production`：
   - secrets：`DEPLOY_HOST`、`DEPLOY_USER`、`DEPLOY_SSH_KEY`（部署私钥）、`DEPLOY_KNOWN_HOSTS`（`ssh-keyscan <host>` 的输出），可选 `DEPLOY_PORT`、`GHCR_PULL_TOKEN`、`AI_API_KEY`；
   - variables：`SRVMON_DOMAIN`，可选 `SRVMON_PROXY`、`SRVMON_HTTP_BIND`、`DEPLOY_PATH`（默认 `/opt/srvmon`）；
   - 将该环境限制为仅 `main` 分支可部署（Deployment branches）。
3. 每次推送到 `main`：CI → Release（镜像 `ghcr.io/<owner>/srvmon-server|agent|ai`，标签 `sha-xxxxxxx` 和 `latest`）→ Deploy（`docker compose pull && up -d`，等待健康检查，失败则回滚到上一个标签）。未设置 `DEPLOY_HOST` 时跳过部署。
4. 首次部署后执行一次：`cd /opt/srvmon && docker compose exec -it server /srvmon-server init`。

在 fork 中，必须将 `deploy/docker-compose.yml`（server、agent、ai 三个镜像）和 `internal/config/config.go` 中的 `ghcr.io/askidmobile/` 改为 `ghcr.io/<你的 owner>/`：Release 只会在你自己的命名空间下发布 `sha-*` 标签，而 Deploy 正是拉取这些标签。上游镜像适用于上文的手动安装。

## 添加服务器

面板中的向导（`#/add`）：填写名称、`host:port`、项目、分组、标签；校验并确认主机密钥指纹；选择一次性登录方式（密码、你自己的私钥，或把面板公钥加入 `authorized_keys`）。随后 srvmon 创建 `srvmon` 用户、验证密钥登录、以 `/etc/srvmon/agent.token`（0600）中的令牌启动 `srvmon-agent` 容器，并可选择关闭 `PasswordAuthentication`。

不使用向导时：

```bash
docker compose exec server /srvmon-server add-server -name shop-db -group prod -tags db,public
# 在目标服务器上，使用 deploy/agent/docker-compose.yml：
SRVMON_URL=https://mon.example.com SRVMON_TOKEN=a_... docker compose up -d
```

## Telegram 告警

1. 通过 [@BotFather](https://t.me/BotFather)（`/newbot`）创建机器人并复制令牌。
2. 在面板的告警页面粘贴令牌并保存。
3. 向机器人发送 `/start`，然后在面板中选择该聊天。
4. 发送测试消息。

如果监控服务器无法访问 `api.telegram.org`，可以用 Cloudflare Worker 搭一个代理并设置 `SRVMON_TELEGRAM_API`，见 [docs/telegram-proxy.md](docs/telegram-proxy.md)。

## AI 代理

在面板设置中启用：选择提供商（pi 目录中的任意一个或自定义兼容地址），填入 API 密钥（加密存储，不会返回给浏览器），加载并勾选模型，然后保存并应用。在面板保存设置之前，`srvmon-ai` 使用 `.env` 中的 `SRVMON_AI_PROVIDER`、`SRVMON_AI_MODEL`、`SRVMON_AI_API_KEY`。

## 本地开发

```bash
make test                 # go test -race
make dev-server           # API 监听 :8080，数据位于 ./data
SRVMON_DATA_DIR=./data go run ./cmd/server init
make dev-web              # Vite 监听 :5173，将 /api 代理到 :8080
cd ai && npm ci && npm test
```

## 贡献与安全

- 贡献指南：[CONTRIBUTING.md](CONTRIBUTING.md)
- 安全漏洞请按 [SECURITY.md](SECURITY.md) 私下报告，不要提交公开 issue。

## 许可证

本项目采用双许可证，任选其一：[MIT](LICENSE-MIT) 或 [Apache-2.0](LICENSE-APACHE)。

除非你明确声明，否则你有意提交并纳入本项目的任何贡献（按 Apache-2.0 许可证的定义）均按上述双许可证授权，不附加任何其他条款或条件。

<a id="donate"></a>

## 支持本项目

如果 srvmon 对你有帮助，欢迎支持项目的发展。捐赠将用于测试服务器和后续开发。

**USDT（TON 网络）** 收款地址：

```text
UQAqmiUAf-kCo7pw1HM4KDrf4r8XCAsDDolODnZJnAydZ37O
```

> [!WARNING]
> 只能发送 **USDT**，且只能通过 **TON 网络**。TON 币、其他代币以及来自其他网络（TRC-20、ERC-20、BEP-20）的转账不会到账，资金可能丢失。
