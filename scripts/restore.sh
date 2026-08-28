#!/usr/bin/env bash
# Restores a control plane backup. Destructive: it replaces the current
# database and key material.
set -euo pipefail

FILE=""
DIR=/opt/smartdns-panel
PASSPHRASE="${BACKUP_PASSPHRASE:-}"
BACKUP_DIR="${BACKUP_DIR:-/var/backups/smartdns}"
ASSUME_YES=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --latest)  FILE=latest; shift;;
    --dir)     DIR="$2"; shift 2;;
    --yes|-y)  ASSUME_YES=1; shift;;
    -h|--help) echo "Использование: restore.sh <файл|--latest> [--dir каталог-панели] [--yes]"; exit 0;;
    -*)        echo "неизвестный аргумент: $1" >&2; exit 1;;
    *)         if [[ -z "$FILE" ]]; then FILE="$1"; elif [[ "$DIR" == /opt/smartdns-panel ]]; then DIR="$1"; fi; shift;;
  esac
done

# --latest, каталог или пусто → берём самую свежую копию из BACKUP_DIR.
if [[ "$FILE" == latest || -z "$FILE" || -d "$FILE" ]]; then
  search="${FILE:-$BACKUP_DIR}"; [[ "$FILE" == latest || -z "$FILE" ]] && search="$BACKUP_DIR"
  FILE="$(ls -t "$search"/smartdns-*.tar "$search"/smartdns-*.tar.enc 2>/dev/null | head -1 || true)"
  [[ -n "$FILE" ]] || { echo "В $search нет копий smartdns-*.tar[.enc]" >&2; exit 1; }
  echo "• Самая свежая копия: $FILE"
fi
[[ -f "$FILE" ]] || { echo "Файл не найден: $FILE" >&2; exit 1; }

if [[ -f "$FILE.sha256" ]]; then
  echo "• Проверка контрольной суммы"
  (cd "$(dirname "$FILE")" && (sha256sum -c "$(basename "$FILE").sha256" || shasum -a 256 -c "$(basename "$FILE").sha256"))
fi

cat <<WARN

ВНИМАНИЕ. Восстановление ЗАМЕНИТ текущую базу данных и ключевой материал
панели в $DIR. Ноды продолжат работать на своей последней конфигурации,
но их доверие к панели зависит от восстановленного CA: если вы
восстанавливаете на другой машине, сохраните тот же ключевой материал,
иначе потребуется повторная регистрация нод.

WARN
if [[ $ASSUME_YES -eq 0 ]]; then
  read -rp "Введите RESTORE для подтверждения: " a
  [[ "$a" == "RESTORE" ]] || { echo "Отменено."; exit 0; }
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [[ "$FILE" == *.enc ]]; then
  [[ -n "$PASSPHRASE" ]] || { echo "Задайте BACKUP_PASSPHRASE" >&2; exit 1; }
  openssl enc -d -aes-256-cbc -pbkdf2 -iter 200000 -in "$FILE" -out "$TMP/backup.tar" -pass pass:"$PASSPHRASE"
else
  cp "$FILE" "$TMP/backup.tar"
fi
tar -xf "$TMP/backup.tar" -C "$TMP"

cd "$DIR"
echo "• Остановка панели (база остаётся поднятой)"
docker compose stop panel-api

echo "• Восстановление базы"
DUMP="$(ls "$TMP"/db-*.dump | head -1)"
docker compose exec -T postgres psql -U "${POSTGRES_USER:-smartdns}" -d postgres \
  -c "DROP DATABASE IF EXISTS ${POSTGRES_DB:-smartdns} WITH (FORCE)" -c "CREATE DATABASE ${POSTGRES_DB:-smartdns}"
docker compose exec -T postgres pg_restore -U "${POSTGRES_USER:-smartdns}" -d "${POSTGRES_DB:-smartdns}" --no-owner < "$DUMP"

echo "• Восстановление ключевого материала"
STATE="$(ls "$TMP"/panelstate-*.tar | head -1)"
docker compose run --rm --no-deps -T --entrypoint sh panel-api -c 'rm -rf /var/lib/smartdns-panel/* && tar -xf - -C /var/lib/smartdns-panel' < "$STATE"

echo "• Запуск"
docker compose up -d panel-api
for _ in $(seq 1 40); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 3; done
curl -fsS http://127.0.0.1:8080/healthz >/dev/null || { echo "Панель не поднялась" >&2; exit 1; }
cat <<'DONE'
✓ Восстановление завершено. Панель работает с прежними CA и ключами —
  ноды доверяют ей по отпечатку сертификата, перевыпускать ничего не нужно.

  Если это переезд на НОВЫЙ адрес, на каждой ноде впустите новый IP панели:
      ufw allow  from <новый-IP> to any port 3333 proto tcp
      ufw delete allow from <старый-IP> to any port 3333 proto tcp
  и обновите DNS-запись панели. Проверка: раздел «Ноды» → колонка «Ревизия».
DONE
