-- Подписчики (конечные люди) и машинные ключи для сервиса страницы подписки.
-- См. ADR 0012.

-- Ссылка не хранится: адрес страницы собирается как {домен_из_настроек}/{short_id},
-- поэтому переезд страницы на другой домен не трогает ни одной записи.
CREATE TABLE IF NOT EXISTS subscribers (
  id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name              text NOT NULL,
  note              text NOT NULL DEFAULT '',
  short_id          text NOT NULL UNIQUE,
  enabled           boolean NOT NULL DEFAULT true,
  expires_at        timestamptz,                 -- NULL — бессрочно
  device_limit      int,                         -- NULL — берём общий из настроек
  query_limit       bigint,                      -- NULL — безлимит
  query_period      text NOT NULL DEFAULT 'month'
                    CHECK (query_period IN ('day','month','never')),
  -- Накопительный итог держит панель, а не нода: счётчики ноды живут в памяти и
  -- обнуляются при рестарте, поэтому показания ноды считаются приростом.
  queries_used      bigint NOT NULL DEFAULT 0,
  period_started_at timestamptz NOT NULL DEFAULT now(),
  version           bigint NOT NULL DEFAULT 1,
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);

-- Устройство принадлежит подписчику. NULL — профиль оператора «для себя»,
-- как было до появления подписчиков.
ALTER TABLE device_profiles
  ADD COLUMN IF NOT EXISTS subscriber_id uuid REFERENCES subscribers(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS device_profiles_subscriber_idx
  ON device_profiles (subscriber_id) WHERE subscriber_id IS NOT NULL;

-- Счётчики с ноды: сколько запросов сделало устройство и когда его видели.
-- Доменов здесь нет и не будет — историю запросов мы не храним сознательно.
ALTER TABLE device_profiles
  ADD COLUMN IF NOT EXISTS queries_total bigint NOT NULL DEFAULT 0;
ALTER TABLE device_profiles
  ADD COLUMN IF NOT EXISTS last_seen_at timestamptz;

-- Машинные ключи. Ключ со scope, а не полноправный токен дашборда: сервис
-- страницы подписки не должен уметь ничего, кроме своих подписчиков.
CREATE TABLE IF NOT EXISTS api_keys (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name         text NOT NULL,
  key_hash     text NOT NULL UNIQUE,
  scopes       text[] NOT NULL DEFAULT '{}',
  last_used_at timestamptz,
  revoked_at   timestamptz,
  created_at   timestamptz NOT NULL DEFAULT now()
);
