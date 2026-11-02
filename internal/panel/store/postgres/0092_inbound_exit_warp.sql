-- +goose Up
-- A protocol can leave through the exit node's own WARP while another through the same
-- node leaves directly: the way out is the inbound's, not the whole node's. The route is
-- marked so the exit sends exactly these streams to WARP.
ALTER TABLE inbounds ADD COLUMN exit_warp BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE inbounds DROP COLUMN IF EXISTS exit_warp;
