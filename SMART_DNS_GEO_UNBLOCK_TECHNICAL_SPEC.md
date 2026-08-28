# Техническое задание: Smart DNS / Geo‑Unblock Orchestration Platform

**Статус документа:** готово к реализации  
**Язык интерфейса первой версии:** русский; архитектура должна допускать i18n  
**Назначение:** передача команде разработки или кодовой модели для реализации всего проекта  
**Класс системы:** self-hosted control plane + распределённый data plane  
**Редакция:** 1.0  

---

## 1. Назначение и конечный результат

Необходимо создать self-hosted платформу персонального Smart DNS, позволяющую владельцу на любом устройстве указать собственный DNS и пользоваться выбранными интернет-сервисами, которые ограничивают доступ для российских IP-адресов. Для выбранных доменов клиент должен незаметно направляться на ближайшую ingress-ноду, а затем через защищённый туннель на зарубежную egress-ноду. Конечный сервис должен видеть IP egress-ноды, а не клиента и не российской ingress-ноды. Остальные сайты должны открываться напрямую и не проходить через инфраструктуру проекта.

Система должна включать:

- отдельную web-панель и API — control plane;
- произвольное количество ingress- и egress-нод;
- агент на каждой ноде;
- DNS frontend с обычным DNS, DoH и DoT;
- Unbound как рекурсивный кеширующий DNS resolver;
- sing-box как основной программируемый транспортный data plane;
- импорт, объединение, проверку и версионирование списков доменов из GitHub/HTTP;
- группы нод, health checks, автоматический failover и last-known-good конфигурации;
- установку через Docker Compose и воспроизводимые install-скрипты;
- безопасность, аудит, метрики, логи, резервное копирование и полноценный test plan.

После реализации владелец должен иметь возможность:

1. Установить панель одной командой на homelab или любой VPS.
2. Добавить ingress-ноду в РФ и egress-ноду в нужной стране одной командой с одноразовым enrollment-токеном.
3. Создать сервис, например `Gemini`, подключить GitHub rule-set и выбрать ingress/egress group.
4. Скачать инструкцию или профиль настройки DNS для Android, iOS/macOS, Windows и роутера.
5. При выключенной панели продолжать пользоваться уже применённой конфигурацией.
6. При отказе egress-ноды автоматически переключаться на резервную без ручной правки нод.

## 2. Термины и нормативный язык

Слова **MUST / ДОЛЖЕН**, **SHOULD / СЛЕДУЕТ**, **MAY / МОЖЕТ** имеют нормативный смысл. Требование MUST обязательно для приёмки; SHOULD может быть отложено только с документированным обоснованием.

| Термин | Определение |
|---|---|
| Control plane | Панель, API, БД, компилятор конфигураций и оркестратор. Не находится в пути пользовательского трафика. |
| Data plane | DNS и транспортные компоненты ingress/egress, через которые проходит реальный трафик. |
| Ingress node | Публичная точка входа: принимает DNS и подключения к синтезированному IP, определяет домен и отправляет поток в выбранный egress. |
| Egress node | Зарубежная нода, принимающая туннель от ingress и устанавливающая соединение с настоящим origin. |
| Agent | Локальный привилегированный минимум на ноде: регистрация, получение revisions, atomic apply, health и rollback. |
| Managed domain | Домен, для которого DNS возвращает адрес ingress вместо настоящего адреса. |
| Rule set | Нормализованный версионируемый набор exact/suffix/regex правил с источниками, исключениями и compiled artifact. |
| Service | Логическая услуга (`ChatGPT`, `Gemini`, `Claude`) с rule-set и route policy. |
| Revision | Неизменяемый снимок всей применимой конфигурации с номером, hash и статусом. |
| LKG | Last-known-good — последняя успешно проверенная и применённая конфигурация. |

## 3. Границы проекта

### 3.1. Входит в первую production-версию

- TCP/TLS passthrough для выбранных сервисов, прежде всего HTTPS/TCP 443.
- Обычный DNS UDP/TCP 53, DoH и DoT на ingress.
- Синтез A и, при наличии IPv6, AAAA для managed domains.
- SNI sniffing без TLS termination и без MITM.
- Защищённый ingress→egress транспорт средствами sing-box; обязательный базовый профиль — VLESS поверх TLS/Reality либо иной один явно зафиксированный и протестированный профиль. Дополнительный профиль WireGuard допускается.
- Active-active и primary/fallback группы ingress; primary/fallback, weighted и lowest-latency группы egress.
- GitHub raw, произвольный HTTPS URL, GitHub repository/path, manual list и built-in preset.
- Plain domain list, `domain:`, `domain-suffix:`, wildcard и sing-box source rule-set JSON. Regex разрешать только в расширенном режиме.
- Auto-apply и manual-approve обновлений.
- Локальная автономность нод при падении панели.
- Однопользовательская self-hosted установка; модель данных при этом не должна мешать будущему multi-user.

### 3.2. Не входит в обязательный MVP

- Коммерческий биллинг, тарифы и публичная регистрация пользователей.
- TLS MITM, выпуск сертификатов для чужих доменов и расшифровка пользовательского HTTPS.
- Полноценный VPN-клиент на конечном устройстве.
- Обход сервисов, использующих certificate pinning, IP literals либо скрывающих имя назначения так, что его невозможно восстановить.
- Гарантированная бесшовная миграция уже установленного TCP-соединения между egress-нодами.
- Anycast/BGP. Архитектура не должна исключать это в будущем.

## 4. Основные принципы

1. **Панель не проксирует пользовательский трафик.** Она может находиться дома, за NAT через исходящее соединение агента или на отдельном сервере.
2. **Ноды работают автономно.** Потеря панели не ломает применённую конфигурацию, DNS, действующие и новые подключения.
3. **Один источник истины.** Один normalized rule-set используется и для DNS rewrite, и для маршрутизации ingress. Рассинхрон запрещён.
4. **Immutable revisions.** Конфигурация сначала собирается, валидируется и тестируется, затем атомарно активируется.
5. **Fail closed для управления, fail operational для data plane.** Недействительный новый revision не заменяет LKG.
6. **Без MITM.** Между устройством и origin сохраняется исходный end-to-end TLS.
7. **Минимум привилегий.** Панель не требует host network; агент и data plane получают только необходимые capabilities.
8. **Воспроизводимость.** Образы закреплены digest-ами, конфиги генерируются детерминированно, обновления имеют миграции и rollback.

## 5. Целевые сценарии

### 5.1. Managed HTTPS

```text
Телефон → DNS ingress: gemini.google.com?
        ← A/AAAA ingress-ноды, TTL 30–60 с

Телефон → ingress:443, TLS ClientHello SNI=gemini.google.com
Ingress → защищённый туннель → egress DE
Egress → настоящий gemini.google.com:443
Gemini видит публичный IP egress DE
```

