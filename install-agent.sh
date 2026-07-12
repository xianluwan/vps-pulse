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

if ! command -v systemctl >/dev/null 2>&1; then
  echo "当前系统未使用 systemd，暂不支持自动安装 Agent 服务。" >&2
  exit 1
fi

install_dependencies() {
  echo "正在检查并安装 Agent 所需工具..."
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update -y
    DEBIAN_FRONTEND=noninteractive apt-get install -y curl ca-certificates iputils-ping procps kmod
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y curl ca-certificates iputils procps-ng kmod
  elif command -v yum >/dev/null 2>&1; then
    yum install -y curl ca-certificates iputils procps-ng kmod
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache curl ca-certificates iputils procps kmod
  else
    echo "无法识别系统包管理器，请手动安装 curl、ca-certificates、ping、sysctl 和 kmod。" >&2
    exit 1
  fi

  for tool in curl ping sysctl; do
    if ! command -v "$tool" >/dev/null 2>&1; then
      echo "依赖安装失败：找不到 $tool" >&2
      exit 1
    fi
  done
  echo "Agent 所需工具已安装。"
}

enable_bbr() {
  echo "正在检查并开启 BBR..."
  modprobe tcp_bbr 2>/dev/null || true

  cat > /etc/sysctl.d/99-vps-pulse-bbr.conf <<'EOF'
net.core.default_qdisc=fq
net.ipv4.tcp_congestion_control=bbr
EOF

  sysctl -p /etc/sysctl.d/99-vps-pulse-bbr.conf >/dev/null 2>&1 || true

  CURRENT_CC="$(sysctl -n net.ipv4.tcp_congestion_control 2>/dev/null || true)"
  AVAILABLE_CC="$(sysctl -n net.ipv4.tcp_available_congestion_control 2>/dev/null || true)"
  CURRENT_QDISC="$(sysctl -n net.core.default_qdisc 2>/dev/null || true)"

  if [ "$CURRENT_CC" = "bbr" ]; then
    echo "BBR 已开启。"
    echo "拥塞控制算法: $CURRENT_CC"
    echo "默认队列算法: ${CURRENT_QDISC:-未知}"
    BBR_STATUS="已开启"
  else
    echo "警告：当前内核或虚拟化环境未能启用 BBR。" >&2
    echo "当前算法: ${CURRENT_CC:-未知}" >&2
    echo "可用算法: ${AVAILABLE_CC:-未知}" >&2
    echo "配置已保存，内核支持后可重启再检查。" >&2
    BBR_STATUS="未开启（内核或虚拟化环境不支持）"
  fi
}

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

install_dependencies
enable_bbr

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
  echo "BBR 状态: $BBR_STATUS"
else
  echo "Agent 安装完成，但服务启动失败。请执行以下命令查看日志：" >&2
  echo "journalctl -u $SERVICE -n 100 --no-pager" >&2
  exit 1
fi
