-- +goose Up
-- «В автовыборе»: a node (or one protocol) can be left out of the auto group and the
-- fastest-pick, while staying in the plain list and in its country's group. On by
-- default, as before.
ALTER TABLE nodes ADD COLUMN auto_include BIGINT NOT NULL DEFAULT 1;
ALTER TABLE inbounds ADD COLUMN auto_include BIGINT NOT NULL DEFAULT 1;

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS auto_include;
ALTER TABLE inbounds DROP COLUMN IF EXISTS auto_include;
