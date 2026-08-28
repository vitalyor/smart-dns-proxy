# Матрица совместимости

Версии зафиксированы. Тег `latest` не используется нигде: ни в Dockerfile,
ни в Compose, ни в CI.

## Сборка

| Компонент | Версия | Где закреплено |
|---|---|---|
| Go | 1.24.4 | `go.mod`, все Dockerfile, `.github/workflows/ci.yml` |
| Node.js | 22.12.0 | `deploy/panel/Dockerfile`, CI |
| npm-зависимости | `panel/web/package.json` | точные версии без диапазонов |

## Образы

| Образ | Тег |
|---|---|
| `golang` | `1.24.4-alpine` |
| `node` | `22.12.0-alpine3.20` |
| `alpine` | `3.20.3` |
| `postgres` | `16.4-alpine` |

Перед выпуском релиза теги заменяются на digest-ы (`image@sha256:…`) командой
`docker buildx imagetools inspect`. Digest-ы фиксируются в `CHANGELOG.md`.

## Runtime

| Компонент | Версия | Примечание |
|---|---|---|
| PostgreSQL | 16.4 | миграции только вперёд, expand/migrate/contract |
| Unbound | из Alpine 3.20 (1.20.x) | рекурсивный резолвер, только внутренняя сеть |
| Docker Engine | ≥ 24.0 | нужен Compose v2 как плагин |
| Docker Compose | ≥ 2.24 | используются `depends_on.condition` и `secrets.environment` |

## Библиотеки Go

| Модуль | Версия | Зачем |
|---|---|---|
| `github.com/miekg/dns` | v1.1.62 | полноценный разбор DNS wire format, DoT, TCP |
| `github.com/jackc/pgx/v5` | v5.7.2 | драйвер и пул PostgreSQL |
| `golang.org/x/crypto` | v0.31.0 | Argon2id |
| `golang.org/x/net` | v0.33.0 | IDNA |

DNS-парсер не пишется вручную. Prometheus-клиент не подключается: экспозиция
реализована в `shared/metrics` без внешних зависимостей.

## Совместимость агента и панели

Манифест ревизии содержит `min_agent_version`. Агент отказывается применять
ревизию, если его версия ниже, и сообщает об этом в панель кодом
`agent_too_old` — вместо тихого применения несовместимой конфигурации.

| Версия панели | Минимальная версия агента | Схема артефакта |
|---|---|---|
| 1.0.x | 1.0.0 | 1 |

## Клиентские платформы

Проверяется в матрице e2e (`docs/OPERATIONS.md`, раздел «Приёмка»).

| Платформа | DoT | DoH | Обычный DNS | Ограничение |
|---|---|---|---|---|
| Android 9+ | да | нет (в системных настройках) | да | Private DNS не передаёт токен доступа |
| iOS 14+ / macOS 11+ | да | да | да | нужен профиль `.mobileconfig` |
| Windows 11 | нет | да | да | Windows 10 не даёт задать произвольный шаблон DoH |
| OpenWrt 22+ | да (stubby) | да (https-dns-proxy) | да | настраивает всю сеть сразу |
