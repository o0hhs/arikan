-- name: AnalyticsSummary :one
-- The money and the people behind it for one period. Revenue is counted when a payment was
-- applied (not when an invoice was opened), and refunded payments are left out by status.
-- MRR is the monthly equivalent of every active subscription's last ruble payment, so a
-- yearly or quarterly term contributes its share of a month rather than its whole price.
WITH last_payment AS (
  SELECT DISTINCT ON (p.user_id)
    p.user_id, p.amount, p.term_days
  FROM payments p
  JOIN users u ON u.id = p.user_id
  WHERE p.status = 'applied'
    AND p.currency = 'RUB'
    AND p.kind IN ('new', 'renew', 'trial')
    AND u.status = 'active'
    AND (u.expires_at IS NULL OR u.expires_at > sqlc.arg(now)::BIGINT)
  ORDER BY p.user_id, p.applied_at DESC NULLS LAST, p.id DESC
),
totals AS (
  SELECT
    CAST(COALESCE(SUM(CASE WHEN p.applied_at >= sqlc.arg(since)::BIGINT AND p.currency = 'RUB' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS revenue_rub,
    CAST(COALESCE(SUM(CASE WHEN p.applied_at >= sqlc.arg(since)::BIGINT AND p.currency = 'XTR' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS revenue_stars,
    CAST(COUNT(*) FILTER (WHERE p.applied_at >= sqlc.arg(since)::BIGINT) AS BIGINT) AS payments,
    CAST(COUNT(DISTINCT p.user_id) FILTER (WHERE p.applied_at >= sqlc.arg(since)::BIGINT AND p.currency = 'RUB' AND p.user_id IS NOT NULL) AS BIGINT) AS paying_users,
    CAST(COALESCE(SUM(CASE WHEN p.currency = 'RUB' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS total_revenue_rub,
    CAST(COUNT(DISTINCT p.user_id) FILTER (WHERE p.currency = 'RUB' AND p.user_id IS NOT NULL) AS BIGINT) AS total_paying_users
  FROM payments p
  WHERE p.status = 'applied'
),
counted AS (
  SELECT
    CAST(COUNT(*) FILTER (WHERE u.hidden = 0 AND u.status = 'active' AND (u.expires_at IS NULL OR u.expires_at > sqlc.arg(now)::BIGINT)) AS BIGINT) AS active_users,
    CAST(COUNT(*) FILTER (WHERE u.hidden = 0 AND u.created_at >= sqlc.arg(since)::BIGINT) AS BIGINT) AS new_users
  FROM users u
),
mrr AS (
  SELECT CAST(COALESCE(SUM(
    CASE WHEN lp.term_days IS NOT NULL AND lp.term_days > 0
      THEN lp.amount * 30 / lp.term_days
      ELSE lp.amount
    END
  ), 0) AS BIGINT) AS mrr_rub
  FROM last_payment lp
)
SELECT totals.*, counted.*, mrr.mrr_rub FROM totals, counted, mrr;

-- name: AnalyticsRevenueDaily :many
-- One row per day that had an applied payment. The API fills the quiet days with zero.
SELECT
  CAST(to_timestamp(p.applied_at) AT TIME ZONE 'UTC' AS DATE) AS day,
  CAST(COALESCE(SUM(CASE WHEN p.currency = 'RUB' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS rub,
  CAST(COALESCE(SUM(CASE WHEN p.currency = 'XTR' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS stars,
  CAST(COUNT(*) AS BIGINT) AS payments
FROM payments p
WHERE p.status = 'applied' AND p.applied_at >= sqlc.arg(since)::BIGINT
GROUP BY day
ORDER BY day;

-- name: AnalyticsByTariff :many
-- What each tariff sold in the period. A deleted tariff keeps the name it was sold under.
SELECT
  p.tariff_id,
  CAST(p.tariff_name AS TEXT) AS tariff_name,
  CAST(COUNT(*) AS BIGINT) AS payments,
  CAST(COALESCE(SUM(CASE WHEN p.currency = 'RUB' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS rub,
  CAST(COALESCE(SUM(CASE WHEN p.currency = 'XTR' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS stars
FROM payments p
WHERE p.status = 'applied'
  AND p.applied_at >= sqlc.arg(since)::BIGINT
  AND p.kind IN ('new', 'renew', 'trial')
GROUP BY p.tariff_id, p.tariff_name
ORDER BY rub DESC, stars DESC, payments DESC
LIMIT 20;

-- name: AnalyticsByProvider :many
-- Which way the buyers paid. Adapters keep their addon: name, so a marketplace method is
-- told apart from the built-in ones.
SELECT
  CAST(p.provider AS TEXT) AS provider,
  CAST(COUNT(*) AS BIGINT) AS payments,
  CAST(COALESCE(SUM(CASE WHEN p.currency = 'RUB' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS rub,
  CAST(COALESCE(SUM(CASE WHEN p.currency = 'XTR' THEN p.amount ELSE 0 END), 0) AS BIGINT) AS stars
FROM payments p
WHERE p.status = 'applied' AND p.applied_at >= sqlc.arg(since)::BIGINT
GROUP BY p.provider
ORDER BY rub DESC, stars DESC, payments DESC;

-- name: AnalyticsTrials :one
-- Trials started in the period and how many of those accounts paid for something after the
-- trial began. A trial payment itself is not a conversion.
SELECT
  CAST(COUNT(*) AS BIGINT) AS started,
  CAST(COUNT(*) FILTER (WHERE EXISTS (
    SELECT 1 FROM payments p
    WHERE p.tg_id = t.tg_id
      AND p.status = 'applied'
      AND p.kind <> 'trial'
      AND p.applied_at >= t.created_at
  )) AS BIGINT) AS converted
FROM trials t
WHERE t.created_at >= sqlc.arg(since)::BIGINT;

-- name: AnalyticsChurn :one
-- Subscriptions that ended in the period and did not buy anything after their own expiry.
-- A payment made before the expiry belongs to the old term and cannot save the subscription.
WITH expired AS (
  SELECT u.id, u.expires_at
  FROM users u
  WHERE u.hidden = 0
    AND u.expires_at IS NOT NULL
    AND u.expires_at >= sqlc.arg(since)::BIGINT
    AND u.expires_at < sqlc.arg(now)::BIGINT
),
renewed AS (
  SELECT DISTINCT e.id
  FROM expired e
  JOIN payments p ON p.user_id = e.id
  WHERE p.status = 'applied'
    AND p.applied_at >= e.expires_at
    AND p.kind IN ('new', 'renew', 'trial')
)
SELECT
  CAST(COUNT(*) AS BIGINT) AS expired,
  CAST(COUNT(*) FILTER (WHERE r.id IS NULL) AS BIGINT) AS churned
FROM expired e
LEFT JOIN renewed r ON r.id = e.id;

-- name: AnalyticsCohorts :many
-- Paying retention for the last six monthly signup cohorts: of the users who came in a
-- month, how many paid in that month and each of the five after it.
WITH cohorts AS (
  SELECT
    u.id,
    CAST(date_trunc('month', to_timestamp(u.created_at) AT TIME ZONE 'UTC') AS DATE) AS cohort
  FROM users u
  WHERE u.created_at >= sqlc.arg(since)::BIGINT AND u.hidden = 0
),
offsets AS (
  SELECT generate_series(0, 5) AS n
),
paid AS (
  SELECT DISTINCT
    p.user_id,
    CAST(date_trunc('month', to_timestamp(p.applied_at) AT TIME ZONE 'UTC') AS DATE) AS month
  FROM payments p
  WHERE p.status = 'applied'
    AND p.user_id IS NOT NULL
    AND p.applied_at >= sqlc.arg(since)::BIGINT
)
SELECT
  c.cohort,
  CAST(o.n AS BIGINT) AS month_offset,
  CAST(COUNT(DISTINCT c.id) AS BIGINT) AS users,
  CAST(COUNT(DISTINCT p.user_id) AS BIGINT) AS paying
FROM cohorts c
CROSS JOIN offsets o
LEFT JOIN paid p ON p.user_id = c.id
  AND p.month = CAST(c.cohort + make_interval(months => o.n) AS DATE)
GROUP BY c.cohort, o.n
ORDER BY c.cohort, o.n;

-- name: AnalyticsCampaigns :many
-- What the advertising links brought in the period, with refunded sales left out.
SELECT
  c.id,
  CAST(c.tag AS TEXT) AS tag,
  CAST(c.name AS TEXT) AS name,
  CAST(COUNT(e.id) FILTER (WHERE e.kind = 'bought' AND e.reverted_at IS NULL) AS BIGINT) AS bought,
  CAST(COUNT(e.id) FILTER (WHERE e.kind = 'renewed' AND e.reverted_at IS NULL) AS BIGINT) AS renewed,
  CAST(COALESCE(SUM(CASE WHEN e.reverted_at IS NULL AND e.currency = 'RUB' THEN e.amount ELSE 0 END), 0) AS BIGINT) AS rub
FROM campaign_tags c
LEFT JOIN campaign_events e ON e.campaign_id = c.id AND e.created_at >= sqlc.arg(since)::BIGINT
WHERE c.deleted = 0
GROUP BY c.id, c.tag, c.name
ORDER BY rub DESC, bought DESC
LIMIT 20;
