# Security

## 部署要求

- 仅通过 Caddy 的 HTTPS 访问，不要重新公开容器端口 8080。
- `.env` 权限必须为 `0600`，`data/` 为 `0700`。
- `ADMIN_PASSWORD` 至少 12 位，`MASTER_KEY` 必须使用安装器生成的随机值。
- Cloudflare Token 仅授予目标 Zone 的 `Zone Read` 与 `DNS Edit`。
- Telegram 只配置个人 User ID，不要允许群组或机器人 ID。
- 定期下载数据库备份并单独安全保存 `.env`。

## 信任边界

Agent 以 root 运行，并可执行预设的换 IP Shell 命令。面板管理员权限等同于所有受管 VPS 的高权限控制权。不要与不可信用户共享面板账号、Telegram Bot 或服务器 shell。

## 密钥处理

- 管理会话使用随机 Token，数据库只保存哈希。
- 新 Agent Token 数据库只保存 SHA-256 派生哈希，并通过 WebSocket Authorization 头发送。
- Agent Token 位于 `/etc/vps-pulse-agent.token`，权限 `0600`。
- Telegram、容灾和新保存的 VPS Cloudflare/换 IP配置使用 AES-GCM 加密。
- 修改 `MASTER_KEY` 会导致已有加密配置无法解密。

## 发现漏洞

不要在公开 Issue 中粘贴 Token、密码、数据库、日志或安装命令。先撤销暴露的凭据，再通过私密渠道报告。
