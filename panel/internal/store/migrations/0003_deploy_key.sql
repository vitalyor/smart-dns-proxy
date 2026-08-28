-- Per-node GitHub deploy key id, so deleting a node can revoke its repo access.
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS deploy_key_id bigint;
