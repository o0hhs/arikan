-- name: GetReferralAccount :one
SELECT * FROM referral_accounts WHERE tg_id = $1;

-- name: TakeReferralAccount :execrows
INSERT INTO referral_accounts (tg_id, code, created_at, updated_at) VALUES ($1, $2, $3, $3) ON CONFLICT (tg_id) DO NOTHING;

-- name: ClaimReferralCode :execrows
UPDATE referral_accounts SET code = sqlc.arg(code), updated_at = sqlc.arg(updated_at) WHERE tg_id = sqlc.arg(tg_id);

-- name: FindReferralAccountByCode :one
SELECT * FROM referral_accounts WHERE code = $1;

-- name: ReferralBalance :one
SELECT balance FROM referral_accounts WHERE tg_id = $1;

-- name: AddReferralBalance :execrows
UPDATE referral_accounts SET balance = balance + sqlc.arg(amount), updated_at = sqlc.arg(updated_at)
WHERE tg_id = sqlc.arg(tg_id) AND balance + sqlc.arg(amount) >= 0;

-- name: TakeReferralBalance :execrows
UPDATE referral_accounts SET balance = balance - sqlc.arg(amount), updated_at = sqlc.arg(updated_at)
WHERE tg_id = sqlc.arg(tg_id) AND balance >= sqlc.arg(amount);

-- name: LinkReferral :execrows
INSERT INTO referrals (referrer_tg_id, referee_tg_id, code, created_at)
VALUES ($1, $2, $3, $4) ON CONFLICT (referee_tg_id) DO NOTHING;

-- name: GetReferralOfReferee :one
SELECT * FROM referrals WHERE referee_tg_id = $1;

-- name: GetReferral :one
SELECT * FROM referrals WHERE id = $1;

-- name: MarkReferralPaid :execrows
UPDATE referrals SET first_paid_at = $2 WHERE id = $1 AND first_paid_at IS NULL;

-- name: ListReferralsOf :many
SELECT * FROM referrals WHERE referrer_tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListReferralsOfBefore :many
SELECT * FROM referrals WHERE referrer_tg_id = sqlc.arg(tg_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: CountReferralsOf :one
SELECT COUNT(*) AS invited,
  CAST(COALESCE(SUM(CASE WHEN first_paid_at IS NOT NULL THEN 1 ELSE 0 END), 0) AS BIGINT) AS paid
FROM referrals WHERE referrer_tg_id = $1;

-- name: ListRecentReferrals :many
SELECT r.*, COALESCE(a.code, '') AS referrer_code,
  CAST(COALESCE(ra.username, '') AS TEXT) AS referrer_username,
  CAST(COALESCE(rb.username, '') AS TEXT) AS referee_username
FROM referrals r
LEFT JOIN referral_accounts a ON a.tg_id = r.referrer_tg_id
LEFT JOIN tg_chats ra ON ra.tg_id = r.referrer_tg_id
LEFT JOIN tg_chats rb ON rb.tg_id = r.referee_tg_id
ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountReferrals :one
SELECT COUNT(*) AS total,
  CAST(COALESCE(SUM(CASE WHEN first_paid_at IS NOT NULL THEN 1 ELSE 0 END), 0) AS BIGINT) AS paid
FROM referrals;

-- name: CountReferralAccounts :one
SELECT COUNT(*) AS accounts,
  CAST(COALESCE(SUM(CASE WHEN balance > 0 THEN balance ELSE 0 END), 0) AS BIGINT) AS owed
FROM referral_accounts;

-- name: CreateReferralReward :one
INSERT INTO referral_rewards
(referrer_tg_id, referral_id, payment_id, rule_id, kind, amount, days, every_n, currency, created_at)
VALUES (sqlc.arg(referrer_tg_id), sqlc.arg(referral_id), sqlc.narg(payment_id), sqlc.arg(rule_id), sqlc.arg(kind), sqlc.arg(amount), sqlc.arg(days), sqlc.arg(every_n), sqlc.arg(currency), sqlc.arg(created_at))
RETURNING *;

-- name: GetReferralReward :one
SELECT * FROM referral_rewards WHERE id = $1;

-- name: ListReferralRewardsByPayment :many
SELECT * FROM referral_rewards WHERE payment_id = $1;

-- name: ListReferralRewardsOf :many
SELECT * FROM referral_rewards WHERE referrer_tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListReferralRewardsOfBefore :many
SELECT * FROM referral_rewards WHERE referrer_tg_id = sqlc.arg(tg_id) AND id < sqlc.arg(before_id)
ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListReferralRewards :many
SELECT r.*, CAST(COALESCE(c.username, '') AS TEXT) AS referrer_username, CAST(COALESCE(c.first_name, '') AS TEXT) AS referrer_name
FROM referral_rewards r LEFT JOIN tg_chats c ON c.tg_id = r.referrer_tg_id
ORDER BY r.id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT) OFFSET CAST(sqlc.arg(row_offset) AS BIGINT);