Ingress и egress передают зашифрованные TLS-байты. Сертификат предоставляет настоящий origin, клиент проверяет его для исходного hostname.

### 5.2. Обычный домен

```text
Телефон → DNS ingress: vk.com?
DNS frontend → Unbound → authoritative DNS
Телефон ← настоящий A/AAAA
Телефон → vk.com напрямую
```

### 5.3. Панель недоступна

Ноды продолжают использовать LKG. Агент экспоненциально переподключается, не очищает файлы и не перезапускает исправный data plane. В UI после восстановления отображаются пропущенные heartbeat и фактические active revision.

## 6. Высокоуровневая архитектура

```text
                         CONTROL PLANE
        ┌──────────────────────────────────────────┐
        │ Web UI → API → PostgreSQL                │
        │             ↘ scheduler / rule fetcher   │
        │              ↘ compiler / revision store │
        └───────────────────┬──────────────────────┘
                            │ outbound mTLS/HTTPS
               config pull │ heartbeat/events
                ┌───────────┴───────────┐
                ▼                       ▼
       INGRESS GROUP RU           EGRESS GROUP EU
   ┌────────────────────┐      ┌───────────────────┐
   │ ingress-01         │      │ egress-de-01      │
   │ edge/SNI mux       │      │ agent             │
   │ DNS frontend       │      │ sing-box inbound  │
   │ Unbound            │      │ direct Internet   │
   │ sing-box + agent   │      └───────────────────┘
   └────────────────────┘
   ┌────────────────────┐      ┌───────────────────┐
   │ ingress-02 ...     │      │ egress-fi-01 ...  │
   └────────────────────┘      └───────────────────┘
```

Control plane публикует желаемое состояние. Агент сообщает фактическое состояние. Панель не должна считать revision применённым, пока агент не вернул успешный apply с тем же SHA-256.

## 7. Рекомендуемый технологический стек

Реализация должна быть монорепозиторием.

- Backend/API/compiler/scheduler: Go, один модуль с разделёнными пакетами.
- Agent: Go, отдельный статический бинарник.
- DNS frontend: Go; библиотека с полноценной поддержкой DNS wire format, DoH и DoT. Не писать DNS parser вручную.
- Web UI: TypeScript + React; сборка в статические assets, раздаваемые backend или отдельным nginx.
- DB: PostgreSQL 16+ либо версия, выбранная и закреплённая в compatibility matrix.
- Queue: встроенная durable job table в PostgreSQL. Redis не вводить в v1 без измеренной необходимости.
- Resolver: Unbound, только во внутренней Docker-сети/loopback.
- Data plane: sing-box, версия строго закреплена; конфиг генерировать под выбранную schema version.
- Edge для shared 443: HAProxy либо nginx stream с `ssl_preread`; выбрать один и покрыть интеграционными тестами.
- Metrics: Prometheus format; dashboards для Grafana поставлять JSON-файлами.
- Logs: JSON stdout с ротацией Docker/journald.

До начала разработки создать `COMPATIBILITY.md` с точными версиями Go, Node, PostgreSQL, Unbound, sing-box, Docker Engine и Compose. Нельзя использовать `latest`.

## 8. Структура репозитория и обязательные артефакты

```text
/
├── cmd/panel-api/
├── cmd/node-agent/
├── cmd/dns-frontend/
├── internal/api/
├── internal/auth/
├── internal/compiler/
├── internal/domainset/
├── internal/health/
├── internal/jobs/
├── internal/revisions/
├── internal/store/
├── web/
├── migrations/
├── deploy/panel/
├── deploy/node/
├── deploy/examples/
├── configs/templates/
├── scripts/install-panel.sh
├── scripts/install-node.sh
├── scripts/upgrade.sh
├── scripts/backup.sh
├── scripts/restore.sh
├── tests/unit/
├── tests/integration/
├── tests/e2e/
├── docs/
├── docker-compose.yml
├── .env.example
├── Makefile
├── SECURITY.md
├── OPERATIONS.md
├── API.md
├── COMPATIBILITY.md
└── README.md
```

Репозиторий обязан содержать исходный код, миграции, Dockerfiles, Compose-профили, примеры конфигурации, CI, тесты, install/upgrade/backup/restore и документацию. Секреты, приватные ключи и рабочие `.env` в git запрещены.

## 9. Control plane

### 9.1. Функции панели

- логин администратора, смена пароля, TOTP 2FA, управление сессиями;
- CRUD нод, групп, сервисов, rule-set, источников, policies и device profiles;
- выдача одноразовых enrollment-токенов;
- плановое скачивание rule-set;
- preview diff, approval, компиляция revision;
- отображение desired/applied revision для каждой ноды;
- health, latency matrix, история переключений;
- аудит всех изменений;
- экспорт/импорт declarative configuration без секретов;
- backup status и диагностика.

### 9.2. Компоненты backend

1. **HTTP API** — валидация запросов, RBAC, optimistic locking.
2. **Domain-set fetcher** — безопасно скачивает внешние источники.
3. **Parser/normalizer** — приводит записи к canonical form.
4. **Compiler** — строит общий model snapshot и node-specific artifacts.
5. **Revision manager** — хранит immutable manifest, hashes и статусы rollout.
6. **Job runner** — lease-based обработка задач из PostgreSQL с retry.
7. **Health controller** — объединяет active и synthetic проверки, рассчитывает effective members.
8. **Agent gateway** — HTTPS/mTLS endpoint для pull, heartbeat и reports.

### 9.3. Требования к UI

Минимальные разделы:

- Dashboard;
- Nodes;
- Ingress Groups;
- Egress Groups;
- Services;
- Rule Sets;
- Route Policies;
- Revisions/Deployments;
- Devices/Setup;
- Health/Events;
- Audit Log;
- Settings/Backups.

Каждая опасная операция должна иметь preview. Удаление используемой сущности запрещается с `409 Conflict` и списком зависимостей. На страницах отображать timestamps одновременно в локальной timezone и UTC в tooltip.

## 10. Node agent

### 10.1. Регистрация

Install script принимает `--panel-url`, `--token`, `--role ingress|egress`, `--name`. Агент генерирует локальную ключевую пару, отправляет public key и fingerprint. Enrollment-токен:

- одноразовый;
- хранится в БД только как hash;
- имеет TTL не более 30 минут по умолчанию;
- привязан к предполагаемой роли;
- становится недействительным после успешного обмена.

Панель выдаёт node ID и короткоживущий client certificate либо подписанный credential с rotation. Приватный ключ ноды никогда не покидает ноду.

### 10.2. Pull-модель

Агент инициирует все управляющие соединения к панели. Это позволяет панели находиться за NAT при наличии доступного reverse tunnel/HTTPS endpoint и не требует открывать agent management port на ноде.

