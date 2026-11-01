# srvmon

[English](README.md) · [Русский](README.ru.md) · **中文**

自托管的服务器监控面板：主机指标与 Docker 容器、浏览器内的 SSH 终端、经你确认后才在服务器上执行操作的 AI 代理，以及 Telegram 告警。登录方式为密码 + TOTP 动态码（Google Authenticator 及兼容应用），并由 fail2ban 防护暴力破解。

界面为深色 TUI 风格（类似 btop），绿色强调色。界面支持英语（默认）、俄语和中文，可在设置中切换；该语言同样用于 Telegram 机器人消息和 AI 智能体的回复。

![总览：所有服务器、容器、主机续费和活动告警](docs/screenshots/overview.png)

- 架构：[docs/architecture.md](docs/architecture.md)（俄语）
- 设计稿：[docs/design](docs/design)
- 监控服务器配置要求：[docs/server-requirements.md](docs/server-requirements.md)（俄语）
- 无法访问 `api.telegram.org` 时的 Telegram 代理：[docs/telegram-proxy.md](docs/telegram-proxy.md)（俄语）

## 功能

- **总览。** 一张表展示所有服务器：CPU、内存和磁盘条形图，运行/总容器数（失败的会标出），运行时间，内存大小，下次主机续费金额及日期，以及最新告警。可按分组筛选，一键打开终端。快捷键：`1` 总览、`3` 终端、`4` 代理、`5` 告警、`6` 设置。
- **指标与容器。** 每台服务器上运行一个小型 Go 代理（`srvmon-agent`），从 `/proc` 读取 CPU、内存、swap、负载、磁盘、网络和运行时间，通过 `srvmon-dockerproxy`（经过过滤的只读 Docker API：不能写入，也看不到容器的环境变量）读取容器信息并推送到面板。面板不可达时最多缓存一小时。指标存储在 VictoriaMetrics 中。
- **服务器详情页。** 按核心的 CPU、内存、磁盘、网络、进程、容器状态/健康检查/重启次数、syslog 与 docker 日志，页面打开期间实时刷新。点击容器可查看其 CPU 和内存历史（1 小时到 7 天；每次推送都从容器的 cgroup 读取用量，因此即使无人查看也会记录历史）。每个磁盘都会显示基于最近 6 小时线性回归的写满预测（“约 13 小时后写满”）。
- **磁盘健康（S.M.A.R.T.）。** 可选，按服务器开启：PASSED/FAILED、温度、通电时间、重映射/待映射/不可纠正扇区（SATA），或磨损、备用空间和介质错误（NVMe）。代理每 30 分钟自行读取一次，不会唤醒休眠的磁盘。由于需要对磁盘的原始读取权限，默认关闭。可在服务器页面一键开启（面板通过 SSH 以 `CAP_SYS_RAWIO`、`CAP_SYS_ADMIN` 和 SATA/NVMe 块设备只读权限重建代理容器）；监控服务器本身用 `SRVMON_SMART=1`（GitHub 变量或 `.env`）；手动安装则取消 `deploy/agent/docker-compose.yml` 中相应注释。
- **服务器档案与续费。** 每台服务器有一份档案：描述、项目、分组、标签、提供商、套餐、费用（含币种）和下次付款日期。续费信息显示在总览中、Telegram（`/bills`）里，并会在到期前几天发出告警。
- **网站、端口与证书检查。** 在服务器页面添加其 URL 和端口；面板每分钟从自身网络检查一次（HTTP 状态、TCP 连接、TLS 握手），在目标无法访问、TLS 证书或域名注册即将到期时发出告警。域名到期日期（RDAP，没有 RDAP 的后缀用 WHOIS）显示在档案中的续费信息旁和 `/bills` 中。
- **添加向导。** 校验 SSH 主机密钥，用密码或你自己的私钥登录一次。srvmon 创建 `srvmon` 用户及其 ed25519 密钥，安装代理，并可关闭密码登录。一次性密码或密钥不会被保存。
- **Web 终端。** xterm.js ↔ WebSocket ↔ `srvmon-server` 内的 SSH 客户端 ↔ sshd。支持多标签、分屏（最多四个会话并排或上下排列）、窗口缩放、`docker exec` 进入容器。终端旁边是 AI 代理面板（`F9` 显示或隐藏），在该服务器的上下文中工作。私钥从不离开监控服务器，并以 XChaCha20-Poly1305 加密存储。
- **SFTP 文件。** 终端旁边的文件面板（`F8`）：浏览、下载、上传（支持拖放）、新建文件夹、重命名和删除，权限与 `srvmon` 用户相同。每次修改和下载都会写入审计日志。
- **会话录制。** 每个网页终端会话都以 [asciinema](https://asciinema.org) 格式录制，可在面板中回放（暂停、拖动进度、1–8 倍速）或下载为 `.cast` 文件。只记录输出和窗口大小变化，不记录按键，因此在隐藏提示符下输入的密码不会被录入。输出中仍可能包含机密，因此录像用主密钥加密，并在保留期满后删除（默认 30 天）。可以在 `[6] 设置` 中关闭录制、修改保留期和单个会话的大小上限。
- **AI 代理。** 基于 [pi](https://pi.dev) SDK，支持任意提供商（DeepSeek、Anthropic、OpenAI、OpenRouter、Google、Ollama，以及任何 OpenAI / Anthropic 兼容接口）。只读命令直接执行；任何会修改服务器的命令都必须经你明确批准，策略由 `srvmon-server` 而非模型执行。每台服务器有独立的对话上下文，对话保留历史，每个对话可单独选择模型。定时任务（cron）结果可发送到 Telegram，也可以在 Telegram 中用 `/ask` 向代理提问。
- **MCP 服务器。** Claude Desktop、Cursor 和 Claude Code 使用带权限、可撤销的令牌连接 `/mcp`，工具相同，变更同样需要在面板或 Telegram 中确认。
- **告警与 Telegram。** 代理离线、容器停止或不健康、频繁重启、CPU、磁盘、磁盘即将写满、S.M.A.R.T. 异常、网站和端口无法访问、证书和域名即将到期、主机续费提醒、fail2ban 封禁、登录尝试。规则可编辑（阈值、持续时间、级别、渠道：通知或仅面板）。按你的时区设置静默时段（严重告警仍会发送），支持确认与临时静音，保留 90 天历史，严重告警附带 AI 诊断。机器人命令：`/status`、`/server`、`/alerts`、`/ack`、`/bills`、`/ask`。
- **更多通知通道。** 除 Telegram 外，告警还可以发送到邮件、ntfy、Discord、Gotify、Slack、Matrix、Pushover、Webhook 以及 [Shoutrrr](https://shoutrrr.nickfedor.com/) 支持的其他服务，每个通道可设置最低级别。
- **面板心跳（dead man's switch）。** 面板每分钟自检一次并 ping 一个外部 URL，例如 [healthchecks.io](https://healthchecks.io)。如果面板宕机，ping 会停止，该服务会通过邮件或其自身渠道提醒您。
- **备份与应急访问。** 按计划将面板数据库加密备份到任意 S3 兼容存储（AWS、Yandex Object Storage、R2、B2、MinIO），支持保留份数，失败时 Telegram 告警；主密钥需凭 TOTP 验证码单独导出。您自己的 SSH 公钥会保存在每台服务器的 `authorized_keys` 中，面板丢失也不会被锁在服务器外。详见 [docs/backup.md](docs/backup.md)。
- **安全。** 密码（argon2id）+ TOTP 或备用码，无需用户名。限流、审计日志，以及面板和 sshd 的 fail2ban 规则。在服务器上只有 `srvmon-dockerproxy` 容器能访问 docker socket，`srvmon` 用户默认只有受限 sudo（见[服务器访问权限](#服务器访问权限)）。

## 截图

**服务器详情页**：按核心的 CPU、内存、网络、进程、带健康状态和日志的 docker 容器、服务器档案。

![服务器详情页](docs/screenshots/server.png)

**Web 终端**，右侧为 AI 代理面板（`F9`）。

![带代理面板的 Web 终端](docs/screenshots/terminal.png)

**AI 代理**：对话、任务和历史，服务器上下文与模型选择，以及已确认命令的日志。

![AI 代理](docs/screenshots/agent.png)

**告警**：活动告警、可编辑的规则、静默时段、Telegram 机器人和历史记录。

![告警与 Telegram 机器人](docs/screenshots/alerts.png)

**设置**：语言、AI 提供商、协议、API 密钥、思考级别以及对话中可用的模型。

![设置：语言与 AI 代理](docs/screenshots/settings.png)

截图中的敏感数据（地址、付款信息、聊天 ID、密钥）已做模糊处理。

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

备份：在「设置 → 备份」中配置 S3 兼容存储，并在同一处导出主密钥（或运行 `docker compose exec server /srvmon-server master-key`），离线保存。备份用它加密且从不包含它。恢复步骤与应急 SSH 密钥见 [docs/backup.md](docs/backup.md)。

## 从你的 fork 自动部署

1. 按上文准备主机（`install-host.sh`）并解析域名。
2. 在 GitHub：Settings → Environments → `production`：
   - secrets：`DEPLOY_HOST`、`DEPLOY_USER`、`DEPLOY_SSH_KEY`（部署私钥）、`DEPLOY_KNOWN_HOSTS`（`ssh-keyscan <host>` 的输出），可选 `DEPLOY_PORT`、`GHCR_PULL_TOKEN`、`AI_API_KEY`；
   - variables：`SRVMON_DOMAIN`，可选 `SRVMON_PROXY`、`SRVMON_HTTP_BIND`、`SRVMON_SMART`（`1`：为监控服务器开启 S.M.A.R.T.）、`DEPLOY_PATH`（默认 `/opt/srvmon`）；
   - 将该环境限制为仅 `main` 分支可部署（Deployment branches）。
3. 每次推送到 `main`：CI → Release（镜像 `ghcr.io/<owner>/srvmon-server|agent|ai`，标签 `sha-xxxxxxx` 和 `latest`）→ Deploy（`docker compose pull && up -d`，等待健康检查，失败则回滚到上一个标签）。未设置 `DEPLOY_HOST` 时跳过部署。
4. 首次部署后执行一次：`cd /opt/srvmon && docker compose exec -it server /srvmon-server init`。

在 fork 中，必须将 `deploy/docker-compose.yml`（server、agent、ai 三个镜像）和 `internal/config/config.go` 中的 `ghcr.io/askidmobile/` 改为 `ghcr.io/<你的 owner>/`：Release 只会在你自己的命名空间下发布 `sha-*` 标签，而 Deploy 正是拉取这些标签。上游镜像适用于上文的手动安装。

## 添加服务器

面板中的向导（`#/add`）：填写名称、`host:port`、项目、分组、标签；校验并确认主机密钥指纹；选择一次性登录方式（密码、你自己的私钥，或把面板公钥加入 `authorized_keys`）。随后 srvmon 创建 `srvmon` 用户（docker 组，sudo 模式可选，见下文）、验证密钥登录、以 `/etc/srvmon/agent.token`（0600）中的令牌启动 `srvmon-agent` 容器，并可选择关闭 `PasswordAuthentication`。

不使用向导时：

```bash
docker compose exec server /srvmon-server add-server -name shop-db -group prod -tags db,public
# 在目标服务器上，使用 deploy/agent/docker-compose.yml：
SRVMON_URL=https://mon.example.com SRVMON_TOKEN=a_... docker compose up -d
```

### 没有 docker 的主机（Proxmox VE）

详细说明（俄语，附英文摘要）：[docs/agent.md](docs/agent.md)。

在虚拟化主机上不宜安装 docker（它会改写 iptables/nftables，并与 KVM、ZFS 共用 cgroup），而 LXC 中的代理只能看到容器本身。因此代理也可以作为普通的 **systemd 服务**运行。向导中的代理模式默认为 `auto`：服务器有 docker 时用 docker；在没有 docker 的 Proxmox VE 节点上用 systemd 服务（不安装 docker）；否则照旧安装 docker。`docker` 和 `systemd` 可强制指定模式。面板镜像自带静态代理程序（`/agent/srvmon-agent-linux-amd64|arm64`），通过 SSH 复制到 `/usr/local/lib/srvmon-agent/`，更新和回滚与容器代理相同。服务器护照中的 **代理令牌** 可重新签发令牌，并给出手动安装命令：

```bash
curl -fsSL https://mon.example.com/agent/install.sh | SRVMON_TOKEN=a_... sh
```

没有任何代理令牌的服务器标记为 **仅 ssh**，而不是 offline。

## 网站与端口检查

在服务器页面的“检查”面板中每行填写一个目标：

| 目标 | 检查内容 |
|---|---|
| `https://example.com/health` | HTTP GET，状态码低于 400 视为正常（最多 5 次重定向），并检查 TLS 证书 |
| `http://10.0.0.5:8080/` | HTTP GET |
| `tls://mail.example.com:993` | TCP 连接和 TLS 握手，并检查证书 |
| `db.example.com:5432` | TCP 连接（`tcp://` 可省略） |

- 监控服务器每分钟检查一次每个目标；检查失败会在 3 秒后重试一次，仍失败才视为无法访问。每台服务器最多 20 个目标。
- 面板显示状态、延迟、证书到期和域名注册到期时间。历史写入 VictoriaMetrics：`srvmon_check_up`、`srvmon_check_latency_ms`、`srvmon_check_cert_days`。
- 域名到期时间每天通过 RDAP（IANA 引导文件）查询两次，没有 RDAP 的后缀（如 `.ru`、`.рф`）使用 WHOIS。监控服务器需要能访问外网 HTTPS 和 TCP 43 端口。
- 告警规则：“网站 / 端口无法访问”持续 2 分钟（crit），“证书 ≤ 14 天内到期”和“域名 ≤ 30 天内到期”（warn）。阈值可在告警页面修改。

## 服务器访问权限

面板在受管服务器上能做什么、不能做什么：

- **代理。** `srvmon-agent` 不接触 docker socket。socket 由单独的 `srvmon-dockerproxy` 容器持有（同一镜像，无网络、无 capabilities、只读根文件系统），它只向代理提供 `GET /_ping`、`/version`、`/containers/json`、`/containers/{id}/json` 和 `/containers/{id}/logs`；容器 inspect 结果中的 `Env` 会被删除，环境变量中的密钥不会离开服务器。主机的 `/run`（docker 和 containerd 的 socket）在代理的 `/host` 中被隐藏。面板更新代理时会自动把旧代理迁移到这种结构。
- **`srvmon` 的 sudo。** 三种模式，在向导中选择，并可在服务器档案中切换：**无 sudo**；**受限**（默认：`systemctl status/start/stop/restart/reload/…`、`journalctl`、`service`、`docker`；不能 `sudo -i`，不能编辑或启用 unit）；**完整**（`NOPASSWD:ALL`，终端中为 root）。面板通过 `sudo -n -l` 实时读取当前模式；此前添加的服务器保持完整 sudo，直到你手动切换。
- **边界说明。** `srvmon` 仍在 `docker` 组中，而 docker 权限等同于 root。受限 sudo 防的是误操作（你自己的，或你批准的 AI 智能体命令），而不是已经控制面板的人。也正因如此，面板可以重新放宽 sudo：它先在服务器上用 `visudo -c` 检查文件，再通过代理镜像的一次性容器写入 `/etc/sudoers.d/srvmon`。

## 告警与 Telegram

`[5] 告警` 页面（`#/alerts`）显示活动告警（可“确认”或“静音 1 小时”）、规则、Telegram 机器人、静默时段和 90 天历史。

- 默认规则：代理静默超过 2 分钟（crit），容器停止或不健康 1 分钟（crit），每小时重启超过 2 次，CPU > 90% 持续 5 分钟，内存 > 95% 持续 5 分钟（默认关闭，仅面板），磁盘 > 85%，按当前增长速度 24 小时内写满，S.M.A.R.T. 异常（自检失败、待映射或不可纠正扇区、NVMe 严重警告、备用空间或寿命耗尽；crit），3 天内需续费，网站或端口无法访问 2 分钟（crit），TLS 证书 14 天内到期，域名 30 天内到期；以及事件：监控服务器上的 fail2ban 封禁、面板密码爆破、面板登录。
- 每条规则都可以关闭、修改（阈值、持续时间、级别 crit/warn/info）或指定渠道：`notify`（面板、Telegram 和通知通道）或 `web`（仅面板）。`+ 规则` 可添加新规则。以退出码 0 停止的容器不算告警。
- 静默时段（例如 `Europe/Moscow` 时区 01:00–07:00）内通知会延后到时段结束；严重告警始终发送。

机器人设置：

1. 通过 [@BotFather](https://t.me/BotFather)（`/newbot`）创建机器人并复制令牌。
2. 在面板的告警页面粘贴令牌并保存。
3. 向机器人发送 `/start`，然后在面板中选择该聊天。
4. 发送测试消息。

如果监控服务器无法访问 `api.telegram.org`，可以用 Cloudflare Worker 搭一个代理并设置 `SRVMON_TELEGRAM_API`，见 [docs/telegram-proxy.md](docs/telegram-proxy.md)。

### 通知通道（Shoutrrr）

同一页面的“通知通道”面板可以用 [Shoutrrr](https://shoutrrr.nickfedor.com/) URL 添加更多告警去处：

| 服务 | URL |
|---|---|
| 邮件 | `smtp://user:password@smtp.example.com:587/?from=srvmon@example.com&to=me@example.com` |
| ntfy | `ntfy://ntfy.sh/my-secret-topic` |
| Discord | `discord://token@webhook-id` |
| Gotify | `gotify://gotify.example.com/app-token` |
| Webhook | `generic+https://hooks.example.com/srvmon` |

- 每个通道有最低级别：`crit+`（仅严重）、`warn+` 或全部。渠道为 `notify` 的告警会发送到 Telegram 以及所有符合条件的已启用通道；静默时段和静音对所有通道生效。
- URL 中包含凭据，因此会用主密钥加密存储，面板只显示服务和主机。“测试”会发送一条测试消息；通道旁显示最后发送时间或错误，页头显示 `ch: N`，失败的通道标红。
- 发送失败会重试三次。AI 代理任务的告警也会发送到这些通道。

### 面板心跳（dead man's switch）

告警由面板自己发送，因此宕机的面板无法告诉你它已宕机。心跳弥补了这一点：`srvmon-server` 每分钟检查数据库、告警引擎、VictoriaMetrics 和 Telegram 机器人，然后 ping 一个外部 URL。如果面板或其主机宕机，ping 会停止，外部服务会通过其自身渠道发出告警。

1. 在 [healthchecks.io](https://healthchecks.io)（免费套餐即可）添加一个检查：period 1 分钟，grace 5 分钟。
2. 在检查的集成中选择提醒方式：默认启用邮件，也可使用 Telegram、ntfy 等。
3. 在面板中：告警 → 面板心跳 → 粘贴 ping URL（`https://hc-ping.com/<uuid>`）→ 保存，然后点击“立即 ping”。

当某项自检连续 3 分钟失败时，面板会把问题发送到 `<url>/fail`，healthchecks.io 会立即告警并显示出错内容。对于其他接收方（Uptime Kuma 推送监控、你自己的第二台服务器），取消勾选“将问题报告到 URL/fail”：此时面板在出问题期间只是跳过 ping。URL 使用主密钥加密存储。

分步设置说明（包括 healthchecks.io 注册邮件收不到时改用 [Better Stack](https://betterstack.com)）：[docs/heartbeat.md](docs/heartbeat.md)。

## AI 代理

`srvmon-ai` 容器（pi SDK，`ai/`）只带有 srvmon 工具：列出服务器和容器、读取指标和日志、通过 SSH 网关在服务器上执行命令、管理定时任务。它没有自己的 SSH 密钥。代理有两个入口：`[3] 终端` 右侧的面板（`F9` 显示或隐藏）和带确认日志的 `[4] 代理` 页面。

- **上下文。** 选择“所有服务器”或某一台服务器。每台服务器有自己的对话，因此从服务器页面或终端打开代理时，它已经知道你指的是哪台主机。对话保存在“历史”中，可以随时继续。
- **模型。** pi 目录中的任意提供商（DeepSeek、Anthropic、OpenAI、OpenRouter、Google、Ollama …）或自定义的 OpenAI / Anthropic 兼容地址。在设置中勾选多个模型，即可在每个对话中切换；推理模型可设置思考级别。
- **命令确认。** 只读命令（`docker ps/logs/inspect`、`df`、`journalctl`、`systemctl status` …）立即执行。任何会修改服务器的命令都会显示为“⚠ 确认”卡片，只有点击“✓ 执行”后才会运行。由 `srvmon-server`（`internal/aiagent/policy.go`）而不是模型来决定。所有命令及其结果都会记入确认日志。
- **机密。** 读取通常存放机密的文件（`.env`、私钥、`/etc/shadow`、`/proc/*/environ`、`~/.aws`、`/run/secrets` …）以及会查询命令中指定主机的命令（按名称执行 `dig`、`host`、`nslookup`、`ping` 或 `getent hosts`）同样需要确认，这样日志中的提示注入无法通过 DNS 泄露数据。递归的 `grep -r` 和 `diff -r` 同样需要确认，因为它们会读取目录树中的所有文件。命令输出在发送给模型之前会被遮盖：`KEY=value` 形式的机密（密码、令牌、API 密钥）、URL 中的凭据、bearer 令牌、已知格式的令牌和私钥块都会替换为 `***`。遮盖只是尽力而为，因此主要边界不是它，而是操作系统权限：
- **只读账户。** 只读命令以 `srvmon-ro` 身份运行：它不属于任何用户组，也没有 sudo。不再以 `srvmon` 身份运行，因为 `srvmon` 属于 docker 组，实际上等同于 root。面板在接入服务器时创建 `srvmon-ro`；对于更早接入的服务器，则在第一次只读命令之前创建。创建方式依次为：以 root 身份、通过免密 sudo，或者在 `srvmon` 只有 docker 组时，通过 agent 镜像的一次性容器。如果 `srvmon` 这些权限都没有，它本身就和 `srvmon-ro` 一样没有特权，会自己执行只读命令。如果无法创建 `srvmon-ro`（例如 sshd 的 `AllowUsers` 限制），只读命令不会执行。
- **只读 Docker。** `srvmon-ro` 只能通过面板访问 Docker。sshd 为只读会话提供一个私有 unix 套接字，由面板自己应答。面板只放行 GET 请求，包括容器、镜像、网络和卷的列表，以及 inspect、logs、top、stats、events、info 和 df。容器和镜像的 `Env` 会被移除。export、archive、保存镜像、attach 和 exec 不会到达守护进程。
- **符号链接。** 执行读取前，面板会在服务器上解析命令中的路径，因此指向机密文件的符号链接同样需要确认。
- **输出标注。** 输出发送给模型时会标注为不可信的服务器数据，代理也被告知不要执行其中的指令。

经确认的命令仍以 `srvmon` 身份运行。
- **任务。** 例如“每 15 分钟检查容器重启，如果一小时内超过 2 次就发到 Telegram”会变成 cron 为 `*/15 * * * *`（UTC）的任务。任务显示在“任务”标签页中，带告警的结果发送到 Telegram。
- **Telegram。** 机器人会把代理的命令连同批准/拒绝按钮发给你确认，`/ask <问题>` 可直接在 Telegram 中向代理提问。
- **告警诊断。** 严重告警触发时，代理会自行用只读命令（容器日志、`inspect`、`df`、`journalctl` …）排查，并以回复 Telegram 告警消息的方式发送可能的原因、依据和修复建议；诊断结果也显示在 `[5] 告警` 中对应告警下方，那里还有手动运行的“诊断”按钮。`srvmon-server` 会直接拒绝诊断中任何会修改服务器的命令（不请求确认），每次诊断最多 12 条命令、5 分钟，同时只运行一个，同一服务器上的同一告警 6 小时内最多诊断一次，并有每小时和每天的上限（默认 4 和 20）。设置 → 告警的 AI 诊断：开关、是否包括警告、上限以及单独（更便宜）的模型。

在面板设置中启用：选择提供商（pi 目录中的任意一个或自定义兼容地址），填入 API 密钥（加密存储，不会返回给浏览器），加载并勾选模型，然后保存并应用。在面板保存设置之前，`srvmon-ai` 使用 `.env` 中的 `SRVMON_AI_PROVIDER`、`SRVMON_AI_MODEL`、`SRVMON_AI_API_KEY`。

## MCP：Claude Desktop、Cursor、Claude Code

srvmon 同时也是一个 [MCP](https://modelcontextprotocol.io) 服务器，地址为 `https://<你的域名>/mcp`（Streamable HTTP）。MCP 客户端获得与内置代理相同的工具和相同的确认策略，并且不需要 `srvmon-ai` 容器。

| 工具 | 权限 | 作用 |
|---|---|---|
| `list_servers`、`get_server` | `read` | 服务器、指标、容器、日志中的最新警告 |
| `list_alerts`、`list_tasks` | `read` | 当前告警、代理的定时任务 |
| `run_command` | `exec` | 通过 SSH 执行命令：只读命令立即执行，变更命令等待你确认 |
| `action_status` | `exec` | 等待确认的命令的状态和输出 |
| `create_task`、`delete_task` | `tasks` | 内置代理的定时任务 |

1. 打开 `[6] 设置` →“MCP 访问”，为令牌命名（每个客户端一个），勾选权限和有效期，然后点击“创建令牌”。令牌（`smcp_…`）只显示一次，并附带现成的配置；数据库只保存其哈希。可在同一页面撤销。
2. 连接客户端：

```bash
# Claude Code
claude mcp add --transport http srvmon https://srvmon.example.com/mcp --header "Authorization: Bearer smcp_…"
```

```jsonc
// Cursor：~/.cursor/mcp.json（或项目中的 .cursor/mcp.json）
{ "mcpServers": { "srvmon": { "url": "https://srvmon.example.com/mcp",
  "headers": { "Authorization": "Bearer smcp_…" } } } }
```

```jsonc
// Claude Desktop：Settings → Developer → Edit config（claude_desktop_config.json）；需要 Node.js
{ "mcpServers": { "srvmon": { "command": "npx",
  "args": ["-y", "mcp-remote@latest", "https://srvmon.example.com/mcp", "--header", "Authorization:${SRVMON_MCP_AUTH}"],
  "env": { "SRVMON_MCP_AUTH": "Bearer smcp_…" } } } }
```

会修改服务器的命令会以“⚠ 确认”卡片出现在代理面板和 Telegram 中，并标注“MCP <令牌名>”。MCP 客户端大约一分钟后会放弃工具调用，因此 `run_command` 等待 40 秒后返回 `pending` 和 `action_id`。命令本身会等待你的决定 15 分钟，客户端通过 `action_status` 获取结果。错误的令牌与登录共用同一个按 IP 的限流，并写入 fail2ban 日志。

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
