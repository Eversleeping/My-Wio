# 生产环境部署记录

## 当前控制机

| 项目 | 当前值 |
| --- | --- |
| 状态 | 生效 |
| 公网 IP | `44.203.157.32` |
| SSH 用户 | `ubuntu` |
| 访问域名 | `https://maomi.xin` |
| Compose 项目名 | `wio-controlplane` |
| 部署目录 | `/opt/wio-controlplane` |
| Wio 本地监听 | `127.0.0.1:18080` |
| Wio 工作区根目录 | `/opt/wio-workspaces` |
| DNS/CDN | Cloudflare 代理，HTTP/2 gRPC 已验证 |

自 2026-07-28 起，原控制机作废，不再作为部署、Agent 注册或管理入口。当前部署使用全新的 PostgreSQL 数据卷，不迁移原控制机数据库、管理员账号、Agent 令牌或 Vault 密文；需要使用的服务器应在新控制机中重新注册。

## 隔离约束

- 使用固定 Compose 项目名 `wio-controlplane`，容器、网络和命名卷均与其他项目隔离。
- 不启动 Compose 自带的 Caddy，避免占用宿主机现有的 80/443 端口。
- 控制面只发布到回环地址 `127.0.0.1:18080`，公网流量统一经过宿主机 Caddy。
- 容器内的 `/srv`、`/opt` 和 `/home` 只映射到 `/opt/wio-workspaces` 下的专属目录，不扫描宿主机其他项目目录。
- 为支持内置控制机 Agent 的容器部署功能，控制面仍挂载 Docker socket。日常操作必须使用 Wio 自己的部署目标和固定 Compose 项目名，禁止复用其他项目的 Compose 名称。

## 部署与更新

生产密钥只保存在控制机 `/opt/wio-controlplane/.env`，权限应为 `0600`，不得提交到 Git。

```bash
sudo install -d -m 0750 -o ubuntu -g ubuntu /opt/wio-controlplane
sudo install -d -m 0750 -o ubuntu -g ubuntu \
  /opt/wio-workspaces/srv \
  /opt/wio-workspaces/opt \
  /opt/wio-workspaces/home

cd /opt/wio-controlplane
sudo docker compose -p wio-controlplane \
  --env-file .env \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.host-proxy.yml \
  up -d --build postgres controlplane
```

宿主机 Caddy 站点文件为 `/etc/caddy/sites-enabled/wio.caddy`：

```caddyfile
maomi.xin {
	encode zstd gzip
	reverse_proxy 127.0.0.1:18080 {
		transport http {
			versions h2c 1.1
		}
	}
	header {
		Strict-Transport-Security "max-age=31536000; includeSubDomains"
		X-Content-Type-Options nosniff
		X-Frame-Options DENY
		Referrer-Policy strict-origin-when-cross-origin
		-Server
	}
}
```

修改后先校验再平滑加载：

```bash
sudo caddy validate --config /etc/caddy/Caddyfile
sudo systemctl reload caddy
```

## 验收

```bash
cd /opt/wio-controlplane
sudo docker compose -p wio-controlplane \
  --env-file .env \
  -f deploy/docker-compose.yml \
  -f deploy/docker-compose.host-proxy.yml \
  ps
curl --fail http://127.0.0.1:18080/api/health
curl --fail https://maomi.xin/api/health
```

部署完成后打开 `https://maomi.xin` 创建新的管理员。启用 TOTP 时，应离线保存恢复码；同时应在 Docker 主机之外备份 `.env` 中的 `WIO_MASTER_KEY` 和 PostgreSQL 数据卷。