-- name: CountReferralRewards :one
SELECT COUNT(*) AS n,
  CAST(COALESCE(SUM(CASE WHEN kind = 'percent' AND reverted_at IS NULL THEN amount ELSE 0 END), 0) AS BIGINT) AS rub,
  CAST(COALESCE(SUM(CASE WHEN reverted_at IS NULL THEN days ELSE 0 END), 0) AS BIGINT) AS days
FROM referral_rewards;

-- name: RevertReferralReward :execrows
UPDATE referral_rewards SET reverted_at = $2 WHERE id = $1 AND reverted_at IS NULL;

-- name: RevertReferralRewardsByPayment :execrows
UPDATE referral_rewards SET reverted_at = sqlc.arg(reverted_at) WHERE payment_id = sqlc.arg(payment_id) AND reverted_at IS NULL;

-- name: AddReferralCount :one
INSERT INTO referral_counters (tg_id, rule_id, paid) VALUES ($1, $2, 1)
ON CONFLICT (tg_id, rule_id) DO UPDATE SET paid = referral_counters.paid + 1
RETURNING paid;

-- name: ListReferralCounts :many
SELECT * FROM referral_counters WHERE tg_id = $1;

-- name: CreateReferralSpend :one
INSERT INTO referral_spends (payment_id, tg_id, amount, created_at)
VALUES (sqlc.arg(payment_id), sqlc.arg(tg_id), sqlc.arg(amount), sqlc.arg(created_at)) RETURNING *;

-- name: GetReferralSpendByPayment :one
SELECT * FROM referral_spends WHERE payment_id = $1;

-- name: ListReferralSpendsOf :many
SELECT * FROM referral_spends WHERE tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: RevertReferralSpend :execrows
UPDATE referral_spends SET reverted_at = $2 WHERE payment_id = $1 AND reverted_at IS NULL;

-- name: ReleaseReferralSpend :execrows
UPDATE referral_spends SET reverted_at = $2 WHERE id = $1 AND reverted_at IS NULL;

-- name: ListSpentClosedPayments :many
-- Invoices whose balance hold must go back: nobody paid them, or their money already went
-- back to the buyer (refunded payments are never applied).
SELECT s.id, s.payment_id FROM referral_spends s JOIN payments p ON p.id = s.payment_id
WHERE s.reverted_at IS NULL AND p.status IN ('expired','failed','refunded') ORDER BY s.id;

-- name: ListLateReferralRefunds :many
-- Payments reported paid after their referral hold was given back whose refund has to be
-- finished: the provider refused it, or the panel died in the middle. The reconcile pass
-- retries them; the error is only set by such an attempt.
SELECT id FROM payments WHERE error='ref_late_refund_failed' AND status IN ('pending','expired','paid','failed') ORDER BY id;

-- name: ReferralSpendTotals :one
SELECT COUNT(*) AS n, CAST(COALESCE(SUM(amount), 0) AS BIGINT) AS total
FROM referral_spends WHERE reverted_at IS NULL;

-- name: CreateReferralRewardGrant :one
-- What a days rule owes a referrer: a temporary subscription on the tariff the referee
-- bought. The row is written on the payment's own transaction, so the reward exists the
-- moment the payment does.
INSERT INTO referral_reward_grants
(referrer_tg_id, referral_id, payment_id, rule_id, tariff_id, days, price, status, created_at, updated_at)
VALUES (sqlc.arg(referrer_tg_id), sqlc.arg(referral_id), sqlc.narg(payment_id), sqlc.arg(rule_id), sqlc.narg(tariff_id),
        sqlc.arg(days), sqlc.arg(price), 'queued', sqlc.arg(created_at), sqlc.arg(created_at))
RETURNING *;

-- name: GetReferralRewardGrant :one
SELECT * FROM referral_reward_grants WHERE id = $1;

-- name: ListReferralRewardGrantsByPayment :many
-- Every grant a payment is owed, reverted ones included: a refund must see what it has
-- already taken back as well as what it still has to.
SELECT * FROM referral_reward_grants WHERE payment_id = $1 ORDER BY id;

