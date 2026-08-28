# API

Полная машинная спецификация: [`docs/openapi.json`](docs/openapi.json) (OpenAPI 3.1).
Разбор файла проверяется в `make check`.

Веб-панель использует ровно тот же публичный API — приватного канала нет.

## Общие правила

- Префикс `/api/v1`, JSON в UTF-8, время в RFC 3339 UTC.
- Ошибка всегда одной формы: `{code, message, details, request_id}`.
  `request_id` дублируется в заголовке `X-Request-Id` и в журнале панели.
- Сессия — cookie `sdns_session` (HttpOnly, SameSite=Lax, Secure при HTTPS).
  Все изменяющие методы требуют заголовок `X-CSRF-Token`.
- `If-Match` с полем `version` объекта защищает от перезаписи чужих изменений
  (иначе `412`).
- `Idempotency-Key` на регистрации нод, сборке, выкате, откате и ручном
  обновлении списков: повтор возвращает сохранённый ответ.
- Постраничность курсорная: `cursor` и `next_cursor`.

## Примеры

Вход и получение CSRF-токена:

```bash
CSRF=$(curl -sS -c jar -H 'Content-Type: application/json' \
  -d '{"email":"you@example.net","password":"…"}' \
  https://panel.example.net/api/v1/auth/login | jq -r .csrf_token)
```

Токен регистрации ноды (значение показывается один раз):

```bash
curl -sS -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"role":"egress","node_name":"egress-de-01","ttl_seconds":900}' \
  https://panel.example.net/api/v1/nodes/enrollment-tokens
```

Проверить конфигурацию, ничего не сохраняя:

```bash
curl -sS -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -d '{"dry_run":true}' https://panel.example.net/api/v1/revisions/compile
```

Собрать и выкатить:

```bash
curl -sS -b jar -H "X-CSRF-Token: $CSRF" -H 'Content-Type: application/json' \
  -H "Idempotency-Key: $(uuidgen)" \
  -d '{"deploy":true}' https://panel.example.net/api/v1/revisions/compile
```

Изменение с защитой от гонки:

```bash
VER=$(curl -sS -b jar https://panel.example.net/api/v1/services | jq -r '.items[0].version')
curl -sS -b jar -X PATCH -H "X-CSRF-Token: $CSRF" -H "If-Match: $VER" \
  -H 'Content-Type: application/json' -d '{"dns_ttl":120}' \
  https://panel.example.net/api/v1/services/<id>
```

## Коды ошибок

| Код | HTTP | Что означает |
|---|---|---|
| `invalid_request` | 400 | тело или параметры не прошли валидацию |
| `unauthenticated` | 401 | нет действующей сессии |
| `invalid_credentials` | 401 | неверный email, пароль или код |
| `totp_required` | 401 | нужен второй фактор |
| `csrf_failed` | 403 | отсутствует или неверен `X-CSRF-Token` |
| `forbidden` | 403 | роль не позволяет действие |
| `cors_denied` | 403 | кросс-доменный запрос |
| `not_found` | 404 | объекта нет |
| `conflict` | 409 | объект используется; `details` — список зависимостей |
| `rule_conflict` | 409 | домен заявлен двумя сервисами с равным приоритетом; `details` — конфликты |
| `version_conflict` | 412 | объект изменился с момента чтения |
| `account_locked` | 429 | слишком много неудачных входов |
| `compile_failed` | 422 | конфигурация не компилируется |
| `fetch_failed` | 502 | источники недоступны; активная версия сохранена |

## API агентов

Отдельный слушатель (по умолчанию TCP/8443) со взаимным TLS. Предназначен
только для нод и намеренно не включён в `openapi.json`.

| Метод и путь | Аутентификация | Назначение |
|---|---|---|
| `GET /agent/v1/ca` | нет | CA панели; агент сверяет отпечаток из команды установки |
| `POST /agent/v1/enroll` | одноразовый токен | обмен токена и CSR на сертификат ноды |
| `POST /agent/v1/heartbeat` | сертификат ноды | статус ноды, ответ содержит назначенную ревизию |
| `GET /agent/v1/desired` | сертификат ноды | назначенная ревизия, ETag и длинный опрос |
| `GET /agent/v1/revisions/{id}/manifest` | сертификат ноды | подписанный манифест |
| `GET /agent/v1/revisions/{id}/artifact` | сертификат ноды | собственный артефакт ноды, ETag по SHA-256 |
| `POST /agent/v1/deployments/report` | сертификат ноды | отчёт о применении с SHA-256 |
| `POST /agent/v1/health/report` | сертификат ноды | локальные наблюдения |

Нода получает только собственный артефакт: выборка идёт по `node_id` из
клиентского сертификата. Отозванный сертификат блокируется немедленно —
отпечаток проверяется на каждом запросе.

## Метрики

`GET /metrics` на панели (без аутентификации, ограничьте доступ межсетевым
экраном) и на каждом компоненте ноды. Формат Prometheus. Метки
низкокардинальные: qname, клиентский IP и request id в метках запрещены.
Перечень рядов — в [`OPERATIONS.md`](OPERATIONS.md#наблюдение).
