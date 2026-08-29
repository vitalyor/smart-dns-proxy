#!/usr/bin/env bash
# Installs the SmartDNS control plane with Docker Compose.
#
#   curl -fsSLO https://RELEASE/install-panel.sh
#   less install-panel.sh          # read it before running it
#   sha256sum -c install-panel.sh.sha256
#   sudo bash install-panel.sh
#
# Non-interactive:
#   sudo bash install-panel.sh --public-url https://panel.example.net \
#        --admin-email you@example.net --dir /opt/smartdns-panel --yes
set -euo pipefail

DIR=/opt/smartdns-panel
PUBLIC_URL=""
ADMIN_EMAIL=""
BACKUP_DIR=/var/backups/smartdns
TZ_NAME="${TZ:-Europe/Moscow}"
ASSUME_YES=0
PANEL_PORT=8080

die() { printf '\033[31mОшибка:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m•\033[0m %s\n' "$*"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DIR="$2"; shift 2;;
    --public-url) PUBLIC_URL="$2"; shift 2;;
    --admin-email) ADMIN_EMAIL="$2"; shift 2;;
    --backup-dir) BACKUP_DIR="$2"; shift 2;;
    --panel-port) PANEL_PORT="$2"; shift 2;;
    --timezone) TZ_NAME="$2"; shift 2;;
    --yes|-y) ASSUME_YES=1; shift;;
    -h|--help) sed -n '2,14p' "$0"; exit 0;;
    *) die "неизвестный аргумент: $1";;
  esac
done

# --- preflight ---------------------------------------------------------------
info "Проверка окружения"
[[ $EUID -eq 0 ]] || die "запустите через sudo"
command -v docker >/dev/null || die "Docker не установлен: https://docs.docker.com/engine/install/"
docker compose version >/dev/null 2>&1 || die "нужен Docker Compose v2 (плагин docker compose)"
case "$(uname -m)" in x86_64|aarch64|arm64) ;; *) die "неподдерживаемая архитектура $(uname -m)";; esac

if command -v ss >/dev/null && ss -lnt "sport = :$PANEL_PORT" 2>/dev/null | grep -q LISTEN; then
  die "порт $PANEL_PORT уже занят"
fi
avail_kb=$(df -Pk "$(dirname "$DIR")" | awk 'NR==2{print $4}')
[[ ${avail_kb:-0} -gt 5000000 ]] || die "нужно минимум 5 ГиБ свободного места в $(dirname "$DIR")"
ok "окружение подходит"

[[ -n "$PUBLIC_URL" ]] || read -rp "Публичный URL панели [http://localhost:$PANEL_PORT]: " PUBLIC_URL
PUBLIC_URL="${PUBLIC_URL:-http://localhost:$PANEL_PORT}"
[[ -n "$ADMIN_EMAIL" ]] || read -rp "Email администратора: " ADMIN_EMAIL
[[ -n "$ADMIN_EMAIL" ]] || die "email администратора обязателен"

cat <<PLAN

План установки
  каталог           $DIR
  публичный URL     $PUBLIC_URL
  модель            push — панель сама подключается к нодам на их порт 3333
  админ             $ADMIN_EMAIL
  резервные копии   $BACKUP_DIR
  часовой пояс      $TZ_NAME

Будут созданы: каталог установки, файл .env со случайными секретами,
том PostgreSQL и два контейнера. Существующий .env не перезаписывается.

PLAN

if [[ $ASSUME_YES -eq 0 ]]; then
  read -rp "Продолжить? [y/N] " a
  [[ "$a" == "y" || "$a" == "Y" ]] || { echo "Отменено."; exit 0; }
fi

mkdir -p "$DIR" "$BACKUP_DIR"
chmod 750 "$DIR"

if [[ -f "$DIR/.env" ]]; then
  info ".env уже существует, секреты сохранены без изменений"
else
  umask 077
  cat > "$DIR/.env" <<ENV
PANEL_PORT=$PANEL_PORT
PANEL_PUBLIC_URL=$PUBLIC_URL
PANEL_SECRET_KEY=$(openssl rand -base64 32)
POSTGRES_PASSWORD=$(openssl rand -base64 24 | tr -d '/+=')
POSTGRES_USER=smartdns
POSTGRES_DB=smartdns
ADMIN_EMAIL=$ADMIN_EMAIL
ADMIN_PASSWORD=
SMARTDNS_VERSION=2.0.2
TZ=$TZ_NAME
LOG_LEVEL=
LOG_MAX_SIZE=10m
LOG_MAX_FILE=3
LAB_MODE=0
# Необязательно: публичный репозиторий (owner/name), на который указывает
# однострочная команда установки ноды. Пусто → встроенный по умолчанию.
GITHUB_REPO=
BACKUP_DIR=$BACKUP_DIR
BACKUP_INTERVAL_SEC=86400
BACKUP_RETENTION_DAYS=14
BACKUP_PASSPHRASE=$(openssl rand -base64 24 | tr -d '/+=')
ENV
  chmod 600 "$DIR/.env"
  ok "создан $DIR/.env со случайными секретами (режим 600)"
fi

if [[ ! -f "$DIR/docker-compose.yml" ]]; then
  SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
  if [[ -f "$SRC/docker-compose.yml" ]]; then
    cp "$SRC/docker-compose.yml" "$DIR/"
    cp -r "$SRC/deploy" "$SRC/panel" "$SRC/agent" "$SRC/node" "$SRC/shared" \
          "$SRC/go.mod" "$SRC/go.sum" "$DIR/" 2>/dev/null || true
    ok "исходники скопированы в $DIR"
  else
    die "рядом со скриптом нет docker-compose.yml: распакуйте релиз и запустите скрипт из его каталога"
  fi
fi

info "Сборка и запуск (первый раз это занимает несколько минут)"
cd "$DIR"
docker compose --env-file .env up -d --build

info "Ожидание готовности"
for _ in $(seq 1 60); do
  if curl -fsS "http://127.0.0.1:$PANEL_PORT/healthz" >/dev/null 2>&1; then break; fi
  sleep 3
done
curl -fsS "http://127.0.0.1:$PANEL_PORT/healthz" >/dev/null || die "панель не поднялась: docker compose logs panel-api"

ok "Панель работает"
cat <<DONE

  Откройте:         $PUBLIC_URL
  Логин:            $ADMIN_EMAIL
  Пароль:           напечатан один раз в журнале:
                    docker compose -f $DIR/docker-compose.yml logs panel-api | grep -A3 'Учётная запись'

  Ключевой материал (CA и ключ подписи манифестов): том smartdns-panel_panelstate
  Резервные копии:  $BACKUP_DIR (автоматически, ежедневно, зашифрованы)

  ПАРОЛЬ ШИФРОВАНИЯ КОПИЙ — сохраните, без него копию не восстановить:
$(if [[ -f "$DIR/.env" ]]; then grep '^BACKUP_PASSPHRASE=' "$DIR/.env" | sed 's/^BACKUP_PASSPHRASE=/                    /'; fi)

  Переезд на новый сервер одной командой (после копирования файла копии туда):
    scp $BACKUP_DIR/smartdns-*.tar.enc  root@новый:/tmp/
    # на новом сервере:
    sudo bash install.sh --role panel --public-url <URL> --admin-email <you> \\
         --restore /tmp/smartdns-<...>.tar.enc

  Дальше: раздел «Быстрый старт» в панели.

DONE