Цикл:

1. heartbeat каждые 15 секунд с jitter;
2. запрос desired revision через long poll либо conditional GET/ETag;
3. загрузка manifest и artifacts;
4. проверка подписи, SHA-256, node ID, role и минимальной версии агента;
5. запись в staging directory;
6. offline validation: JSON schema, `sing-box check`, `unbound-checkconf`, проверка edge config;
7. локальные smoke probes на staging ports/network namespace, где возможно;
8. atomic rename/symlink switch;
9. graceful reload/recreate только затронутых containers;
10. post-apply probes;
11. report `applied` либо rollback на LKG с причиной.

### 10.3. Локальное хранение

```text
/var/lib/smartdns-agent/
├── identity/
├── revisions/<revision-id>/
├── active -> revisions/...
├── previous -> revisions/...
├── state.json
└── locks/
```

Сохранять минимум 3 последних валидных revisions. Удалять старые только после успешного apply и с защитой active/previous. При заполнении диска агент не должен удалять LKG.

### 10.4. Защита от restart loop

Не более 3 автоматических apply/rollback попыток за 10 минут. Затем node state `degraded`, data plane остаётся на LKG. Ручной retry доступен из панели.

## 11. Ingress node

### 11.1. Контейнеры

- `edge-mux` — только при shared-IP topology;
- `dns-frontend`;
- `unbound`;
- `sing-box`;
- `node-agent`;
- опциональный local metrics exporter.

### 11.2. Потоки и порты

Рекомендуемый production-вариант — два публичных IP:

| Назначение | Bind |
|---|---|
| Plain DNS | DNS_IP:53 TCP/UDP |
| DoT | DNS_IP:853 TCP |
| DoH | DNS_IP:443 TCP |
| Managed HTTPS proxy | PROXY_IP:443 TCP и, если включено, UDP |

Если доступен один IPv4, обязателен `edge-mux` на TCP/443:

- SNI `dns.example.net` → TLS/DoH frontend на внутренний `dns-frontend:8443`;
- все остальные допустимые SNI → sing-box inbound на `sing-box:9443`;
- отсутствующий/недопустимый SNI → reject;
- health/admin hostnames не должны случайно попадать в proxy.

Нельзя пытаться одновременно bind `dns-frontend:443` и `sing-box:443` на одном IP. UDP/443 не может быть разделён обычным TCP SNI mux; QUIC требует отдельного IP, QUIC-aware dispatcher либо отключения.

### 11.3. DNS frontend

Frontend принимает DNS message, приводит QNAME к lowercase FQDN без завершающей точки для match, сохраняет исходный ID/flags и выполняет:

1. аутентификацию/ACL/rate limit;
2. поиск exact match;
3. поиск самого специфичного suffix match по label boundary;
4. применение exclusions с приоритетом над includes;
5. определение service и ingress policy;
6. синтез A/AAAA ingress addresses для managed domain;
7. иначе передачу исходного wire query в Unbound;
8. возврат ответа клиенту с ограничением размера и корректной обработкой EDNS0.

Для managed domain:

- A содержит только eligible ingress addresses выбранной группы;
- AAAA выдаётся только при полностью работающем IPv6 data path; иначе NODATA, но не поддельный IPv6;
- TTL по умолчанию 60 секунд, диапазон 30–300;
- не копировать AD из стороннего ответа;
- пометить метрику `synthesized=true`;
- CNAME-цепочки не должны приводить к утечке обхода: правила обязаны включать конечные service dependencies, а diagnostic crawler выявляет unmanaged CNAME targets.

Обычные запросы полностью обслуживаются Unbound. Frontend не должен сам быть recursive resolver.

### 11.4. Unbound

Unbound слушает только loopback/internal network, недоступен из Интернета. Обязательные настройки:

- recursive caching mode с root hints либо выбранными trusted forwarders;
- DNSSEC validation и обновляемый trust anchor;
- qname minimisation;
- hide identity/version;
- serve-expired с разумным TTL для кратких upstream failures;
- prefetch;
- private-address/rebinding protection с явными исключениями;
- access-control только для DNS frontend;
- remote control только через Unix socket или локальную сеть с ключами;
- memory/cache limits, соответствующие размеру VPS.

### 11.5. TLS/SNI routing

Ingress принимает TCP stream, извлекает TLS ClientHello/SNI без termination. Допускаются только домены из того же compiled rule-set revision, который использует DNS. Затем metadata destination заменяется на sniffed domain с исходным портом и поток передаётся в выбранный sing-box outbound к egress.

Требования:

- timeout ClientHello 3 секунды;
- ограничение размера pre-read buffer;
- SNI должен пройти IDNA/canonical validation;
- IP literal, пустой SNI и SNI вне managed set отклоняются;
- запрещён произвольный open TCP proxy;
- origin DNS resolution выполняется на egress, а не на ingress;
- исходный client IP не передаётся origin; PROXY protocol допускается только внутри доверенной инфраструктуры и не до origin.

## 12. Egress node

Egress содержит agent и sing-box. Он принимает соединения только от аутентифицированных ingress peers по выбранному защищённому протоколу. Public Internet inbound к relay-порту ограничивается firewall/rate limit, где это совместимо с transport.

Egress должен:

- получить destination domain/port из туннельного протокола;
- разрешить domain через локальный защищённый resolver;
- проверить destination policy: разрешены только порты и домены, объявленные revision;
- блокировать loopback, RFC1918, link-local, metadata endpoints и собственные management networks после DNS resolution;
- повторять проверку при каждом новом resolution для защиты от DNS rebinding;
- устанавливать direct connection к origin;
- выполнять SNAT обычным сетевым стеком VPS;
- собирать агрегированные метрики без payload logging.

Egress не является общедоступным SOCKS/HTTP proxy. Любая возможность destination вне allowlist считается критической уязвимостью.

## 13. QUIC, HTTP/3, ECH и фундаментальные ограничения

### 13.1. QUIC

MVP гарантирует TCP/443. Для UDP/443 режимы:

1. `disabled-fallback` — ingress дропает/reject UDP/443, клиент обычно откатывается на TCP; режим по умолчанию.
2. `proxy` — включается только после успешных e2e-тестов конкретного сервиса; sing-box sniff/route должен сохранить UDP association и target.
3. `separate-ip` — QUIC dispatcher использует отдельный proxy IP.

UI обязан показывать, что HTTP/3 не гарантирован. Тесты должны проверить реальный fallback в Chrome/Firefox/Safari, а не предполагать его.

### 13.2. ECH

Smart DNS теряет настоящий destination IP из-за DNS rewrite и обычно восстанавливает origin по SNI. При Encrypted ClientHello внутренний SNI может быть недоступен. Поэтому:

