-- Push model: the panel connects out to each node's management port instead of
-- nodes dialing in. A node now carries the address the panel reaches it at, and
-- its identity is the TLS *server* cert the panel minted into the bundle.

ALTER TABLE nodes ADD COLUMN IF NOT EXISTS mgmt_address text NOT NULL DEFAULT '';

-- Health carries only DNS-meaningful signals; system telemetry lives in the
-- operator's host view, not here. These are the latest observed values.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS applied_sequence bigint NOT NULL DEFAULT 0;
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS health jsonb NOT NULL DEFAULT '{}';

-- The panel's own client certificate (for pushing) and its pinned fingerprint
-- live in the state volume, not the database. Nothing to add there.

-- Enrollment tokens are unused under push (the bundle replaces them). The table
-- is left in place so old rows and audit references stay valid.
