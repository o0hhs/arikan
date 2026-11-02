-- +goose Up
-- The last TOTP time step an admin spent: one code is one use, also when the panel
-- restarts between two tries (the in-memory guard alone forgets; the row does not).
ALTER TABLE admins ADD COLUMN totp_step BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE admins DROP COLUMN totp_step;
