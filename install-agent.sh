#!/bin/sh
set -eu
SERVER="$1"; TOKEN="$2"
curl -fsSL "$SERVER/downloads/agent" -o /usr/local/bin/vps-pulse-agent
chmod +x /usr/local/bin/vps-pulse-agent
cat >/etc/systemd/system/vps-pulse-agent.service <<EOF
[Unit]
Description=VPS Pulse Agent
After=network-online.target
[Service]
ExecStart=/usr/local/bin/vps-pulse-agent -server $SERVER -token $TOKEN
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload
systemctl enable --now vps-pulse-agent
echo "VPS Pulse Agent 已安装"
