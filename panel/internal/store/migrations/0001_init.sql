-- Initial schema. Forward-only migrations; see docs/OPERATIONS.md for the
-- backup gate that must run before applying any migration.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE users (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  email         text NOT NULL UNIQUE,
  password_hash text NOT NULL,
  role          text NOT NULL DEFAULT 'owner' CHECK (role IN ('owner','operator','viewer')),
  display_name  text NOT NULL DEFAULT '',
  totp_secret_encrypted bytea,
  totp_enabled  boolean NOT NULL DEFAULT false,
  recovery_codes_hash text[] NOT NULL DEFAULT '{}',
  failed_logins int NOT NULL DEFAULT 0,
  locked_until  timestamptz,
  disabled_at   timestamptz,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE sessions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash   text NOT NULL UNIQUE,
  csrf_token   text NOT NULL,
  expires_at   timestamptz NOT NULL,
  last_seen_at timestamptz NOT NULL DEFAULT now(),
  ip           inet,
  user_agent   text NOT NULL DEFAULT '',
  created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON sessions (user_id);
CREATE INDEX ON sessions (expires_at);

CREATE TABLE nodes (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name          text NOT NULL UNIQUE,
  role          text NOT NULL CHECK (role IN ('ingress','egress')),
  public_ipv4   text,
  public_ipv6   text,
  relay_endpoint text,
  relay_sni     text,
  region        text NOT NULL DEFAULT '',
  country       text NOT NULL DEFAULT '',
  agent_version text NOT NULL DEFAULT '',
  status        text NOT NULL DEFAULT 'unknown'
                 CHECK (status IN ('unknown','healthy','degraded','unhealthy','maintenance','disabled')),
  desired_revision_id uuid,
  applied_revision_id uuid,
  last_seen_at  timestamptz,
  last_error    text NOT NULL DEFAULT '',
  notes         text NOT NULL DEFAULT '',
  labels        jsonb NOT NULL DEFAULT '{}',
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON nodes (last_seen_at);
CREATE INDEX ON nodes (role);

CREATE TABLE node_identities (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id     uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  cert_serial text NOT NULL,
  cert_pem    text NOT NULL,
  fingerprint text NOT NULL,
  not_before  timestamptz NOT NULL,
  not_after   timestamptz NOT NULL,
  revoked_at  timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON node_identities (node_id);
CREATE UNIQUE INDEX ON node_identities (fingerprint);

CREATE TABLE enrollment_tokens (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  token_hash text NOT NULL UNIQUE,
  role       text NOT NULL CHECK (role IN ('ingress','egress')),
  node_name  text,
  expires_at timestamptz NOT NULL,
  used_at    timestamptz,
  used_by_node uuid REFERENCES nodes(id) ON DELETE SET NULL,
  created_by uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ingress_groups (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name     text NOT NULL UNIQUE,
  mode     text NOT NULL DEFAULT 'active_active'
            CHECK (mode IN ('active_active','primary_fallback','weighted')),
  settings jsonb NOT NULL DEFAULT '{}',
  version  bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE egress_groups (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name     text NOT NULL UNIQUE,
  mode     text NOT NULL DEFAULT 'primary_fallback'
            CHECK (mode IN ('primary_fallback','weighted','lowest_latency','manual_fixed')),
  settings jsonb NOT NULL DEFAULT '{}',
  version  bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE ingress_group_members (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  group_id uuid NOT NULL REFERENCES ingress_groups(id) ON DELETE CASCADE,
  node_id  uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  priority int NOT NULL DEFAULT 1,
  weight   int NOT NULL DEFAULT 1,
  enabled  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (group_id, node_id)
);

CREATE TABLE egress_group_members (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  group_id uuid NOT NULL REFERENCES egress_groups(id) ON DELETE CASCADE,
  node_id  uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  priority int NOT NULL DEFAULT 1,
  weight   int NOT NULL DEFAULT 1,
  enabled  boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now(),
  UNIQUE (group_id, node_id)
);

CREATE TABLE rule_sets (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name          text NOT NULL UNIQUE,
  description   text NOT NULL DEFAULT '',
  update_mode   text NOT NULL DEFAULT 'manual_approve'
                 CHECK (update_mode IN ('auto_apply','manual_approve','manual_only')),
  interval_sec  int NOT NULL DEFAULT 21600,
  allow_regex   boolean NOT NULL DEFAULT false,
  priority      int NOT NULL DEFAULT 100,
  manual_include text[] NOT NULL DEFAULT '{}',
  manual_exclude text[] NOT NULL DEFAULT '{}',
  active_version_id uuid,
  last_fetch_at timestamptz,
  version       bigint NOT NULL DEFAULT 1,
  created_at    timestamptz NOT NULL DEFAULT now(),
  updated_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE rule_sources (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  rule_set_id uuid NOT NULL REFERENCES rule_sets(id) ON DELETE CASCADE,
  name        text NOT NULL DEFAULT '',
  type        text NOT NULL CHECK (type IN ('github_raw','github_repo','https','manual','preset','singbox_json')),
  url         text NOT NULL DEFAULT '',
  repo        text NOT NULL DEFAULT '',
  ref         text NOT NULL DEFAULT '',
  path        text NOT NULL DEFAULT '',
  mode        text NOT NULL DEFAULT 'include' CHECK (mode IN ('include','exclude')),
  expected_sha256 text NOT NULL DEFAULT '',
  enabled     boolean NOT NULL DEFAULT true,
  secret_id   uuid,
  etag        text NOT NULL DEFAULT '',
  last_modified text NOT NULL DEFAULT '',
  version     bigint NOT NULL DEFAULT 1,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON rule_sources (rule_set_id);

CREATE TABLE rule_fetches (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  source_id   uuid NOT NULL REFERENCES rule_sources(id) ON DELETE CASCADE,
  status      text NOT NULL,
  http_status int NOT NULL DEFAULT 0,
  etag        text NOT NULL DEFAULT '',
  content_hash text NOT NULL DEFAULT '',
  size_bytes  bigint NOT NULL DEFAULT 0,
  entries     int NOT NULL DEFAULT 0,
  error       text NOT NULL DEFAULT '',
  started_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz
);
CREATE INDEX ON rule_fetches (source_id, started_at DESC);

CREATE TABLE rule_set_versions (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  rule_set_id  uuid NOT NULL REFERENCES rule_sets(id) ON DELETE CASCADE,
  sequence     bigint NOT NULL,
  content_hash text NOT NULL,
  counts       jsonb NOT NULL DEFAULT '{}',
  status       text NOT NULL DEFAULT 'candidate'
                CHECK (status IN ('candidate','awaiting_approval','active','rejected','superseded')),
  source_manifest jsonb NOT NULL DEFAULT '{}',
  warnings     text[] NOT NULL DEFAULT '{}',
  created_by   uuid REFERENCES users(id) ON DELETE SET NULL,
  created_at   timestamptz NOT NULL DEFAULT now(),
  UNIQUE (rule_set_id, sequence)
);

CREATE TABLE rule_entries (
  version_id uuid NOT NULL REFERENCES rule_set_versions(id) ON DELETE CASCADE,
  kind       text NOT NULL CHECK (kind IN ('exact','suffix','regex')),
  value      text NOT NULL,
  PRIMARY KEY (version_id, kind, value)
);

CREATE TABLE route_policies (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name     text NOT NULL UNIQUE,
  mode     text NOT NULL DEFAULT 'primary_fallback',
  settings jsonb NOT NULL DEFAULT '{}',
  version  bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE services (
  id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name            text NOT NULL UNIQUE,
  slug            text NOT NULL UNIQUE,
  description     text NOT NULL DEFAULT '',
  enabled         boolean NOT NULL DEFAULT true,
  rule_set_id     uuid REFERENCES rule_sets(id) ON DELETE RESTRICT,
  ingress_group_id uuid REFERENCES ingress_groups(id) ON DELETE RESTRICT,
  egress_group_id uuid REFERENCES egress_groups(id) ON DELETE RESTRICT,
  route_policy_id uuid REFERENCES route_policies(id) ON DELETE SET NULL,
  allowed_ports   int[] NOT NULL DEFAULT '{443}',
  udp_mode        text NOT NULL DEFAULT 'disabled_fallback'
                   CHECK (udp_mode IN ('disabled_fallback','proxy','separate_ip')),
  dns_ttl         int NOT NULL DEFAULT 60 CHECK (dns_ttl BETWEEN 30 AND 300),
  priority        int NOT NULL DEFAULT 100,
  notes           text NOT NULL DEFAULT '',
  probe           jsonb NOT NULL DEFAULT '{}',
  version         bigint NOT NULL DEFAULT 1,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE revisions (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  sequence    bigserial UNIQUE,
  state       text NOT NULL DEFAULT 'draft'
               CHECK (state IN ('draft','compiled','validation_failed','awaiting_approval',
                                'deploying','active','partially_active','superseded','rolled_back')),
  model_hash  text NOT NULL DEFAULT '',
  manifest    jsonb NOT NULL DEFAULT '{}',
  summary     jsonb NOT NULL DEFAULT '{}',
  error       text NOT NULL DEFAULT '',
  created_by  uuid REFERENCES users(id) ON DELETE SET NULL,
  activated_at timestamptz,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE revision_artifacts (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  revision_id uuid NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
  node_id     uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  kind        text NOT NULL DEFAULT 'node-config',
  content     bytea NOT NULL,
  sha256      text NOT NULL,
  size_bytes  bigint NOT NULL,
  created_at  timestamptz NOT NULL DEFAULT now(),
  UNIQUE (revision_id, node_id, kind)
);

CREATE TABLE node_deployments (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  node_id     uuid NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
  revision_id uuid NOT NULL REFERENCES revisions(id) ON DELETE CASCADE,
  state       text NOT NULL DEFAULT 'pending'
               CHECK (state IN ('pending','downloading','validating','applying','applied','failed','rolled_back','skipped')),
  wave        int NOT NULL DEFAULT 0,
  error_code  text NOT NULL DEFAULT '',
  error_detail text NOT NULL DEFAULT '',
  started_at  timestamptz NOT NULL DEFAULT now(),
  finished_at timestamptz,
  UNIQUE (node_id, revision_id)
);
CREATE INDEX ON node_deployments (revision_id);

CREATE TABLE health_checks (
  id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  scope_type text NOT NULL CHECK (scope_type IN ('node','service','tunnel')),
  scope_id  uuid,
  type      text NOT NULL,
  config    jsonb NOT NULL DEFAULT '{}',
  enabled   boolean NOT NULL DEFAULT true,
  version   bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE health_samples (
  id          bigserial PRIMARY KEY,
  check_id    uuid REFERENCES health_checks(id) ON DELETE CASCADE,
  node_id     uuid REFERENCES nodes(id) ON DELETE CASCADE,
  service_id  uuid REFERENCES services(id) ON DELETE CASCADE,
  kind        text NOT NULL,
  success     boolean NOT NULL,
  latency_ms  int NOT NULL DEFAULT 0,
  error_code  text NOT NULL DEFAULT '',
  detail      text NOT NULL DEFAULT '',
  observed_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON health_samples (observed_at DESC);
CREATE INDEX ON health_samples (node_id, observed_at DESC);
CREATE INDEX ON health_samples (service_id, observed_at DESC);

CREATE TABLE jobs (
  id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  type        text NOT NULL,
  payload     jsonb NOT NULL DEFAULT '{}',
  state       text NOT NULL DEFAULT 'queued'
               CHECK (state IN ('queued','running','done','failed','cancelled')),
  run_at      timestamptz NOT NULL DEFAULT now(),
  attempts    int NOT NULL DEFAULT 0,
  max_attempts int NOT NULL DEFAULT 5,
  lease_owner text NOT NULL DEFAULT '',
  lease_until timestamptz,
  last_error  text NOT NULL DEFAULT '',
  dedupe_key  text UNIQUE,
  created_at  timestamptz NOT NULL DEFAULT now(),
  updated_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON jobs (state, run_at);

CREATE TABLE audit_events (
  id          bigserial PRIMARY KEY,
  actor       text NOT NULL DEFAULT '',
  actor_id    uuid,
  action      text NOT NULL,
  object_type text NOT NULL DEFAULT '',
  object_id   text NOT NULL DEFAULT '',
  request_id  text NOT NULL DEFAULT '',
  ip          inet,
  before_json jsonb,
  after_json  jsonb,
  created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON audit_events (created_at DESC);
CREATE INDEX ON audit_events (object_type, object_id);

CREATE TABLE events (
  id        bigserial PRIMARY KEY,
  level     text NOT NULL DEFAULT 'info' CHECK (level IN ('debug','info','warn','error')),
  component text NOT NULL DEFAULT '',
  node_id   uuid REFERENCES nodes(id) ON DELETE CASCADE,
  code      text NOT NULL DEFAULT '',
  message   text NOT NULL,
  data      jsonb NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON events (created_at DESC);

CREATE TABLE secrets (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  kind       text NOT NULL,
  name       text NOT NULL DEFAULT '',
  ciphertext bytea NOT NULL,
  key_version int NOT NULL DEFAULT 1,
  rotated_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE device_profiles (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name       text NOT NULL,
  type       text NOT NULL CHECK (type IN ('android_dot','apple_doh','apple_dot','windows_doh','router','plain')),
  config     jsonb NOT NULL DEFAULT '{}',
  token_hash text NOT NULL DEFAULT '',
  revoked_at timestamptz,
  version    bigint NOT NULL DEFAULT 1,
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE settings (
  key        text PRIMARY KEY,
  value      jsonb NOT NULL,
  updated_at timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE nodes ADD CONSTRAINT nodes_desired_revision_fk
  FOREIGN KEY (desired_revision_id) REFERENCES revisions(id) ON DELETE SET NULL;
ALTER TABLE nodes ADD CONSTRAINT nodes_applied_revision_fk
  FOREIGN KEY (applied_revision_id) REFERENCES revisions(id) ON DELETE SET NULL;
ALTER TABLE rule_sets ADD CONSTRAINT rule_sets_active_version_fk
  FOREIGN KEY (active_version_id) REFERENCES rule_set_versions(id) ON DELETE SET NULL;

CREATE TABLE idempotency_keys (
  key        text PRIMARY KEY,
  endpoint   text NOT NULL,
  response   jsonb NOT NULL,
  status     int NOT NULL,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX ON idempotency_keys (created_at);
