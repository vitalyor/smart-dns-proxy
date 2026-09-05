-- Счётчики устройств. Панель держит накопительный итог у себя, потому что
-- счётчики на ноде живут в памяти и обнуляются при рестарте — иначе квоту
-- можно было бы обойти простым перезапуском контейнера (ADR 0012).
--
-- База хранится на пару (устройство, нода): при нескольких ingress-нодах у
-- одного токена свой счётчик на каждой, и суммировать надо приросты.
CREATE TABLE IF NOT EXISTS device_counters (
  device_id  uuid NOT NULL REFERENCES device_profiles(id) ON DELETE CASCADE,
  node_id    uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  last_raw   bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (device_id, node_id)
);

-- Набор токенов больше не настройка: источник истины — device_profiles, а
-- доставка идёт каналом доступа. Убираем осиротевшую строку, чтобы она не
-- выглядела значимой.
DELETE FROM settings WHERE key = 'doh_path_tokens';
