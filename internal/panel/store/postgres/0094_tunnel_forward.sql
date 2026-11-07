-- +goose Up
-- The ports of a node's protocols that clients reach through the panel, carried over the
-- node's reverse tunnel: a client connects to the panel on the port, and the panel forwards
-- it to the node's loopback. A JSON array of ports ("[443,8443]"); empty: the node's
-- protocols are reached directly, as before.
ALTER TABLE nodes ADD COLUMN tunnel_forward TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS tunnel_forward;
