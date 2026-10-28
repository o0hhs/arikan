-- name: AddWebhookDelivery :exec
-- One event for the admin's site, due at next_at: queued in the transaction that made it.
INSERT INTO webhook_deliveries (event, payload, next_at, created_at) VALUES ($1, $2, $3, $4);

-- name: AddWebhookEventDelivery :exec
-- One feed event for the admin's site. event_id makes a retried batch a no-op.
INSERT INTO webhook_deliveries (event, payload, next_at, created_at, event_id)
VALUES ($1, $2, $3, $4, sqlc.arg(event_id)::BIGINT)
ON CONFLICT (event_id) DO NOTHING;

-- name: DueWebhookDeliveries :many
-- What the worker takes now: never delivered, and its next try has come.
SELECT * FROM webhook_deliveries WHERE delivered_at IS NULL AND next_at <= $1 ORDER BY next_at, id LIMIT $2;

-- name: WebhookDelivered :exec
-- A delivery the site took (any 2xx): its attempts counted, no error kept.
UPDATE webhook_deliveries SET attempts = attempts + 1, delivered_at = sqlc.arg(now), last_error = '' WHERE id = sqlc.arg(id);

-- name: WebhookRetry :exec
-- A delivery the site did not take: counted, and tried again at next_at with the reason.
UPDATE webhook_deliveries SET attempts = attempts + 1, next_at = sqlc.arg(next_at), last_error = sqlc.arg(err) WHERE id = sqlc.arg(id);

-- name: PruneWebhookDeliveries :exec
-- Delivered and failed rows alike, after the retention: the site had its tries.
DELETE FROM webhook_deliveries WHERE created_at < $1;
