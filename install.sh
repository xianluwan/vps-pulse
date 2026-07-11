#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="VPS Pulse"
INSTALL_DIR="${VPS_PULSE_DIR:-/opt/vps-pulse}"
REPO_URL="${VPS_PULSE_REPO:-https://github.com/OWNER/vps-pulse.git}"
BRANCH="${VPS_PULSE_BRANCH:-main}"
PORT="${VPS_PULSE_PORT:-8080}"

green='\033[0;32m'; yellow='\033[1;33m'; red='\033[0;31m'; reset='\033[0m'
info(){ printf "%b[+]%b %s\n" "$green" "$reset" "$*"; }
warn(){ printf "%b[!]%b %s\n" "$yellow" "$reset" "$*"; }
die(){ printf "%b[x]%b %s\n" "$red" "$reset" "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "请使用 root 执行，或在命令前加 sudo"
[[ "${REPO_URL}" != *"OWNER/"* ]] || die "项目发布者尚未在 install.sh 中设置 GitHub 仓库地址"

export DEBIAN_FRONTEND=noninteractive
if ! command -v curl >/dev/null || ! command -v git >/dev/null; then
  info "安装基础工具"
  apt-get update -y
  apt-get install -y curl git ca-certificates openssl
fi

if ! command -v docker >/dev/null; then
  info "安装 Docker"
  curl -fsSL https://get.docker.com | sh
fi

if ! docker compose version >/dev/null 2>&1; then
  info "安装 Docker Compose 插件"
  apt-get update -y
  apt-get install -y docker-compose-plugin
fi

systemctl enable --now docker >/dev/null 2>&1 || true

if [[ -d "${INSTALL_DIR}/.git" ]]; then
  info "更新现有项目"
  git -C "$INSTALL_DIR" fetch origin "$BRANCH"
  git -C "$INSTALL_DIR" checkout "$BRANCH"
  git -C "$INSTALL_DIR" pull --ff-only origin "$BRANCH"
elif [[ -e "$INSTALL_DIR" && -n "$(ls -A "$INSTALL_DIR" 2>/dev/null)" ]]; then
  die "$INSTALL_DIR 已存在且不是 Git 仓库。请先备份或设置 VPS_PULSE_DIR"
else
  info "下载 ${APP_NAME}"
  rm -rf "$INSTALL_DIR"
  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$INSTALL_DIR"
fi

cd "$INSTALL_DIR"
mkdir -p data

PUBLIC_IP="$(curl -4fsS --max-time 8 https://api.ipify.org || true)"
[[ -n "$PUBLIC_IP" ]] || PUBLIC_IP="$(hostname -I | awk '{print $1}')"
[[ -n "$PUBLIC_IP" ]] || PUBLIC_IP="SERVER_IP"

if [[ ! -f .env ]]; then
  ADMIN_PASSWORD="$(openssl rand -base64 18 | tr -d '/+=' | head -c 18)"
  SESSION_SECRET="$(openssl rand -hex 32)"
  MASTER_KEY="$(openssl rand -hex 32)"
  cat > .env <<EOF
ADMIN_PASSWORD=${ADMIN_PASSWORD}
SESSION_SECRET=${SESSION_SECRET}
MASTER_KEY=${MASTER_KEY}
PUBLIC_URL=http://${PUBLIC_IP}:${PORT}
TELEGRAM_BOT_TOKEN=
TELEGRAM_ALLOWED_USER_ID=
DATABASE_PATH=/data/panel.db
TZ=Asia/Shanghai
EOF
  chmod 600 .env
else
  ADMIN_PASSWORD="$(sed -n 's/^ADMIN_PASSWORD=//p' .env | head -n1)"
  warn "保留已有 .env 和管理员密码"
fi

info "构建并启动面板（首次构建可能需要几分钟）"
docker compose up -d --build

for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:${PORT}/" >/dev/null 2>&1; then break; fi
  sleep 2
done

ACCESS_URL="http://${PUBLIC_IP}:${PORT}"
CREDENTIAL_FILE="/root/vps-pulse-credentials.txt"
cat > "$CREDENTIAL_FILE" <<EOF
VPS Pulse 管理面板
地址: ${ACCESS_URL}
管理员密码: ${ADMIN_PASSWORD}
安装目录: ${INSTALL_DIR}
EOF
chmod 600 "$CREDENTIAL_FILE"

printf "\n%b================================================%b\n" "$green" "$reset"
printf "%b  %s 部署完成%b\n" "$green" "$APP_NAME" "$reset"
printf "  访问地址: %s\n" "$ACCESS_URL"
printf "  管理密码: %s\n" "$ADMIN_PASSWORD"
printf "  凭据文件: %s（仅 root 可读）\n" "$CREDENTIAL_FILE"
printf "  查看日志: cd %s && docker compose logs -f panel\n" "$INSTALL_DIR"
printf "%b================================================%b\n\n" "$green" "$reset"
warn "当前使用 HTTP。正式使用前请配置域名和 HTTPS。"
