-- Отдельная key/value таблица для секретов панели (токен Cloudflare, ACME-ключ).
-- Таблица `secrets` из 0001 занята под другую схему (kind/ciphertext), поэтому
-- не переиспользуем её, чтобы ничего не сломать. Значение шифруется тем же
-- ключом панели, что и TOTP.
CREATE TABLE IF NOT EXISTS panel_secrets (
  key        text PRIMARY KEY,
  value      bytea NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);
