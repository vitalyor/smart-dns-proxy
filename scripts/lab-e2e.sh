#!/usr/bin/env bash
# Brings up the container lab, configures it through the public API exactly as
# an operator would, and asserts the acceptance behaviour end to end.
#
# Usage: scripts/lab-e2e.sh [--keep]
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
LAB="$ROOT/deploy/examples/lab"
COMPOSE=(docker compose -f "$LAB/docker-compose.yml")
PANEL="http://localhost:8080/api/v1"
JAR="$(mktemp -t sdnscookie.XXXXXX)"
KEEP=0
[[ "${1:-}" == "--keep" ]] && KEEP=1

pass=0; fail=0
ok()   { printf '  \033[32m✓\033[0m %s\n' "$1"; pass=$((pass+1)); }
bad()  { printf '  \033[31m✗\033[0m %s\n' "$1"; fail=$((fail+1)); }
step() { printf '\n\033[1m%s\033[0m\n' "$1"; }

cleanup() {
  rm -f "$JAR"
  if [[ $KEEP -eq 0 ]]; then
    step "Останавливаю стенд"
    "${COMPOSE[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  else
    printf '\nСтенд оставлен работать. Панель: http://localhost:8080 (admin@lab.local / labadminpassword)\n'
    printf 'Остановить: docker compose -f %s down -v\n' "$LAB/docker-compose.yml"
  fi
}
trap cleanup EXIT

jsonq() { python3 -c "import sys,json;d=json.load(sys.stdin);print($1)"; }

api() { # api METHOD PATH [BODY]
  local method="$1" path="$2" body="${3:-}"
  local args=(-sS -X "$method" -b "$JAR" -c "$JAR" -H "Content-Type: application/json"
              -H "X-CSRF-Token: ${CSRF:-}" -H "Idempotency-Key: $(uuidgen 2>/dev/null || date +%s%N)")
  [[ -n "$body" ]] && args+=(-d "$body")
  curl "${args[@]}" "$PANEL$path"
}

# cx_sh runs a shell command inside the client container, which shares the
# /shared volume with the agents.
cx_sh() { "${COMPOSE[@]}" exec -T client sh -c "$1"; }

wait_for() { # wait_for SECONDS DESCRIPTION COMMAND...
  local deadline=$(( $(date +%s) + $1 )); local desc="$2"; shift 2
  while (( $(date +%s) < deadline )); do
    if "$@" >/dev/null 2>&1; then return 0; fi
    sleep 2
  done
  bad "таймаут ожидания: $desc"
  return 1
}

step "1/8 Сборка образов"
"${COMPOSE[@]}" build --quiet
ok "образы собраны"

step "2/8 Запуск control plane и тестовых заглушек"
"${COMPOSE[@]}" up -d postgres panel mock-auth mock-origin unbound client
wait_for 180 "панель отвечает на /healthz" curl -fsS http://localhost:8080/healthz
ok "панель поднялась"

step "3/8 Вход и чеканка нод (push-модель)"
LOGIN=$(curl -sS -c "$JAR" -H "Content-Type: application/json" \
  -d '{"email":"admin@lab.local","password":"labadminpassword"}' "$PANEL/auth/login")
CSRF=$(echo "$LOGIN" | jsonq "d['csrf_token']")
[[ -n "$CSRF" ]] && ok "вход выполнен" || { bad "вход не выполнен: $LOGIN"; exit 1; }

