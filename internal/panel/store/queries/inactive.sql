-- Subscriptions inactive long enough for the admin's removal rules (domain/inactive.go).
-- A term that ended and a last connection, both at or before `before` (now less the rules'
-- days). What keeps one is named beside it: money on the linked account's balance, a payment
-- not applied yet or a top-up waiting for the admin, traffic packages still running.
-- user_id 0 lists every one; any other the one alone, as the removal's own transaction
-- checks it again. The fork has no waiting term and no next tariff, so those guards of
-- upstream's query are not here.

-- name: ListRemovableUsers :many
SELECT u.id, u.name, u.tariff_id,
       CAST(COALESCE(u.expires_at, 0) AS BIGINT) AS expires_at, u.online_at,
       CAST(COALESCE(l.tg_id, 0) AS BIGINT) AS tg_id,
       CAST(COALESCE(b.amount, 0) > 0 AS BOOLEAN) AS has_balance,
       EXISTS (SELECT 1 FROM payments p WHERE p.user_id = u.id AND p.status IN ('pending', 'paid')) AS paying,
       CAST(l.tg_id IS NOT NULL AND EXISTS (SELECT 1 FROM topups t WHERE t.tg_id = l.tg_id AND t.status = 'pending') AS BOOLEAN) AS topping_up,
       EXISTS (SELECT 1 FROM traffic_grants g WHERE g.user_id = u.id AND g.remaining > 0
               AND (g.expires_at IS NULL OR g.expires_at > sqlc.arg(now)::bigint)) AS granted
FROM users u
LEFT JOIN tg_links l ON l.user_id = u.id
LEFT JOIN balances b ON b.tg_id = l.tg_id
WHERE u.expires_at IS NOT NULL AND u.expires_at <= sqlc.arg(before)::bigint
  AND COALESCE(u.online_at, 0) <= sqlc.arg(before)::bigint
  AND (sqlc.arg(user_id)::bigint = 0 OR u.id = sqlc.arg(user_id)::bigint)
ORDER BY u.expires_at, u.id;
