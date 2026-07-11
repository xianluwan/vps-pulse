# VPS Pulse

个人 Linux VPS 管理面板：Go 单服务、SQLite、内嵌 Web UI、Agent 主动连接。

## 一键部署

```bash
curl -fsSL https://raw.githubusercontent.com/xianluwan/vps-pulse/main/install.sh | sudo bash
```

安装完成后会在终端输出访问地址和随机管理员密码，并保存到 `/root/vps-pulse-credentials.txt`。

## 启动

```bash
cp .env.example .env
docker compose up -d --build
```

打开 `http://服务器IP:8080`。生产环境请配置 HTTPS。

功能：实时资源与流量、日/月/永久流量、动态 IPv4、连通性状态机、换 IP 命令、Cloudflare DNS、Telegram 通知和远程操作。


## 更新

cd /opt/vps-pulse

git pull

docker compose up -d --build
