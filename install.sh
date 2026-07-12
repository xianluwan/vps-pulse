#!/usr/bin/env bash
set -Eeuo pipefail

APP_NAME="VPS Pulse"
INSTALL_DIR="${VPS_PULSE_DIR:-/opt/vps-pulse}"
REPO_URL="${VPS_PULSE_REPO:-https://github.com/xianluwan/vps-pulse.git}"
BRANCH="${VPS_PULSE_BRANCH:-main}"
DOMAIN="${VPS_PULSE_DOMAIN:-}"

green='\033[0;32m'; yellow='\033[1;33m'; red='\033[0;31m'; reset='\033[0m'
info(){ printf "%b[+]%b %s\n" "$green" "$reset" "$*"; }
warn(){ printf "%b[!]%b %s\n" "$yellow" "$reset" "$*"; }
die(){ printf "%b[x]%b %s\n" "$red" "$reset" "$*" >&2; exit 1; }

[[ "${EUID}" -eq 0 ]] || die "请使用 root 执行，或在命令前加 sudo"
export DEBIAN_FRONTEND=noninteractive

if ! command -v curl >/dev/null || ! command -v git >/dev/null || ! command -v openssl >/dev/null; then
  info "安装基础工具"
  apt-get update -y
  apt-get install -y curl git ca-certificates openssl dnsutils
fi

if [[ -z "$DOMAIN" ]]; then
  if [[ -r /dev/tty ]]; then
    printf "请输入已经解析到本服务器的面板域名（例如 panel.example.com）: " >/dev/tty
    IFS= read -r DOMAIN </dev/tty
  else
    die "无交互环境必须设置域名，例如：curl ... | sudo env VPS_PULSE_DOMAIN=panel.example.com bash"
  fi
fi
DOMAIN="${DOMAIN#http://}"; DOMAIN="${DOMAIN#https://}"; DOMAIN="${DOMAIN%%/*}"; DOMAIN="${DOMAIN%.}"
[[ "$DOMAIN" =~ ^([a-zA-Z0-9-]+\.)+[a-zA-Z]{2,}$ ]] || die "域名格式无效：$DOMAIN"

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

PUBLIC_IP="$(curl -4fsS --max-time 8 https://api.ipify.org || true)"
DNS_IP="$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')"
info "服务器公网 IP：${PUBLIC_IP:-未知}"
info "域名当前解析：${DNS_IP:-未解析}"
if [[ -z "$DNS_IP" || ( -n "$PUBLIC_IP" && "$DNS_IP" != "$PUBLIC_IP" ) ]]; then
  warn "域名尚未解析到本服务器。请先添加 A 记录：$DOMAIN → $PUBLIC_IP"
  if [[ -r /dev/tty ]]; then
    printf "确认 DNS 已设置并继续等待生效？[y/N]: " >/dev/tty
    IFS= read -r answer </dev/tty
    [[ "$answer" =~ ^[Yy]([Ee][Ss])?$ ]] || die "已取消部署"
  else
    die "DNS 未指向当前服务器，无法自动申请 SSL"
  fi
  info "等待 DNS 生效，最长 5 分钟"
  for _ in $(seq 1 60); do
    DNS_IP="$(getent ahostsv4 "$DOMAIN" 2>/dev/null | awk 'NR==1{print $1}')"
    [[ -n "$DNS_IP" && ( -z "$PUBLIC_IP" || "$DNS_IP" == "$PUBLIC_IP" ) ]] && break
    sleep 5
  done
  [[ -n "$DNS_IP" && ( -z "$PUBLIC_IP" || "$DNS_IP" == "$PUBLIC_IP" ) ]] || die "DNS 在 5 分钟内未生效，请稍后重新运行安装命令"
fi

if [[ -d "${INSTALL_DIR}/.git" ]]; then
  info "更新现有项目"
  git -C "$INSTALL_DIR" fetch origin "$BRANCH"
  git -C "$INSTALL_DIR" checkout "$BRANCH"
  git -C "$INSTALL_DIR" pull --ff-only origin "$BRANCH"
elif [[ -e "$INSTALL_DIR" && -n "$(ls -A "$INSTALL_DIR" 2>/dev/null)" ]]; then
  die "$INSTALL_DIR 已存在且不是 Git 仓库，请先备份或设置 VPS_PULSE_DIR"
else
  info "下载 ${APP_NAME}"
  rm -rf "$INSTALL_DIR"
  git clone --depth 1 --branch "$BRANCH" "$REPO_URL" "$INSTALL_DIR"
fi

cd "$INSTALL_DIR"
mkdir -p data
chmod 700 data

