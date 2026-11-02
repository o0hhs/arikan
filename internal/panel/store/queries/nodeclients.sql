-- name: AddNodeClientsTraffic :exec
-- What a node carried for its users in one batch: the same bytes the users' statistics and
-- the node's own counters took. A user deleted meanwhile is skipped, the rest still counts.
-- first_seen is kept from the first time the row was made (a touch may have made it).
INSERT INTO node_clients (user_id, node_id, up, down, first_seen, last_seen)
SELECT t.user_id, sqlc.arg(node_id)::bigint, t.up, t.down, sqlc.arg(now)::bigint, sqlc.arg(now)::bigint
FROM (SELECT unnest(sqlc.arg(user_ids)::bigint[]) AS user_id,
             unnest(sqlc.arg(up)::bigint[]) AS up,
             unnest(sqlc.arg(down)::bigint[]) AS down) AS t
JOIN users u ON u.id = t.user_id
JOIN nodes n ON n.id = sqlc.arg(node_id)::bigint
ORDER BY t.user_id
ON CONFLICT (user_id, node_id) DO UPDATE SET
  up = node_clients.up + excluded.up,
  down = node_clients.down + excluded.down,
  last_seen = GREATEST(node_clients.last_seen, excluded.last_seen);

-- name: TouchNodeClients :exec
-- Users seen online on a node: when they were last seen there. A batch of pairs, so a user
-- on several nodes gets a row per node; one deleted meanwhile is skipped.
INSERT INTO node_clients (user_id, node_id, first_seen, last_seen)
SELECT t.user_id, t.node_id, sqlc.arg(now)::bigint, sqlc.arg(now)::bigint
FROM (SELECT unnest(sqlc.arg(user_ids)::bigint[]) AS user_id,
             unnest(sqlc.arg(node_ids)::bigint[]) AS node_id) AS t
JOIN users u ON u.id = t.user_id
JOIN nodes n ON n.id = t.node_id
ORDER BY t.user_id, t.node_id
ON CONFLICT (user_id, node_id) DO UPDATE SET last_seen = GREATEST(node_clients.last_seen, excluded.last_seen);