- сервис с обязательным ECH не объявлять поддерживаемым без доказанного способа определения origin;
- health probe должен выявлять ECH-related failure;
- возможные будущие стратегии: выделенный proxy IP на service, IPv6 address-per-service, controlled DNS mapping с connection correlation или explicit application proxy;
- запрещено незаметно отправлять неопределённый трафик на «default origin»;
- документация должна прямо сообщать это ограничение.

### 13.3. Другие ограничения

Не гарантируются приложения с IP pinning, собственным DNS, обязательным QUIC без fallback, certificate pinning к нестандартному flow, непубличными dependency domains и соединениями без hostname. Rule-set должен включать все API/CDN/auth зависимости, но слишком широкие suffix вроде всего `google.com` недопустимы без явного предупреждения.

## 14. Rule-set subsystem

### 14.1. Источники

Поддержать:

- GitHub raw URL;
- GitHub `owner/repo`, ref, path через API/raw;
- произвольный HTTPS URL;
- manual include/exclude;
- built-in preset, поставляемый с проектом;
- импорт sing-box source rule-set JSON.

Для GitHub сохранять ref. Для production рекомендуется commit SHA или tag; branch разрешён для автообновления. Personal access token хранится зашифрованно и никогда не попадает в artifact ноды.

### 14.2. Защищённая загрузка

- Только HTTPS по умолчанию.
- DNS resolve и каждый redirect проверяются против loopback/private/link-local/metadata IP (SSRF protection).
- Максимум 5 redirects.
- Ограничения: 10 MiB compressed, 50 MiB decompressed, 1 000 000 entries; значения configurable.
- Connect/read/total timeouts.
- ETag и If-Modified-Since.
- Проверка content type не является единственным критерием.
- Опциональные expected SHA-256 и signature verification.
- Архивы распаковывать с защитой от zip bomb и path traversal либо не поддерживать в v1.

### 14.3. Парсинг и нормализация

Алгоритм:

1. удалить BOM, comments и пустые строки;
2. распознать explicit prefix;
3. привести Unicode hostname через IDNA к ASCII;
4. lowercase, удалить единственную terminal dot;
5. валидировать labels/total length;
6. `*.example.com` преобразовать в suffix `example.com` с документированной семантикой включения apex;
7. дедупликация;
8. применить manual excludes после union includes;
9. выявить конфликты между services;
10. отсортировать детерминированно;
11. рассчитать content SHA-256.

Regex выключен по умолчанию. При включении использовать безопасный linear-time engine, лимиты длины/числа и отдельное предупреждение. Match suffix только по границе label: `example.com` совпадает с `api.example.com`, но не с `notexample.com`.

### 14.4. Merge и приоритеты

```text
effective = (union(all enabled include sources) ∪ manual_add)
            − union(exclude sources)
            − manual_exclude
```

При overlap services действует явный priority route. Если priorities одинаковы, compilation MUST fail с перечислением конфликтов. Не допускается зависимость от порядка строк или случайного порядка map.

### 14.5. Обновление

Pipeline:

```text
fetch → parse → normalize → validate → diff → compile candidate
      → static checks → optional probes → approve/auto policy → activate
```

Ошибка любого шага оставляет активным предыдущий revision. Пустой результат после непустого списка считается suspicious и не применяется автоматически. Порог mass change по умолчанию: удалено >30% либо добавлено >1000 записей — manual approval.

UI diff показывает counts, первые/последние изменения, downloadable full diff, source revision и warnings.

### 14.6. Compiled artifacts

Один canonical normalized set компилируется в:

- compact matcher artifact для DNS frontend;
- sing-box rule-set/config fragment для ingress;
- egress allowlist;
- manifest с counts, hashes и source provenance.

Все artifacts одного revision имеют общий manifest ID. Смешивание версий запрещено.

## 15. Services, groups и routing policy

### 15.1. Service

Поля: name, slug, enabled, rule_set_id, ingress_group_id, egress_group_id, route_policy_id, allowed_ports, TCP/UDP mode, DNS TTL, priority, notes и probe definitions.

### 15.2. Ingress group

Режимы:

- `active_active`: DNS возвращает все healthy members, с rotation порядка;
- `primary_fallback`: возвращается primary, fallback — после подтверждённого отказа;
- `weighted`: адреса выдаются с контролируемой вероятностью; веса применяются на запрос, не обещают точного распределения.

Обычные primary/secondary DNS-настройки ОС не являются гарантированным failover: ОС могут использовать оба сервера или долго держаться за первый. Это должно быть отражено в UI и документации.

### 15.3. Egress group

- `primary_fallback`;
- `weighted`;
- `lowest_latency` с hysteresis;
- `manual_fixed`.

`least_connections` MAY быть добавлен, только если контроллер имеет достаточно свежие данные; глобально точное значение без общей state plane не обещать.

### 15.4. Failover semantics

- Пассивный локальный failover на ingress должен работать без панели: sing-box получает список outbounds и health/URL-test policy.
- Панельный health меняет будущие revisions/DNS eligibility.
- Не переключать состояние по одной неудаче: default unhealthy после 3 failures, healthy после 2 successes.
- Hysteresis lowest-latency: переключать, если кандидат быстрее минимум на 20% и 20 ms в 3 последовательных измерениях.
- Существующий TCP поток при падении egress завершается; новое соединение использует резерв.

## 16. Health checks

### 16.1. Уровни

1. Agent heartbeat.
2. Process/container health.
3. Port readiness.
4. Tunnel reachability ingress→egress.
5. Egress Internet/IP check.
6. Service synthetic probe по настоящему flow DNS→ingress→egress→origin.

Нельзя считать сервис здоровым только потому, что порт открыт.

### 16.2. Probes

Probe definition содержит hostname, TCP/UDP, path (если безопасно), ожидаемый status range, TLS hostname, timeout и interval. Не использовать authenticated user endpoints и не хранить пользовательские cookies. Для сервисов с bot protection допускается TLS-only probe плюс отдельный ручной e2e.

### 16.3. Состояния

`unknown`, `healthy`, `degraded`, `unhealthy`, `maintenance`, `disabled`. Maintenance исключает ноду из новых ответов/routes, но не удаляет конфигурацию.

## 17. Revision и rollout model

Состояния revision: `draft`, `compiled`, `validation_failed`, `awaiting_approval`, `deploying`, `active`, `partially_active`, `superseded`, `rolled_back`.

Manifest содержит:

```json
{
  "revision_id": "uuid",
  "sequence": 184,
  "created_at": "RFC3339",
  "compiler_version": "...",
  "model_sha256": "...",
  "artifacts": [{"node_id":"...","path":"...","sha256":"..."}],
  "min_agent_version": "...",
  "signature": "base64"
}
```

