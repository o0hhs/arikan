-- +goose Up
-- An emergency node keeps Telegram reachable for subscriptions that have ended: their
-- profiles list its inbounds alone (a banner of dead entries says the rest), and the sync
-- lets them in there with the speed the node says, while every other node turns them away
-- as it does today.

ALTER TABLE nodes ADD COLUMN emergency BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE nodes ADD COLUMN emergency_speed INT;  -- Mbit/s for ended subscriptions; NULL: the node's own fair share

-- +goose Down
ALTER TABLE nodes DROP COLUMN IF EXISTS emergency_speed;
ALTER TABLE nodes DROP COLUMN IF EXISTS emergency;
