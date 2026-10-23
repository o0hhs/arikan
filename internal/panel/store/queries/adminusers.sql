-- name: GetAdminUser :one
SELECT * FROM admin_users WHERE tg_id = $1;

-- name: ListAdminUsers :many
SELECT * FROM admin_users ORDER BY role, created_at, tg_id;

-- name: UpsertAdminUser :one
INSERT INTO admin_users (tg_id, username, role, created_at, created_by)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (tg_id) DO UPDATE SET username = excluded.username, role = excluded.role
RETURNING *;

-- name: DeleteAdminUser :execrows
DELETE FROM admin_users WHERE tg_id = $1;

-- name: SleepingTrials :many
-- The accounts that took a trial and moved no traffic through it: the ones that could not
-- connect. Their trial subscriptions are what a reactivation mailing extends.
SELECT t.tg_id, t.user_id, COALESCE(c.username, '') AS username, COALESCE(c.first_name, '') AS first_name
FROM trials t
JOIN users u ON u.id = t.user_id
LEFT JOIN tg_chats c ON c.tg_id = t.tg_id
WHERE u.used_up + u.used_down = 0
  AND t.user_id IS NOT NULL
ORDER BY t.created_at, t.tg_id;

-- name: ExtendUser :one
-- A support gift of days: from now when the term already ended, after it when it runs.
-- Unlimited (no term) stays unlimited.
UPDATE users
SET expires_at = CASE WHEN expires_at IS NULL THEN NULL ELSE GREATEST(expires_at, sqlc.arg(now)::BIGINT) + sqlc.arg(days)::BIGINT * 86400 END,
    updated_at = sqlc.arg(now)
WHERE id = sqlc.arg(id)
RETURNING *;

-- name: SetUserDisabled :one
UPDATE users SET status = sqlc.arg(status), updated_at = sqlc.arg(now) WHERE id = sqlc.arg(id) RETURNING *;

-- name: DeleteUserBoundDevices :execrows
DELETE FROM bound_devices WHERE user_id = $1;

-- name: FindUserByTgUsername :one
SELECT u.* FROM users u
JOIN tg_links l ON l.user_id = u.id
JOIN tg_chats c ON c.tg_id = l.tg_id
WHERE lower(c.username) = lower(sqlc.arg(username)) OR lower(c.first_name) = lower(sqlc.arg(username))
ORDER BY u.id LIMIT 1;

-- name: FindUserByPayment :one
SELECT u.* FROM users u JOIN payments p ON p.user_id = u.id WHERE p.id = sqlc.arg(payment_id) ORDER BY u.id LIMIT 1;
