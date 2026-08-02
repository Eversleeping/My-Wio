# 生产环境部署指南

本文是可复用的部署模板，不记录任何实际公网 IP、域名、SSH 用户、主机目录或既有基础设施状态。请在受控的密码管理工具或部署平台中保存本次部署的真实值；不要把 `.env`、私钥、Token 或机器清单提交到 Git。

## 本地部署档案

每个实际部署环境都应在仓库根目录维护一份 `.deployment.local.md`。该文件已被 `.gitignore` 排除，只用于记录当前生产主机的 SSH 地址、账号、私钥相对路径、已核验的主机指纹、部署目录、Compose 项目名、Compose 文件组合和验收地址。实际值变化时必须同步更新该档案，禁止把真实值补进本文或提交到 Git。

部署前必须先执行以下检查：

```bash
git check-ignore .deployment.local.md '*.pem' .env
```

随后按本地档案中的 **IP、账号和私钥** 显式建立 SSH 连接，不要仅依赖可能过期的 `~/.ssh/config` 别名。若 SSH 报告主机密钥变化，应停止部署并重新从可信渠道核对指纹；不要使用 `StrictHostKeyChecking=no` 绕过校验。连接成功后，还应在远端核对部署目录、Git remote、当前分支、Compose 容器和宿主反向代理状态，再执行任何写操作。

## 部署前条件

- Linux Docker 主机已安装 Docker Engine 和 Docker Compose v2。
- 已为公开访问准备 DNS 名称，且该名称指向此主机或其前置反向代理；TLS 终止点可使用 80/443 端口。
- 选择“独立 Caddy”或“宿主机 Caddy”之一。两种方式不能同时占用同一主机的 80/443 端口。
- 部署账户能够运行 Docker；如果使用宿主机 Caddy，另需具备校验和重载 Caddy 配置的权限。
- 已确定部署方式及其目录暴露边界：**方案 A** 会直接映射宿主机的 `/srv`、`/opt`、`/home`；**方案 B** 才会将这些容器路径替换为专用工作区根目录下的子目录。需要最小化宿主机可见范围时必须选择方案 B。

## 一次性部署变量

以下变量是模板。将尖括号中的内容替换为本次部署的真实值后，在当前 shell 中加载；不要把包含密钥的变量文件提交到仓库。

```bash
export WIO_DEPLOY_DIR='<absolute deployment directory>'
export WIO_WORKSPACE_ROOT='<absolute dedicated workspace root>'
export WIO_RUN_USER='<deploy user>'
export WIO_COMPOSE_PROJECT='<unique compose project name>'
export WIO_PUBLIC_DOMAIN='<public DNS name>'
export WIO_HOST_PORT='<loopback port, for example 18080>'
export WIO_CADDY_SITE_FILE='<host Caddy site-file path>'
export WIO_CADDY_MAIN_CONFIG='<host Caddy main-config path>'
```

后续 Compose 命令应由 `$WIO_RUN_USER` 执行；若当前用户不同，请先切换到该账户。下面的命令假定仓库内容已经以发布版本的形式位于 `$WIO_DEPLOY_DIR`。如果使用 Git 获取源码，请在目录为空时从可信远程仓库克隆；如果使用制品发布，请先校验制品来源和版本。

```bash
sudo install -d -m 0750 -o "$WIO_RUN_USER" -g "$WIO_RUN_USER" "$WIO_DEPLOY_DIR"
sudo install -d -m 0750 -o "$WIO_RUN_USER" -g "$WIO_RUN_USER" \
  "$WIO_WORKSPACE_ROOT/srv" \
  "$WIO_WORKSPACE_ROOT/opt" \
  "$WIO_WORKSPACE_ROOT/home"

# 仅在使用 Git 且部署目录为空时执行；替换为可信的仓库地址。
git clone '<trusted repository URL>' "$WIO_DEPLOY_DIR"
```

## 配置生产密钥

