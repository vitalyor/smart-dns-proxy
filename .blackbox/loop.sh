#!/bin/sh
: "${TARGET:=212.67.10.15}"; : "${VANTAGE:=mac}"
DOH="AAABAAABAAAAAAAAB2V4YW1wbGUDb3JnAAABAAE"
while true; do
  ts=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  g=$(curl -s -o /dev/null -w "%{http_code}:%{time_total}" --resolve gemini.google.com:443:$TARGET https://gemini.google.com/ -m5 2>/dev/null); [ -z "$g" ] && g="000:0"
  d=$(curl -s -o /dev/null -w "%{http_code}:%{time_total}" --resolve dns.nolim.cloud:443:$TARGET "https://dns.nolim.cloud/dns-query?dns=$DOH" -m5 2>/dev/null); [ -z "$d" ] && d="000:0"
  echo "$ts $VANTAGE gemini $g"; echo "$ts $VANTAGE doh $d"
  sleep 2
done
