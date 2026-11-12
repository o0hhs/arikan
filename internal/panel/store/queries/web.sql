-- name: GetWebAccount :one
SELECT * FROM web_accounts WHERE id = $1;

-- name: GetWebAccountByEmail :one
SELECT * FROM web_accounts WHERE email = $1;

-- name: CreateWebAccount :one
INSERT INTO web_accounts (email, created_at) VALUES ($1, $2) RETURNING *;

-- name: CountWebLinksOf :one
SELECT COUNT(*) FROM web_links WHERE account_id = $1;

-- name: GetWebLink :one
SELECT user_id, account_id, created_at FROM web_links WHERE user_id = $1;

-- name: ListWebSubs :many
SELECT u.* FROM web_links l JOIN users u ON u.id = l.user_id WHERE l.account_id = $1 ORDER BY l.created_at, u.id;

-- name: LinkWeb :exec
-- A new subscription is linked once. An existing owner is left as it is: proving a token
-- must not move a subscription onto another email.
INSERT INTO web_links (user_id, account_id, created_at) VALUES ($1, $2, $3)
ON CONFLICT (user_id) DO NOTHING;

-- name: CreateWebCode :exec
INSERT INTO web_codes (email, code_hash, expires_at, attempts, created_at) VALUES ($1, $2, $3, 0, $4);

-- name: LatestWebCode :one
SELECT * FROM web_codes WHERE email = $1 ORDER BY id DESC LIMIT 1;

-- name: AddWebCodeAttempt :exec
UPDATE web_codes SET attempts = attempts + 1 WHERE id = $1;

-- name: DeleteWebCodes :exec
DELETE FROM web_codes WHERE email = $1;

-- name: CountRecentWebCodes :one
SELECT COUNT(*) FROM web_codes WHERE email = $1 AND created_at > $2;

-- name: CreateWebSession :exec
INSERT INTO web_sessions (token_hash, account_id, tg_id, user_id, expires_at, created_at)
VALUES ($1, sqlc.narg(account_id), sqlc.narg(tg_id), sqlc.narg(user_id), $2, $3);

-- name: GetWebSession :one
SELECT * FROM web_sessions WHERE token_hash = $1 AND expires_at > $2;

-- name: DeleteWebSession :exec
DELETE FROM web_sessions WHERE token_hash = $1;

-- name: SetWebSessionUser :exec
UPDATE web_sessions SET user_id = $1 WHERE token_hash = $2;

-- name: WasWebClient :one
-- The account bought before, or still holds a subscription: unlinking does not make it new.
SELECT EXISTS (
  SELECT 1 FROM web_links WHERE account_id = $1
  UNION ALL
  SELECT 1 FROM payments WHERE web_account_id = $1 AND status IN ('paid', 'applied', 'refunded')
);

-- name: CountWebPaidPayments :one
SELECT COUNT(*) FROM payments WHERE web_account_id = $1 AND status IN ('paid', 'applied', 'refunded');

-- name: CountPaidByUser :one
SELECT COUNT(*) FROM payments WHERE user_id = $1 AND status IN ('paid', 'applied', 'refunded');

-- name: LockWebInvoices :exec
SELECT pg_advisory_xact_lock(hashtextextended('mikan-web-invoice:' || CAST(sqlc.arg(account_id) AS BIGINT), 0));

-- name: LockTokenInvoices :exec
SELECT pg_advisory_xact_lock(hashtextextended('mikan-token-invoice:' || CAST(sqlc.arg(user_id) AS BIGINT), 0));

-- name: CountRecentWebInvoices :one
SELECT count(*) FROM payments WHERE web_account_id = $1 AND status IN ('pending', 'paid') AND created_at > $2;

-- name: CountRecentTokenInvoices :one
SELECT count(*) FROM payments
WHERE tg_id = 0 AND web_account_id IS NULL AND web AND user_id = $1 AND status IN ('pending', 'paid') AND created_at > $2;

-- name: FindOpenWebPayments :many
SELECT * FROM payments
WHERE web_account_id = sqlc.arg(web_account_id) AND tariff_id = sqlc.arg(tariff_id) AND provider = sqlc.arg(provider) AND kind = sqlc.arg(kind) AND web
  AND user_id IS NOT DISTINCT FROM NULLIF(CAST(sqlc.arg(user_id) AS BIGINT), 0)
  AND (status = 'pending' AND pay_url <> '' AND created_at > sqlc.arg(since)
    OR provider = 'balance' AND status IN ('paid', 'applied') AND created_at > sqlc.arg(repeat_since))
ORDER BY id DESC;

-- name: FindOpenTokenPayments :many
SELECT * FROM payments
WHERE tg_id = 0 AND web_account_id IS NULL AND web AND user_id = sqlc.arg(user_id)
  AND tariff_id = sqlc.arg(tariff_id) AND provider = sqlc.arg(provider) AND kind = 'renew'
  AND (status = 'pending' AND pay_url <> '' AND created_at > sqlc.arg(since)
    OR provider = 'balance' AND status IN ('paid', 'applied') AND created_at > sqlc.arg(repeat_since))
ORDER BY id DESC;
