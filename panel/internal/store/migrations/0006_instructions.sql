-- Инструкции как данные, а не как код. Раньше тексты были зашиты в Go, и
-- поправить фразу (Google переставил пункт в настройках Android) значило
-- пересобрать и передеплоить панель. Теперь они правятся из панели (ADR 0012).
CREATE TABLE IF NOT EXISTS instructions (
  platform   text PRIMARY KEY
             CHECK (platform IN ('android_dot','apple_doh','apple_dot','windows_doh','router','plain')),
  body       text NOT NULL DEFAULT '',
  updated_at timestamptz NOT NULL DEFAULT now()
);

-- Картинки лежат в Postgres, а не в томе: у панели уже есть бэкап через
-- pg_dump, поэтому скриншоты попадают в резервную копию автоматически.
CREATE TABLE IF NOT EXISTS instruction_assets (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  filename     text NOT NULL,
  content_type text NOT NULL,
  bytes        bytea NOT NULL,
  size         int NOT NULL,
  created_at   timestamptz NOT NULL DEFAULT now()
);