# The panel connects out to each node, so we register the node with its
# management address and get back a bundle (like remnanode's SECRET_KEY).
# The data-plane address (public_ipv4) is what devices/ingress reach; the
# management address is a separate port the panel dials. They differ in the lab
# so the two roles never share a container IP.
mk_node() { # mk_node ROLE NAME PUBLIC_IPV4 MGMT [RELAY_PORT]
  local extra=""; [[ -n "${5:-}" ]] && extra=",\"relay_port\":$5"
  api POST /nodes "{\"role\":\"$1\",\"name\":\"$2\",\"public_ipv4\":\"$3\",\"mgmt_address\":\"$4\"$extra}"
}
ING1=$(mk_node ingress ingress-lab-1 172.28.0.20 172.28.0.21:3333)
ING2=$(mk_node ingress ingress-lab-2 172.28.0.25 172.28.0.26:3333)
EGR=$(mk_node  egress  egress-lab    172.28.0.30 172.28.0.31:3333 8443)
ING1_ID=$(echo "$ING1" | jsonq "d['node_id']"); ING1_B=$(echo "$ING1" | jsonq "d['bundle']")
ING2_ID=$(echo "$ING2" | jsonq "d['node_id']"); ING2_B=$(echo "$ING2" | jsonq "d['bundle']")
EGR_ID=$(echo "$EGR"  | jsonq "d['node_id']");  EGR_B=$(echo "$EGR"  | jsonq "d['bundle']")
[[ -n "$ING1_B" && -n "$ING2_B" && -n "$EGR_B" ]] \
  && ok "три ноды созданы, бандлы выданы" || { bad "бандлы не выданы"; exit 1; }

step "4/8 Установка бандлов и запуск агентов"
# Drop each bundle where its agent waits for it, then start the agents. The
# panel dials them; nodes never dial out.
printf '%s' "$ING1_B" | cx_sh "cat > /shared/ingress1.bundle"
printf '%s' "$ING2_B" | cx_sh "cat > /shared/ingress2.bundle"
printf '%s' "$EGR_B"  | cx_sh "cat > /shared/egress.bundle"
"${COMPOSE[@]}" up -d agent-ingress agent-ingress-2 agent-egress
wait_for 150 "панель дозвонилась до всех трёх нод" bash -c \
  "curl -sS -b '$JAR' '$PANEL/nodes' | python3 -c \"import sys,json;d=json.load(sys.stdin);ns=d['items'];sys.exit(0 if len(ns)==3 and all(n['status'] in ('healthy','degraded') for n in ns) else 1)\""
ok "панель подключилась к трём нодам по mTLS"

ING_ID=$ING1_ID

step "5/8 Настройка доступа, групп, набора правил и сервиса"
api PUT /settings '{"dns_allowed_cidrs":["172.28.0.0/24"],"dns_access_mode":"allowlist","egress_resolver":"172.28.0.40:53","doh_hostname":"dns.lab.test","dot_hostname":"dns.lab.test","dns_rate_limit_qps":500,"dns_rate_limit_burst":2000}' >/dev/null
ok "заданы список доступа к DNS и резолвер egress"

# A typo in the log level must be refused, not silently applied: a level the
# panel does not understand would otherwise turn logging off.
CODE=$(curl -sS -o /dev/null -w '%{http_code}' -X PUT -b "$JAR" -c "$JAR" \
  -H "Content-Type: application/json" -H "X-CSRF-Token: ${CSRF:-}" \
  -d '{"log_level":"verbose"}' "$PANEL/settings")
[[ "$CODE" == "400" ]] && ok "недопустимый уровень логирования отклонён (400)" \
  || bad "недопустимый уровень логирования принят: код $CODE"
api PUT /settings '{"log_level":"info","node_log_level":"warn"}' >/dev/null
ok "уровни логирования панели и нод заданы"

IG=$(api POST /ingress-groups '{"name":"Вход РФ","mode":"active_active","settings":{}}' | jsonq "d['id']")
EG=$(api POST /egress-groups  '{"name":"Выход ЕС","mode":"primary_fallback","settings":{}}' | jsonq "d['id']")
api POST "/ingress-groups/$IG/members" "{\"node_id\":\"$ING1_ID\",\"priority\":1,\"weight\":1}" >/dev/null
api POST "/ingress-groups/$IG/members" "{\"node_id\":\"$ING2_ID\",\"priority\":2,\"weight\":1}" >/dev/null
api POST "/egress-groups/$EG/members"  "{\"node_id\":\"$EGR_ID\",\"priority\":1,\"weight\":1}" >/dev/null
ok "группы созданы, обе ingress и egress добавлены"

