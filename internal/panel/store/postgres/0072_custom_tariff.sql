-- +goose Up
-- A payment for a subscription the buyer put together themselves: which base tariff it was
-- built on, what it gives (CustomPlan as JSON, in custom) and the price that was computed
-- from it (amount). The snapshot stays with the payment so a later change to the tariff the
-- buyer paid for is visible.
ALTER TABLE payments ADD COLUMN custom TEXT NOT NULL DEFAULT '';
ALTER TABLE payments ADD COLUMN custom_base_id BIGINT REFERENCES tariffs(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE payments DROP COLUMN custom_base_id;
ALTER TABLE payments DROP COLUMN custom;
