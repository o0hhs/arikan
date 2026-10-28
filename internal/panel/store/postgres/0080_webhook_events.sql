-- +goose Up
-- Every event of the panel's feed can go to the admin's site, not only a subscription
-- sale. event_id makes queueing idempotent when the worker retries a batch.
ALTER TABLE webhook_deliveries ADD COLUMN event_id BIGINT;
CREATE UNIQUE INDEX webhook_deliveries_event_id ON webhook_deliveries (event_id);

-- +goose Down
DROP INDEX webhook_deliveries_event_id;
ALTER TABLE webhook_deliveries DROP COLUMN event_id;
