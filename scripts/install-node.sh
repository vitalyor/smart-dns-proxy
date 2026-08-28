#!/usr/bin/env bash
# Installs an ingress or egress node under the push model. Copy the bundle from
# the panel (Ноды → Добавить ноду) — it carries the node's TLS identity and pins
# the panel, exactly like remnanode's SECRET_KEY. The panel then connects to
# this node; the node never dials out.
#
#   sudo bash install-node.sh --role ingress --bundle <BASE64> --panel-ip 203.0.113.9
set -euo pipefail

ROLE=""; BUNDLE=""; PANEL_IP=""; DIR=/opt/smartdns-node
MGMT_PORT=3333; RELAY_PORT=8443; DOH_PORT=8443
ASSUME_YES=0
SRC="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

die() { printf '\033[31mОшибка:\033[0m %s\n' "$*" >&2; exit 1; }
info() { printf '\033[36m•\033[0m %s\n' "$*"; }
ok() { printf '\033[32m✓\033[0m %s\n' "$*"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role) ROLE="$2"; shift 2;;
    --bundle) BUNDLE="$2"; shift 2;;
    --panel-ip) PANEL_IP="$2"; shift 2;;
    --dir) DIR="$2"; shift 2;;
    --mgmt-port) MGMT_PORT="$2"; shift 2;;
    --relay-port) RELAY_PORT="$2"; shift 2;;
    --doh-port) DOH_PORT="$2"; shift 2;;
    --yes|-y) ASSUME_YES=1; shift;;
    -h|--help) sed -n '2,9p' "$0"; exit 0;;
    *) die "неизвестный аргумент: $1";;
  esac
done

[[ $EUID -eq 0 ]] || die "запустите через sudo"
[[ "$ROLE" == "ingress" || "$ROLE" == "egress" ]] || die "--role должен быть ingress или egress"

# Бандл можно передать флагом --bundle или вставить по запросу. Второе чище:
# секрет не остаётся в истории shell и в логах терминала.
if [[ -z "$BUNDLE" ]]; then
  printf '\n%sВставьте бандл из панели%s (Ноды → Добавить ноду → Копировать бандл), затем Enter:\n' "$(tput bold 2>/dev/null)" "$(tput sgr0 2>/dev/null)"
  while [[ -z "$BUNDLE" ]]; do
    read -rp "Бандл: " BUNDLE </dev/tty || die "ввод прерван"
    BUNDLE="${BUNDLE//[[:space:]]/}"   # убираем переносы/пробелы, если вставка их добавила
  done
fi
# Лёгкая проверка: бандл должен быть корректным base64 (сам агент проверит глубже).
printf '%s' "$BUNDLE" | base64 -d >/dev/null 2>&1 || die "бандл не похож на base64 — скопируйте его из панели целиком"
command -v docker >/dev/null || die "Docker не установлен"
docker compose version >/dev/null 2>&1 || die "нужен Docker Compose v2"

# --- preflight ---------------------------------------------------------------
info "Проверка портов и времени"
need_ports=("$MGMT_PORT")
if [[ "$ROLE" == "ingress" ]]; then need_ports+=(53 443 853 "$DOH_PORT"); else need_ports+=("$RELAY_PORT"); fi
for p in "${need_ports[@]}"; do
  if command -v ss >/dev/null && ss -lnt "sport = :$p" 2>/dev/null | grep -q LISTEN; then
    die "порт $p занят (частая причина на 53: systemd-resolved — отключите DNSStubListener)"
  fi
done
if command -v timedatectl >/dev/null; then
  timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes \
    || info "предупреждение: время не синхронизировано по NTP; расхождение часов ломает mTLS"
fi
ok "предварительные проверки пройдены"

cat <<PLAN

План установки ноды
  роль             $ROLE
  каталог          $DIR
  порт управления  $MGMT_PORT (сюда подключается панель)
  открыть порты    ${need_ports[*]}
  панель ходит с   ${PANEL_IP:-<любого адреса — задайте --panel-ip, чтобы ограничить>}

PLAN
if [[ $ASSUME_YES -eq 0 ]]; then
  read -rp "Продолжить? [y/N] " a
  [[ "$a" == "y" || "$a" == "Y" ]] || { echo "Отменено."; exit 0; }
fi

mkdir -p "$DIR"
if [[ ! -f "$DIR/docker-compose.yml" ]]; then
  [[ -f "$SRC/node/deploy/$ROLE/docker-compose.yml" ]] || die "не найден compose роли $ROLE в $SRC/node/deploy/$ROLE"
  cp "$SRC/node/deploy/$ROLE/docker-compose.yml" "$DIR/docker-compose.yml"
  ok "compose роли $ROLE установлен в $DIR (образы тянутся из реестра — сборка на сервере не нужна)"
fi

umask 077
cat > "$DIR/.env" <<ENV
NODE_BUNDLE=$BUNDLE
MGMT_BIND=$MGMT_PORT
RELAY_PORT=$RELAY_PORT
DOH_PORT=$DOH_PORT
SMARTDNS_VERSION=2.0.0
LOG_LEVEL=
LOG_MAX_SIZE=10m
LOG_MAX_FILE=3
ALLOW_SELF_SIGNED_TLS=0
ENV
chmod 600 "$DIR/.env"

info "Загрузка образов из реестра и запуск"
cd "$DIR"
docker compose --env-file .env up -d --pull always

info "Ожидание, пока агент начнёт слушать порт управления"
for _ in $(seq 1 40); do
  if docker compose logs node-agent 2>/dev/null | grep -q 'agent listening'; then break; fi
  sleep 3
done
docker compose logs node-agent 2>/dev/null | grep -q 'agent listening' \
  || die "агент не поднялся. Смотрите: docker compose -f $DIR/docker-compose.yml logs node-agent"
ok "агент слушает порт $MGMT_PORT, ждёт подключения панели"

# --- firewall ----------------------------------------------------------------
if command -v ufw >/dev/null; then
  if [[ -n "$PANEL_IP" ]]; then
    ufw allow from "$PANEL_IP" to any port "$MGMT_PORT" proto tcp comment 'SmartDNS panel push' >/dev/null 2>&1 || true
    ok "ufw: порт $MGMT_PORT открыт только для панели $PANEL_IP"
  else
    info "не задан --panel-ip: откройте порт $MGMT_PORT ТОЛЬКО для адреса панели вручную:"
    printf '     ufw allow from <PANEL_IP> to any port %s proto tcp\n' "$MGMT_PORT"
  fi
fi

cat <<DONE

Нода поднята и ждёт панель. Дальше — в панели:
  1. Убедитесь, что адрес управления ноды указан верно (этот сервер:$MGMT_PORT).
  2. Добавьте ноду в группу ($ROLE) и соберите ревизию — панель протолкнёт конфиг.
DONE
if [[ "$ROLE" == "ingress" ]]; then
cat <<CHECK
  Открыть на этой ноде для устройств:
       ufw allow 853/tcp                       # DoT (Android Private DNS)
       ufw allow from <VPN-подсеть> to any port 53   # обычный DNS только для своих
       # 443 для SNI-прокси и DoH откройте согласно вашей раскладке
CHECK
else
cat <<CHECK
  Открыть на этой ноде ТОЛЬКО для ingress-нод:
       ufw allow from <IP-ingress> to any port $RELAY_PORT proto tcp
CHECK
fi
