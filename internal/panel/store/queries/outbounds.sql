-- name: ListOutbounds :many
SELECT * FROM outbounds ORDER BY name, id;

-- name: GetOutbound :one
SELECT * FROM outbounds WHERE id = $1;

-- name: CreateOutbound :one
INSERT INTO outbounds (name, link, enabled, created_at, updated_at)
VALUES ($1, $2, $3, $4, $4)
RETURNING *;

-- name: UpdateOutbound :one
UPDATE outbounds SET name = $1, link = $2, enabled = $3, updated_at = $4 WHERE id = $5
RETURNING *;

-- name: CountOutbounds :one
SELECT count(*) FROM outbounds;

-- name: DeleteOutbound :execrows
DELETE FROM outbounds WHERE id = $1;

-- SetInboundRoute is the one write of where an inbound's traffic leaves: direct or warp
-- (the column), another node's relay (exit_node_id), or an outbound by link (outbound_id).
-- name: SetInboundRoute :exec
UPDATE inbounds SET outbound = $1, exit_node_id = $2, outbound_id = $3, exit_warp = $4 WHERE id = $5;