RS=$(api POST /rule-sets '{"name":"Тестовый список","update_mode":"manual_only","interval_sec":21600,"manual_include":["origin.test"],"manual_exclude":[]}' | jsonq "d['id']")
FETCH=$(api POST "/rule-sets/$RS/fetch" '{}')
echo "$FETCH" | grep -q '"content_hash"' && ok "набор правил нормализован" || bad "набор правил не собрался: $FETCH"

api POST /services "{\"name\":\"Тестовый сервис\",\"slug\":\"testsvc\",\"rule_set_id\":\"$RS\",\"ingress_group_id\":\"$IG\",\"egress_group_id\":\"$EG\",\"allowed_ports\":[443],\"dns_ttl\":60,\"priority\":100,\"udp_mode\":\"disabled_fallback\",\"probe\":{\"hostname\":\"origin.test\",\"port\":443}}" >/dev/null
ok "сервис создан"

step "6/8 Сборка и выкат ревизии"
REV=$(api POST /revisions/compile '{"deploy":true}')
echo "$REV" | grep -q '"revision_id"' && ok "ревизия собрана и подписана" || { bad "сборка не удалась: $REV"; exit 1; }

"${COMPOSE[@]}" up -d dns-frontend sni-proxy dns-frontend-2 sni-proxy-2 egress-relay
wait_for 180 "панель протолкнула ревизию на все ноды" bash -c \
  "curl -sS -b '$JAR' '$PANEL/nodes' | python3 -c \"import sys,json;d=json.load(sys.stdin);ns=d['items'];sys.exit(0 if len(ns)==3 and all(n['applied_sequence'] and n['applied_sequence']==n['desired_sequence'] for n in ns) else 1)\""
ok "все три ноды применили ревизию (push)"

# Two ingress nodes in one group must run the SAME services and rules — the
# panel compiles once and pushes an identical service set to both.
svc_of() { "${COMPOSE[@]}" exec -T "$1" cat /var/lib/smartdns-agent/active/config.json 2>/dev/null \
  | python3 -c "import json,sys;d=json.load(sys.stdin);print(sorted(s['slug'] for s in d.get('services',[])))" 2>/dev/null || true; }
S1=$(svc_of sni-proxy); S2=$(svc_of sni-proxy-2)
[[ -n "$S1" && "$S1" == "$S2" ]] \
  && ok "обе ingress-ноды получили идентичный набор сервисов: $S1" \
  || bad "конфиг ingress-нод разошёлся: '$S1' vs '$S2'"

# The level chosen in the panel must reach the node artifact, and the node must
# act on it: LOG_LEVEL is empty in the lab, so nothing overrides the push.
LVL=$("${COMPOSE[@]}" exec -T sni-proxy cat /var/lib/smartdns-agent/active/config.json \
  | python3 -c "import sys,json;print(json.load(sys.stdin).get('log_level',''))" 2>/dev/null || true)
[[ "$LVL" == "warn" ]] && ok "уровень логирования доехал до артефакта ноды (warn)" \
  || bad "в артефакте ноды log_level=$LVL, ожидалось warn"
"${COMPOSE[@]}" logs sni-proxy 2>/dev/null | grep -q '"msg":"log level changed"' \
  && ok "нода переключила уровень без перезапуска" \
  || bad "нода не сообщила о смене уровня логирования"

step "7/8 Проверка поведения data plane"
cx() { "${COMPOSE[@]}" exec -T client "$@"; }

# active_active: each ingress publishes the whole group's A-set, so one query
# already gives the device multi-A failover.
MANAGED=$(cx dig +short +timeout=3 origin.test @172.28.0.20 2>/dev/null | grep -E '^172\.28\.0\.(20|25)$' | sort | tr '\n' ' ')
[[ "$MANAGED" == *172.28.0.20* && "$MANAGED" == *172.28.0.25* ]] \
  && ok "управляемый домен возвращает multi-A обеих ingress ($MANAGED)" \
  || bad "ожидались оба ingress-адреса, получено: '$MANAGED'"

