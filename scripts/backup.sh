#!/usr/bin/env bash
# Creates an encrypted, checksummed backup of the control plane.
# Contents: PostgreSQL dump, panel CA + manifest signing key, declarative config.
# Runtime logs are deliberately excluded.
set -euo pipefail

DIR="${1:-/opt/smartdns-panel}"
OUT="${BACKUP_DIR:-/var/backups/smartdns}"
PASSPHRASE="${BACKUP_PASSPHRASE:-}"
TS="$(date -u +%Y%m%dT%H%M%SZ)"

command -v docker >/dev/null || { echo "Docker не найден" >&2; exit 1; }
mkdir -p "$OUT"
cd "$DIR"

echo "• Дамп базы данных"
docker compose exec -T postgres pg_dump -U "${POSTGRES_USER:-smartdns}" -Fc "${POSTGRES_DB:-smartdns}" > "$OUT/db-$TS.dump"

echo "• Ключевой материал панели"
docker compose exec -T panel-api tar -cf - -C /var/lib/smartdns-panel . > "$OUT/panelstate-$TS.tar"

echo "• Объединение"
tar -cf "$OUT/smartdns-$TS.tar" -C "$OUT" "db-$TS.dump" "panelstate-$TS.tar"
rm -f "$OUT/db-$TS.dump" "$OUT/panelstate-$TS.tar"

if [[ -n "$PASSPHRASE" ]]; then
  echo "• Шифрование"
  openssl enc -aes-256-cbc -pbkdf2 -iter 200000 -salt \
    -in "$OUT/smartdns-$TS.tar" -out "$OUT/smartdns-$TS.tar.enc" -pass pass:"$PASSPHRASE"
  rm -f "$OUT/smartdns-$TS.tar"
  FILE="$OUT/smartdns-$TS.tar.enc"
else
  echo "  ВНИМАНИЕ: BACKUP_PASSPHRASE не задан — копия содержит приватные ключи в открытом виде."
  FILE="$OUT/smartdns-$TS.tar"
fi

sha256sum "$FILE" > "$FILE.sha256" 2>/dev/null || shasum -a 256 "$FILE" > "$FILE.sha256"
chmod 600 "$FILE" "$FILE.sha256"
echo "✓ Готово: $FILE"
echo "  Проверьте восстановление хотя бы раз в квартал: scripts/restore.sh $FILE"