Rollout по умолчанию: одна canary ingress + одна canary egress, post-apply probes, затем остальные с concurrency limit 25%. Для установки с одной нодой canary равен этой ноде. Partial failure не должен автоматически откатывать уже здоровые ноды, если это создаст несовместимость; controller выбирает совместимый общий revision и показывает оператору план.

## 18. Модель данных PostgreSQL

Все таблицы имеют UUID PK, `created_at`, `updated_at`; изменяемые сущности — `version bigint` для optimistic locking. Секреты отделены.

| Таблица | Ключевые поля и ограничения |
|---|---|
| users | email unique, password_hash, totp_secret_encrypted, disabled_at |
| sessions | user_id, token_hash, expires_at, last_seen_at, ip, user_agent |
| nodes | name unique, role, public_ipv4/6, agent_version, desired_revision_id, applied_revision_id, status, last_seen_at |
| node_identities | node_id, cert_serial, public_key, fingerprint, not_before/after, revoked_at |
| enrollment_tokens | token_hash unique, role, expires_at, used_at |
| ingress_groups | name unique, mode, settings_json |
| ingress_group_members | group_id, node_id, priority, weight, enabled; unique pair |
| egress_groups | name unique, mode, settings_json |
| egress_group_members | group_id, node_id, priority, weight, enabled; unique pair |
| rule_sets | name unique, update_mode, interval, active_version_id, priority |
| rule_sources | rule_set_id, type, url/repo/ref/path, include_or_exclude, enabled, secret_id |
| rule_fetches | source_id, status, http metadata, content_hash, size, error, started/finished_at |
| rule_set_versions | rule_set_id, sequence, content_hash, counts_json, status, source_manifest_json |
| rule_entries | version_id, kind exact/suffix/regex, value; unique(version,kind,value) |
| services | name/slug unique, rule_set_id, ingress_group_id, egress_group_id, policy_id, settings_json |
| route_policies | name unique, mode, settings_json |
| health_checks | scope_type/id, type, config_json, enabled |
| health_samples | check_id, node_id, success, latency_ms, error_code, observed_at; partition/retention |
| revisions | sequence unique, state, model_hash, manifest_json, created_by, activated_at |
| revision_artifacts | revision_id, node_id, kind, object_key/path, sha256, size |
| node_deployments | node_id, revision_id, state, error_code/detail, started/finished_at |
| jobs | type, payload_json, state, run_at, attempts, lease_owner/until, last_error |
| audit_events | actor, action, object_type/id, request_id, before_json, after_json, created_at |
| secrets | kind, ciphertext, key_version, rotated_at; metadata only in normal API |
| device_profiles | name, type, config_json, secret_id, revoked_at |

Индексы обязательны по FK, `jobs(state,run_at)`, `nodes(last_seen_at)`, `health_samples(observed_at)`, `audit_events(created_at)` и `rule_entries(version_id,kind,value)`. Health/audit retention настраивается; deletion выполняется batch jobs. Миграции только forward с документированным rollback/backup gate.

## 19. API

### 19.1. Общие правила

- Prefix `/api/v1`.
- JSON UTF-8; timestamps RFC 3339 UTC.
- OpenAPI 3.1 генерируется и проверяется CI.
- Pagination cursor-based.
- Ошибки: `{code,message,details,request_id}`.
- `Idempotency-Key` для enrollment, compile, deploy, rollback и manual fetch.
- `If-Match`/version для update.
- UI использует тот же публичный API.

### 19.2. Основные endpoints

```text
POST   /auth/login                 POST /auth/logout
POST   /auth/totp/enable           GET  /auth/sessions

GET/POST       /nodes              GET/PATCH/DELETE /nodes/{id}
POST           /nodes/enrollment-tokens
POST           /nodes/{id}/maintenance

GET/POST       /ingress-groups     GET/PATCH/DELETE /ingress-groups/{id}
GET/POST       /egress-groups      GET/PATCH/DELETE /egress-groups/{id}
GET/POST       /services           GET/PATCH/DELETE /services/{id}
GET/POST       /route-policies     GET/PATCH/DELETE /route-policies/{id}

GET/POST       /rule-sets          GET/PATCH/DELETE /rule-sets/{id}
POST           /rule-sets/{id}/sources
POST           /rule-sets/{id}/fetch
GET            /rule-sets/{id}/diff
POST           /rule-sets/{id}/approve

POST           /revisions/compile
GET            /revisions/{id}
POST           /revisions/{id}/deploy
POST           /revisions/{id}/rollback

GET            /health/summary
GET            /health/samples
GET            /events
GET            /audit
GET/POST       /device-profiles
GET            /device-profiles/{id}/download
```

### 19.3. Agent API

```text
POST /agent/v1/enroll
POST /agent/v1/heartbeat
GET  /agent/v1/desired             # ETag/long poll
GET  /agent/v1/revisions/{id}/manifest
GET  /agent/v1/artifacts/{id}      # signed/authenticated, resumable
POST /agent/v1/deployments/report
POST /agent/v1/health/report
```

Agent endpoints требуют mTLS/rotating node credential. Node может читать только собственный artifact. Отозванный сертификат немедленно блокируется.

## 20. Device onboarding и DNS access control

### 20.1. Профили

- Android Private DNS: DoT hostname, с инструкцией о cellular support.
- Apple: подписываемый `.mobileconfig` для DoH/DoT; уникальный profile UUID.
- Windows: DoH template/instruction в зависимости от поддерживаемой версии.
- Router/OpenWrt: DoH/DoT/plain DNS example.
- Plain DNS: только для доверенных сетей или как явно предупреждённый compatibility mode.

### 20.2. Проблема открытого resolver

Android Private DNS (DoT hostname) обычно не передаёт произвольный bearer token. Мобильный source IP меняется, поэтому IP allowlist недостаточен. Следовательно, проект не должен обещать сильную per-device auth для стандартного DoT там, где ОС её не поддерживает.

Режимы доступа:

1. `allowlist` — лучший для фиксированных IP/VPN.
2. `doh-token` — уникальный URL/path/header для совместимых клиентов; токен хранится hash-ом.
3. `mtls` — для управляемых клиентов.
4. `restricted-public-dot` — для Android: строгий global/per-IP rate limit, concurrency cap, minimal answers, abuse monitoring; это контролируемо публичный resolver, а не криптографически персональный.

По умолчанию запрещена публикация unrestricted UDP/53 recursion. DNS amplification protection: RRL, лимит UDP response, минимизация ANY, TCP fallback, no recursion для неавторизованных источников.

## 21. Security requirements

### 21.1. Панель

