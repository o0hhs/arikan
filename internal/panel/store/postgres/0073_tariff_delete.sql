-- +goose Up
-- A tariff may now be deleted, not only archived: a payment keeps its own tariff_name, so
-- the history stays readable, and the link to a row that is gone becomes none.
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_tariff_id_fkey;
ALTER TABLE payments ADD CONSTRAINT payments_tariff_id_fkey
  FOREIGN KEY (tariff_id) REFERENCES tariffs(id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE payments DROP CONSTRAINT IF EXISTS payments_tariff_id_fkey;
ALTER TABLE payments ADD CONSTRAINT payments_tariff_id_fkey
  FOREIGN KEY (tariff_id) REFERENCES tariffs(id);
