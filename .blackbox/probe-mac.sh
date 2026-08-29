#!/bin/bash
# Пробник доступности SmartDNS с ТЕКУЩЕЙ сети мака (голый curl, без докера).
#
#   ./probe-mac.sh clean     # запусти на ЧИСТОЙ сети, дай поработать 2-3 минуты, потом Ctrl+C
#   ./probe-mac.sh proxy     # переключись на подкоп и запусти снова
#
# Логи лягут рядом: probe-<метка>-<дата>.log — скинь их мне (оба).
# Три проверки каждые 2с (код 000 = провал, после кода — секунды):
#   net    — обычный интернет (TLS к 1.1.1.1), жив ли канал вообще
#   gemini — путь разблокировки (TLS к ingress:443, SNI gemini → egress → сайт)
#   doh    — путь DNS (реальный DoH-запрос example.org к ноде)
set -u
LABEL="${1:-run}"
ING="212.67.10.15"
DOH="AAABAAABAAAAAAAAB2V4YW1wbGUDb3JnAAABAAE"
DIR="$(cd "$(dirname "$0")" && pwd)"
LOG="$DIR/probe-$LABEL-$(date +%Y%m%d-%H%M%S).log"

EXIT_IP=$(curl -s -m5 https://api.ipify.org 2>/dev/null || echo "?")
{ echo "# старт $(date -u +%FT%TZ)  метка=$LABEL  внешний_IP=$EXIT_IP"
  echo "# столбцы: время метка net=код:сек gemini=код:сек doh=код:сек  (000 = провал)"; } | tee "$LOG"

total=0; fails=0
trap 'echo; echo "# стоп. тиков: $total, с провалом: $fails"; echo "# лог: $LOG"; exit 0' INT

while true; do
  ts=$(date -u +%FT%TZ)
  net=$(curl -s -o /dev/null -w "%{http_code}:%{time_total}" -m5 https://1.1.1.1/ 2>/dev/null);                                              [ -z "$net" ] && net="000:0"
  gem=$(curl -s -o /dev/null -w "%{http_code}:%{time_total}" -m5 --resolve gemini.google.com:443:$ING https://gemini.google.com/ 2>/dev/null); [ -z "$gem" ] && gem="000:0"
  doh=$(curl -s -o /dev/null -w "%{http_code}:%{time_total}" -m5 --resolve dns.nolim.cloud:443:$ING "https://dns.nolim.cloud/dns-query?dns=$DOH" 2>/dev/null); [ -z "$doh" ] && doh="000:0"
  echo "$ts $LABEL net=$net gemini=$gem doh=$doh" | tee -a "$LOG"
  total=$((total+1))
  case "$net$gem$doh" in *000:*) fails=$((fails+1));; esac
  sleep 2
done