- Argon2id password hashing с параметрами из конфигурации.
- Secure/HttpOnly/SameSite cookies; CSRF protection.
- TOTP и recovery codes.
- Rate limit login и lockout с безопасным recovery.
- RBAC минимум `owner`, `operator`, `viewer` даже если v1 использует owner.
- CSP, HSTS после подтверждения TLS, frame-ancestors none, безопасные headers.
- CORS deny by default.
- Redaction secrets в логах и audit.

### 21.2. Supply chain

- Multi-stage non-root images.
- SBOM для каждого release.
- Image signing и verification option.
- Dependency and container scanning в CI.
- Pin base images by digest.
- Release checksums и подписанные manifests.

### 21.3. Node hardening

- Host firewall default deny; открывать только необходимые public ports и SSH policy владельца.
- Containers: read-only rootfs, `no-new-privileges`, cap drop; выдавать `NET_BIND_SERVICE/NET_ADMIN` только нужному компоненту.
- Docker socket нельзя монтировать напрямую в agent. Использовать ограниченный supervisor API либо systemd/compose wrapper с allowlist операций. Если v1 всё же использует socket proxy, документировать risk и разрешить только конкретные endpoints.
- Secret files mode 0600, отдельные volumes.
- Автоматическая rotation node certificates и tunnel keys.
- Clock synchronization и alert по drift.

### 21.4. SSRF и open proxy

Rule fetcher и egress являются двумя основными SSRF поверхностями. Обязательны IP range deny после каждого DNS resolve/redirect, port allowlist, domain allowlist, rebinding protection и тесты metadata endpoints (`169.254.169.254`, IPv6 link-local и vendor-specific).

### 21.5. Приватность

По умолчанию не логировать полные DNS qnames и client IP. Допустимы агрегаты по service и hashed/truncated client identity. Debug query log включается явно, имеет TTL и заметное предупреждение. Payload трафика никогда не записывается.

## 22. Observability

### 22.1. Metrics

Минимум:

- DNS QPS, latency histogram, cache hit, synthesized/recursive counts, RCODE;
- rate-limit/rejected queries;
- active TCP/UDP sessions, bytes ingress/egress по service/node без high-cardinality domain labels;
- tunnel connect success/latency/failures;
- rule fetch duration/status/entries/diff;
- revision compile/deploy/apply/rollback;
- agent heartbeat age, version and config drift;
- health state and failover count;
- CPU, memory, disk, file descriptors и container restarts.

Нельзя использовать raw qname, client IP, request ID как Prometheus label.

### 22.2. Logs и tracing

JSON fields: timestamp, level, component, node_id, request_id/correlation_id, event, error_code. Секреты и query payload redacted. Distributed tracing нужен для control-plane requests/jobs; не трассировать каждый проксируемый packet.

### 22.3. Alerts

- node heartbeat age >60 s;
- data plane unhealthy;
- revision drift >10 min;
- all egress members service unavailable;
- rule fetch stale >2 intervals;
- certificate expires <14 days;
- disk >80/90%;
- DNS rejection/traffic anomaly;
- repeated rollback/restart loop;
- backup older than policy.

## 23. Docker deployment

### 23.1. Panel Compose

Сервисы: `panel-api`, `postgres`, опционально `reverse-proxy`, `backup`. PostgreSQL не публикует порт наружу. Healthchecks должны проверять readiness, не только process existence. Secrets передаются файлами/Docker secrets, не command-line arguments.

### 23.2. Node Compose

Ingress и egress используют отдельные Compose profiles. Generated config монтируется read-only. Agent state — persistent volume. `network_mode: host` допускается только для data plane при доказанной необходимости; предпочтительно явное port mapping. Upgrade не должен пересоздавать identity volume.

### 23.3. Ресурсы

Документировать стартовый минимум: panel 2 vCPU/2–4 GiB RAM, PostgreSQL persistent disk; node 1 vCPU/1 GiB для малой персональной нагрузки. Это не SLA: нагрузочные тесты должны дать фактические пределы. Добавить ulimit для file descriptors и conntrack guidance.

## 24. Installation flows

### 24.1. Панель

```bash
curl -fsSL https://RELEASE/install-panel.sh -o install-panel.sh
less install-panel.sh
sudo bash install-panel.sh
```

Скрипт обязан поддерживать non-interactive flags, проверять OS/arch/Docker/ports/disk, показывать план до изменений, создавать случайные секреты, скачивать подписанный release, запускать migrations, ждать readiness и печатать URL/путь к recovery data. Pipe-to-shell не должен быть единственным способом: документация предлагает сначала скачать и проверить checksum.

Wizard запрашивает public URL, TLS mode, timezone/display locale, admin email и backup directory. Повторный запуск идемпотентен.

### 24.2. Нода

Пользователь создаёт в UI token, копирует одну команду. Installer:

1. preflight ports/IP/DNS/time;
2. ставит только нужные файлы в `/opt/smartdns-node` и state в `/var/lib`;
3. создаёт identity;
4. enroll;
5. загружает role-specific Compose;
6. применяет initial revision;
7. выполняет local и panel-observed smoke tests;
8. показывает firewall rules и результат.

Нельзя объявлять установку успешной до e2e probe.

### 24.3. Добавление первого сервиса

UI wizard: Rule Set → source/preview → Service → ingress group → egress group → probe → compile → canary deploy → generate device profile → guided verification (`dig`, TLS certificate, observed egress IP).

### 24.4. Upgrade и rollback

- backup DB и current manifests;
- compatibility/preflight;
- migration gate;
- rolling panel update;
- agents/nodes canary-first;
- автоматический rollback binary/image при failed health, кроме необратимой DB migration;
- migration policy должна избегать необратимых изменений в одном release (expand/migrate/contract).

## 25. Backup и disaster recovery

Backup включает PostgreSQL logical dump/custom format, encrypted secrets/key material, panel signing keys, declarative config и manifests. Не включать runtime logs по умолчанию. Backups шифруются, имеют checksum, retention и опциональную внешнюю destination.

RPO default 24 h, RTO target 2 h для персональной установки. Ежеквартальный restore test обязателен в документации/CI fixture. Без panel backup ноды продолжают LKG, но полноценное управление не восстанавливается автоматически. Restore на новую панель должен сохранить node IDs/cert trust либо предоставить контролируемый re-enrollment.

## 26. Failure modes и требуемое поведение

