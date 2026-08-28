#!/usr/bin/env bash
# Upgrades the control plane: backup, preflight, migration gate, rolling update.
set -euo pipefail

DIR="${1:-/opt/smartdns-panel}"
cd "$DIR"

echo "• Резервная копия перед обновлением"
"$(dirname "${BASH_SOURCE[0]}")/backup.sh" "$DIR"

echo "• Текущая версия"
docker compose exec -T panel-api /usr/local/bin/panel-api -version || true

echo "• Сборка новых образов"
docker compose build

cat <<WARN

Миграции базы применяются автоматически при старте и только вперёд.
Релизы придерживаются схемы expand/migrate/contract: несовместимые
изменения не выходят в одном релизе. Если обновление не удастся,
восстановите копию: scripts/restore.sh <файл>

WARN
read -rp "Продолжить обновление? [y/N] " a
[[ "$a" == "y" || "$a" == "Y" ]] || { echo "Отменено."; exit 0; }

docker compose up -d --no-deps panel-api
for _ in $(seq 1 40); do curl -fsS http://127.0.0.1:8080/healthz >/dev/null 2>&1 && break; sleep 3; done
if ! curl -fsS http://127.0.0.1:8080/healthz >/dev/null; then
  echo "Панель не прошла health check. Журналы: docker compose logs panel-api" >&2
  exit 1
fi
echo "✓ Панель обновлена."
echo "  Ноды обновляйте по одной: на каждой выполните docker compose pull && docker compose up -d"
echo "  Начните с одной ноды каждой роли и дождитесь, пока в панели она станет healthy."
