#!/usr/bin/env bash
# SmartDNS — установка панели или ноды одной командой.
#
#   bash <(curl -fsSL https://<ваш-хост>/install.sh)                 # спросит роль
#   bash <(curl -fsSL https://<ваш-хост>/install.sh) --role panel \
#        --public-url https://panel.example.net --admin-email you@example.net
#   bash <(curl -fsSL https://<ваш-хост>/install.sh) --role ingress \
#        --bundle <BASE64> --panel-ip 203.0.113.9
#   bash <(curl -fsSL https://<ваш-хост>/install.sh) --role egress \
#        --bundle <BASE64> --panel-ip 203.0.113.9
#
# Скрипт НИЧЕГО не ломает: если нужный порт уже занят, он останавливается и
# показывает, кто его держит, — а не отбирает. Все проверки идут ДО первого
# изменения на сервере.
#
# Переменные окружения для приватного репозитория:
#   SMARTDNS_REPO   git-адрес (по умолчанию из --repo)
#   SMARTDNS_REF    ветка/тег (по умолчанию main)
#   GITHUB_TOKEN    токен для https-клона приватного репозитория
set -euo pipefail

ROLE=""
REPO="${SMARTDNS_REPO:-}"
REF="${SMARTDNS_REF:-main}"
SRC_DIR=/opt/smartdns-src
ASSUME_YES=0
FREE_DNS=0            # --free-dns-port: освободить 53 у systemd-resolved без вопросов
RESTORE=""            # файл резервной копии для переезда панели (--restore)
PASS=()               # аргументы, которые уйдут в под-скрипт как есть

C_RED=$'\033[31m'; C_GRN=$'\033[32m'; C_CYN=$'\033[36m'; C_YEL=$'\033[33m'; C_0=$'\033[0m'
die()  { printf '%sОшибка:%s %b\n' "$C_RED" "$C_0" "$*" >&2; exit 1; }
info() { printf '%s•%s %s\n' "$C_CYN" "$C_0" "$*"; }
ok()   { printf '%s✓%s %s\n' "$C_GRN" "$C_0" "$*"; }
warn() { printf '%s⚠%s  %s\n' "$C_YEL" "$C_0" "$*"; }
ask()  { local a; read -rp "$1" a </dev/tty; printf '%s' "$a"; }

while [[ $# -gt 0 ]]; do
  case "$1" in
    --role)      ROLE="$2"; shift 2;;
    --repo)      REPO="$2"; shift 2;;
    --ref)       REF="$2"; shift 2;;
    --src-dir)   SRC_DIR="$2"; shift 2;;
    --restore)   RESTORE="$2"; shift 2;;
    --free-dns-port) FREE_DNS=1; shift;;
    --yes|-y)    ASSUME_YES=1; PASS+=("--yes"); shift;;
    -h|--help)   sed -n '2,22p' "$0"; exit 0;;
    *)           PASS+=("$1"); shift;;   # всё прочее — под-скрипту (--bundle, --panel-ip, --public-url, …)
  esac
done

[[ $EUID -eq 0 ]] || die "запустите через sudo (нужен для проверки портов и ufw)"

# --- роль -------------------------------------------------------------------
if [[ -z "$ROLE" ]]; then
  echo "Что ставим на этот сервер?"
  echo "  1) panel    — панель управления (control plane)"
  echo "  2) ingress  — нода приёма DNS от устройств"
  echo "  3) egress   — нода выхода в интернет"
  case "$(ask 'Выбор [1/2/3]: ')" in
    1|panel)   ROLE=panel;;
    2|ingress) ROLE=ingress;;
    3|egress)  ROLE=egress;;
    *) die "не понял выбор";;
  esac
fi
[[ "$ROLE" =~ ^(panel|ingress|egress)$ ]] || die "--role должен быть panel, ingress или egress"
info "Роль: $ROLE"

# --- какие порты роль займёт ------------------------------------------------
# Держим и порт, и человекочитаемое назначение — для внятного сообщения о конфликте.
declare -a PORTS PORTWHY
add_port() { PORTS+=("$1"); PORTWHY+=("$2"); }
case "$ROLE" in
  panel)   add_port 8080 "UI панели";;
  ingress) add_port 3333 "управление (панель→агент)"
           add_port 53   "DNS"; add_port 443 "SNI-прокси"
           add_port 853  "DoT"; add_port 8443 "DoH";;
  egress)  add_port 3333 "управление (панель→агент)"
           add_port 8443 "relay (ingress→egress)";;
