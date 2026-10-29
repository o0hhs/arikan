-- +goose Up
-- The node the panel reaches this one through when its own path to it does not answer: the
-- panel asks that node's API for an HTTP CONNECT to this one's address and carries the
-- stream. 0 is the panel's choice (the first healthy node it reaches directly); a node's id
-- is the admin's.
ALTER TABLE nodes ADD COLUMN transport_id BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS transport_id;
