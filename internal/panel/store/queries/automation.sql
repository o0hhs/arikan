-- name: ListAutomationRules :many
SELECT * FROM automation_rules ORDER BY id;

-- name: ListEnabledAutomationRules :many
SELECT * FROM automation_rules WHERE enabled = 1 ORDER BY id;

-- name: GetAutomationRule :one
SELECT * FROM automation_rules WHERE id = $1;

-- name: CreateAutomationRule :one
INSERT INTO automation_rules (name, enabled, trigger, conditions, actions, cooldown_seconds, dry_run, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8)
RETURNING *;

-- name: UpdateAutomationRule :one
UPDATE automation_rules
SET name = $2, enabled = $3, trigger = $4, conditions = $5, actions = $6,
    cooldown_seconds = $7, dry_run = $8, updated_at = $9
WHERE id = $1
RETURNING *;

-- name: DeleteAutomationRule :execrows
DELETE FROM automation_rules WHERE id = $1;

-- name: AddAutomationRun :execrows
-- A retried event is not run twice for the same rule.
INSERT INTO automation_runs (rule_id, event_id, event_type, user_id, tg_id, status, dry_run, detail, created_at)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (rule_id, event_id) DO NOTHING;

-- name: ReserveAutomationRun :execrows
-- The row is written before the actions, so a retried event cannot run them twice. A
-- crash between the reservation and the result leaves a "running" row: it is visible in
-- the journal and is never repeated.
INSERT INTO automation_runs (rule_id, event_id, event_type, user_id, tg_id, status, dry_run, detail, created_at)
VALUES ($1, $2, $3, $4, $5, 'skipped', $6, 'running', $7)
ON CONFLICT (rule_id, event_id) DO NOTHING;

-- name: FinishAutomationRun :execrows
UPDATE automation_runs
SET status = $3, detail = $4
WHERE rule_id = $1 AND event_id = $2 AND status = 'skipped' AND detail = 'running';

-- name: MarkAutomationRuleRun :exec
UPDATE automation_rules SET last_run_at = $2, runs_count = runs_count + 1 WHERE id = $1;

-- name: LastAutomationRunForUser :one
SELECT COALESCE(MAX(created_at), 0)::BIGINT AS last_run
FROM automation_runs
WHERE rule_id = $1 AND user_id = $2 AND dry_run = 0;

-- name: ListInactiveUsers :many
-- Active subscriptions that have not connected for a while: the win-back event's source.
-- A subscription that never connected is measured from its creation.
SELECT
  u.id,
  CAST(COALESCE(l.tg_id, 0) AS BIGINT) AS tg_id,
  CAST(COALESCE(u.online_at, u.created_at) AS BIGINT) AS last_seen
FROM users u
LEFT JOIN tg_links l ON l.user_id = u.id
LEFT JOIN tg_chats c ON c.tg_id = l.tg_id
WHERE u.status = 'active' AND u.hidden = 0
  AND (u.expires_at IS NULL OR u.expires_at > sqlc.arg(now)::BIGINT)
  AND COALESCE(u.online_at, u.created_at) < sqlc.arg(before)::BIGINT
  AND COALESCE(c.blocked, 0) = 0
ORDER BY u.id
LIMIT sqlc.arg(lim)::INT;

-- name: ListAutomationRuns :many
SELECT * FROM automation_runs WHERE id < sqlc.arg(before_id)::BIGINT ORDER BY id DESC LIMIT sqlc.arg(lim)::INT;

-- name: ListAutomationRunsOfRule :many
SELECT * FROM automation_runs WHERE rule_id = $1 ORDER BY id DESC LIMIT sqlc.arg(lim)::INT;

-- name: PruneAutomationRuns :execrows
DELETE FROM automation_runs WHERE created_at < sqlc.arg(before)::BIGINT;
