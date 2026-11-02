-- +goose Up
-- One payment pays out once per rule, not once in all: every enabled rule naming the
-- tariff earns on the same payment, so the payment id alone cannot be unique — the rule's
-- id is what tells two payouts of one payment apart.
ALTER TABLE referral_rewards DROP CONSTRAINT referral_rewards_payment_id_key;
CREATE UNIQUE INDEX referral_rewards_payment_rule ON referral_rewards (payment_id, rule_id);
-- What looks a payment's rewards up takes the composite index's prefix; the old one is
-- dead weight on every write.
DROP INDEX referral_rewards_payment;

-- +goose Down
CREATE INDEX referral_rewards_payment ON referral_rewards(payment_id);
DROP INDEX referral_rewards_payment_rule;
ALTER TABLE referral_rewards ADD CONSTRAINT referral_rewards_payment_id_key UNIQUE (payment_id);
