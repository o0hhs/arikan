-- +goose Up
-- The protocols a tariff closes. A closed inbound is out of every user-of-the-tariff's
-- subscription and off the nodes for them, whatever their own allow-list says: the
-- per-user switches work on what the tariff leaves open. Nothing is copied to users —
-- the profile and the node sync read the tariff — so opening a protocol again is instant.
ALTER TABLE tariffs ADD COLUMN closed TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE tariffs DROP COLUMN closed;
