-- name: CreateApproval :one
INSERT INTO approval_requests (action, target_type, target_id, summary, payload, status, requested_by, requested_at, expires_at)
VALUES ($1, $2, $3, $4, $5, 'pending', $6, $7, $8)
RETURNING *;

-- name: GetApproval :one
SELECT * FROM approval_requests WHERE id = $1;

-- name: GetPendingApproval :one
SELECT * FROM approval_requests
WHERE action = $1 AND target_type = $2 AND target_id = $3
  AND requested_by = $4 AND status = 'pending' AND expires_at > $5
ORDER BY id DESC LIMIT 1;

-- name: ListApprovals :many
SELECT * FROM approval_requests
WHERE (sqlc.arg(status)::TEXT = '' OR status = sqlc.arg(status)::TEXT)
  AND (sqlc.arg(requested_by)::BIGINT = 0 OR requested_by = sqlc.arg(requested_by)::BIGINT)
ORDER BY id DESC LIMIT sqlc.arg(lim)::INT;

-- name: DecideApproval :execrows
-- The requester cannot approve their own request; only a pending one can be decided.
UPDATE approval_requests
SET status = sqlc.arg(status)::TEXT, decided_by = sqlc.arg(decided_by)::BIGINT, decided_at = sqlc.arg(decided_at)::BIGINT
WHERE id = sqlc.arg(id)::BIGINT AND status = 'pending' AND requested_by <> sqlc.arg(decided_by)::BIGINT;

-- name: ClaimApproval :execrows
-- The action runs only after this claim: two concurrent executes cannot both perform it.
UPDATE approval_requests
SET status = 'executing', executing_at = sqlc.arg(executing_at)::BIGINT
WHERE id = sqlc.arg(id)::BIGINT AND status = 'approved' AND expires_at > sqlc.arg(executing_at)::BIGINT;

-- name: FinishApproval :execrows
UPDATE approval_requests
SET status = 'used', used_at = sqlc.arg(used_at)::BIGINT, executing_at = NULL
WHERE id = sqlc.arg(id)::BIGINT AND status = 'executing';

-- name: RevertApproval :execrows
-- Put an approval back when its action failed, so the same request can be retried.
UPDATE approval_requests
SET status = 'approved', used_at = NULL, executing_at = NULL
WHERE id = sqlc.arg(id)::BIGINT AND status = 'executing';

-- name: RecoverStaleExecutingApprovals :execrows
-- A panel that stopped mid-action leaves executing behind. After the lease it is safe to
-- retry the action (or expire the request when its window has passed).
UPDATE approval_requests
SET status = CASE WHEN expires_at <= sqlc.arg(now)::BIGINT THEN 'expired' ELSE 'approved' END,
    executing_at = NULL
WHERE status = 'executing' AND executing_at <= sqlc.arg(stale_before)::BIGINT;

-- name: ExpireApprovals :execrows
UPDATE approval_requests SET status = 'expired'
WHERE status IN ('pending', 'approved') AND expires_at <= sqlc.arg(now)::BIGINT;

-- name: CountPendingApprovals :one
SELECT count(*)::BIGINT FROM approval_requests WHERE status = 'pending' AND expires_at > sqlc.arg(now)::BIGINT;