esac

# --- предполёт: всё проверяем ДО любого изменения ---------------------------
info "Предполётные проверки (сервер пока не трогаем)"

case "$(uname -m)" in x86_64|aarch64|arm64) ;; *) die "архитектура $(uname -m) не поддерживается";; esac
[[ -f /etc/os-release ]] && . /etc/os-release || true

# Docker: если его нет вовсе — предложим поставить (ломать нечего, контейнеров ещё нет).
if ! command -v docker >/dev/null; then
  warn "Docker не установлен."
  if [[ $ASSUME_YES -eq 1 || "$(ask 'Поставить Docker официальным скриптом get.docker.com? [y/N] ')" =~ ^[yY]$ ]]; then
    curl -fsSL https://get.docker.com | sh || die "не удалось установить Docker"
    systemctl enable --now docker >/dev/null 2>&1 || true
    ok "Docker установлен"
  else
    die "Docker обязателен: https://docs.docker.com/engine/install/"
  fi
fi
docker compose version >/dev/null 2>&1 || die "нужен Docker Compose v2 (плагин 'docker compose')"
ok "Docker и Compose на месте"

# Время: расхождение часов ломает mTLS между панелью и нодами.
if command -v timedatectl >/dev/null; then
  timedatectl show -p NTPSynchronized --value 2>/dev/null | grep -q yes \
    && ok "часы синхронизированы по NTP" \
    || warn "время НЕ синхронизировано по NTP — при расхождении часов mTLS не поднимется"
fi

# Диск: панель собирает образы и держит Postgres.
need_kb=$([[ "$ROLE" == panel ]] && echo 5000000 || echo 3000000)
avail_kb=$(df -Pk /opt 2>/dev/null | awk 'NR==2{print $4}')
avail_kb=${avail_kb:-$(df -Pk / | awk 'NR==2{print $4}')}
[[ ${avail_kb:-0} -ge $need_kb ]] \
  || die "мало места: свободно $((avail_kb/1024)) МиБ, нужно $((need_kb/1024)) МиБ.\n     На тесной ноде соберите образ на другой машине: docker save … | ssh сюда 'docker load'"
ok "диска достаточно ($((avail_kb/1024)) МиБ свободно)"

# Память: панель собирается на месте, нода лишь тянет готовые образы.
mem_free_mb=$(free -m 2>/dev/null | awk '/^Mem:/{print $7}')
[[ "$ROLE" == panel && -n "$mem_free_mb" && $mem_free_mb -lt 300 ]] \
  && warn "свободно всего ${mem_free_mb} МиБ — сборка панели может упереться в память"

# Порт 53 на ingress почти всегда держит systemd-resolved. Освобождаем его
# БЕЗОПАСНО: отключаем только заглушку и перенаправляем resolv.conf на реальный
# апстрим, чтобы DNS самого сервера продолжил работать.
free_dns_port() {
  info "Освобождаю порт 53 у systemd-resolved (DNS хоста сохранится)"
  mkdir -p /etc/systemd/resolved.conf.d
  printf '[Resolve]\nDNSStubListener=no\n' > /etc/systemd/resolved.conf.d/smartdns.conf
  if [[ -e /run/systemd/resolve/resolv.conf ]]; then
    ln -sf /run/systemd/resolve/resolv.conf /etc/resolv.conf
  else
    warn "нет /run/systemd/resolve/resolv.conf — /etc/resolv.conf не трогаю; проверь DNS хоста после установки"
  fi
  systemctl restart systemd-resolved || die "не удалось перезапустить systemd-resolved"
  ok "порт 53 освобождён"
}
if [[ "$ROLE" == ingress ]] && ss -lntpH "sport = :53" 2>/dev/null | grep -q systemd-resolve; then
  if [[ $FREE_DNS -eq 1 ]] || { [[ $ASSUME_YES -eq 0 ]] && [[ "$(ask 'Порт 53 занят systemd-resolved. Освободить его (DNS хоста сохранится)? [y/N] ')" =~ ^[yY]$ ]]; }; then
    free_dns_port
  fi
