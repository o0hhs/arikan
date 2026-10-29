-- +goose Up
-- A node may keep its own API port closed: the panel reaches it over the tunnel the node
-- opens out to it (a panel in one country, a node in another). Existing nodes keep their
-- port, as they always had it; a node added from now on starts with it closed, and the
-- panel says otherwise.
ALTER TABLE nodes ADD COLUMN inbound_api BIGINT NOT NULL DEFAULT 1;
ALTER TABLE nodes ALTER COLUMN inbound_api SET DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS inbound_api;