| Отказ | Ожидаемое поведение |
|---|---|
| Panel/API down | Ноды продолжают LKG; UI недоступен; агенты backoff+jitter. |
| PostgreSQL down | Панель read/write unavailable; ноды не затронуты. |
| GitHub/HTTP source down | Активный rule-set не меняется; stale alert. |
| Source вернул HTML/empty/corrupt | Candidate rejected; LKG остаётся. |
| Compiler bug/invalid sing-box config | Pre-apply validation fail; ничего не перезапускается. |
| Agent crash | Data plane контейнеры продолжают работу; supervisor перезапускает agent. |
| Ingress down | DNS group исключает ноду после threshold; клиентский failover зависит от DNS cache/OS. |
| Egress down | Новые соединения локально идут на fallback; существующие могут оборваться. |
| Tunnel partition | Egress marked unhealthy for affected ingress; другой egress используется. |
| Unbound down | Managed synthesized answers MAY работать; обычные DNS получают SERVFAIL, alert; не отдавать ложные ответы. |
| DNS frontend down | Resolver endpoint недоступен; клиент использует secondary DNS, если ОС решит. |
| Shared 443 mux misroute | Reject unknown SNI; health detects DoH/proxy separately; rollback. |
| Certificate expired | Alert заранее; DoH/DoT fail безопасно, passthrough origin TLS не зависит от DNS cert. |
| Disk full | Не принимать candidate; сохранить active/LKG; alert. |
| Clock skew | Не применять revision с недействительной подписью/cert; degraded alert. |
| IPv6 partially broken | Не публиковать AAAA ingress до полного e2e health. |
| ECH hides SNI | Reject/diagnose; не превращать в open/default proxy. |
| DNS poisoning upstream | Unbound DNSSEC validation; SERVFAIL для bogus. |
| Both panel and all egress down | DNS SHOULD перестать синтезировать неработающий ingress после локальной health policy, где возможно; иначе соединения fail, ordinary DNS remains. |

## 27. Подводные камни, которые нельзя скрывать

1. DNS сам не переносит трафик; обязателен L4 proxy/tunnel.
2. DNS rewrite меняет только destination IP, а настоящий hostname восстанавливается по SNI; ECH может сломать модель.
3. Один IP/TCP 443 требует SNI mux; UDP 443 — отдельная проблема.
4. TLS passthrough не означает reverse proxy: нельзя завершать TLS сертификатом собственного домена.
5. DNSSEC для синтезированного ответа не может сохранять оригинальную подпись. Frontend не должен выставлять ложный AD.
6. Слишком широкий список (`google.com`, `gstatic.com`) уводит лишний трафик, увеличивает стоимость и может ломать локальную географию/CDN.
7. Слишком узкий список ломает login/API/assets. Нужны probes и CNAME/dependency diagnostics.
8. Низкий TTL ускоряет failover, но увеличивает DNS QPS; ОС всё равно может игнорировать ожидания TTL.
9. Два DNS адреса на клиенте не гарантируют primary/fallback semantics.
10. Мобильный DoT без дополнительной клиентской auth трудно сделать одновременно персональным и не публичным.
11. UDP/QUIC stateful proxy сложнее TCP; нельзя заявлять поддержку только по наличию sniff option.
12. Egress IP может быть заблокирован самим сервисом или иметь плохую репутацию; health должен отличать network failure от geo/account denial.
13. Некоторые сервисы привязывают сессию к региону, account country, cookies, payment profile — Smart DNS не гарантирует обход таких ограничений.
14. Подмена A при наличии настоящего HTTPS/SVCB/HTTPS RR требует согласованной политики. На managed domain frontend должен не выдавать origin hints, которые позволят обойти ingress; обработку типов 64/65 покрыть тестами.
15. Happy Eyeballs выберет IPv6, если опубликован AAAA; неполный IPv6 превращает систему в случайно неработающую.
16. Open resolver и open proxy быстро приводят к abuse. Без ACL/rate limit выпуск запрещён.
17. Docker socket даёт почти root-доступ; прямой mount — исключение, не нормальный дизайн.
18. Автообновление GitHub branch — supply-chain риск; pin/signature/mass-diff approval обязательны для критичных списков.

## 28. Test plan

### 28.1. Unit tests

- IDNA/lowercase/trailing-dot normalization;
- exact/suffix label boundaries, wildcard/apex semantics;
- includes/excludes/priority conflicts;
- parsers каждого формата, BOM/comments/invalid lines;
- deterministic output/hash;
- suspicious mass diff;
- SSRF IP classification IPv4/IPv6 и redirects;
- policy selection/hysteresis;
- API validation, RBAC, idempotency и optimistic locking;
- revision state machine;
- secret redaction.

### 28.2. Integration tests

В CI поднять panel, PostgreSQL, ingress, egress, Unbound и mock origins через контейнеры/network namespaces.

- ordinary domain returns mock authoritative IP and goes direct;
- managed A/AAAA returns ingress;
- TLS passthrough получает сертификат mock origin;
- mock origin видит egress source, не ingress/client;
- SNI вне allowlist rejected;
- egress private/metadata destination rejected;
- DoH GET/POST, DoT и DNS TCP/UDP interoperability;
- shared 443 correctly dispatches DoH hostname and managed SNI;
- DNSSEC valid/bogus behavior ordinary domains;
- HTTPS/SVCB managed policy;
- rule-set update → candidate → atomic activate;
- invalid update keeps LKG;
- artifact tamper/signature failure;
- panel outage during traffic/apply;
- agent rollback after broken config;
- certificate/key rotation.

### 28.3. E2E on real VPS

Матрица минимум: Android Private DNS on cellular/Wi-Fi, iOS profile on cellular/Wi-Fi, macOS, Windows, OpenWrt. Проверять ChatGPT/Gemini/Claude только там, где это разрешено правилами сервиса и законом владельца.

Для каждого:

1. DNS ordinary/managed result.
2. Browser/app login and assets.
3. TLS certificate belongs to origin.
4. Observed egress country/IP.
5. Non-managed IP remains client ISP IP.
6. QUIC enabled/disabled behavior.
7. ingress failure and recovery.
8. egress failure and recovery.
9. panel shutdown for минимум 1 час.

### 28.4. Performance

- DNS steady/spike QPS and p50/p95/p99;
- 10k concurrent TCP connections or documented environment-scaled target;
- throughput and CPU per Gbit/s;
- connection setup latency overhead;
- rule-set 1M entries compile/memory/match latency;
- rollout 100 logical nodes via simulators;
- rate-limit/abuse tests.

Performance budgets для малой production установки:

- DNS frontend added p95 <10 ms без recursion;
- synthesized lookup p99 <5 ms на рекомендованном VPS;
- control API p95 <300 ms для обычного CRUD;
- config compile <60 s для 1M entries;
- agent heartbeat не создаёт unbounded DB growth.

### 28.5. Security tests

- auth/session/CSRF/XSS/SQL injection;
- enrollment replay/expired/wrong-role token;
- revoked node credential;
- malicious rule source SSRF/rebinding/oversize/regex DoS;
- open DNS amplification scan;
- open relay scan TCP/UDP;
- container privilege/secret exposure;
- artifact path traversal;
- dependency/image scan;
- fuzz DNS frontend and parsers.

### 28.6. Chaos tests

