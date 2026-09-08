#!/usr/bin/env bash
# Installs an ingress or egress node under the push model. Copy the connection
# key from the panel (Ноды → Добавить ноду) — it carries the node's TLS identity
# and pins the panel, exactly like remnanode's SECRET_KEY. The panel then
# connects to this node; the node never dials out.
#
#   sudo bash install-node.sh --role ingress --bundle <BASE64> --panel-ip 203.0.113.9
set -euo pipefail

ROLE=""; BUNDLE=""; PANEL_IP=""; INGRESS_IP=""; DIR=/opt/smartdns-node
SMARTDNS_VERSION="${SMARTDNS_VERSION:-0.4.0}"
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
    --ingress-ip) INGRESS_IP="$2"; shift 2;;
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
# Человекочитаемая подпись роли для вывода оператору (сам ROLE — служебный).
if [[ "$ROLE" == "ingress" ]]; then ROLE_LABEL="точка входа"; else ROLE_LABEL="точка выхода"; fi

# Ключ можно передать флагом --bundle или вставить по запросу. Второе чище:
# секрет не остаётся в истории shell и в логах терминала.
#
# Флаг и переменная окружения называются bundle: это имена на проводе, их менять
# нельзя — сломались бы уже установленные ноды. Оператору же везде говорим
# «ключ подключения», ровно так это называется в панели.
if [[ -z "$BUNDLE" ]]; then
  printf '\n%sВставьте ключ подключения из панели%s (Ноды → Добавить ноду → Копировать ключ), затем Enter:\n' "$(tput bold 2>/dev/null)" "$(tput sgr0 2>/dev/null)"
  while [[ -z "$BUNDLE" ]]; do
    read -rp "Ключ подключения: " BUNDLE </dev/tty || die "ввод прерван"
    BUNDLE="${BUNDLE//[[:space:]]/}"   # убираем переносы/пробелы, если вставка их добавила
  done
fi
# Лёгкая проверка: ключ должен быть корректным base64 (сам агент проверит глубже).
printf '%s' "$BUNDLE" | base64 -d >/dev/null 2>&1 || die "ключ не похож на base64 — скопируйте его из панели целиком"
command -v docker >/dev/null || die "Docker не установлен"
docker compose version >/dev/null 2>&1 || die "нужен Docker Compose v2"

# --- preflight ---------------------------------------------------------------
info "Проверка портов и времени"
need_ports=("$MGMT_PORT")
# 53 в списке нет намеренно: нода его наружу не публикует. Устройства ходят по
# DoH и DoT с токеном, а открытый 53 отвечал бы REFUSED кому угодно.
if [[ "$ROLE" == "ingress" ]]; then need_ports+=(80 443 853 "$DOH_PORT"); else need_ports+=("$RELAY_PORT"); fi
for p in "${need_ports[@]}"; do
  if command -v ss >/dev/null && ss -lnt "sport = :$p" 2>/dev/null | grep -q LISTEN; then
    die "порт $p занят"
  fi
done
if command -v timedatectl >/dev/null; then
  timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes \
    || info "предупреждение: время не синхронизировано по NTP; расхождение часов ломает mTLS"
fi
ok "предварительные проверки пройдены"

cat <<PLAN

План установки ноды
  роль             $ROLE_LABEL ($ROLE)
  каталог          $DIR
  порт управления  $MGMT_PORT (сюда подключается панель)
  открыть порты    ${need_ports[*]}
  панель ходит с   ${PANEL_IP:-<любого адреса — задайте --panel-ip, чтобы ограничить>}

PLAN
if [[ $ASSUME_YES -eq 0 ]]; then
  read -rp "Продолжить? [y/N] " a
  [[ "$a" == "y" || "$a" == "Y" ]] || { echo "Отменено."; exit 0; }
fi

# Контейнеры работают в сети хоста и не от рута, а входной ноде нужны 53, 80,
# 443 и 853 — порты ниже 1024. В отдельной сети Docker разрешал такие привязки
# сам, в сети хоста этой поблажки нет, и процессы просто не стартуют.
if [[ "$ROLE" == "ingress" ]]; then
  printf '# Ноде нужны 53, 80, 443, 853, а её процессы работают не от рута.\nnet.ipv4.ip_unprivileged_port_start=0\n' \
    > /etc/sysctl.d/99-smartdns.conf
  sysctl -q -p /etc/sysctl.d/99-smartdns.conf 2>/dev/null || true
  ok "низкие порты разрешены непривилегированным процессам (99-smartdns.conf)"
fi

mkdir -p "$DIR"
# Compose — генерируемый файл, а не настройка оператора: обновляем всегда.
# Раньше он ставился только при отсутствии, и повторная установка молча
# оставляла старый — с версией образа, которой в реестре уже нет.
[[ -f "$SRC/node/deploy/$ROLE/docker-compose.yml" ]] || die "не найден compose роли $ROLE в $SRC/node/deploy/$ROLE"
if [[ -f "$DIR/docker-compose.yml" ]] \
   && ! cmp -s "$SRC/node/deploy/$ROLE/docker-compose.yml" "$DIR/docker-compose.yml"; then
  cp "$DIR/docker-compose.yml" "$DIR/docker-compose.yml.bak-$(date +%Y%m%d-%H%M%S)"
  info "прежний compose сохранён рядом как .bak-*"
fi
cp "$SRC/node/deploy/$ROLE/docker-compose.yml" "$DIR/docker-compose.yml"
ok "compose роли $ROLE установлен в $DIR (образы тянутся из реестра — сборка на сервере не нужна)"

# Ingress хранит здесь сертификаты, выпущенные из панели. Контейнер работает
# под uid 10001, поэтому смонтированный каталог должен быть ему доступен на
# запись (dns-frontend делит тот же uid и читает cert оттуда же).
if [[ "$ROLE" == "ingress" ]]; then
  install -d -o 10001 -g 10001 -m 0755 "$DIR/tls"
