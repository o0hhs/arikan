-- +goose Up
-- Payments whose refund after a released referral hold has to be retried: the reconcile
-- pass finds them by this error, and the partial index keeps that scan (it runs every
-- minute) off the payments table's size.
CREATE INDEX payments_late_referral_refund ON payments(id)
WHERE error='ref_late_refund_failed' AND status IN ('pending','expired','paid','failed');

-- +goose Down
DROP INDEX payments_late_referral_refund;
