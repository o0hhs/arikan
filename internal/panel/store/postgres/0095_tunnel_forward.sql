-- +goose Up
-- The ports of a node's protocols that clients reach through the panel, carried over the
-- node's reverse tunnel. A JSON array of ports ("[443,8443]"); empty: reached directly.
-- IF NOT EXISTS: a database that briefly ran 0.5.5.11 already added it (as its 0094).
ALTER TABLE nodes ADD COLUMN IF NOT EXISTS tunnel_forward TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS tunnel_forward;