在部署目录创建 `.env`，并限制其权限。使用独立随机值：`WIO_MASTER_KEY` 需要 Base64 编码的 32 字节密钥；`POSTGRES_PASSWORD` 应为长随机密码。

```bash
cd "$WIO_DEPLOY_DIR"
umask 077
cp .env.example .env
openssl rand -base64 32  # 写入 WIO_MASTER_KEY
openssl rand -base64 36  # 写入 POSTGRES_PASSWORD
chmod 600 .env
```

在 `.env` 中至少设置以下值：

```dotenv
WIO_DOMAIN=<public DNS name>
POSTGRES_PASSWORD=<long random password>
WIO_DATABASE_URL=postgres://wio:<URL-encoded password>@postgres:5432/wio?sslmode=disable
WIO_MASTER_KEY=<base64-encoded 32-byte key>
WIO_CONTROL_AGENT_ENABLED=true
WIO_HOST_PORT=<loopback port, only for host-Caddy mode>
WIO_WORKSPACE_ROOT=<absolute dedicated workspace root, only for host-Caddy mode>
```

`WIO_DATABASE_URL` 中的密码必须与 `POSTGRES_PASSWORD` 相同，并且 `@`、`:`、`/`、`?`、`#` 等 URL 保留字符需要 URL 编码。`sslmode=disable` 仅适用于 Compose 内部的 PostgreSQL 网络；不要将此连接串用于跨主机数据库连接。

## 方案 A：独立 Caddy（默认，但目录可见范围较广）

当这台主机没有其他反向代理占用 80/443 时，使用默认 Compose 文件即可。Caddy 会自动申请和续期证书。

> **目录边界**：默认 Compose 文件会把宿主机的 `/srv`、`/opt`、`/home` 直接映射到控制面容器，以支持内置控制机 Agent 扫描与部署。`WIO_WORKSPACE_ROOT` 和 `WIO_HOST_PORT` 在本方案中不生效；仅设置它们不会缩小挂载范围。请只在专用于 Wio、且这三个目录内容均可被其管理的主机上使用本方案。

```bash
cd "$WIO_DEPLOY_DIR"
docker compose -p "$WIO_COMPOSE_PROJECT" \
  --env-file .env \
  -f deploy/docker-compose.yml \
  up -d --build

docker compose -p "$WIO_COMPOSE_PROJECT" \
  --env-file .env \
  -f deploy/docker-compose.yml \
  ps
```

## 方案 B：宿主机 Caddy（专用工作区隔离）

当宿主机已有 Caddy 或其他统一入口时，不启动 Compose 自带的 Caddy，并只将控制面绑定到回环地址。`deploy/docker-compose.host-proxy.yml` 会读取 `.env` 中的 `WIO_HOST_PORT` 和 `WIO_WORKSPACE_ROOT`；它按容器目标路径覆盖基础 Compose 文件中的三个宿主机挂载，因此容器只能看到 `$WIO_WORKSPACE_ROOT/srv`、`$WIO_WORKSPACE_ROOT/opt`、`$WIO_WORKSPACE_ROOT/home`。

```bash
cd "$WIO_DEPLOY_DIR"
docker compose -p "$WIO_COMPOSE_PROJECT" \
  --env-file .env \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.host-proxy.yml \
  up -d --build postgres controlplane
```

先复制仓库内的 Caddy 模板。该模板通过 Caddyfile 环境变量 `{$WIO_PUBLIC_DOMAIN}` 和 `{$WIO_HOST_PORT}` 读取站点域名和回环端口，WebSocket 会单独按 HTTP/1.1 转发，其余 HTTP 与 gRPC 请求会通过 h2c 转发。

```bash
sudo install -m 0644 deploy/Caddyfile.host-proxy "$WIO_CADDY_SITE_FILE"
```

将同名变量提供给 **Caddy systemd 服务进程**；仅在当前 shell 中 `export` 变量不足以让服务重载时读取到它们。使用 `sudo systemctl edit caddy` 创建覆盖配置，并写入本次部署的真实值：

