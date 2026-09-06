#!/bin/sh
# Не пускает в коммит то, чему не место в публичном репозитории: ключи, токены,
# пароли со значением, боевые адреса и личные данные.
#
#   scripts/check-secrets.sh            # проверить добавленное в индекс
#   scripts/check-secrets.sh --all      # проверить всё дерево
#   scripts/check-secrets.sh --self-test
#
# Ставится один раз: git config core.hooksPath .githooks
# Сам файл из проверки исключён — в нём лежат заведомо поддельные образцы.
set -u

# Секреты узнаются по форме — значение искать не нужно.
KEYS='-----BEGIN [A-Z ]*PRIVATE KEY-----|gh[pousr]_[A-Za-z0-9]{30,}|AKIA[0-9A-Z]{16}|sk-[A-Za-z0-9]{32,}|xox[baprs]-[A-Za-z0-9-]{10,}|[0-9]{8,10}:AA[A-Za-z0-9_-]{30,}|eyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.'
# Пароль или ключ, которому присвоено значение. Имя константы без значения — не улика.
CREDS='(password|passwd|secret_key|api_key|access_key)[[:space:]]*[:=][[:space:]]*.?[A-Za-z0-9/+_-]{12,}'
# Личная почта живого человека. Служебные адреса проекта не в счёт.
PERSONAL='[A-Za-z0-9._%+-]+@(gmail|yandex|mail|proton|outlook|icloud|yahoo)\.[A-Za-z]{2,}'
# Заведомо ненастоящие значения: примеры, лабораторные стенды, подстановки.
FAKE='example|lab-|labadmin|placeholder|not-for-production|change-?me|your-|\$\{|<[a-z-]+>|xxx|dummy|fake'
IPRE='(^|[^0-9A-Za-z._-])([0-9]{1,3}\.){3}[0-9]{1,3}([^0-9A-Za-z._-]|$)'
# Адреса, которые в коде и документации законны: служебные диапазоны, публичные
# резолверы, примеры из RFC 5737 и адреса лабораторной сети.
IP_OK='^(0\.0\.0\.0|10\.|100\.64\.|127\.|169\.254\.|17[23]\.(1[6-9]|2[0-9]|3[01])\.|172\.28\.|192\.0\.0\.|192\.0\.2\.|192\.88\.99\.|192\.168\.|198\.1[89]\.|198\.51\.100\.|203\.0\.113\.|224\.|240\.|255\.|1\.1\.1\.1|1\.0\.0\.1|8\.8\.8\.8|8\.8\.4\.4|9\.9\.9\.9|142\.250\.|1\.2\.3\.)'

scan() {
  txt=$(cat)
  printf '%s' "$txt" | grep -oE -e "$KEYS"                     | sed 's/^/  ключ или токен: /'
  printf '%s' "$txt" | grep -iE "$CREDS" | grep -viE "$FAKE" | cut -c1-110 | sed 's/^/  пароль со значением: /'
  printf '%s' "$txt" | grep -oiE "$PERSONAL"                | sed 's/^/  личная почта: /'
  printf '%s' "$txt" | grep -oE "$IPRE" | grep -oE '([0-9]{1,3}\.){3}[0-9]{1,3}' \
                     | grep -vE "$IP_OK" | sort -u          | sed 's/^/  боевой IP: /'
}

case "${1:-}" in
  --self-test)
    bad=$(printf '%s\n' \
      'HOST = "45.10.20.30"' \
      'token = "ghp_0123456789012345678901234567890123"' \
      'mail: someone@gmail.com' \
      'ADMIN_PASSWORD: hunter2hunter2' \
      '-----BEGIN PRIVATE KEY-----' | scan | wc -l | tr -d ' ')
    ok=$(printf '%s\n' \
      'upstream = "1.1.1.1:53"' \
      'const secretCFToken = "cloudflare_api_token"' \
      '## [2.0.0] — 2026-08-27' \
      '"caniuse-lite": "^1.0.30001809"' \
      'ING1=$(mk_node ingress ingress-lab-1 172.28.0.20)' \
      'PANEL_SECRET_KEY: lab-secret-key-not-for-production' \
      'addr = "192.0.2.10"' \
      'admin@example.net' | scan | wc -l | tr -d ' ')
    [ "$bad" -eq 5 ] && [ "$ok" -eq 0 ] && { echo "самопроверка пройдена"; exit 0; }
    echo "САМОПРОВЕРКА НЕ ПРОЙДЕНА: поймано $bad из 5, ложных срабатываний $ok" >&2
    printf '%s\n' 'upstream = "1.1.1.1:53"' 'const secretCFToken = "cloudflare_api_token"' \
      '## [2.0.0] — 2026-08-27' '"caniuse-lite": "^1.0.30001809"' \
      'ING1=$(mk_node ingress ingress-lab-1 172.28.0.20)' \
      'PANEL_SECRET_KEY: lab-secret-key-not-for-production' 'addr = "192.0.2.10"' 'admin@example.net' | scan >&2
    exit 1 ;;
  --all) found=$(git grep -hI '' -- . ':!*.lock' ':!*.sum' ':!package-lock.json'  ':!*.woff2' ':!scripts/check-secrets.sh' | scan) ;;
  *)     found=$(git diff --cached -U0 --no-color -- . ':!*.lock' ':!*.sum'  ':!package-lock.json' ':!scripts/check-secrets.sh' \
                 | grep '^+' | grep -v '^+++' | cut -c2- | scan) ;;
esac

[ -z "$found" ] && exit 0
echo "Коммит остановлен: в изменениях есть то, чему не место в публичном репозитории." >&2
echo "$found" >&2
echo "" >&2
echo "Уберите находку. Если это ложная тревога — git commit --no-verify." >&2
exit 1
