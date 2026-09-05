-- Wildcard-сертификат выпускается панелью через DNS-01 и раздаётся на ноды по
-- push-каналу. Токен Cloudflare при этом остаётся в панели: он может переписать
-- всю зону, а нода — самая уязвимая точка периметра (ADR 0012).
CREATE TABLE IF NOT EXISTS certificates (
  name       text PRIMARY KEY,           -- 'resolver' — серт резолвера
  domains    text[] NOT NULL DEFAULT '{}',
  cert_pem   text NOT NULL,
  key_pem    text NOT NULL,
  not_after  timestamptz NOT NULL,
  staging    boolean NOT NULL DEFAULT false,
  updated_at timestamptz NOT NULL DEFAULT now()
);