Kill/restart каждый container, network loss panel↔node и ingress↔egress, packet loss/latency, disk full, clock skew, corrupted state, expired cert, DB failover/restore. Для каждого теста проверяется сохранение LKG и отсутствие restart loop.

## 29. Acceptance criteria

Проект считается принятым только если выполнены все пункты:

- [ ] Чистая машина устанавливает panel по документации без ручной сборки файлов.
- [ ] Ingress и egress регистрируются одноразовым token и появляются healthy.
- [ ] Панель может находиться отдельно и не видит пользовательский payload.
- [ ] Managed hostname возвращает ingress IP, ordinary hostname — настоящий IP через Unbound.
- [ ] TLS managed service проходит без MITM; клиент видит origin certificate.
- [ ] Origin test server фиксирует IP egress, не ingress/client.
- [ ] Невключённый SNI и private destination нельзя использовать как open proxy.
- [ ] Один rule-set revision одновременно управляет DNS, ingress routing и egress allowlist.
- [ ] GitHub update проходит diff/validation/activation; битый/пустой update не заменяет LKG.
- [ ] Две ingress и две egress ноды работают в группах.
- [ ] При падении primary egress новые соединения идут через fallback в пределах health thresholds.
- [ ] При падении панели data plane работает минимум 24 часа без degradation из-за control plane.
- [ ] Broken revision автоматически откатывается.
- [ ] DoH, DoT и поддерживаемый plain DNS проходят interoperability tests.
- [ ] Shared-IP install не имеет конфликта TCP/443; либо используется отдельный DNS IP.
- [ ] IPv6 AAAA публикуется только после e2e IPv6 probe.
- [ ] UI отображает desired/applied revision, health, rule diff, events и audit.
- [ ] OpenAPI, DB migrations, backups и tested restore присутствуют.
- [ ] Нет `latest`, секретов в git/logs и прямого unrestricted open resolver/open proxy.
- [ ] Unit, integration, e2e smoke, security и failure tests проходят CI/release pipeline.
- [ ] README содержит пошаговую настройку Android, Apple, Windows и OpenWrt с ограничениями.
- [ ] Все известные ограничения QUIC/ECH/DNSSEC synthesis явно показаны в документации и UI.

## 30. Этапы реализации

### Phase 0 — архитектурный spike

Доказать на контейнерном стенде: DNS rewrite → ingress SNI sniff → tunnel → egress → mock origin; отдельно shared TCP/443 и QUIC fallback. Результат — ADR с packet captures без персональных данных и точными версиями sing-box/edge.

### Phase 1 — минимальный data plane

DNS frontend, Unbound, одна ingress/egress, статический rule-set, Compose, e2e test. Без панели, но с тем же artifact format.

### Phase 2 — control plane и agent

Enrollment, nodes/groups/services, compiler, immutable revisions, atomic apply/rollback, базовый UI.

### Phase 3 — rule sources и failover

GitHub/HTTP fetch, diff/approval/LKG, health controller, multi-node groups, canary rollout.

### Phase 4 — security/operations

2FA, audit, key rotation, backup/restore, observability, installers, hardening, device profiles.

### Phase 5 — production validation

Real-device matrix, load/security/chaos tests, upgrade from previous release, documentation freeze и release artifacts.

Переход к следующей фазе возможен только после тестов предыдущей. Нельзя сначала построить большую панель, не доказав фундаментальный L4 flow.

## 31. Обязательные ADR (Architecture Decision Records)

Исполнитель должен зафиксировать:

1. выбранный sing-box transport и threat model;
2. способ восстановления destination из SNI и поведение при ECH;
3. separate-IP против shared-443;
4. QUIC policy;
5. DNS authentication modes и Android limitation;
6. PostgreSQL job queue вместо Redis;
7. artifact signing и key rotation;
8. Docker supervision без unrestricted socket;
9. DNSSEC semantics для synthesized answers;
10. supported rule-set formats/version compatibility.

ADR содержит контекст, решение, альтернативы, последствия и тест, доказывающий решение.

## 32. Требования к документации и передаче результата

Готовый проект передаётся с:

- `README.md` quick start;
- подробным `OPERATIONS.md`;
- `SECURITY.md` и threat model;
- OpenAPI и примерами запросов;
- ER diagram и migration guide;
- topology diagrams;
- runbooks для всех failure modes из раздела 26;
- инструкции panel/ingress/egress/device;
- backup/restore drill;
- release/upgrade/rollback guide;
- troubleshooting decision tree;
- changelog и compatibility matrix.

Каждая команда должна быть копируемой, но перед destructive action объяснять эффект. Все placeholders явно обозначать; нельзя оставлять скрытые ручные шаги «настроить по необходимости».

## 33. Инструкции реализующей модели

Реализующая модель должна воспринимать этот документ как контракт. Если конкретный параметр внешнего компонента изменился в закреплённой версии, следует:

1. проверить официальную документацию выбранной версии;
2. сохранить требуемую семантику;
3. записать отклонение в ADR/COMPATIBILITY;
4. добавить автоматический test, доказывающий эквивалентность.

Запрещено:

- заменять рабочий код псевдокодом или TODO;
- выпускать compose с конфликтующими портами;
- считать health только по `container is running`;
- очищать active rule-set при fetch failure;
- использовать панель как hop пользовательского трафика;
- завершать TLS чужих сервисов;
- создавать open resolver/open proxy;
- заявлять QUIC/ECH поддержку без e2e доказательства;
- хранить credentials в plaintext;
- завершать работу без clean-install и restore test.

Если весь объём нельзя безопасно реализовать одним изменением, сначала должен быть завершён Phase 0/1 vertical slice, затем последовательно остальные фазы. Однако конечная поставка по этому ТЗ должна содержать все обязательные части и проходить acceptance criteria.

## 34. Справочные первичные источники

При реализации сверять конкретную закреплённую версию с официальной документацией:

- sing-box: `https://sing-box.sagernet.org/`;
- Unbound: `https://unbound.docs.nlnetlabs.nl/`;
- Docker Engine/Compose: `https://docs.docker.com/`;
- PostgreSQL: `https://www.postgresql.org/docs/`;
- DNS over HTTPS RFC 8484, DNS over TLS RFC 7858, serve stale RFC 8767;
- Apple deployment/DNS settings и Android Private DNS — официальные platform docs.

Документация внешнего проекта не отменяет обязательных интеграционных тестов: особенно для SNI destination override, UDP/QUIC, shared 443 и reload semantics.

---

**Итоговое определение готовности:** владелец настраивает на устройстве только собственный DNS; выбранные сервисы автоматически проходят через ingress и нужный зарубежный egress, остальные соединения остаются прямыми; панель управляет множеством нод и rule-set, но её отказ не останавливает data plane; любые обновления применяются атомарно и могут быть проверены, отклонены или откачены.
