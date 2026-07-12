#!/bin/sh
set -eu

SERVER="${1:-}"
TOKEN="${2:-}"
MODE="${3:-}"
SERVICE="vps-pulse-agent"
SERVICE_FILE="/etc/systemd/system/${SERVICE}.service"
BINARY="/usr/local/bin/vps-pulse-agent"

if [ -z "$SERVER" ] || [ -z "$TOKEN" ]; then
  echo "用法: install-agent.sh <面板地址> <Agent Token> [--force]" >&2
  exit 1
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "请使用 root 执行，或在命令前加 sudo。" >&2
  exit 1
fi

EXISTING=0
[ -f "$SERVICE_FILE" ] && EXISTING=1
[ -f "$BINARY" ] && EXISTING=1

if [ "$EXISTING" -eq 1 ] && [ "$MODE" != "--force" ]; then
  echo
  echo "检测到 VPS Pulse Agent 已经安装。"
  if systemctl is-active --quiet "$SERVICE" 2>/dev/null; then
    echo "当前状态: 正在运行"
  else
    echo "当前状态: 未运行或异常"
  fi
  echo "现有程序: $BINARY"
  echo "现有服务: $SERVICE_FILE"
  printf "是否停止旧 Agent 并覆盖安装？[y/N]: "
  if [ -r /dev/tty ]; then
    IFS= read -r ANSWER </dev/tty || ANSWER=""
  else
    echo
    echo "当前环境无法交互，已取消覆盖。需要强制覆盖时在命令末尾增加 --force。" >&2
    exit 2
  fi
  case "$ANSWER" in
    y|Y|yes|YES|Yes) ;;
    *) echo "已取消，现有 Agent 未发生改变。"; exit 0 ;;
  esac
fi

if [ "$EXISTING" -eq 1 ]; then
  echo "正在停止旧 Agent..."
  systemctl stop "$SERVICE" 2>/dev/null || true
fi

TMP_FILE="$(mktemp /tmp/vps-pulse-agent.XXXXXX)"
trap 'rm -f "$TMP_FILE"' EXIT INT TERM

echo "正在下载新版 Agent..."
curl -fsSL "$SERVER/downloads/agent" -o "$TMP_FILE"
chmod 755 "$TMP_FILE"
mv -f "$TMP_FILE" "$BINARY"
trap - EXIT INT TERM

cat >"$SERVICE_FILE" <<EOF
[Unit]
Description=VPS Pulse Agent
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BINARY -server $SERVER -token $TOKEN
Restart=always
RestartSec=3

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable "$SERVICE" >/dev/null 2>&1
systemctl restart "$SERVICE"

sleep 1
if systemctl is-active --quiet "$SERVICE"; then
  if [ "$EXISTING" -eq 1 ]; then
    echo "VPS Pulse Agent 已覆盖安装并成功启动。"
  else
    echo "VPS Pulse Agent 已安装并成功启动。"
  fi
else
  echo "Agent 安装完成，但服务启动失败。请执行以下命令查看日志：" >&2
  echo "journalctl -u $SERVICE -n 100 --no-pager" >&2
  exit 1
fi