```ini
[Service]
Environment="WIO_PUBLIC_DOMAIN=<public DNS name>"
Environment="WIO_HOST_PORT=<loopback port>"
```

确认 `$WIO_CADDY_SITE_FILE` 已由 `$WIO_CADDY_MAIN_CONFIG` 引入后，重载 systemd。校验命令显式传入当前 shell 的变量，确保 Caddyfile 在校验阶段也能展开。

```bash
sudo systemctl daemon-reload
sudo env WIO_PUBLIC_DOMAIN="$WIO_PUBLIC_DOMAIN" WIO_HOST_PORT="$WIO_HOST_PORT" \
  caddy validate --config "$WIO_CADDY_MAIN_CONFIG"
sudo systemctl reload caddy
```

## 验收与首次登录

先检查容器和本机健康检查。方案 A 使用公开 HTTPS 地址；方案 B 还应检查回环端口。

```bash
cd "$WIO_DEPLOY_DIR"
docker compose -p "$WIO_COMPOSE_PROJECT" \
  --env-file .env \
  -f deploy/docker-compose.yml \
  ps

curl --fail --silent "https://$WIO_PUBLIC_DOMAIN/api/health"
# 方案 B 额外执行：
curl --fail --silent "http://127.0.0.1:$WIO_HOST_PORT/api/health"
```

部署前可先展开方案 B 配置，确认三个容器挂载的来源均位于 `$WIO_WORKSPACE_ROOT`，再执行 `up`：

```bash
docker compose -p "$WIO_COMPOSE_PROJECT" \
  --env-file .env \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.host-proxy.yml \
  config
```

在浏览器打开 `https://<public DNS name>` 并创建管理员。启用 TOTP 时，立即离线保存恢复码。登录后，在开发者工具中确认 `/api/ws` 返回 `101 Switching Protocols`；如使用宿主机 Caddy，日志中不应出现 `http2: invalid Upgrade request header`。

## 更新、回滚与备份

- 更新前先备份 PostgreSQL 数据卷、`.env` 中的 `WIO_MASTER_KEY`，以及需要保留的 Agent 状态；这些内容应加密保存到 Docker 主机之外。
- 更新前核对远端工作树干净、Git remote 与预期仓库一致，并记录旧 HEAD；不要在存在未知改动时直接拉取或切换版本。
- 备份至少应包含 `.env`、PostgreSQL 自定义格式 dump 和旧 HEAD 的 Git bundle，并将备份目录和各文件大小写入部署记录。
- 使用与首次部署完全相同的 Compose 项目名和文件组合执行 `up -d --build`，避免创建重复的网络、卷或容器。
- 使用宿主机代理时，只重建 `postgres` 与 `controlplane`；不要启动基础 Compose 中的仓库 Caddy，也不要修改正在工作的宿主 Caddy 配置。
- 发布失败时先保留容器日志和当前镜像摘要，再选择已验证的上一版制品恢复；不要在未备份的情况下删除 PostgreSQL 数据卷。
- 丢失 `WIO_MASTER_KEY` 将使 Vault 中加密的 TOTP 密钥、凭据预设和部分 Agent 状态无法恢复，不能用新的随机值无损替换。

## 常见失败

| 现象 | 排查方向 |
| --- | --- |
| 证书申请或公开健康检查失败 | 确认 DNS 已指向正确入口、80/443 未被其他服务占用，并检查防火墙和 Caddy 日志。 |
| `controlplane` 无法连接 PostgreSQL | 对比 `.env` 中的 `POSTGRES_PASSWORD` 与 URL 编码后的 `WIO_DATABASE_URL`，再检查 `postgres` 容器健康状态。 |
| WebSocket 连接失败但页面可打开 | 宿主机代理必须将 Upgrade 请求单独按 HTTP/1.1 转发；不要把 WebSocket 请求强制走 h2c。 |
| 容器可启动但无法扫描或部署宿主机项目 | 检查专用工作区目录是否存在、是否映射到 `srv`/`opt`/`home`，并确认 Docker socket 挂载及部署账户权限。 |
