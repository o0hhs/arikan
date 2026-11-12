-- +goose Up
-- This version belongs to the upstream/dev numbering the panel briefly moved to
-- (0.5.5.7–0.5.5.11). A database that ran one of those releases already has it applied; on a
-- fresh install it changes nothing, since the fork's own code does not use it. It is kept so
-- the migration numbering is continuous and an updated database is not refused as "newer".
SELECT 1;

-- +goose Down
SELECT 1;