fi

umask 077
cat > "$DIR/.env" <<ENV
NODE_BUNDLE=$BUNDLE
MGMT_BIND=$MGMT_PORT
RELAY_PORT=$RELAY_PORT
DOH_PORT=$DOH_PORT
SMARTDNS_VERSION=${SMARTDNS_VERSION}
LOG_LEVEL=
LOG_MAX_SIZE=10m
LOG_MAX_FILE=3
ALLOW_SELF_SIGNED_TLS=0
ENV
chmod 600 "$DIR/.env"

info "Загрузка образов из реестра и запуск"
cd "$DIR"
# Проверяем тег до запуска: без этого compose видит «образа нет», решает, что
# его надо собрать, и падает уже на попытке сборки — из сообщения непонятно,
# что виноват отсутствующий тег.
IMAGE="ghcr.io/${GHCR_OWNER:-vitalyor}/smartdns-node:${SMARTDNS_VERSION}"
if ! docker manifest inspect "$IMAGE" >/dev/null 2>&1; then
  die "образа $IMAGE нет в реестре.
  Это значит, что установщик взят из ветки, где версия уже другая.
  Посмотрите доступные теги: https://github.com/${GITHUB_REPO:-vitalyor/smart-dns-proxy}/pkgs/container/smartdns-node
  и запустите с нужной веткой: --ref <ветка>, либо задайте SMARTDNS_VERSION=<тег>."
fi
docker compose --env-file .env up -d --pull always

# Ждём без пайпа намеренно. При `set -o pipefail` grep -q закрывает поток на
# первом совпадении, docker compose получает SIGPIPE, и пайплайн возвращает 141
# — то есть «не нашли» ровно тогда, когда нашли. Установка из-за этого ругалась
# «агент не поднялся» на живом, работающем агенте.
info "Ожидание, пока агент начнёт слушать порт управления"
up=0
for _ in $(seq 1 40); do
  logs=$(docker compose logs node-agent 2>/dev/null || true)
  case "$logs" in *"agent listening"*) up=1; break;; esac
  sleep 3
done
[[ $up -eq 1 ]] \
  || die "агент не поднялся. Смотрите: docker compose -f $DIR/docker-compose.yml logs node-agent"
ok "агент слушает порт $MGMT_PORT, ждёт подключения панели"

# --- firewall ----------------------------------------------------------------
# Контейнеры ноды работают в сети хоста и портов не публикуют, поэтому ufw —
# единственная точка управления ими, как и ожидает оператор.
#
# Раньше здесь стоял совет закрыть порт через ufw, и он был вредным: при
# публикации портов Docker заворачивает их своими правилами раньше цепочек ufw,
# команда выполнялась, рапортовала об успехе и не делала ничего. Оператор
# оставался с портом управления, открытым всему интернету, будучи уверенным в
# обратном. В сети хоста этой ловушки нет.
if command -v ufw >/dev/null && ufw status 2>/dev/null | grep -q "Status: active"; then
  allow() { ufw allow "$@" >/dev/null 2>&1 || true; }
  if [[ -n "$PANEL_IP" ]]; then
    allow from "$PANEL_IP" proto tcp to any port "$MGMT_PORT" comment 'SmartDNS: панель управляет нодой'
    ok "порт $MGMT_PORT открыт только для панели $PANEL_IP"
  else
    allow proto tcp to any port "$MGMT_PORT" comment 'SmartDNS: панель управляет нодой'
    info "порт $MGMT_PORT открыт всем: задайте --panel-ip, чтобы сузить до адреса панели"
  fi
  if [[ "$ROLE" == "ingress" ]]; then
    allow proto tcp to any port 443 comment 'SmartDNS: SNI-прокси и DoH'
    allow proto tcp to any port 853 comment 'SmartDNS: DoT'
    allow proto tcp to any port "$DOH_PORT" comment 'SmartDNS: DoH'
    ok "открыты 443, 853 и $DOH_PORT — устройствам"
  else
    if [[ -n "$INGRESS_IP" ]]; then
      allow from "$INGRESS_IP" proto tcp to any port "$RELAY_PORT" comment 'SmartDNS: туннель от входной ноды'
      ok "порт $RELAY_PORT открыт только для входной ноды $INGRESS_IP"
    else
      allow proto tcp to any port "$RELAY_PORT" comment 'SmartDNS: туннель от входной ноды'
      info "порт $RELAY_PORT открыт всем: задайте --ingress-ip, чтобы сузить до входной ноды"
    fi
  fi
  info "порт 53 наружу не открываем: устройства ходят по DoH и DoT с токеном"
else
  info "ufw не активен — откройте порты сами: $MGMT_PORT для панели и ${need_ports[*]} для работы"
fi

cat <<DONE

Нода поднята и ждёт панель. Дальше — в панели:
  1. Убедитесь, что адрес управления ноды указан верно (этот сервер:$MGMT_PORT).
  2. Добавьте ноду в «$ROLE_LABEL» и соберите конфигурацию — панель протолкнёт её на ноду.
DONE
if [[ "$ROLE" == "ingress" ]]; then
cat <<CHECK
  Устройствам нужны с этой ноды: 443 (SNI-прокси и DoH), 853 (DoT, Android
  Private DNS) и $DOH_PORT, если DoH вынесен на отдельный порт. Обычный DNS на
  53 наружу не публикуется: устройства ходят по DoH и DoT с токеном.
CHECK
else
cat <<CHECK
  Открыть на этой ноде ТОЛЬКО для входных нод:
       ufw allow from <IP-входной-ноды> to any port $RELAY_PORT proto tcp
CHECK
fi
