-- +goose Up
-- Custom admin permissions: the three built-in roles stay presets, while a custom admin
-- carries the list of areas it may open. An empty list keeps the old role's meaning.
ALTER TABLE admins ADD COLUMN permissions TEXT NOT NULL DEFAULT '[]';
ALTER TABLE admins DROP CONSTRAINT IF EXISTS admins_role_check;
ALTER TABLE admins ADD CONSTRAINT admins_role_check CHECK (role IN ('superadmin', 'support', 'tech', 'custom'));

-- +goose Down
ALTER TABLE admins DROP CONSTRAINT IF EXISTS admins_role_check;
ALTER TABLE admins ADD CONSTRAINT admins_role_check CHECK (role IN ('superadmin', 'support', 'tech'));
ALTER TABLE admins DROP COLUMN IF EXISTS permissions;