-- name: ListReferralRewardGrantsOf :many
-- One referrer's grants, newest first: what a screen shows.
SELECT * FROM referral_reward_grants WHERE referrer_tg_id = $1 ORDER BY id DESC LIMIT CAST(sqlc.arg(lim) AS BIGINT);

-- name: ListPendingReferralRewardGrants :many
-- The queue as it runs: every referrer with something owed, dearest reward first and
-- arrival between equals. The advance pass walks it.
SELECT * FROM referral_reward_grants
WHERE status IN ('queued','running') AND reverted_at IS NULL
ORDER BY referrer_tg_id, price DESC, id;

-- name: ListDueReferralRewardGrants :many
-- The rewards whose days have run out: what the advance pass ends first.
SELECT * FROM referral_reward_grants WHERE status = 'running' AND reverted_at IS NULL AND ends_at <= sqlc.arg(now)
ORDER BY ends_at;

-- name: NextQueuedReferralRewardGrant :one
-- The next reward a referrer has waiting: the dearest, then the one that arrived first.
SELECT * FROM referral_reward_grants
WHERE referrer_tg_id = $1 AND status = 'queued' AND reverted_at IS NULL
ORDER BY price DESC, id LIMIT 1;

-- name: StartReferralRewardGrant :execrows
-- Puts a grant's days on the calendar and marks it the running one, with the referrer's
-- own subscription saved in the same statement: the suspension and the grant are one write.
-- A grant that is not queued (already running, done or reverted) changes nothing.
UPDATE referral_reward_grants SET
  status = 'running', run_at = sqlc.arg(run_at), ends_at = sqlc.arg(ends_at), reward_id = sqlc.narg(reward_id),
  before_user_id = sqlc.narg(before_user_id), before_status = sqlc.arg(before_status), before_tariff_id = sqlc.narg(before_tariff_id),
  before_traffic_limit = sqlc.narg(before_traffic_limit), before_device_limit = sqlc.narg(before_device_limit),
  before_speed_limit = sqlc.narg(before_speed_limit), before_reset_strategy = sqlc.arg(before_reset_strategy),
  before_device_mode = sqlc.arg(before_device_mode), before_period_days = sqlc.arg(before_period_days),
  before_period_start = sqlc.arg(before_period_start), before_expires_at = sqlc.narg(before_expires_at),
  before_billing_day = sqlc.narg(before_billing_day), updated_at = sqlc.arg(updated_at)
WHERE id = sqlc.arg(id) AND status = 'queued' AND reverted_at IS NULL;

-- name: SetReferralRewardGrantBeforeUser :execrows
-- The subscription a grant made for a referrer who had none: the grant's own row is where
-- the reward's end learns it must not take the days back. before_status stays '' for it
-- (users.status is never empty), which is what marks the case; this is written by the pass
-- that made the subscription, right after StartReferralRewardGrant.
UPDATE referral_reward_grants SET before_user_id = $1, updated_at = $2 WHERE id = $3;

-- name: FinishReferralRewardGrant :execrows
-- The reward's days are over: it is done, and the subscription it suspended is the
-- caller's to put back (restore, below).
UPDATE referral_reward_grants SET status = 'done', updated_at = sqlc.arg(updated_at)
WHERE id = $1 AND status = 'running' AND reverted_at IS NULL;

-- name: RevertReferralRewardGrant :execrows
-- A refunded payment takes its reward out of the queue or off the streak. Reverted once:
-- the pass and the refund may both ask.
UPDATE referral_reward_grants SET status = 'done', reverted_at = sqlc.arg(reverted_at), updated_at = sqlc.arg(reverted_at)
WHERE id = $1 AND reverted_at IS NULL AND status IN ('queued','running');

-- name: ClearReferralRewardGrantQueue :execrows
-- A referrer's whole queue dropped at once, with any running reward stopped: the refund of
-- one payment may leave later ones waiting on a streak that no longer starts.
UPDATE referral_reward_grants SET status = 'done', reverted_at = sqlc.arg(reverted_at), updated_at = sqlc.arg(reverted_at)
WHERE referrer_tg_id = $1 AND status IN ('queued','running') AND reverted_at IS NULL;

-- name: CountPendingReferralRewardGrants :one
SELECT COUNT(*) AS n FROM referral_reward_grants WHERE status IN ('queued','running') AND reverted_at IS NULL;