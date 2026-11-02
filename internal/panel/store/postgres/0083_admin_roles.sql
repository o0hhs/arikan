-- +goose Up
-- The panel's admins get roles: a superadmin keeps everything (the till, the nodes, the
-- tariffs, naming admins); support sees one subscriber's card — the search, the status, the
-- bonus days, the sessions, the block — and nothing else; tech keeps the nodes, the
-- protocols and the logs. Every admin there was is a superadmin: nobody loses access.

ALTER TABLE admins ADD COLUMN role TEXT NOT NULL DEFAULT 'superadmin' CHECK (role IN ('superadmin', 'support', 'tech'));

-- +goose Down
ALTER TABLE admins DROP COLUMN IF EXISTS role;
