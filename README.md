# VPS Pulse

个人 Linux VPS 实时管理、连通性自愈与 DNS 容灾面板。

## 功能

- CPU、内存、磁盘、负载及上下行速度每秒更新
- 日、月、永久累计流量与独立账单日
- Agent 自动 Ping、换 IP、IPv4 检测和 Cloudflare DNS 更新
- Cloudflare A/CNAME 主备容灾、5 分钟恢复检测和容灾演练
- Telegram 通知与受限远程管理
- TCP、HTTP、DNS、TLS 证书服务监控
- CPU、内存、磁盘持续超限告警与维护模式
- 1 小时、24 小时、7 天历史图表
- 每日 SQLite 自动备份、手动备份、下载与 SHA-256 校验
- Agent 版本显示、HTTPS 在线升级、SHA-256 校验和回滚
- 随机会话、登录限速、严格 Origin、敏感字段加密和 Agent Token 哈希

## 一键 HTTPS 部署

准备一个已经解析到服务器 IPv4 的域名，并开放 TCP 80、443：

```bash
curl -fsSL https://raw.githubusercontent.com/xianluwan/vps-pulse/main/install.sh | sudo bash
```

无交互部署：

```bash
curl -fsSL https://raw.githubusercontent.com/xianluwan/vps-pulse/main/install.sh |
  sudo env VPS_PULSE_DOMAIN=panel.example.com VPS_PULSE_PASSWORD='StrongPassword2026' bash
```

凭据保存在 `/root/vps-pulse-credentials.txt`。面板数据位于 `/opt/vps-pulse/data`。

## 更新

提交代码后等待 GitHub Actions 的 `Build Docker image` 成功，然后：

```bash
cd /opt/vps-pulse
git pull origin main
docker compose pull
docker compose up -d
```

## Agent

在面板添加 VPS 后复制安装命令。安装器会自动安装 `curl`、`ping`、CA、`sysctl`、`kmod`，尝试开启并验证 BBR。Token 存在权限 `0600` 的 `/etc/vps-pulse-agent.token`。

旧 Agent 再次安装时会询问是否覆盖。在线 Agent 也可从服务器操作菜单升级或回滚。

## 备份恢复

面板每天自动创建 SQLite 快照并保留 7 份。下载数据库备份时必须同时保存 `.env`；其中的 `MASTER_KEY` 是解密 Telegram、Cloudflare 和换 IP 配置所必需的。

恢复前停止面板，替换 `/opt/vps-pulse/data/panel.db`，保持原 `.env`，然后重新启动。

## 开发验证

```bash
go mod tidy
go test ./...
CGO_ENABLED=0 go build ./cmd/server
CGO_ENABLED=0 go build ./cmd/agent
```

安全部署要求见 [SECURITY.md](SECURITY.md)。
