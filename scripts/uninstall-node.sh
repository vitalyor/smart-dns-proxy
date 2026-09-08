#!/usr/bin/env bash
# Снимает ноду SmartDNS с сервера: контейнеры, тома, каталог установки,
# образы и правило ufw, которое открывало порт управления панели.
#
#   sudo bash -c "$(curl -fsSL https://raw.githubusercontent.com/vitalyor/smart-dns-proxy/main/scripts/uninstall-node.sh)"
#   sudo bash scripts/uninstall-node.sh --yes
#
# Панель об этом не узнаёт: запись ноды удаляется отдельно, в самой панели.
set -euo pipefail

DIR=/opt/smartdns-node
SRC_DIR=/opt/smartdns-src
ASSUME_YES=0
KEEP_IMAGES=0

die()  { printf '\033[31mОшибка:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m•\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m✓\033[0m %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --dir) DIR="$2"; shift 2;;
    --keep-images) KEEP_IMAGES=1; shift;;
    --yes|-y) ASSUME_YES=1; shift;;
    -h|--help) sed -n '2,9p' "$0"; exit 0;;
    *) die "неизвестный аргумент: $1";;
  esac
done

[[ $EUID -eq 0 ]] || die "запустите через sudo"
command -v docker >/dev/null || die "Docker не установлен — снимать нечего"

# Роль нужна только для внятного плана: сами контейнеры compose найдёт по каталогу.
ROLE="неизвестна"
[[ -f "$DIR/docker-compose.yml" ]] && ROLE=$(sed -n 's/^name: smartdns-//p' "$DIR/docker-compose.yml" | head -1)

cat <<PLAN

Что будет удалено с этого сервера
  роль             ${ROLE:-неизвестна}
  контейнеры       все контейнеры ноды
  тома             состояние агента, включая его TLS-личность
  каталог          $DIR (ключ подключения, .env, сертификаты)
  образы           $([[ $KEEP_IMAGES -eq 1 ]] && echo 'остаются (--keep-images)' || echo 'ghcr.io/*/smartdns-*')
  правило ufw      открытие порта управления для панели

Отменить это нельзя: заново нода поднимается только новым ключом из панели.

PLAN
if [[ $ASSUME_YES -eq 0 ]]; then
  read -rp "Удалить? [y/N] " a </dev/tty
  [[ "$a" == "y" || "$a" == "Y" ]] || { echo "Отменено."; exit 0; }
fi

# NODE_BUNDLE=- : в compose он объявлен обязательным, и без .env даже `down`
# отказался бы работать. Для остановки значение не нужно, важно лишь непустое.
if [[ -f "$DIR/docker-compose.yml" ]]; then
  info "Останавливаю контейнеры и удаляю тома"
  (cd "$DIR" && NODE_BUNDLE=- docker compose down -v --remove-orphans) || die "compose down не отработал — смотрите вывод выше"
  ok "контейнеры и тома удалены"
else
  info "нет $DIR/docker-compose.yml — пропускаю остановку контейнеров"
fi

if [[ -d "$DIR" ]]; then
  rm -rf "$DIR"
  ok "каталог $DIR удалён"
fi
# Установщик кладёт сюда скачанные compose и install-node.sh — исходников ноды тут нет.
[[ -d "$SRC_DIR" ]] && rm -rf "$SRC_DIR" && ok "временные файлы установщика $SRC_DIR удалены"

if [[ $KEEP_IMAGES -eq 0 ]]; then
  imgs=$(docker images --filter=reference='ghcr.io/*/smartdns-*' -q | sort -u)
  if [[ -n "$imgs" ]]; then
    # shellcheck disable=SC2086
    docker rmi $imgs >/dev/null 2>&1 || true
    ok "образы ноды удалены"
  fi
fi

# Правило ufw установщик помечал комментарием — по нему и находим. Удаляем с
# конца: после каждого удаления номера оставшихся правил сдвигаются.
if command -v ufw >/dev/null; then
  nums=$(ufw status numbered 2>/dev/null | grep -i 'SmartDNS' | sed -n 's/^\[ *\([0-9]\{1,\}\)\].*/\1/p' | sort -rn)
  for n in $nums; do yes | ufw delete "$n" >/dev/null 2>&1 || true; done
  [[ -n "$nums" ]] && ok "правила ufw, поставленные установщиком, сняты"
fi

# Разрешение низких портов ставилось ради наших контейнеров — снимаем.
if [[ -f /etc/sysctl.d/99-smartdns.conf ]]; then
  rm -f /etc/sysctl.d/99-smartdns.conf
  sysctl -q -w net.ipv4.ip_unprivileged_port_start=1024 2>/dev/null || true
  ok "низкие порты снова только для root"
fi

# Ingress мог забрать порт 53 у systemd-resolved. Возвращаем как было.
if [[ -f /etc/systemd/resolved.conf.d/smartdns.conf ]]; then
  rm -f /etc/systemd/resolved.conf.d/smartdns.conf
  systemctl restart systemd-resolved >/dev/null 2>&1 || true
  ok "порт 53 возвращён systemd-resolved"
fi

cat <<DONE

Нода снята с сервера.
Осталось удалить её запись в панели: Ноды → нужная нода → Удалить.
Без этого панель будет и дальше стучаться на этот адрес и показывать тревогу.
DONE