fi

# ГЛАВНОЕ: занятые порты. Если порт слушается — СТОП, ничего не отбираем.
conflict=0
for i in "${!PORTS[@]}"; do
  p="${PORTS[$i]}"
  if ss -lntH "sport = :$p" 2>/dev/null | grep -q . || ss -lnuH "sport = :$p" 2>/dev/null | grep -q .; then
    conflict=1
    holder=$(ss -lntp "sport = :$p" 2>/dev/null | awk 'NR>1{print $NF}' | tr '\n' ' ')
    printf '%s✗%s порт %-5s (%s) занят: %s\n' "$C_RED" "$C_0" "$p" "${PORTWHY[$i]}" "${holder:-неизвестно}" >&2
    if [[ "$p" == 53 ]] && ss -lntp "sport = :53" 2>/dev/null | grep -q systemd-resolve; then
      printf '     это systemd-resolved. Освободите 53: в /etc/systemd/resolved.conf задайте\n'  >&2
      printf '     DNSStubListener=no, затем: systemctl restart systemd-resolved\n' >&2
    fi
  fi
done
if [[ $conflict -eq 1 ]]; then
  die "порты заняты — установка остановлена, на сервере ничего не изменено.\n     Освободите порты выше или поставьте роль на отдельный IP/сервер, затем повторите."
fi
ok "все нужные порты свободны: ${PORTS[*]}"

# --- исходники: локальные рядом со скриптом, иначе клонируем -----------------
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd || true)"
if [[ -n "$here" && -f "$here/scripts/install-panel.sh" ]]; then
  SRC="$here"
  ok "исходники найдены рядом со скриптом: $SRC"
else
  command -v git >/dev/null || die "нужен git для загрузки исходников (apt install git)"
  [[ -n "$REPO" ]] || die "укажите репозиторий: --repo git@github.com:<вы>/smart-dns-proxy.git (или переменную SMARTDNS_REPO)"
  url="$REPO"
  if [[ -n "${GITHUB_TOKEN:-}" && "$url" == https://* ]]; then
    url="https://x-access-token:${GITHUB_TOKEN}@${REPO#https://}"
  fi
  if [[ -d "$SRC_DIR/.git" ]]; then
    info "Обновляю исходники в $SRC_DIR"
    git -C "$SRC_DIR" fetch --depth 1 origin "$REF" && git -C "$SRC_DIR" checkout -q FETCH_HEAD
  else
    info "Клонирую $REPO ($REF) в $SRC_DIR"
    git clone --depth 1 --branch "$REF" "$url" "$SRC_DIR" || die "клонирование не удалось (приватный репо? задайте GITHUB_TOKEN или используйте ssh-адрес)"
  fi
  SRC="$SRC_DIR"
fi

# --- передаём управление под-скрипту роли -----------------------------------
info "Запуск установщика роли $ROLE"
if [[ "$ROLE" == panel ]]; then
  if [[ -n "$RESTORE" ]]; then
    [[ -f "$RESTORE" ]] || die "файл копии не найден: $RESTORE"
    if [[ "$RESTORE" == *.enc && -z "${BACKUP_PASSPHRASE:-}" ]]; then
      die "копия зашифрована — задайте пароль из .env исходной панели:\n     BACKUP_PASSPHRASE=<пароль> bash install.sh … --restore $RESTORE"
    fi
    bash "$SRC/scripts/install-panel.sh" "${PASS[@]}"
    info "Восстановление из $RESTORE"
    exec env BACKUP_PASSPHRASE="${BACKUP_PASSPHRASE:-}" bash "$SRC/scripts/restore.sh" "$RESTORE" --yes
  fi
  exec bash "$SRC/scripts/install-panel.sh" "${PASS[@]}"
else
  [[ -z "$RESTORE" ]] || warn "--restore имеет смысл только для роли panel — игнорирую"
  # install-node.sh сам ждёт --role — добавляем его к проброшенным аргументам.
  exec bash "$SRC/scripts/install-node.sh" --role "$ROLE" "${PASS[@]}"
fi