EXISTING_PASSWORD=""
if [[ -f .env ]]; then
  EXISTING_PASSWORD="$(sed -n 's/^ADMIN_PASSWORD=//p' .env | head -n1)"
fi

ADMIN_PASSWORD="${VPS_PULSE_PASSWORD:-}"
if [[ -z "$ADMIN_PASSWORD" && -r /dev/tty ]]; then
  if [[ -n "$EXISTING_PASSWORD" ]]; then
    printf "设置新的管理员密码（直接回车保留现有密码）: " >/dev/tty
  else
    printf "设置管理员密码（直接回车自动生成强密码）: " >/dev/tty
  fi
  IFS= read -r -s ADMIN_PASSWORD </dev/tty
  printf "\n" >/dev/tty
  if [[ -n "$ADMIN_PASSWORD" ]]; then
    printf "再次输入管理员密码: " >/dev/tty
    IFS= read -r -s PASSWORD_CONFIRM </dev/tty
    printf "\n" >/dev/tty
    [[ "$ADMIN_PASSWORD" == "$PASSWORD_CONFIRM" ]] || die "两次输入的密码不一致"
  fi
fi

if [[ -z "$ADMIN_PASSWORD" ]]; then
  if [[ -n "$EXISTING_PASSWORD" ]]; then
    ADMIN_PASSWORD="$EXISTING_PASSWORD"
  else
    ADMIN_PASSWORD="$(openssl rand -base64 24 | tr -d '/+=' | head -c 20)"
    warn "未手动设置密码，已自动生成随机管理员密码"
  fi
fi

[[ ${#ADMIN_PASSWORD} -ge 12 ]] || die "管理员密码至少需要 12 位"
[[ "$ADMIN_PASSWORD" =~ ^[a-zA-Z0-9@%_+=:,.-]+$ ]] || die "密码只能包含英文字母、数字和 @%_+=:,.-"

upsert_env(){
  local key="$1" value="$2"
  if grep -q "^${key}=" .env 2>/dev/null; then
    sed -i "s|^${key}=.*|${key}=${value}|" .env
  else
    printf '%s=%s\n' "$key" "$value" >> .env
  fi
}

if [[ ! -f .env ]]; then
  cat > .env <<EOF
ADMIN_PASSWORD=${ADMIN_PASSWORD}
SESSION_SECRET=$(openssl rand -hex 32)
MASTER_KEY=$(openssl rand -hex 32)
DOMAIN=${DOMAIN}
PUBLIC_URL=https://${DOMAIN}
TELEGRAM_BOT_TOKEN=
TELEGRAM_ALLOWED_USER_ID=
DATABASE_PATH=/data/panel.db
TZ=Asia/Shanghai
EOF
else
  upsert_env ADMIN_PASSWORD "$ADMIN_PASSWORD"
  upsert_env DOMAIN "$DOMAIN"
  upsert_env PUBLIC_URL "https://${DOMAIN}"
  warn "保留现有数据库、密钥和管理员密码"
fi
chmod 600 .env

if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q '^Status: active'; then
  info "开放 HTTPS 所需端口"
  ufw allow 80/tcp >/dev/null
  ufw allow 443/tcp >/dev/null
fi

info "构建并启动面板与 HTTPS 反向代理"
docker compose up -d --build

info "等待 SSL 证书签发"
READY=0
for _ in $(seq 1 60); do
  if curl -fsS --max-time 8 "https://${DOMAIN}/" >/dev/null 2>&1; then READY=1;break;fi
  sleep 3
done
if [[ "$READY" -ne 1 ]]; then
  docker compose logs --tail=80 caddy >&2 || true
  die "HTTPS 在 3 分钟内未就绪。请检查云防火墙是否开放 80/443，以及域名是否启用了错误的代理或 AAAA 记录"
fi

ACCESS_URL="https://${DOMAIN}"
CREDENTIAL_FILE="/root/vps-pulse-credentials.txt"
cat > "$CREDENTIAL_FILE" <<EOF
VPS Pulse 管理面板
地址: ${ACCESS_URL}
管理员密码: ${ADMIN_PASSWORD}
安装目录: ${INSTALL_DIR}
EOF
chmod 600 "$CREDENTIAL_FILE"

printf "\n%b================================================%b\n" "$green" "$reset"
printf "%b  %s HTTPS 部署完成%b\n" "$green" "$APP_NAME" "$reset"
printf "  访问地址: %s\n" "$ACCESS_URL"
printf "  管理密码: %s\n" "$ADMIN_PASSWORD"
printf "  凭据文件: %s（仅 root 可读）\n" "$CREDENTIAL_FILE"
printf "  查看日志: cd %s && docker compose logs -f\n" "$INSTALL_DIR"
printf "%b================================================%b\n\n" "$green" "$reset"