ORDINARY=$(cx dig +short +timeout=5 ordinary.test @172.28.0.20 | head -1 || true)
[[ "$ORDINARY" == "203.0.113.10" ]] \
  && ok "обычный домен получает настоящий адрес через Unbound ($ORDINARY)" \
  || bad "обычный домен вернул '$ORDINARY', ожидался 203.0.113.10"

AAAA=$(cx dig +noall +comments +timeout=3 origin.test AAAA @172.28.0.20 | grep -c 'status: NOERROR' || true)
AAAA_ANS=$(cx dig +short +timeout=3 origin.test AAAA @172.28.0.20 | wc -l | tr -d ' ')
[[ "$AAAA" == "1" && "$AAAA_ANS" == "0" ]] \
  && ok "AAAA возвращает NODATA, пока IPv6-путь не проверен" \
  || bad "AAAA вернул неожиданный ответ (status hits=$AAAA, answers=$AAAA_ANS)"

HTTPS=$(cx curl -sS --max-time 15 --cacert /shared/origin-ca.pem \
  --resolve origin.test:443:172.28.0.20 https://origin.test/ 2>&1 || true)
echo "$HTTPS" | grep -q 'origin-ok' \
  && ok "TLS проходит насквозь: клиент проверил сертификат настоящего origin" \
  || bad "сквозной запрос не удался: $HTTPS"

echo "$HTTPS" | grep -q 'source=172.28.0.30' \
  && ok "origin видит IP egress-ноды, а не клиента и не ingress" \
  || bad "origin увидел не тот источник: $HTTPS"

DIRECT=$(cx curl -sS --max-time 10 --cacert /shared/origin-ca.pem \
  --resolve origin.test:443:172.28.0.50 https://origin.test/ 2>&1 || true)
echo "$DIRECT" | grep -q 'source=172.28.0.60' \
  && ok "непроксируемое соединение сохраняет исходный IP клиента" \
  || bad "прямая проверка дала неожиданный результат: $DIRECT"

REJECT=$(cx curl -sS --max-time 10 -k --resolve other.test:443:172.28.0.20 https://other.test/ 2>&1 || true)
echo "$REJECT" | grep -q 'origin-ok' \
  && bad "SNI вне набора правил был проксирован — это открытый прокси" \
  || ok "SNI вне набора правил отклонён на ingress"

# Asserted on the relay, not on the openssl client: in TLS 1.3 the alert for a
# missing client certificate arrives after the client has already hung up, so
# the client side sees a "successful" connection either way. The relay's own
# counter is the authoritative answer.
relay_rejects() {
  cx curl -sS --max-time 5 http://172.28.0.30:9103/metrics 2>/dev/null \
    | awk -F' ' '/^smartdns_egress_requests_total\{result="(handshake_failed|no_client_cert)"\}/ {n+=$2} END {print n+0}'
}
BEFORE=$(relay_rejects)
cx timeout 10 openssl s_client -connect 172.28.0.30:8443 -servername egress-lab </dev/null >/dev/null 2>&1 || true
AFTER=$(relay_rejects)
[[ "${AFTER:-0}" -gt "${BEFORE:-0}" ]] \
  && ok "egress-relay отклоняет подключение без сертификата ноды" \
  || bad "egress-relay принял неаутентифицированное подключение (отказов было $BEFORE, стало $AFTER)"

step "7b/8 Несколько ingress и failover устройства"
# The second ingress must resolve and proxy independently: a device that lists
# both gets a working answer from whichever is up.
MANAGED2=$(cx dig +short +timeout=3 origin.test @172.28.0.25 2>/dev/null | grep -E '^172\.28\.0\.(20|25)$' | tr '\n' ' ')
[[ "$MANAGED2" == *172.28.0.25* ]] \
  && ok "вторая ingress-нода отвечает и публикует свой адрес ($MANAGED2)" \
  || bad "вторая ingress не вернула свой адрес: '$MANAGED2'"

HTTPS2=$(cx curl -sS --max-time 15 --cacert /shared/origin-ca.pem \
  --resolve origin.test:443:172.28.0.25 https://origin.test/ 2>&1 || true)
echo "$HTTPS2" | grep -q 'origin-ok' \
  && ok "вторая ingress проксирует насквозь через egress" \
  || bad "вторая ingress не проксирует: $HTTPS2"

# Kill the first ingress data plane; a client listing both IPs fails over to the
# second, which keeps resolving and proxying.
"${COMPOSE[@]}" stop dns-frontend sni-proxy >/dev/null
sleep 3
DOWN=$(cx dig +short +timeout=3 origin.test @172.28.0.20 2>/dev/null | grep -E '^172\.28\.0\.[0-9]+$' | head -1 || true)
[[ -z "$DOWN" ]] \
  && ok "первая ingress-нода погашена (не отвечает)" \
  || bad "первая ingress всё ещё отвечает после остановки: '$DOWN'"
FAILOVER=$(cx dig +short +timeout=3 origin.test @172.28.0.25 2>/dev/null | grep -E '^172\.28\.0\.(20|25)$' | head -1 || true)
[[ -n "$FAILOVER" ]] \
  && ok "failover: клиент обслужен второй ingress-нодой, пока первая лежит" \
  || bad "failover не сработал: вторая ingress не ответила"
FAILOVER_HTTP=$(cx curl -sS --max-time 15 --cacert /shared/origin-ca.pem \
  --resolve origin.test:443:172.28.0.25 https://origin.test/ 2>&1 || true)
echo "$FAILOVER_HTTP" | grep -q 'origin-ok' \
  && ok "failover: трафик разблокирован через вторую ingress при упавшей первой" \
  || bad "failover HTTPS не удался: $FAILOVER_HTTP"
"${COMPOSE[@]}" start dns-frontend sni-proxy >/dev/null

step "8/8 Устойчивость к отказу панели"
"${COMPOSE[@]}" stop panel postgres >/dev/null
sleep 5
OFFLINE=$(cx curl -sS --max-time 15 --cacert /shared/origin-ca.pem \
  --resolve origin.test:443:172.28.0.20 https://origin.test/ 2>&1 || true)
echo "$OFFLINE" | grep -q 'origin-ok' \
  && ok "трафик продолжает идти при выключенной панели (last-known-good)" \
  || bad "data plane сломался без панели: $OFFLINE"
OFFLINE_DNS=$(cx dig +short +timeout=3 origin.test @172.28.0.20 2>/dev/null | grep -E '^172\.28\.0\.(20|25)$' | head -1 || true)
[[ -n "$OFFLINE_DNS" ]] \
  && ok "DNS продолжает отвечать при выключенной панели ($OFFLINE_DNS)" \
  || bad "DNS перестал отвечать без панели: '$OFFLINE_DNS'"
"${COMPOSE[@]}" start postgres panel >/dev/null
wait_for 60 "панель снова онлайн" curl -fsS http://localhost:8080/healthz

# Panel move / reconnect: the panel keeps the same client cert (state volume),
# so after coming back it re-attaches to every node by fingerprint, regardless
# of address. A fresh compile+deploy must reach all three.
RELOGIN=$(curl -sS -c "$JAR" -H "Content-Type: application/json" \
  -d '{"email":"admin@lab.local","password":"labadminpassword"}' "$PANEL/auth/login")
CSRF=$(echo "$RELOGIN" | jsonq "d['csrf_token']")
REV2=$(api POST /revisions/compile '{"deploy":true}')
echo "$REV2" | grep -q '"revision_id"' \
  && ok "панель после перезапуска собрала новую ревизию" \
  || bad "панель не собрала ревизию после перезапуска: $REV2"
wait_for 120 "панель повторно протолкнула ревизию всем нодам" bash -c \
  "curl -sS -b '$JAR' '$PANEL/nodes' | python3 -c \"import sys,json;d=json.load(sys.stdin);ns=d['items'];sys.exit(0 if all(n['applied_sequence']==n['desired_sequence'] and n['desired_sequence'] for n in ns) else 1)\""
ok "панель переподключилась к нодам по сертификату и протолкнула ревизию"

printf '\n\033[1mИтог:\033[0m %d пройдено, %d провалено\n' "$pass" "$fail"
[[ $fail -eq 0 ]] || exit 1
